package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/eternityx/lagent/internal/core/clock"
	"github.com/eternityx/lagent/internal/infra/coord/redis"
	"github.com/eternityx/lagent/internal/infra/eventbus"
	busredis "github.com/eternityx/lagent/internal/infra/eventbus/redis"
	"github.com/eternityx/lagent/internal/infra/queue"
	queueasynq "github.com/eternityx/lagent/internal/infra/queue/asynq"
)

// Redis backs coordination, the event buffer and the job queue in
// multi-instance deployments. It holds no facts (P2).
func init() {
	redisDriver = func(ctx context.Context, cfg *Config, log *slog.Logger) (Coordination, eventbus.Bus, queue.Queue, queue.Server, func(context.Context) error, error) {
		rdb := goredis.NewClient(&goredis.Options{
			Addr:     cfg.Redis.Addr,
			Password: cfg.Redis.Password,
			DB:       cfg.Redis.DB,
			// Leave room for asynq workers (each blocks a connection while
			// fetching) next to API traffic.
			PoolSize: 2*cfg.Limits.Workers + 32,
		})
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := rdb.Ping(pctx).Err(); err != nil {
			_ = rdb.Close()
			return nil, nil, nil, nil, nil, fmt.Errorf("redis %s: %w", cfg.Redis.Addr, err)
		}
		bus := busredis.New(rdb, busredis.Options{TTL: cfg.Limits.EventTTL, Clock: clock.Real{}, Log: log})
		prod := queueasynq.NewProducer(rdb)
		srv := queueasynq.NewServer(rdb, queueasynq.ServerOptions{Concurrency: cfg.Limits.Workers, Log: log})
		closer := func(context.Context) error {
			// Server first: running jobs may still append events or enqueue.
			srv.Stop()
			return errors.Join(bus.Close(), prod.Close(), rdb.Close())
		}
		return redis.New(rdb), bus, prod, srv, closer, nil
	}
}
