// Package asynq implements queue.Queue and queue.Server on hibiken/asynq.
//
// The task payload is the whole queue.Job as JSON so TenantID and the job's
// own options survive the trip; Attempt is filled from asynq's retry count.
package asynq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/hibiken/asynq"
	goredis "github.com/redis/go-redis/v9"

	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/infra/queue"
)

const (
	// DefaultQueue is used when a job does not name one.
	DefaultQueue = "default"
	// DefaultMaxRetry matches the memory queue (asynq's own default is 25).
	DefaultMaxRetry = 3
)

// Producer implements queue.Queue.
type Producer struct {
	client    *asynq.Client
	inspector *asynq.Inspector
}

// NewProducer enqueues through rdb. The caller owns (and closes) rdb.
func NewProducer(rdb goredis.UniversalClient) *Producer {
	return &Producer{client: asynq.NewClientFromRedisClient(rdb), inspector: asynq.NewInspectorFromRedisClient(rdb)}
}

// Close is a no-op: asynq refuses to close a shared client/inspector, and the
// redis client belongs to the caller.
func (p *Producer) Close() error { return nil }

// taskID scopes UniqueKey by tenant so two tenants cannot dedupe each
// other's jobs.
func taskID(j queue.Job) string {
	if j.UniqueKey == "" {
		return ""
	}
	return j.TenantID + ":" + j.UniqueKey
}

func queueName(j queue.Job) string {
	if j.Queue == "" {
		return DefaultQueue
	}
	return j.Queue
}

// toTask maps a job to an asynq task and its options.
func toTask(j queue.Job) (*asynq.Task, []asynq.Option, error) {
	if j.Type == "" {
		return nil, nil, errors.New("queue: job without type")
	}
	if j.MaxRetry <= 0 {
		j.MaxRetry = DefaultMaxRetry
	}
	j.Attempt = 0 // owned by the server
	raw, err := json.Marshal(j)
	if err != nil {
		return nil, nil, err
	}
	opts := []asynq.Option{asynq.Queue(queueName(j)), asynq.MaxRetry(j.MaxRetry)}
	if id := taskID(j); id != "" {
		opts = append(opts, asynq.TaskID(id))
	}
	if !j.ProcessAt.IsZero() {
		opts = append(opts, asynq.ProcessAt(j.ProcessAt))
	}
	if j.Timeout > 0 {
		opts = append(opts, asynq.Timeout(j.Timeout))
	}
	return asynq.NewTask(j.Type, raw), opts, nil
}

// fromTask rebuilds the job; retried is asynq's retry count (0 first time).
func fromTask(t *asynq.Task, retried int) (queue.Job, error) {
	var j queue.Job
	if err := json.Unmarshal(t.Payload(), &j); err != nil {
		return queue.Job{}, err
	}
	if j.Type == "" {
		j.Type = t.Type()
	}
	j.Attempt = retried
	return j, nil
}

// Enqueue is idempotent for jobs with a UniqueKey: a conflict with a pending,
// scheduled, retrying or running task means it is already queued.
//
// asynq keeps the TaskID of archived (failed) and retained (completed) tasks,
// which would block the key forever; queue.Job.UniqueKey only de-duplicates
// while a job is pending, so such leftovers are deleted and the enqueue retried.
func (p *Producer) Enqueue(ctx context.Context, j queue.Job) error {
	task, opts, err := toTask(j)
	if err != nil {
		return err
	}
	_, err = p.client.EnqueueContext(ctx, task, opts...)
	if err == nil || !errors.Is(err, asynq.ErrTaskIDConflict) {
		return err
	}
	id, qn := taskID(j), queueName(j)
	info, ierr := p.inspector.GetTaskInfo(qn, id)
	if ierr != nil {
		if errors.Is(ierr, asynq.ErrTaskNotFound) {
			// Finished between the two calls: try once more.
			_, err = p.client.EnqueueContext(ctx, task, opts...)
			if errors.Is(err, asynq.ErrTaskIDConflict) {
				return nil
			}
			return err
		}
		return nil // cannot tell; the conflict most likely means it is queued
	}
	if info.State != asynq.TaskStateArchived && info.State != asynq.TaskStateCompleted {
		return nil
	}
	if derr := p.inspector.DeleteTask(qn, id); derr != nil && !errors.Is(derr, asynq.ErrTaskNotFound) {
		return derr
	}
	_, err = p.client.EnqueueContext(ctx, task, opts...)
	if errors.Is(err, asynq.ErrTaskIDConflict) {
		return nil
	}
	return err
}

// ServerOptions configures a Server.
type ServerOptions struct {
	Concurrency int
	// Queues are served in addition to DefaultQueue (dedicated tenant queues).
	Queues []string
	// ShutdownTimeout bounds how long Stop waits for running jobs before
	// asynq pushes them back to Redis (default 10s).
	ShutdownTimeout time.Duration
	Log             *slog.Logger
}

// Server implements queue.Server.
type Server struct {
	log *slog.Logger
	srv *asynq.Server

	mu       sync.RWMutex
	handlers map[string]queue.Handler
	baseCtx  context.Context
	started  bool
	stopOnce sync.Once
}

// NewServer consumes from rdb. The caller owns (and closes) rdb.
func NewServer(rdb goredis.UniversalClient, o ServerOptions) *Server {
	if o.Concurrency <= 0 {
		o.Concurrency = 8
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.ShutdownTimeout <= 0 {
		o.ShutdownTimeout = 10 * time.Second
	}
	qs := map[string]int{DefaultQueue: 1}
	for _, q := range o.Queues {
		if q != "" {
			qs[q] = 1
		}
	}
	s := &Server{log: o.Log, handlers: map[string]queue.Handler{}, baseCtx: context.Background()}
	s.srv = asynq.NewServerFromRedisClient(rdb, asynq.Config{
		Concurrency:     o.Concurrency,
		Queues:          qs,
		BaseContext:     s.base,
		RetryDelayFunc:  retryDelay,
		ShutdownTimeout: o.ShutdownTimeout,
		Logger:          slogAdapter{o.Log},
		LogLevel:        asynq.WarnLevel,
		ErrorHandler: asynq.ErrorHandlerFunc(func(ctx context.Context, t *asynq.Task, err error) {
			n, _ := asynq.GetRetryCount(ctx)
			max, _ := asynq.GetMaxRetry(ctx)
			o.Log.Warn("job failed", "type", t.Type(), "attempt", n, "max_retry", max, "err", err)
		}),
	})
	return s
}

func (s *Server) base() context.Context {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.baseCtx
}

// retryDelay is exponential with jitter: 1s, 2s, 4s ... capped at 10 minutes.
func retryDelay(n int, _ error, _ *asynq.Task) time.Duration {
	if n > 10 {
		n = 10
	}
	d := time.Second << n
	if d > 10*time.Minute {
		d = 10 * time.Minute
	}
	return d + rand.N(d/4+1)
}

// Handle registers h for an exact job type. Dispatch is by exact type rather
// than asynq.ServeMux, whose patterns match by prefix.
func (s *Server) Handle(typ string, h queue.Handler) {
	s.mu.Lock()
	s.handlers[typ] = h
	s.mu.Unlock()
}

// Start begins consuming. Handlers inherit ctx's values but not its
// cancellation: shutdown goes through Stop so running jobs can finish.
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("queue: server already started")
	}
	s.started = true
	s.baseCtx = context.WithoutCancel(ctx)
	s.mu.Unlock()
	if err := s.srv.Start(asynq.HandlerFunc(s.process)); err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		s.Stop()
	}()
	return nil
}

// Stop stops fetching, waits up to ShutdownTimeout for running jobs and
// requeues the rest. Safe to call more than once.
func (s *Server) Stop() {
	s.stopOnce.Do(func() {
		s.srv.Stop()
		s.srv.Shutdown()
	})
}

func (s *Server) process(ctx context.Context, t *asynq.Task) (err error) {
	retried, _ := asynq.GetRetryCount(ctx)
	j, derr := fromTask(t, retried)
	if derr != nil {
		return fmt.Errorf("decode job %s: %v: %w", t.Type(), derr, asynq.SkipRetry)
	}
	s.mu.RLock()
	h := s.handlers[j.Type]
	s.mu.RUnlock()
	if h == nil {
		// Retry rather than drop: during a rolling deploy another worker may
		// know this type.
		return fmt.Errorf("no handler for job type %q", j.Type)
	}
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("job panic", "type", j.Type, "panic", r)
			err = fmt.Errorf("job panic: %v: %w", r, asynq.SkipRetry)
		}
	}()
	err = h(tenant.SystemContext(ctx, j.TenantID), j)
	if err != nil && queue.IsSkipRetry(err) {
		return fmt.Errorf("%w: %w", err, asynq.SkipRetry)
	}
	return err
}

// slogAdapter routes asynq's logs into slog.
type slogAdapter struct{ l *slog.Logger }

func (a slogAdapter) Debug(args ...interface{}) { a.l.Debug(fmt.Sprint(args...), "component", "asynq") }
func (a slogAdapter) Info(args ...interface{})  { a.l.Info(fmt.Sprint(args...), "component", "asynq") }
func (a slogAdapter) Warn(args ...interface{})  { a.l.Warn(fmt.Sprint(args...), "component", "asynq") }
func (a slogAdapter) Error(args ...interface{}) { a.l.Error(fmt.Sprint(args...), "component", "asynq") }
func (a slogAdapter) Fatal(args ...interface{}) {
	a.l.Error(fmt.Sprint(args...), "component", "asynq", "fatal", true)
}
