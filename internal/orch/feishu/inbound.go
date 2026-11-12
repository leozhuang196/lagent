package feishu

import (
	"context"
	"fmt"
	"strings"

	connfeishu "github.com/eternityx/lagent/internal/capability/connector/feishu"
	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/queue"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/infra/repo/scope"
	"github.com/eternityx/lagent/internal/orch/session"
	"github.com/eternityx/lagent/internal/rules/approval"
)

// HandleInbound is the queue handler for queue.TypeFeishuInbound.
func (s *Service) HandleInbound(ctx context.Context, j queue.Job) error {
	var in Inbound
	if err := j.Decode(&in); err != nil {
		return queue.SkipRetry(err)
	}
	switch in.Kind {
	case KindMessage:
		return permanent(s.handleMessage(ctx, in))
	case KindCardAction:
		return permanent(s.handleCardAction(ctx, in))
	}
	return queue.SkipRetry(fmt.Errorf("unknown feishu inbound kind %q", in.Kind))
}

// permanent stops retries for errors that cannot heal (bad ids, the bot was
// removed from the chat): retrying would only repeat the failure.
func permanent(err error) error {
	if err != nil && !queue.IsSkipRetry(err) && (errs.Is(err, errs.CodeInvalid) || errs.Is(err, errs.CodeNotFound)) {
		return queue.SkipRetry(err)
	}
	return err
}

// chatBinding finds the tenant that bound chatID. This is the one cross-tenant
// read of the module: an event carries no tenant, only the chat (§9.4).
func (s *Service) chatBinding(ctx context.Context, chatID string) (*types.FeishuChat, error) {
	cs, err := repo.All(scope.AllTenants(ctx), s.Store.FeishuChats, repo.Query{Filter: repo.Filter{"chat_id": chatID}}, 10)
	if err != nil {
		return nil, err
	}
	for _, c := range cs {
		if c.Bound {
			return c, nil
		}
	}
	return nil, nil
}

// userCtx acts as the Feishu user inside the chat's tenant. Feishu users are
// plain members: permission caps and ownership checks apply as on the web.
func userCtx(ctx context.Context, chat *types.FeishuChat, in Inbound) context.Context {
	return tenant.With(ctx, tenant.Principal{Kind: tenant.KindFeishuUser, ID: in.OpenID, TenantID: chat.TenantID, Role: tenant.RoleMember, Name: in.Name})
}

func (s *Service) reply(ctx context.Context, msgID, text, uuid string) error {
	_, err := s.Client.ReplyText(ctx, msgID, text, uuid)
	return err
}

func (s *Service) handleMessage(ctx context.Context, in Inbound) error {
	if in.ChatID == "" || in.MessageID == "" || in.OpenID == "" {
		return queue.SkipRetry(errs.Invalid("feishu message without chat, message or sender"))
	}
	chat, err := s.chatBinding(ctx, in.ChatID)
	if err != nil {
		return err
	}
	if chat == nil {
		return s.reply(ctx, in.MessageID, msgUnbound, uuidFor("ub", in.MessageID))
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		msg := msgEmpty
		if in.MsgType != "" && in.MsgType != "text" && in.MsgType != "post" {
			msg = msgUnsupported
		}
		return s.reply(ctx, in.MessageID, msg, uuidFor("em", in.MessageID))
	}
	uctx := userCtx(ctx, chat, in)
	root := in.RootID
	if root == "" {
		root = in.MessageID
	}
	sid, err := s.threadSession(uctx, chat, root, text)
	if errs.Is(err, errs.CodeInvalid) {
		s.log().Warn("feishu: cannot create session", "chat", chat.ChatID, "err", err)
		_ = s.reply(ctx, in.MessageID, msgRoleMissing, uuidFor("rm", in.MessageID))
		return queue.SkipRetry(err)
	}
	if err != nil {
		return err
	}
	if handled, err := s.answerByText(uctx, sid, in, text); handled || err != nil {
		return err
	}
	res, err := s.Sessions.Submit(uctx, sid, text, "feishu:"+in.MessageID)
	switch {
	case errs.Is(err, errs.CodeForbidden):
		return s.reply(ctx, in.MessageID, msgNotOwner, uuidFor("no", in.MessageID))
	case errs.Is(err, errs.CodeConflict):
		return s.reply(ctx, in.MessageID, msgBusy, uuidFor("bz", in.MessageID))
	case errs.Is(err, errs.CodeInvalid):
		_ = s.reply(ctx, in.MessageID, msgFailedStart, uuidFor("fs", in.MessageID))
		return queue.SkipRetry(err)
	case err != nil:
		return err
	}
	if !res.Queued {
		return nil
	}
	msg := msgQueued
	if res.Turn != nil && res.Turn.Status == types.TurnSuspended {
		msg = msgWaitingButton
	}
	return s.reply(ctx, in.MessageID, msg, uuidFor("qd", in.MessageID))
}

// threadSession returns the session bound to the thread, creating and binding
// one on the first @mention (§9.4).
func (s *Service) threadSession(ctx context.Context, chat *types.FeishuChat, root, text string) (string, error) {
	th, err := s.Store.FeishuThreads.FindOne(ctx, repo.Filter{"root_msg_id": root})
	if err == nil {
		return th.SessionID, nil
	}
	if !errs.Is(err, errs.CodeNotFound) {
		return "", err
	}
	title, _ := truncateRunes(text, 40)
	sess, err := s.Sessions.Create(ctx, session.CreateInput{
		Title:   title,
		RoleKey: chat.DefaultRole, // empty: the tenant's default role
		Origin:  types.OriginFeishu,
		Channels: []types.Channel{
			{Kind: types.ChannelFeishu, Target: ThreadTarget(chat.ChatID, root)},
			{Kind: types.ChannelInbox},
		},
		// Replying in this one thread needs no approval; every other action
		// still follows the permission mode (§9.4, standing grants are exact).
		StandingGrants: []types.StandingGrant{{Tool: connfeishu.ToolReply, Field: connfeishu.ReplyField, Value: root}},
		Labels:         map[string]string{"feishu_chat": chat.ChatID, "feishu_root": root},
	})
	if err != nil {
		return "", err
	}
	th = &types.FeishuThread{ChatID: chat.ChatID, RootMsgID: root, SessionID: sess.ID}
	if err := s.Store.FeishuThreads.Insert(ctx, th); err != nil {
		if !errs.Is(err, errs.CodeConflict) {
			return "", err
		}
		// A concurrent message bound the thread first (unique root_msg_id):
		// use its session; ours stays idle and empty.
		th, err = s.Store.FeishuThreads.FindOne(ctx, repo.Filter{"root_msg_id": root})
		if err != nil {
			return "", err
		}
	}
	return th.SessionID, nil
}

// answerByText resolves the session's single open question from a thread
// message when the text maps clearly to it: an exact option, or any text for
// an open-ended question. Anything else is never taken as a decision (§9.5);
// approvals are only decided by buttons.
func (s *Service) answerByText(ctx context.Context, sessionID string, in Inbound, text string) (bool, error) {
	open, err := repo.All(ctx, s.Store.Pendings, repo.Query{Filter: repo.Filter{"session_id": sessionID, "status": string(types.PendingOpen)}}, 20)
	if err != nil || len(open) == 0 {
		return false, err
	}
	var q *types.Pending
	for _, p := range open {
		if p.Kind != types.PendingQuestion {
			continue
		}
		if q != nil {
			return false, nil // ambiguous: more than one question
		}
		q = p
	}
	if q == nil {
		return false, nil
	}
	answer := ""
	if len(q.Options) == 0 {
		answer = text
	} else {
		for _, o := range q.Options {
			if strings.EqualFold(strings.TrimSpace(o), text) {
				answer = o
				break
			}
		}
	}
	if answer == "" {
		return false, nil
	}
	res, err := s.Sessions.Resolve(ctx, q.ID, types.DecisionAnswer, answer, "feishu")
	if errs.Is(err, errs.CodeForbidden) {
		return true, s.reply(ctx, in.MessageID, "这个问题需要由发起人回答。", uuidFor("qf", in.MessageID))
	}
	if err != nil {
		return false, err
	}
	if res.AlreadyResolved {
		return false, nil // treat as a normal message
	}
	a, _ := truncateRunes(answer, 200)
	return true, s.reply(ctx, in.MessageID, "已记录你的回答："+sanitize(a), uuidFor("qa", in.MessageID))
}

func (s *Service) handleCardAction(ctx context.Context, in Inbound) error {
	if in.ChatID == "" || in.OpenID == "" || in.Decision == "" {
		return queue.SkipRetry(errs.Invalid("incomplete card action"))
	}
	chat, err := s.chatBinding(ctx, in.ChatID)
	if err != nil {
		return err
	}
	if chat == nil {
		return nil // the chat was unbound since the card was sent
	}
	uctx := userCtx(ctx, chat, in)
	if in.Decision == DecisionApproveAll {
		return s.approveAll(uctx, in)
	}
	p, err := s.Store.Pendings.Get(uctx, in.PendingID)
	if errs.Is(err, errs.CodeNotFound) {
		return queue.SkipRetry(err)
	}
	if err != nil {
		return err
	}
	if ok, err := s.inChat(uctx, p.SessionID, in.ChatID); err != nil || !ok {
		return err
	}
	res, err := s.Sessions.Resolve(uctx, p.ID, types.Decision(in.Decision), in.Answer, "feishu")
	switch {
	case errs.Is(err, errs.CodeForbidden):
		return s.reply(ctx, in.CardMsgID, fmt.Sprintf("%s 这个事项需要由发起人处理。", "<at user_id=\""+in.OpenID+"\"></at>"), uuidFor("cf", in.EventID))
	case errs.Is(err, errs.CodeInvalid):
		s.log().Warn("feishu: invalid card decision", "pending", p.ID, "decision", in.Decision, "err", err)
		return queue.SkipRetry(err)
	case err != nil:
		return err
	}
	return s.updateCard(ctx, in.CardMsgID, ResolvedCard(res.Pending, res.AlreadyResolved, ""))
}

// approveAll approves every open, batchable item of the turn. High-risk items
// are left for one-by-one confirmation (§9.5).
func (s *Service) approveAll(ctx context.Context, in Inbound) error {
	t, err := s.Store.Turns.Get(ctx, in.TurnID)
	if errs.Is(err, errs.CodeNotFound) {
		return queue.SkipRetry(err)
	}
	if err != nil {
		return err
	}
	if ok, err := s.inChat(ctx, t.SessionID, in.ChatID); err != nil || !ok {
		return err
	}
	open, err := repo.All(ctx, s.Store.Pendings, repo.Query{Filter: repo.Filter{"turn_id": t.ID, "status": string(types.PendingOpen)}}, 100)
	if err != nil {
		return err
	}
	approved := 0
	var left []string
	for _, p := range open {
		if !approval.Batchable(p) {
			if p.Kind == types.PendingApproval {
				left = append(left, "`"+mdEscape(p.ToolName)+"`")
			}
			continue
		}
		res, err := s.Sessions.Resolve(ctx, p.ID, types.DecisionApprove, "", "feishu")
		if errs.Is(err, errs.CodeForbidden) {
			return s.reply(ctx, in.CardMsgID, fmt.Sprintf("%s 这些事项需要由发起人处理。", "<at user_id=\""+in.OpenID+"\"></at>"), uuidFor("cf", in.EventID))
		}
		if err != nil {
			return err
		}
		if !res.AlreadyResolved {
			approved++
		}
	}
	extra := fmt.Sprintf("本次批量批准 %d 项。", approved)
	if len(left) > 0 {
		extra += "以下高风险事项需逐个确认：" + strings.Join(left, "、")
	}
	if in.PendingID == "" {
		return nil
	}
	p, err := s.Store.Pendings.Get(ctx, in.PendingID)
	if err != nil {
		return err
	}
	if p.Status == types.PendingOpen {
		return nil
	}
	already := p.Resolution != nil && (p.Resolution.Via != "feishu" || p.Resolution.By.ID != in.OpenID)
	return s.updateCard(ctx, in.CardMsgID, ResolvedCard(p, already, extra))
}

// inChat checks that a session belongs to a thread of chatID, so a forged
// action from another chat cannot reach it.
func (s *Service) inChat(ctx context.Context, sessionID, chatID string) (bool, error) {
	th, err := s.Store.FeishuThreads.FindOne(ctx, repo.Filter{"session_id": sessionID})
	if errs.Is(err, errs.CodeNotFound) {
		s.log().Warn("feishu: card action for a session without thread", "session", sessionID)
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if th.ChatID != chatID {
		s.log().Warn("feishu: card action from another chat", "session", sessionID, "chat", chatID)
		return false, nil
	}
	return true, nil
}

func (s *Service) updateCard(ctx context.Context, msgID string, card []byte) error {
	if msgID == "" {
		return nil
	}
	return s.Client.UpdateCard(ctx, msgID, card)
}
