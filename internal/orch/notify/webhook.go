// Package notify mirrors turn lifecycle into delivery channels that are not
// the event stream: signed completion webhooks for internal systems.
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/queue"
	"github.com/eternityx/lagent/internal/orch/turn"
)

// Webhook delivers HMAC-signed callbacks through the queue (retried for ~24h).
type Webhook struct {
	Queue  queue.Queue
	Secret []byte
	Client *http.Client
	Log    *slog.Logger
}

// Payload is the callback body.
type Payload struct {
	Event     string            `json:"event"` // turn.ended, pending.created
	SessionID string            `json:"session_id"`
	TurnID    string            `json:"turn_id"`
	Status    string            `json:"status"`
	FinalText string            `json:"final_text,omitempty"`
	Result    *types.StepResult `json:"result,omitempty"`
	Error     *types.TurnError  `json:"error,omitempty"`
	Pending   *types.Pending    `json:"pending,omitempty"`
	Usage     types.Usage       `json:"usage"`
	At        time.Time         `json:"at"`
}

type job struct {
	URL  string          `json:"url"`
	Body json.RawMessage `json:"body"`
}

func (w *Webhook) enqueue(ctx context.Context, s *types.Session, p Payload) {
	c, ok := s.HasChannel(types.ChannelWebhook)
	if !ok || c.Target == "" {
		return
	}
	body, _ := json.Marshal(p)
	j, err := queue.NewJob(queue.TypeWebhook, s.TenantID, job{URL: c.Target, Body: body})
	if err != nil {
		return
	}
	j.MaxRetry = 15
	if err := w.Queue.Enqueue(ctx, j); err != nil && w.Log != nil {
		w.Log.Warn("enqueue webhook", "err", err)
	}
}

// OnPending notifies that the turn waits for a person (not a timeout).
func (w *Webhook) OnPending(ctx context.Context, s *types.Session, t *types.Turn, p *types.Pending) {
	w.enqueue(ctx, s, Payload{Event: "pending.created", SessionID: s.ID, TurnID: t.ID, Status: "waiting_human", Pending: p, At: time.Now().UTC()})
}

// OnTurnEnd notifies completion.
func (w *Webhook) OnTurnEnd(ctx context.Context, s *types.Session, t *types.Turn) {
	w.enqueue(ctx, s, Payload{Event: "turn.ended", SessionID: s.ID, TurnID: t.ID, Status: string(t.Status), FinalText: t.FinalText, Result: t.Result, Error: t.Error, Usage: t.Usage, At: time.Now().UTC()})
}

// Sign computes the X-Lagent-Signature header: hex HMAC-SHA256 of "ts.body".
func Sign(secret []byte, ts int64, body []byte) string {
	m := hmac.New(sha256.New, secret)
	fmt.Fprintf(m, "%d.", ts)
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// Handle delivers one webhook job.
func (w *Webhook) Handle(ctx context.Context, j queue.Job) error {
	var p job
	if err := j.Decode(&p); err != nil {
		return queue.SkipRetry(err)
	}
	client := w.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	ts := time.Now().Unix()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL, bytes.NewReader(p.Body))
	if err != nil {
		return queue.SkipRetry(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Lagent-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-Lagent-Signature", Sign(w.Secret, ts, p.Body))
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("webhook %s: status %d", p.URL, resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return queue.SkipRetry(fmt.Errorf("webhook %s: status %d", p.URL, resp.StatusCode))
	}
	return nil
}

var _ turn.Hooks = (*Webhook)(nil)
