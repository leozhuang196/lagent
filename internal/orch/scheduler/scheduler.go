// Package scheduler owns recurring triggers (§9.11): schedule CRUD, the
// cross-tenant sweep that claims due schedules, and firing either a prompt
// for a role (a new session) or a workflow run.
//
// Exactly-once firing across replicas comes from the claim: a conditional
// update that moves NextRunAt forward only if it still equals the planned
// time this sweeper saw. A planned time older than schedule.Grace is recorded
// as missed; there is no catch-up after downtime.
package scheduler

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/eternityx/lagent/internal/core/clock"
	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/queue"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/orch/session"
	"github.com/eternityx/lagent/internal/orch/workflow"
	"github.com/eternityx/lagent/internal/rules/flowdef"
	"github.com/eternityx/lagent/internal/rules/schedule"
)

// DefaultTZ is used when a schedule does not name a zone.
const DefaultTZ = "Asia/Shanghai"

// Service implements schedule use cases and the sweep.
type Service struct {
	Store     *repo.Store
	Sessions  *session.Service
	Workflows *workflow.Service
	Queue     queue.Queue
	Clock     clock.Clock
	Log       *slog.Logger
	// SweepEvery is the sweep period (~15s, well inside schedule.Grace).
	SweepEvery time.Duration
	// QueueRecheck is how often a queued firing re-checks its predecessor.
	QueueRecheck time.Duration
}

// Init fills defaults; call once after construction.
func (s *Service) Init() *Service {
	if s.Clock == nil {
		s.Clock = clock.Real{}
	}
	if s.Log == nil {
		s.Log = slog.Default()
	}
	if s.SweepEvery == 0 {
		s.SweepEvery = 15 * time.Second
	}
	if s.QueueRecheck == 0 {
		s.QueueRecheck = 15 * time.Second
	}
	return s
}

// Input is the editable part of a schedule.
type Input struct {
	Name           string                `json:"name"`
	Cron           string                `json:"cron"`
	TZ             string                `json:"tz,omitempty"`
	Overlap        types.OverlapPolicy   `json:"overlap,omitempty"`
	Target         types.ScheduleTarget  `json:"target"`
	PermissionMode types.PermissionMode  `json:"permission_mode"`
	StandingGrants []types.StandingGrant `json:"standing_grants,omitempty"`
	Enabled        *bool                 `json:"enabled,omitempty"`
}

func (s *Service) validate(ctx context.Context, in *Input) error {
	if strings.TrimSpace(in.Name) == "" {
		return errs.Invalid("name is required")
	}
	if in.TZ == "" {
		in.TZ = DefaultTZ
	}
	if err := schedule.Validate(in.Cron, in.TZ); err != nil {
		return err
	}
	// Nobody watches a scheduled run start: the approval policy must be an
	// explicit choice (§9.10).
	if in.PermissionMode == "" {
		return errs.Invalid("permission_mode is required for schedules")
	}
	if !in.PermissionMode.Valid() {
		return errs.Invalid("unknown permission_mode %q", in.PermissionMode)
	}
	switch in.Overlap {
	case "":
		in.Overlap = types.OverlapSkip
	case types.OverlapSkip, types.OverlapQueue, types.OverlapParallel:
	default:
		return errs.Invalid("unknown overlap policy %q", in.Overlap)
	}
	t := in.Target
	hasPrompt, hasFlow := strings.TrimSpace(t.Prompt) != "", t.WorkflowKey != ""
	switch {
	case hasPrompt == hasFlow:
		return errs.Invalid("target needs exactly one of prompt or workflow_key")
	case hasPrompt:
		if len(t.Params) > 0 {
			return errs.Invalid("params only apply to workflow targets")
		}
		if t.RoleKey != "" {
			if _, err := repo.LatestPublished(ctx, s.Store.Roles, t.RoleKey); err != nil {
				return errs.Invalid("role %q has no published version", t.RoleKey)
			}
		}
	default:
		def, err := repo.LatestPublished(ctx, s.Store.WorkflowDefs, t.WorkflowKey)
		if err != nil {
			return errs.Invalid("workflow %q has no published version", t.WorkflowKey)
		}
		// Catch missing or malformed params now rather than at 3 a.m.
		if _, err := flowdef.Freeze(def, t.Params, s.Clock.Now(), in.TZ, 0); err != nil {
			return err
		}
	}
	return nil
}

// Create validates and stores a schedule owned by the caller.
func (s *Service) Create(ctx context.Context, in Input) (*types.Schedule, error) {
	p, ok := tenant.From(ctx)
	if !ok {
		return nil, errs.New(errs.CodeUnauthorized, "no principal")
	}
	if err := s.validate(ctx, &in); err != nil {
		return nil, err
	}
	sc := &types.Schedule{Owner: p, Enabled: true}
	apply(sc, in)
	if err := s.arm(sc); err != nil {
		return nil, err
	}
	if sc.Target.WorkflowKey != "" {
		if def, err := repo.LatestPublished(ctx, s.Store.WorkflowDefs, sc.Target.WorkflowKey); err == nil {
			sc.LastDefVer = def.Ver
		}
	}
	if err := s.Store.Schedules.Insert(ctx, sc); err != nil {
		return nil, err
	}
	audit(ctx, s.Store, "schedule.create", sc.ID, map[string]any{"cron": sc.Cron, "tz": sc.TZ})
	return sc, nil
}

func apply(sc *types.Schedule, in Input) {
	sc.Name, sc.Cron, sc.TZ, sc.Overlap = in.Name, in.Cron, in.TZ, in.Overlap
	sc.Target, sc.PermissionMode, sc.StandingGrants = in.Target, in.PermissionMode, in.StandingGrants
	if in.Enabled != nil {
		sc.Enabled = *in.Enabled
	}
}

// arm sets NextRunAt from now (or clears it when disabled). Arming from now
// is what makes resume and edits skip firings that fell in between.
func (s *Service) arm(sc *types.Schedule) error {
	if !sc.Enabled {
		sc.NextRunAt = nil
		return nil
	}
	next, err := schedule.Next(sc.Cron, sc.TZ, s.Clock.Now())
	if err != nil {
		return err
	}
	sc.NextRunAt = &next
	return nil
}

// Get returns a schedule the caller may manage.
func (s *Service) Get(ctx context.Context, idv string) (*types.Schedule, error) {
	p, ok := tenant.From(ctx)
	if !ok {
		return nil, errs.New(errs.CodeUnauthorized, "no principal")
	}
	sc, err := s.Store.Schedules.Get(ctx, idv)
	if err != nil {
		return nil, err
	}
	if !(p.IsAdmin() || p.Kind == tenant.KindSystem || sc.Owner.ID == p.ID) {
		return nil, errs.New(errs.CodeForbidden, "not your schedule")
	}
	return sc, nil
}

// List pages the caller's schedules (admins may list the tenant's).
func (s *Service) List(ctx context.Context, all bool, after string, limit int) (repo.Page[types.Schedule], error) {
	p, _ := tenant.From(ctx)
	f := repo.Filter{}
	if !(all && p.IsAdmin()) {
		f["owner.id"] = p.ID
	}
	return s.Store.Schedules.List(ctx, repo.Query{Filter: f, Desc: true, After: after, Limit: limit})
}

// Update replaces the editable fields and re-arms the schedule.
func (s *Service) Update(ctx context.Context, idv string, in Input) (*types.Schedule, error) {
	cur, err := s.Get(ctx, idv)
	if err != nil {
		return nil, err
	}
	if err := s.validate(ctx, &in); err != nil {
		return nil, err
	}
	sc, err := repo.Update(ctx, s.Store.Schedules, cur.ID, func(x *types.Schedule) error {
		oldFlow := x.Target.WorkflowKey
		apply(x, in)
		if x.Target.WorkflowKey != oldFlow {
			x.LastDefVer, x.DefUpdated = 0, false
		}
		return s.arm(x)
	})
	if err != nil {
		return nil, err
	}
	audit(ctx, s.Store, "schedule.update", sc.ID, nil)
	return sc, nil
}

// Delete disables a schedule; its history stays (DELETE = disable).
func (s *Service) Delete(ctx context.Context, idv string) (*types.Schedule, error) {
	return s.setEnabled(ctx, idv, false, "schedule.delete")
}

// Pause stops firing until Resume.
func (s *Service) Pause(ctx context.Context, idv string) (*types.Schedule, error) {
	return s.setEnabled(ctx, idv, false, "schedule.pause")
}

// Resume re-arms from now; firings planned while paused are not replayed.
func (s *Service) Resume(ctx context.Context, idv string) (*types.Schedule, error) {
	return s.setEnabled(ctx, idv, true, "schedule.resume")
}

func (s *Service) setEnabled(ctx context.Context, idv string, on bool, action string) (*types.Schedule, error) {
	if _, err := s.Get(ctx, idv); err != nil {
		return nil, err
	}
	sc, err := repo.Update(ctx, s.Store.Schedules, idv, func(x *types.Schedule) error {
		if x.Enabled == on && (!on || x.NextRunAt != nil) {
			return errs.New(errs.CodeAlreadyResolved, "unchanged")
		}
		x.Enabled = on
		return s.arm(x)
	})
	if errs.Is(err, errs.CodeAlreadyResolved) {
		return sc, nil // idempotent
	}
	if err != nil {
		return nil, err
	}
	audit(ctx, s.Store, action, idv, nil)
	return sc, nil
}

// Upcoming previews the next n firings (n ≤ 50).
func (s *Service) Upcoming(ctx context.Context, idv string, n int) ([]time.Time, error) {
	sc, err := s.Get(ctx, idv)
	if err != nil {
		return nil, err
	}
	if n <= 0 {
		n = 5
	}
	if n > 50 {
		n = 50
	}
	return schedule.Upcoming(sc.Cron, sc.TZ, s.Clock.Now(), n)
}

// Runs pages a schedule's firing history, newest first.
func (s *Service) Runs(ctx context.Context, idv, after string, limit int) (repo.Page[types.ScheduleRun], error) {
	if _, err := s.Get(ctx, idv); err != nil {
		return repo.Page[types.ScheduleRun]{}, err
	}
	return s.Store.ScheduleRuns.List(ctx, repo.Query{Filter: repo.Filter{"schedule_id": idv}, Desc: true, After: after, Limit: limit})
}

func audit(ctx context.Context, store *repo.Store, action, target string, detail map[string]any) {
	p, _ := tenant.From(ctx)
	_ = store.Audit.Insert(context.WithoutCancel(ctx), &types.AuditLog{Actor: p, Action: action, Target: target, Detail: detail})
}

func fmtTime(t time.Time, tz string) string {
	if loc, err := time.LoadLocation(tz); err == nil {
		t = t.In(loc)
	}
	return t.Format("2006-01-02 15:04 MST")
}

