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
	"github.com/eternityx/lagent/internal/rules/budget"
)

// GetOwnTenant returns the caller's tenant (settings page).
func (s *Service) GetOwnTenant(ctx context.Context) (*types.Tenant, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	id, err := tenant.ID(ctx)
	if err != nil {
		return nil, err
	}
	return s.Store.Tenants.Get(ctx, id)
}

// TenantSettings are what a tenant admin may change. Pointers mean "unchanged".
type TenantSettings struct {
	Name                  *string `json:"name,omitempty"`
	DailyTokenBudget      *int64  `json:"daily_token_budget,omitempty"`
	MaxConcurrentTurns    *int    `json:"max_concurrent_turns,omitempty"`
	DefaultRole           *string `json:"default_role,omitempty"`
	ArtifactRetentionDays *int    `json:"artifact_retention_days,omitempty"`
	LLMCallRetentionDays  *int    `json:"llm_call_retention_days,omitempty"`
}

// UpdateSettings changes the caller's tenant settings. 0 means "no limit" /
// "platform default" for every numeric field.
func (s *Service) UpdateSettings(ctx context.Context, in TenantSettings) (*types.Tenant, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	id, err := tenant.ID(ctx)
	if err != nil {
		return nil, err
	}
	if in.DailyTokenBudget != nil && *in.DailyTokenBudget < 0 {
		return nil, errs.Invalid("daily_token_budget must be >= 0")
	}
	if in.MaxConcurrentTurns != nil && (*in.MaxConcurrentTurns < 0 || *in.MaxConcurrentTurns > 10_000) {
		return nil, errs.Invalid("max_concurrent_turns must be 0..10000")
	}
	for _, d := range []*int{in.ArtifactRetentionDays, in.LLMCallRetentionDays} {
		if d != nil && (*d < 0 || *d > 3650) {
			return nil, errs.Invalid("retention days must be 0..3650")
		}
	}
	if in.Name != nil && (strings.TrimSpace(*in.Name) == "" || len(*in.Name) > MaxNameChars) {
		return nil, errs.Invalid("name must be 1..%d characters", MaxNameChars)
	}
	if in.DefaultRole != nil && *in.DefaultRole != "" {
		// A default role nobody can start would break every session that relies on it.
		r, err := repo.LatestPublished(ctx, s.Store.Roles, *in.DefaultRole)
		if err != nil || r.Status != types.AssetPublished {
			return nil, errs.Invalid("default_role %q has no published version", *in.DefaultRole)
		}
	}
	cur, err := s.Store.Tenants.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	before := *cur
	t, err := repo.Update(ctx, s.Store.Tenants, id, func(t *types.Tenant) error {
		if in.Name != nil {
			t.Name = *in.Name
		}
		if in.DailyTokenBudget != nil {
			t.DailyTokenBudget = *in.DailyTokenBudget
		}
		if in.MaxConcurrentTurns != nil {
			t.MaxConcurrentTurns = *in.MaxConcurrentTurns
		}
		if in.DefaultRole != nil {
			t.DefaultRole = *in.DefaultRole
		}
		if in.ArtifactRetentionDays != nil {
			t.ArtifactRetentionDay = *in.ArtifactRetentionDays
		}
		if in.LLMCallRetentionDays != nil {
			t.LLMCallRetentionDay = *in.LLMCallRetentionDays
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.audit(ctx, "tenant.settings", id, map[string]any{"diff": Diff(&before, t)})
	return t, nil
}

// UsageRow aggregates the turns of one day, role and model.
type UsageRow struct {
	Day      string      `json:"day"` // YYYY-MM-DD (UTC)
	Role     string      `json:"role"`
	Model    string      `json:"model"`
	Turns    int         `json:"turns"`
	Failures int         `json:"failures"`
	Usage    types.Usage `json:"usage"`
	Tokens   int64       `json:"tokens"`
}

// UsageReport is the usage page (§11). Debug turns are excluded from rows and
// reported separately: test runs must not distort business statistics.
type UsageReport struct {
	Days        int        `json:"days"`
	From        time.Time  `json:"from"`
	Rows        []UsageRow `json:"rows"`
	TotalTokens int64      `json:"total_tokens"`
	TotalTurns  int        `json:"total_turns"`
	DebugTokens int64      `json:"debug_tokens"`
	DebugTurns  int        `json:"debug_turns"`
	Today       struct {
		Day string `json:"day"`
		// LiveTokens is the budget counter (all model calls incl. debug and
		// compaction); it is what the daily budget is enforced against.
		LiveTokens  int64 `json:"live_tokens"`
		DailyBudget int64 `json:"daily_budget"`
	} `json:"today"`
	// Truncated is set when more turns existed than were scanned.
	Truncated bool `json:"truncated,omitempty"`
}

const maxUsageTurns = 200_000

// Usage aggregates the tenant's turns of the last days (UTC days, today included).
func (s *Service) Usage(ctx context.Context, days int) (*UsageReport, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	id, err := tenant.ID(ctx)
	if err != nil {
		return nil, err
	}
	if days <= 0 {
		days = 7
	}
	if days > 90 {
		days = 90
	}
	now := s.now().UTC()
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(days - 1))
	turns, err := repo.All(ctx, s.Store.Turns, repo.Query{Filter: repo.Filter{"created_at": repo.Gte(from)}}, maxUsageTurns)
	if err != nil {
		return nil, err
	}
	rep := &UsageReport{Days: days, From: from, Rows: []UsageRow{}, Truncated: len(turns) >= maxUsageTurns}
	type k struct{ day, role, model string }
	rows := map[k]*UsageRow{}
	for _, t := range turns {
		if t.Debug {
			rep.DebugTurns++
			rep.DebugTokens += t.Usage.Total()
			continue
		}
		key := k{t.CreatedAt.UTC().Format("2006-01-02"), t.RoleKey, t.Model}
		r := rows[key]
		if r == nil {
			r = &UsageRow{Day: key.day, Role: key.role, Model: key.model}
			rows[key] = r
		}
		r.Turns++
		if t.Status == types.TurnFailed {
			r.Failures++
		}
		r.Usage.Add(t.Usage)
		r.Tokens = r.Usage.Total()
		rep.TotalTurns++
		rep.TotalTokens += t.Usage.Total()
	}
	for _, r := range rows {
		rep.Rows = append(rep.Rows, *r)
	}
	sort.Slice(rep.Rows, func(i, j int) bool {
		a, b := rep.Rows[i], rep.Rows[j]
		if a.Day != b.Day {
			return a.Day < b.Day
		}
		if a.Role != b.Role {
			return a.Role < b.Role
		}
		return a.Model < b.Model
	})
	rep.Today.Day = now.Format("2006-01-02")
	if s.Counter != nil {
		rep.Today.LiveTokens, _ = s.Counter.Get(ctx, coord.TenantDailyTokensKey(id, budget.Day(now)))
	}
	if tn, err := s.Store.Tenants.Get(ctx, id); err == nil {
		rep.Today.DailyBudget = tn.DailyTokenBudget
	}
	return rep, nil
}

// AuditQuery filters the audit log. Empty fields match everything.
type AuditQuery struct {
	Action  string
	Target  string
	ActorID string
	From    *time.Time
	To      *time.Time
	After   string
	Limit   int
}

// QueryAudit pages the tenant's audit log, newest first.
func (s *Service) QueryAudit(ctx context.Context, q AuditQuery) (repo.Page[types.AuditLog], error) {
	if err := requireAdmin(ctx); err != nil {
		return repo.Page[types.AuditLog]{}, err
	}
	f := repo.Filter{}
	if q.Action != "" {
		f["action"] = q.Action
	}
	if q.Target != "" {
		f["target"] = q.Target
	}
	if q.ActorID != "" {
		f["actor.id"] = q.ActorID
	}
	// A filter holds one op per field. Audit logs are append-only, so
	// updated_at equals created_at and can carry the upper bound.
	if q.From != nil {
		f["created_at"] = repo.Gte(*q.From)
	}
	if q.To != nil {
		f["updated_at"] = repo.Lte(*q.To)
	}
	return s.Store.Audit.List(ctx, repo.Query{Filter: f, Desc: true, After: q.After, Limit: q.Limit})
}
