// Package workflow runs declarative flows (§9.14–9.17). Every step instance is
// one standard execution turn in its own session (Origin workflow, RunID and
// StepRunID set), so approvals, audit and crash recovery are inherited from
// the turn engine; this package only decides what to dispatch and when.
//
// Run start order is fixed (§5.2): pre-checks → freeze → expand → write the
// run and every step run (pending) → run.created → schedule. A failed
// pre-check writes nothing.
package workflow

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/eternityx/lagent/internal/core/clock"
	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/id"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/eventbus"
	"github.com/eternityx/lagent/internal/infra/queue"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/orch/session"
	"github.com/eternityx/lagent/internal/rules/flowdef"
)

// MaxResultData is the largest structured result a step may hand downstream;
// bigger payloads belong in artifacts (§9.16).
const MaxResultData = 64 << 10

// Config tunes scheduling. Zero values take the defaults below.
type Config struct {
	StepTimeout time.Duration // per step unless the step sets timeout_sec
	FlowTimeout time.Duration // per run unless the definition sets timeout_sec
	MaxParallel int           // fan-out cap unless the definition sets max_parallel
	RetryBase   time.Duration // first retry delay; doubles per attempt
	RetryMax    time.Duration
	SweepEvery  time.Duration
	DefaultRole string
}

func (c *Config) defaults() {
	if c.StepTimeout == 0 {
		c.StepTimeout = 30 * time.Minute
	}
	if c.FlowTimeout == 0 {
		c.FlowTimeout = 6 * time.Hour
	}
	if c.MaxParallel == 0 {
		c.MaxParallel = 8
	}
	if c.RetryBase == 0 {
		c.RetryBase = 10 * time.Second
	}
	if c.RetryMax == 0 {
		c.RetryMax = 10 * time.Minute
	}
	if c.SweepEvery == 0 {
		c.SweepEvery = 5 * time.Second
	}
	if c.DefaultRole == "" {
		c.DefaultRole = "general"
	}
}

// Service implements the workflow run use cases, the advance job and the
// turn hook that records step outcomes.
type Service struct {
	Store    *repo.Store
	Queue    queue.Queue
	Bus      eventbus.Bus
	Sessions *session.Service
	Clock    clock.Clock
	Log      *slog.Logger
	Cfg      Config
}

// Init fills defaults; call once after construction.
func (s *Service) Init() *Service {
	s.Cfg.defaults()
	if s.Clock == nil {
		s.Clock = clock.Real{}
	}
	if s.Log == nil {
		s.Log = slog.Default()
	}
	return s
}

// StartInput describes a new run.
type StartInput struct {
	DefKey string `json:"-"`
	// DefVer pins a version; 0 means the latest published one.
	DefVer int `json:"def_ver,omitempty"`
	// Draft runs the draft (version 0); only allowed for debug runs.
	Draft          bool                       `json:"draft,omitempty"`
	Params         map[string]json.RawMessage `json:"params,omitempty"`
	PermissionMode types.PermissionMode       `json:"permission_mode,omitempty"`
	StandingGrants []types.StandingGrant      `json:"standing_grants,omitempty"`
	ScheduleID     string                     `json:"-"`
	Debug          bool                       `json:"debug,omitempty"`
	TZ             string                     `json:"tz,omitempty"`
}

// Start validates, freezes, expands and records a run, then schedules it.
func (s *Service) Start(ctx context.Context, in StartInput) (*types.WorkflowRun, error) {
	if _, ok := tenant.From(ctx); !ok {
		return nil, errs.New(errs.CodeUnauthorized, "no principal")
	}
	if in.Draft && !in.Debug {
		return nil, errs.Invalid("draft definitions can only be run in debug mode")
	}
	if in.PermissionMode != "" && !in.PermissionMode.Valid() {
		return nil, errs.Invalid("unknown permission_mode %q", in.PermissionMode)
	}
	def, err := s.loadDef(ctx, in.DefKey, in.DefVer, in.Draft)
	if err != nil {
		return nil, err
	}
	if def.Status == types.AssetDisabled {
		return nil, errs.Invalid("workflow %q v%d is disabled", def.Key, def.Ver)
	}
	if def.Status == types.AssetDraft && !in.Debug {
		return nil, errs.Invalid("workflow %q is a draft; publish it or run in debug mode", def.Key)
	}
	fr, err := flowdef.Freeze(def, in.Params, s.Clock.Now(), in.TZ, rand.Int64())
	if err != nil {
		return nil, err
	}
	return s.create(ctx, def, fr, runSpec{
		mode: types.ModeStart, params: in.Params, permission: in.PermissionMode, grants: in.StandingGrants,
		scheduleID: in.ScheduleID, debug: in.Debug,
	})
}

// Continue starts a new run that keeps the origin's frozen values, so every
// step whose fingerprint is unchanged is reused instead of re-executed. The
// origin run is never modified (§9.15).
func (s *Service) Continue(ctx context.Context, runID string) (*types.WorkflowRun, error) {
	origin, def, err := s.originFor(ctx, runID)
	if err != nil {
		return nil, err
	}
	return s.create(ctx, def, origin.Frozen, runSpec{
		mode: types.ModeContinue, origin: origin.ID, params: origin.Params, permission: origin.PermissionMode,
		grants: origin.StandingGrants, debug: origin.Debug,
	})
}

// Rerun starts a new run of the same definition version with values frozen
// again (relative periods and "now" move forward): a full re-execution.
func (s *Service) Rerun(ctx context.Context, runID string) (*types.WorkflowRun, error) {
	origin, def, err := s.originFor(ctx, runID)
	if err != nil {
		return nil, err
	}
	params := origin.Params
	if params == nil {
		// Runs recorded before raw params were kept: resolved values still
		// freeze (periods are accepted as explicit ranges).
		params = origin.Frozen.Params
	}
	fr, err := flowdef.Freeze(def, params, s.Clock.Now(), origin.Frozen.TZ, rand.Int64())
	if err != nil {
		return nil, err
	}
	return s.create(ctx, def, fr, runSpec{
		mode: types.ModeRerun, origin: origin.ID, params: params, permission: origin.PermissionMode,
		grants: origin.StandingGrants, debug: origin.Debug,
	})
}

func (s *Service) originFor(ctx context.Context, runID string) (*types.WorkflowRun, *types.WorkflowDef, error) {
	origin, err := s.getRun(ctx, runID)
	if err != nil {
		return nil, nil, err
	}
	if !origin.Status.Terminal() {
		return nil, nil, errs.Conflict("run %s is still %s; wait for it or cancel it first", origin.ID, origin.Status)
	}
	// Runs pin key+version and versions are never deleted, so a disabled
	// version can still be continued (§9.17).
	def, err := repo.Version(ctx, s.Store.WorkflowDefs, origin.DefKey, origin.DefVer)
	if err != nil {
		return nil, nil, errs.Wrap(errs.CodeInvalid, err, "workflow %q v%d is no longer available", origin.DefKey, origin.DefVer)
	}
	return origin, def, nil
}

func (s *Service) loadDef(ctx context.Context, key string, ver int, draft bool) (*types.WorkflowDef, error) {
	if key == "" {
		return nil, errs.Invalid("workflow key is required")
	}
	var def *types.WorkflowDef
	var err error
	switch {
	case draft:
		def, err = repo.Version(ctx, s.Store.WorkflowDefs, key, 0)
	case ver > 0:
		def, err = repo.Version(ctx, s.Store.WorkflowDefs, key, ver)
	default:
		def, err = repo.LatestPublished(ctx, s.Store.WorkflowDefs, key)
	}
	if errs.Is(err, errs.CodeNotFound) {
		return nil, errs.Invalid("workflow %q has no usable version", key).With("def_key", key)
	}
	return def, err
}

// runSpec carries what differs between start, continue and rerun.
type runSpec struct {
	mode       types.RunMode
	origin     string
	params     map[string]json.RawMessage
	permission types.PermissionMode
	grants     []types.StandingGrant
	scheduleID string
	debug      bool
}

// planned is the fixed graph of a run before anything is written.
type planned struct {
	def   *types.WorkflowDef
	role  *types.Role
	steps []*types.StepRun
}

// plan runs the pre-checks and expands the graph. It writes nothing, so a
// rejected run leaves no record (§9.14).
func (s *Service) plan(ctx context.Context, def *types.WorkflowDef, fr types.Frozen, debug bool) (*planned, error) {
	if err := flowdef.Validate(def); err != nil {
		return nil, err
	}
	runRole, err := s.resolveRole(ctx, s.roleKey(def, ""), debug)
	if err != nil {
		return nil, err
	}
	roles := map[string]*types.Role{}
	for _, st := range def.Steps {
		key := s.roleKey(def, st.Role)
		if _, ok := roles[key]; ok {
			continue
		}
		r, err := s.resolveRole(ctx, key, debug)
		if err != nil {
			return nil, errs.Invalid("step %q: %s", st.ID, err.Error()).With("step_id", st.ID)
		}
		roles[key] = r
	}
	for _, st := range def.Steps {
		for _, src := range st.Sources {
			if !s.sourceExists(ctx, src) {
				return nil, errs.Invalid("step %q needs retrieval source %q, which does not exist", st.ID, src).With("step_id", st.ID)
			}
		}
	}
	inst, err := flowdef.Expand(def, fr)
	if err != nil {
		return nil, err
	}
	out := &planned{def: def, role: runRole}
	for _, in := range inst {
		role := roles[s.roleKey(def, in.Def.Role)]
		pol := in.Def.OnFailure
		if pol == "" {
			pol = types.FailAbort
		}
		retries := in.Def.Retries
		if in.Def.SideEffect {
			retries = 0 // side effects are never repeated automatically (§9.16)
		}
		out.steps = append(out.steps, &types.StepRun{
			StepID: in.StepID, Index: in.Index, Item: in.Item, DependsOn: in.DependsOn,
			StepKey: flowdef.StepKey(def.Key, def.Ver, in.StepID, in.Item, in.Input, role.Key, role.Ver),
			Input:   in.Input, RoleKey: role.Key, RoleVer: role.Ver, Status: types.StepPending,
			MaxRetries: retries, OnFailure: pol, SideEffect: in.Def.SideEffect, TimeoutSec: in.Def.TimeoutSec,
		})
	}
	return out, nil
}

func (s *Service) roleKey(def *types.WorkflowDef, stepRole string) string {
	switch {
	case stepRole != "":
		return stepRole
	case def.Role != "":
		return def.Role
	}
	return s.Cfg.DefaultRole
}

// resolveRole picks the latest published role; debug runs may fall back to a
// draft so templates can be tried together with unpublished roles.
func (s *Service) resolveRole(ctx context.Context, key string, debug bool) (*types.Role, error) {
	r, err := repo.LatestPublished(ctx, s.Store.Roles, key)
	if errs.Is(err, errs.CodeNotFound) && debug {
		r, err = repo.Version(ctx, s.Store.Roles, key, 0)
	}
	if errs.Is(err, errs.CodeNotFound) {
		return nil, errs.Invalid("role %q does not exist", key)
	}
	if err != nil {
		return nil, err
	}
	if r.Status == types.AssetDisabled {
		return nil, errs.Invalid("role %q v%d is disabled", key, r.Ver)
	}
	return r, nil
}

func (s *Service) sourceExists(ctx context.Context, name string) bool {
	for _, cx := range []context.Context{ctx, repo.SystemLayer(ctx)} {
		if _, err := s.Store.Retrievals.FindOne(cx, repo.Filter{"name": name}); err == nil {
			return true
		}
	}
	return false
}

// create writes the step runs, then the run, then announces and schedules it.
// Step runs go first: a crash in between leaves only unreachable step rows,
// never a run with a partial graph.
func (s *Service) create(ctx context.Context, def *types.WorkflowDef, fr types.Frozen, spec runSpec) (*types.WorkflowRun, error) {
	p, _ := tenant.From(ctx)
	pl, err := s.plan(ctx, def, fr, spec.debug)
	if err != nil {
		return nil, err
	}
	now := s.Clock.Now()
	timeout := s.Cfg.FlowTimeout
	if def.TimeoutSec > 0 {
		timeout = time.Duration(def.TimeoutSec) * time.Second
	}
	run := &types.WorkflowRun{
		DefKey: def.Key, DefVer: def.Ver, DefTenant: def.TenantID, RoleKey: pl.role.Key, RoleVer: pl.role.Ver,
		Frozen: fr, Status: types.RunPending, Mode: spec.mode, OriginRunID: spec.origin, ScheduleID: spec.scheduleID,
		StartedBy: p, PermissionMode: spec.permission, StandingGrants: spec.grants, Debug: spec.debug,
		Deadline: types.Ptr(now.Add(timeout)), Params: spec.params,
	}
	run.ID = id.NewWithPrefix(id.Run)
	for _, st := range pl.steps {
		st.ID = id.NewWithPrefix(id.StepRun)
		st.RunID = run.ID
		if err := s.Store.StepRuns.Insert(ctx, st); err != nil {
			return nil, err
		}
	}
	if err := s.Store.Runs.Insert(ctx, run); err != nil {
		return nil, err
	}
	s.emit(ctx, run.TenantID, run.ID, types.EvRunCreated, map[string]any{
		"run_id": run.ID, "def_key": run.DefKey, "def_ver": run.DefVer, "mode": run.Mode, "origin_run_id": run.OriginRunID, "steps": len(pl.steps),
	})
	audit(ctx, s.Store, "workflow.run."+string(spec.mode), run.ID, map[string]any{"def_key": def.Key, "def_ver": def.Ver, "origin_run_id": spec.origin, "schedule_id": spec.scheduleID})
	// A lost enqueue is recovered by the sweeper, which nudges stalled runs.
	if err := s.EnqueueAdvance(ctx, run.TenantID, run.ID); err != nil {
		s.Log.Warn("enqueue advance", "run", run.ID, "err", err)
	}
	return run, nil
}

func (s *Service) emit(ctx context.Context, tenantID, runID string, typ types.EventType, data map[string]any) {
	if s.Bus == nil {
		return
	}
	ev := types.Event{Stream: runID, TenantID: tenantID, Type: typ, TS: s.Clock.Now(), Data: data}
	if _, err := s.Bus.Append(context.WithoutCancel(ctx), ev); err != nil {
		s.Log.Warn("emit run event", "type", typ, "err", err)
	}
}

func audit(ctx context.Context, store *repo.Store, action, target string, detail map[string]any) {
	p, _ := tenant.From(ctx)
	_ = store.Audit.Insert(context.WithoutCancel(ctx), &types.AuditLog{Actor: p, Action: action, Target: target, Detail: detail})
}
