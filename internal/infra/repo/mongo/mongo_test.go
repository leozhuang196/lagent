package mongo_test

import (
	"context"
	"os"
	"testing"
	"time"

	mongodrv "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/eternityx/lagent/internal/core/id"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/infra/repo/mongo"
	"github.com/eternityx/lagent/internal/infra/repo/repotest"
)

// TestContract runs the repository contract against a real server. Set
// LAGENT_TEST_MONGO_URI (e.g. mongodb://localhost:27017) to enable it; every
// sub-test gets its own throw-away database.
func TestContract(t *testing.T) {
	uri := os.Getenv("LAGENT_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("LAGENT_TEST_MONGO_URI not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client, err := mongodrv.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })
	if err := client.Ping(ctx, nil); err != nil {
		t.Fatal(err)
	}
	repotest.Run(t, func(t *testing.T) *repo.Store {
		db := "lagent_test_" + id.New()
		t.Cleanup(func() { _ = client.Database(db).Drop(context.Background()) })
		s, err := mongo.NewStore(ctx, client, db, nil)
		if err != nil {
			t.Fatal(err)
		}
		// NewStore is idempotent: a second call over existing indexes succeeds.
		if _, err := mongo.NewStore(ctx, client, db, nil); err != nil {
			t.Fatalf("second NewStore: %v", err)
		}
		return s
	})
}
