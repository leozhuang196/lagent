package repo

import (
	"context"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
)

// SystemLayer returns ctx switched to the built-in (read-only) tenant.
func SystemLayer(ctx context.Context) context.Context {
	return tenant.With(ctx, tenant.Principal{Kind: tenant.KindSystem, ID: "system", TenantID: tenant.System, Role: tenant.RoleTenantAdmin})
}

// LatestPublished finds the newest published version of a versioned asset:
// the tenant layer first, then the built-in layer.
func LatestPublished[E any](ctx context.Context, c Collection[E], key string) (*E, error) {
	f := Filter{"key": key, "status": string(types.AssetPublished)}
	for _, cx := range []context.Context{ctx, SystemLayer(ctx)} {
		items, err := All(cx, c, Query{Filter: f}, 0)
		if err != nil {
			return nil, err
		}
		if best := maxVer(items); best != nil {
			return best, nil
		}
	}
	return nil, errs.NotFound("asset", key)
}

// Version finds an exact version (0 = draft) in the tenant layer, then built-in.
func Version[E any](ctx context.Context, c Collection[E], key string, ver int) (*E, error) {
	f := Filter{"key": key, "ver": ver}
	for _, cx := range []context.Context{ctx, SystemLayer(ctx)} {
		v, err := c.FindOne(cx, f)
		if err == nil {
			return v, nil
		}
		if !errs.Is(err, errs.CodeNotFound) {
			return nil, err
		}
	}
	return nil, errs.NotFound("asset", key)
}

func maxVer[E any](items []*E) *E {
	var best *E
	bestVer := -1
	for _, it := range items {
		v := any(it).(types.VersionedEntity).V()
		if v.Ver > bestVer {
			best, bestVer = it, v.Ver
		}
	}
	return best
}
