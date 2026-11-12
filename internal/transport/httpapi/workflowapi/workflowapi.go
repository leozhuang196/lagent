// Package workflowapi exposes workflow runs, promotion and schedules over
// HTTP. Like the rest of transport it only translates: authenticate (done by
// the /v1 middleware), validate shape, call orch, map errors.
//
// Workflow definition CRUD lives elsewhere; this package only runs them.
package workflowapi

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/eternityx/lagent/internal/core/clock"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/eventbus"
	"github.com/eternityx/lagent/internal/orch/scheduler"
	"github.com/eternityx/lagent/internal/orch/workflow"
	"github.com/eternityx/lagent/internal/transport/httpapi"
)

// Handler serves the routes.
type Handler struct {
	Workflows *workflow.Service
	Schedules *scheduler.Service
	Bus       eventbus.Bus
	Clock     clock.Clock
}

// Routes registers every route on the authenticated /v1 router. Full paths
// are registered (no sub-routers) so other packages can add routes under the
// same prefixes without mount conflicts.
func (h *Handler) Routes(r chi.Router) {
	r.Post("/workflows/dry-run", h.dryRun)
	r.Post("/workflows/{key}/runs", h.startRun)
	r.Get("/runs", h.listRuns)
	r.Get("/runs/{id}", h.getRun)
	r.Post("/runs/{id}/continue", h.continueRun)
	r.Post("/runs/{id}/rerun", h.rerun)
	r.Post("/runs/{id}/cancel", h.cancelRun)
	r.Get("/runs/{id}/events", h.runEvents)
	r.Post("/sessions/{id}/promote", h.promote)

	r.Get("/schedules", h.listSchedules)
	r.Post("/schedules", h.createSchedule)
	r.Get("/schedules/{id}", h.getSchedule)
	r.Put("/schedules/{id}", h.updateSchedule)
	r.Delete("/schedules/{id}", h.deleteSchedule)
	r.Post("/schedules/{id}/pause", h.pauseSchedule)
	r.Post("/schedules/{id}/resume", h.resumeSchedule)
	r.Get("/schedules/{id}/upcoming", h.upcoming)
	r.Get("/schedules/{id}/runs", h.scheduleRuns)
}

// --- runs ---

func (h *Handler) startRun(w http.ResponseWriter, r *http.Request) {
	var in workflow.StartInput
	if err := httpapi.ReadJSON(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	in.DefKey = chi.URLParam(r, "key")
	run, err := h.Workflows.Start(r.Context(), in)
	reply(w, http.StatusCreated, run, err)
}

func (h *Handler) dryRun(w http.ResponseWriter, r *http.Request) {
	var in workflow.DryRunInput
	if err := httpapi.ReadJSON(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	res, err := h.Workflows.DryRun(r.Context(), in)
	reply(w, http.StatusOK, res, err)
}

func (h *Handler) listRuns(w http.ResponseWriter, r *http.Request) {
	after, limit := httpapi.Page(r)
	q := r.URL.Query()
	p, err := h.Workflows.List(r.Context(), workflow.ListFilter{
		DefKey: q.Get("def_key"), Status: types.RunStatus(q.Get("status")), ScheduleID: q.Get("schedule_id"), All: q.Get("all") == "1",
	}, after, limit)
	reply(w, http.StatusOK, p, err)
}

func (h *Handler) getRun(w http.ResponseWriter, r *http.Request) {
	v, err := h.Workflows.Get(r.Context(), chi.URLParam(r, "id"))
	reply(w, http.StatusOK, v, err)
}

func (h *Handler) continueRun(w http.ResponseWriter, r *http.Request) {
	run, err := h.Workflows.Continue(r.Context(), chi.URLParam(r, "id"))
	reply(w, http.StatusCreated, run, err)
}

func (h *Handler) rerun(w http.ResponseWriter, r *http.Request) {
	run, err := h.Workflows.Rerun(r.Context(), chi.URLParam(r, "id"))
	reply(w, http.StatusCreated, run, err)
}

func (h *Handler) cancelRun(w http.ResponseWriter, r *http.Request) {
	run, err := h.Workflows.Cancel(r.Context(), chi.URLParam(r, "id"))
	reply(w, http.StatusOK, run, err)
}

func (h *Handler) promote(w http.ResponseWriter, r *http.Request) {
	def, err := h.Workflows.Promote(r.Context(), chi.URLParam(r, "id"))
	reply(w, http.StatusCreated, def, err)
}

// runEvents streams a run's events (stream = run id) over SSE, with the same
// resume contract as session streams: Last-Event-ID or ?after=, and a
// stream.reset event when the cursor fell out of the buffer.
func (h *Handler) runEvents(w http.ResponseWriter, r *http.Request) {
	rid := chi.URLParam(r, "id")
	ctx := r.Context()
	if _, err := h.Workflows.Get(ctx, rid); err != nil { // authorisation
		httpapi.WriteError(w, err)
		return
	}
	httpapi.StreamEvents(w, r, h.Bus, rid, h.Clock)
}

// --- schedules ---

func (h *Handler) listSchedules(w http.ResponseWriter, r *http.Request) {
	after, limit := httpapi.Page(r)
	p, err := h.Schedules.List(r.Context(), r.URL.Query().Get("all") == "1", after, limit)
	reply(w, http.StatusOK, p, err)
}

func (h *Handler) createSchedule(w http.ResponseWriter, r *http.Request) {
	var in scheduler.Input
	if err := httpapi.ReadJSON(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	sc, err := h.Schedules.Create(r.Context(), in)
	reply(w, http.StatusCreated, sc, err)
}

func (h *Handler) getSchedule(w http.ResponseWriter, r *http.Request) {
	sc, err := h.Schedules.Get(r.Context(), chi.URLParam(r, "id"))
	reply(w, http.StatusOK, sc, err)
}

func (h *Handler) updateSchedule(w http.ResponseWriter, r *http.Request) {
	var in scheduler.Input
	if err := httpapi.ReadJSON(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	sc, err := h.Schedules.Update(r.Context(), chi.URLParam(r, "id"), in)
	reply(w, http.StatusOK, sc, err)
}

func (h *Handler) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	sc, err := h.Schedules.Delete(r.Context(), chi.URLParam(r, "id"))
	reply(w, http.StatusOK, sc, err)
}

func (h *Handler) pauseSchedule(w http.ResponseWriter, r *http.Request) {
	sc, err := h.Schedules.Pause(r.Context(), chi.URLParam(r, "id"))
	reply(w, http.StatusOK, sc, err)
}

func (h *Handler) resumeSchedule(w http.ResponseWriter, r *http.Request) {
	sc, err := h.Schedules.Resume(r.Context(), chi.URLParam(r, "id"))
	reply(w, http.StatusOK, sc, err)
}

func (h *Handler) upcoming(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	ts, err := h.Schedules.Upcoming(r.Context(), chi.URLParam(r, "id"), n)
	reply(w, http.StatusOK, map[string]any{"times": ts}, err)
}

func (h *Handler) scheduleRuns(w http.ResponseWriter, r *http.Request) {
	after, limit := httpapi.Page(r)
	p, err := h.Schedules.Runs(r.Context(), chi.URLParam(r, "id"), after, limit)
	reply(w, http.StatusOK, p, err)
}

func reply(w http.ResponseWriter, status int, v any, err error) {
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	httpapi.WriteJSON(w, status, v)
}
