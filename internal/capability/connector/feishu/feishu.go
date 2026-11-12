// Package feishu provides the Feishu connector tools the model can call:
// replying in the session's own thread and posting to a chat bound to the
// tenant (§9.4, §9.18). Both are external calls; replying in the session's
// thread is pre-approved by a standing grant written when the thread was bound.
package feishu

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/eternityx/lagent/internal/capability/tool"
	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/repo"
)

// Tool names. The standing grant for a Feishu thread refers to ToolReply and
// its ReplyField.
const (
	ToolReply  = "feishu_reply"
	ToolSend   = "feishu_send_message"
	ReplyField = "thread_id"
)

// maxText bounds one message; Feishu rejects very large text bodies.
const maxText = 20_000

// Sender is the part of the Feishu client the tools need.
type Sender interface {
	SendText(ctx context.Context, chatID, text, uuid string) (string, error)
	ReplyText(ctx context.Context, msgID, text, uuid string) (string, error)
}

// Bindings tells whether a chat is bound to a tenant.
type Bindings interface {
	IsBound(ctx context.Context, tenantID, chatID string) (bool, error)
}

// StoreBindings reads bindings from the repository.
type StoreBindings struct{ Store *repo.Store }

func (b StoreBindings) IsBound(ctx context.Context, tenantID, chatID string) (bool, error) {
	c, err := b.Store.FeishuChats.FindOne(tenant.SystemContext(ctx, tenantID), repo.Filter{"chat_id": chatID})
	if errs.Is(err, errs.CodeNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return c.Bound, nil
}

// Tools returns the connector tools.
func Tools(s Sender, b Bindings) []tool.Tool {
	return []tool.Tool{replyTool(s), sendTool(s, b)}
}

// threadOf returns the chat and root message of the session's Feishu thread.
// The target format is "{chat_id}/{root_msg_id}" (orch/feishu.ThreadTarget).
func threadOf(env tool.Env) (chatID, root string, ok bool) {
	for _, c := range env.Channels {
		if c.Kind != types.ChannelFeishu {
			continue
		}
		i := strings.LastIndex(c.Target, "/")
		if i <= 0 || i == len(c.Target)-1 {
			return "", "", false
		}
		return c.Target[:i], c.Target[i+1:], true
	}
	return "", "", false
}

// uuid derives Feishu's de-duplication key from the ledger's idempotency key,
// so a retried call after a crash cannot post twice.
func uuid(env tool.Env) string {
	k := env.IdempotencyKey
	if len(k) > 50 {
		h := sha256.Sum256([]byte(k))
		k = hex.EncodeToString(h[:])[:40]
	}
	return k
}

func checkText(text string) *tool.Result {
	if strings.TrimSpace(text) == "" {
		r := tool.Errorf("text is required")
		return &r
	}
	if utf8.RuneCountInString(text) > maxText {
		r := tool.Errorf("text is too long (max %d characters)", maxText)
		return &r
	}
	return nil
}

func replyTool(s Sender) tool.Tool {
	return &tool.Func{
		S: types.ToolSpec{
			Name:        ToolReply,
			Description: "Reply with a text message in the Feishu thread this session is bound to. thread_id must be the thread's root message id given in the session context.",
			InputSchema: tool.Schema(map[string]any{
				"thread_id": tool.Str("Root message id of the session's Feishu thread."),
				"text":      tool.Str("Message text."),
			}, "thread_id", "text"),
		},
		M: types.ToolMeta{Category: "feishu", Source: types.SourceConnector, Risk: types.RiskExternal, SideEffect: true, Idempotent: true},
		Fn: func(ctx context.Context, in json.RawMessage, env tool.Env) (tool.Result, error) {
			var a struct {
				ThreadID string `json:"thread_id"`
				Text     string `json:"text"`
			}
			if r := tool.Decode(in, &a); r != nil {
				return *r, nil
			}
			if r := checkText(a.Text); r != nil {
				return *r, nil
			}
			_, root, ok := threadOf(env)
			if !ok {
				return tool.Errorf("this session is not bound to a Feishu thread"), nil
			}
			// Only the session's own thread: the standing grant covers exactly it,
			// and any other target would bypass approval.
			if a.ThreadID != root {
				return tool.Errorf("thread_id must be this session's thread (%s)", root), nil
			}
			id, err := s.ReplyText(ctx, root, a.Text, uuid(env))
			if err != nil {
				return tool.Result{}, err
			}
			return tool.Result{Content: "sent message " + id}, nil
		},
	}
}

func sendTool(s Sender, b Bindings) tool.Tool {
	return &tool.Func{
		S: types.ToolSpec{
			Name:        ToolSend,
			Description: "Send a text message to a Feishu group chat. The chat must be bound to this tenant by an administrator; other chats are refused.",
			InputSchema: tool.Schema(map[string]any{
				"chat_id": tool.Str("Feishu chat id (oc_...) of a group bound to this tenant."),
				"text":    tool.Str("Message text."),
			}, "chat_id", "text"),
		},
		M: types.ToolMeta{Category: "feishu", Source: types.SourceConnector, Risk: types.RiskExternal, SideEffect: true, Idempotent: true},
		Fn: func(ctx context.Context, in json.RawMessage, env tool.Env) (tool.Result, error) {
			var a struct {
				ChatID string `json:"chat_id"`
				Text   string `json:"text"`
			}
			if r := tool.Decode(in, &a); r != nil {
				return *r, nil
			}
			if r := checkText(a.Text); r != nil {
				return *r, nil
			}
			a.ChatID = strings.TrimSpace(a.ChatID)
			if a.ChatID == "" {
				return tool.Errorf("chat_id is required"), nil
			}
			// The model's chat id is only a selector: the binding configured by a
			// tenant admin decides whether the target is allowed.
			ok, err := b.IsBound(ctx, env.TenantID, a.ChatID)
			if err != nil {
				return tool.Result{}, err
			}
			if !ok {
				return tool.Errorf("chat %s is not bound to this tenant; ask a tenant admin to bind it first", a.ChatID), nil
			}
			id, err := s.SendText(ctx, a.ChatID, a.Text, uuid(env))
			if err != nil {
				return tool.Result{}, err
			}
			return tool.Result{Content: "sent message " + id}, nil
		},
	}
}
