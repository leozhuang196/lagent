package admin

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/coord"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/infra/repo/scope"
	"github.com/eternityx/lagent/internal/rules/budget"
)

// TenantStatus counts live work of one tenant.
type TenantStatus struct {
	TenantID     string `json:"tenant_id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	Queued       int64  `json:"queued"`
	Running      int64  `json:"running"`
	Suspended    int64  `json:"suspended"`
	OpenPendings int64  `json:"open_pendings"`
	TodayTokens  int64  `json:"today_tokens"`
	DailyBudget  int64  `json:"daily_budget"`
}

// FailedTurn is a failed turn without its content: platform admins see that
// something failed, not what the tenant was working on.
type FailedTurn struct {
	ID        string           `json:"id"`
	TenantID  string           `json:"tenant_id"`
	SessionID string           `json:"session_id"`
	RoleKey   string           `json:"role_key"`
	RoleVer   int              `json:"role_ver"`
	Model     string           `json:"model,omitempty"`
	Error     *types.TurnError `json:"error,omitempty"`
	EndedAt   *time.Time       `json:"ended_at,omitempty"`
	Debug     bool             `json:"debug,omitempty"`
}

// PlatformStatus is the platform page. Queue depth and worker liveness are
// not observable through the queue interface (and not at all in memory
// mode), so they are absent rather than guessed; turn states stand in.
type PlatformStatus struct {
	Info           PlatformInfo   `json:"info"`
	Now            time.Time      `json:"now"`
	Tenants        []TenantStatus `json:"tenants"`
	RecentFailures []FailedTurn   `json:"recent_failures"`
	Notes          []string       `json:"notes,omitempty"`
}

// Status reports what this process can honestly see (platform admin).
func (s *Service) Status(ctx context.Context) (*PlatformStatus, error) {
	if err := requirePlatform(ctx); err != nil {
		return nil, err
	}
	now := s.now()
	st := &PlatformStatus{Info: s.Info, Now: now, Tenants: []TenantStatus{}, RecentFailures: []FailedTurn{}}
	tns, err := repo.All(scope.AllTenants(ctx), s.Store.Tenants, repo.Query{}, 1000)
	if err != nil {
		return nil, err
	}
	for _, tn := range tns {
		tc := tenant.SystemContext(ctx, tn.ID)
		ts := TenantStatus{TenantID: tn.ID, Name: tn.Name, Status: tn.Status, DailyBudget: tn.DailyTokenBudget}
		ts.Queued, _ = s.Store.Turns.Count(tc, repo.Filter{"status": string(types.TurnQueued)})
		ts.Running, _ = s.Store.Turns.Count(tc, repo.Filter{"status": string(types.TurnRunning)})
		ts.Suspended, _ = s.Store.Turns.Count(tc, repo.Filter{"status": string(types.TurnSuspended)})
		ts.OpenPendings, _ = s.Store.Pendings.Count(tc, repo.Filter{"status": string(types.PendingOpen)})
		if s.Counter != nil {
			ts.TodayTokens, _ = s.Counter.Get(tc, coord.TenantDailyTokensKey(tn.ID, budget.Day(now)))
		}
		st.Tenants = append(st.Tenants, ts)
	}
	sort.Slice(st.Tenants, func(i, j int) bool { return st.Tenants[i].TenantID < st.Tenants[j].TenantID })
	failed, err := s.Store.Turns.List(scope.AllTenants(ctx), repo.Query{Filter: repo.Filter{"status": string(types.TurnFailed)}, Desc: true, Limit: 20})
	if err != nil {
		return nil, err
	}
	for _, t := range failed.Items {
		st.RecentFailures = append(st.RecentFailures, FailedTurn{ID: t.ID, TenantID: t.TenantID, SessionID: t.SessionID, RoleKey: t.RoleKey, RoleVer: t.RoleVer, Model: t.Model, Error: t.Error, EndedAt: t.EndedAt, Debug: t.Debug})
	}
	if s.Info.StoreDriver == "memory" || s.Info.StoreDriver == "" {
		st.Notes = append(st.Notes, "memory store: counts cover this process only and are lost on restart")
	}
	if !s.Info.Redis {
		st.Notes = append(st.Notes, "no redis: queue, leases and counters are in-process; queue depth is not reported")
	}
	return st, nil
}

// minReasonLen keeps "x" from passing as a justification.
const minReasonLen = 8

func checkReason(reason string) (string, error) {
	r := strings.TrimSpace(reason)
	if len(r) < minReasonLen {
		return "", errs.Invalid("a reason (at least %d characters) is required to read tenant content", minReasonLen)
	}
	return r, nil
}

// BreakGlassSession is a tenant session as a platform admin reads it.
type BreakGlassSession struct {
	Session  *types.Session   `json:"session"`
	Messages []*types.Message `json:"messages"`
	Turns    []*types.Turn    `json:"turns"`
	Pendings []*types.Pending `json:"pendings"`
}

// ReadSessionBreakGlass lets a platform admin read any tenant's session
// content. A reason is mandatory and the read is audited in the owning
// tenant's log, so the tenant can see who looked and why (§11).
func (s *Service) ReadSessionBreakGlass(ctx context.Context, sessionID, reason string) (*BreakGlassSession, error) {
	if err := requirePlatform(ctx); err != nil {
		return nil, err
	}
	why, err := checkReason(reason)
	if err != nil {
		return nil, err
	}
	sess, err := s.Store.Sessions.Get(scope.AllTenants(ctx), sessionID)
	if err != nil {
		return nil, err
	}
	tc := sessionTenantCtx(ctx, sess.TenantID)
	// Audit before returning anything: a read that cannot be recorded is refused.
	a := &types.AuditLog{Action: "breakglass.session.read", Target: sess.ID, Reason: why}
	a.Actor, _ = tenant.From(ctx)
	a.TenantID = sess.TenantID
	if err := s.Store.Audit.Insert(context.WithoutCancel(tc), a); err != nil {
		return nil, errs.Wrap(errs.CodeUnavailable, err, "cannot record break-glass audit")
	}
	out := &BreakGlassSession{Session: sess}
	if out.Messages, err = repo.All(tc, s.Store.Messages, repo.Query{Filter: repo.Filter{"session_id": sess.ID}}, 2000); err != nil {
		return nil, err
	}
	sort.Slice(out.Messages, func(i, j int) bool { return out.Messages[i].Seq < out.Messages[j].Seq })
	if out.Turns, err = repo.All(tc, s.Store.Turns, repo.Query{Filter: repo.Filter{"session_id": sess.ID}}, 1000); err != nil {
		return nil, err
	}
	if out.Pendings, err = repo.All(tc, s.Store.Pendings, repo.Query{Filter: repo.Filter{"session_id": sess.ID}}, 1000); err != nil {
		return nil, err
	}
	return out, nil
}

// ReadTurnBreakGlass returns a turn timeline of any tenant, under the same
// reason-and-audit rule as ReadSessionBreakGlass.
func (s *Service) ReadTurnBreakGlass(ctx context.Context, turnID, reason string) (*Timeline, error) {
	if err := requirePlatform(ctx); err != nil {
		return nil, err
	}
	why, err := checkReason(reason)
	if err != nil {
		return nil, err
	}
	t, err := s.Store.Turns.Get(scope.AllTenants(ctx), turnID)
	if err != nil {
		return nil, err
	}
	tc := sessionTenantCtx(ctx, t.TenantID)
	a := &types.AuditLog{Action: "breakglass.turn.read", Target: t.ID, TurnID: t.ID, Reason: why}
	a.Actor, _ = tenant.From(ctx)
	a.TenantID = t.TenantID
	if err := s.Store.Audit.Insert(context.WithoutCancel(tc), a); err != nil {
		return nil, errs.Wrap(errs.CodeUnavailable, err, "cannot record break-glass audit")
	}
	return s.timeline(tc, turnID)
}
