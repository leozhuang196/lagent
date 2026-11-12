package workflow

import (
	"context"
	"fmt"
	"time"

	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/infra/repo/scope"
)

// RunSweeper calls Sweep every Cfg.SweepEvery until ctx ends (App.OnStart,
// worker role). Every replica may run it: all its writes are conditional.
func (s *Service) RunSweeper(ctx context.Context) error {
	tk := time.NewTicker(s.Cfg.SweepEvery)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tk.C:
			if err := s.Sweep(ctx); err != nil && ctx.Err() == nil {
				s.Log.Warn("workflow sweep", "err", err)
			}
		}
	}
}

// Sweep enforces timeouts across tenants (§7.4: model call < step < flow):
// runs past their deadline are cancelled, steps past theirs fail (and their
// sessions are cancelled). It also wakes runs whose retry backoff has ended
// and nudges runs whose advance job may have been lost.
func (s *Service) Sweep(ctx context.Context) error {
	all := scope.AllTenants(tenant.SystemContext(ctx, tenant.System))
	runs, err := repo.All(all, s.Store.Runs, repo.Query{Filter: repo.Filter{"status": repo.In(string(types.RunPending), string(types.RunRunning))}}, 5000)
	if err != nil {
		return err
	}
	now := s.Clock.Now()
	for _, run := range runs {
		tctx := tenant.SystemContext(ctx, run.TenantID)
		if run.Deadline != nil && now.After(*run.Deadline) {
			limit := run.Deadline.Sub(run.CreatedAt).Round(time.Second)
			if _, err := s.cancelRun(tctx, run.ID, &types.TurnError{Code: "timeout", Message: fmt.Sprintf("the run exceeded its time limit of %s and was stopped", limit)}); err != nil {
				s.Log.Warn("cancel overdue run", "run", run.ID, "err", err)
			}
			continue
		}
		steps, err := s.steps(tctx, run.ID)
		if err != nil {
			continue
		}
		s.reconcileStarted(tctx, steps)
		nudge := run.Status == types.RunPending
		active := false
		for _, st := range steps {
			switch st.Status {
			case types.StepDispatched, types.StepRunning:
				active = true
				timeout := s.Cfg.StepTimeout
				if st.TimeoutSec > 0 {
					timeout = time.Duration(st.TimeoutSec) * time.Second
				}
				if st.DispatchedAt != nil && now.Sub(*st.DispatchedAt) > timeout {
					s.finishAttempt(tctx, st.ID, st.SessionID, nil, &types.TurnError{Code: "timeout", Message: fmt.Sprintf("the step did not finish within %s", timeout)}, true)
					s.cancelSession(tctx, run.TenantID, st.SessionID)
				}
			case types.StepPending:
				if st.NotBefore != nil && !now.Before(*st.NotBefore) {
					nudge = true
				}
			}
		}
		if !active {
			nudge = true // nothing in flight yet not terminal: make sure a pass happens
		}
		if nudge {
			_ = s.EnqueueAdvance(tctx, run.TenantID, run.ID)
		}
	}
	return nil
}
