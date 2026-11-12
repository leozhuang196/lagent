// Package eventbus is the per-stream ordered event log (Redis Stream in production).
// It is a buffer with TTL, not the source of truth.
package eventbus

import (
	"context"

	"github.com/eternityx/lagent/internal/core/types"
)

// Bus appends and reads events of a stream (a session or a run).
type Bus interface {
	// Append assigns Seq (strictly increasing per stream) and TS, and returns Seq.
	// The tenant is taken from ev.TenantID.
	Append(ctx context.Context, ev types.Event) (int64, error)
	// Read returns events with Seq > after (up to limit). reset is true when
	// after is older than the retained buffer: the client must reload a snapshot.
	Read(ctx context.Context, stream string, after int64, limit int) (evs []types.Event, reset bool, err error)
	// Subscribe delivers events appended after the call. cancel stops delivery.
	Subscribe(ctx context.Context, stream string) (ch <-chan types.Event, cancel func(), err error)
	// LastSeq returns the newest Seq of a stream (0 if empty).
	LastSeq(ctx context.Context, stream string) (int64, error)
}
