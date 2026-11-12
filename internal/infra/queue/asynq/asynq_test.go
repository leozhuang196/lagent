package asynq

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/hibiken/asynq"
	goredis "github.com/redis/go-redis/v9"

	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/infra/queue"
)

func TestJobTaskMapping(t *testing.T) {
	at := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	j := queue.Job{
		Type: queue.TypeTurnRun, TenantID: "acme", Payload: json.RawMessage(`{"turn_id":"t1"}`),
		UniqueKey: "turn:t1", ProcessAt: at, Timeout: time.Minute, Queue: "tenant-acme", Attempt: 7,
	}
	task, opts, err := toTask(j)
	if err != nil {
		t.Fatal(err)
	}
	if task.Type() != queue.TypeTurnRun {
		t.Fatalf("type %q", task.Type())
	}
	got := map[asynq.OptionType]any{}
	for _, o := range opts {
		got[o.Type()] = o.Value()
	}
	want := map[asynq.OptionType]any{
		asynq.QueueOpt:     "tenant-acme",
		asynq.MaxRetryOpt:  DefaultMaxRetry,
		asynq.TaskIDOpt:    "acme:turn:t1",
		asynq.ProcessAtOpt: at,
		asynq.TimeoutOpt:   time.Minute,
	}
	for k, v := range want {
		if g, ok := got[k]; !ok || g != v {
			t.Fatalf("option %v = %v, want %v", k, g, v)
		}
	}

	back, err := fromTask(task, 2)
	if err != nil {
		t.Fatal(err)
	}
	if back.TenantID != "acme" || back.Type != j.Type || string(back.Payload) != `{"turn_id":"t1"}` ||
		back.UniqueKey != j.UniqueKey || back.MaxRetry != DefaultMaxRetry || back.Queue != j.Queue ||
		!back.ProcessAt.Equal(at) || back.Timeout != time.Minute || back.Attempt != 2 {
		t.Fatalf("round trip: %+v", back)
	}

	// Defaults: default queue, no task id, explicit MaxRetry kept.
	_, opts, _ = toTask(queue.Job{Type: "x", TenantID: "a", MaxRetry: 9})
	got = map[asynq.OptionType]any{}
	for _, o := range opts {
		got[o.Type()] = o.Value()
	}
	if got[asynq.QueueOpt] != DefaultQueue || got[asynq.MaxRetryOpt] != 9 || got[asynq.TaskIDOpt] != nil || got[asynq.ProcessAtOpt] != nil {
		t.Fatalf("defaults: %v", got)
	}
	if _, _, err := toTask(queue.Job{}); err == nil {
		t.Fatal("job without type must fail")
	}
}

func TestProcessDispatch(t *testing.T) {
	s := &Server{log: nil, handlers: map[string]queue.Handler{}}
	s.log = slogDiscard()
	var gotTenant string
	var gotAttempt int
	s.Handle("ok", func(ctx context.Context, j queue.Job) error {
		gotTenant = tenant.MustID(ctx)
		gotAttempt = j.Attempt
		return nil
	})
	s.Handle("skip", func(context.Context, queue.Job) error { return queue.SkipRetry(errors.New("bad input")) })
	s.Handle("fail", func(context.Context, queue.Job) error { return errors.New("transient") })
	s.Handle("panic", func(context.Context, queue.Job) error { panic("boom") })

	task := func(typ string) *asynq.Task {
		tk, _, err := toTask(queue.Job{Type: typ, TenantID: "acme"})
		if err != nil {
			t.Fatal(err)
		}
		return tk
	}
	ctx := context.Background()
	if err := s.process(ctx, task("ok")); err != nil || gotTenant != "acme" || gotAttempt != 0 {
		t.Fatalf("ok: %v %q %d", err, gotTenant, gotAttempt)
	}
	if err := s.process(ctx, task("skip")); !errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("skip: %v", err)
	}
	if err := s.process(ctx, task("fail")); err == nil || errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("fail: %v", err)
	}
	if err := s.process(ctx, task("panic")); !errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("panic: %v", err)
	}
	if err := s.process(ctx, task("unknown")); err == nil || errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("unknown type should retry: %v", err)
	}
	if err := s.process(ctx, asynq.NewTask("ok", []byte("not json"))); !errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("bad payload: %v", err)
	}
}

func TestRetryDelay(t *testing.T) {
	prev := time.Duration(0)
	for n := 0; n < 15; n++ {
		d := retryDelay(n, nil, nil)
		base := time.Second << min(n, 10)
		if base > 10*time.Minute {
			base = 10 * time.Minute
		}
		if d < base || d > base+base/4 {
			t.Fatalf("n=%d delay %v", n, d)
		}
		if d+d/4 < prev {
			t.Fatalf("not increasing at %d", n)
		}
		prev = d
	}
}

// TestIntegration runs asynq end to end on miniredis (asynq's scripts work
// there), or on a real Redis when LAGENT_TEST_REDIS_ADDR is set.
func TestIntegration(t *testing.T) {
	addr := os.Getenv("LAGENT_TEST_REDIS_ADDR")
	if addr == "" {
		addr = miniredis.RunT(t).Addr()
	}
	runIntegration(t, addr)
}

func runIntegration(t *testing.T, addr string) {
	rdb := goredis.NewClient(&goredis.Options{Addr: addr, DB: 15})
	defer rdb.Close()
	ctx := context.Background()
	if err := rdb.FlushDB(ctx).Err(); err != nil {
		t.Fatal(err)
	}

	p := NewProducer(rdb)
	srv := NewServer(rdb, ServerOptions{Concurrency: 4, Queues: []string{"tenant-x"}, Log: slogDiscard()})
	var mu sync.Mutex
	attempts := map[string][]int{}
	done := make(chan string, 16)
	handler := func(ctx context.Context, j queue.Job) error {
		mu.Lock()
		attempts[j.UniqueKey] = append(attempts[j.UniqueKey], j.Attempt)
		mu.Unlock()
		if tenant.MustID(ctx) != j.TenantID {
			t.Errorf("tenant %q != %q", tenant.MustID(ctx), j.TenantID)
		}
		switch j.UniqueKey {
		case "flaky":
			if j.Attempt == 0 {
				return errors.New("transient")
			}
		case "perm":
			defer func() { done <- j.UniqueKey }()
			return queue.SkipRetry(errors.New("permanent"))
		}
		done <- j.UniqueKey
		return nil
	}
	srv.Handle("t.test", handler)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := srv.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	enq := func(key, q string) {
		j, _ := queue.NewJob("t.test", "tenant-a", map[string]string{"k": key})
		j.UniqueKey, j.Queue = key, q
		if err := p.Enqueue(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	// Delayed so the duplicate is still pending when enqueued.
	j, _ := queue.NewJob("t.test", "tenant-a", nil)
	j.UniqueKey, j.ProcessAt = "dup", time.Now().Add(2*time.Second)
	for i := 0; i < 3; i++ {
		if err := p.Enqueue(ctx, j); err != nil {
			t.Fatalf("duplicate enqueue: %v", err)
		}
	}
	enq("plain", "")
	enq("dedicated", "tenant-x")
	enq("flaky", "")
	enq("perm", "")

	want := map[string]bool{"dup": true, "plain": true, "dedicated": true, "flaky": true, "perm": true}
	deadline := time.After(30 * time.Second)
	for len(want) > 0 {
		select {
		case k := <-done:
			delete(want, k)
		case <-deadline:
			t.Fatalf("missing %v", want)
		}
	}
	time.Sleep(time.Second)
	mu.Lock()
	if len(attempts["dup"]) != 1 {
		t.Fatalf("dup ran %d times", len(attempts["dup"]))
	}
	if a := attempts["flaky"]; len(a) != 2 || a[0] != 0 || a[1] != 1 {
		t.Fatalf("flaky attempts %v", a)
	}
	if len(attempts["perm"]) != 1 {
		t.Fatalf("perm ran %v", attempts["perm"])
	}
	mu.Unlock()
	// The archived "perm" task must not block its key forever.
	enq("perm", "")
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("re-enqueue after archive did not run")
	}
}

func slogDiscard() *slog.Logger { return slog.New(slog.DiscardHandler) }
