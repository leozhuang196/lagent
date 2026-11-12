// Package memory is an in-process queue with delays, retries and unique keys.
package memory

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/infra/queue"
)

// Queue is both the producer and the server.
type Queue struct {
	Workers int
	log     *slog.Logger

	mu       sync.Mutex
	cond     *sync.Cond
	pending  []queue.Job
	unique   map[string]bool
	handlers map[string]queue.Handler
	stopped  bool
	running  int
	wg       sync.WaitGroup
}

func New(workers int, log *slog.Logger) *Queue {
	if workers <= 0 {
		workers = 8
	}
	if log == nil {
		log = slog.Default()
	}
	q := &Queue{Workers: workers, log: log, unique: map[string]bool{}, handlers: map[string]queue.Handler{}}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *Queue) Enqueue(_ context.Context, j queue.Job) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if j.UniqueKey != "" {
		if q.unique[j.UniqueKey] {
			return nil
		}
		q.unique[j.UniqueKey] = true
	}
	if j.MaxRetry == 0 {
		j.MaxRetry = 3
	}
	q.pending = append(q.pending, j)
	q.cond.Broadcast()
	return nil
}

func (q *Queue) Handle(typ string, h queue.Handler) {
	q.mu.Lock()
	q.handlers[typ] = h
	q.mu.Unlock()
}

// Start launches workers and a ticker that wakes them for delayed jobs.
func (q *Queue) Start(ctx context.Context) error {
	for i := 0; i < q.Workers; i++ {
		q.wg.Add(1)
		go q.worker(ctx)
	}
	q.wg.Add(1)
	go func() {
		defer q.wg.Done()
		t := time.NewTicker(20 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				q.Stop()
				return
			case <-t.C:
				q.mu.Lock()
				if q.stopped {
					q.mu.Unlock()
					return
				}
				q.cond.Broadcast()
				q.mu.Unlock()
			}
		}
	}()
	return nil
}

func (q *Queue) Stop() {
	q.mu.Lock()
	q.stopped = true
	q.cond.Broadcast()
	q.mu.Unlock()
}

// Wait stops the queue and waits for workers (tests).
func (q *Queue) Wait() {
	q.Stop()
	q.wg.Wait()
}

// Idle blocks until no job is pending or running, or timeout (tests).
func (q *Queue) Idle(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	quiet := 0
	for time.Now().Before(deadline) {
		q.mu.Lock()
		n := len(q.pending) + q.running
		q.mu.Unlock()
		if n == 0 {
			quiet++
			if quiet >= 3 { // stay idle across a few polls: handlers may enqueue follow-ups
				return true
			}
		} else {
			quiet = 0
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func (q *Queue) next() (queue.Job, queue.Handler, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for {
		if q.stopped {
			return queue.Job{}, nil, false
		}
		now := time.Now()
		for i, j := range q.pending {
			if j.ProcessAt.IsZero() || !now.Before(j.ProcessAt) {
				q.pending = append(q.pending[:i:i], q.pending[i+1:]...)
				if j.UniqueKey != "" {
					delete(q.unique, j.UniqueKey)
				}
				q.running++
				return j, q.handlers[j.Type], true
			}
		}
		q.cond.Wait()
	}
}

func (q *Queue) worker(ctx context.Context) {
	defer q.wg.Done()
	for {
		j, h, ok := q.next()
		if !ok {
			return
		}
		q.run(ctx, j, h)
		q.mu.Lock()
		q.running--
		q.mu.Unlock()
	}
}

func (q *Queue) run(ctx context.Context, j queue.Job, h queue.Handler) {
	if h == nil {
		q.log.Warn("no handler for job", "type", j.Type)
		return
	}
	jctx := tenant.SystemContext(ctx, j.TenantID)
	var cancel context.CancelFunc
	if j.Timeout > 0 {
		jctx, cancel = context.WithTimeout(jctx, j.Timeout)
	} else {
		jctx, cancel = context.WithCancel(jctx)
	}
	err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				q.log.Error("job panic", "type", j.Type, "panic", r)
				err = queue.SkipRetry(context.Canceled)
			}
		}()
		return h(jctx, j)
	}()
	cancel()
	if err == nil || queue.IsSkipRetry(err) || ctx.Err() != nil {
		if err != nil {
			q.log.Warn("job failed permanently", "type", j.Type, "err", err)
		}
		return
	}
	if j.Attempt >= j.MaxRetry {
		q.log.Warn("job retries exhausted", "type", j.Type, "err", err)
		return
	}
	j.Attempt++
	j.ProcessAt = time.Now().Add(time.Duration(j.Attempt*j.Attempt) * 50 * time.Millisecond)
	q.log.Info("job retry", "type", j.Type, "attempt", j.Attempt, "err", err)
	_ = q.Enqueue(ctx, j)
}
