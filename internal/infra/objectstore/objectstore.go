// Package objectstore exposes object semantics only (P5): no file handles,
// random access, rename or real directories.
package objectstore

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/eternityx/lagent/internal/core/tenant"
)

// ObjectInfo describes a stored object.
type ObjectInfo struct {
	Key         string    `json:"key"`
	Size        int64     `json:"size"`
	ContentType string    `json:"content_type,omitempty"`
	ModTime     time.Time `json:"mod_time"`
}

// Store is an object store.
type Store interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)
	List(ctx context.Context, prefix string) ([]ObjectInfo, error)
	Delete(ctx context.Context, key string) error
	// SignedURL returns a time-limited download URL.
	SignedURL(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// TenantKey prefixes rel with the tenant of ctx: tenants/{tenant}/{rel}.
func TenantKey(ctx context.Context, rel string) (string, error) {
	t, err := tenant.ID(ctx)
	if err != nil {
		return "", err
	}
	return "tenants/" + t + "/" + strings.TrimPrefix(rel, "/"), nil
}

// OwnedBy reports whether key belongs to tenantID.
func OwnedBy(key, tenantID string) bool {
	return strings.HasPrefix(key, "tenants/"+tenantID+"/")
}
