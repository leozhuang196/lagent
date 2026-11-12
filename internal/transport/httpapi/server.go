// Package httpapi translates HTTP into use-case calls. No business decisions
// live here: authenticate, validate shape, call orch, map errors.
package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/eternityx/lagent/internal/core/clock"
	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/infra/eventbus"
	"github.com/eternityx/lagent/internal/infra/objectstore"
	"github.com/eternityx/lagent/internal/orch/session"
)

// Deps are what the HTTP layer calls.
type Deps struct {
	Sessions *session.Service
	Bus      eventbus.Bus
	Objects  objectstore.Store
	Auth     *Auth
	Clock    clock.Clock
	Log      *slog.Logger
	// ObjectVerifier validates signed object URLs of the local object store.
	ObjectVerifier func(key, exp, sig string) bool
}

// Server is the HTTP API.
type Server struct {
	Deps
	router chi.Router
	api    chi.Router
}

// New builds the router. Mount adds more route groups (workflows, admin, static UI).
func New(d Deps) *Server {
	if d.Clock == nil {
		d.Clock = clock.Real{}
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	s := &Server{Deps: d}
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, recoverer(d.Log), requestLog(d.Log))
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	r.Post("/v1/auth/login", s.login)
	r.Post("/v1/auth/logout", s.logout)
	r.Get("/v1/objects", s.getObject) // signed URL, no auth header
	r.Route("/v1", func(api chi.Router) {
		api.Use(d.Auth.Middleware)
		api.Get("/me", s.me)
		api.Post("/sessions", s.createSession)
		api.Get("/sessions", s.listSessions)
		api.Get("/sessions/{id}", s.getSession)
		api.Post("/sessions/{id}/messages", s.submitMessage)
		api.Get("/sessions/{id}/events", s.events)
		api.Post("/sessions/{id}/cancel", s.cancel)
		api.Get("/turns/{id}", s.getTurn)
		api.Get("/pendings", s.listPendings)
		api.Get("/pendings/{id}", s.getPending)
		api.Post("/pendings/{id}/resolve", s.resolvePending)
		api.Get("/artifacts/{id}", s.getArtifact)
		api.Get("/artifacts/{id}/url", s.artifactURL)
		s.api = api
	})
	s.router = r
	return s
}

// Mount registers more authenticated /v1 routes.
func (s *Server) Mount(fn func(r chi.Router)) { fn(s.api) }

// Router exposes the root router (for static files).
func (s *Server) Router() chi.Router { return s.router }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.router.ServeHTTP(w, r) }

// --- helpers shared by every handler package ---

// WriteJSON writes v with status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ErrorBody is the error payload.
type ErrorBody struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details,omitempty"`
	} `json:"error"`
}

// WriteError maps an error code to an HTTP status.
func WriteError(w http.ResponseWriter, err error) {
	var body ErrorBody
	code := errs.CodeOf(err)
	body.Error.Code = string(code)
	body.Error.Message = err.Error()
	var e *errs.Error
	if errors.As(err, &e) {
		body.Error.Message = e.Message
		body.Error.Details = e.Details
	}
	status := http.StatusInternalServerError
	switch code {
	case errs.CodeNotFound:
		status = http.StatusNotFound
	case errs.CodeInvalid:
		status = http.StatusUnprocessableEntity
	case errs.CodeConflict, errs.CodeVersionConflict:
		status = http.StatusConflict
	case errs.CodeUnauthorized:
		status = http.StatusUnauthorized
	case errs.CodeForbidden:
		status = http.StatusForbidden
	case errs.CodeBudget:
		status = http.StatusTooManyRequests
	case errs.CodeUnavailable, errs.CodeLeaseLost:
		status = http.StatusServiceUnavailable
	case errs.CodeInternal:
		body.Error.Message = "internal error"
	}
	WriteJSON(w, status, body)
}

// ReadJSON decodes exactly one non-null JSON value (max 4 MiB), rejecting unknown fields.
func ReadJSON(r *http.Request, v any) error {
	const maxBody = 4 << 20
	if r.Body == nil {
		return errs.Invalid("JSON body is required")
	}
	// Read one extra byte so a valid prefix cannot hide an oversized body.
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return errs.Invalid("cannot read JSON body: %v", err)
	}
	if len(body) > maxBody {
		return errs.Invalid("JSON body exceeds 4 MiB")
	}
	if bytes.Equal(bytes.TrimSpace(body), []byte("null")) {
		return errs.Invalid("JSON body must not be null")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errs.Invalid("invalid JSON body: %v", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return errs.Invalid("JSON body must contain exactly one value")
	}
	return nil
}

// Page reads ?after=&limit= pagination.
func Page(r *http.Request) (string, int) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	return r.URL.Query().Get("after"), limit
}

func recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("panic in handler", "panic", rec, "path", r.URL.Path)
					WriteError(w, errs.New(errs.CodeInternal, "panic"))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func requestLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			if r.URL.Path == "/healthz" {
				return
			}
			log.Debug("http", "method", r.Method, "path", r.URL.Path, "status", ww.Status(), "ms", time.Since(start).Milliseconds())
		})
	}
}
