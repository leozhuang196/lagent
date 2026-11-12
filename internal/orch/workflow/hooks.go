package workflow

import (
	"context"
	"fmt"
	"time"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/orch/turn"
)

var _ turn.Hooks = (*Service)(nil)

// OnPending marks a step running as soon as its turn suspends on a person:
// the turn has certainly started even if no scheduling pass noticed yet.
func (s *Service) OnPending(ctx context.Context, sess *types.Session, t *types.Turn, _ *types.Pending) {
	if sess.StepRunID == "" {
		return
	}
	s.markRunning(ctx, sess.StepRunID, sess.ID, t.StartedAt)
}

// OnTurnEnd records the outcome of a step turn on its step run and wakes the
// scheduler. A completed turn's result is what submit_result recorded, or
// its final text (set by the engine).
func (s *Service) OnTurnEnd(ctx context.Context, sess *types.Session, t *types.Turn) {
	if sess.StepRunID == "" {
		return
	}
	switch t.Status {
	case types.TurnCompleted:
		res := t.Result
		if res == nil {
			res = &types.StepResult{Summary: t.FinalText}
		}
		if len(res.Data) > MaxResultData {
			s.finishAttempt(ctx, sess.StepRunID, sess.ID, nil, &types.TurnError{
				Code:    "result_too_large",
				Message: fmt.Sprintf("the step returned %d KiB of data (limit %d KiB); archive large output as an artifact and return its id", len(res.Data)>>10, MaxResultData>>10),
			}, false)
			return
		}
		s.finishAttempt(ctx, sess.StepRunID, sess.ID, res, nil, false)
	case types.TurnFailed:
		te := t.Error
		if te == nil {
			te = &types.TurnError{Code: "failed", Message: "the step turn failed"}
		}
		s.finishAttempt(ctx, sess.StepRunID, sess.ID, nil, te, true)
	case types.TurnCancelled:
		// A person (or the platform) stopped the step deliberately: retrying
		// would undo that decision.
		s.finishAttempt(ctx, sess.StepRunID, sess.ID, nil, &types.TurnError{Code: "cancelled", Message: "the step's session was cancelled"}, false)
	}
}

// finishAttempt ends one attempt of a step. sessionID guards against stale
// attempts (a timed-out session ending after its retry was dispatched).
// Failed attempts of non-side-effect steps with retries left go back to
// pending with an exponential NotBefore; the fingerprint and the step run
// stay the same (§9.16).
func (s *Service) finishAttempt(ctx context.Context, stepID, sessionID string, res *types.StepResult, te *types.TurnError, retryable bool) {
	now := s.Clock.Now()
	retrying := false
	st, err := repo.Update(ctx, s.Store.StepRuns, stepID, func(x *types.StepRun) error {
		if x.Status != types.StepDispatched && x.Status != types.StepRunning {
			return errs.New(errs.CodeAlreadyResolved, "step is %s", x.Status)
		}
		if x.SessionID != sessionID {
			return errs.New(errs.CodeAlreadyResolved, "stale attempt")
		}
		retrying = false
		if x.StartedAt == nil {
			x.StartedAt = x.DispatchedAt
		}
		switch {
		case te == nil:
			x.Status, x.Result, x.Error, x.EndedAt = types.StepSucceeded, res, nil, &now
		case retryable && !x.SideEffect && x.Attempts <= x.MaxRetries:
			retrying = true
			at := now.Add(s.backoff(x.Attempts))
			x.Status, x.NotBefore = types.StepPending, &at
			x.Error = &types.TurnError{Code: te.Code, Message: fmt.Sprintf("attempt %d of %d failed: %s; retrying", x.Attempts, x.MaxRetries+1, te.Message)}
		default:
			x.Status, x.Error, x.EndedAt = types.StepFailed, te, &now
			if x.Attempts > 1 {
				x.Error = &types.TurnError{Code: te.Code, Message: fmt.Sprintf("%s (after %d attempts)", te.Message, x.Attempts)}
			}
		}
		return nil
	})
	if err != nil {
		if !errs.Is(err, errs.CodeAlreadyResolved) {
			s.Log.Warn("record step outcome", "step", stepID, "err", err)
		}
		return
	}
	if run, err := s.Store.Runs.Get(ctx, st.RunID); err == nil {
		s.emitStep(ctx, run, st, types.EvStepCompleted)
	}
	if retrying {
		// A dedicated delayed job wakes the run when the backoff ends; the
		// sweeper is the fallback if it is lost.
		suffix := fmt.Sprintf(":retry:%s:%d", st.ID, st.Attempts)
		if err := s.enqueueAdvance(ctx, st.TenantID, st.RunID, suffix, *st.NotBefore); err != nil {
			s.Log.Warn("enqueue retry", "step", st.ID, "err", err)
		}
		return
	}
	if err := s.EnqueueAdvance(ctx, st.TenantID, st.RunID); err != nil {
		s.Log.Warn("enqueue advance", "run", st.RunID, "err", err)
	}
}

// backoff is RetryBase·2^(attempt-1), capped at RetryMax.
func (s *Service) backoff(attempt int) time.Duration {
	d := s.Cfg.RetryBase
	for i := 1; i < attempt && d < s.Cfg.RetryMax; i++ {
		d *= 2
	}
	if d > s.Cfg.RetryMax {
		d = s.Cfg.RetryMax
	}
	return d
}
