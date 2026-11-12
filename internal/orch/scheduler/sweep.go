package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/queue"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/infra/repo/scope"
	"github.com/eternityx/lagent/internal/orch/session"
	"github.com/eternityx/lagent/internal/orch/turn"
	"github.com/eternityx/lagent/internal/orch/workflow"
	"github.com/eternityx/lagent/internal/rules/schedule"
)

// Run sweeps every SweepEvery until ctx ends (App.OnStart, worker role).
func (s *Service) Run(ctx context.Context) error {
	tk := time.NewTicker(s.SweepEvery)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tk.C:
			if err := s.Sweep(ctx); err != nil && ctx.Err() == nil {
				s.Log.Warn("schedule sweep", "err", err)
			}
		}
	}
}

// Sweep claims and handles every enabled schedule whose NextRunAt has come,
// across tenants. Concurrent sweepers are safe: only one wins each claim.
func (s *Service) Sweep(ctx context.Context) error {
	now := s.Clock.Now()
	all := scope.AllTenants(tenant.SystemContext(ctx, tenant.System))
	due, err := repo.All(all, s.Store.Schedules, repo.Query{Filter: repo.Filter{"enabled": true, "next_run_at": repo.Lte(now)}}, 1000)
	if err != nil {
		return err
	}
	for _, sc := range due {
		s.claimAndFire(ctx, sc, now)
	}
	return nil
}

func (s *Service) claimAndFire(ctx context.Context, sc *types.Schedule, now time.Time) {
	if sc.NextRunAt == nil {
		return
	}
	tctx := tenant.SystemContext(ctx, sc.TenantID)
	planned := *sc.NextRunAt
	after := now
	if planned.After(after) {
		after = planned
	}
	// Next from now (not from planned): after downtime the schedule jumps to
	// its next future slot instead of replaying every missed one.
	next, err := schedule.Next(sc.Cron, sc.TZ, after)
	if err != nil {
		s.Log.Warn("schedule has no next firing", "schedule", sc.ID, "err", err)
		return
	}
	claimed, err := repo.Update(tctx, s.Store.Schedules, sc.ID, func(x *types.Schedule) error {
		if !x.Enabled || x.NextRunAt == nil || !x.NextRunAt.Equal(planned) {
			return errs.New(errs.CodeAlreadyResolved, "claimed elsewhere")
		}
		x.NextRunAt = &next
		return nil
	})
	if err != nil {
		if !errs.Is(err, errs.CodeAlreadyResolved) {
			s.Log.Warn("claim schedule", "schedule", sc.ID, "err", err)
		}
		return
	}
	switch schedule.Check(planned, now) {
	case schedule.Missed:
		s.record(tctx, &types.ScheduleRun{ScheduleID: claimed.ID, PlannedAt: planned, Outcome: types.OutcomeMissed,
			Reason: fmt.Sprintf("planned for %s but only picked up %s later (grace %s); missed firings are not caught up",
				fmtTime(planned, claimed.TZ), now.Sub(planned).Round(time.Second), schedule.Grace)})
	case schedule.Due:
		s.fireDue(tctx, claimed, planned)
	}
}

func (s *Service) fireDue(ctx context.Context, sc *types.Schedule, planned time.Time) {
	act, reason := schedule.Overlap(sc.Overlap, s.active(ctx, sc))
	switch act {
	case schedule.SkipThis:
		s.record(ctx, &types.ScheduleRun{ScheduleID: sc.ID, PlannedAt: planned, Outcome: types.OutcomeSkipped, Reason: reason})
	case schedule.QueueAfter:
		s.enqueueQueued(ctx, sc, planned)
	default:
		s.fire(ctx, sc, planned, "")
	}
}

type firePayload struct {
	ScheduleID string    `json:"schedule_id"`
	PlannedAt  time.Time `json:"planned_at"`
}

func (s *Service) enqueueQueued(ctx context.Context, sc *types.Schedule, planned time.Time) {
	j, err := queue.NewJob(queue.TypeScheduleFire, sc.TenantID, firePayload{ScheduleID: sc.ID, PlannedAt: planned})
	if err != nil {
		return
	}
	j.UniqueKey = fmt.Sprintf("%s:%s:%d", queue.TypeScheduleFire, sc.ID, planned.Unix())
	j.ProcessAt = time.Now().Add(s.QueueRecheck)
	if err := s.Queue.Enqueue(ctx, j); err != nil {
		s.Log.Warn("queue schedule firing", "schedule", sc.ID, "err", err)
	}
}

// HandleFire runs a firing queued behind a still-active predecessor (overlap
// policy "queue"). It waits until the predecessor ends; if the next planned
// firing comes first, this one is recorded as skipped.
func (s *Service) HandleFire(ctx context.Context, j queue.Job) error {
	var p firePayload
	if err := j.Decode(&p); err != nil || p.ScheduleID == "" {
		return queue.SkipRetry(fmt.Errorf("bad schedule payload: %v", err))
	}
	sc, err := s.Store.Schedules.Get(ctx, p.ScheduleID)
	if err != nil {
		return queue.SkipRetry(err)
	}
	if !sc.Enabled {
		s.record(ctx, &types.ScheduleRun{ScheduleID: sc.ID, PlannedAt: p.PlannedAt, Outcome: types.OutcomeSkipped, Reason: "the schedule was paused or disabled while this firing was queued"})
		return nil
	}
	if s.active(ctx, sc) {
		if sc.NextRunAt != nil && !s.Clock.Now().Before(*sc.NextRunAt) {
			s.record(ctx, &types.ScheduleRun{ScheduleID: sc.ID, PlannedAt: p.PlannedAt, Outcome: types.OutcomeSkipped, Reason: "the previous run was still active when the next firing came due"})
			return nil
		}
		s.enqueueQueued(ctx, sc, p.PlannedAt)
		return nil
	}
	s.fire(ctx, sc, p.PlannedAt, "ran after the previous run finished (overlap policy: queue)")
	return nil
}

// active reports whether the schedule's previous session or run is still going.
func (s *Service) active(ctx context.Context, sc *types.Schedule) bool {
	if sc.ActiveRef == "" {
		return false
	}
	if run, err := s.Store.Runs.Get(ctx, sc.ActiveRef); err == nil {
		return !run.Status.Terminal()
	}
	if sess, err := s.Store.Sessions.Get(ctx, sc.ActiveRef); err == nil {
		return sess.ActiveTurnID != "" || sess.Status != types.SessionIdle
	}
	return false
}

// fire starts the target as the schedule's owner and records the outcome.
// The history row is inserted first: its unique (schedule_id, planned_at)
// index turns a redelivered job into a no-op.
func (s *Service) fire(ctx context.Context, sc *types.Schedule, planned time.Time, note string) {
	rec := &types.ScheduleRun{ScheduleID: sc.ID, PlannedAt: planned, Outcome: types.OutcomeRan, Reason: note}
	if err := s.Store.ScheduleRuns.Insert(ctx, rec); err != nil {
		if !errs.Is(err, errs.CodeConflict) {
			s.Log.Warn("record schedule firing", "schedule", sc.ID, "err", err)
		}
		return
	}
	octx := tenant.With(ctx, sc.Owner)
	var ref string
	var defVer int
	var err error
	if sc.Target.WorkflowKey != "" {
		ref, defVer, err = s.fireWorkflow(octx, sc)
		rec.RunID = ref
	} else {
		ref, err = s.firePrompt(octx, sc, planned)
		rec.SessionID = ref
	}
	if err != nil {
		rec.Outcome, rec.Reason = types.OutcomeSkipped, "could not start: "+message(err)
	}
	if _, uerr := repo.Update(ctx, s.Store.ScheduleRuns, rec.ID, func(x *types.ScheduleRun) error {
		x.Outcome, x.Reason, x.RunID, x.SessionID = rec.Outcome, rec.Reason, rec.RunID, rec.SessionID
		return nil
	}); uerr != nil {
		s.Log.Warn("update schedule firing", "schedule", sc.ID, "err", uerr)
	}
	if err != nil {
		return
	}
	if _, err := repo.Update(ctx, s.Store.Schedules, sc.ID, func(x *types.Schedule) error {
		x.ActiveRef, x.LastRunAt = ref, &planned
		if defVer > 0 {
			// The schedule binds to "latest published"; flag the switch so
			// the console can say the template changed since the last run.
			x.DefUpdated = x.LastDefVer != 0 && x.LastDefVer != defVer
			x.LastDefVer = defVer
		}
		return nil
	}); err != nil {
		s.Log.Warn("update schedule after firing", "schedule", sc.ID, "err", err)
	}
}

func (s *Service) fireWorkflow(ctx context.Context, sc *types.Schedule) (string, int, error) {
	def, err := repo.LatestPublished(ctx, s.Store.WorkflowDefs, sc.Target.WorkflowKey)
	if err != nil {
		return "", 0, errs.Invalid("workflow %q has no published version", sc.Target.WorkflowKey)
	}
	run, err := s.Workflows.Start(ctx, workflow.StartInput{
		DefKey: def.Key, DefVer: def.Ver, Params: sc.Target.Params, PermissionMode: sc.PermissionMode,
		StandingGrants: sc.StandingGrants, ScheduleID: sc.ID, TZ: sc.TZ,
	})
	if err != nil {
		return "", 0, err
	}
	return run.ID, def.Ver, nil
}

func (s *Service) firePrompt(ctx context.Context, sc *types.Schedule, planned time.Time) (string, error) {
	sess, err := s.Sessions.Create(ctx, session.CreateInput{
		Title: sc.Name + " · " + fmtTime(planned, sc.TZ), RoleKey: sc.Target.RoleKey, PermissionMode: sc.PermissionMode,
		StandingGrants: sc.StandingGrants, Origin: types.OriginSchedule, Labels: map[string]string{"schedule_id": sc.ID},
	})
	if err != nil {
		return "", err
	}
	if _, err := turn.StartTurn(ctx, s.Store, s.Queue, sess.ID, turn.StartInput{
		Text: sc.Target.Prompt, Author: "schedule:" + sc.ID, Trigger: types.TriggerSchedule,
		IdemKey: fmt.Sprintf("schedule:%s:%d", sc.ID, planned.Unix()),
	}); err != nil {
		return "", err
	}
	return sess.ID, nil
}

func (s *Service) record(ctx context.Context, rec *types.ScheduleRun) {
	if err := s.Store.ScheduleRuns.Insert(ctx, rec); err != nil && !errs.Is(err, errs.CodeConflict) {
		s.Log.Warn("record schedule outcome", "schedule", rec.ScheduleID, "err", err)
	}
}

func message(err error) string {
	var e *errs.Error
	if errors.As(err, &e) {
		return e.Message
	}
	return err.Error()
}
