// Package turn runs execution turns, the only execution unit of the system:
// rebuild context from storage → loop model ⇄ tools → finish or suspend.
//
// A turn never waits in memory. Approval, questions, subtasks, external jobs
// and budget prompts all suspend it: the pending item and the tool-call ledger
// are written, the lease is released, and the job ends. When the item is
// resolved a turn.resume job continues on any instance.
package turn

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/eternityx/lagent/internal/capability/tool"
	"github.com/eternityx/lagent/internal/core/clock"
	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/coord"
	"github.com/eternityx/lagent/internal/infra/eventbus"
	"github.com/eternityx/lagent/internal/infra/llm"
	"github.com/eternityx/lagent/internal/infra/objectstore"
	"github.com/eternityx/lagent/internal/infra/queue"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/infra/sandbox"
	"github.com/eternityx/lagent/internal/infra/secret"
	"github.com/eternityx/lagent/internal/rules/sessionfsm"
)

// Config tunes the engine.
type Config struct {
	DefaultModel   string
	LeaseTTL       time.Duration
	RenewEvery     time.Duration
	ContextLimit   int // estimated tokens sent to the model before compaction
	ContextReserve int
	MaxTokens      int
	RecordLLMCalls bool
	DeltaFlush     time.Duration
}

func (c *Config) defaults() {
	if c.LeaseTTL == 0 {
		c.LeaseTTL = 30 * time.Second
	}
	if c.RenewEvery == 0 {
		c.RenewEvery = c.LeaseTTL / 3
	}
	if c.ContextLimit == 0 {
		c.ContextLimit = 150_000
	}
	if c.ContextReserve == 0 {
		c.ContextReserve = 20_000
	}
	if c.MaxTokens == 0 {
		c.MaxTokens = 16_000
	}
	if c.DeltaFlush == 0 {
		c.DeltaFlush = 60 * time.Millisecond
	}
}

// Hooks observe turn lifecycle to mirror it into channels (Feishu, webhooks,
// parent turns, workflow steps). They must be fast and must not fail the turn.
type Hooks interface {
	OnPending(ctx context.Context, s *types.Session, t *types.Turn, p *types.Pending)
	OnTurnEnd(ctx context.Context, s *types.Session, t *types.Turn)
}

// Deps are the engine's collaborators.
type Deps struct {
	Store     *repo.Store
	Bus       eventbus.Bus
	Lease     coord.Lease
	Signal    coord.Signal
	Counter   coord.Counter
	Semaphore coord.Semaphore
	Queue     queue.Queue
	LLM       llm.Gateway
	Registry  *tool.Registry
	Sandbox   *sandbox.Sandbox
	Objects   objectstore.Store
	Secrets   secret.Store
	Clock     clock.Clock
	Log       *slog.Logger
	Hooks     []Hooks
}

// Engine executes turns.
type Engine struct {
	Deps
	cfg Config
}

func New(d Deps, cfg Config) *Engine {
	cfg.defaults()
	if d.Clock == nil {
		d.Clock = clock.Real{}
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Engine{Deps: d, cfg: cfg}
}

// JobPayload is the payload of turn.run / turn.resume.
type JobPayload struct {
	TurnID string `json:"turn_id"`
}

// HandleJob is the queue handler for both run and resume.
func (e *Engine) HandleJob(ctx context.Context, j queue.Job) error {
	var p JobPayload
	if err := j.Decode(&p); err != nil {
		return queue.SkipRetry(err)
	}
	return e.Execute(ctx, p.TurnID)
}

var (
	errCancelled = errors.New("turn cancelled")
	errLeaseLost = errors.New("lease lost")
)

// Execute runs (or resumes) a turn until it ends or suspends. It is safe to
// call repeatedly: terminal turns and turns still waiting are no-ops.
func (e *Engine) Execute(ctx context.Context, turnID string) error {
	t, err := e.Store.Turns.Get(ctx, turnID)
	if err != nil {
		if errs.Is(err, errs.CodeNotFound) {
			return queue.SkipRetry(err)
		}
		return err
	}
	if t.Status.Terminal() {
		return nil
	}
	if t.Status == types.TurnSuspended {
		open, err := e.openPendings(ctx, t)
		if err != nil {
			return err
		}
		if len(open) > 0 {
			return nil // still waiting; a duplicate resume
		}
	}
	sess, err := e.Store.Sessions.Get(ctx, t.SessionID)
	if err != nil {
		return err
	}
	// Tenant-level concurrency cap.
	tn, _ := e.Store.Tenants.Get(ctx, t.TenantID)
	if tn != nil && tn.MaxConcurrentTurns > 0 && e.Semaphore != nil {
		rel, ok, err := e.Semaphore.TryAcquire(ctx, coord.TenantTurnsKey(t.TenantID), tn.MaxConcurrentTurns, time.Hour)
		if err != nil {
			return err
		}
		if !ok {
			return errs.New(errs.CodeUnavailable, "tenant concurrency limit reached")
		}
		defer rel()
	}
	leaseKey := coord.SessionLeaseKey(t.TenantID, t.SessionID)
	epoch, ok, err := e.Lease.Acquire(ctx, leaseKey, e.cfg.LeaseTTL)
	if err != nil {
		return err
	}
	if !ok {
		return errs.New(errs.CodeUnavailable, "session %s is busy on another instance", t.SessionID)
	}
	defer func() { _ = e.Lease.Release(context.WithoutCancel(ctx), leaseKey, epoch) }()

	now := e.Clock.Now()
	t, err = repo.Update(ctx, e.Store.Turns, turnID, func(x *types.Turn) error { return sessionfsm.StartTurn(x, epoch, now) })
	if err != nil {
		if errs.Is(err, errs.CodeConflict) {
			return nil // ended meanwhile
		}
		return err
	}
	sess, err = repo.Update(ctx, e.Store.Sessions, sess.ID, func(s *types.Session) error { return sessionfsm.Start(s, turnID) })
	if err != nil {
		return err
	}

	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	go e.renewLoop(runCtx, cancel, leaseKey, epoch)
	if e.Signal != nil {
		if ch, stop, err := e.Signal.Subscribe(ctx, coord.SessionControlChannel(t.TenantID, t.SessionID)); err == nil {
			defer stop()
			go func() {
				select {
				case msg := <-ch:
					if string(msg) == "cancel" {
						cancel(errCancelled)
					}
				case <-runCtx.Done():
				}
			}()
		}
	}

	r := &run{e: e, sess: sess, turn: t, epoch: epoch, log: e.Log.With("turn", t.ID, "session", sess.ID, "tenant", t.TenantID)}
	runErr := r.loop(runCtx)
	cause := context.Cause(runCtx)
	fin := context.WithoutCancel(ctx)
	switch {
	case errors.Is(cause, errLeaseLost) || errs.Is(runErr, errs.CodeLeaseLost):
		// Another instance may own the session now: stop writing, let the queue retry.
		r.log.Warn("lease lost; abandoning turn")
		return errs.New(errs.CodeUnavailable, "lease lost")
	case errors.Is(cause, errCancelled) || errors.Is(runErr, errCancelled):
		return r.finish(fin, types.TurnCancelled, nil)
	case runErr != nil && ctx.Err() != nil:
		return ctx.Err() // shutting down: the queue redelivers
	case runErr != nil:
		var te *types.TurnError
		var ee *errs.Error
		if errors.As(runErr, &ee) {
			// Keep the cause chain: the outer message alone ("model call
			// failed") does not tell an operator what went wrong.
			te = &types.TurnError{Code: string(ee.Code), Message: errs.Display(runErr)}
		} else {
			te = &types.TurnError{Code: string(errs.CodeInternal), Message: runErr.Error()}
		}
		r.log.Error("turn failed", "err", runErr)
		return r.finish(fin, types.TurnFailed, te)
	}
	return nil
}

func (e *Engine) renewLoop(ctx context.Context, cancel context.CancelCauseFunc, key string, epoch int64) {
	tk := time.NewTicker(e.cfg.RenewEvery)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			ok, err := e.Lease.Renew(ctx, key, epoch, e.cfg.LeaseTTL)
			if err != nil || !ok {
				// A failed renewal means the lease may already belong to someone
				// else: give up the turn rather than keep writing (§7.1).
				cancel(errLeaseLost)
				return
			}
		}
	}
}

func (e *Engine) openPendings(ctx context.Context, t *types.Turn) ([]*types.Pending, error) {
	var open []*types.Pending
	for _, pid := range t.WaitingOn {
		p, err := e.Store.Pendings.Get(ctx, pid)
		if err != nil {
			return nil, err
		}
		if p.Status == types.PendingOpen {
			open = append(open, p)
		}
	}
	return open, nil
}

// Enqueue schedules a turn job.
func Enqueue(ctx context.Context, q queue.Queue, tenantID, turnID string, resume bool) error {
	typ := queue.TypeTurnRun
	if resume {
		typ = queue.TypeTurnResume
	}
	j, err := queue.NewJob(typ, tenantID, JobPayload{TurnID: turnID})
	if err != nil {
		return err
	}
	j.UniqueKey = typ + ":" + turnID
	j.Timeout = 30 * time.Minute
	j.MaxRetry = 5
	return q.Enqueue(ctx, j)
}

func systemCtx(ctx context.Context, tenantID string) context.Context {
	if _, ok := tenant.From(ctx); ok {
		return ctx
	}
	return tenant.SystemContext(ctx, tenantID)
}

var _ = fmt.Sprintf
