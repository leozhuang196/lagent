// Package orchtest wires the orchestration layer on in-memory infrastructure
// and a scripted model, for component and end-to-end tests.
package orchtest

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/eternityx/lagent/internal/capability/builtin"
	"github.com/eternityx/lagent/internal/capability/tool"
	"github.com/eternityx/lagent/internal/core/clock"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	coordmem "github.com/eternityx/lagent/internal/infra/coord/memory"
	busmem "github.com/eternityx/lagent/internal/infra/eventbus/memory"
	"github.com/eternityx/lagent/internal/infra/llm"
	"github.com/eternityx/lagent/internal/infra/llm/fake"
	"github.com/eternityx/lagent/internal/infra/objectstore/local"
	"github.com/eternityx/lagent/internal/infra/queue"
	queuemem "github.com/eternityx/lagent/internal/infra/queue/memory"
	"github.com/eternityx/lagent/internal/infra/repo"
	repomem "github.com/eternityx/lagent/internal/infra/repo/memory"
	"github.com/eternityx/lagent/internal/infra/sandbox"
	secretlocal "github.com/eternityx/lagent/internal/infra/secret/local"
	"github.com/eternityx/lagent/internal/orch/session"
	"github.com/eternityx/lagent/internal/orch/turn"
)

// Env is a fully wired in-memory platform.
type Env struct {
	T        *testing.T
	Store    *repo.Store
	Bus      *busmem.Bus
	Coord    *coordmem.Coord
	Queue    *queuemem.Queue
	Fake     *fake.Provider
	Registry *tool.Registry
	Sandbox  *sandbox.Sandbox
	Objects  *local.Store
	Engine   *turn.Engine
	Sessions *session.Service
	Ctx      context.Context // tenant t1, member u1
	Admin    context.Context // tenant t1, tenant admin
	cancel   context.CancelFunc
}

// Option customises the environment before start.
type Option func(*Env)

// New builds and starts an environment.
func New(t *testing.T, opts ...Option) *Env {
	t.Helper()
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	e := &Env{T: t, Store: repomem.NewStore(nil), Bus: busmem.New(0, nil), Coord: coordmem.New(nil), Queue: queuemem.New(4, log), Fake: fake.New(nil), Registry: tool.NewRegistry()}
	var err error
	e.Objects, err = local.New(dir+"/objects", "http://test", []byte("k"))
	if err != nil {
		t.Fatal(err)
	}
	e.Sandbox, err = sandbox.New(sandbox.Config{Root: dir + "/ws"}, e.Objects)
	if err != nil {
		t.Fatal(err)
	}
	secrets, _ := secretlocal.New("", "k")
	e.Registry.Register(builtin.All(builtin.Deps{Sandbox: e.Sandbox, Store: e.Store})...)
	router := llm.NewRouter(llm.RouterConfig{DefaultModel: "fake/test", MaxRetries: 1}, e.Coord, log, e.Fake)
	e.Engine = turn.New(turn.Deps{
		Store: e.Store, Bus: e.Bus, Lease: e.Coord, Signal: e.Coord, Counter: e.Coord, Semaphore: e.Coord,
		Queue: e.Queue, LLM: router, Registry: e.Registry, Sandbox: e.Sandbox, Objects: e.Objects, Secrets: secrets,
		Clock: clock.Real{}, Log: log,
	}, turn.Config{DefaultModel: "fake/test", RecordLLMCalls: true})
	e.Sessions = &session.Service{Store: e.Store, Queue: e.Queue, Bus: e.Bus, Signal: e.Coord, Clock: clock.Real{}, Log: log, DefaultRole: "general"}
	e.Ctx = tenant.With(context.Background(), tenant.Principal{Kind: tenant.KindAPIKey, ID: "u1", TenantID: "t1", Role: tenant.RoleMember})
	e.Admin = tenant.With(context.Background(), tenant.Principal{Kind: tenant.KindAPIKey, ID: "admin", TenantID: "t1", Role: tenant.RoleTenantAdmin})
	tn := &types.Tenant{Name: "Test", Status: "active"}
	tn.ID = "t1"
	if err := e.Store.Tenants.Insert(e.Ctx, tn); err != nil {
		t.Fatal(err)
	}
	e.SeedRole(types.Role{Name: "General", Prompt: "You help.", Tools: []string{"*"}, DefaultPermissionMode: types.ModeAskAll}, "general")
	for _, o := range opts {
		o(e)
	}
	e.Queue.Handle(queue.TypeTurnRun, e.Engine.HandleJob)
	e.Queue.Handle(queue.TypeTurnResume, e.Engine.HandleJob)
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	if err := e.Queue.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		e.Queue.Wait()
	})
	return e
}

// SeedRole publishes a role (version 1) in the built-in layer.
func (e *Env) SeedRole(r types.Role, key string) *types.Role {
	r.Key, r.Ver, r.Status = key, 1, types.AssetPublished
	r.TenantID = tenant.System
	if err := e.Store.Roles.Insert(repo.SystemLayer(context.Background()), &r); err != nil {
		e.T.Fatal(err)
	}
	return &r
}

// Wait blocks until the queue is idle.
func (e *Env) Wait() {
	e.T.Helper()
	if !e.Queue.Idle(10 * time.Second) {
		e.T.Fatal("queue did not become idle")
	}
}

// Turn reloads a turn.
func (e *Env) Turn(id string) *types.Turn {
	e.T.Helper()
	t, err := e.Store.Turns.Get(e.Ctx, id)
	if err != nil {
		e.T.Fatal(err)
	}
	return t
}

// Messages returns a session's messages in order.
func (e *Env) Messages(sessionID string) []*types.Message {
	e.T.Helper()
	snap, err := e.Sessions.Snapshot(e.Admin, sessionID, 500)
	if err != nil {
		e.T.Fatal(err)
	}
	return snap.Messages
}

// OpenPendings lists open items of a turn.
func (e *Env) OpenPendings(turnID string) []*types.Pending {
	e.T.Helper()
	ps, err := repo.All(e.Ctx, e.Store.Pendings, repo.Query{Filter: repo.Filter{"turn_id": turnID, "status": "open"}}, 0)
	if err != nil {
		e.T.Fatal(err)
	}
	return ps
}

// Events returns all events of a stream.
func (e *Env) Events(stream string) []types.Event {
	evs, _, _ := e.Bus.Read(e.Ctx, stream, 0, 10000)
	return evs
}
