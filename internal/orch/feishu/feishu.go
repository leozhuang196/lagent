// Package feishu holds the Feishu use cases (§9.4, §9.5, §9.18): group
// @mentions become session messages, pending items are mirrored into the
// thread as interactive cards, card clicks resolve through the same Resolve
// use case as the web, and turn results are replied into the thread.
//
// The long-connection gateway (transport/feishu) only calls Ingest, which
// de-duplicates and enqueues. Everything slow happens in queue handlers, so
// Feishu never sees a slow callback (slow callbacks are retried by Feishu and
// would create duplicates).
package feishu

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/eternityx/lagent/internal/core/clock"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/infra/coord"
	"github.com/eternityx/lagent/internal/infra/queue"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/orch/session"
)

// Client is the small slice of the Feishu open API the platform uses. The
// production implementation wraps the lark SDK; tests use a fake.
//
// uuid is Feishu's request de-duplication key (max 50 chars): sending twice
// with the same uuid delivers one message, which makes retried jobs safe.
type Client interface {
	// SendText posts a text message to a chat.
	SendText(ctx context.Context, chatID, text, uuid string) (msgID string, err error)
	// ReplyText replies to msgID inside its thread (reply_in_thread).
	ReplyText(ctx context.Context, msgID, text, uuid string) (string, error)
	// ReplyCard replies an interactive card to msgID inside its thread.
	ReplyCard(ctx context.Context, msgID string, card json.RawMessage, uuid string) (string, error)
	// UpdateCard replaces the content of a card message the app sent.
	UpdateCard(ctx context.Context, msgID string, card json.RawMessage) error
}

// Link is one downloadable deliverable.
type Link struct {
	Name string
	URL  string
}

// ArtifactLinks lists the deliverables of a turn as signed download URLs.
// The app wires it to session.Service.ListArtifacts + objectstore.SignedURL.
type ArtifactLinks interface {
	TurnLinks(ctx context.Context, sessionID, turnID string) ([]Link, error)
}

// LinksFunc adapts a function to ArtifactLinks.
type LinksFunc func(ctx context.Context, sessionID, turnID string) ([]Link, error)

func (f LinksFunc) TurnLinks(ctx context.Context, sessionID, turnID string) ([]Link, error) {
	return f(ctx, sessionID, turnID)
}

// Service implements the Feishu use cases. It is also a turn.Hooks.
type Service struct {
	Store    *repo.Store
	Sessions *session.Service
	Queue    queue.Queue
	Counter  coord.Counter // event de-duplication
	Client   Client
	Links    ArtifactLinks // optional
	Clock    clock.Clock
	Log      *slog.Logger
	// WebBaseURL lets replies point to the console for content that does not fit.
	WebBaseURL string
	// MaxReplyRunes truncates final texts (Feishu messages are limited; long
	// answers belong in the console). Default 3000.
	MaxReplyRunes int
	// DedupeTTL is how long event ids are remembered. Default 24h, longer than
	// Feishu's retry window.
	DedupeTTL time.Duration
}

func (s *Service) log() *slog.Logger {
	if s.Log == nil {
		return slog.Default()
	}
	return s.Log
}

func (s *Service) now() time.Time {
	if s.Clock == nil {
		return time.Now()
	}
	return s.Clock.Now()
}

// Inbound kinds.
const (
	KindMessage    = "message"
	KindCardAction = "card_action"
)

// Card action decisions besides types.Decision values.
const DecisionApproveAll = "approve_all"

// Inbound is the minimal payload the gateway enqueues for one Feishu event.
type Inbound struct {
	EventID string `json:"event_id"`
	Kind    string `json:"kind"`
	ChatID  string `json:"chat_id"`
	OpenID  string `json:"open_id"` // sender / clicking user
	Name    string `json:"name,omitempty"`

	// message
	ChatType  string `json:"chat_type,omitempty"` // group, p2p
	MessageID string `json:"message_id,omitempty"`
	RootID    string `json:"root_id,omitempty"` // thread root; empty for a top-level message
	Text      string `json:"text,omitempty"`    // mentions already stripped
	MsgType   string `json:"msg_type,omitempty"`

	// card action
	CardMsgID string `json:"card_msg_id,omitempty"`
	PendingID string `json:"pending_id,omitempty"`
	TurnID    string `json:"turn_id,omitempty"`
	Decision  string `json:"decision,omitempty"`
	Answer    string `json:"answer,omitempty"`
}

// Ingest de-duplicates an event by id and enqueues it. It never runs a turn
// and returns quickly; dup reports an event seen before (§9.4).
func (s *Service) Ingest(ctx context.Context, in Inbound) (dup bool, err error) {
	key := ""
	if in.EventID != "" && s.Counter != nil {
		ttl := s.DedupeTTL
		if ttl == 0 {
			ttl = 24 * time.Hour
		}
		key = "feishu:evt:" + in.EventID
		n, err := s.Counter.IncrBy(ctx, key, 1, ttl)
		if err != nil {
			return false, err
		}
		if n > 1 {
			return true, nil
		}
	}
	// The tenant is unknown until the chat binding is read, so inbound jobs run
	// under the system tenant and the handler switches to the chat's tenant.
	j, err := queue.NewJob(queue.TypeFeishuInbound, tenant.System, in)
	if err == nil {
		if in.EventID != "" {
			j.UniqueKey = "feishu:in:" + in.EventID
		}
		j.MaxRetry = 5
		j.Timeout = time.Minute
		err = s.Queue.Enqueue(ctx, j)
	}
	if err != nil && key != "" {
		// Forget the id so Feishu's redelivery is not dropped as a duplicate.
		_, _ = s.Counter.IncrBy(context.WithoutCancel(ctx), key, -1, time.Minute)
	}
	return false, err
}

// ThreadTarget encodes a Feishu thread as a channel target.
func ThreadTarget(chatID, rootMsgID string) string { return chatID + "/" + rootMsgID }

// ParseTarget splits a channel target made by ThreadTarget.
func ParseTarget(t string) (chatID, rootMsgID string, ok bool) {
	i := strings.LastIndex(t, "/")
	if i <= 0 || i == len(t)-1 {
		return "", "", false
	}
	return t[:i], t[i+1:], true
}

// uuidFor builds a Feishu de-duplication key (≤ 50 chars) from parts.
func uuidFor(parts ...string) string {
	u := strings.Join(parts, ":")
	if len(u) <= 50 {
		return u
	}
	h := sha256.Sum256([]byte(u))
	return hex.EncodeToString(h[:])[:40]
}

// truncateRunes cuts s to n runes, reporting whether it did.
func truncateRunes(s string, n int) (string, bool) {
	if n <= 0 {
		return s, false
	}
	r := []rune(s)
	if len(r) <= n {
		return s, false
	}
	return string(r[:n]), true
}
