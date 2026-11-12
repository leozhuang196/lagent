package feishu

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/queue"
	"github.com/eternityx/lagent/internal/orch/turn"
)

// Delivery kinds.
const (
	DeliverPending = "pending"
	DeliverTurnEnd = "turn_end"
)

// Delivery is the payload of a queue.TypeFeishuDeliver job. It only carries
// ids: the handler reads the current state, so a late delivery never shows
// stale content.
type Delivery struct {
	Kind      string `json:"kind"`
	SessionID string `json:"session_id"`
	TurnID    string `json:"turn_id,omitempty"`
	PendingID string `json:"pending_id,omitempty"`
}

var _ turn.Hooks = (*Service)(nil)

func feishuThread(s *types.Session) (chatID, root string, ok bool) {
	c, has := s.HasChannel(types.ChannelFeishu)
	if !has {
		return "", "", false
	}
	return ParseTarget(c.Target)
}

func (s *Service) enqueue(ctx context.Context, tenantID string, d Delivery, unique string) {
	j, err := queue.NewJob(queue.TypeFeishuDeliver, tenantID, d)
	if err != nil {
		return
	}
	j.UniqueKey = unique
	j.MaxRetry = 8
	j.Timeout = time.Minute
	if err := s.Queue.Enqueue(ctx, j); err != nil {
		s.log().Warn("feishu: enqueue delivery", "kind", d.Kind, "session", d.SessionID, "err", err)
	}
}

// OnPending mirrors a new pending item into the thread as a card. Hooks must
// be fast, so it only enqueues.
func (s *Service) OnPending(ctx context.Context, sess *types.Session, t *types.Turn, p *types.Pending) {
	if _, _, ok := feishuThread(sess); !ok {
		return
	}
	if p.Kind == types.PendingSubtasks || p.Kind == types.PendingExternalJob {
		return // resolved by the platform, nothing for people to click
	}
	s.enqueue(ctx, sess.TenantID, Delivery{Kind: DeliverPending, SessionID: sess.ID, TurnID: t.ID, PendingID: p.ID}, "feishu:pending:"+p.ID)
}

// OnTurnEnd replies the turn's result into the thread.
func (s *Service) OnTurnEnd(ctx context.Context, sess *types.Session, t *types.Turn) {
	if _, _, ok := feishuThread(sess); !ok {
		return
	}
	s.enqueue(ctx, sess.TenantID, Delivery{Kind: DeliverTurnEnd, SessionID: sess.ID, TurnID: t.ID}, "feishu:turn:"+t.ID)
}

// HandleDeliver is the queue handler for queue.TypeFeishuDeliver; ctx is the
// system principal of the session's tenant.
func (s *Service) HandleDeliver(ctx context.Context, j queue.Job) error {
	return permanent(s.deliver(ctx, j))
}

func (s *Service) deliver(ctx context.Context, j queue.Job) error {
	var d Delivery
	if err := j.Decode(&d); err != nil {
		return queue.SkipRetry(err)
	}
	sess, err := s.Store.Sessions.Get(ctx, d.SessionID)
	if errs.Is(err, errs.CodeNotFound) {
		return queue.SkipRetry(err)
	}
	if err != nil {
		return err
	}
	_, root, ok := feishuThread(sess)
	if !ok {
		return nil
	}
	switch d.Kind {
	case DeliverPending:
		p, err := s.Store.Pendings.Get(ctx, d.PendingID)
		if err != nil {
			return err
		}
		if p.Status != types.PendingOpen {
			return nil // already handled elsewhere; no card needed
		}
		card := PendingCard(p, sess.CreatedBy)
		if card == nil {
			return nil
		}
		_, err = s.Client.ReplyCard(ctx, root, card, uuidFor("p", p.ID))
		return err
	case DeliverTurnEnd:
		t, err := s.Store.Turns.Get(ctx, d.TurnID)
		if err != nil {
			return err
		}
		text := s.resultText(ctx, sess, t)
		if text == "" {
			return nil
		}
		_, err = s.Client.ReplyText(ctx, root, text, uuidFor("t", t.ID))
		return err
	}
	return queue.SkipRetry(fmt.Errorf("unknown feishu delivery %q", d.Kind))
}

// resultText is the thread reply for an ended turn: the final text (cut to
// fit a message) and signed links to the turn's deliverables.
func (s *Service) resultText(ctx context.Context, sess *types.Session, t *types.Turn) string {
	switch t.Status {
	case types.TurnCancelled:
		return msgCancelled
	case types.TurnFailed:
		msg := "未知错误"
		if t.Error != nil && t.Error.Message != "" {
			msg, _ = truncateRunes(t.Error.Message, 500)
		}
		return "任务失败：" + sanitize(msg)
	case types.TurnCompleted:
	default:
		return ""
	}
	max := s.MaxReplyRunes
	if max == 0 {
		max = 3000
	}
	var b strings.Builder
	text, cut := truncateRunes(strings.TrimSpace(t.FinalText), max)
	b.WriteString(sanitize(text))
	if cut {
		b.WriteString("\n\n……（内容过长已截断")
		if s.WebBaseURL != "" {
			fmt.Fprintf(&b, "，完整结果见 %s/sessions/%s）", strings.TrimRight(s.WebBaseURL, "/"), sess.ID)
		} else {
			b.WriteString("，完整结果请在控制台查看）")
		}
	}
	if s.Links != nil {
		links, err := s.Links.TurnLinks(ctx, sess.ID, t.ID)
		if err != nil {
			s.log().Warn("feishu: artifact links", "turn", t.ID, "err", err)
		}
		if len(links) > 0 {
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString("产出文件（下载链接有时效）：")
			for _, l := range links {
				fmt.Fprintf(&b, "\n• %s：%s", sanitize(l.Name), l.URL)
			}
		}
	}
	if b.Len() == 0 {
		return msgDone
	}
	return b.String()
}
