package feishuapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	orchfeishu "github.com/eternityx/lagent/internal/orch/feishu"
)

type fakeSvc struct{ bound []orchfeishu.BindInput }

func (f *fakeSvc) ListChats(context.Context) ([]*types.FeishuChat, error) { return nil, nil }
func (f *fakeSvc) BindChat(_ context.Context, in orchfeishu.BindInput) (*types.FeishuChat, error) {
	f.bound = append(f.bound, in)
	return &types.FeishuChat{ChatID: in.ChatID, DefaultRole: in.DefaultRole, Bound: true}, nil
}
func (f *fakeSvc) UnbindChat(_ context.Context, id string) (*types.FeishuChat, error) {
	return &types.FeishuChat{ChatID: id}, nil
}
func (f *fakeSvc) ListThreads(context.Context, string) ([]*types.FeishuThread, error) { return nil, nil }

func serve(role tenant.Role, svc Service, method, path, body string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			p := tenant.Principal{Kind: tenant.KindAPIKey, ID: "u", TenantID: "t1", Role: role}
			next.ServeHTTP(w, req.WithContext(tenant.With(req.Context(), p)))
		})
	})
	Routes(svc)(r)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
	return w
}

func TestRoutes(t *testing.T) {
	svc := &fakeSvc{}
	if w := serve(tenant.RoleMember, svc, "GET", "/feishu/chats", ""); w.Code != http.StatusForbidden {
		t.Fatalf("member: %d", w.Code)
	}
	w := serve(tenant.RoleTenantAdmin, svc, "POST", "/feishu/chats", `{"chat_id":"oc_1","name":"ops","default_role":"general"}`)
	if w.Code != http.StatusCreated || len(svc.bound) != 1 || svc.bound[0].DefaultRole != "general" {
		t.Fatalf("bind: %d %s", w.Code, w.Body)
	}
	if w := serve(tenant.RoleTenantAdmin, svc, "GET", "/feishu/threads?session_id=s1", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatalf("threads: %d %s", w.Code, w.Body)
	}
	if w := serve(tenant.RoleTenantAdmin, svc, "DELETE", "/feishu/chats/oc_1", ""); w.Code != 200 {
		t.Fatalf("unbind: %d", w.Code)
	}
}
