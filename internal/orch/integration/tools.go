package integration

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/eternityx/lagent/internal/capability/tool"
	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/id"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/infra/sandbox"
	"github.com/eternityx/lagent/internal/rules/permission"
)

// CatalogEntry is one tool available to the tenant, with its risk as the
// engine will apply it. Risk (from ToolMeta) is the base risk; EffectiveRisk
// includes tenant-wide overrides; per-user overrides are listed separately.
type CatalogEntry struct {
	types.ToolMeta
	Description   string                `json:"description"`
	InputSchema   json.RawMessage       `json:"input_schema"`
	EffectiveRisk types.RiskLevel       `json:"effective_risk"`
	Overrides     []*types.RiskOverride `json:"overrides"`
}

func (s *Service) overrides(ctx context.Context) ([]*types.RiskOverride, error) {
	return repo.All(ctx, s.Store.RiskOverrides, repo.Query{}, 1000)
}

// ToolCatalog lists every tool registered for the tenant in ctx.
func (s *Service) ToolCatalog(ctx context.Context) ([]CatalogEntry, error) {
	metas, err := s.Registry.List(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(metas))
	for i, m := range metas {
		names[i] = m.Name
	}
	tools, _, err := s.Registry.Resolve(ctx, names)
	if err != nil {
		return nil, err
	}
	ovs, err := s.overrides(ctx)
	if err != nil {
		return nil, err
	}
	byTool := map[string][]*types.RiskOverride{}
	for _, o := range ovs {
		byTool[o.Tool] = append(byTool[o.Tool], o)
	}
	out := make([]CatalogEntry, 0, len(tools))
	for _, t := range tools {
		spec, meta := t.Spec(), t.Meta()
		var tenantWide []types.RiskLevel
		for _, o := range byTool[meta.Name] {
			if o.UserID == "" {
				tenantWide = append(tenantWide, o.Level)
			}
		}
		e := CatalogEntry{
			ToolMeta: meta, Description: spec.Description, InputSchema: spec.InputSchema,
			EffectiveRisk: permission.EffectiveRisk(meta.Risk, tenantWide...), Overrides: byTool[meta.Name],
		}
		if e.Overrides == nil {
			e.Overrides = []*types.RiskOverride{}
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ListRiskOverrides lists the tenant's overrides.
func (s *Service) ListRiskOverrides(ctx context.Context) ([]*types.RiskOverride, error) {
	ovs, err := s.overrides(ctx)
	if err != nil {
		return nil, err
	}
	if ovs == nil {
		ovs = []*types.RiskOverride{}
	}
	return ovs, nil
}

// RiskOverrideInput sets the risk of one tool for the tenant, or for one user
// when UserID is set.
type RiskOverrideInput struct {
	Tool   string          `json:"tool"`
	Level  types.RiskLevel `json:"level"`
	UserID string          `json:"user_id,omitempty"`
}

// SetRiskOverride creates or replaces the override for (tool, user). Overrides
// may only raise a tool's risk above its base level (§9.9): lowering is an
// explicit per-tool decision in the MCP server configuration, never here.
func (s *Service) SetRiskOverride(ctx context.Context, in RiskOverrideInput) (*types.RiskOverride, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if !in.Level.Valid() {
		return nil, errs.Invalid("unknown risk level %q", in.Level)
	}
	t, err := s.Registry.Get(ctx, in.Tool)
	if err != nil {
		return nil, err
	}
	base := permission.EffectiveRisk(t.Meta().Risk)
	if in.Level.Rank() < base.Rank() {
		return nil, errs.Invalid("an override can only raise risk: %s is %s, %s would lower it", in.Tool, base, in.Level).
			With("base_risk", string(base))
	}
	f := repo.Filter{"tool": in.Tool, "user_id": in.UserID}
	if in.UserID == "" {
		f["user_id"] = repo.Exists(false)
	}
	var out *types.RiskOverride
	for attempt := 0; attempt < 3; attempt++ {
		cur, err := s.Store.RiskOverrides.FindOne(ctx, f)
		if err == nil {
			out, err = repo.Update(ctx, s.Store.RiskOverrides, cur.ID, func(o *types.RiskOverride) error {
				o.Level = in.Level
				return nil
			})
			if err != nil {
				return nil, err
			}
			break
		}
		if !errs.Is(err, errs.CodeNotFound) {
			return nil, err
		}
		o := &types.RiskOverride{Tool: in.Tool, Level: in.Level, UserID: in.UserID}
		err = s.Store.RiskOverrides.Insert(ctx, o)
		if err == nil {
			out = o
			break
		}
		if !errs.Is(err, errs.CodeConflict) {
			return nil, err
		}
		// Lost a race with a concurrent insert: update the winner instead.
	}
	if out == nil {
		return nil, errs.New(errs.CodeUnavailable, "too much contention on risk override %s", in.Tool)
	}
	s.audit(ctx, "risk_override.set", out.ID, map[string]any{"tool": in.Tool, "level": in.Level, "user_id": in.UserID, "base_risk": base})
	return out, nil
}

// DeleteRiskOverride removes an override.
func (s *Service) DeleteRiskOverride(ctx context.Context, oid string) error {
	if err := requireAdmin(ctx); err != nil {
		return err
	}
	o, err := s.Store.RiskOverrides.Get(ctx, oid)
	if err != nil {
		return err
	}
	if err := s.Store.RiskOverrides.Delete(ctx, oid); err != nil {
		return err
	}
	s.audit(ctx, "risk_override.delete", oid, map[string]any{"tool": o.Tool, "level": o.Level, "user_id": o.UserID})
	return nil
}

// ToolTestResult is the outcome of a direct tool call from the console.
type ToolTestResult struct {
	Tool      string          `json:"tool"`
	Risk      types.RiskLevel `json:"risk"`
	Content   string          `json:"content"`
	IsError   bool            `json:"is_error"`
	Artifacts []string        `json:"artifacts,omitempty"`
	Control   string          `json:"control,omitempty"`
	MS        int64           `json:"duration_ms"`
}

const toolTestTimeout = 2 * time.Minute

// TestTool calls a registered tool directly for the console's tool tester.
// Anything that is not read-only (after overrides) needs confirm=true: the
// tester bypasses the approval flow, so the admin's confirmation is the
// approval. Every call is audited, successful or not.
func (s *Service) TestTool(ctx context.Context, name string, input json.RawMessage, confirm bool) (*ToolTestResult, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	p, _ := tenant.From(ctx)
	t, err := s.Registry.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	meta := t.Meta()
	if meta.Source == types.SourcePlatform {
		// Control tools (ask_user, submit_result, subtasks) only make sense in a turn.
		return nil, errs.Invalid("%s steers a running task and cannot be tested directly", name)
	}
	ovs, err := s.overrides(ctx)
	if err != nil {
		return nil, err
	}
	var lv []types.RiskLevel
	for _, o := range ovs {
		if o.Tool == name && (o.UserID == "" || o.UserID == p.ID) {
			lv = append(lv, o.Level)
		}
	}
	risk := permission.EffectiveRisk(meta.Risk, lv...)
	if risk != types.RiskReadOnly && !confirm {
		return nil, errs.Conflict("%s is a %s tool; confirm the call to run it", name, risk).With("risk", string(risk))
	}
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	var keys []string
	var m map[string]json.RawMessage
	if json.Unmarshal(input, &m) == nil {
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
	}

	callID := id.NewWithPrefix("tt")
	env := tool.Env{
		TenantID: p.TenantID, SessionID: "tooltest", ToolUseID: callID, IdempotencyKey: "tooltest:" + callID,
		Principal: p, Secret: s.Secrets.Get,
	}
	if s.Sandbox != nil {
		// One scratch workspace per admin, separate from every real session.
		ws, err := s.Sandbox.Open(ctx, p.TenantID, "tooltest-"+sanitizeID(p.ID))
		if err != nil {
			return nil, err
		}
		env.Workspace = ws
	} else {
		env.Workspace = &sandbox.Workspace{TenantID: p.TenantID, SessionID: "tooltest"}
	}

	cctx, cancel := context.WithTimeout(ctx, toolTestTimeout)
	defer cancel()
	start := time.Now()
	res, cerr := t.Call(cctx, input, env)
	out := &ToolTestResult{Tool: name, Risk: risk, MS: time.Since(start).Milliseconds()}
	if cerr != nil {
		// Internal errors may carry implementation detail; coded ones are meant to be shown.
		out.IsError, out.Content = true, "tool call failed (internal error)"
		if errs.CodeOf(cerr) != errs.CodeInternal {
			out.Content = cerr.Error()
		}
	} else {
		out.Content, out.IsError, out.Artifacts = res.Content, res.IsError, res.Artifacts
		if res.Control != nil {
			out.Control = string(res.Control.Kind)
		}
	}
	// Input values are not recorded: an admin may paste anything into the
	// tester. The keys are enough to tell what was attempted.
	s.audit(ctx, "tool.test", name, map[string]any{
		"call_id": callID, "risk": risk, "confirmed": confirm, "input_keys": keys,
		"is_error": out.IsError, "duration_ms": out.MS,
	})
	return out, nil
}

func sanitizeID(s string) string {
	b := []byte(s)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			b[i] = '_'
		}
	}
	return string(b)
}
