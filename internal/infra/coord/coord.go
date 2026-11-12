// Package coord holds cross-instance coordination primitives (Redis in
// production): execution leases with fencing tokens, control signals,
// counters and semaphores. Coordination never stores facts (P2).
package coord

import (
	"context"
	"time"
)

// Lease guarantees at most one holder per key.
type Lease interface {
	// Acquire returns a fencing token that strictly increases per key.
	// ok is false when someone else holds the lease.
	Acquire(ctx context.Context, key string, ttl time.Duration) (token int64, ok bool, err error)
	// Renew extends the lease; ok is false when it was lost (expired or taken).
	Renew(ctx context.Context, key string, token int64, ttl time.Duration) (ok bool, err error)
	// Release frees the lease if token still holds it.
	Release(ctx context.Context, key string, token int64) error
}

// Signal is best-effort pub/sub: messages published while a subscriber is
// disconnected are lost. Correctness must never depend on it.
type Signal interface {
	Publish(ctx context.Context, channel string, msg []byte) error
	Subscribe(ctx context.Context, channel string) (ch <-chan []byte, cancel func(), err error)
}

// Counter is an expiring integer counter (budgets, rate accounting).
type Counter interface {
	IncrBy(ctx context.Context, key string, n int64, ttl time.Duration) (int64, error)
	Get(ctx context.Context, key string) (int64, error)
}

// Semaphore bounds concurrency across instances (model calls, tenant turns).
type Semaphore interface {
	// TryAcquire takes one slot of key out of limit; release returns it. Slots
	// expire after ttl so a crashed holder cannot leak them forever.
	TryAcquire(ctx context.Context, key string, limit int, ttl time.Duration) (release func(), ok bool, err error)
}

// Acquire blocks until a slot is free or ctx ends.
func Acquire(ctx context.Context, s Semaphore, key string, limit int, ttl time.Duration) (func(), error) {
	backoff := 50 * time.Millisecond
	for {
		rel, ok, err := s.TryAcquire(ctx, key, limit, ttl)
		if err != nil {
			return nil, err
		}
		if ok {
			return rel, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < time.Second {
			backoff *= 2
		}
	}
}

// Keys used across the platform. All are tenant-prefixed by callers.
func SessionLeaseKey(tenantID, sessionID string) string {
	return "t:" + tenantID + ":session:lease:" + sessionID
}
func SessionControlChannel(tenantID, sessionID string) string {
	return "t:" + tenantID + ":session:control:" + sessionID
}
func TenantTurnsKey(tenantID string) string { return "t:" + tenantID + ":turns" }
func TenantDailyTokensKey(tenantID string, day string) string {
	return "t:" + tenantID + ":tokens:" + day
}
func ModelKey(provider string) string { return "llm:" + provider }
