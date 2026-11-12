package admin_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/eternityx/lagent/internal/core/clock"
	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/llm"
	"github.com/eternityx/lagent/internal/infra/llm/fake"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/orch/admin"
	"github.com/eternityx/lagent/internal/orch/orchtest"
	"github.com/eternityx/lagent/internal/orch/session"
)

func setup(t *testing.T) (*orchtest.Env, *admin.Service) {
	t.Helper()
	e := orchtest.New(t)
	a := &admin.Service{Store: e.Store, Clock: clock.Real{}, Registry: e.Registry, Sessions: e.Sessions, Counter: e.Coord, Bus: e.Bus,
		Info: admin.PlatformInfo{StoreDriver: "memory", ProcessRoles: []string{"api", "engine", "worker"}}}
	return e, a
}

// t2Admin is a tenant admin of a second tenant, for isolation checks.
func t2Admin(t *testing.T, e *orchtest.Env) context.Context {
	t.Helper()
	tn := &types.Tenant{Name: "Other", Status: "active"}
	tn.ID = "t2"
	if err := e.Store.Tenants.Insert(tenant.SystemContext(context.Background(), "t2"), tn); err != nil {
		t.Fatal(err)
	}
	return tenant.With(context.Background(), tenant.Principal{Kind: tenant.KindAPIKey, ID: "admin2", TenantID: "t2", Role: tenant.RoleTenantAdmin})
}

func platformCtx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{Kind: tenant.KindAPIKey, ID: "ops", TenantID: "ops", Role: tenant.RolePlatformAdmin})
}

func role(prompt string, tools ...string) *types.Role {
	return &types.Role{Name: "Ads", Prompt: prompt, Tools: tools, DefaultPermissionMode: types.ModeAskExternal}
}

func must[T any](t *testing.T) func(v T, err error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

func audits(t *testing.T, e *orchtest.Env, ctx context.Context, action string) []*types.AuditLog {
	t.Helper()
	out, err := repo.All(ctx, e.Store.Audit, repo.Query{Filter: repo.Filter{"action": action}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// hasPath reports whether a stored diff contains path. Audit details are
// free-form maps, so a []Change written by the service comes back from any
// repository (memory or Mongo, both JSON-shaped) as []any of map[string]any.
func hasPath(d any, path string) bool {
	switch v := d.(type) {
	case []admin.Change:
		for _, c := range v {
			if c.Path == path {
				return true
			}
		}
	case []any:
		for _, x := range v {
			if m, ok := x.(map[string]any); ok && m["path"] == path {
				return true
			}
		}
	}
	return false
}

func TestDraftPublishRollbackVersioning(t *testing.T) {
	e, a := setup(t)
	roles := a.Roles()
	must[*types.Role](t)(roles.SaveDraft(e.Admin, "ads", role("v1 prompt", "read_file")))
	v1 := must[*types.Role](t)(roles.Publish(e.Admin, "ads", "first"))
	if v1.Ver != 1 || v1.Status != types.AssetPublished || v1.PublishedBy == nil || v1.Note != "first" {
		t.Fatalf("v1: %+v", v1.Versioned)
	}
	if _, err := roles.Publish(e.Admin, "ads", ""); !errs.Is(err, errs.CodeConflict) {
		t.Fatalf("publish without draft: %v", err)
	}
	must[*types.Role](t)(roles.SaveDraft(e.Admin, "ads", role("v2 prompt", "read_file", "write_file")))
	v2 := must[*types.Role](t)(roles.Publish(e.Admin, "ads", "second"))
	if v2.Ver != 2 {
		t.Fatalf("v2 ver %d", v2.Ver)
	}
	v3 := must[*types.Role](t)(roles.Rollback(e.Admin, "ads", 1, "bad tools"))
	if v3.Ver != 3 || v3.Prompt != "v1 prompt" || !strings.Contains(v3.Note, "v1") {
		t.Fatalf("rollback: ver %d prompt %q note %q", v3.Ver, v3.Prompt, v3.Note)
	}
	d := must[*admin.Detail[types.Role]](t)(roles.Get(e.Admin, "ads"))
	if d.Draft != nil || len(d.Versions) != 3 || d.EffectiveVer != 3 || d.Effective.Layer != admin.LayerTenant {
		t.Fatalf("detail: draft=%v versions=%d eff=%d", d.Draft != nil, len(d.Versions), d.EffectiveVer)
	}
	// History is intact: v1 and v2 are unchanged.
	if g := must[*admin.Version[types.Role]](t)(roles.GetVersion(e.Admin, "ads", 2)); g.Asset.Prompt != "v2 prompt" {
		t.Fatalf("v2 rewritten: %q", g.Asset.Prompt)
	}
	ch := must[[]admin.Change](t)(roles.DiffVersions(e.Admin, "ads", 1, 2))
	paths := map[string]bool{}
	for _, c := range ch {
		paths[c.Path] = true
	}
	if !paths["prompt"] || !paths["tools"] || paths["ver"] || paths["id"] {
		t.Fatalf("diff paths %v", paths)
	}
	// Every write is audited with a diff.
	pubs := audits(t, e, e.Admin, "role.publish")
	if len(pubs) != 2 || !hasPath(pubs[1].Detail["diff"], "prompt") {
		t.Fatalf("publish audits: %d %v", len(pubs), pubs)
	}
	if rb := audits(t, e, e.Admin, "role.rollback"); len(rb) != 1 || !hasPath(rb[0].Detail["diff"], "prompt") {
		t.Fatalf("rollback audit: %+v", rb)
	}
	if ds := audits(t, e, e.Admin, "role.draft.save"); len(ds) != 2 {
		t.Fatalf("draft audits: %d", len(ds))
	}
	// Disable the latest: "latest" falls back to v2, new sessions cannot pin v3.
	must[*types.Role](t)(roles.SetDisabled(e.Admin, "ads", 3, true))
	must[*types.Role](t)(roles.SetDisabled(e.Admin, "ads", 3, true)) // idempotent
	if l, _ := repo.LatestPublished(e.Admin, e.Store.Roles, "ads"); l.Ver != 2 {
		t.Fatalf("latest after disable: %d", l.Ver)
	}
	three := 3
	if _, err := e.Sessions.Create(e.Admin, session.CreateInput{RoleKey: "ads", RoleVer: &three}); err == nil {
		t.Fatal("disabled version accepted")
	}
	if n := len(audits(t, e, e.Admin, "role.disable")); n != 1 {
		t.Fatalf("disable audits %d", n)
	}
	must[*types.Role](t)(roles.SetDisabled(e.Admin, "ads", 3, false))
	// Draft delete.
	must[*types.Role](t)(roles.SaveDraft(e.Admin, "ads", role("tmp", "*")))
	if err := roles.DeleteDraft(e.Admin, "ads"); err != nil {
		t.Fatal(err)
	}
	if _, err := roles.GetVersion(e.Admin, "ads", 0); !errs.Is(err, errs.CodeNotFound) {
		t.Fatalf("draft still there: %v", err)
	}
}

func TestConcurrentPublishNeverDuplicates(t *testing.T) {
	e, a := setup(t)
	roles := a.Roles()
	// Same draft published by many admins at once: exactly one new version.
	must[*types.Role](t)(roles.SaveDraft(e.Admin, "race", role("p", "*")))
	var wg sync.WaitGroup
	var mu sync.Mutex
	okCount, conflicts := 0, 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := roles.Publish(e.Admin, "race", "")
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				okCount++
			case errs.Is(err, errs.CodeConflict):
				conflicts++
			default:
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	wg.Wait()
	if okCount != 1 || conflicts != 15 {
		t.Fatalf("same draft: ok=%d conflicts=%d", okCount, conflicts)
	}
	// Publishes and rollbacks racing on one key get distinct numbers.
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				_, _ = roles.Rollback(e.Admin, "race", 1, "")
				return
			}
			if _, err := roles.SaveDraft(e.Admin, "race", role(fmt.Sprint("p", i), "*")); err != nil {
				t.Error(err)
				return
			}
			_, _ = roles.Publish(e.Admin, "race", "")
		}(i)
	}
	wg.Wait()
	all, _ := repo.All(e.Admin, e.Store.Roles, repo.Query{Filter: repo.Filter{"key": "race"}}, 0)
	seen := map[int]bool{}
	for _, r := range all {
		if seen[r.Ver] {
			t.Fatalf("duplicate version %d", r.Ver)
		}
		seen[r.Ver] = true
	}
	for v := 1; v < len(seen); v++ {
		if !seen[v] && !(v == len(seen) && seen[0]) {
			// gaps are allowed only when a draft (0) is counted in len(seen)
			if !seen[0] {
				t.Fatalf("missing version %d in %v", v, seen)
			}
		}
	}
	if len(seen) < 7 {
		t.Fatalf("expected at least 7 versions (1 + 6 rollbacks), got %v", seen)
	}
}

func TestBuiltinEditCreatesTenantOverride(t *testing.T) {
	e, a := setup(t)
	roles := a.Roles()
	ctx2 := t2Admin(t, e)
	// "general" is a built-in (seeded by orchtest in _system, v1).
	list := must[[]admin.Summary[types.Role]](t)(roles.List(e.Admin))
	if len(list) != 1 || list[0].Layer != admin.LayerBuiltin || !list[0].Builtin {
		t.Fatalf("list before: %+v", list)
	}
	d := must[*types.Role](t)(roles.SaveDraft(e.Admin, "general", &types.Role{Name: "General", Prompt: "Tenant flavoured.", Tools: []string{"read_file"}}))
	if d.TenantID != "t1" || d.Ver != 0 {
		t.Fatalf("draft in %s v%d", d.TenantID, d.Ver)
	}
	// Built-in untouched, still effective until publish.
	if l, _ := repo.LatestPublished(e.Admin, e.Store.Roles, "general"); l.TenantID != tenant.System {
		t.Fatalf("draft took effect early: %s", l.TenantID)
	}
	v := must[*types.Role](t)(roles.Publish(e.Admin, "general", "override"))
	if v.Ver != 2 || v.TenantID != "t1" {
		t.Fatalf("override ver %d in %s (must not reuse built-in numbers)", v.Ver, v.TenantID)
	}
	list = must[[]admin.Summary[types.Role]](t)(roles.List(e.Admin))
	if list[0].Layer != admin.LayerTenant || !list[0].Overridden || list[0].LatestVer != 2 {
		t.Fatalf("list after: %+v", list[0])
	}
	s := must[*types.Session](t)(e.Sessions.Create(e.Ctx, session.CreateInput{}))
	if s.RoleVer != 2 {
		t.Fatalf("session got v%d", s.RoleVer)
	}
	// Another tenant still sees the built-in.
	l2 := must[[]admin.Summary[types.Role]](t)(roles.List(ctx2))
	if len(l2) != 1 || l2[0].Layer != admin.LayerBuiltin || l2[0].Overridden {
		t.Fatalf("t2 list: %+v", l2)
	}
	// Built-in versions cannot be disabled by a tenant.
	if _, err := roles.SetDisabled(e.Admin, "general", 1, true); !errs.Is(err, errs.CodeForbidden) {
		t.Fatalf("disable built-in: %v", err)
	}
	// The built-in tenant itself cannot be edited through the API.
	if _, err := roles.SaveDraft(repo.SystemLayer(context.Background()), "general", role("x", "*")); !errs.Is(err, errs.CodeForbidden) {
		t.Fatalf("system edit: %v", err)
	}
}

func TestValidation(t *testing.T) {
	e, a := setup(t)
	roles := a.Roles()
	bad := []*types.Role{
		role("p", "read_file", "not_a_tool"),
		role("p", "mcp:nowhere"),
		{Name: "x", Tools: []string{"*"}, DefaultPermissionMode: "yolo"},
		{Name: "x", Tools: []string{"*"}, MaxIterations: -1},
		{Name: "x", Tools: []string{"*"}, TokenBudget: 1 << 40},
		{Name: "", Tools: []string{"*"}},
	}
	for i, r := range bad {
		if _, err := roles.SaveDraft(e.Admin, "v", r); !errs.Is(err, errs.CodeInvalid) {
			t.Fatalf("case %d accepted: %v", i, err)
		}
	}
	if _, err := roles.SaveDraft(e.Admin, "Bad Key", role("p", "*")); !errs.Is(err, errs.CodeInvalid) {
		t.Fatalf("bad key: %v", err)
	}
	// A configured MCP server is a valid pick even before its tools sync.
	srv := &types.MCPServer{Name: "crm", Transport: types.MCPStreamable, Enabled: true}
	if err := e.Store.MCPServers.Insert(e.Admin, srv); err != nil {
		t.Fatal(err)
	}
	must[*types.Role](t)(roles.SaveDraft(e.Admin, "v", role("p", "mcp:crm", "read_file")))

	skills := a.Skills()
	if _, err := skills.SaveDraft(e.Admin, "s", &types.Skill{Name: "s", Description: strings.Repeat("d", 2000)}); !errs.Is(err, errs.CodeInvalid) {
		t.Fatalf("long description: %v", err)
	}
	must[*types.Skill](t)(skills.SaveDraft(e.Admin, "s", &types.Skill{Name: "s", Description: "short", Body: "b"}))

	wf := a.WorkflowDefs()
	if _, err := wf.SaveDraft(e.Admin, "w", &types.WorkflowDef{Name: "w", Steps: []types.StepDef{{ID: "a", Instruction: "x", DependsOn: []string{"zz"}}}}); !errs.Is(err, errs.CodeInvalid) {
		t.Fatalf("bad workflow: %v", err)
	}
	must[*types.WorkflowDef](t)(wf.SaveDraft(e.Admin, "w", &types.WorkflowDef{Name: "w", Steps: []types.StepDef{{ID: "a", Instruction: "do it"}}}))
	if v := must[*types.WorkflowDef](t)(wf.Publish(e.Admin, "w", "")); v.Ver != 1 {
		t.Fatalf("wf ver %d", v.Ver)
	}
}

func TestMembersCannotWrite(t *testing.T) {
	e, a := setup(t)
	if _, err := a.Roles().List(e.Ctx); err != nil {
		t.Fatalf("member list: %v", err)
	}
	if _, err := a.Roles().SaveDraft(e.Ctx, "x", role("p", "*")); !errs.Is(err, errs.CodeForbidden) {
		t.Fatalf("member save: %v", err)
	}
	must[*types.Role](t)(a.Roles().SaveDraft(e.Admin, "x", role("p", "*")))
	if _, err := a.Roles().Publish(e.Ctx, "x", ""); !errs.Is(err, errs.CodeForbidden) {
		t.Fatalf("member publish: %v", err)
	}
	if _, err := a.Roles().GetVersion(e.Ctx, "x", 0); !errs.Is(err, errs.CodeForbidden) {
		t.Fatalf("member draft read: %v", err)
	}
	// Members do not see draft-only keys.
	for _, s := range must[[]admin.Summary[types.Role]](t)(a.Roles().List(e.Ctx)) {
		if s.Key == "x" {
			t.Fatal("draft-only key visible to member")
		}
	}
	if _, err := a.WorkflowDefs().List(e.Ctx); !errs.Is(err, errs.CodeForbidden) {
		t.Fatalf("member workflow list: %v", err)
	}
	for name, err := range map[string]error{
		"space":    second(a.CreateSpace(e.Ctx, admin.SpaceInput{Name: "m"})),
		"usage":    second(a.Usage(e.Ctx, 7)),
		"audit":    second(a.QueryAudit(e.Ctx, admin.AuditQuery{})),
		"preview":  second(a.AssemblyPreview(e.Ctx, admin.PreviewInput{Role: "general"})),
		"play":     second(a.PlaygroundSession(e.Ctx, admin.PlaygroundInput{Role: "general"})),
		"settings": second(a.UpdateSettings(e.Ctx, admin.TenantSettings{})),
		"status":   second(a.Status(e.Admin)),
	} {
		if !errs.Is(err, errs.CodeForbidden) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func second[T any](_ T, err error) error { return err }

func writeThenDone() fake.Script {
	return func(req llm.Request) (*llm.Response, error) {
		if res, ok := fake.LastToolResult(req); ok {
			return fake.TextReply("done: " + res.Text), nil
		}
		return fake.ToolCall("tu1", "write_file", map[string]string{"path": "out.md", "content": "# hi"}), nil
	}
}

func TestPlaygroundDraftAskAllPreviewAndTimeline(t *testing.T) {
	e, a := setup(t)
	// Memory and a skill make the preview non-trivial.
	must[*types.MemorySpace](t)(a.CreateSpace(e.Admin, admin.SpaceInput{Name: "facts"}))
	must[*types.MemoryEntry](t)(a.PutEntry(e.Admin, "facts", admin.EntryInput{Key: "fy", Content: "Fiscal year starts in April."}))
	must[*types.Skill](t)(a.Skills().SaveDraft(e.Admin, "sql", &types.Skill{Name: "sql", Description: "How to query the warehouse."}))
	must[*types.Skill](t)(a.Skills().Publish(e.Admin, "sql", ""))
	r := &types.Role{Name: "Dbg", Prompt: "Draft prompt.", Tools: []string{"write_file", "read_file"}, Skills: []string{"*"}, MemorySpaces: []string{"facts"}, DefaultPermissionMode: types.ModeAuto}
	must[*types.Role](t)(a.Roles().SaveDraft(e.Admin, "dbg", r))

	zero := 0
	// Drafts are refused outside debug sessions.
	if _, err := e.Sessions.Create(e.Admin, session.CreateInput{RoleKey: "dbg", RoleVer: &zero}); err == nil {
		t.Fatal("draft in a normal session")
	}
	s := must[*types.Session](t)(a.PlaygroundSession(e.Admin, admin.PlaygroundInput{Role: "dbg", Version: &zero}))
	if !s.Debug || s.PermissionMode != types.ModeAskAll || s.RoleVer != 0 || s.Origin != types.OriginDebug {
		t.Fatalf("playground session: debug=%v mode=%s ver=%d", s.Debug, s.PermissionMode, s.RoleVer)
	}
	e.Fake.SetScript(writeThenDone())
	res := must[*session.SubmitResult](t)(e.Sessions.Submit(e.Admin, s.ID, "write it", ""))
	e.Wait()
	tr := e.Turn(res.Turn.ID)
	// The role says auto, but debug sessions ask: the write waits for approval.
	if tr.Status != types.TurnSuspended || !tr.Debug || len(e.OpenPendings(tr.ID)) != 1 {
		t.Fatalf("turn %s debug=%v pendings=%d", tr.Status, tr.Debug, len(e.OpenPendings(tr.ID)))
	}

	// The stable preview matches the request; the runtime clock is uncached
	// and is only known when each model request is actually sent.
	pv := must[*admin.Preview](t)(a.AssemblyPreview(e.Admin, admin.PreviewInput{Role: "dbg", Version: &zero}))
	req := e.Fake.Requests[len(e.Fake.Requests)-1]
	if len(req.System) != len(pv.Parts)+1 {
		t.Fatalf("parts: model got %d, preview %d plus runtime clock", len(req.System), len(pv.Parts))
	}
	clock := req.System[len(req.System)-1]
	if clock.Cache || !strings.HasPrefix(clock.Text, "Current date and time: ") {
		t.Fatalf("runtime clock missing or cached: %+v", clock)
	}
	for i, p := range pv.Parts {
		if req.System[i].Text != p.Text || req.System[i].Cache != p.Cache {
			t.Fatalf("part %d differs:\nmodel:   %q\npreview: %q", i, req.System[i].Text, p.Text)
		}
	}
	var sent []string
	for _, sp := range req.Tools {
		sent = append(sent, sp.Name)
	}
	if strings.Join(sent, ",") != strings.Join(pv.Tools, ",") {
		t.Fatalf("tools: model %v preview %v", sent, pv.Tools)
	}
	if pv.TokensEstimate <= 0 || len(pv.Skills) != 1 || len(pv.MemorySpaces) != 1 || !pv.MemorySpaces[0].Found {
		t.Fatalf("preview: %+v", pv)
	}

	// Timeline: the model call, the ledger entry with its decision, the pending.
	tl := must[*admin.Timeline](t)(a.TurnTimeline(e.Admin, tr.ID))
	if tl.Totals.LLMCalls != 1 || tl.Totals.ToolCalls != 1 || tl.Totals.Pendings != 1 || len(tl.Events) == 0 {
		t.Fatalf("totals %+v events %d", tl.Totals, len(tl.Events))
	}
	if tl.LLMCalls[0].Request != nil {
		t.Fatal("timeline should not carry request bodies")
	}
	kinds := map[string]bool{}
	for _, en := range tl.Entries {
		kinds[en.Kind] = true
		if en.Kind == "event" && en.Title == string(types.EvMessageDelta) {
			t.Fatal("deltas in timeline")
		}
	}
	if !kinds["llm_call"] || !kinds["tool_call"] || !kinds["pending"] || !kinds["event"] {
		t.Fatalf("entry kinds %v", kinds)
	}
	if tl.ToolCalls[0].Tool != "write_file" || tl.ToolCalls[0].Decision == "" {
		t.Fatalf("ledger: %+v", tl.ToolCalls[0])
	}
	calls := must[[]*types.LLMCall](t)(a.ListLLMCalls(e.Admin, tr.ID))
	if len(calls) != 1 || len(calls[0].Request) == 0 {
		t.Fatalf("llm calls: %d", len(calls))
	}

	// Usage excludes debug turns.
	u := must[*admin.UsageReport](t)(a.Usage(e.Admin, 7))
	if u.TotalTurns != 0 || u.DebugTurns != 1 || u.DebugTokens == 0 || u.Today.LiveTokens == 0 {
		t.Fatalf("usage %+v", u)
	}
}

func TestForkCopiesHistoryAndContinues(t *testing.T) {
	e, a := setup(t)
	src := must[*types.Session](t)(e.Sessions.Create(e.Ctx, session.CreateInput{PermissionMode: types.ModeAuto}))
	for _, text := range []string{"one", "two"} {
		must[*session.SubmitResult](t)(e.Sessions.Submit(e.Ctx, src.ID, text, ""))
		e.Wait()
	}
	if len(e.Messages(src.ID)) != 4 {
		t.Fatalf("source messages %d", len(e.Messages(src.ID)))
	}
	fork := must[*types.Session](t)(a.Fork(e.Admin, src.ID, admin.ForkInput{UptoSeq: 2}))
	if !fork.Debug || fork.PermissionMode != types.ModeAskAll || fork.Labels["forked_from"] != src.ID || fork.RoleVer != src.RoleVer {
		t.Fatalf("fork: %+v", fork)
	}
	msgs := e.Messages(fork.ID)
	if len(msgs) != 2 || msgs[0].PlainText() != "one" || msgs[1].PlainText() != "echo: one" {
		t.Fatalf("fork history: %d", len(msgs))
	}
	r := must[*session.SubmitResult](t)(e.Sessions.Submit(e.Admin, fork.ID, "three", ""))
	e.Wait()
	if tr := e.Turn(r.Turn.ID); tr.Status != types.TurnCompleted || tr.FinalText != "echo: three" || !tr.Debug {
		t.Fatalf("fork turn %s %q", tr.Status, tr.FinalText)
	}
	req := e.Fake.Requests[len(e.Fake.Requests)-1]
	var all []string
	for _, m := range req.Messages {
		all = append(all, m.PlainText())
	}
	joined := strings.Join(all, "|")
	if joined != "one|echo: one|three" {
		t.Fatalf("fork sent %q", joined)
	}
	// The source is untouched.
	if len(e.Messages(src.ID)) != 4 {
		t.Fatal("source changed")
	}
	// Cutting between a tool call and its result is refused.
	e.Fake.SetScript(writeThenDone())
	s2 := must[*types.Session](t)(e.Sessions.Create(e.Ctx, session.CreateInput{PermissionMode: types.ModeAskAll}))
	must[*session.SubmitResult](t)(e.Sessions.Submit(e.Ctx, s2.ID, "go", ""))
	e.Wait()
	if _, err := a.Fork(e.Admin, s2.ID, admin.ForkInput{UptoSeq: 2}); !errs.Is(err, errs.CodeInvalid) {
		t.Fatalf("fork inside tool call: %v", err)
	}
}

func TestBreakGlassRequiresReasonAndIsAudited(t *testing.T) {
	e, a := setup(t)
	s := must[*types.Session](t)(e.Sessions.Create(e.Ctx, session.CreateInput{PermissionMode: types.ModeAuto}))
	r := must[*session.SubmitResult](t)(e.Sessions.Submit(e.Ctx, s.ID, "secret plan", ""))
	e.Wait()
	ops := platformCtx()
	if _, err := a.ReadSessionBreakGlass(ops, s.ID, ""); !errs.Is(err, errs.CodeInvalid) {
		t.Fatalf("no reason: %v", err)
	}
	if _, err := a.ReadSessionBreakGlass(e.Admin, s.ID, "because I want to"); !errs.Is(err, errs.CodeForbidden) {
		t.Fatalf("tenant admin: %v", err)
	}
	if n := len(audits(t, e, e.Admin, "breakglass.session.read")); n != 0 {
		t.Fatalf("audited a refused read: %d", n)
	}
	bg := must[*admin.BreakGlassSession](t)(a.ReadSessionBreakGlass(ops, s.ID, "incident INC-42 investigation"))
	if len(bg.Messages) != 2 || bg.Messages[0].PlainText() != "secret plan" {
		t.Fatalf("break-glass read: %d", len(bg.Messages))
	}
	logs := audits(t, e, e.Admin, "breakglass.session.read")
	if len(logs) != 1 || logs[0].Reason != "incident INC-42 investigation" || logs[0].Actor.ID != "ops" || logs[0].Target != s.ID {
		t.Fatalf("audit: %+v", logs)
	}
	tl := must[*admin.Timeline](t)(a.ReadTurnBreakGlass(ops, r.Turn.ID, "incident INC-42 investigation"))
	if tl.Totals.LLMCalls != 1 {
		t.Fatalf("turn timeline %+v", tl.Totals)
	}
	if n := len(audits(t, e, e.Admin, "breakglass.turn.read")); n != 1 {
		t.Fatalf("turn audit %d", n)
	}
	// The ordinary debugger never crosses tenants.
	if _, err := a.TurnTimeline(tenant.With(context.Background(), tenant.Principal{ID: "ops", TenantID: "ops", Role: tenant.RolePlatformAdmin}), r.Turn.ID); !errs.Is(err, errs.CodeNotFound) {
		t.Fatalf("cross-tenant timeline: %v", err)
	}
	st := must[*admin.PlatformStatus](t)(a.Status(ops))
	if len(st.Tenants) != 1 || st.Tenants[0].TenantID != "t1" || len(st.Notes) == 0 {
		t.Fatalf("status %+v", st)
	}
}

func TestTenantIsolation(t *testing.T) {
	e, a := setup(t)
	ctx2 := t2Admin(t, e)
	must[*types.Role](t)(a.Roles().SaveDraft(e.Admin, "mine", role("p", "*")))
	must[*types.Role](t)(a.Roles().Publish(e.Admin, "mine", ""))
	must[*types.Skill](t)(a.Skills().SaveDraft(e.Admin, "sk", &types.Skill{Name: "sk", Description: "d"}))
	must[*types.WorkflowDef](t)(a.WorkflowDefs().SaveDraft(e.Admin, "wf", &types.WorkflowDef{Name: "w", Steps: []types.StepDef{{ID: "a", Instruction: "x"}}}))
	must[*types.MemorySpace](t)(a.CreateSpace(e.Admin, admin.SpaceInput{Name: "facts"}))
	must[*types.MemoryEntry](t)(a.PutEntry(e.Admin, "facts", admin.EntryInput{Key: "k", Content: "v"}))
	s := must[*types.Session](t)(e.Sessions.Create(e.Ctx, session.CreateInput{PermissionMode: types.ModeAuto}))
	r := must[*session.SubmitResult](t)(e.Sessions.Submit(e.Ctx, s.ID, "hi", ""))
	e.Wait()

	for _, l := range must[[]admin.Summary[types.Role]](t)(a.Roles().List(ctx2)) {
		if l.Key == "mine" {
			t.Fatal("t2 sees t1 role")
		}
	}
	if _, err := a.Roles().Get(ctx2, "mine"); !errs.Is(err, errs.CodeNotFound) {
		t.Fatalf("t2 get: %v", err)
	}
	if l := must[[]admin.Summary[types.Skill]](t)(a.Skills().List(ctx2)); len(l) != 0 {
		t.Fatalf("t2 skills %d", len(l))
	}
	if l := must[[]admin.Summary[types.WorkflowDef]](t)(a.WorkflowDefs().List(ctx2)); len(l) != 0 {
		t.Fatalf("t2 workflows %d", len(l))
	}
	if l := must[[]admin.SpaceView](t)(a.ListSpaces(ctx2)); len(l) != 0 {
		t.Fatalf("t2 spaces %d", len(l))
	}
	if _, err := a.ListEntries(ctx2, "facts", admin.EntryQuery{}); !errs.Is(err, errs.CodeNotFound) {
		t.Fatalf("t2 entries: %v", err)
	}
	if p := must[repo.Page[types.AuditLog]](t)(a.QueryAudit(ctx2, admin.AuditQuery{})); len(p.Items) != 0 {
		t.Fatalf("t2 audit %d", len(p.Items))
	}
	if p := must[repo.Page[types.AuditLog]](t)(a.QueryAudit(e.Admin, admin.AuditQuery{})); len(p.Items) == 0 {
		t.Fatal("t1 audit empty")
	}
	if u := must[*admin.UsageReport](t)(a.Usage(ctx2, 7)); u.TotalTurns != 0 || u.Today.LiveTokens != 0 {
		t.Fatalf("t2 usage %+v", u)
	}
	if u := must[*admin.UsageReport](t)(a.Usage(e.Admin, 7)); u.TotalTurns != 1 || len(u.Rows) != 1 || u.Rows[0].Role != "general" || u.Rows[0].Tokens == 0 {
		t.Fatalf("t1 usage %+v", u)
	}
	for name, err := range map[string]error{
		"timeline": second(a.TurnTimeline(ctx2, r.Turn.ID)),
		"llm":      second(a.ListLLMCalls(ctx2, r.Turn.ID)),
		"fork":     second(a.Fork(ctx2, s.ID, admin.ForkInput{UptoSeq: 1})),
		"preview":  second(a.AssemblyPreview(ctx2, admin.PreviewInput{Role: "mine"})),
	} {
		if !errs.Is(err, errs.CodeNotFound) {
			t.Fatalf("%s across tenants: %v", name, err)
		}
	}
	if k := must[[]*types.APIKey](t)(a.ListKeys(ctx2)); len(k) != 0 {
		t.Fatalf("t2 keys %d", len(k))
	}
	tn := must[*types.Tenant](t)(a.GetOwnTenant(ctx2))
	if tn.ID != "t2" {
		t.Fatalf("own tenant %s", tn.ID)
	}
}

func TestMemoryAndSettings(t *testing.T) {
	e, a := setup(t)
	ro := true
	must[*types.MemorySpace](t)(a.CreateSpace(e.Admin, admin.SpaceInput{Name: "kb", ReadOnly: &ro}))
	if _, err := a.CreateSpace(e.Admin, admin.SpaceInput{Name: "kb"}); !errs.Is(err, errs.CodeConflict) {
		t.Fatalf("duplicate space: %v", err)
	}
	user := types.MemoryUser
	if _, err := a.CreateSpace(e.Admin, admin.SpaceInput{Name: "u", Scope: &user}); !errs.Is(err, errs.CodeInvalid) {
		t.Fatalf("user space without owner: %v", err)
	}
	for i := 0; i < 7; i++ {
		must[*types.MemoryEntry](t)(a.PutEntry(e.Admin, "kb", admin.EntryInput{Key: fmt.Sprintf("metric.%d", i), Content: "c"}))
	}
	must[*types.MemoryEntry](t)(a.PutEntry(e.Admin, "kb", admin.EntryInput{Key: "other", Content: "c"}))
	up := must[*types.MemoryEntry](t)(a.PutEntry(e.Admin, "kb", admin.EntryInput{Key: "other", Content: "changed", Priority: 3}))
	if up.Content != "changed" || up.Priority != 3 {
		t.Fatalf("update %+v", up)
	}
	p1 := must[repo.Page[types.MemoryEntry]](t)(a.ListEntries(e.Admin, "kb", admin.EntryQuery{Prefix: "metric.", Limit: 5}))
	if len(p1.Items) != 5 || p1.Next == "" {
		t.Fatalf("page1 %d next=%q", len(p1.Items), p1.Next)
	}
	p2 := must[repo.Page[types.MemoryEntry]](t)(a.ListEntries(e.Admin, "kb", admin.EntryQuery{Prefix: "metric.", Limit: 5, After: p1.Next}))
	if len(p2.Items) != 2 || p2.Next != "" {
		t.Fatalf("page2 %d next=%q", len(p2.Items), p2.Next)
	}
	if err := a.DeleteSpace(e.Admin, "kb"); !errs.Is(err, errs.CodeConflict) {
		t.Fatalf("delete non-empty: %v", err)
	}
	prio := 5
	sp := must[*types.MemorySpace](t)(a.UpdateSpace(e.Admin, "kb", admin.SpaceInput{Priority: &prio}))
	if sp.Priority != 5 || !sp.ReadOnly {
		t.Fatalf("space update %+v", sp)
	}
	if err := a.DeleteEntry(e.Admin, "kb", "other"); err != nil {
		t.Fatal(err)
	}
	if len(audits(t, e, e.Admin, "memory.entry.update")) != 1 || len(audits(t, e, e.Admin, "memory.entry.delete")) != 1 {
		t.Fatal("memory audits missing")
	}

	budget := int64(1000)
	bogus := "nope"
	if _, err := a.UpdateSettings(e.Admin, admin.TenantSettings{DefaultRole: &bogus}); !errs.Is(err, errs.CodeInvalid) {
		t.Fatalf("bogus default role: %v", err)
	}
	tn := must[*types.Tenant](t)(a.UpdateSettings(e.Admin, admin.TenantSettings{DailyTokenBudget: &budget}))
	if tn.DailyTokenBudget != 1000 {
		t.Fatalf("budget %d", tn.DailyTokenBudget)
	}
	logs := audits(t, e, e.Admin, "tenant.settings")
	if len(logs) != 1 || !hasPath(logs[0].Detail["diff"], "daily_token_budget") {
		t.Fatalf("settings audit %+v", logs)
	}
}
