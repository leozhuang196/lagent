package retrieval_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/eternityx/lagent/internal/capability/mcp"
	"github.com/eternityx/lagent/internal/capability/mcp/mcptest"
	"github.com/eternityx/lagent/internal/capability/retrieval"
	"github.com/eternityx/lagent/internal/capability/tool"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	repomem "github.com/eternityx/lagent/internal/infra/repo/memory"
	secretlocal "github.com/eternityx/lagent/internal/infra/secret/local"
)

func TestMissingSources(t *testing.T) {
	def := &types.WorkflowDef{Steps: []types.StepDef{
		{ID: "a", Sources: []string{"kb", "crm"}},
		{ID: "b", Sources: []string{"crm", "web", ""}},
		{ID: "c"},
	}}
	got := retrieval.MissingSources(def, map[string]bool{"kb": true, "web": false})
	if !reflect.DeepEqual(got, []string{"crm", "web"}) {
		t.Fatalf("got %v", got)
	}
	if got := retrieval.MissingSources(def, map[string]bool{"kb": true, "crm": true, "web": true}); len(got) != 0 {
		t.Fatalf("all available: %v", got)
	}
	if retrieval.MissingSources(nil, nil) != nil {
		t.Fatal("nil def")
	}
}

func TestBuildArgs(t *testing.T) {
	src := &types.RetrievalSource{Name: "kb", FixedArgs: json.RawMessage(`{"index":"handbook","q":"fixed"}`), QueryField: "q"}
	schema := json.RawMessage(`{"type":"object","properties":{"q":{},"limit":{}}}`)
	raw, err := retrieval.BuildArgs(src, "vacation", 5, schema)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if m["index"] != "handbook" || m["q"] != "vacation" || m["limit"] != float64(5) {
		t.Fatalf("args %v", m)
	}
	raw, _ = retrieval.BuildArgs(src, "x", 5, json.RawMessage(`{"type":"object"}`))
	if strings.Contains(string(raw), "limit") {
		t.Fatalf("limit sent to a tool without it: %s", raw)
	}
	if _, err := retrieval.BuildArgs(&types.RetrievalSource{FixedArgs: json.RawMessage(`[1]`)}, "x", 0, nil); err == nil {
		t.Fatal("non-object fixed args accepted")
	}
}

func TestSearchTool(t *testing.T) {
	store := repomem.NewStore(nil)
	secrets, _ := secretlocal.New("", "k")
	f := mcptest.New()
	m := mcp.New(store, secrets, mcp.Options{Dial: f.Dial})
	defer m.Close()
	ctx := tenant.With(context.Background(), tenant.Principal{Kind: tenant.KindAPIKey, ID: "u", TenantID: "t1", Role: tenant.RoleMember})

	srv := &types.MCPServer{Name: "docs", Transport: types.MCPStreamable, URL: "http://x", Enabled: true}
	_ = store.MCPServers.Insert(ctx, srv)
	srv.Tools, _ = m.ListTools(ctx, srv)
	_ = store.MCPServers.Replace(ctx, srv)
	_ = store.Retrievals.Insert(ctx, &types.RetrievalSource{Name: "handbook", Description: "Company handbook", Server: "docs", Tool: "docs.search",
		FixedArgs: json.RawMessage(`{"index":"hb"}`)})

	tools := retrieval.Tools(store, m)
	search, list := tools[0], tools[1]
	if search.Meta().Risk != types.RiskReadOnly || !search.Meta().ReadOnly || list.Meta().Risk != types.RiskReadOnly {
		t.Fatal("retrieval tools must be read-only")
	}
	res, err := search.Call(ctx, json.RawMessage(`{"source":"handbook","query":"leave policy","limit":3}`), tool.Env{})
	if err != nil || res.IsError {
		t.Fatalf("search: %v %+v", err, res)
	}
	if !strings.Contains(res.Content, `source="retrieval:handbook"`) {
		t.Fatalf("not wrapped: %q", res.Content)
	}
	calls := f.Calls()
	last := calls[len(calls)-1]
	if last.Tool != "docs.search" || last.Args["index"] != "hb" || last.Args["query"] != "leave policy" || last.Args["limit"] != float64(3) {
		t.Fatalf("call %+v", last)
	}

	res, _ = search.Call(ctx, json.RawMessage(`{"source":"nope","query":"x"}`), tool.Env{})
	if !res.IsError || !strings.Contains(res.Content, "not found") {
		t.Fatalf("unknown source: %+v", res)
	}
	// Other tenants do not see t1's sources.
	other := tenant.With(context.Background(), tenant.Principal{Kind: tenant.KindAPIKey, ID: "u", TenantID: "t2"})
	res, _ = search.Call(other, json.RawMessage(`{"source":"handbook","query":"x"}`), tool.Env{})
	if !res.IsError {
		t.Fatalf("t2 searched t1's source: %+v", res)
	}

	res, _ = list.Call(ctx, nil, tool.Env{})
	if !strings.Contains(res.Content, "- handbook: Company handbook") {
		t.Fatalf("list %q", res.Content)
	}
}
