package turn

import (
	"context"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/id"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/queue"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/rules/sessionfsm"
)

// StartInput describes a new turn.
type StartInput struct {
	Text        string
	Blocks      []types.Block // extra content (e.g. attachment references)
	Author      string
	Trigger     types.TurnTrigger
	IdemKey     string
	TokenBudget int64
	Debug       bool
}

// StartTurn admits the session (idle → queued), writes the user message,
// creates the turn and enqueues it. A busy session returns CodeConflict.
// With an idempotency key, a repeated submission returns the original turn.
func StartTurn(ctx context.Context, store *repo.Store, q queue.Queue, sessionID string, in StartInput) (*types.Turn, error) {
	if in.IdemKey != "" {
		if t, err := store.Turns.FindOne(ctx, repo.Filter{"session_id": sessionID, "idem_key": in.IdemKey}); err == nil {
			return t, nil
		}
	}
	turnID := id.NewWithPrefix(id.Turn)
	sess, err := repo.Update(ctx, store.Sessions, sessionID, func(s *types.Session) error {
		return sessionfsm.Admit(s, turnID)
	})
	if err != nil {
		return nil, err
	}
	rollback := func() {
		_, _ = repo.Update(context.WithoutCancel(ctx), store.Sessions, sessionID, func(s *types.Session) error {
			return sessionfsm.Finish(s, turnID)
		})
	}
	seq, err := lastSeq(ctx, store, sessionID)
	if err != nil {
		rollback()
		return nil, err
	}
	content := append([]types.Block{types.Text(in.Text)}, in.Blocks...)
	m := &types.Message{SessionID: sessionID, TurnID: turnID, Seq: seq + 1, Role: types.RoleUser, Content: content, Author: in.Author}
	m.TenantID = sess.TenantID
	if err := store.Messages.Insert(ctx, m); err != nil {
		rollback()
		return nil, err
	}
	t := &types.Turn{
		SessionID: sessionID, Trigger: in.Trigger, Status: types.TurnQueued, RoleKey: sess.RoleKey, RoleVer: sess.RoleVer,
		ParentTurnID: sess.ParentTurnID, StepRunID: sess.StepRunID, Debug: sess.Debug || in.Debug,
		IdemKey: in.IdemKey, TokenBudget: in.TokenBudget,
	}
	t.ID, t.TenantID = turnID, sess.TenantID
	if t.Trigger == "" {
		t.Trigger = types.TriggerMessage
	}
	if err := store.Turns.Insert(ctx, t); err != nil {
		rollback()
		return nil, err
	}
	if err := Enqueue(ctx, q, sess.TenantID, turnID, false); err != nil {
		return nil, errs.Wrap(errs.CodeUnavailable, err, "enqueue turn")
	}
	return t, nil
}

func lastSeq(ctx context.Context, store *repo.Store, sessionID string) (int64, error) {
	p, err := store.Messages.List(ctx, repo.Query{Filter: repo.Filter{"session_id": sessionID}, Desc: true, Limit: 20})
	if err != nil {
		return 0, err
	}
	var max int64
	for _, m := range p.Items {
		if m.Seq > max {
			max = m.Seq
		}
	}
	return max, nil
}
