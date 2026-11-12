package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eternityx/lagent/internal/capability/mcp"
	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/orch/integration"
	"github.com/eternityx/lagent/internal/rules/permission"
)

func bootstrapApp(t *testing.T, file string) *App {
	t.Helper()
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg.LLM.Default = "fake/echo"
	cfg.Object.Root = filepath.Join(dir, "objects")
	cfg.Sandbox.Root = filepath.Join(dir, "workspaces")
	cfg.BootstrapFile = file
	a, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, close := range a.Infra.Closers {
			_ = close(context.Background())
		}
	})
	return a
}

func writeBootstrap(t *testing.T, data string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "bootstrap.json")
	if err := os.WriteFile(file, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

const webBootstrap = `{
 "mcp_servers":[{"name":"bing","transport":"http","url":"https://search.example.com/mcp","risk_overrides":{"bing_search":"read_only","crawl_webpage":"read_only"}}],
 "retrieval_sources":[{"name":"web","server":"bing","tool":"bing_search","query_field":"query","fixed_args":{"count":5}}],
 "roles":[{"key":"general","name":"Web assistant","prompt":"Search live information; never use commands for web lookup.","tools":["search","list_sources","mcp:bing"],"default_permission_mode":"ask_external"}]
}`

func TestBootstrapRestoresMemoryStoreAndReadOnlySearch(t *testing.T) {
	file := writeBootstrap(t, webBootstrap)
	// Two independent process builds must restore the same local configuration.
	for i := 0; i < 2; i++ {
		a := bootstrapApp(t, file)
		ctx := tenant.SystemContext(context.Background(), "default")
		server, err := a.Infra.Store.MCPServers.FindOne(ctx, repo.Filter{"name": "bing"})
		if err != nil {
			t.Fatal(err)
		}
		if server.Transport != types.MCPStreamable || !server.Enabled {
			t.Fatalf("server: %+v", server)
		}
		if server.SyncedAt != nil {
			t.Fatal("bootstrap must not dial the network before workers start")
		}
		for _, tool := range []string{"bing_search", "crawl_webpage"} {
			risk := mcp.BaseRisk(server, tool)
			decision, _ := permission.Decide(permission.Input{Tool: tool, Risk: risk, Mode: types.ModeAskAll})
			if risk != types.RiskReadOnly || decision != permission.Allow {
				t.Fatalf("%s should be read-only and require no approval", tool)
			}
		}
		if risk := mcp.BaseRisk(server, "unknown_tool"); risk != types.RiskExternal {
			t.Fatal("unknown tools must still require approval")
		}
		src, err := a.Infra.Store.Retrievals.FindOne(ctx, repo.Filter{"name": "web"})
		if err != nil {
			t.Fatal(err)
		}
		var args map[string]int
		if err := json.Unmarshal(src.FixedArgs, &args); err != nil {
			t.Fatal(err)
		}
		if src.Server != "bing" || src.Tool != "bing_search" || src.QueryField != "query" || args["count"] != 5 {
			t.Fatalf("source: %+v", src)
		}
		role, err := repo.LatestPublished(ctx, a.Infra.Store.Roles, "general")
		if err != nil {
			t.Fatal(err)
		}
		if role.TenantID != "default" || role.Name != "Web assistant" || role.Ver != 2 {
			t.Fatalf("role: %+v", role)
		}
		other := tenant.SystemContext(context.Background(), "other")
		if _, err := a.Infra.Store.MCPServers.FindOne(other, repo.Filter{"name": "bing"}); !errs.Is(err, errs.CodeNotFound) {
			t.Fatalf("bootstrap leaked into another tenant: %v", err)
		}
	}
}

func TestBootstrapKeepsExistingTenantEdits(t *testing.T) {
	a := bootstrapApp(t, writeBootstrap(t, webBootstrap))
	ctx := tenant.SystemContext(context.Background(), "default")
	servers, _ := a.Integrations.ListServers(ctx)
	disabled := false
	_, err := a.Integrations.UpdateServer(ctx, servers[0].ID, integration.ServerInput{Name: "bing", Transport: types.MCPStreamable, URL: "https://edited.example.com/mcp", Enabled: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	src, _ := a.Infra.Store.Retrievals.FindOne(ctx, repo.Filter{"name": "web"})
	_, err = a.Integrations.UpdateSource(ctx, src.ID, integration.SourceInput{Name: "web", Description: "admin edit", Server: "bing", Tool: "bing_search"})
	if err != nil {
		t.Fatal(err)
	}
	role, _ := repo.LatestPublished(ctx, a.Infra.Store.Roles, "general")
	role.Name = "Tenant edit"
	if _, err := a.Admin.Roles().SaveDraft(ctx, "general", role); err != nil {
		t.Fatal(err)
	}
	if err := a.bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	servers, _ = a.Integrations.ListServers(ctx)
	if len(servers) != 1 || servers[0].Enabled || servers[0].URL != "https://edited.example.com/mcp" {
		t.Fatalf("server overwritten: %+v", servers)
	}
	src, _ = a.Infra.Store.Retrievals.Get(ctx, src.ID)
	if src.Description != "admin edit" {
		t.Fatal("source overwritten")
	}
	detail, err := a.Admin.Roles().Get(ctx, "general")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Draft == nil || detail.Draft.Name != "Tenant edit" || detail.EffectiveVer != 2 {
		t.Fatalf("role edits overwritten: %+v", detail)
	}
}

func TestBootstrapValidation(t *testing.T) {
	for _, tt := range []struct{ name, data, want string }{
		{"unknown field", `{"mcpServers":{}}`, "unknown field"},
		{"null", `null`, "not null"},
		{"trailing JSON", `{} {}`, "exactly one"},
		{"invalid transport", `{"mcp_servers":[{"name":"bing","transport":"streamable_http","url":"https://example.com"}]}`, "transport must"},
		{"missing server", `{"retrieval_sources":[{"name":"web","server":"missing","tool":"search"}]}`, "does not exist"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := bootstrapApp(t, "")
			a.Cfg.BootstrapFile = writeBootstrap(t, tt.data)
			err := a.bootstrap(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want %q", err, tt.want)
			}
		})
	}
}
