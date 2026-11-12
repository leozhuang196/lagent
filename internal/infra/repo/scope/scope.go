// Package scope lets a few platform-level use cases (scheduler sweeps, platform
// admin) read across tenants. depguard restricts who may import it (R-6).
package scope

import "context"

type allKey struct{}

// AllTenants marks ctx as unscoped: repositories skip the tenant filter on
// reads. Writes still use the entity's own tenant.
func AllTenants(ctx context.Context) context.Context {
	return context.WithValue(ctx, allKey{}, true)
}

// IsAll reports whether ctx is unscoped.
func IsAll(ctx context.Context) bool {
	v, _ := ctx.Value(allKey{}).(bool)
	return v
}
