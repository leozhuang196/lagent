package app

import (
	"context"

	"github.com/eternityx/lagent/internal/infra/queue"
	"github.com/eternityx/lagent/internal/orch/scheduler"
	"github.com/eternityx/lagent/internal/orch/workflow"
	"github.com/eternityx/lagent/internal/transport/httpapi/workflowapi"
)

// The workflow extension wires runs, schedules and their HTTP routes (§9.14–9.17, §9.11).
func init() {
	RegisterExtension(Extension{Name: "workflow", Build: func(a *App) error {
		inf := a.Infra
		wf := (&workflow.Service{
			Store: inf.Store, Queue: inf.Queue, Bus: inf.Bus, Sessions: a.Sessions, Clock: a.Clock, Log: a.Log.With("module", "workflow"),
			Cfg: workflow.Config{
				StepTimeout: a.Cfg.Limits.Step, FlowTimeout: a.Cfg.Limits.Flow, MaxParallel: a.Cfg.Limits.FanoutParallel,
				DefaultRole: a.Cfg.DefaultRole,
			},
		}).Init()
		sch := (&scheduler.Service{
			Store: inf.Store, Sessions: a.Sessions, Workflows: wf, Queue: inf.Queue, Clock: a.Clock, Log: a.Log.With("module", "scheduler"),
		}).Init()

		// Step turns report their outcome through the turn hook.
		a.AddHook(wf)
		a.Handle(queue.TypeWorkflowAdvance, wf.HandleAdvance)
		a.Handle(queue.TypeScheduleFire, sch.HandleFire)

		// Sweeps are cross-tenant background loops; only worker processes run
		// them. Every replica may: claims are conditional updates.
		a.OnStart(func(ctx context.Context) error {
			if !a.Cfg.Has(RoleWorker) {
				return nil
			}
			return wf.RunSweeper(ctx)
		})
		a.OnStart(func(ctx context.Context) error {
			if !a.Cfg.Has(RoleWorker) {
				return nil
			}
			return sch.Run(ctx)
		})

		h := &workflowapi.Handler{Workflows: wf, Schedules: sch, Bus: inf.Bus, Clock: a.Clock}
		a.HTTP.Mount(h.Routes)
		return nil
	}})
}
