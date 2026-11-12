package llm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/eternityx/lagent/internal/infra/coord"
)

// RouterConfig tunes the router.
type RouterConfig struct {
	DefaultModel string
	// Concurrency caps in-flight calls per provider across all instances.
	Concurrency map[string]int
	MaxRetries  int
}

// Router dispatches to providers with a global semaphore, retries with
// backoff and an optional fallback model.
type Router struct {
	cfg       RouterConfig
	providers map[string]Provider
	sem       coord.Semaphore
	log       *slog.Logger
	sleep     func(context.Context, time.Duration) error
}

func NewRouter(cfg RouterConfig, sem coord.Semaphore, log *slog.Logger, providers ...Provider) *Router {
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}
	if log == nil {
		log = slog.Default()
	}
	r := &Router{cfg: cfg, providers: map[string]Provider{}, sem: sem, log: log, sleep: sleepCtx}
	for _, p := range providers {
		r.providers[p.Name()] = p
	}
	return r
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// WithFallback returns a gateway that tries fallback once primary keeps failing.
type fallbackKey struct{}

// ContextWithFallback attaches a fallback model for calls made with ctx.
func ContextWithFallback(ctx context.Context, model string) context.Context {
	return context.WithValue(ctx, fallbackKey{}, model)
}

func (r *Router) Stream(ctx context.Context, req Request, onDelta func(Delta)) (*Response, error) {
	if req.Model == "" {
		req.Model = r.cfg.DefaultModel
	}
	resp, err := r.streamModel(ctx, req, onDelta)
	if err == nil || !Retryable(err) {
		return resp, err
	}
	fb, _ := ctx.Value(fallbackKey{}).(string)
	if fb == "" || fb == req.Model {
		return nil, err
	}
	r.log.Warn("model failed, falling back", "model", req.Model, "fallback", fb, "err", err)
	req.Model = fb
	return r.streamModel(ctx, req, onDelta)
}

func (r *Router) streamModel(ctx context.Context, req Request, onDelta func(Delta)) (*Response, error) {
	provName, model := SplitModel(req.Model)
	p, ok := r.providers[provName]
	if !ok {
		return nil, &Error{Kind: ErrInvalid, Err: fmt.Errorf("unknown provider %q in model %q", provName, req.Model)}
	}
	var lastErr error
	for attempt := 0; attempt <= r.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			wait := time.Duration(1<<uint(attempt-1)) * time.Second
			var e *Error
			if errors.As(lastErr, &e) && e.RetryAfter > 0 {
				wait = time.Duration(e.RetryAfter) * time.Second
			}
			if err := r.sleep(ctx, wait); err != nil {
				return nil, err
			}
		}
		release := func() {}
		if limit := r.cfg.Concurrency[provName]; limit > 0 && r.sem != nil {
			rel, err := coord.Acquire(ctx, r.sem, coord.ModelKey(provName), limit, 10*time.Minute)
			if err != nil {
				return nil, err
			}
			release = rel
		}
		streamed := false
		resp, err := p.Stream(ctx, model, req, func(d Delta) {
			streamed = true
			if onDelta != nil {
				onDelta(d)
			}
		})
		release()
		if err == nil {
			if resp.Model == "" {
				resp.Model = req.Model
			}
			return resp, nil
		}
		lastErr = err
		// Once output reached the client, a silent retry would duplicate it.
		if streamed || !Retryable(err) {
			return nil, err
		}
		r.log.Info("model call retry", "model", req.Model, "attempt", attempt+1, "err", err)
	}
	return nil, lastErr
}
