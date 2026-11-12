package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/orch/integration"
)

// bootstrapConfig is a local, opt-in seed for the bootstrap tenant. It uses
// the same inputs and validation as the administration API. Existing records
// (including disabled roles and drafts) are never overwritten on restart.
// MCP creation queues the normal tool sync; boot does not depend on the network.
type bootstrapConfig struct {
	Servers []integration.ServerInput `json:"mcp_servers"`
	Sources []integration.SourceInput `json:"retrieval_sources"`
	Roles   []types.Role              `json:"roles"`
}

func (a *App) bootstrap(ctx context.Context) error {
	if a.Cfg.BootstrapFile == "" {
		return nil
	}
	f, err := os.Open(a.Cfg.BootstrapFile)
	if err != nil {
		return err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var cfg *bootstrapConfig
	if err := dec.Decode(&cfg); err != nil {
		return fmt.Errorf("decode file: %w", err)
	}
	if cfg == nil {
		return fmt.Errorf("file must contain a JSON object, not null")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("file must contain exactly one JSON object")
	}
	if a.Cfg.Auth.BootstrapTenant == "" || a.Cfg.Auth.BootstrapTenant == tenant.System {
		return fmt.Errorf("auth.bootstrap_tenant must name a non-system tenant")
	}
	ctx = tenant.SystemContext(ctx, a.Cfg.Auth.BootstrapTenant)
	for _, in := range cfg.Servers {
		_, err := a.Infra.Store.MCPServers.FindOne(ctx, repo.Filter{"name": in.Name})
		if err == nil {
			continue
		}
		if !errs.Is(err, errs.CodeNotFound) {
			return err
		}
		if _, err := a.Integrations.CreateServer(ctx, in); err != nil {
			return fmt.Errorf("MCP server %q: %w", in.Name, err)
		}
	}
	for _, in := range cfg.Sources {
		_, err := a.Infra.Store.Retrievals.FindOne(ctx, repo.Filter{"name": in.Name})
		if err == nil {
			continue
		}
		if !errs.Is(err, errs.CodeNotFound) {
			return err
		}
		if _, err := a.Integrations.CreateSource(ctx, in); err != nil {
			return fmt.Errorf("retrieval source %q: %w", in.Name, err)
		}
	}
	for _, role := range cfg.Roles {
		// Query the tenant layer, not LatestPublished (which can return a built-in).
		_, err := a.Infra.Store.Roles.FindOne(ctx, repo.Filter{"key": role.Key})
		if err == nil {
			continue
		}
		if !errs.Is(err, errs.CodeNotFound) {
			return err
		}
		if _, err := a.Admin.Roles().SaveDraft(ctx, role.Key, &role); err != nil {
			return fmt.Errorf("role %q: %w", role.Key, err)
		}
		if _, err := a.Admin.Roles().Publish(ctx, role.Key, "local bootstrap"); err != nil {
			return fmt.Errorf("publish role %q: %w", role.Key, err)
		}
	}
	return nil
}
