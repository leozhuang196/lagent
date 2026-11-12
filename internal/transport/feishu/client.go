// Package feishu is the Feishu transport: the long-connection gateway that
// turns events into Ingest calls, and the lark SDK adapter used to send.
package feishu

import (
	"context"
	"encoding/json"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/eternityx/lagent/internal/core/errs"
	orchfeishu "github.com/eternityx/lagent/internal/orch/feishu"
)

// LarkClient implements orch/feishu.Client (and the connector's Sender) on
// the lark SDK.
type LarkClient struct {
	c *lark.Client
}

// NewLarkClient builds the API client. domain is empty for Feishu China or a
// base URL such as lark.LarkBaseUrl.
func NewLarkClient(appID, appSecret, domain string) *LarkClient {
	opts := []lark.ClientOptionFunc{lark.WithReqTimeout(15 * time.Second)}
	if domain != "" {
		opts = append(opts, lark.WithOpenBaseUrl(BaseURL(domain)))
	}
	return &LarkClient{c: lark.NewClient(appID, appSecret, opts...)}
}

var _ orchfeishu.Client = (*LarkClient)(nil)

func textContent(text string) string {
	raw, _ := json.Marshal(map[string]string{"text": text})
	return string(raw)
}

// apiErr maps a Feishu business error. Client-side errors (bad ids, bot not in
// chat, no permission) will not heal by retrying, so they are marked invalid.
func apiErr(op string, code int, msg string) error {
	if code >= 230000 && code < 240000 || code == 99991672 || code == 99991663 {
		return errs.New(errs.CodeInvalid, "feishu %s: %d %s", op, code, msg)
	}
	return errs.New(errs.CodeUnavailable, "feishu %s: %d %s", op, code, msg)
}

func (l *LarkClient) SendText(ctx context.Context, chatID, text, uuid string) (string, error) {
	body := larkim.NewCreateMessageReqBodyBuilder().ReceiveId(chatID).MsgType(larkim.MsgTypeText).Content(textContent(text))
	if uuid != "" {
		body = body.Uuid(uuid)
	}
	req := larkim.NewCreateMessageReqBuilder().ReceiveIdType(larkim.CreateMessageV1ReceiveIDTypeChatId).Body(body.Build()).Build()
	resp, err := l.c.Im.Message.Create(ctx, req)
	if err != nil {
		return "", errs.Wrap(errs.CodeUnavailable, err, "feishu send")
	}
	if !resp.Success() {
		return "", apiErr("send", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return "", nil
	}
	return *resp.Data.MessageId, nil
}

func (l *LarkClient) reply(ctx context.Context, msgID, msgType, content, uuid string) (string, error) {
	body := larkim.NewReplyMessageReqBodyBuilder().MsgType(msgType).Content(content).ReplyInThread(true)
	if uuid != "" {
		body = body.Uuid(uuid)
	}
	req := larkim.NewReplyMessageReqBuilder().MessageId(msgID).Body(body.Build()).Build()
	resp, err := l.c.Im.Message.Reply(ctx, req)
	if err != nil {
		return "", errs.Wrap(errs.CodeUnavailable, err, "feishu reply")
	}
	if !resp.Success() {
		return "", apiErr("reply", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return "", nil
	}
	return *resp.Data.MessageId, nil
}

func (l *LarkClient) ReplyText(ctx context.Context, msgID, text, uuid string) (string, error) {
	return l.reply(ctx, msgID, larkim.MsgTypeText, textContent(text), uuid)
}

func (l *LarkClient) ReplyCard(ctx context.Context, msgID string, card json.RawMessage, uuid string) (string, error) {
	return l.reply(ctx, msgID, larkim.MsgTypeInteractive, string(card), uuid)
}

func (l *LarkClient) UpdateCard(ctx context.Context, msgID string, card json.RawMessage) error {
	req := larkim.NewPatchMessageReqBuilder().MessageId(msgID).
		Body(larkim.NewPatchMessageReqBodyBuilder().Content(string(card)).Build()).Build()
	resp, err := l.c.Im.Message.Patch(ctx, req)
	if err != nil {
		return errs.Wrap(errs.CodeUnavailable, err, "feishu patch")
	}
	if !resp.Success() {
		return apiErr("patch", resp.Code, resp.Msg)
	}
	return nil
}

// BaseURL maps the configured domain: "" or "feishu" is Feishu China, "lark"
// is Lark international, anything else is taken as a base URL.
func BaseURL(domain string) string {
	switch domain {
	case "", "feishu":
		return lark.FeishuBaseUrl
	case "lark":
		return lark.LarkBaseUrl
	}
	return domain
}
