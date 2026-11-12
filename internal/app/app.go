package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/eternityx/lagent/internal/capability/builtin"
	"github.com/eternityx/lagent/internal/capability/tool"
	"github.com/eternityx/lagent/internal/core/clock"
	"github.com/eternityx/lagent/internal/infra/llm"
	"github.com/eternityx/lagent/internal/infra/queue"
	"github.com/eternityx/lagent/internal/infra/sandbox"
	"github.com/eternityx/lagent/internal/infra/secret"
	secretlocal "github.com/eternityx/lagent/internal/infra/secret/local"
	"github.com/eternityx/lagent/internal/orch/admin"
	"github.com/eternityx/lagent/internal/orch/integration"
	"github.com/eternityx/lagent/internal/orch/notify"
	"github.com/eternityx/lagent/internal/orch/session"
	"github.com/eternityx/lagent/internal/orch/subtask"
	"github.com/eternityx/lagent/internal/orch/turn"
	"github.com/eternityx/lagent/internal/transport/httpapi"
)

// App is a running process.
type App struct {
	Cfg          *Config
	Log          *slog.Logger
	Clock        clock.Clock
	Infra        *Infra
	Secrets      secret.Store
	LLM          llm.Gateway
	Registry     *tool.Registry
	Sandbox      *sandbox.Sandbox
	Engine       *turn.Engine
	Sessions     *session.Service
	Admin        *admin.Service
	Integrations *integration.Service
	Subtasks     *subtask.Dispatcher
	Webhook      *notify.Webhook
	HTTP         *httpapi.Server

	hooks    []turn.Hooks
	starters []func(ctx context.Context) error
}

// Extension wires optional modules (workflow, scheduler, MCP, Feishu, console)
// into the app. Each module's file registers one with RegisterExtension.
type Extension struct {
	Name string
	// Build runs after core services exist and before roles start.
	Build func(a *App) error
}

var extensions []Extension

// RegisterExtension adds a module; called from init() in this package.
func RegisterExtension(e Extension) { extensions = append(extensions, e) }

// AddHook adds a turn lifecycle hook (must be called during Build).
func (a *App) AddHook(h turn.Hooks) { a.hooks = append(a.hooks, h) }

// OnStart registers a background loop started with the process.
func (a *App) OnStart(fn func(ctx context.Context) error) { a.starters = append(a.starters, fn) }

// New builds every component for cfg.
func New(ctx context.Context, cfg *Config) (*App, error) {
	lvl := slog.LevelInfo
	_ = lvl.UnmarshalText([]byte(cfg.LogLevel))
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
	a := &App{Cfg: cfg, Log: log, Clock: clock.Real{}}
	inf, err := buildInfra(ctx, cfg, a.Clock, log)
	if err != nil {
		return nil, err
	}
	a.Infra = inf
	a.Secrets, err = secretlocal.New(cfg.Secret.Path, cfg.Secret.Key)
	if err != nil {
		return nil, err
	}
	provs, conc, err := buildProviders(cfg)
	if err != nil {
		return nil, err
	}
	a.LLM = llm.NewRouter(llm.RouterConfig{DefaultModel: cfg.LLM.Default, Concurrency: conc}, inf.Coord, log, provs...)
	a.Sandbox, err = sandbox.New(sandbox.Config{
		Root: cfg.Sandbox.Root, RunAsUID: cfg.Sandbox.RunAsUID, RunAsGID: cfg.Sandbox.RunAsGID,
		EgressProxy: cfg.Sandbox.EgressProxy, CPUSeconds: cfg.Sandbox.CPUSeconds, MemoryMB: cfg.Sandbox.MemoryMB,
	}, inf.Objects)
	if err != nil {
		return nil, err
	}
	a.Registry = tool.NewRegistry()
	a.Registry.Register(builtin.All(builtin.Deps{Sandbox: a.Sandbox, Store: inf.Store})...)

	a.Sessions = &session.Service{Store: inf.Store, Queue: inf.Queue, Bus: inf.Bus, Signal: inf.Coord, Clock: a.Clock, Log: log, DefaultRole: cfg.DefaultRole}
	a.Admin = &admin.Service{Store: inf.Store, Clock: a.Clock, Log: log}
	a.Subtasks = &subtask.Dispatcher{Sessions: a.Sessions, Store: inf.Store, Queue: inf.Queue, MaxParallel: cfg.Limits.SubtaskParallel}
	a.Registry.Register(a.Subtasks.Tool())
	a.Webhook = &notify.Webhook{Queue: inf.Queue, Secret: []byte(cfg.HTTP.CookieSecret), Log: log}
	a.AddHook(a.Subtasks)
	a.AddHook(a.Webhook)

	a.HTTP = httpapi.New(httpapi.Deps{
		Sessions: a.Sessions, Bus: inf.Bus, Objects: inf.Objects, Clock: a.Clock, Log: log, ObjectVerifier: inf.Verify,
		Auth: &httpapi.Auth{Keys: a.Admin, CookieSecret: []byte(cfg.HTTP.CookieSecret), BootstrapKey: cfg.Auth.BootstrapKey, BootstrapTenant: cfg.Auth.BootstrapTenant},
	})

	for _, ext := range extensions {
		if err := ext.Build(a); err != nil {
			return nil, fmt.Errorf("extension %s: %w", ext.Name, err)
		}
	}
	// The engine is built last so it sees every hook.
	a.Engine = turn.New(turn.Deps{
		Store: inf.Store, Bus: inf.Bus, Lease: inf.Coord, Signal: inf.Coord, Counter: inf.Coord, Semaphore: inf.Coord,
		Queue: inf.Queue, LLM: a.LLM, Registry: a.Registry, Sandbox: a.Sandbox, Objects: inf.Objects, Secrets: a.Secrets,
		Clock: a.Clock, Log: log, Hooks: a.hooks,
	}, turn.Config{DefaultModel: cfg.LLM.Default, LeaseTTL: cfg.Limits.LeaseTTL, ContextLimit: cfg.Limits.ContextLimit, RecordLLMCalls: cfg.RecordLLMCalls})

	if err := a.Admin.Seed(ctx, cfg.Auth.BootstrapTenant); err != nil {
		return nil, fmt.Errorf("seed: %w", err)
	}
	if err := a.bootstrap(ctx); err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}
	return a, nil
}

// Handle registers a queue handler (consumer roles only).
func (a *App) Handle(typ string, h queue.Handler) { a.Infra.Server.Handle(typ, h) }

// Run starts the configured roles and blocks until ctx ends.
func (a *App) Run(ctx context.Context) error {
	cfg := a.Cfg
	if cfg.Has(RoleEngine) {
		a.Handle(queue.TypeTurnRun, a.Engine.HandleJob)
		a.Handle(queue.TypeTurnResume, a.Engine.HandleJob)
	}
	if cfg.Has(RoleWorker) {
		a.Handle(queue.TypeWebhook, a.Webhook.Handle)
	}
	if cfg.Has(RoleEngine) || cfg.Has(RoleWorker) {
		if err := a.Infra.Server.Start(ctx); err != nil {
			return err
		}
	}
	for _, s := range a.starters {
		fn := s
		go func() {
			if err := fn(ctx); err != nil && ctx.Err() == nil {
				a.Log.Error("background loop stopped", "err", err)
			}
		}()
	}
	var srv *http.Server
	errc := make(chan error, 1)
	if cfg.Has(RoleAPI) {
		srv = &http.Server{Addr: cfg.HTTP.Addr, Handler: a.HTTP, ReadHeaderTimeout: 10 * time.Second}
		go func() {
			a.Log.Info("http listening", "addr", cfg.HTTP.Addr, "roles", cfg.Roles)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}()
	} else {
		a.Log.Info("started", "roles", cfg.Roles)
	}
	select {
	case <-ctx.Done():
	case err := <-errc:
		return err
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if srv != nil {
		_ = srv.Shutdown(shutdown)
	}
	a.Infra.Server.Stop()
	for _, c := range a.Infra.Closers {
		_ = c(shutdown)
	}
	return nil
}

// Describe summarises the configuration for logs.
func (a *App) Describe() string {
	var roles []string
	for _, r := range a.Cfg.Roles {
		roles = append(roles, string(r))
	}
	return fmt.Sprintf("roles=%s store=%s redis=%t object=%s llm=%s", strings.Join(roles, ","), a.Cfg.Store.Driver, a.Cfg.Redis.Addr != "", a.Cfg.Object.Driver, a.Cfg.LLM.Default)
}
