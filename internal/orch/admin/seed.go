package admin

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/infra/repo/scope"
)

// SeedFS holds built-in assets (roles, skills, workflow templates) released
// with the binary. They live in the _system tenant and are read-only.
//
//go:embed seeds
var SeedFS embed.FS

// Seed makes sure the bootstrap tenant exists and the built-in layer matches
// this binary: each seed file is published as a new version only when its
// content changed, so old versions stay resolvable for running sessions.
func (s *Service) Seed(ctx context.Context, bootstrapTenant string) error {
	sys := repo.SystemLayer(ctx)
	if bootstrapTenant != "" {
		if _, err := s.Store.Tenants.Get(tenant.SystemContext(ctx, bootstrapTenant), bootstrapTenant); errs.Is(err, errs.CodeNotFound) {
			t := &types.Tenant{Name: bootstrapTenant, Status: "active"}
			t.ID, t.TenantID = bootstrapTenant, bootstrapTenant
			if err := s.Store.Tenants.Insert(tenant.SystemContext(ctx, bootstrapTenant), t); err != nil && !errs.Is(err, errs.CodeConflict) {
				return err
			}
		}
	}
	return fs.WalkDir(SeedFS, "seeds", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".json") {
			return err
		}
		raw, err := SeedFS.ReadFile(p)
		if err != nil {
			return err
		}
		kind := path.Base(path.Dir(p))
		switch kind {
		case "roles":
			var v types.Role
			if err := json.Unmarshal(raw, &v); err != nil {
				return fmt.Errorf("%s: %w", p, err)
			}
			return seedVersioned(sys, s.Store.Roles, &v, func(a, b *types.Role) bool { return sameJSON(stripRole(a), stripRole(b)) })
		case "skills":
			var v types.Skill
			if err := json.Unmarshal(raw, &v); err != nil {
				return fmt.Errorf("%s: %w", p, err)
			}
			return seedVersioned(sys, s.Store.Skills, &v, func(a, b *types.Skill) bool {
				return a.Name == b.Name && a.Description == b.Description && a.Body == b.Body
			})
		case "workflows":
			var v types.WorkflowDef
			if err := json.Unmarshal(raw, &v); err != nil {
				return fmt.Errorf("%s: %w", p, err)
			}
			return seedVersioned(sys, s.Store.WorkflowDefs, &v, func(a, b *types.WorkflowDef) bool {
				return sameJSON(a.Steps, b.Steps) && sameJSON(a.Params, b.Params) && a.Role == b.Role
			})
		}
		return nil
	})
}

func stripRole(r *types.Role) any {
	c := *r
	c.Meta, c.Versioned = types.Meta{}, types.Versioned{}
	return c
}

func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func seedVersioned[E any](ctx context.Context, c repo.Collection[E], v *E, same func(a, b *E) bool) error {
	ve := any(v).(types.VersionedEntity)
	key := ve.V().Key
	cur, err := repo.LatestPublished(ctx, c, key)
	if err == nil && same(cur, v) {
		return nil
	}
	// The next number is above every tenant's override of this key too:
	// sessions pin (key, ver) and repo.Version looks in the tenant layer
	// first, so a built-in version must never reuse a tenant's number.
	ver := 1
	all, aerr := repo.All(scope.AllTenants(ctx), c, repo.Query{Filter: repo.Filter{"key": key}}, 0)
	if aerr != nil {
		return aerr
	}
	for _, it := range all {
		if v := any(it).(types.VersionedEntity).V().Ver; v >= ver {
			ver = v + 1
		}
	}
	ve.V().Ver, ve.V().Status = ver, types.AssetPublished
	ve.M().ID, ve.M().TenantID = "", tenant.System
	if err := c.Insert(ctx, v); err != nil && !errs.Is(err, errs.CodeConflict) {
		return err
	}
	return nil
}
