package app

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	mongodrv "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	"github.com/eternityx/lagent/internal/core/clock"
	"github.com/eternityx/lagent/internal/infra/repo"
	repomongo "github.com/eternityx/lagent/internal/infra/repo/mongo"
	repopg "github.com/eternityx/lagent/internal/infra/repo/postgres"
)

// Store drivers for the shared backends (store.driver: mongo | postgres).
// Both connect eagerly and create their collections/tables and indexes, so a
// misconfigured store fails at startup rather than on the first request.
func init() {
	storeDrivers["mongo"] = func(ctx context.Context, cfg *Config, clk clock.Clock) (*repo.Store, func(context.Context) error, error) {
		if cfg.Store.URI == "" {
			return nil, nil, fmt.Errorf("store: mongo needs store.uri")
		}
		client, err := mongodrv.Connect(options.Client().ApplyURI(cfg.Store.URI))
		if err != nil {
			return nil, nil, fmt.Errorf("store: mongo connect: %w", err)
		}
		closer := func(ctx context.Context) error { return client.Disconnect(ctx) }
		if err := client.Ping(ctx, readpref.Primary()); err != nil {
			_ = closer(ctx)
			return nil, nil, fmt.Errorf("store: mongo ping: %w", err)
		}
		s, err := repomongo.NewStore(ctx, client, cfg.Store.Database, clk)
		if err != nil {
			_ = closer(ctx)
			return nil, nil, err
		}
		return s, closer, nil
	}

	// store.database is not used: the database is part of the postgres URI.
	storeDrivers["postgres"] = func(ctx context.Context, cfg *Config, clk clock.Clock) (*repo.Store, func(context.Context) error, error) {
		if cfg.Store.URI == "" {
			return nil, nil, fmt.Errorf("store: postgres needs store.uri")
		}
		pool, err := pgxpool.New(ctx, cfg.Store.URI)
		if err != nil {
			return nil, nil, fmt.Errorf("store: postgres connect: %w", err)
		}
		closer := func(context.Context) error { pool.Close(); return nil }
		if err := pool.Ping(ctx); err != nil {
			pool.Close()
			return nil, nil, fmt.Errorf("store: postgres ping: %w", err)
		}
		s, err := repopg.NewStore(ctx, pool, clk)
		if err != nil {
			pool.Close()
			return nil, nil, err
		}
		return s, closer, nil
	}
}
