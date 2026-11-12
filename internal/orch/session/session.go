// Package session holds the use cases every entry point shares (web, Feishu,
// internal API, schedules): create a session, submit a message, resolve a
// pending item, cancel. Entry points only translate protocols.
package session

import (
	"context"
	"log/slog"
	"strings"

	"github.com/eternityx/lagent/internal/core/clock"
	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/coord"
	"github.com/eternityx/lagent/internal/infra/eventbus"
	"github.com/eternityx/lagent/internal/infra/queue"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/orch/turn"
	"github.com/eternityx/lagent/internal/rules/approval"
	"github.com/eternityx/lagent/internal/rules/permission"
	"github.com/eternityx/lagent/internal/rules/sessionfsm"
)

// Service implements the session use cases.
type Service struct {
	Store       *repo.Store
	Queue       queue.Queue
	Bus         eventbus.Bus
	Signal      coord.Signal
	Clock       clock.Clock
	Log         *slog.Logger
	DefaultRole string
}

// CreateInput describes a new session.
type CreateInput struct {
	Title          string                `json:"title,omitempty"`
	RoleKey        string                `json:"role,omitempty"`
	RoleVer        *int                  `json:"role_version,omitempty"` // nil = latest published; 0 = draft (debug only)
	PermissionMode types.PermissionMode  `json:"permission_mode,omitempty"`
	MemorySpaces   []string              `json:"memory_spaces,omitempty"`
	Channels       []types.Channel       `json:"channels,omitempty"`
	StandingGrants []types.StandingGrant `json:"standing_grants,omitempty"`
	Labels         map[string]string     `json:"labels,omitempty"`
	Origin         types.Origin          `json:"-"`
	Debug          bool                  `json:"debug,omitempty"`
	ParentTurnID   string                `json:"-"`
	RunID          string                `json:"-"`
	StepRunID      string                `json:"-"`
	ToolDeny       []string              `json:"-"`
}

// Create resolves the role, fixes its version for the session's lifetime and
// picks the permission mode (clamped by the caller's cap; debug sessions always ask).
func (s *Service) Create(ctx context.Context, in CreateInput) (*types.Session, error) {
	p, ok := tenant.From(ctx)
	if !ok {
		return nil, errs.New(errs.CodeUnauthorized, "no principal")
	}
	key := in.RoleKey
	if key == "" {
		key = s.defaultRole(ctx)
	}
	var role *types.Role
	var err error
	switch {
	case in.RoleVer == nil:
		role, err = repo.LatestPublished(ctx, s.Store.Roles, key)
	case *in.RoleVer == 0 && !in.Debug:
		return nil, errs.Invalid("draft roles can only be used in debug sessions")
	default:
		role, err = repo.Version(ctx, s.Store.Roles, key, *in.RoleVer)
	}
	if err != nil {
		return nil, errs.Wrap(errs.CodeInvalid, err, "role %q is not available", key)
	}
	if role.Status == types.AssetDisabled {
		return nil, errs.Invalid("role %q v%d is disabled", key, role.Ver)
	}
	mode := in.PermissionMode
	if mode == "" {
		mode = role.DefaultPermissionMode
	}
	if !mode.Valid() {
		mode = types.ModeAskAll
	}
	mode = permission.ClampMode(mode, types.PermissionMode(p.MaxPermissionMode))
	if in.Debug {
		mode = types.ModeAskAll
	}
	channels := in.Channels
	if _, ok := hasKind(channels, types.ChannelInbox); !ok {
		channels = append(channels, types.Channel{Kind: types.ChannelInbox})
	}
	origin := in.Origin
	if origin == "" {
		origin = types.OriginAPI
	}
	sess := &types.Session{
		Title: in.Title, RoleKey: role.Key, RoleVer: role.Ver, PermissionMode: mode, MemorySpaces: in.MemorySpaces,
		Channels: channels, StandingGrants: in.StandingGrants, Status: types.SessionIdle, Origin: origin,
		CreatedBy: p, Debug: in.Debug, ParentTurnID: in.ParentTurnID, RunID: in.RunID, StepRunID: in.StepRunID,
		ToolDeny: in.ToolDeny, Labels: in.Labels,
	}
	if err := s.Store.Sessions.Insert(ctx, sess); err != nil {
		return nil, err
	}
	return sess, nil
}

func (s *Service) defaultRole(ctx context.Context) string {
	if t, err := tenant.ID(ctx); err == nil {
		if tn, err := s.Store.Tenants.Get(ctx, t); err == nil && tn.DefaultRole != "" {
			return tn.DefaultRole
		}
	}
	if s.DefaultRole != "" {
		return s.DefaultRole
	}
	return "general"
}

func hasKind(cs []types.Channel, k types.ChannelKind) (types.Channel, bool) {
	for _, c := range cs {
		if c.Kind == k {
			return c, true
		}
	}
	return types.Channel{}, false
}

// SubmitResult reports what happened to a message.
type SubmitResult struct {
	Turn   *types.Turn `json:"turn"`
	Queued bool        `json:"queued"` // folded into the running turn (web only)
}

// Submit starts a turn, or folds the message into the running turn for
// interactive sessions (§8.5). API sessions get a conflict instead.
func (s *Service) Submit(ctx context.Context, sessionID, text, idemKey string) (*SubmitResult, error) {
	if strings.TrimSpace(text) == "" {
		return nil, errs.Invalid("message text is required")
	}
	if len(text) > 100_000 {
		return nil, errs.Invalid("message too long")
	}
	p, _ := tenant.From(ctx)
	sess, err := s.Store.Sessions.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := s.canUse(p, sess); err != nil {
		return nil, err
	}
	t, err := turn.StartTurn(ctx, s.Store, s.Queue, sessionID, turn.StartInput{Text: text, Author: p.ID, IdemKey: idemKey, Debug: sess.Debug})
	if err == nil {
		return &SubmitResult{Turn: t}, nil
	}
	if !errs.Is(err, errs.CodeConflict) || sess.Origin == types.OriginAPI {
		return nil, err
	}
	cur, err := repo.Update(ctx, s.Store.Sessions, sessionID, func(x *types.Session) error {
		return sessionfsm.Enqueue(x, types.InboxItem{Text: text, Author: p.ID, At: s.Clock.Now()})
	})
	if err != nil {
		if errs.Is(err, errs.CodeConflict) {
			// It went idle between the two writes: start normally.
			t, err := turn.StartTurn(ctx, s.Store, s.Queue, sessionID, turn.StartInput{Text: text, Author: p.ID, IdemKey: idemKey})
			if err != nil {
				return nil, err
			}
			return &SubmitResult{Turn: t}, nil
		}
		return nil, err
	}
	active, err := s.Store.Turns.Get(ctx, cur.ActiveTurnID)
	if err != nil {
		return nil, err
	}
	return &SubmitResult{Turn: active, Queued: true}, nil
}

func (s *Service) canUse(p tenant.Principal, sess *types.Session) error {
	if p.IsAdmin() || p.Kind == tenant.KindSystem || sess.CreatedBy.ID == p.ID {
		return nil
	}
	return errs.New(errs.CodeForbidden, "not your session")
}

// Resolution outcome.
type ResolveResult struct {
	Pending         *types.Pending `json:"pending"`
	AlreadyResolved bool           `json:"already_resolved"`
}

// Resolve applies a person's decision. A second resolution from another
// channel returns the first one with AlreadyResolved=true (not an error).
func (s *Service) Resolve(ctx context.Context, pendingID string, decision types.Decision, answer, via string) (*ResolveResult, error) {
	p, _ := tenant.From(ctx)
	cur, err := s.Store.Pendings.Get(ctx, pendingID)
	if err != nil {
		return nil, err
	}
	if !p.IsAdmin() && p.Kind != tenant.KindSystem && cur.Assignee != "" && cur.Assignee != p.ID {
		return nil, errs.New(errs.CodeForbidden, "this item is assigned to someone else")
	}
	if err := s.authorizePending(ctx, cur); err != nil {
		return nil, err
	}
	return s.resolve(ctx, pendingID, types.Resolution{Decision: decision, Answer: answer, By: p, Via: via, At: s.Clock.Now()}, p.Kind == tenant.KindSystem)
}

// ResolveSystem is used by the platform (subtasks done, external jobs).
func (s *Service) ResolveSystem(ctx context.Context, pendingID string, decision types.Decision, answer string) (*ResolveResult, error) {
	p := tenant.Principal{Kind: tenant.KindSystem, ID: "system", TenantID: tenant.MustID(ctx), Role: tenant.RoleTenantAdmin}
	return s.resolve(ctx, pendingID, types.Resolution{Decision: decision, Answer: answer, By: p, Via: "system", At: s.Clock.Now()}, true)
}

func (s *Service) resolve(ctx context.Context, pendingID string, r types.Resolution, bySystem bool) (*ResolveResult, error) {
	p, err := repo.Update(ctx, s.Store.Pendings, pendingID, func(p *types.Pending) error {
		return approval.Resolve(p, r, bySystem)
	})
	if errs.Is(err, errs.CodeAlreadyResolved) {
		return &ResolveResult{Pending: p, AlreadyResolved: true}, nil
	}
	if err != nil {
		return nil, err
	}
	s.emit(ctx, p.TenantID, p.SessionID, p.TurnID, types.EvPendingResolved, map[string]any{"pending_id": p.ID, "decision": r.Decision, "via": r.Via, "by": r.By.ID})
	audit(ctx, s.Store, "pending.resolve", p.ID, map[string]any{"decision": r.Decision, "via": r.Via, "kind": p.Kind, "tool": p.ToolName})
	if err := s.maybeResume(ctx, p.TurnID); err != nil {
		return nil, err
	}
	return &ResolveResult{Pending: p}, nil
}

// maybeResume requeues a suspended turn once none of its items is open.
func (s *Service) maybeResume(ctx context.Context, turnID string) error {
	t, err := s.Store.Turns.Get(ctx, turnID)
	if err != nil {
		return err
	}
	if t.Status != types.TurnSuspended {
		return nil
	}
	for _, pid := range t.WaitingOn {
		p, err := s.Store.Pendings.Get(ctx, pid)
		if err != nil {
			return err
		}
		if p.Status == types.PendingOpen {
			return nil
		}
	}
	if _, err := repo.Update(ctx, s.Store.Sessions, t.SessionID, func(x *types.Session) error {
		return sessionfsm.Requeue(x, turnID)
	}); err != nil && !errs.Is(err, errs.CodeConflict) {
		return err
	}
	return turn.Enqueue(ctx, s.Queue, t.TenantID, turnID, true)
}

// Cancel stops further work: a durable flag (correctness) plus a signal
// (latency). Queued or suspended turns are ended right here. Idempotent.
func (s *Service) Cancel(ctx context.Context, sessionID string) (*types.Session, error) {
	p, _ := tenant.From(ctx)
	sess, err := s.Store.Sessions.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := s.canUse(p, sess); err != nil {
		return nil, err
	}
	sess, err = repo.Update(ctx, s.Store.Sessions, sessionID, func(x *types.Session) error {
		sessionfsm.RequestCancel(x)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if sess.ActiveTurnID == "" {
		return sess, nil
	}
	if s.Signal != nil {
		_ = s.Signal.Publish(ctx, coord.SessionControlChannel(sess.TenantID, sess.ID), []byte("cancel"))
	}
	t, err := s.Store.Turns.Get(ctx, sess.ActiveTurnID)
	if err != nil {
		return nil, err
	}
	if t.Status == types.TurnQueued || t.Status == types.TurnSuspended {
		if err := s.endIdleTurn(ctx, sess, t); err != nil {
			return nil, err
		}
	}
	return s.Store.Sessions.Get(ctx, sessionID)
}

// endIdleTurn cancels a turn no executor holds.
func (s *Service) endIdleTurn(ctx context.Context, sess *types.Session, t *types.Turn) error {
	now := s.Clock.Now()
	t, err := repo.Update(ctx, s.Store.Turns, t.ID, func(x *types.Turn) error {
		if x.Status != types.TurnQueued && x.Status != types.TurnSuspended {
			return errs.New(errs.CodeAlreadyResolved, "turn is %s", x.Status)
		}
		return sessionfsm.EndTurn(x, 0, types.TurnCancelled, nil, now)
	})
	if errs.Is(err, errs.CodeAlreadyResolved) {
		return nil
	}
	if err != nil {
		return err
	}
	stop := types.Resolution{Decision: types.DecisionStop, By: tenant.Principal{Kind: tenant.KindSystem, ID: "system", TenantID: sess.TenantID}, Via: "system", At: now}
	open, _ := repo.All(ctx, s.Store.Pendings, repo.Query{Filter: repo.Filter{"turn_id": t.ID, "status": string(types.PendingOpen)}}, 0)
	for _, p := range open {
		_, _ = repo.Update(ctx, s.Store.Pendings, p.ID, func(x *types.Pending) error { return approval.Resolve(x, stop, true) })
	}
	if _, err := repo.Update(ctx, s.Store.Sessions, sess.ID, func(x *types.Session) error {
		x.Inbox = nil
		return sessionfsm.Finish(x, t.ID)
	}); err != nil && !errs.Is(err, errs.CodeConflict) {
		return err
	}
	s.emit(ctx, sess.TenantID, sess.ID, t.ID, types.EvTurnCancelled, map[string]any{"status": types.TurnCancelled})
	return nil
}

func (s *Service) emit(ctx context.Context, tenantID, stream, turnID string, typ types.EventType, data map[string]any) {
	if s.Bus == nil {
		return
	}
	_, _ = s.Bus.Append(ctx, types.Event{Stream: stream, TenantID: tenantID, TurnID: turnID, Type: typ, TS: s.Clock.Now(), Data: data})
}

func audit(ctx context.Context, store *repo.Store, action, target string, detail map[string]any) {
	p, _ := tenant.From(ctx)
	a := &types.AuditLog{Actor: p, Action: action, Target: target, Detail: detail}
	_ = store.Audit.Insert(context.WithoutCancel(ctx), a)
}
