// Command lagent is the single binary of the platform. The same image runs
// every role: `lagent serve --roles=api,engine,worker,feishu`.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/eternityx/lagent/internal/app"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
)

var version = "dev"

func usage() {
	fmt.Fprintf(os.Stderr, `lagent %s

Usage:
  lagent serve   [--config FILE] [--roles api,engine,worker,feishu]
  lagent tenant create --id ID [--name NAME] [--config FILE]
  lagent key create --tenant ID --name NAME [--role member|tenant_admin|platform_admin] [--max-mode ask_all|ask_external|auto] [--config FILE]
  lagent config check [--config FILE]
  lagent version
`, version)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(os.Args[2:])
	case "tenant":
		err = tenantCmd(os.Args[2:])
	case "key":
		err = keyCmd(os.Args[2:])
	case "config":
		err = configCmd(os.Args[2:])
	case "version":
		fmt.Println(version)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func loadConfig(fs *flag.FlagSet, args []string) (*app.Config, *flag.FlagSet, error) {
	cfgPath := fs.String("config", os.Getenv("LAGENT_CONFIG"), "config file")
	roles := fs.String("roles", "", "comma-separated roles (overrides config)")
	if err := fs.Parse(args); err != nil {
		return nil, nil, err
	}
	cfg, err := app.Load(*cfgPath)
	if err != nil {
		return nil, nil, err
	}
	if *roles != "" {
		cfg.Roles = app.ParseRoles(*roles)
		if err := cfg.Validate(); err != nil {
			return nil, nil, err
		}
	}
	return cfg, fs, nil
}

func serve(args []string) error {
	cfg, _, err := loadConfig(flag.NewFlagSet("serve", flag.ExitOnError), args)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a, err := app.New(ctx, cfg)
	if err != nil {
		return err
	}
	a.Log.Info("lagent starting", "version", version, "config", a.Describe())
	return a.Run(ctx)
}

func adminCtx(tenantID string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{Kind: tenant.KindSystem, ID: "cli", TenantID: tenantID, Role: tenant.RolePlatformAdmin, Name: "cli"})
}

func tenantCmd(args []string) error {
	if len(args) == 0 || args[0] != "create" {
		usage()
		return nil
	}
	fs := flag.NewFlagSet("tenant create", flag.ExitOnError)
	idv := fs.String("id", "", "tenant id (slug)")
	name := fs.String("name", "", "display name")
	cfg, _, err := loadConfig(fs, args[1:])
	if err != nil {
		return err
	}
	a, err := app.New(context.Background(), cfg)
	if err != nil {
		return err
	}
	t := &types.Tenant{Name: *name}
	t.ID = *idv
	if t.Name == "" {
		t.Name = t.ID
	}
	created, err := a.Admin.CreateTenant(adminCtx(*idv), t)
	if err != nil {
		return err
	}
	return printJSON(created)
}

func keyCmd(args []string) error {
	if len(args) == 0 || args[0] != "create" {
		usage()
		return nil
	}
	fs := flag.NewFlagSet("key create", flag.ExitOnError)
	tid := fs.String("tenant", "", "tenant id")
	name := fs.String("name", "", "key name")
	role := fs.String("role", "member", "member | tenant_admin | platform_admin")
	maxMode := fs.String("max-mode", "ask_all", "highest permission mode sessions of this key may use")
	cfg, _, err := loadConfig(fs, args[1:])
	if err != nil {
		return err
	}
	if *tid == "" || *name == "" {
		return fmt.Errorf("--tenant and --name are required")
	}
	a, err := app.New(context.Background(), cfg)
	if err != nil {
		return err
	}
	k, err := a.Admin.CreateKey(adminCtx(*tid), *name, tenant.Role(*role), types.PermissionMode(*maxMode))
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Store this key now; it is not shown again.")
	return printJSON(k)
}

func configCmd(args []string) error {
	if len(args) == 0 || args[0] != "check" {
		usage()
		return nil
	}
	cfg, _, err := loadConfig(flag.NewFlagSet("config check", flag.ExitOnError), args[1:])
	if err != nil {
		return err
	}
	fmt.Println("ok:", cfg.Roles)
	return nil
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
