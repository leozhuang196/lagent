// Package secret stores credentials outside the main database. Secrets never
// enter prompts, context, traces or API responses.
package secret

import (
	"context"
	"strings"

	"github.com/eternityx/lagent/internal/core/tenant"
)

// Store reads and writes secrets by reference.
type Store interface {
	Get(ctx context.Context, ref string) (string, error)
	Put(ctx context.Context, ref, value string) error
	Delete(ctx context.Context, ref string) error
}

// TenantRef namespaces a reference: lagent/{tenant}/{name}.
func TenantRef(ctx context.Context, name string) (string, error) {
	t, err := tenant.ID(ctx)
	if err != nil {
		return "", err
	}
	return "lagent/" + t + "/" + strings.TrimPrefix(name, "/"), nil
}

// Allowed reports whether ctx's tenant may read ref (its own or platform-level).
func Allowed(ctx context.Context, ref string) bool {
	t, err := tenant.ID(ctx)
	if err != nil {
		return false
	}
	return strings.HasPrefix(ref, "lagent/"+t+"/") || strings.HasPrefix(ref, "platform/")
}
