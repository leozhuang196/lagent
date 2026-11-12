package workflow

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/queue"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/orch/session"
	"github.com/eternityx/lagent/internal/orch/turn"
	"github.com/eternityx/lagent/internal/rules/flowdef"
)

type advancePayload struct {
	RunID string `json:"run_id"`
}

// EnqueueAdvance schedules one scheduling pass of a run. The unique key
// collapses bursts (many steps ending at once) into a single pending job.
func (s *Service) EnqueueAdvance(ctx context.Context, tenantID, runID string) error {
	return s.enqueueAdvance(ctx, tenantID, runID, "", time.Time{})
}

func (s *Service) enqueueAdvance(ctx context.Context, tenantID, runID, suffix string, at time.Time) error {
	j, err := queue.NewJob(queue.TypeWorkflowAdvance, tenantID, advancePayload{RunID: runID})
	if err != nil {
		return err
	}
	j.UniqueKey = queue.TypeWorkflowAdvance + ":" + runID + suffix
	j.ProcessAt = at
	j.MaxRetry = 5
	j.Timeout = 2 * time.Minute
	return s.Queue.Enqueue(ctx, j)
}

// HandleAdvance is the queue handler of queue.TypeWorkflowAdvance.
func (s *Service) HandleAdvance(ctx context.Context, j queue.Job) error {
	var p advancePayload
	if err := j.Decode(&p); err != nil || p.RunID == "" {
		return queue.SkipRetry(fmt.Errorf("bad advance payload: %v", err))
	}
	err := s.Advance(ctx, p.RunID)
	if errs.Is(err, errs.CodeNotFound) {
		return queue.SkipRetry(err)
	}
	return err
}

// Advance applies flowdef.Advance until nothing changes: skip, cancel,
// reuse and dispatch steps, and complete the run when every instance is
// terminal. Safe to run concurrently and repeatedly: every transition is a
// conditional update from an expected state.
func (s *Service) Advance(ctx context.Context, runID string) error {
	for round := 0; round < 20; round++ {
		run, err := s.Store.Runs.Get(ctx, runID)
		if err != nil {
			return err
		}
		if run.Status.Terminal() {
			return nil
		}
		now := s.Clock.Now()
		if run.Deadline != nil && now.After(*run.Deadline) {
			_, err := s.cancelRun(ctx, run.ID, &types.TurnError{Code: "timeout", Message: "the run exceeded its overall time limit and was stopped"})
			return err
		}
		if run.Status == types.RunPending {
			run, err = repo.Update(ctx, s.Store.Runs, run.ID, func(r *types.WorkflowRun) error {
				if r.Status != types.RunPending {
					return errs.New(errs.CodeAlreadyResolved, "run is %s", r.Status)
				}
				r.Status = types.RunRunning
				return nil
			})
			if err != nil && !errs.Is(err, errs.CodeAlreadyResolved) {
				return err
			}
		}
		steps, err := s.steps(ctx, runID)
		if err != nil {
			return err
		}
		s.reconcileStarted(ctx, steps)
		plan := flowdef.Advance(steps, s.maxParallel(ctx, run))
		changed := false
		byID := map[string]*types.StepRun{}
		for _, st := range steps {
			byID[st.ID] = st
		}
		for _, sid := range plan.Skip {
			st := byID[sid]
			msg := fmt.Sprintf("skipped because an upstream step (%s) failed or was skipped", strings.Join(st.DependsOn, ", "))
			if s.endPending(ctx, run, sid, types.StepSkipped, &types.TurnError{Code: "skipped", Message: msg}) {
				changed = true
			}
		}
		if plan.Abort {
			reason := abortReason(steps)
			for _, st := range steps {
				if st.Status == types.StepPending {
					if s.endPending(ctx, run, st.ID, types.StepCancelled, &types.TurnError{Code: "aborted", Message: reason}) {
						changed = true
					}
				}
			}
			// Abort stops in-flight siblings too; their results would be thrown
			// away and they may be waiting for a person who no longer needs to act.
			if s.stopActive(ctx, run, steps, &types.TurnError{Code: "aborted", Message: reason}) {
				changed = true
			}
		} else {
			for _, sid := range plan.Dispatch {
				st := byID[sid]
				if st.NotBefore != nil && now.Before(*st.NotBefore) {
					continue // retry backoff; woken by a delayed job or the sweeper
				}
				reused, err := s.dispatch(ctx, run, st, steps)
				if err != nil {
					return err
				}
				if reused {
					changed = true // a reused result may unblock dependents right away
				}
			}
		}
		if changed {
			continue
		}
		if plan.Done {
			return s.complete(ctx, run.ID)
		}
		return nil
	}
	return nil
}

func abortReason(steps []*types.StepRun) string {
	for _, st := range steps {
		if st.Status == types.StepFailed && (st.OnFailure == "" || st.OnFailure == types.FailAbort) {
			return fmt.Sprintf("cancelled because step %s failed and its policy is abort", label(st))
		}
	}
	return "cancelled because the run was aborted"
}

func label(st *types.StepRun) string {
	if len(st.Item) > 0 {
		return fmt.Sprintf("%s[%d]", st.StepID, st.Index)
	}
	return st.StepID
}

func (s *Service) maxParallel(ctx context.Context, run *types.WorkflowRun) int {
	if def, err := repo.Version(ctx, s.Store.WorkflowDefs, run.DefKey, run.DefVer); err == nil && def.MaxParallel > 0 {
		return def.MaxParallel
	}
	return s.Cfg.MaxParallel
}

func (s *Service) steps(ctx context.Context, runID string) ([]*types.StepRun, error) {
	steps, err := repo.All(ctx, s.Store.StepRuns, repo.Query{Filter: repo.Filter{"run_id": runID}}, 0)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].ID < steps[j].ID })
	return steps, nil
}

// endPending moves a pending step to a terminal state without running it.
func (s *Service) endPending(ctx context.Context, run *types.WorkflowRun, stepID string, to types.StepStatus, te *types.TurnError) bool {
	now := s.Clock.Now()
	st, err := repo.Update(ctx, s.Store.StepRuns, stepID, func(x *types.StepRun) error {
		if x.Status != types.StepPending {
			return errs.New(errs.CodeAlreadyResolved, "step is %s", x.Status)
		}
		x.Status, x.Error, x.EndedAt, x.NotBefore = to, te, &now, nil
		return nil
	})
	if err != nil {
		return false
	}
	s.emitStep(ctx, run, st, types.EvStepCompleted)
	return true
}

// stopActive cancels dispatched and running steps and their sessions.
func (s *Service) stopActive(ctx context.Context, run *types.WorkflowRun, steps []*types.StepRun, te *types.TurnError) bool {
	changed := false
	now := s.Clock.Now()
	for _, st := range steps {
		if st.Status != types.StepDispatched && st.Status != types.StepRunning {
			continue
		}
		cur, err := repo.Update(ctx, s.Store.StepRuns, st.ID, func(x *types.StepRun) error {
			if x.Status != types.StepDispatched && x.Status != types.StepRunning {
				return errs.New(errs.CodeAlreadyResolved, "step is %s", x.Status)
			}
			x.Status, x.Error, x.EndedAt = types.StepCancelled, te, &now
			return nil
		})
		if err != nil {
			continue
		}
		changed = true
		s.cancelSession(ctx, cur.TenantID, cur.SessionID)
		s.emitStep(ctx, run, cur, types.EvStepCompleted)
	}
	return changed
}

func (s *Service) cancelSession(ctx context.Context, tenantID, sessionID string) {
	if sessionID == "" || s.Sessions == nil {
		return
	}
	if _, err := s.Sessions.Cancel(tenant.SystemContext(context.WithoutCancel(ctx), tenantID), sessionID); err != nil {
		s.Log.Warn("cancel step session", "session", sessionID, "err", err)
	}
}

// reconcileStarted moves dispatched steps whose turn has begun to running.
// There is no turn-start hook, so this runs on every pass and in the sweeper;
// OnPending covers turns that suspend before either looks.
func (s *Service) reconcileStarted(ctx context.Context, steps []*types.StepRun) {
	for _, st := range steps {
		if st.Status != types.StepDispatched || st.TurnID == "" {
			continue
		}
		t, err := s.Store.Turns.Get(ctx, st.TurnID)
		if err != nil || t.Status == types.TurnQueued {
			continue
		}
		if cur, ok := s.markRunning(ctx, st.ID, st.SessionID, t.StartedAt); ok {
			*st = *cur
		}
	}
}

func (s *Service) markRunning(ctx context.Context, stepID, sessionID string, at *time.Time) (*types.StepRun, bool) {
	if at == nil {
		at = types.Ptr(s.Clock.Now())
	}
	cur, err := repo.Update(ctx, s.Store.StepRuns, stepID, func(x *types.StepRun) error {
		if x.Status != types.StepDispatched || x.SessionID != sessionID {
			return errs.New(errs.CodeAlreadyResolved, "step is %s", x.Status)
		}
		x.Status, x.StartedAt = types.StepRunning, at
		return nil
	})
	if err != nil {
		return nil, false
	}
	if run, err := s.Store.Runs.Get(ctx, cur.RunID); err == nil {
		s.emitStep(ctx, run, cur, types.EvStepStarted)
	}
	return cur, true
}

// dispatch resolves the step's input against upstream results, fingerprints
// it, reuses an identical result of the origin run when allowed, and
// otherwise claims the step (pending → dispatched) and starts its turn.
// It reports whether the step was reused.
func (s *Service) dispatch(ctx context.Context, run *types.WorkflowRun, st *types.StepRun, all []*types.StepRun) (bool, error) {
	resolved, err := flowdef.RenderUpstream(st.Input, upstreamResults(st, all))
	if err != nil {
		s.failPending(ctx, run, st.ID, &types.TurnError{Code: "invalid_input", Message: "cannot build the step instruction: " + err.Error()})
		return false, nil
	}
	key := flowdef.StepKey(run.DefKey, run.DefVer, st.StepID, st.Item, resolved, st.RoleKey, st.RoleVer)
	// Only Continue reuses: it keeps the origin's frozen values, so equal
	// fingerprints mean equal work. Rerun freezes again on purpose (§9.7).
	if run.Mode == types.ModeContinue && run.OriginRunID != "" && !st.SideEffect {
		if prev := s.findReusable(ctx, run.OriginRunID, key); prev != nil {
			return s.reuse(ctx, run, st.ID, key, resolved, prev), nil
		}
	}
	now := s.Clock.Now()
	claimed, err := repo.Update(ctx, s.Store.StepRuns, st.ID, func(x *types.StepRun) error {
		if x.Status != types.StepPending {
			return errs.New(errs.CodeAlreadyResolved, "step is %s", x.Status)
		}
		x.Status, x.StepKey, x.Input = types.StepDispatched, key, resolved
		x.Attempts++
		x.DispatchedAt, x.NotBefore, x.StartedAt, x.EndedAt = &now, nil, nil, nil
		x.SessionID, x.TurnID, x.Result = "", "", nil
		return nil
	})
	if errs.Is(err, errs.CodeAlreadyResolved) {
		return false, nil // another pass got it
	}
	if err != nil {
		return false, err
	}
	// Steps act as the person who started the run: their permission cap and
	// ownership of approvals carry over (§9.14).
	pctx := tenant.With(ctx, run.StartedBy)
	roleVer := claimed.RoleVer
	title := fmt.Sprintf("%s · %s", run.DefKey, label(claimed))
	sess, err := s.Sessions.Create(pctx, session.CreateInput{
		Title: title, RoleKey: claimed.RoleKey, RoleVer: &roleVer, PermissionMode: run.PermissionMode,
		StandingGrants: run.StandingGrants, Origin: types.OriginWorkflow, Debug: run.Debug,
		RunID: run.ID, StepRunID: claimed.ID,
		Labels: map[string]string{"run_id": run.ID, "step_id": claimed.StepID},
	})
	if err != nil {
		s.finishAttempt(ctx, claimed.ID, "", nil, &types.TurnError{Code: "dispatch_failed", Message: "cannot create the step session: " + errMessage(err)}, false)
		return false, nil
	}
	// The session id is recorded before the turn starts, so the end-of-turn
	// hook always finds it and can ignore stale attempts.
	if _, err := repo.Update(ctx, s.Store.StepRuns, claimed.ID, func(x *types.StepRun) error {
		if x.Status != types.StepDispatched || x.SessionID != "" {
			return errs.New(errs.CodeAlreadyResolved, "step is %s", x.Status)
		}
		x.SessionID = sess.ID
		return nil
	}); err != nil {
		return false, nil // cancelled meanwhile
	}
	t, err := turn.StartTurn(pctx, s.Store, s.Queue, sess.ID, turn.StartInput{
		Text: resolved, Author: "workflow:" + run.ID, Trigger: types.TriggerStep, Debug: run.Debug,
		IdemKey: fmt.Sprintf("step:%s:%d", claimed.ID, claimed.Attempts),
	})
	if err != nil {
		s.finishAttempt(ctx, claimed.ID, sess.ID, nil, &types.TurnError{Code: "dispatch_failed", Message: "cannot start the step turn: " + errMessage(err)}, true)
		return false, nil
	}
	cur, err := repo.Update(ctx, s.Store.StepRuns, claimed.ID, func(x *types.StepRun) error {
		if x.SessionID != sess.ID {
			return errs.New(errs.CodeAlreadyResolved, "stale attempt")
		}
		x.TurnID = t.ID
		return nil
	})
	if err == nil {
		s.emitStep(ctx, run, cur, types.EvStepStarted)
	}
	return false, nil
}

func errMessage(err error) string {
	var e *errs.Error
	if errors.As(err, &e) {
		return e.Message
	}
	return err.Error()
}

// upstreamResults collects the results of every instance of each dependency
// in index order; failed instances contribute nil and are left out.
func upstreamResults(st *types.StepRun, all []*types.StepRun) map[string][]*types.StepResult {
	out := map[string][]*types.StepResult{}
	deps := map[string]bool{}
	for _, d := range st.DependsOn {
		deps[d] = true
	}
	var inst []*types.StepRun
	for _, x := range all {
		if deps[x.StepID] {
			inst = append(inst, x)
		}
	}
	sort.SliceStable(inst, func(i, j int) bool { return inst[i].Index < inst[j].Index })
	for _, x := range inst {
		var r *types.StepResult
		if x.Status.Ok() {
			r = x.Result
		}
		out[x.StepID] = append(out[x.StepID], r)
	}
	return out
}

func (s *Service) findReusable(ctx context.Context, originRunID, key string) *types.StepRun {
	prev, err := s.Store.StepRuns.FindOne(ctx, repo.Filter{
		"run_id": originRunID, "step_key": key, "status": repo.In(string(types.StepSucceeded), string(types.StepReused)),
	})
	if err != nil || prev.SideEffect || prev.Result == nil {
		return nil
	}
	return prev
}

// reuse copies the result (and artifact references) of an identical step of
// the origin run instead of executing it again (§9.15).
func (s *Service) reuse(ctx context.Context, run *types.WorkflowRun, stepID, key, resolved string, prev *types.StepRun) bool {
	now := s.Clock.Now()
	res := *prev.Result
	res.Artifacts = append([]string(nil), prev.Result.Artifacts...)
	st, err := repo.Update(ctx, s.Store.StepRuns, stepID, func(x *types.StepRun) error {
		if x.Status != types.StepPending {
			return errs.New(errs.CodeAlreadyResolved, "step is %s", x.Status)
		}
		x.Status, x.StepKey, x.Input, x.Result, x.ReusedFrom = types.StepReused, key, resolved, &res, prev.ID
		x.Error, x.NotBefore, x.StartedAt, x.EndedAt = nil, nil, &now, &now
		return nil
	})
	if err != nil {
		return false
	}
	s.emitStep(ctx, run, st, types.EvStepReused)
	return true
}

// failPending fails a step that cannot even be dispatched (no retry: the
// cause is in the definition, not transient).
func (s *Service) failPending(ctx context.Context, run *types.WorkflowRun, stepID string, te *types.TurnError) {
	s.endPending(ctx, run, stepID, types.StepFailed, te)
}

// complete judges a finished run, sums token usage of every attempt and
// emits run.completed. Exactly one caller wins the conditional update.
func (s *Service) complete(ctx context.Context, runID string) error {
	steps, err := s.steps(ctx, runID)
	if err != nil {
		return err
	}
	for _, st := range steps {
		if !st.Status.Terminal() {
			return nil
		}
	}
	status := flowdef.FinalStatus(steps)
	usage := s.usage(ctx, steps)
	var te *types.TurnError
	if status != types.RunSucceeded {
		te = &types.TurnError{Code: string(status), Message: failureSummary(steps)}
	}
	now := s.Clock.Now()
	run, err := repo.Update(ctx, s.Store.Runs, runID, func(r *types.WorkflowRun) error {
		if r.Status.Terminal() {
			return errs.New(errs.CodeAlreadyResolved, "run is %s", r.Status)
		}
		r.Status, r.Usage, r.Error, r.EndedAt = status, usage, te, &now
		return nil
	})
	if errs.Is(err, errs.CodeAlreadyResolved) {
		return nil
	}
	if err != nil {
		return err
	}
	s.emit(ctx, run.TenantID, run.ID, types.EvRunCompleted, map[string]any{"run_id": run.ID, "status": run.Status, "usage": run.Usage, "error": run.Error})
	return nil
}

// usage sums every turn that ran for the run's steps, retries included.
func (s *Service) usage(ctx context.Context, steps []*types.StepRun) types.Usage {
	var u types.Usage
	for _, st := range steps {
		if st.Attempts == 0 {
			continue
		}
		turns, err := repo.All(ctx, s.Store.Turns, repo.Query{Filter: repo.Filter{"step_run_id": st.ID}}, 0)
		if err != nil {
			continue
		}
		for _, t := range turns {
			u.Add(t.Usage)
		}
	}
	return u
}

func failureSummary(steps []*types.StepRun) string {
	var parts []string
	for _, st := range steps {
		if st.Status == types.StepFailed {
			msg := "failed"
			if st.Error != nil {
				msg = st.Error.Message
			}
			parts = append(parts, fmt.Sprintf("step %s: %s", label(st), msg))
		}
	}
	if len(parts) == 0 {
		for _, st := range steps {
			if st.Status == types.StepCancelled || st.Status == types.StepSkipped {
				parts = append(parts, fmt.Sprintf("step %s was %s", label(st), st.Status))
			}
		}
	}
	if len(parts) > 5 {
		parts = append(parts[:5], fmt.Sprintf("and %d more", len(parts)-5))
	}
	return strings.Join(parts, "; ")
}

func (s *Service) emitStep(ctx context.Context, run *types.WorkflowRun, st *types.StepRun, typ types.EventType) {
	data := map[string]any{
		"step_run_id": st.ID, "step_id": st.StepID, "index": st.Index, "status": st.Status,
		"attempt": st.Attempts, "session_id": st.SessionID,
	}
	if st.Error != nil {
		data["error"] = st.Error
	}
	if st.ReusedFrom != "" {
		data["reused_from"] = st.ReusedFrom
	}
	if st.NotBefore != nil {
		data["retry_at"] = st.NotBefore
	}
	if st.Result != nil {
		data["summary"] = st.Result.Summary
	}
	s.emit(ctx, run.TenantID, run.ID, typ, data)
}
