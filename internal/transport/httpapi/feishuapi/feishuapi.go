// Package feishuapi exposes the tenant-admin routes that bind Feishu groups to
// a tenant (§9.4) and inspect thread ↔ session bindings.
package feishuapi

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	orchfeishu "github.com/eternityx/lagent/internal/orch/feishu"
	"github.com/eternityx/lagent/internal/transport/httpapi"
)

// Service is the use-case surface the routes call (orch/feishu.Service).
type Service interface {
	ListChats(ctx context.Context) ([]*types.FeishuChat, error)
	BindChat(ctx context.Context, in orchfeishu.BindInput) (*types.FeishuChat, error)
	UnbindChat(ctx context.Context, idOrChat string) (*types.FeishuChat, error)
	ListThreads(ctx context.Context, sessionID string) ([]*types.FeishuThread, error)
}

// Routes returns a function for httpapi.Server.Mount.
func Routes(svc Service) func(r chi.Router) {
	h := &handler{svc: svc}
	return func(r chi.Router) {
		r.Route("/feishu", func(r chi.Router) {
			r.Get("/chats", httpapi.RequireRole(tenant.RoleTenantAdmin, h.listChats))
			r.Post("/chats", httpapi.RequireRole(tenant.RoleTenantAdmin, h.bindChat))
			r.Delete("/chats/{id}", httpapi.RequireRole(tenant.RoleTenantAdmin, h.unbindChat))
			r.Get("/threads", httpapi.RequireRole(tenant.RoleTenantAdmin, h.listThreads))
		})
	}
}

type handler struct{ svc Service }

type list[T any] struct {
	Items []*T `json:"items"`
}

func (h *handler) listChats(w http.ResponseWriter, r *http.Request) {
	cs, err := h.svc.ListChats(r.Context())
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	if cs == nil {
		cs = []*types.FeishuChat{}
	}
	httpapi.WriteJSON(w, http.StatusOK, list[types.FeishuChat]{Items: cs})
}

func (h *handler) bindChat(w http.ResponseWriter, r *http.Request) {
	var in orchfeishu.BindInput
	if err := httpapi.ReadJSON(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	c, err := h.svc.BindChat(r.Context(), in)
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusCreated, c)
}

func (h *handler) unbindChat(w http.ResponseWriter, r *http.Request) {
	c, err := h.svc.UnbindChat(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, c)
}

func (h *handler) listThreads(w http.ResponseWriter, r *http.Request) {
	ts, err := h.svc.ListThreads(r.Context(), r.URL.Query().Get("session_id"))
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	if ts == nil {
		ts = []*types.FeishuThread{}
	}
	httpapi.WriteJSON(w, http.StatusOK, list[types.FeishuThread]{Items: ts})
}
