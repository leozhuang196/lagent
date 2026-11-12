package app

import (
	"context"

	"github.com/eternityx/lagent/internal/capability/mcp"
	"github.com/eternityx/lagent/internal/capability/retrieval"
	"github.com/eternityx/lagent/internal/infra/queue"
	"github.com/eternityx/lagent/internal/orch/integration"
	httpintegration "github.com/eternityx/lagent/internal/transport/httpapi/integration"
)

// The MCP extension wires the third tool source (§6.1–6.2): tenant MCP
// servers appear in the registry through a provider, retrieval sources
// through the search/list_sources tools, and the console manages both
// through the integration routes (§9.9).
func init() {
	RegisterExtension(Extension{Name: "mcp", Build: func(a *App) error {
		m := mcp.New(a.Infra.Store, a.Secrets, mcp.Options{Clock: a.Clock, Log: a.Log})
		a.Registry.AddProvider(m.Provider())
		a.Registry.Register(retrieval.Tools(a.Infra.Store, m)...)

		svc := &integration.Service{
			Store: a.Infra.Store, Secrets: a.Secrets, MCP: m, Registry: a.Registry, Sandbox: a.Sandbox,
			Queue: a.Infra.Queue, Clock: a.Clock, Log: a.Log,
		}
		a.Integrations = svc
		a.HTTP.Mount((&httpintegration.Handler{Svc: svc}).Mount)
		// Background sync (enqueued after configuration changes) runs on workers.
		if a.Cfg.Has(RoleWorker) {
			a.Handle(queue.TypeMCPSync, svc.HandleSyncJob)
		}
		a.Infra.Closers = append(a.Infra.Closers, func(context.Context) error { m.Close(); return nil })
		return nil
	}})
}
