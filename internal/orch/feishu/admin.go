package feishu

import (
	"context"
	"strings"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/infra/repo/scope"
)

// BindInput binds a Feishu group to the caller's tenant.
type BindInput struct {
	ChatID      string `json:"chat_id"`
	Name        string `json:"name,omitempty"`
	DefaultRole string `json:"default_role,omitempty"`
}

func requireAdmin(ctx context.Context) error {
	p, ok := tenant.From(ctx)
	if !ok {
		return errs.New(errs.CodeUnauthorized, "no principal")
	}
	if !p.IsAdmin() && p.Kind != tenant.KindSystem {
		return errs.New(errs.CodeForbidden, "requires tenant_admin")
	}
	return nil
}

// ListChats lists the chats bound (or once bound) to the tenant.
func (s *Service) ListChats(ctx context.Context) ([]*types.FeishuChat, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	return repo.All(ctx, s.Store.FeishuChats, repo.Query{}, 1000)
}

// BindChat binds a chat to the tenant. A chat belongs to at most one tenant:
// messages must map to exactly one place (§9.4).
func (s *Service) BindChat(ctx context.Context, in BindInput) (*types.FeishuChat, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	in.ChatID = strings.TrimSpace(in.ChatID)
	if in.ChatID == "" {
		return nil, errs.Invalid("chat_id is required")
	}
	if in.DefaultRole != "" {
		if _, err := repo.LatestPublished(ctx, s.Store.Roles, in.DefaultRole); err != nil {
			return nil, errs.Wrap(errs.CodeInvalid, err, "role %q is not available", in.DefaultRole)
		}
	}
	tid := tenant.MustID(ctx)
	others, err := repo.All(scope.AllTenants(ctx), s.Store.FeishuChats, repo.Query{Filter: repo.Filter{"chat_id": in.ChatID}}, 10)
	if err != nil {
		return nil, err
	}
	for _, c := range others {
		if c.TenantID != tid && c.Bound {
			return nil, errs.Conflict("chat %s is bound to another tenant", in.ChatID)
		}
	}
	var out *types.FeishuChat
	cur, err := s.Store.FeishuChats.FindOne(ctx, repo.Filter{"chat_id": in.ChatID})
	switch {
	case err == nil:
		out, err = repo.Update(ctx, s.Store.FeishuChats, cur.ID, func(c *types.FeishuChat) error {
			c.Name, c.DefaultRole, c.Bound = in.Name, in.DefaultRole, true
			return nil
		})
	case errs.Is(err, errs.CodeNotFound):
		out = &types.FeishuChat{ChatID: in.ChatID, Name: in.Name, DefaultRole: in.DefaultRole, Bound: true}
		err = s.Store.FeishuChats.Insert(ctx, out)
	}
	if err != nil {
		return nil, err
	}
	s.audit(ctx, "feishu.chat.bind", in.ChatID, map[string]any{"default_role": in.DefaultRole})
	return out, nil
}

// UnbindChat stops serving a chat; idOrChat is the record id or the chat id.
// The record is kept (bound=false) so threads stay explainable.
func (s *Service) UnbindChat(ctx context.Context, idOrChat string) (*types.FeishuChat, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	cur, err := s.Store.FeishuChats.Get(ctx, idOrChat)
	if errs.Is(err, errs.CodeNotFound) {
		cur, err = s.Store.FeishuChats.FindOne(ctx, repo.Filter{"chat_id": idOrChat})
	}
	if err != nil {
		return nil, err
	}
	out, err := repo.Update(ctx, s.Store.FeishuChats, cur.ID, func(c *types.FeishuChat) error {
		c.Bound = false
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.audit(ctx, "feishu.chat.unbind", out.ChatID, nil)
	return out, nil
}

// ListThreads lists thread bindings, optionally of one session.
func (s *Service) ListThreads(ctx context.Context, sessionID string) ([]*types.FeishuThread, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	f := repo.Filter{}
	if sessionID != "" {
		f["session_id"] = sessionID
	}
	return repo.All(ctx, s.Store.FeishuThreads, repo.Query{Filter: f, Desc: true}, 500)
}

func (s *Service) audit(ctx context.Context, action, target string, detail map[string]any) {
	p, _ := tenant.From(ctx)
	_ = s.Store.Audit.Insert(context.WithoutCancel(ctx), &types.AuditLog{Actor: p, Action: action, Target: target, Detail: detail})
}
