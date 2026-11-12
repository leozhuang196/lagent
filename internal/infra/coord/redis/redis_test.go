package redis

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

func setup(t *testing.T) (*Coord, *miniredis.Miniredis) {
	t.Helper()
	m := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: m.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return New(rdb), m
}

func TestLeaseFencing(t *testing.T) {
	c, m := setup(t)
	ctx := context.Background()
	key := "t:a:session:lease:s1"

	tok1, ok, err := c.Acquire(ctx, key, time.Second)
	if err != nil || !ok || tok1 <= 0 {
		t.Fatalf("acquire: %d %v %v", tok1, ok, err)
	}
	if _, ok, _ := c.Acquire(ctx, key, time.Second); ok {
		t.Fatal("second acquire must fail while held")
	}
	if ok, _ := c.Renew(ctx, key, tok1, time.Second); !ok {
		t.Fatal("holder renew must succeed")
	}
	if ok, _ := c.Renew(ctx, key, tok1+1, time.Second); ok {
		t.Fatal("renew with wrong token must fail")
	}

	// Expiry, then takeover by another holder.
	m.FastForward(2 * time.Second)
	if ok, _ := c.Renew(ctx, key, tok1, time.Second); ok {
		t.Fatal("renew after expiry must fail")
	}
	tok2, ok, err := c.Acquire(ctx, key, time.Second)
	if err != nil || !ok || tok2 <= tok1 {
		t.Fatalf("takeover: tok2=%d tok1=%d ok=%v err=%v", tok2, tok1, ok, err)
	}
	if ok, _ := c.Renew(ctx, key, tok1, time.Second); ok {
		t.Fatal("old holder renew after takeover must fail")
	}
	// Release only by the holder.
	if err := c.Release(ctx, key, tok1); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := c.Acquire(ctx, key, time.Second); ok {
		t.Fatal("stale release must not free the lease")
	}
	if err := c.Release(ctx, key, tok2); err != nil {
		t.Fatal(err)
	}
	tok3, ok, err := c.Acquire(ctx, key, time.Second)
	if err != nil || !ok || tok3 <= tok2 {
		t.Fatalf("after release: tok3=%d tok2=%d ok=%v err=%v", tok3, tok2, ok, err)
	}
}

// Tokens must keep increasing even when Redis loses the counter (P2).
func TestLeaseFencingSurvivesCounterLoss(t *testing.T) {
	c, m := setup(t)
	ctx := context.Background()
	key := "t:a:session:lease:s2"
	m.SetTime(time.Unix(1_700_000_000, 0))
	tok1, ok, _ := c.Acquire(ctx, key, time.Second)
	if !ok {
		t.Fatal("acquire")
	}
	m.FlushAll()
	m.SetTime(time.Unix(1_700_000_001, 0))
	tok2, ok, _ := c.Acquire(ctx, key, time.Second)
	if !ok || tok2 <= tok1 {
		t.Fatalf("tok2=%d tok1=%d", tok2, tok1)
	}
}

func TestSemaphore(t *testing.T) {
	c, m := setup(t)
	ctx := context.Background()
	t0 := time.Unix(1_700_000_000, 0)
	m.SetTime(t0)

	r1, ok1, err := c.TryAcquire(ctx, "sem", 2, time.Minute)
	_, ok2, _ := c.TryAcquire(ctx, "sem", 2, time.Minute)
	_, ok3, _ := c.TryAcquire(ctx, "sem", 2, time.Minute)
	if err != nil || !ok1 || !ok2 || ok3 {
		t.Fatalf("limit: %v %v %v %v", ok1, ok2, ok3, err)
	}
	r1()
	r1() // idempotent
	if _, ok, _ := c.TryAcquire(ctx, "sem", 2, time.Minute); !ok {
		t.Fatal("slot must be free after release")
	}
	if _, ok, _ := c.TryAcquire(ctx, "sem", 2, time.Minute); ok {
		t.Fatal("limit again")
	}
	// Crashed holders' slots expire.
	m.SetTime(t0.Add(time.Minute + time.Second))
	if _, ok, _ := c.TryAcquire(ctx, "sem", 2, time.Minute); !ok {
		t.Fatal("expired slots must be purged")
	}
	// limit <= 0 means unlimited.
	for i := 0; i < 5; i++ {
		if _, ok, _ := c.TryAcquire(ctx, "free", 0, time.Minute); !ok {
			t.Fatal("unlimited")
		}
	}
}

func TestCounter(t *testing.T) {
	c, m := setup(t)
	ctx := context.Background()
	if n, _ := c.Get(ctx, "cnt"); n != 0 {
		t.Fatalf("empty get = %d", n)
	}
	if n, _ := c.IncrBy(ctx, "cnt", 5, time.Minute); n != 5 {
		t.Fatalf("incr = %d", n)
	}
	m.FastForward(30 * time.Second)
	if n, _ := c.IncrBy(ctx, "cnt", 3, time.Minute); n != 8 {
		t.Fatalf("incr = %d", n)
	}
	// The window does not slide: expires one minute after creation.
	m.FastForward(31 * time.Second)
	if n, _ := c.Get(ctx, "cnt"); n != 0 {
		t.Fatalf("after ttl get = %d", n)
	}
	if n, _ := c.IncrBy(ctx, "cnt", 1, time.Minute); n != 1 {
		t.Fatalf("restart = %d", n)
	}
}

func TestSignal(t *testing.T) {
	c, _ := setup(t)
	ctx := context.Background()
	ch, cancel, err := c.Subscribe(ctx, "ctl")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if err := c.Publish(ctx, "ctl", []byte("cancel")); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-ch:
		if string(msg) != "cancel" {
			t.Fatalf("got %q", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no message")
	}
	cancel()
	cancel()
}
