package httpapi

import (
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/objectstore"
	"github.com/eternityx/lagent/internal/orch/session"
	"github.com/eternityx/lagent/internal/rules/artifact"
)

type createSessionBody struct {
	session.CreateInput
	// Webhook receives a signed completion callback (internal systems).
	Webhook string `json:"webhook,omitempty"`
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var b createSessionBody
	if err := ReadJSON(r, &b); err != nil {
		WriteError(w, err)
		return
	}
	in := b.CreateInput
	in.Origin = types.OriginAPI
	if r.Header.Get("X-Lagent-Client") == "web" {
		in.Origin = types.OriginWeb
		in.Channels = append(in.Channels, types.Channel{Kind: types.ChannelStream})
	} else if in.PermissionMode == "" {
		// Unattended callers must choose explicitly (§9.10).
		WriteError(w, errs.Invalid("permission_mode is required for API sessions"))
		return
	}
	if b.Webhook != "" {
		in.Channels = append(in.Channels, types.Channel{Kind: types.ChannelWebhook, Target: b.Webhook})
	}
	sess, err := s.Sessions.Create(r.Context(), in)
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, http.StatusCreated, sess)
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	after, limit := Page(r)
	p, err := s.Sessions.ListSessions(r.Context(), after, limit, r.URL.Query().Get("all") == "1")
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, p)
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	snap, err := s.Sessions.Snapshot(r.Context(), chi.URLParam(r, "id"), 0)
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, snap)
}

func (s *Server) submitMessage(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Text string `json:"text"`
	}
	if err := ReadJSON(r, &b); err != nil {
		WriteError(w, err)
		return
	}
	res, err := s.Sessions.Submit(r.Context(), chi.URLParam(r, "id"), b.Text, r.Header.Get("Idempotency-Key"))
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, http.StatusAccepted, res)
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	sess, err := s.Sessions.Cancel(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"session": sess, "note": "已停止，已发生的操作不会撤回"})
}

func (s *Server) getTurn(w http.ResponseWriter, r *http.Request) {
	t, err := s.Sessions.GetTurn(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, t)
}

func (s *Server) listPendings(w http.ResponseWriter, r *http.Request) {
	after, limit := Page(r)
	q := r.URL.Query()
	p, err := s.Sessions.ListPendings(r.Context(), q.Get("status"), q.Get("mine") == "1", after, limit)
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, p)
}

func (s *Server) getPending(w http.ResponseWriter, r *http.Request) {
	p, err := s.Sessions.GetPending(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, p)
}

func (s *Server) resolvePending(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Decision types.Decision `json:"decision"`
		Answer   string         `json:"answer,omitempty"`
		Via      string         `json:"via,omitempty"`
	}
	if err := ReadJSON(r, &b); err != nil {
		WriteError(w, err)
		return
	}
	if b.Via == "" {
		b.Via = "web"
	}
	res, err := s.Sessions.Resolve(r.Context(), chi.URLParam(r, "id"), b.Decision, b.Answer, b.Via)
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, res)
}

func (s *Server) getArtifact(w http.ResponseWriter, r *http.Request) {
	a, err := s.Sessions.GetArtifact(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, a)
}

func (s *Server) artifactURL(w http.ResponseWriter, r *http.Request) {
	a, err := s.Sessions.GetArtifact(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, err)
		return
	}
	t, _ := tenant.ID(r.Context())
	if !objectstore.OwnedBy(a.ObjectKey, t) {
		WriteError(w, errs.NotFound("artifact", a.ID))
		return
	}
	u, err := s.Objects.SignedURL(r.Context(), a.ObjectKey, artifact.SignedURLTTL)
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"url": u, "expires_at": s.Clock.Now().Add(artifact.SignedURLTTL), "name": a.Name})
}

// getObject serves local-store signed URLs.
func (s *Server) getObject(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if s.ObjectVerifier == nil || !s.ObjectVerifier(q.Get("key"), q.Get("exp"), q.Get("sig")) {
		WriteError(w, errs.New(errs.CodeForbidden, "invalid or expired link"))
		return
	}
	rc, info, err := s.Objects.Get(r.Context(), q.Get("key"))
	if err != nil {
		WriteError(w, err)
		return
	}
	defer rc.Close()
	if info.ContentType != "" {
		w.Header().Set("Content-Type", info.ContentType)
	}
	w.Header().Set("Cache-Control", "private, max-age=60")
	_, _ = io.Copy(w, rc)
}
