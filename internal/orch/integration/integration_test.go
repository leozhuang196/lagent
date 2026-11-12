package integration_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/eternityx/lagent/internal/capability/mcp"
	"github.com/eternityx/lagent/internal/capability/mcp/mcptest"
	"github.com/eternityx/lagent/internal/capability/retrieval"
	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/llm"
	"github.com/eternityx/lagent/internal/infra/llm/fake"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/infra/secret"
	secretlocal "github.com/eternityx/lagent/internal/infra/secret/local"
	"github.com/eternityx/lagent/internal/orch/integration"
	"github.com/eternityx/lagent/internal/orch/orchtest"
	"github.com/eternityx/lagent/internal/orch/session"
)

const secretValue = "sk-very-secret-value"

type fixture struct {
	e       *orchtest.Env
	fake    *mcptest.Fake
	secrets secret.Store
	svc     *integration.Service
}

func setup(t *testing.T) *fixture {
	t.Helper()
	e := orchtest.New(t)
	secrets, _ := secretlocal.New("", "k")
	f := mcptest.New()
	m := mcp.New(e.Store, secrets, mcp.Options{Dial: f.Dial})
	t.Cleanup(m.Close)
	e.Registry.AddProvider(m.Provider())
	e.Registry.Register(retrieval.Tools(e.Store, m)...)
	svc := &integration.Service{Store: e.Store, Secrets: secrets, MCP: m, Registry: e.Registry, Sandbox: e.Sandbox}
	return &fixture{e: e, fake: f, secrets: secrets, svc: svc}
}

func ptr[T any](v T) *T { return &v }

// docsServer creates and syncs the "docs" server with docs.search explicitly lowered.
func (fx *fixture) docsServer(t *testing.T) *integration.ServerView {
	t.Helper()
	v, err := fx.svc.CreateServer(fx.e.Admin, integration.ServerInput{
		Name: "docs", Transport: types.MCPStreamable, URL: "https://mcp.example.com/mcp", Secret: ptr(secretValue),
		RiskOverrides: map[string]types.RiskLevel{"docs.search": types.RiskReadOnly},
	})
	if err != nil {
		t.Fatal(err)
	}
	v, err = fx.svc.SyncServer(fx.e.Admin, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != "ok" || len(v.Tools) != 2 {
		t.Fatalf("sync: %+v", v)
	}
	return v
}

func noSecret(t *testing.T, what string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), secretValue) {
		t.Fatalf("%s leaks the secret: %s", what, b)
	}
}

func TestServerLifecycleKeepsSecretOut(t *testing.T) {
	fx := setup(t)
	ctx := fx.e.Admin
	v := fx.docsServer(t)
	if !v.HasSecret {
		t.Fatal("has_secret not set")
	}
	noSecret(t, "create/sync view", v)
	stored, _ := fx.svc.Store.MCPServers.Get(ctx, v.ID)
	if stored.SecretRef != "lagent/t1/mcp/"+v.ID {
		t.Fatalf("ref %q", stored.SecretRef)
	}
	noSecret(t, "record", stored)
	if got, _ := fx.secrets.Get(ctx, stored.SecretRef); got != secretValue {
		t.Fatalf("secret store has %q", got)
	}
	if s := fx.fake.Secrets(); len(s) == 0 || s[len(s)-1] != secretValue {
		t.Fatalf("secret not passed to transport: %v", s)
	}
	list, _ := fx.svc.ListServers(ctx)
	noSecret(t, "list", list)
	got, _ := fx.svc.GetServer(ctx, v.ID)
	noSecret(t, "get", got)

	// Risk: explicit lowering for docs.search only.
	risks := map[string]types.RiskLevel{}
	for _, tl := range got.Tools {
		risks[tl.RegistryName] = tl.Risk
	}
	if risks["docs__docs_search"] != types.RiskReadOnly || risks["docs__create_ticket"] != types.RiskExternal {
		t.Fatalf("risks %v", risks)
	}

	// Update without secret keeps it; empty secret removes it.
	up, err := fx.svc.UpdateServer(ctx, v.ID, integration.ServerInput{Transport: types.MCPStreamable, URL: "https://mcp.example.com/v2"})
	if err != nil || !up.HasSecret {
		t.Fatalf("update: %v %+v", err, up)
	}
	if _, err := fx.svc.UpdateServer(ctx, v.ID, integration.ServerInput{Name: "renamed", Transport: types.MCPStreamable, URL: "https://x"}); !errs.Is(err, errs.CodeInvalid) {
		t.Fatalf("rename: %v", err)
	}
	up, err = fx.svc.UpdateServer(ctx, v.ID, integration.ServerInput{Transport: types.MCPStreamable, URL: "https://mcp.example.com/v2", Secret: ptr("")})
	if err != nil || up.HasSecret {
		t.Fatalf("clear secret: %v %+v", err, up)
	}
	if _, err := fx.secrets.Get(ctx, stored.SecretRef); !errs.Is(err, errs.CodeNotFound) {
		t.Fatalf("secret not deleted: %v", err)
	}

	audits, _ := repo.All(ctx, fx.svc.Store.Audit, repo.Query{}, 0)
	if len(audits) < 4 {
		t.Fatalf("audits %d", len(audits))
	}
	noSecret(t, "audit", audits)

	// Members cannot write; tenant admins cannot run host processes.
	if _, err := fx.svc.CreateServer(fx.e.Ctx, integration.ServerInput{Name: "x", Transport: types.MCPStreamable, URL: "https://x"}); !errs.Is(err, errs.CodeForbidden) {
		t.Fatalf("member create: %v", err)
	}
	if _, err := fx.svc.CreateServer(ctx, integration.ServerInput{Name: "x", Transport: types.MCPStdio, Command: []string{"sh"}}); !errs.Is(err, errs.CodeForbidden) {
		t.Fatalf("stdio by tenant admin: %v", err)
	}
	if _, err := fx.svc.CreateServer(ctx, integration.ServerInput{Name: "docs", Transport: types.MCPStreamable, URL: "https://x"}); !errs.Is(err, errs.CodeConflict) {
		t.Fatalf("duplicate: %v", err)
	}

	if err := fx.svc.DeleteServer(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	metas, _ := fx.e.Registry.List(ctx)
	for _, m := range metas {
		if m.Source == types.SourceMCP {
			t.Fatalf("deleted server still provides %s", m.Name)
		}
	}
}

func TestRiskOverrideRaiseOnly(t *testing.T) {
	fx := setup(t)
	ctx := fx.e.Admin
	fx.docsServer(t)

	if _, err := fx.svc.SetRiskOverride(ctx, integration.RiskOverrideInput{Tool: "docs__docs_search", Level: types.RiskLocalWrite}); err != nil {
		t.Fatalf("raise: %v", err)
	}
	if _, err := fx.svc.SetRiskOverride(ctx, integration.RiskOverrideInput{Tool: "docs__docs_search", Level: types.RiskExternal}); err != nil {
		t.Fatalf("raise again: %v", err)
	}
	if _, err := fx.svc.SetRiskOverride(ctx, integration.RiskOverrideInput{Tool: "docs__create_ticket", Level: types.RiskReadOnly}); !errs.Is(err, errs.CodeInvalid) {
		t.Fatalf("lowering accepted: %v", err)
	}
	if _, err := fx.svc.SetRiskOverride(ctx, integration.RiskOverrideInput{Tool: "write_file", Level: types.RiskReadOnly}); !errs.Is(err, errs.CodeInvalid) {
		t.Fatalf("lowering builtin accepted: %v", err)
	}
	if _, err := fx.svc.SetRiskOverride(ctx, integration.RiskOverrideInput{Tool: "nope", Level: types.RiskExternal}); !errs.Is(err, errs.CodeNotFound) {
		t.Fatalf("unknown tool: %v", err)
	}
	if _, err := fx.svc.SetRiskOverride(fx.e.Ctx, integration.RiskOverrideInput{Tool: "read_file", Level: types.RiskExternal}); !errs.Is(err, errs.CodeForbidden) {
		t.Fatalf("member: %v", err)
	}
	ovs, _ := fx.svc.ListRiskOverrides(ctx)
	if len(ovs) != 1 || ovs[0].Level != types.RiskExternal {
		t.Fatalf("overrides %+v", ovs)
	}
	cat, err := fx.svc.ToolCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range cat {
		if c.Name == "docs__docs_search" {
			found = true
			if c.Risk != types.RiskReadOnly || c.EffectiveRisk != types.RiskExternal || len(c.Overrides) != 1 || c.Server != "docs" {
				t.Fatalf("catalog entry %+v", c)
			}
		}
	}
	if !found {
		t.Fatal("mcp tool missing from catalog")
	}
	if err := fx.svc.DeleteRiskOverride(ctx, ovs[0].ID); err != nil {
		t.Fatal(err)
	}
}

func TestToolTester(t *testing.T) {
	fx := setup(t)
	ctx := fx.e.Admin
	fx.docsServer(t)

	if _, err := fx.svc.TestTool(ctx, "docs__create_ticket", json.RawMessage(`{"title":"t"}`), false); !errs.Is(err, errs.CodeConflict) {
		t.Fatalf("side effect without confirm: %v", err)
	}
	res, err := fx.svc.TestTool(ctx, "docs__create_ticket", json.RawMessage(`{"title":"t"}`), true)
	if err != nil || res.IsError || !strings.Contains(res.Content, "created ticket") || res.Risk != types.RiskExternal {
		t.Fatalf("confirmed: %v %+v", err, res)
	}
	res, err = fx.svc.TestTool(ctx, "read_file", json.RawMessage(`{"path":"missing.txt"}`), false)
	if err != nil || !res.IsError {
		t.Fatalf("read-only tool: %v %+v", err, res)
	}
	if _, err := fx.svc.TestTool(ctx, "ask_user", json.RawMessage(`{"question":"q"}`), true); !errs.Is(err, errs.CodeInvalid) {
		t.Fatalf("control tool: %v", err)
	}
	if _, err := fx.svc.TestTool(fx.e.Ctx, "read_file", nil, true); !errs.Is(err, errs.CodeForbidden) {
		t.Fatalf("member: %v", err)
	}
	audits, _ := repo.All(ctx, fx.svc.Store.Audit, repo.Query{Filter: repo.Filter{"action": "tool.test"}}, 0)
	if len(audits) != 2 {
		t.Fatalf("tool.test audits: %d", len(audits))
	}
}

func TestRetrievalAndTemplateAvailability(t *testing.T) {
	fx := setup(t)
	ctx := fx.e.Admin
	srv := fx.docsServer(t)

	def := &types.WorkflowDef{Name: "Report", Steps: []types.StepDef{
		{ID: "a", Instruction: "x", Sources: []string{"handbook"}},
		{ID: "b", Instruction: "y", Sources: []string{"crm", "handbook"}},
	}}
	def.Key, def.Ver, def.Status = "report", 1, types.AssetPublished
	if err := fx.e.Store.WorkflowDefs.Insert(ctx, def); err != nil {
		t.Fatal(err)
	}
	missing := func() []string {
		t.Helper()
		st, err := fx.svc.TemplateAvailability(ctx)
		if err != nil || len(st) != 1 || st[0].Key != "report" {
			t.Fatalf("availability: %v %+v", err, st)
		}
		if st[0].NeedsConfig != (len(st[0].Missing) > 0) {
			t.Fatalf("needs_config inconsistent: %+v", st[0])
		}
		return st[0].Missing
	}
	if got := missing(); !reflect.DeepEqual(got, []string{"crm", "handbook"}) {
		t.Fatalf("before: %v", got)
	}

	if _, err := fx.svc.CreateSource(ctx, integration.SourceInput{Name: "handbook", Server: "docs", Tool: "no_such_tool"}); !errs.Is(err, errs.CodeInvalid) {
		t.Fatalf("unknown tool accepted: %v", err)
	}
	src, err := fx.svc.CreateSource(ctx, integration.SourceInput{Name: "handbook", Server: "docs", Tool: "docs.search", FixedArgs: json.RawMessage(`{"index":"hb"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if got := missing(); !reflect.DeepEqual(got, []string{"crm"}) {
		t.Fatalf("after source: %v", got)
	}

	try, err := fx.svc.TrySource(ctx, src.ID, "holidays", 0)
	if err != nil || try.IsError || !strings.Contains(try.Content, `"index":"hb"`) {
		t.Fatalf("try: %v %+v", err, try)
	}

	// Disabling the server makes the source unavailable again.
	if _, err := fx.svc.UpdateServer(ctx, srv.ID, integration.ServerInput{Transport: types.MCPStreamable, URL: "https://mcp.example.com/mcp", Enabled: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	if got := missing(); !reflect.DeepEqual(got, []string{"crm", "handbook"}) {
		t.Fatalf("after disable: %v", got)
	}
}

// The model calls search inside a real turn; it is read-only, so it runs
// without approval even in ask_all mode, and the turn completes.
func TestSearchInTurn(t *testing.T) {
	fx := setup(t)
	fx.docsServer(t)
	if _, err := fx.svc.CreateSource(fx.e.Admin, integration.SourceInput{Name: "handbook", Server: "docs", Tool: "docs.search", FixedArgs: json.RawMessage(`{"index":"hb"}`)}); err != nil {
		t.Fatal(err)
	}
	fx.e.Fake.SetScript(func(req llm.Request) (*llm.Response, error) {
		if res, ok := fake.LastToolResult(req); ok {
			return fake.TextReply("done: " + res.Text), nil
		}
		return fake.ToolCall("tu1", "search", map[string]any{"source": "handbook", "query": "leave"}), nil
	})
	s, err := fx.e.Sessions.Create(fx.e.Ctx, session.CreateInput{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := fx.e.Sessions.Submit(fx.e.Ctx, s.ID, "find the leave policy", "")
	if err != nil {
		t.Fatal(err)
	}
	fx.e.Wait()
	tr := fx.e.Turn(r.Turn.ID)
	if tr.Status != types.TurnCompleted || !strings.Contains(tr.FinalText, "retrieval:handbook") || !strings.Contains(tr.FinalText, `"query":"leave"`) {
		t.Fatalf("turn %s %q %+v", tr.Status, tr.FinalText, tr.Error)
	}
}
