package redis

import (
	"context"
	"math"
	"os"
	"strconv"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/eternityx/lagent/internal/core/tenant"
)

// Enable against a real Redis (including 6.0) without flushing any database.
// Only this test's unique stream/counter keys are created and removed.
func TestRealRedisReadCompatibility(t *testing.T) {
	addr := os.Getenv("LAGENT_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("LAGENT_TEST_REDIS_ADDR not set")
	}
	db := 0
	if value := os.Getenv("LAGENT_TEST_REDIS_DB"); value != "" {
		var err error
		db, err = strconv.Atoi(value)
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rdb := goredis.NewClient(&goredis.Options{Addr: addr, DB: db})
	bus := New(rdb, Options{})
	tn := "lagent-test-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	ctx = tenant.SystemContext(ctx, tn)
	key := streamKey(tn, "compat")
	t.Cleanup(func() {
		_ = bus.Close()
		_ = rdb.Del(context.Background(), key, seqKey(key)).Err()
		_ = rdb.Close()
	})
	for i := 0; i < 2; i++ {
		if _, err := bus.Append(ctx, ev(tn, "compat", i)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		after int64
		count int
		reset bool
	}{
		{0, 2, false}, {1, 1, false}, {2, 0, false}, {math.MaxInt64, 0, true},
	} {
		events, reset, err := bus.Read(ctx, "compat", tc.after, 10)
		if err != nil || reset != tc.reset || len(events) != tc.count {
			t.Fatalf("after=%d: events=%d reset=%v err=%v", tc.after, len(events), reset, err)
		}
	}
}
