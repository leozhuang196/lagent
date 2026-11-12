package workflow

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"sort"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/rules/flowdef"
)

// RunView is a run with its step instances (in graph order).
type RunView struct {
	Run   *types.WorkflowRun `json:"run"`
	Steps []*types.StepRun   `json:"steps"`
}

// Get returns a run and its steps.
func (s *Service) Get(ctx context.Context, runID string) (*RunView, error) {
	run, err := s.getRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	steps, err := s.steps(ctx, runID)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(steps, func(i, j int) bool {
		if steps[i].StepID == steps[j].StepID {
			return steps[i].Index < steps[j].Index
		}
		return steps[i].ID < steps[j].ID
	})
	return &RunView{Run: run, Steps: steps}, nil
}

// getRun loads a run the caller may see: its starter, tenant admins and the
// platform itself.
func (s *Service) getRun(ctx context.Context, runID string) (*types.WorkflowRun, error) {
	p, ok := tenant.From(ctx)
	if !ok {
		return nil, errs.New(errs.CodeUnauthorized, "no principal")
	}
	run, err := s.Store.Runs.Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	if !(p.IsAdmin() || p.Kind == tenant.KindSystem || run.StartedBy.ID == p.ID) {
		return nil, errs.New(errs.CodeForbidden, "not your run")
	}
	return run, nil
}

// ListFilter narrows List.
type ListFilter struct {
	DefKey     string
	Status     types.RunStatus
	ScheduleID string
	// All lists every run of the tenant (admins only); otherwise the caller's.
	All bool
}

// List pages runs, newest first.
func (s *Service) List(ctx context.Context, f ListFilter, after string, limit int) (repo.Page[types.WorkflowRun], error) {
	p, _ := tenant.From(ctx)
	q := repo.Filter{}
	if !(f.All && p.IsAdmin()) {
		q["started_by.id"] = p.ID
	}
	if f.DefKey != "" {
		q["def_key"] = f.DefKey
	}
	if f.Status != "" {
		q["status"] = string(f.Status)
	}
	if f.ScheduleID != "" {
		q["schedule_id"] = f.ScheduleID
	}
	return s.Store.Runs.List(ctx, repo.Query{Filter: q, Desc: true, After: after, Limit: limit})
}

// Cancel stops advancing a run: pending and dispatched steps are cancelled,
// running steps have their sessions cancelled. Nothing is rolled back.
// Cancelling a finished run returns it unchanged (idempotent).
func (s *Service) Cancel(ctx context.Context, runID string) (*types.WorkflowRun, error) {
	run, err := s.getRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run.Status.Terminal() {
		return run, nil
	}
	p, _ := tenant.From(ctx)
	run, err = s.cancelRun(ctx, runID, &types.TurnError{Code: "cancelled", Message: "cancelled by " + p.ID})
	if err != nil {
		return nil, err
	}
	audit(ctx, s.Store, "workflow.run.cancel", runID, nil)
	return run, nil
}

func (s *Service) cancelRun(ctx context.Context, runID string, te *types.TurnError) (*types.WorkflowRun, error) {
	now := s.Clock.Now()
	run, err := repo.Update(ctx, s.Store.Runs, runID, func(r *types.WorkflowRun) error {
		if r.Status.Terminal() {
			return errs.New(errs.CodeAlreadyResolved, "run is %s", r.Status)
		}
		r.Status, r.Error, r.EndedAt = types.RunCancelled, te, &now
		return nil
	})
	if errs.Is(err, errs.CodeAlreadyResolved) {
		return run, nil
	}
	if err != nil {
		return nil, err
	}
	steps, err := s.steps(ctx, runID)
	if err != nil {
		return run, err
	}
	stepErr := &types.TurnError{Code: "cancelled", Message: "the run was stopped: " + te.Message}
	for _, st := range steps {
		if st.Status == types.StepPending {
			s.endPending(ctx, run, st.ID, types.StepCancelled, stepErr)
		}
	}
	s.stopActive(ctx, run, steps, stepErr)
	if steps, err = s.steps(ctx, runID); err == nil {
		u := s.usage(ctx, steps)
		if r, err := repo.Update(ctx, s.Store.Runs, runID, func(r *types.WorkflowRun) error { r.Usage = u; return nil }); err == nil {
			run = r
		}
	}
	s.emit(ctx, run.TenantID, run.ID, types.EvRunCompleted, map[string]any{"run_id": run.ID, "status": run.Status, "usage": run.Usage, "error": run.Error})
	return run, nil
}

// DryRunInput previews a run. Give either an inline Def (e.g. an unsaved
// edit) or DefKey/DefVer. With OriginRunID the preview uses that run's
// definition version and frozen values, as Continue would.
type DryRunInput struct {
	Def         *types.WorkflowDef         `json:"def,omitempty"`
	DefKey      string                     `json:"def_key,omitempty"`
	DefVer      int                        `json:"def_ver,omitempty"`
	Draft       bool                       `json:"draft,omitempty"`
	Params      map[string]json.RawMessage `json:"params,omitempty"`
	TZ          string                     `json:"tz,omitempty"`
	OriginRunID string                     `json:"origin_run_id,omitempty"`
}

// DryInstance is one step instance of the preview.
type DryInstance struct {
	StepID     string          `json:"step_id"`
	Index      int             `json:"index"`
	Item       json.RawMessage `json:"item,omitempty"`
	DependsOn  []string        `json:"depends_on,omitempty"`
	Input      string          `json:"input"`
	RoleKey    string          `json:"role_key"`
	RoleVer    int             `json:"role_ver"`
	SideEffect bool            `json:"side_effect,omitempty"`
	// StepKey is the static fingerprint (upstream placeholders unresolved).
	StepKey string `json:"step_key"`
	// WouldReuse: with an origin run, this instance would be reused.
	WouldReuse bool   `json:"would_reuse,omitempty"`
	ReuseFrom  string `json:"reuse_from,omitempty"`
}

// DryRunResult is what a run would look like; nothing is written.
type DryRunResult struct {
	DefKey    string        `json:"def_key"`
	DefVer    int           `json:"def_ver"`
	Frozen    types.Frozen  `json:"frozen"`
	Instances []DryInstance `json:"instances"`
}

// DryRun runs every pre-check, freezes and expands without writing records.
func (s *Service) DryRun(ctx context.Context, in DryRunInput) (*DryRunResult, error) {
	var def *types.WorkflowDef
	var fr types.Frozen
	var origin *types.WorkflowRun
	var err error
	switch {
	case in.OriginRunID != "":
		if origin, err = s.getRun(ctx, in.OriginRunID); err != nil {
			return nil, err
		}
		if def, err = repo.Version(ctx, s.Store.WorkflowDefs, origin.DefKey, origin.DefVer); err != nil {
			return nil, errs.Wrap(errs.CodeInvalid, err, "workflow %q v%d is no longer available", origin.DefKey, origin.DefVer)
		}
		fr = origin.Frozen
	case in.Def != nil:
		def = in.Def
	default:
		if def, err = s.loadDef(ctx, in.DefKey, in.DefVer, in.Draft); err != nil {
			return nil, err
		}
	}
	if origin == nil {
		if fr, err = flowdef.Freeze(def, in.Params, s.Clock.Now(), in.TZ, rand.Int64()); err != nil {
			return nil, err
		}
	}
	// Previews may use draft roles: nothing runs.
	pl, err := s.plan(ctx, def, fr, true)
	if err != nil {
		return nil, err
	}
	out := &DryRunResult{DefKey: def.Key, DefVer: def.Ver, Frozen: fr}
	for _, st := range pl.steps {
		out.Instances = append(out.Instances, DryInstance{
			StepID: st.StepID, Index: st.Index, Item: st.Item, DependsOn: st.DependsOn, Input: st.Input,
			RoleKey: st.RoleKey, RoleVer: st.RoleVer, SideEffect: st.SideEffect, StepKey: st.StepKey,
		})
	}
	if origin != nil {
		s.predictReuse(ctx, origin, pl.steps, out.Instances)
	}
	return out, nil
}

// predictReuse simulates Continue: an instance is reused when its key, with
// upstream results substituted, matches an ok step of the origin. Results of
// steps that would re-run are unknown, so dependents that read them would
// re-run too; dependents that do not read them can still hit.
func (s *Service) predictReuse(ctx context.Context, origin *types.WorkflowRun, steps []*types.StepRun, out []DryInstance) {
	known := map[string][]*types.StepResult{} // step id → results in index order, when all reused
	unknown := map[string]bool{}
	// Instances have no id before they are stored, so track progress by
	// position; StepID alone cannot tell fan-out siblings apart.
	done := make([]bool, len(steps))
	remaining := len(steps)
	for pass := 0; remaining > 0 && pass <= len(steps); pass++ {
		for i, st := range steps {
			if done[i] || !depsDone(st, steps, done) {
				continue
			}
			done[i] = true
			remaining--
			reads := false
			for _, r := range flowdef.Refs(st.Input) {
				if r.Space == "steps" && len(r.Path) > 0 && unknown[r.Path[0]] {
					reads = true
				}
			}
			hit := (*types.StepRun)(nil)
			if !reads && !st.SideEffect {
				resolved, err := flowdef.RenderUpstream(st.Input, known)
				if err == nil {
					key := flowdef.StepKey(origin.DefKey, origin.DefVer, st.StepID, st.Item, resolved, st.RoleKey, st.RoleVer)
					hit = s.findReusable(ctx, origin.ID, key)
				}
			}
			if hit != nil {
				out[i].WouldReuse, out[i].ReuseFrom = true, hit.ID
				if !unknown[st.StepID] {
					known[st.StepID] = append(known[st.StepID], hit.Result)
				}
			} else {
				unknown[st.StepID] = true
				delete(known, st.StepID)
			}
		}
	}
}

// depsDone reports whether every instance a step depends on has been visited,
// i.e. its reuse outcome (and so its results) is already known.
func depsDone(st *types.StepRun, all []*types.StepRun, done []bool) bool {
	for i, x := range all {
		if done[i] {
			continue
		}
		for _, d := range st.DependsOn {
			if x.StepID == d {
				return false
			}
		}
	}
	return true
}

