// Package admin holds the administration use cases (§9.3, §11, §12): tenants
// and keys, the release process of versioned assets (roles, skills, workflow
// templates), memory maintenance, usage, audit, the turn debugger and platform
// status. It is one of the few packages allowed to read across tenants
// (repo/scope, R-6); every cross-tenant read of tenant content is audited.
package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"strings"
	"time"

	"github.com/eternityx/lagent/internal/capability/tool"
	"github.com/eternityx/lagent/internal/core/clock"
	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/coord"
	"github.com/eternityx/lagent/internal/infra/eventbus"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/infra/repo/scope"
	"github.com/eternityx/lagent/internal/orch/session"
)

// Service implements administration. Only Store is required for the tenant
// and key use cases; the optional collaborators enable the rest and are
// filled by the app wiring (ext_admin.go).
type Service struct {
	Store *repo.Store
	Clock clock.Clock
	Log   *slog.Logger
	// Registry validates role tool pickers and resolves tools for assembly
	// previews. When nil, tool names are not checked.
	Registry *tool.Registry
	// Sessions creates debug sessions (playground, fork).
	Sessions *session.Service
	// Counter reads today's live token counter of a tenant.
	Counter coord.Counter
	// Bus provides the events of a turn timeline.
	Bus eventbus.Bus
	// Info describes this process for the platform status page.
	Info PlatformInfo
}

// PlatformInfo is static information about the running process.
type PlatformInfo struct {
	StoreDriver  string   `json:"store_driver"`
	QueueDriver  string   `json:"queue_driver,omitempty"`
	Redis        bool     `json:"redis"`
	ProcessRoles []string `json:"process_roles"`
	DefaultModel string   `json:"default_model,omitempty"`
}

func (s *Service) now() time.Time {
	if s.Clock == nil {
		return time.Now()
	}
	return s.Clock.Now()
}

// NewKey generates a plaintext key "lk_<prefix>_<secret>", its prefix and hash.
func NewKey() (plain, prefix, hash string) {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	p := make([]byte, 6)
	_, _ = rand.Read(p)
	prefix = hex.EncodeToString(p)
	plain = "lk_" + prefix + "_" + base64.RawURLEncoding.EncodeToString(b)
	return plain, prefix, HashKey(plain)
}

// HashKey hashes a plaintext key.
func HashKey(plain string) string {
	h := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(h[:])
}

func requirePlatform(ctx context.Context) error {
	p, _ := tenant.From(ctx)
	if p.Role != tenant.RolePlatformAdmin && p.Kind != tenant.KindSystem {
		return errs.New(errs.CodeForbidden, "platform admin only")
	}
	return nil
}

func requireAdmin(ctx context.Context) error {
	p, _ := tenant.From(ctx)
	if !p.IsAdmin() && p.Kind != tenant.KindSystem {
		return errs.New(errs.CodeForbidden, "tenant admin only")
	}
	return nil
}

// CreateTenant creates a tenant with id (slug).
func (s *Service) CreateTenant(ctx context.Context, t *types.Tenant) (*types.Tenant, error) {
	if err := requirePlatform(ctx); err != nil {
		return nil, err
	}
	if t.ID == "" || strings.ContainsAny(t.ID, " /:") || t.ID == tenant.System {
		return nil, errs.Invalid("tenant id must be a slug")
	}
	if t.Status == "" {
		t.Status = "active"
	}
	t.TenantID = t.ID
	if err := s.Store.Tenants.Insert(tenant.SystemContext(ctx, t.ID), t); err != nil {
		return nil, err
	}
	s.audit(ctx, "tenant.create", t.ID, nil)
	return t, nil
}

// ListTenants lists every tenant (platform admin).
func (s *Service) ListTenants(ctx context.Context) ([]*types.Tenant, error) {
	if err := requirePlatform(ctx); err != nil {
		return nil, err
	}
	return repo.All(scope.AllTenants(ctx), s.Store.Tenants, repo.Query{}, 1000)
}

// UpdateTenant changes quotas and defaults of the caller's tenant (or any, for platform admins).
func (s *Service) UpdateTenant(ctx context.Context, id string, fn func(t *types.Tenant)) (*types.Tenant, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	p, _ := tenant.From(ctx)
	if id != p.TenantID && p.Role != tenant.RolePlatformAdmin {
		return nil, errs.New(errs.CodeForbidden, "other tenant")
	}
	t, err := repo.Update(tenant.SystemContext(ctx, id), s.Store.Tenants, id, func(t *types.Tenant) error { fn(t); return nil })
	if err == nil {
		s.audit(ctx, "tenant.update", id, nil)
	}
	return t, err
}

// CreatedKey carries the plaintext exactly once.
type CreatedKey struct {
	Key       *types.APIKey `json:"key"`
	Plaintext string        `json:"plaintext"`
}

// CreateKey issues a key in the caller's tenant. Only platform admins can mint
// platform-admin keys; the permission cap defaults to the strictest mode.
func (s *Service) CreateKey(ctx context.Context, name string, role tenant.Role, maxMode types.PermissionMode) (*CreatedKey, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	p, _ := tenant.From(ctx)
	switch role {
	case tenant.RoleMember, tenant.RoleTenantAdmin:
	case tenant.RolePlatformAdmin:
		if p.Role != tenant.RolePlatformAdmin {
			return nil, errs.New(errs.CodeForbidden, "cannot mint platform admin keys")
		}
	default:
		return nil, errs.Invalid("unknown role %q", role)
	}
	if maxMode == "" {
		maxMode = types.ModeAskAll
	}
	if !maxMode.Valid() {
		return nil, errs.Invalid("unknown permission mode %q", maxMode)
	}
	plain, prefix, hash := NewKey()
	k := &types.APIKey{Name: name, Prefix: prefix, Hash: hash, Role: role, MaxPermissionMode: maxMode}
	if err := s.Store.APIKeys.Insert(ctx, k); err != nil {
		return nil, err
	}
	s.audit(ctx, "key.create", k.ID, map[string]any{"role": role, "max_permission_mode": maxMode})
	return &CreatedKey{Key: k, Plaintext: plain}, nil
}

// ListKeys lists the tenant's keys.
func (s *Service) ListKeys(ctx context.Context) ([]*types.APIKey, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	return repo.All(ctx, s.Store.APIKeys, repo.Query{}, 1000)
}

// SetKeyDisabled revokes or restores a key.
func (s *Service) SetKeyDisabled(ctx context.Context, id string, disabled bool) (*types.APIKey, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	k, err := repo.Update(ctx, s.Store.APIKeys, id, func(k *types.APIKey) error { k.Disabled = disabled; return nil })
	if err == nil {
		s.audit(ctx, "key.disable", id, map[string]any{"disabled": disabled})
	}
	return k, err
}

// ByPrefix implements the transport's key lookup (across tenants).
func (s *Service) ByPrefix(ctx context.Context, prefix string) (*types.APIKey, error) {
	return s.Store.APIKeys.FindOne(scope.AllTenants(ctx), repo.Filter{"prefix": prefix})
}

// ByID implements the transport's key lookup (across tenants).
func (s *Service) ByID(ctx context.Context, id string) (*types.APIKey, error) {
	return s.Store.APIKeys.Get(scope.AllTenants(ctx), id)
}

func (s *Service) audit(ctx context.Context, action, target string, detail map[string]any) {
	p, _ := tenant.From(ctx)
	s.auditIn(ctx, p.TenantID, action, target, "", detail)
}

// auditIn writes an audit record into tenantID's log. Break-glass reads are
// recorded in the tenant whose data was read, so its admins can see them.
func (s *Service) auditIn(ctx context.Context, tenantID, action, target, reason string, detail map[string]any) {
	p, _ := tenant.From(ctx)
	if tenantID == "" {
		return
	}
	a := &types.AuditLog{Actor: p, Action: action, Target: target, Detail: detail, Reason: reason}
	a.TenantID = tenantID
	if err := s.Store.Audit.Insert(context.WithoutCancel(tenant.SystemContext(ctx, tenantID)), a); err != nil && s.Log != nil {
		s.Log.Warn("audit", "err", err)
	}
}

func requireMember(ctx context.Context) (tenant.Principal, error) {
	p, ok := tenant.From(ctx)
	if !ok {
		return p, errs.New(errs.CodeUnauthorized, "no principal")
	}
	return p, nil
}
