package postgres_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/eternityx/lagent/internal/core/id"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/infra/repo/postgres"
	"github.com/eternityx/lagent/internal/infra/repo/repotest"
)

// TestContract runs the repository contract against a real server. Set
// LAGENT_TEST_PG_URI (e.g. postgres://postgres:postgres@localhost:5432/postgres)
// to enable it; every sub-test gets its own throw-away schema.
func TestContract(t *testing.T) {
	uri := os.Getenv("LAGENT_TEST_PG_URI")
	if uri == "" {
		t.Skip("LAGENT_TEST_PG_URI not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	admin, err := pgxpool.New(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)

	repotest.Run(t, func(t *testing.T) *repo.Store {
		schema := "lagent_test_" + strings.ToLower(id.New())
		if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		})
		cfg, err := pgxpool.ParseConfig(uri)
		if err != nil {
			t.Fatal(err)
		}
		cfg.ConnConfig.RuntimeParams["search_path"] = schema
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		s, err := postgres.NewStore(ctx, pool, nil)
		if err != nil {
			t.Fatal(err)
		}
		// NewStore is idempotent: a second call over the existing schema succeeds.
		if _, err := postgres.NewStore(ctx, pool, nil); err != nil {
			t.Fatalf("second NewStore: %v", err)
		}
		return s
	})
}
