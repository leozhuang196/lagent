package turn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"mime"
	"path/filepath"
	"strings"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/rules/sessionfsm"
)

// suspendOn writes new pending items, links them to their ledger entries,
// parks the turn and the session, and ends this execution (P3).
func (r *run) suspendOn(ctx context.Context, newPendings []*types.Pending, alsoWaiting ...string) error {
	ids := append([]string(nil), alsoWaiting...)
	for _, p := range newPendings {
		p.SessionID, p.TurnID, p.Status = r.sess.ID, r.turn.ID, types.PendingOpen
		p.TenantID = r.sess.TenantID
		if p.Assignee == "" {
			p.Assignee = r.sess.CreatedBy.ID
		}
		if err := r.e.Store.Pendings.Insert(ctx, p); err != nil {
			return err
		}
		if p.ToolCallID != "" {
			if _, err := r.setLedger(ctx, p.ToolCallID, func(c *types.ToolCall) { c.PendingID = p.ID }); err != nil {
				return err
			}
		}
		ids = append(ids, p.ID)
		r.emit(ctx, types.EvPendingCreated, map[string]any{"pending": p})
	}
	t, err := repo.Update(ctx, r.e.Store.Turns, r.turn.ID, func(t *types.Turn) error {
		return sessionfsm.SuspendTurn(t, r.epoch, ids)
	})
	if err != nil {
		return err
	}
	r.turn = t
	s, err := repo.Update(ctx, r.e.Store.Sessions, r.sess.ID, func(s *types.Session) error {
		return sessionfsm.Suspend(s, r.turn.ID)
	})
	if err != nil {
		return err
	}
	r.sess = s
	r.snapshot(ctx)
	r.emit(ctx, types.EvTurnSuspended, map[string]any{"waiting_on": ids})
	for _, p := range newPendings {
		for _, h := range r.e.Hooks {
			h.OnPending(ctx, r.sess, r.turn, p)
		}
	}
	// Pending items may have been resolved while we were writing (e.g. an
	// auto-resolved subtask). Re-check so the resume is not lost.
	if open, err := r.e.openPendings(ctx, r.turn); err == nil && len(open) == 0 {
		if s, err := repo.Update(ctx, r.e.Store.Sessions, r.sess.ID, func(s *types.Session) error {
			return sessionfsm.Requeue(s, r.turn.ID)
		}); err == nil {
			r.sess = s
			return Enqueue(ctx, r.e.Queue, r.sess.TenantID, r.turn.ID, true)
		}
	}
	return nil
}

// finish ends the turn with a terminal status and frees the session.
func (r *run) finish(ctx context.Context, status types.TurnStatus, te *types.TurnError) error {
	final := ""
	for i := len(r.history) - 1; i >= 0; i-- {
		if r.history[i].Role == types.RoleAssistant {
			final = r.history[i].PlainText()
			break
		}
	}
	now := r.e.Clock.Now()
	t, err := repo.Update(ctx, r.e.Store.Turns, r.turn.ID, func(t *types.Turn) error {
		if err := sessionfsm.EndTurn(t, r.epoch, status, te, now); err != nil {
			return err
		}
		t.FinalText = final
		if t.Result == nil && status == types.TurnCompleted && (r.sess.StepRunID != "" || r.sess.ParentTurnID != "") {
			t.Result = &types.StepResult{Summary: final}
		}
		return nil
	})
	if errs.Is(err, errs.CodeAlreadyResolved) {
		return nil
	}
	if err != nil {
		return err
	}
	r.turn = t
	var followUp []types.InboxItem
	s, err := repo.Update(ctx, r.e.Store.Sessions, r.sess.ID, func(s *types.Session) error {
		if err := sessionfsm.Finish(s, r.turn.ID); err != nil {
			return err
		}
		if status == types.TurnCompleted {
			followUp = sessionfsm.TakeInbox(s)
		} else {
			s.Inbox = nil
		}
		return nil
	})
	if err != nil {
		return err
	}
	r.sess = s
	r.snapshot(ctx)
	ev := map[types.TurnStatus]types.EventType{types.TurnCompleted: types.EvTurnCompleted, types.TurnFailed: types.EvTurnFailed, types.TurnCancelled: types.EvTurnCancelled}[status]
	r.emit(ctx, ev, map[string]any{"status": status, "error": te, "usage": t.Usage, "result": t.Result, "final_text": final})
	for _, h := range r.e.Hooks {
		h.OnTurnEnd(ctx, r.sess, r.turn)
	}
	if len(followUp) > 0 {
		// Messages that arrived after the last inbox check start the next turn.
		var texts []string
		for _, it := range followUp {
			texts = append(texts, it.Text)
		}
		if _, err := StartTurn(ctx, r.e.Store, r.e.Queue, r.sess.ID, StartInput{Text: strings.Join(texts, "\n\n"), Author: followUp[0].Author, Trigger: types.TriggerMessage}); err != nil {
			r.log.Warn("follow-up turn", "err", err)
		}
	}
	return nil
}

func (r *run) snapshot(ctx context.Context) {
	if r.e.Sandbox == nil || r.ws == nil || r.ws.Dir == "" {
		return
	}
	if err := r.e.Sandbox.Snapshot(context.WithoutCancel(ctx), r.ws); err != nil {
		r.log.Warn("workspace snapshot", "err", err)
	}
}

func (r *run) emit(ctx context.Context, typ types.EventType, data map[string]any) {
	if r.e.Bus == nil {
		return
	}
	ev := types.Event{Stream: r.sess.ID, TenantID: r.sess.TenantID, TurnID: r.turn.ID, Type: typ, TS: r.e.Clock.Now(), Data: data}
	if _, err := r.e.Bus.Append(context.WithoutCancel(ctx), ev); err != nil {
		r.log.Warn("emit event", "type", typ, "err", err)
	}
}

func (r *run) audit(ctx context.Context, action, target string, detail map[string]any) {
	a := &types.AuditLog{Actor: r.sess.CreatedBy, Action: action, Target: target, TurnID: r.turn.ID, Detail: detail}
	a.TenantID = r.sess.TenantID
	if err := r.e.Store.Audit.Insert(context.WithoutCancel(ctx), a); err != nil {
		r.log.Warn("audit", "err", err)
	}
}

type hashReader struct {
	r io.Reader
	h hash.Hash
}

func newHashReader(r io.Reader) *hashReader { return &hashReader{r: r, h: sha256.New()} }

func (h *hashReader) Read(p []byte) (int, error) {
	n, err := h.r.Read(p)
	h.h.Write(p[:n])
	return n, err
}

func (h *hashReader) sum() string { return hex.EncodeToString(h.h.Sum(nil)) }

func contentType(name string) string {
	if ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); ct != "" {
		return ct
	}
	return "application/octet-stream"
}
