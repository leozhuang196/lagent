package app

import (
	"context"

	"github.com/eternityx/lagent/internal/infra/objectstore"
	"github.com/eternityx/lagent/internal/infra/objectstore/s3"
)

func init() {
	objectDrivers["s3"] = func(ctx context.Context, cfg *Config) (objectstore.Store, error) {
		return s3.New(ctx, s3.Config{
			Bucket:   cfg.Object.Bucket,
			Region:   cfg.Object.Region,
			Endpoint: cfg.Object.Endpoint,
			// A custom endpoint means MinIO or another S3-compatible store,
			// which generally needs path-style addressing; AWS itself uses
			// virtual-hosted style. Credentials come from the default AWS chain.
			UsePathStyle: cfg.Object.Endpoint != "",
		})
	}
}
