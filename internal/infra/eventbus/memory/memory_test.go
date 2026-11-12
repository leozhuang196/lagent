package memory

import (
	"context"
	"testing"

	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
)

func TestReadResetContract(t *testing.T) {
	b := New(2, nil)
	ctx := tenant.SystemContext(context.Background(), "t")
	for i := 0; i < 3; i++ {
		if _, err := b.Append(ctx, types.Event{TenantID: "t", Stream: "s"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []struct {
		after int64
		reset bool
		count int
	}{{0, true, 2}, {1, false, 2}, {3, false, 0}, {4, true, 0}} {
		events, reset, err := b.Read(ctx, "s", tt.after, 10)
		if err != nil || reset != tt.reset || len(events) != tt.count {
			t.Fatalf("after %d: %d %v %v", tt.after, len(events), reset, err)
		}
	}
	if _, reset, err := b.Read(ctx, "missing", 99, 10); err != nil || !reset {
		t.Fatalf("missing incarnation: %v %v", reset, err)
	}
}
