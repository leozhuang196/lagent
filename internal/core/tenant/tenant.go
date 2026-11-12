// Package tenant carries the caller identity through context.
//
// Every request belongs to exactly one tenant. Transport puts a Principal into
// the context once; repositories read the tenant from it and never accept it
// as an argument, so business code cannot forget or bypass the filter.
package tenant

import (
	"context"

	"github.com/eternityx/lagent/internal/core/errs"
)

// System is the tenant that owns built-in, read-only assets.
const System = "_system"

// PrincipalKind is how the caller authenticated.
type PrincipalKind string

const (
	KindAPIKey     PrincipalKind = "api_key"
	KindFeishuUser PrincipalKind = "feishu_user"
	KindSystem     PrincipalKind = "system" // scheduler, workers, internal resumes
)

// Role is the caller's role inside its tenant.
type Role string

const (
	RoleMember        Role = "member"
	RoleTenantAdmin   Role = "tenant_admin"
	RolePlatformAdmin Role = "platform_admin"
)

// Principal identifies who is acting.
type Principal struct {
	Kind     PrincipalKind `json:"kind" bson:"kind"`
	ID       string        `json:"id" bson:"id"`
	TenantID string        `json:"tenant_id" bson:"tenant_id"`
	Role     Role          `json:"role" bson:"role"`
	Name     string        `json:"name,omitempty" bson:"name,omitempty"`
	// MaxPermissionMode caps what sessions created by this principal may use.
	MaxPermissionMode string `json:"max_permission_mode,omitempty" bson:"max_permission_mode,omitempty"`
}

// IsAdmin reports whether p administers its tenant.
func (p Principal) IsAdmin() bool {
	return p.Role == RoleTenantAdmin || p.Role == RolePlatformAdmin
}

type ctxKey struct{}

// With returns a context carrying p.
func With(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// From returns the principal in ctx.
func From(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok && p.TenantID != ""
}

// ID returns the tenant id in ctx or an error when absent.
func ID(ctx context.Context) (string, error) {
	p, ok := From(ctx)
	if !ok {
		return "", errs.New(errs.CodeUnauthorized, "no tenant in context")
	}
	return p.TenantID, nil
}

// MustID is ID for code paths where a missing tenant is a programming error.
func MustID(ctx context.Context) string {
	id, err := ID(ctx)
	if err != nil {
		panic(err)
	}
	return id
}

// SystemContext returns a context acting as an internal system principal of tenantID.
func SystemContext(ctx context.Context, tenantID string) context.Context {
	return With(ctx, Principal{Kind: KindSystem, ID: "system", TenantID: tenantID, Role: RoleTenantAdmin})
}
