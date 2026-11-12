package mcp_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/eternityx/lagent/internal/capability/mcp"
	"github.com/eternityx/lagent/internal/capability/mcp/mcptest"
	"github.com/eternityx/lagent/internal/capability/tool"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/repo"
	repomem "github.com/eternityx/lagent/internal/infra/repo/memory"
	secretlocal "github.com/eternityx/lagent/internal/infra/secret/local"
)

func tctx(tid string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{Kind: tenant.KindAPIKey, ID: "admin", TenantID: tid, Role: tenant.RoleTenantAdmin})
}

func TestToolName(t *testing.T) {
	if got := mcp.ToolName("my srv", "docs.search/v2"); got != "my_srv__docs_search_v2" {
		t.Fatalf("got %q", got)
	}
	long1 := mcp.ToolName(strings.Repeat("s", 40), strings.Repeat("t", 40)+"a")
	long2 := mcp.ToolName(strings.Repeat("s", 40), strings.Repeat("t", 40)+"b")
	if len(long1) > 64 || len(long2) > 64 || long1 == long2 {
		t.Fatalf("long names: %q %q", long1, long2)
	}
	for _, r := range long1 {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			t.Fatalf("bad rune %q in %q", r, long1)
		}
	}
}

func TestBaseRisk(t *testing.T) {
	srv := &types.MCPServer{Name: "docs", RiskOverrides: map[string]types.RiskLevel{"docs.search": types.RiskReadOnly, "x": "bogus"}}
	if r := mcp.BaseRisk(srv, "docs.search"); r != types.RiskReadOnly {
		t.Fatalf("explicit lowering: %s", r)
	}
	if r := mcp.BaseRisk(srv, "create_ticket"); r != types.RiskExternal {
		t.Fatalf("default: %s", r)
	}
	if r := mcp.BaseRisk(srv, "x"); r != types.RiskExternal {
		t.Fatalf("invalid override must not lower: %s", r)
	}
	m := mcp.MetaFor(srv, "create_ticket")
	if !m.SideEffect || m.ReadOnly || m.Source != types.SourceMCP || m.Server != "docs" {
		t.Fatalf("meta %+v", m)
	}
	m = mcp.MetaFor(srv, "docs.search")
	if m.SideEffect || !m.ReadOnly {
		t.Fatalf("meta %+v", m)
	}
}

type env struct {
	store *repo.Store
	fake  *mcptest.Fake
	m     *mcp.Manager
}

func newEnv(t *testing.T) *env {
	t.Helper()
	store := repomem.NewStore(nil)
	secrets, _ := secretlocal.New("", "k")
	f := mcptest.New()
	m := mcp.New(store, secrets, mcp.Options{Dial: f.Dial})
	t.Cleanup(m.Close)
	ctx := tctx("t1")
	if err := secrets.Put(ctx, "lagent/t1/mcp/docs", "tok-t1"); err != nil {
		t.Fatal(err)
	}
	srv := &types.MCPServer{Name: "docs", Transport: types.MCPStreamable, URL: "http://x", Enabled: true, SecretRef: "lagent/t1/mcp/docs",
		RiskOverrides: map[string]types.RiskLevel{"docs.search": types.RiskReadOnly}}
	if err := store.MCPServers.Insert(ctx, srv); err != nil {
		t.Fatal(err)
	}
	tools, err := m.ListTools(ctx, srv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Update(ctx, store.MCPServers, srv.ID, func(v *types.MCPServer) error { v.Tools = tools; return nil }); err != nil {
		t.Fatal(err)
	}
	m.Invalidate(ctx, "")
	return &env{store: store, fake: f, m: m}
}

func TestProviderPerTenant(t *testing.T) {
	e := newEnv(t)
	reg := tool.NewRegistry()
	reg.AddProvider(e.m.Provider())

	metas, err := reg.List(tctx("t1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 2 || metas[0].Name != "docs__create_ticket" || metas[1].Name != "docs__docs_search" {
		t.Fatalf("metas %+v", metas)
	}
	if metas[0].Risk != types.RiskExternal || metas[1].Risk != types.RiskReadOnly {
		t.Fatalf("risks %+v", metas)
	}
	other, err := reg.List(tctx("t2"))
	if err != nil || len(other) != 0 {
		t.Fatalf("tenant t2 sees %v (%v)", other, err)
	}

	tl, err := reg.Get(tctx("t1"), "docs__docs_search")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tl.Spec().Description, "[docs] ") || !strings.Contains(string(tl.Spec().InputSchema), "query") {
		t.Fatalf("spec %+v", tl.Spec())
	}
	res, err := tl.Call(tctx("t1"), []byte(`{"query":"q1"}`), tool.Env{})
	if err != nil || res.IsError {
		t.Fatalf("call: %v %+v", err, res)
	}
	if !strings.Contains(res.Content, `source="mcp:docs/docs.search"`) || !strings.Contains(res.Content, "results for\n") || !strings.Contains(res.Content, `"q1"`) {
		t.Fatalf("content %q", res.Content)
	}
	ticket, _ := reg.Get(tctx("t1"), "docs__create_ticket")
	res, _ = ticket.Call(tctx("t1"), nil, tool.Env{})
	if !res.IsError || !strings.Contains(res.Content, "title is required") {
		t.Fatalf("isError not propagated: %+v", res)
	}
	// Another tenant cannot use t1's server record even if it got hold of it.
	srv, _ := e.store.MCPServers.FindOne(tctx("t1"), repo.Filter{"name": "docs"})
	if _, err := e.m.CallTool(tctx("t2"), srv, "docs.search", nil); err == nil {
		t.Fatal("cross-tenant call allowed")
	}
	for _, s := range e.fake.Secrets() {
		if s != "tok-t1" {
			t.Fatalf("secret %q", s)
		}
	}
}

func TestReconnectAfterServerDrop(t *testing.T) {
	e := newEnv(t)
	ctx := tctx("t1")
	srv, _ := e.store.MCPServers.FindOne(ctx, repo.Filter{"name": "docs"})
	if _, err := e.m.ListTools(ctx, srv); err != nil {
		t.Fatal(err)
	}
	before := e.fake.Dials()
	e.fake.DropSessions()
	if _, err := e.m.ListTools(ctx, srv); err != nil {
		t.Fatalf("no reconnect: %v", err)
	}
	if e.fake.Dials() != before+1 {
		t.Fatalf("dials %d -> %d", before, e.fake.Dials())
	}
	// A configuration change reconnects too.
	srv.URL = "http://y"
	if _, err := e.m.CallTool(ctx, srv, "docs.search", []byte(`{"query":"a"}`)); err != nil {
		t.Fatal(err)
	}
	if e.fake.Dials() != before+2 {
		t.Fatalf("config change did not reconnect: %d", e.fake.Dials())
	}
}

func TestHTTPTransportsSendAuthHeader(t *testing.T) {
	f := mcptest.New()
	var mu sync.Mutex
	var seen []string
	record := func(h http.Handler, header string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen = append(seen, r.Header.Get(header))
			mu.Unlock()
			h.ServeHTTP(w, r)
		})
	}
	getServer := func(*http.Request) *sdk.Server { return f.Server }
	streamable := httptest.NewServer(record(sdk.NewStreamableHTTPHandler(getServer, nil), "Authorization"))
	defer streamable.Close()
	sse := httptest.NewServer(record(sdk.NewSSEHandler(getServer, nil), "X-Api-Key"))
	defer sse.Close()

	store := repomem.NewStore(nil)
	secrets, _ := secretlocal.New("", "k")
	ctx := tctx("t1")
	_ = secrets.Put(ctx, "lagent/t1/mcp/a", "s3cret")
	m := mcp.New(store, secrets, mcp.Options{})
	defer m.Close()

	cases := []struct {
		srv  *types.MCPServer
		want string
	}{
		{&types.MCPServer{Name: "a", Transport: types.MCPStreamable, URL: streamable.URL, SecretRef: "lagent/t1/mcp/a", Enabled: true}, "Bearer s3cret"},
		{&types.MCPServer{Name: "b", Transport: types.MCPSSE, URL: sse.URL, SecretRef: "lagent/t1/mcp/a", AuthHeader: "X-Api-Key", Enabled: true}, "s3cret"},
	}
	for _, c := range cases {
		mu.Lock()
		seen = nil
		mu.Unlock()
		tools, err := m.ListTools(ctx, c.srv)
		if err != nil {
			t.Fatalf("%s: %v", c.srv.Transport, err)
		}
		if len(tools) != 2 {
			t.Fatalf("%s: tools %v", c.srv.Transport, tools)
		}
		res, err := m.CallTool(ctx, c.srv, "docs.search", []byte(`{"query":"x"}`))
		if err != nil || !strings.Contains(res.Text, "results for") {
			t.Fatalf("%s: call %v %+v", c.srv.Transport, err, res)
		}
		mu.Lock()
		if len(seen) == 0 {
			t.Fatalf("%s: no requests", c.srv.Transport)
		}
		for _, h := range seen {
			if h != c.want {
				t.Fatalf("%s: header %q want %q", c.srv.Transport, h, c.want)
			}
		}
		mu.Unlock()
	}
	// Another tenant cannot read t1's secret, so it cannot connect with it.
	other := &types.MCPServer{Name: "a", Transport: types.MCPStreamable, URL: streamable.URL, SecretRef: "lagent/t1/mcp/a", Enabled: true}
	if _, err := m.ListTools(tctx("t2"), other); err == nil {
		t.Fatal("t2 used t1's secret")
	}
}

func TestAuthHeader(t *testing.T) {
	for _, c := range []struct{ conf, name, value string }{
		{"", "Authorization", "Bearer s"},
		{"Authorization: Token", "Authorization", "Token s"},
		{"X-Key", "X-Key", "s"},
		{"X-Key:", "X-Key", "s"},
	} {
		n, v := mcp.AuthHeader(c.conf, "s")
		if n != c.name || v != c.value {
			t.Fatalf("%q: %q %q", c.conf, n, v)
		}
	}
}
