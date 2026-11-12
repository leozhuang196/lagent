package feishu

import (
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	orchfeishu "github.com/eternityx/lagent/internal/orch/feishu"
)

// Ingestor accepts events; orch/feishu.Service implements it. It must only
// de-duplicate and enqueue.
type Ingestor interface {
	Ingest(ctx context.Context, in orchfeishu.Inbound) (dup bool, err error)
}

// Gateway holds the long connection(s) to Feishu (§9.4: no public ingress).
// Only processes with the feishu role run it, because Feishu limits the number
// of connections per app.
type Gateway struct {
	AppID     string
	AppSecret string
	Domain    string // empty = Feishu China
	Ingest    Ingestor
	Log       *slog.Logger
	// BotOpenID, when set, requires group messages to @mention exactly the bot.
	BotOpenID string
	// AllowP2P accepts direct messages to the bot as well.
	AllowP2P bool
	// Connections is how many long connections to hold (default 1). Feishu
	// delivers each event on one of them; more connections only add resilience.
	Connections int
	MinBackoff  time.Duration // default 1s
	MaxBackoff  time.Duration // default 2m

	live atomic.Int32
}

func (g *Gateway) log() *slog.Logger {
	if g.Log == nil {
		return slog.Default()
	}
	return g.Log
}

// Dispatcher builds the SDK event dispatcher with our handlers. No
// verification token or encrypt key: long-connection frames are authenticated
// by the connection itself.
func (g *Gateway) Dispatcher() *dispatcher.EventDispatcher {
	return dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(g.OnMessage).
		OnP2CardActionTrigger(g.OnCardAction)
}

// Run holds the connections until ctx ends. Each connection reconnects with
// exponential backoff; losing all of them is logged as an error (alert).
func (g *Gateway) Run(ctx context.Context) error {
	n := g.Connections
	if n <= 0 {
		n = 1
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			g.hold(ctx, idx, n)
		}(i)
	}
	wg.Wait()
	return nil
}

// hold keeps one connection alive. The SDK's own reconnect is disabled so the
// backoff and the "all connections lost" alert are ours: a client that failed
// after connecting once is terminal, so every attempt uses a fresh client.
func (g *Gateway) hold(ctx context.Context, idx, total int) {
	minB, maxB := g.MinBackoff, g.MaxBackoff
	if minB <= 0 {
		minB = time.Second
	}
	if maxB <= 0 {
		maxB = 2 * time.Minute
	}
	backoff := minB
	for ctx.Err() == nil {
		var up atomic.Bool
		opts := []larkws.ClientOption{
			larkws.WithEventHandler(g.Dispatcher()),
			larkws.WithAutoReconnect(false),
			larkws.WithLogLevel(larkcore.LogLevelWarn),
			larkws.WithOnReady(func() {
				up.Store(true)
				live := g.live.Add(1)
				g.log().Info("feishu connection ready", "conn", idx, "live", live, "of", total)
			}),
		}
		if g.Domain != "" {
			opts = append(opts, larkws.WithDomain(BaseURL(g.Domain)))
		}
		cli := larkws.NewClient(g.AppID, g.AppSecret, opts...)
		started := time.Now()
		err := cli.Start(ctx)
		if up.Load() {
			live := g.live.Add(-1)
			if live == 0 && ctx.Err() == nil {
				g.log().Error("feishu: all long connections lost", "err", err)
			} else if ctx.Err() == nil {
				g.log().Warn("feishu connection lost", "conn", idx, "live", live, "err", err)
			}
			// A connection that stayed up for a while resets the backoff.
			if time.Since(started) > maxB {
				backoff = minB
			}
		} else if ctx.Err() == nil {
			lvl := slog.LevelWarn
			if g.live.Load() == 0 {
				lvl = slog.LevelError
			}
			g.log().Log(ctx, lvl, "feishu connect failed", "conn", idx, "retry_in", backoff, "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxB {
			backoff = maxB
		}
	}
}

func headerEventID(b *larkevent.EventV2Base) string {
	if b == nil || b.Header == nil {
		return ""
	}
	return b.Header.EventID
}

var mentionKey = regexp.MustCompile(`@_user_\d+`)

// OnMessage accepts im.message.receive_v1: group messages that @mention the
// bot (and direct messages when allowed). It only enqueues.
func (g *Gateway) OnMessage(ctx context.Context, ev *larkim.P2MessageReceiveV1) error {
	if ev == nil || ev.Event == nil || ev.Event.Message == nil {
		return nil
	}
	m := ev.Event.Message
	if ev.Event.Sender != nil && str(ev.Event.Sender.SenderType) != "" && str(ev.Event.Sender.SenderType) != "user" {
		return nil // ignore bots, including ourselves
	}
	chatType := str(m.ChatType)
	switch chatType {
	case "group":
		if !g.mentionsBot(m.Mentions) {
			return nil
		}
	case "p2p":
		if !g.AllowP2P {
			return nil
		}
	default:
		return nil
	}
	openID := ""
	if ev.Event.Sender != nil && ev.Event.Sender.SenderId != nil {
		openID = str(ev.Event.Sender.SenderId.OpenId)
	}
	in := orchfeishu.Inbound{
		EventID:   headerEventID(ev.EventV2Base),
		Kind:      orchfeishu.KindMessage,
		ChatID:    str(m.ChatId),
		OpenID:    openID,
		ChatType:  chatType,
		MessageID: str(m.MessageId),
		RootID:    str(m.RootId),
		MsgType:   str(m.MessageType),
		Text:      messageText(str(m.MessageType), str(m.Content)),
	}
	if in.EventID == "" {
		in.EventID = "msg:" + in.MessageID // still de-duplicates redeliveries
	}
	if _, err := g.Ingest.Ingest(ctx, in); err != nil {
		// Returning an error makes Feishu redeliver later, which is what we want
		// when the queue is unavailable.
		g.log().Error("feishu: ingest message", "event", in.EventID, "err", err)
		return err
	}
	return nil
}

// mentionsBot decides whether a group message is addressed to the bot. With a
// configured bot open_id it must match; otherwise a bot mention suffices (the
// platform only pushes group messages that @mention the app unless the app
// has the "read all group messages" scope).
func (g *Gateway) mentionsBot(ms []*larkim.MentionEvent) bool {
	if len(ms) == 0 {
		return false
	}
	typed := false
	for _, m := range ms {
		if m == nil {
			continue
		}
		if g.BotOpenID != "" {
			if m.Id != nil && str(m.Id.OpenId) == g.BotOpenID {
				return true
			}
			continue
		}
		if t := str(m.MentionedType); t != "" {
			typed = true
			if t == "bot" {
				return true
			}
		}
	}
	return g.BotOpenID == "" && !typed
}

// messageText extracts plain text and strips @mention placeholders.
func messageText(msgType, content string) string {
	var text string
	switch msgType {
	case "text":
		var c struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal([]byte(content), &c)
		text = c.Text
	case "post":
		text = postText(content)
	default:
		return ""
	}
	text = mentionKey.ReplaceAllString(text, "")
	return strings.TrimSpace(text)
}

// postText flattens a rich-text post into plain text.
func postText(content string) string {
	var p struct {
		Title   string `json:"title"`
		Content [][]struct {
			Tag  string `json:"tag"`
			Text string `json:"text"`
			Href string `json:"href"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(content), &p); err != nil {
		return ""
	}
	var b strings.Builder
	if p.Title != "" {
		b.WriteString(p.Title + "\n")
	}
	for _, line := range p.Content {
		for _, el := range line {
			switch el.Tag {
			case "text", "md":
				b.WriteString(el.Text)
			case "a":
				b.WriteString(el.Text)
				if el.Href != "" {
					b.WriteString(" (" + el.Href + ")")
				}
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// OnCardAction accepts card.action.trigger. It answers with a toast at once and
// leaves the decision to the queue; the card is updated when it is applied.
func (g *Gateway) OnCardAction(ctx context.Context, ev *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
	if ev == nil || ev.Event == nil || ev.Event.Action == nil {
		return nil, nil
	}
	pid, tid, decision, answer, ok := orchfeishu.ParseAction(ev.Event.Action.Value)
	if !ok {
		return nil, nil
	}
	in := orchfeishu.Inbound{
		EventID:   headerEventID(ev.EventV2Base),
		Kind:      orchfeishu.KindCardAction,
		PendingID: pid,
		TurnID:    tid,
		Decision:  decision,
		Answer:    answer,
	}
	if ev.Event.Operator != nil {
		in.OpenID = ev.Event.Operator.OpenID
	}
	if c := ev.Event.Context; c != nil {
		in.ChatID, in.CardMsgID = c.OpenChatID, c.OpenMessageID
	}
	if _, err := g.Ingest.Ingest(ctx, in); err != nil {
		g.log().Error("feishu: ingest card action", "event", in.EventID, "err", err)
		return &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: "error", Content: "系统繁忙，请稍后重试"}}, nil
	}
	return &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: "info", Content: "已收到，正在处理"}}, nil
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
