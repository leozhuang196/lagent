package integration_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/eternityx/lagent/internal/capability/mcp"
	"github.com/eternityx/lagent/internal/capability/mcp/mcptest"
	"github.com/eternityx/lagent/internal/capability/retrieval"
	"github.com/eternityx/lagent/internal/capability/tool"
	"github.com/eternityx/lagent/internal/core/tenant"
	repomem "github.com/eternityx/lagent/internal/infra/repo/memory"
	secretlocal "github.com/eternityx/lagent/internal/infra/secret/local"
	orchint "github.com/eternityx/lagent/internal/orch/integration"
	httpint "github.com/eternityx/lagent/internal/transport/httpapi/integration"
)

const secretValue = "sk-http-secret-123"

// server mounts the routes behind a stub authenticator selecting the role
// from the X-Role header.
func server(t *testing.T) *httptest.Server {
	t.Helper()
	store := repomem.NewStore(nil)
	secrets, _ := secretlocal.New("", "k")
	f := mcptest.New()
	m := mcp.New(store, secrets, mcp.Options{Dial: f.Dial})
	t.Cleanup(m.Close)
	reg := tool.NewRegistry()
	reg.AddProvider(m.Provider())
	reg.Register(retrieval.Tools(store, m)...)
	svc := &orchint.Service{Store: store, Secrets: secrets, MCP: m, Registry: reg}

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			p := tenant.Principal{Kind: tenant.KindAPIKey, ID: "k1", TenantID: "t1", Role: tenant.Role(req.Header.Get("X-Role"))}
			next.ServeHTTP(w, req.WithContext(tenant.With(req.Context(), p)))
		})
	})
	(&httpint.Handler{Svc: svc}).Mount(r)
	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)
	return ts
}

func call(t *testing.T, ts *httptest.Server, role, method, path string, body any) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, ts.URL+path, rd)
	req.Header.Set("X-Role", role)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(b), secretValue) {
		t.Fatalf("%s %s leaked the secret: %s", method, path, b)
	}
	return resp.StatusCode, string(b)
}

func TestRoutes(t *testing.T) {
	ts := server(t)
	const admin, member = "tenant_admin", "member"
	create := map[string]any{"name": "docs", "transport": "http", "url": "https://mcp.example.com/mcp", "secret": secretValue,
		"risk_overrides": map[string]string{"docs.search": "read_only"}}

	if code, _ := call(t, ts, member, "POST", "/mcp-servers", create); code != http.StatusForbidden {
		t.Fatalf("member create: %d", code)
	}
	code, body := call(t, ts, admin, "POST", "/mcp-servers", create)
	if code != http.StatusCreated || !strings.Contains(body, `"has_secret":true`) || strings.Contains(body, "secret_ref") {
		t.Fatalf("create: %d %s", code, body)
	}
	var srv struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(body), &srv)

	if code, body = call(t, ts, admin, "POST", "/mcp-servers/"+srv.ID+"/sync", nil); code != 200 || !strings.Contains(body, `"status":"ok"`) {
		t.Fatalf("sync: %d %s", code, body)
	}
	if code, body = call(t, ts, admin, "POST", "/mcp-servers/"+srv.ID+"/test", nil); code != 200 || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("test: %d %s", code, body)
	}
	if code, body = call(t, ts, member, "GET", "/mcp-servers", nil); code != 200 || !strings.Contains(body, `"registry_name":"docs__docs_search"`) {
		t.Fatalf("list: %d %s", code, body)
	}
	call(t, ts, member, "GET", "/mcp-servers/"+srv.ID, nil)
	if code, body = call(t, ts, admin, "PUT", "/mcp-servers/"+srv.ID, map[string]any{"transport": "http", "url": "https://mcp.example.com/v2"}); code != 200 || !strings.Contains(body, `"has_secret":true`) {
		t.Fatalf("put: %d %s", code, body)
	}

	code, body = call(t, ts, admin, "POST", "/retrieval-sources", map[string]any{"name": "handbook", "server": "docs", "tool": "docs.search", "fixed_args": map[string]any{"index": "hb"}})
	if code != http.StatusCreated {
		t.Fatalf("create source: %d %s", code, body)
	}
	var src struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(body), &src)
	if code, body = call(t, ts, admin, "POST", "/retrieval-sources/"+src.ID+"/try", map[string]any{"query": "leave"}); code != 200 || !strings.Contains(body, `\"index\":\"hb\"`) {
		t.Fatalf("try: %d %s", code, body)
	}
	if code, body = call(t, ts, member, "GET", "/retrieval-sources", nil); code != 200 || !strings.Contains(body, `"handbook"`) {
		t.Fatalf("list sources: %d %s", code, body)
	}

	if code, body = call(t, ts, member, "GET", "/tools", nil); code != 200 || !strings.Contains(body, `"effective_risk"`) || !strings.Contains(body, `"search"`) {
		t.Fatalf("catalog: %d %s", code, body)
	}
	if code, body = call(t, ts, admin, "POST", "/risk-overrides", map[string]any{"tool": "docs__create_ticket", "level": "read_only"}); code != http.StatusUnprocessableEntity {
		t.Fatalf("lowering: %d %s", code, body)
	}
	code, body = call(t, ts, admin, "POST", "/risk-overrides", map[string]any{"tool": "docs__docs_search", "level": "external"})
	if code != 200 {
		t.Fatalf("raise: %d %s", code, body)
	}
	var ov struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(body), &ov)
	if code, _ = call(t, ts, admin, "DELETE", "/risk-overrides/"+ov.ID, nil); code != http.StatusNoContent {
		t.Fatalf("delete override: %d", code)
	}

	if code, body = call(t, ts, admin, "POST", "/tools/docs__create_ticket/test", map[string]any{"input": map[string]any{"title": "x"}}); code != http.StatusConflict {
		t.Fatalf("unconfirmed: %d %s", code, body)
	}
	if code, body = call(t, ts, admin, "POST", "/tools/docs__create_ticket/test", map[string]any{"input": map[string]any{"title": "x"}, "confirm": true}); code != 200 || !strings.Contains(body, "created ticket") {
		t.Fatalf("confirmed: %d %s", code, body)
	}
	if code, body = call(t, ts, member, "GET", "/templates/availability", nil); code != 200 || !strings.Contains(body, `"items":[]`) {
		t.Fatalf("availability: %d %s", code, body)
	}

	if code, _ = call(t, ts, admin, "DELETE", "/retrieval-sources/"+src.ID, nil); code != http.StatusNoContent {
		t.Fatalf("delete source: %d", code)
	}
	if code, _ = call(t, ts, admin, "DELETE", "/mcp-servers/"+srv.ID, nil); code != http.StatusNoContent {
		t.Fatalf("delete server: %d", code)
	}
	if code, _ = call(t, ts, admin, "GET", "/mcp-servers/"+srv.ID, nil); code != http.StatusNotFound {
		t.Fatalf("get deleted: %d", code)
	}
}
