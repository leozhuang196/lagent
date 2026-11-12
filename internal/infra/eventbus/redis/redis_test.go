package redis

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
)

func setup(t *testing.T, o Options) (*Bus, *miniredis.Miniredis, *goredis.Client) {
	t.Helper()
	m := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: m.Addr()})
	b := New(rdb, o)
	t.Cleanup(func() {
		_ = b.Close()
		_ = rdb.Close()
	})
	return b, m, rdb
}

func ev(tn, s string, i int) types.Event {
	return types.Event{TenantID: tn, Stream: s, Type: types.EvMessageDelta, Data: map[string]any{"i": float64(i)}}
}

func TestAppendReadConcurrent(t *testing.T) {
	m := miniredis.RunT(t)
	// Separate clients/buses model appenders in different processes.
	const writers, per = 4, 50
	var wg sync.WaitGroup
	seqs := make(chan int64, writers*per)
	for w := 0; w < writers; w++ {
		rdb := goredis.NewClient(&goredis.Options{Addr: m.Addr()})
		b := New(rdb, Options{})
		t.Cleanup(func() { _ = b.Close(); _ = rdb.Close() })
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				s, err := b.Append(context.Background(), ev("a", "s1", i))
				if err != nil {
					t.Error(err)
					return
				}
				seqs <- s
			}
		}()
	}
	wg.Wait()
	close(seqs)
	seen := map[int64]bool{}
	for s := range seqs {
		if seen[s] {
			t.Fatalf("duplicate seq %d", s)
		}
		seen[s] = true
	}

	rdb := goredis.NewClient(&goredis.Options{Addr: m.Addr()})
	defer rdb.Close()
	b := New(rdb, Options{})
	defer b.Close()
	ctx := tenant.SystemContext(context.Background(), "a")
	evs, reset, err := b.Read(ctx, "s1", 0, 10000)
	if err != nil || reset {
		t.Fatalf("read: %v %v", reset, err)
	}
	if len(evs) != writers*per {
		t.Fatalf("got %d events", len(evs))
	}
	for i, e := range evs {
		if e.Seq != int64(i+1) || e.TenantID != "a" || e.Stream != "s1" || e.TS.IsZero() {
			t.Fatalf("event %d: %+v", i, e)
		}
	}
	if n, _ := b.LastSeq(ctx, "s1"); n != writers*per {
		t.Fatalf("last seq %d", n)
	}
	// Tenants are isolated.
	other := tenant.SystemContext(context.Background(), "b")
	if evs, _, _ := b.Read(other, "s1", 0, 10); len(evs) != 0 {
		t.Fatal("tenant b sees tenant a events")
	}
	if _, _, err := b.Read(context.Background(), "s1", 0, 10); err == nil {
		t.Fatal("read without tenant must fail")
	}
}

func TestReadWindowAndReset(t *testing.T) {
	b, m, rdb := setup(t, Options{MaxLen: 10, TTL: time.Hour})
	ctx := tenant.SystemContext(context.Background(), "a")
	for i := 0; i < 5; i++ {
		if _, err := b.Append(ctx, ev("a", "s", i)); err != nil {
			t.Fatal(err)
		}
	}
	evs, reset, _ := b.Read(ctx, "s", 2, 2)
	if reset || len(evs) != 2 || evs[0].Seq != 3 || evs[1].Seq != 4 {
		t.Fatalf("window: %v %+v", reset, evs)
	}
	if evs[0].Data["i"] != float64(2) {
		t.Fatalf("payload %+v", evs[0].Data)
	}
	// Trim the head as MAXLEN would: cursor 1 is now older than the buffer.
	rdb.XTrimMaxLen(ctx, "t:a:events:s", 2)
	if _, reset, _ := b.Read(ctx, "s", 1, 10); !reset {
		t.Fatal("expected reset for trimmed cursor")
	}
	if evs, reset, _ := b.Read(ctx, "s", 3, 10); reset || len(evs) != 2 {
		t.Fatalf("cursor at oldest-1: %v %d", reset, len(evs))
	}
	// A cursor beyond the counter belongs to an expired incarnation.
	if _, reset, _ := b.Read(ctx, "s", 99, 10); !reset {
		t.Fatal("expected reset for future cursor")
	}

	// TTL is refreshed on append and the stream expires when idle; the
	// counter outlives it so seq continues.
	if ttl := m.TTL("t:a:events:s"); ttl != time.Hour {
		t.Fatalf("stream ttl %v", ttl)
	}
	m.FastForward(2 * time.Hour)
	if m.Exists("t:a:events:s") {
		t.Fatal("stream should have expired")
	}
	if _, reset, err := b.Read(ctx, "s", 2, 10); err != nil || !reset {
		t.Fatalf("expired empty buffer must reset: %v %v", reset, err)
	}
	if _, reset, err := b.Read(ctx, "s", 5, 10); err != nil || reset {
		t.Fatalf("caught-up cursor should not reset: %v %v", reset, err)
	}
	if _, reset, err := b.Read(ctx, "s", 1<<63-1, 10); err != nil || !reset {
		t.Fatalf("maximum cursor must reset without overflow: %v %v", reset, err)
	}
	if s, _ := b.Append(ctx, ev("a", "s", 9)); s != 6 {
		t.Fatalf("seq after expiry %d", s)
	}
	if _, reset, _ := b.Read(ctx, "s", 2, 10); !reset {
		t.Fatal("cursor before expired buffer must reset")
	}
	// Counter lost but stream kept (eviction): re-seeded from the stream.
	m.Del("t:a:events:s:seq")
	if s, err := b.Append(ctx, ev("a", "s", 10)); err != nil || s != 7 {
		t.Fatalf("seq after counter loss %d %v", s, err)
	}
}

func TestMaxLenTrims(t *testing.T) {
	b, _, rdb := setup(t, Options{MaxLen: 50})
	ctx := tenant.SystemContext(context.Background(), "a")
	for i := 0; i < 300; i++ {
		if _, err := b.Append(ctx, ev("a", "s", i)); err != nil {
			t.Fatal(err)
		}
	}
	n, _ := rdb.XLen(ctx, "t:a:events:s").Result()
	if n >= 300 {
		t.Fatalf("not trimmed: %d", n)
	}
	if _, reset, _ := b.Read(ctx, "s", 0, 10); !reset {
		t.Fatal("cursor 0 must reset once the head is trimmed")
	}
}

func TestSubscribe(t *testing.T) {
	b, _, _ := setup(t, Options{})
	ctx := tenant.SystemContext(context.Background(), "a")
	if _, err := b.Append(ctx, ev("a", "s", 0)); err != nil { // before subscribe: not delivered
		t.Fatal(err)
	}
	ch1, cancel1, err := b.Subscribe(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	ch2, cancel2, err := b.Subscribe(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	other, cancelOther, _ := b.Subscribe(tenant.SystemContext(context.Background(), "b"), "s")
	defer cancelOther()

	for i := 1; i <= 3; i++ {
		if _, err := b.Append(ctx, ev("a", "s", i)); err != nil {
			t.Fatal(err)
		}
	}
	for _, ch := range []<-chan types.Event{ch1, ch2} {
		for want := int64(2); want <= 4; want++ {
			select {
			case e := <-ch:
				if e.Seq != want {
					t.Fatalf("got seq %d want %d", e.Seq, want)
				}
			case <-time.After(3 * time.Second):
				t.Fatalf("timeout waiting for seq %d", want)
			}
		}
	}
	select {
	case e := <-other:
		t.Fatalf("tenant b got %+v", e)
	default:
	}

	cancel1()
	cancel1()
	if _, err := b.Append(ctx, ev("a", "s", 4)); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-ch2:
		if e.Seq != 5 {
			t.Fatalf("seq %d", e.Seq)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("remaining subscriber stopped receiving")
	}
	cancel2()

	// Re-subscribing after the last cancel works again.
	ch3, cancel3, err := b.Subscribe(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel3()
	if _, err := b.Append(ctx, ev("a", "s", 5)); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-ch3:
		if e.Seq != 6 {
			t.Fatalf("seq %d", e.Seq)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("resubscribe timeout")
	}
}
