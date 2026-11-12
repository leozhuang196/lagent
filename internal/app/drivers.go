package app

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/eternityx/lagent/internal/core/clock"
	"github.com/eternityx/lagent/internal/infra/coord"
	coordmem "github.com/eternityx/lagent/internal/infra/coord/memory"
	"github.com/eternityx/lagent/internal/infra/eventbus"
	busmem "github.com/eternityx/lagent/internal/infra/eventbus/memory"
	"github.com/eternityx/lagent/internal/infra/llm"
	"github.com/eternityx/lagent/internal/infra/llm/fake"
	"github.com/eternityx/lagent/internal/infra/objectstore"
	"github.com/eternityx/lagent/internal/infra/objectstore/local"
	"github.com/eternityx/lagent/internal/infra/queue"
	queuemem "github.com/eternityx/lagent/internal/infra/queue/memory"
	"github.com/eternityx/lagent/internal/infra/repo"
	repomem "github.com/eternityx/lagent/internal/infra/repo/memory"
)

// Coordination bundles every coord primitive; one backend provides all.
type Coordination interface {
	coord.Lease
	coord.Signal
	coord.Counter
	coord.Semaphore
}

// Infra is what a backend driver builds.
type Infra struct {
	Store   *repo.Store
	Coord   Coordination
	Bus     eventbus.Bus
	Queue   queue.Queue
	Server  queue.Server
	Objects objectstore.Store
	// Verify validates local signed URLs (nil for stores with native signing).
	Verify  func(key, exp, sig string) bool
	Closers []func(context.Context) error
}

// Driver factories. Implementation packages register themselves from files in
// this package (driver_*.go) so that only app/ imports them.
var (
	storeDrivers  = map[string]func(ctx context.Context, cfg *Config, clk clock.Clock) (*repo.Store, func(context.Context) error, error){}
	redisDriver   func(ctx context.Context, cfg *Config, log *slog.Logger) (Coordination, eventbus.Bus, queue.Queue, queue.Server, func(context.Context) error, error)
	objectDrivers = map[string]func(ctx context.Context, cfg *Config) (objectstore.Store, error){}
	llmDrivers    = map[string]func(name string, p LLMProvider) (llm.Provider, error){}
)

func buildInfra(ctx context.Context, cfg *Config, clk clock.Clock, log *slog.Logger) (*Infra, error) {
	inf := &Infra{}
	switch cfg.Store.Driver {
	case "memory":
		inf.Store = repomem.NewStore(clk)
	default:
		f, ok := storeDrivers[cfg.Store.Driver]
		if !ok {
			return nil, fmt.Errorf("store driver %q not available (have: memory%s)", cfg.Store.Driver, keys(storeDrivers))
		}
		s, closer, err := f(ctx, cfg, clk)
		if err != nil {
			return nil, err
		}
		inf.Store = s
		inf.Closers = append(inf.Closers, closer)
	}
	if cfg.Redis.Addr == "" {
		c := coordmem.New(clk)
		q := queuemem.New(cfg.Limits.Workers, log)
		inf.Coord, inf.Bus, inf.Queue, inf.Server = c, busmem.New(20000, clk), q, q
	} else {
		if redisDriver == nil {
			return nil, fmt.Errorf("redis driver not available in this build")
		}
		c, b, q, srv, closer, err := redisDriver(ctx, cfg, log)
		if err != nil {
			return nil, err
		}
		inf.Coord, inf.Bus, inf.Queue, inf.Server = c, b, q, srv
		inf.Closers = append(inf.Closers, closer)
	}
	switch cfg.Object.Driver {
	case "local":
		ls, err := local.New(cfg.Object.Root, cfg.HTTP.BaseURL, []byte(cfg.Object.Secret))
		if err != nil {
			return nil, err
		}
		inf.Objects = ls
		inf.Verify = func(key, exp, sig string) bool { return ls.Verify(key, exp, sig, clk.Now()) }
	default:
		f, ok := objectDrivers[cfg.Object.Driver]
		if !ok {
			return nil, fmt.Errorf("object driver %q not available", cfg.Object.Driver)
		}
		o, err := f(ctx, cfg)
		if err != nil {
			return nil, err
		}
		inf.Objects = o
	}
	return inf, nil
}

func buildProviders(cfg *Config) ([]llm.Provider, map[string]int, error) {
	var out []llm.Provider
	conc := map[string]int{}
	out = append(out, fake.New(nil)) // "fake/<anything>" echoes; handy for local runs
	for name, p := range cfg.LLM.Providers {
		if name == "fake" {
			continue
		}
		f, ok := llmDriverFor(name, p)
		if !ok {
			return nil, nil, fmt.Errorf("llm provider %q not available (have: fake%s)", name, keys(llmDrivers))
		}
		prov, err := f(name, p)
		if err != nil {
			return nil, nil, fmt.Errorf("llm provider %s: %w", name, err)
		}
		out = append(out, prov)
		conc[name] = p.Concurrency
	}
	return out, conc, nil
}

func keys[V any](m map[string]V) string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	s := ""
	for _, k := range ks {
		s += ", " + k
	}
	return s
}
