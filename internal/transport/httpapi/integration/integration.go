// Package integration exposes the integration use cases (MCP servers,
// retrieval sources, tool catalog, risk overrides, tool tester, template
// availability) under the authenticated /v1 API. Reads are open to members;
// every write needs a tenant admin.
package integration

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/orch/integration"
	"github.com/eternityx/lagent/internal/transport/httpapi"
)

// Handler serves the routes.
type Handler struct {
	Svc *integration.Service
}

// Mount registers the routes; pass it to httpapi.Server.Mount.
func (h *Handler) Mount(r chi.Router) {
	admin := func(fn http.HandlerFunc) http.HandlerFunc { return httpapi.RequireRole(tenant.RoleTenantAdmin, fn) }

	r.Get("/mcp-servers", h.listServers)
	r.Post("/mcp-servers", admin(h.createServer))
	r.Get("/mcp-servers/{id}", h.getServer)
	r.Put("/mcp-servers/{id}", admin(h.updateServer))
	r.Delete("/mcp-servers/{id}", admin(h.deleteServer))
	r.Post("/mcp-servers/{id}/sync", admin(h.syncServer))
	r.Post("/mcp-servers/{id}/test", admin(h.testServer))

	r.Get("/retrieval-sources", h.listSources)
	r.Post("/retrieval-sources", admin(h.createSource))
	r.Get("/retrieval-sources/{id}", h.getSource)
	r.Put("/retrieval-sources/{id}", admin(h.updateSource))
	r.Delete("/retrieval-sources/{id}", admin(h.deleteSource))
	r.Post("/retrieval-sources/{id}/try", admin(h.trySource))

	r.Get("/tools", h.catalog)
	r.Post("/tools/{name}/test", admin(h.testTool))

	r.Get("/risk-overrides", h.listOverrides)
	r.Post("/risk-overrides", admin(h.setOverride))
	r.Delete("/risk-overrides/{id}", admin(h.deleteOverride))

	r.Get("/templates/availability", h.availability)
}

type items[T any] struct {
	Items []T `json:"items"`
}

func reply[T any](w http.ResponseWriter, status int, v T, err error) {
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	httpapi.WriteJSON(w, status, v)
}

func list[T any](w http.ResponseWriter, v []T, err error) {
	if v == nil {
		v = []T{}
	}
	reply(w, http.StatusOK, items[T]{Items: v}, err)
}

func noContent(w http.ResponseWriter, err error) {
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- MCP servers

func (h *Handler) listServers(w http.ResponseWriter, r *http.Request) {
	v, err := h.Svc.ListServers(r.Context())
	list(w, v, err)
}

func (h *Handler) getServer(w http.ResponseWriter, r *http.Request) {
	v, err := h.Svc.GetServer(r.Context(), chi.URLParam(r, "id"))
	reply(w, http.StatusOK, v, err)
}

func (h *Handler) createServer(w http.ResponseWriter, r *http.Request) {
	var in integration.ServerInput
	if err := httpapi.ReadJSON(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	v, err := h.Svc.CreateServer(r.Context(), in)
	reply(w, http.StatusCreated, v, err)
}

func (h *Handler) updateServer(w http.ResponseWriter, r *http.Request) {
	var in integration.ServerInput
	if err := httpapi.ReadJSON(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	v, err := h.Svc.UpdateServer(r.Context(), chi.URLParam(r, "id"), in)
	reply(w, http.StatusOK, v, err)
}

func (h *Handler) deleteServer(w http.ResponseWriter, r *http.Request) {
	noContent(w, h.Svc.DeleteServer(r.Context(), chi.URLParam(r, "id")))
}

func (h *Handler) syncServer(w http.ResponseWriter, r *http.Request) {
	v, err := h.Svc.SyncServer(r.Context(), chi.URLParam(r, "id"))
	reply(w, http.StatusOK, v, err)
}

func (h *Handler) testServer(w http.ResponseWriter, r *http.Request) {
	v, err := h.Svc.TestServer(r.Context(), chi.URLParam(r, "id"))
	reply(w, http.StatusOK, v, err)
}

// --- retrieval sources

func (h *Handler) listSources(w http.ResponseWriter, r *http.Request) {
	v, err := h.Svc.ListSources(r.Context())
	list(w, v, err)
}

func (h *Handler) getSource(w http.ResponseWriter, r *http.Request) {
	v, err := h.Svc.GetSource(r.Context(), chi.URLParam(r, "id"))
	reply(w, http.StatusOK, v, err)
}

func (h *Handler) createSource(w http.ResponseWriter, r *http.Request) {
	var in integration.SourceInput
	if err := httpapi.ReadJSON(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	v, err := h.Svc.CreateSource(r.Context(), in)
	reply(w, http.StatusCreated, v, err)
}

func (h *Handler) updateSource(w http.ResponseWriter, r *http.Request) {
	var in integration.SourceInput
	if err := httpapi.ReadJSON(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	v, err := h.Svc.UpdateSource(r.Context(), chi.URLParam(r, "id"), in)
	reply(w, http.StatusOK, v, err)
}

func (h *Handler) deleteSource(w http.ResponseWriter, r *http.Request) {
	noContent(w, h.Svc.DeleteSource(r.Context(), chi.URLParam(r, "id")))
}

func (h *Handler) trySource(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Query string `json:"query"`
		Limit int    `json:"limit,omitempty"`
	}
	if err := httpapi.ReadJSON(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	v, err := h.Svc.TrySource(r.Context(), chi.URLParam(r, "id"), in.Query, in.Limit)
	reply(w, http.StatusOK, v, err)
}

// --- tools and risk overrides

func (h *Handler) catalog(w http.ResponseWriter, r *http.Request) {
	v, err := h.Svc.ToolCatalog(r.Context())
	list(w, v, err)
}

func (h *Handler) testTool(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Input   json.RawMessage `json:"input,omitempty"`
		Confirm bool            `json:"confirm,omitempty"`
	}
	if err := httpapi.ReadJSON(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	v, err := h.Svc.TestTool(r.Context(), chi.URLParam(r, "name"), in.Input, in.Confirm)
	reply(w, http.StatusOK, v, err)
}

func (h *Handler) listOverrides(w http.ResponseWriter, r *http.Request) {
	v, err := h.Svc.ListRiskOverrides(r.Context())
	list(w, v, err)
}

func (h *Handler) setOverride(w http.ResponseWriter, r *http.Request) {
	var in integration.RiskOverrideInput
	if err := httpapi.ReadJSON(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	v, err := h.Svc.SetRiskOverride(r.Context(), in)
	reply(w, http.StatusOK, v, err)
}

func (h *Handler) deleteOverride(w http.ResponseWriter, r *http.Request) {
	noContent(w, h.Svc.DeleteRiskOverride(r.Context(), chi.URLParam(r, "id")))
}

// --- templates

func (h *Handler) availability(w http.ResponseWriter, r *http.Request) {
	v, err := h.Svc.TemplateAvailability(r.Context())
	list(w, v, err)
}
