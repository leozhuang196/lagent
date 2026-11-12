// Package adminapi exposes the administration console API (§11, §12) under
// the authenticated /v1 router. It only translates HTTP: authorization
// decisions live in orch/admin; RequireRole here is a first, cheap gate.
package adminapi

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/orch/admin"
	"github.com/eternityx/lagent/internal/transport/httpapi"
)

// Mount registers every admin route on r (the authenticated /v1 router).
func Mount(r chi.Router, a *admin.Service) {
	h := &handler{a: a}
	mountAssets(r, "/roles", a.Roles, true)
	mountAssets(r, "/skills", a.Skills, true)
	mountAssets(r, "/workflow-defs", a.WorkflowDefs, false)

	adminOnly := func(fn http.HandlerFunc) http.HandlerFunc { return httpapi.RequireRole(tenant.RoleTenantAdmin, fn) }
	platform := func(fn http.HandlerFunc) http.HandlerFunc { return httpapi.RequireRole(tenant.RolePlatformAdmin, fn) }

	r.Get("/memory/spaces", adminOnly(h.listSpaces))
	r.Post("/memory/spaces", adminOnly(h.createSpace))
	r.Get("/memory/spaces/{name}", adminOnly(h.getSpace))
	r.Put("/memory/spaces/{name}", adminOnly(h.updateSpace))
	r.Delete("/memory/spaces/{name}", adminOnly(h.deleteSpace))
	r.Get("/memory/spaces/{name}/entries", adminOnly(h.listEntries))
	r.Post("/memory/spaces/{name}/entries", adminOnly(h.putEntry))
	r.Get("/memory/spaces/{name}/entries/{key}", adminOnly(h.getEntry))
	r.Put("/memory/spaces/{name}/entries/{key}", adminOnly(h.putEntry))
	r.Delete("/memory/spaces/{name}/entries/{key}", adminOnly(h.deleteEntry))

	r.Get("/tenant", adminOnly(h.getTenant))
	r.Put("/tenant", adminOnly(h.updateTenant))
	r.Get("/keys", adminOnly(h.listKeys))
	r.Post("/keys", adminOnly(h.createKey))
	r.Post("/keys/{id}/disable", adminOnly(h.setKey(true)))
	r.Post("/keys/{id}/enable", adminOnly(h.setKey(false)))
	r.Get("/usage", adminOnly(h.usage))
	r.Get("/audit", adminOnly(h.audit))

	r.Get("/debug/turns/{id}", adminOnly(h.timeline))
	r.Get("/debug/turns/{id}/llm-calls", adminOnly(h.llmCalls))
	r.Post("/debug/assembly", adminOnly(h.assembly))
	r.Post("/debug/sessions/{id}/fork", adminOnly(h.fork))
	r.Post("/debug/playground", adminOnly(h.playground))

	r.Get("/platform/tenants", platform(h.listTenants))
	r.Post("/platform/tenants", platform(h.createTenant))
	r.Get("/platform/status", platform(h.status))
	r.Get("/platform/sessions/{id}", platform(h.breakGlassSession))
	r.Get("/platform/turns/{id}", platform(h.breakGlassTurn))
}

type handler struct{ a *admin.Service }

func reply(w http.ResponseWriter, status int, v any, err error) {
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	if v == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	httpapi.WriteJSON(w, status, v)
}

// param returns a path parameter, unescaped (keys may contain %-encoded bytes).
func param(r *http.Request, name string) string {
	v := chi.URLParam(r, name)
	if u, err := url.PathUnescape(v); err == nil {
		return u
	}
	return v
}

func intParam(r *http.Request, name string) (int, error) {
	n, err := strconv.Atoi(param(r, name))
	if err != nil {
		return 0, errs.Invalid("%s must be an integer", name)
	}
	return n, nil
}

// verQuery parses a version query value; "draft" is accepted for 0.
func verQuery(r *http.Request, name string) (int, error) {
	v := r.URL.Query().Get(name)
	if v == "draft" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, errs.Invalid("query %s must be a version number or 'draft'", name)
	}
	return n, nil
}

// --- versioned assets ---

func mountAssets[E any](r chi.Router, path string, get func() *admin.Assets[E], memberRead bool) {
	read := func(fn http.HandlerFunc) http.HandlerFunc {
		if memberRead {
			return httpapi.RequireRole(tenant.RoleMember, fn)
		}
		return httpapi.RequireRole(tenant.RoleTenantAdmin, fn)
	}
	write := func(fn http.HandlerFunc) http.HandlerFunc { return httpapi.RequireRole(tenant.RoleTenantAdmin, fn) }
	r.Route(path, func(r chi.Router) {
		r.Get("/", read(func(w http.ResponseWriter, r *http.Request) {
			v, err := get().List(r.Context())
			reply(w, http.StatusOK, map[string]any{"items": v}, err)
		}))
		r.Get("/{key}", read(func(w http.ResponseWriter, r *http.Request) {
			v, err := get().Get(r.Context(), param(r, "key"))
			reply(w, http.StatusOK, v, err)
		}))
		r.Get("/{key}/versions/{ver}", read(func(w http.ResponseWriter, r *http.Request) {
			ver, err := intParam(r, "ver")
			if err != nil {
				httpapi.WriteError(w, err)
				return
			}
			v, err := get().GetVersion(r.Context(), param(r, "key"), ver)
			reply(w, http.StatusOK, v, err)
		}))
		r.Get("/{key}/diff", read(func(w http.ResponseWriter, r *http.Request) {
			va, err := verQuery(r, "a")
			if err != nil {
				httpapi.WriteError(w, err)
				return
			}
			vb, err := verQuery(r, "b")
			if err != nil {
				httpapi.WriteError(w, err)
				return
			}
			ch, err := get().DiffVersions(r.Context(), param(r, "key"), va, vb)
			reply(w, http.StatusOK, map[string]any{"a": va, "b": vb, "changes": ch}, err)
		}))
		r.Put("/{key}/draft", write(func(w http.ResponseWriter, r *http.Request) {
			var body E
			if err := httpapi.ReadJSON(r, &body); err != nil {
				httpapi.WriteError(w, err)
				return
			}
			v, err := get().SaveDraft(r.Context(), param(r, "key"), &body)
			reply(w, http.StatusOK, v, err)
		}))
		r.Delete("/{key}/draft", write(func(w http.ResponseWriter, r *http.Request) {
			reply(w, 0, nil, get().DeleteDraft(r.Context(), param(r, "key")))
		}))
		r.Post("/{key}/publish", write(func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Note string `json:"note"`
			}
			if err := httpapi.ReadJSON(r, &body); err != nil {
				httpapi.WriteError(w, err)
				return
			}
			v, err := get().Publish(r.Context(), param(r, "key"), body.Note)
			reply(w, http.StatusCreated, v, err)
		}))
		r.Post("/{key}/rollback", write(func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Ver  int    `json:"ver"`
				Note string `json:"note"`
			}
			if err := httpapi.ReadJSON(r, &body); err != nil {
				httpapi.WriteError(w, err)
				return
			}
			v, err := get().Rollback(r.Context(), param(r, "key"), body.Ver, body.Note)
			reply(w, http.StatusCreated, v, err)
		}))
		for _, action := range []string{"disable", "enable"} {
			disabled := action == "disable"
			r.Post("/{key}/versions/{ver}/"+action, write(func(w http.ResponseWriter, r *http.Request) {
				ver, err := intParam(r, "ver")
				if err != nil {
					httpapi.WriteError(w, err)
					return
				}
				v, err := get().SetDisabled(r.Context(), param(r, "key"), ver, disabled)
				reply(w, http.StatusOK, v, err)
			}))
		}
	})
}

// --- memory ---

func (h *handler) listSpaces(w http.ResponseWriter, r *http.Request) {
	v, err := h.a.ListSpaces(r.Context())
	reply(w, http.StatusOK, map[string]any{"items": v}, err)
}

func (h *handler) createSpace(w http.ResponseWriter, r *http.Request) {
	var in admin.SpaceInput
	if err := httpapi.ReadJSON(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	v, err := h.a.CreateSpace(r.Context(), in)
	reply(w, http.StatusCreated, v, err)
}

func (h *handler) getSpace(w http.ResponseWriter, r *http.Request) {
	v, err := h.a.GetSpace(r.Context(), param(r, "name"))
	reply(w, http.StatusOK, v, err)
}

func (h *handler) updateSpace(w http.ResponseWriter, r *http.Request) {
	var in admin.SpaceInput
	if err := httpapi.ReadJSON(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	v, err := h.a.UpdateSpace(r.Context(), param(r, "name"), in)
	reply(w, http.StatusOK, v, err)
}

func (h *handler) deleteSpace(w http.ResponseWriter, r *http.Request) {
	reply(w, 0, nil, h.a.DeleteSpace(r.Context(), param(r, "name")))
}

func (h *handler) listEntries(w http.ResponseWriter, r *http.Request) {
	after, limit := httpapi.Page(r)
	v, err := h.a.ListEntries(r.Context(), param(r, "name"), admin.EntryQuery{Prefix: r.URL.Query().Get("prefix"), After: after, Limit: limit})
	reply(w, http.StatusOK, v, err)
}

func (h *handler) getEntry(w http.ResponseWriter, r *http.Request) {
	v, err := h.a.GetEntry(r.Context(), param(r, "name"), param(r, "key"))
	reply(w, http.StatusOK, v, err)
}

// putEntry serves POST .../entries (key in body) and PUT .../entries/{key}.
func (h *handler) putEntry(w http.ResponseWriter, r *http.Request) {
	var in admin.EntryInput
	if err := httpapi.ReadJSON(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	if k := param(r, "key"); k != "" {
		if in.Key != "" && in.Key != k {
			httpapi.WriteError(w, errs.Invalid("key in body differs from path"))
			return
		}
		in.Key = k
	}
	v, err := h.a.PutEntry(r.Context(), param(r, "name"), in)
	reply(w, http.StatusOK, v, err)
}

func (h *handler) deleteEntry(w http.ResponseWriter, r *http.Request) {
	reply(w, 0, nil, h.a.DeleteEntry(r.Context(), param(r, "name"), param(r, "key")))
}

// --- tenant, keys, usage, audit ---

func (h *handler) getTenant(w http.ResponseWriter, r *http.Request) {
	v, err := h.a.GetOwnTenant(r.Context())
	reply(w, http.StatusOK, v, err)
}

func (h *handler) updateTenant(w http.ResponseWriter, r *http.Request) {
	var in admin.TenantSettings
	if err := httpapi.ReadJSON(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	v, err := h.a.UpdateSettings(r.Context(), in)
	reply(w, http.StatusOK, v, err)
}

func (h *handler) listKeys(w http.ResponseWriter, r *http.Request) {
	v, err := h.a.ListKeys(r.Context())
	if v == nil {
		v = []*types.APIKey{}
	}
	reply(w, http.StatusOK, map[string]any{"items": v}, err)
}

func (h *handler) createKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name              string               `json:"name"`
		Role              tenant.Role          `json:"role"`
		MaxPermissionMode types.PermissionMode `json:"max_permission_mode"`
	}
	if err := httpapi.ReadJSON(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		httpapi.WriteError(w, errs.Invalid("name is required"))
		return
	}
	if in.Role == "" {
		in.Role = tenant.RoleMember
	}
	v, err := h.a.CreateKey(r.Context(), in.Name, in.Role, in.MaxPermissionMode)
	reply(w, http.StatusCreated, v, err)
}

func (h *handler) setKey(disabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := h.a.SetKeyDisabled(r.Context(), param(r, "id"), disabled)
		reply(w, http.StatusOK, v, err)
	}
}

func (h *handler) usage(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	v, err := h.a.Usage(r.Context(), days)
	reply(w, http.StatusOK, v, err)
}

func timeQuery(r *http.Request, name string) (*time.Time, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return nil, errs.Invalid("query %s must be RFC 3339", name)
	}
	return &t, nil
}

func (h *handler) audit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, err := timeQuery(r, "from")
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	to, err := timeQuery(r, "to")
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	after, limit := httpapi.Page(r)
	v, err := h.a.QueryAudit(r.Context(), admin.AuditQuery{Action: q.Get("action"), Target: q.Get("target"), ActorID: q.Get("actor"), From: from, To: to, After: after, Limit: limit})
	reply(w, http.StatusOK, v, err)
}

// --- debugger ---

func (h *handler) timeline(w http.ResponseWriter, r *http.Request) {
	v, err := h.a.TurnTimeline(r.Context(), param(r, "id"))
	reply(w, http.StatusOK, v, err)
}

func (h *handler) llmCalls(w http.ResponseWriter, r *http.Request) {
	v, err := h.a.ListLLMCalls(r.Context(), param(r, "id"))
	reply(w, http.StatusOK, map[string]any{"items": v}, err)
}

func (h *handler) assembly(w http.ResponseWriter, r *http.Request) {
	var in admin.PreviewInput
	if err := httpapi.ReadJSON(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	v, err := h.a.AssemblyPreview(r.Context(), in)
	reply(w, http.StatusOK, v, err)
}

func (h *handler) fork(w http.ResponseWriter, r *http.Request) {
	var in admin.ForkInput
	if err := httpapi.ReadJSON(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	v, err := h.a.Fork(r.Context(), param(r, "id"), in)
	reply(w, http.StatusCreated, v, err)
}

func (h *handler) playground(w http.ResponseWriter, r *http.Request) {
	var in admin.PlaygroundInput
	if err := httpapi.ReadJSON(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	v, err := h.a.PlaygroundSession(r.Context(), in)
	reply(w, http.StatusCreated, v, err)
}

// --- platform ---

func (h *handler) listTenants(w http.ResponseWriter, r *http.Request) {
	v, err := h.a.ListTenants(r.Context())
	if v == nil {
		v = []*types.Tenant{}
	}
	reply(w, http.StatusOK, map[string]any{"items": v}, err)
}

func (h *handler) createTenant(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID                 string `json:"id"`
		Name               string `json:"name"`
		DailyTokenBudget   int64  `json:"daily_token_budget,omitempty"`
		MaxConcurrentTurns int    `json:"max_concurrent_turns,omitempty"`
		DefaultRole        string `json:"default_role,omitempty"`
	}
	if err := httpapi.ReadJSON(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	t := &types.Tenant{Name: in.Name, DailyTokenBudget: in.DailyTokenBudget, MaxConcurrentTurns: in.MaxConcurrentTurns, DefaultRole: in.DefaultRole}
	t.ID = in.ID
	v, err := h.a.CreateTenant(r.Context(), t)
	reply(w, http.StatusCreated, v, err)
}

func (h *handler) status(w http.ResponseWriter, r *http.Request) {
	v, err := h.a.Status(r.Context())
	reply(w, http.StatusOK, v, err)
}

func (h *handler) breakGlassSession(w http.ResponseWriter, r *http.Request) {
	v, err := h.a.ReadSessionBreakGlass(r.Context(), param(r, "id"), r.URL.Query().Get("reason"))
	reply(w, http.StatusOK, v, err)
}

func (h *handler) breakGlassTurn(w http.ResponseWriter, r *http.Request) {
	v, err := h.a.ReadTurnBreakGlass(r.Context(), param(r, "id"), r.URL.Query().Get("reason"))
	reply(w, http.StatusOK, v, err)
}
