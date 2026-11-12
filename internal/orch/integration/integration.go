// Package integration holds the tenant-admin use cases for external tool
// sources (§6.2, §9.9): MCP servers, retrieval sources, the tool catalog with
// risk overrides, the console tool tester and template availability.
//
// Credentials are accepted on write, stored in the secret store under a
// tenant reference and never returned, logged or audited.
package integration

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"time"

	"github.com/eternityx/lagent/internal/capability/mcp"
	"github.com/eternityx/lagent/internal/capability/retrieval"
	"github.com/eternityx/lagent/internal/capability/tool"
	"github.com/eternityx/lagent/internal/core/clock"
	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/id"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/queue"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/infra/sandbox"
	"github.com/eternityx/lagent/internal/infra/secret"
)

// Service implements the integration use cases.
type Service struct {
	Store    *repo.Store
	Secrets  secret.Store
	MCP      *mcp.Manager
	Registry *tool.Registry
	Sandbox  *sandbox.Sandbox // optional: scratch workspace for the tool tester
	Queue    queue.Queue      // optional: background sync after configuration changes
	Clock    clock.Clock
	Log      *slog.Logger
}

func (s *Service) now() time.Time {
	if s.Clock == nil {
		return time.Now().UTC()
	}
	return s.Clock.Now()
}

var nameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,40}$`)

func requireAdmin(ctx context.Context) error {
	p, _ := tenant.From(ctx)
	if !p.IsAdmin() && p.Kind != tenant.KindSystem {
		return errs.New(errs.CodeForbidden, "tenant admin only")
	}
	return nil
}

func (s *Service) audit(ctx context.Context, action, target string, detail map[string]any) {
	p, _ := tenant.From(ctx)
	if p.TenantID == "" {
		return
	}
	a := &types.AuditLog{Actor: p, Action: action, Target: target, Detail: detail}
	a.TenantID = p.TenantID
	if err := s.Store.Audit.Insert(context.WithoutCancel(ctx), a); err != nil && s.Log != nil {
		s.Log.Warn("audit", "action", action, "err", err)
	}
}

// ---------------------------------------------------------------- MCP servers

// ServerInput is the writable part of an MCP server. Secret is write-only:
// nil keeps the stored credential, "" removes it, anything else replaces it.
type ServerInput struct {
	Name          string                     `json:"name"`
	Transport     types.MCPTransport         `json:"transport"`
	URL           string                     `json:"url,omitempty"`
	Command       []string                   `json:"command,omitempty"`
	Env           map[string]string          `json:"env,omitempty"`
	AuthHeader    string                     `json:"auth_header,omitempty"`
	Enabled       *bool                      `json:"enabled,omitempty"`
	RiskOverrides map[string]types.RiskLevel `json:"risk_overrides,omitempty"`
	Secret        *string                    `json:"secret,omitempty"`
}

// ServerTool is one synced tool of a server as the console shows it.
type ServerTool struct {
	Name         string          `json:"name"`          // MCP tool name
	RegistryName string          `json:"registry_name"` // <server>__<tool>
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema"`
	Risk         types.RiskLevel `json:"risk"` // base risk after the server's explicit overrides
}

// ServerView is an MCP server as returned by the API: no secret, no secret
// reference, only whether a credential is set.
type ServerView struct {
	ID            string                     `json:"id"`
	Version       int64                      `json:"version"`
	Name          string                     `json:"name"`
	Transport     types.MCPTransport         `json:"transport"`
	URL           string                     `json:"url,omitempty"`
	Command       []string                   `json:"command,omitempty"`
	Env           map[string]string          `json:"env,omitempty"`
	AuthHeader    string                     `json:"auth_header,omitempty"`
	HasSecret     bool                       `json:"has_secret"`
	Enabled       bool                       `json:"enabled"`
	Status        string                     `json:"status"`
	LastError     string                     `json:"last_error,omitempty"`
	Tools         []ServerTool               `json:"tools"`
	RiskOverrides map[string]types.RiskLevel `json:"risk_overrides,omitempty"`
	SyncedAt      *time.Time                 `json:"synced_at,omitempty"`
	CreatedAt     time.Time                  `json:"created_at"`
	UpdatedAt     time.Time                  `json:"updated_at"`
}

func viewServer(srv *types.MCPServer) *ServerView {
	v := &ServerView{
		ID: srv.ID, Version: srv.Version, Name: srv.Name, Transport: srv.Transport, URL: srv.URL,
		Command: srv.Command, Env: srv.Env, AuthHeader: srv.AuthHeader, HasSecret: srv.SecretRef != "",
		Enabled: srv.Enabled, Status: srv.Status, LastError: srv.LastError, RiskOverrides: srv.RiskOverrides,
		SyncedAt: srv.SyncedAt, CreatedAt: srv.CreatedAt, UpdatedAt: srv.UpdatedAt, Tools: []ServerTool{},
	}
	for _, t := range srv.Tools {
		v.Tools = append(v.Tools, ServerTool{
			Name: t.Name, RegistryName: mcp.ToolName(srv.Name, t.Name), Description: t.Description,
			InputSchema: t.InputSchema, Risk: mcp.BaseRisk(srv, t.Name),
		})
	}
	return v
}

func validateServer(ctx context.Context, in *ServerInput) error {
	if !nameRE.MatchString(in.Name) {
		return errs.Invalid("name must be 1-40 characters of letters, digits, '_' or '-'")
	}
	switch in.Transport {
	case types.MCPStreamable, types.MCPSSE:
		u, err := url.Parse(in.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errs.Invalid("url must be an http(s) URL")
		}
		if u.User != nil {
			return errs.Invalid("put credentials in secret, not in the URL")
		}
	case types.MCPStdio:
		if len(in.Command) == 0 || in.Command[0] == "" {
			return errs.Invalid("command is required for stdio servers")
		}
		// A stdio server is a process on the platform hosts, so only platform
		// admins may configure one; tenant admins use remote transports.
		p, _ := tenant.From(ctx)
		if p.Role != tenant.RolePlatformAdmin && p.Kind != tenant.KindSystem {
			return errs.New(errs.CodeForbidden, "stdio MCP servers can only be configured by a platform admin")
		}
	default:
		return errs.Invalid("transport must be one of http, sse, stdio")
	}
	for k, lvl := range in.RiskOverrides {
		if !lvl.Valid() {
			return errs.Invalid("risk_overrides[%s]: unknown level %q", k, lvl)
		}
	}
	return nil
}

// ListServers lists the tenant's MCP servers.
func (s *Service) ListServers(ctx context.Context) ([]*ServerView, error) {
	all, err := repo.All(ctx, s.Store.MCPServers, repo.Query{}, 0)
	if err != nil {
		return nil, err
	}
	out := make([]*ServerView, 0, len(all))
	for _, srv := range all {
		out = append(out, viewServer(srv))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// GetServer returns one server.
func (s *Service) GetServer(ctx context.Context, sid string) (*ServerView, error) {
	srv, err := s.Store.MCPServers.Get(ctx, sid)
	if err != nil {
		return nil, err
	}
	return viewServer(srv), nil
}

// CreateServer stores a new server and its credential.
func (s *Service) CreateServer(ctx context.Context, in ServerInput) (*ServerView, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if err := validateServer(ctx, &in); err != nil {
		return nil, err
	}
	srv := &types.MCPServer{
		Name: in.Name, Transport: in.Transport, URL: in.URL, Command: in.Command, Env: in.Env,
		AuthHeader: in.AuthHeader, Enabled: in.Enabled == nil || *in.Enabled, Status: "unknown",
		RiskOverrides: in.RiskOverrides,
	}
	srv.ID = id.NewWithPrefix("mcp")
	if in.Secret != nil && *in.Secret != "" {
		ref, err := s.secretRef(ctx, srv.ID)
		if err != nil {
			return nil, err
		}
		if err := s.Secrets.Put(ctx, ref, *in.Secret); err != nil {
			return nil, err
		}
		srv.SecretRef = ref
	}
	if err := s.Store.MCPServers.Insert(ctx, srv); err != nil {
		if srv.SecretRef != "" {
			_ = s.Secrets.Delete(ctx, srv.SecretRef)
		}
		if errs.Is(err, errs.CodeConflict) {
			return nil, errs.Conflict("an MCP server named %q already exists", in.Name)
		}
		return nil, err
	}
	s.audit(ctx, "mcp.create", srv.ID, map[string]any{"name": srv.Name, "transport": srv.Transport, "has_secret": srv.SecretRef != ""})
	s.changed(ctx, srv)
	return viewServer(srv), nil
}

func (s *Service) secretRef(ctx context.Context, serverID string) (string, error) {
	return secret.TenantRef(ctx, "mcp/"+serverID)
}

// UpdateServer replaces the editable fields. The name is immutable because
// retrieval sources and role tool lists ("mcp:<server>") refer to it.
func (s *Service) UpdateServer(ctx context.Context, sid string, in ServerInput) (*ServerView, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	cur, err := s.Store.MCPServers.Get(ctx, sid)
	if err != nil {
		return nil, err
	}
	if in.Name == "" {
		in.Name = cur.Name
	}
	if in.Name != cur.Name {
		return nil, errs.Invalid("the name of an MCP server cannot change; create a new server instead")
	}
	if err := validateServer(ctx, &in); err != nil {
		return nil, err
	}
	ref, err := s.secretRef(ctx, sid)
	if err != nil {
		return nil, err
	}
	setSecret := in.Secret != nil && *in.Secret != ""
	clearSecret := in.Secret != nil && *in.Secret == ""
	if setSecret {
		// Written before the record so a server never points at a missing secret.
		if err := s.Secrets.Put(ctx, ref, *in.Secret); err != nil {
			return nil, err
		}
	}
	srv, err := repo.Update(ctx, s.Store.MCPServers, sid, func(v *types.MCPServer) error {
		v.Transport, v.URL, v.Command, v.Env, v.AuthHeader = in.Transport, in.URL, in.Command, in.Env, in.AuthHeader
		v.RiskOverrides = in.RiskOverrides
		if in.Enabled != nil {
			v.Enabled = *in.Enabled
		}
		switch {
		case setSecret:
			v.SecretRef = ref
		case clearSecret:
			v.SecretRef = ""
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if clearSecret {
		_ = s.Secrets.Delete(ctx, ref)
	}
	s.audit(ctx, "mcp.update", sid, map[string]any{
		"name": srv.Name, "transport": srv.Transport, "enabled": srv.Enabled,
		"secret_changed": setSecret || clearSecret, "risk_overrides": srv.RiskOverrides,
	})
	s.changed(ctx, srv)
	return viewServer(srv), nil
}

// DeleteServer removes a server and its credential. Retrieval sources on it
// stay and simply become unavailable, which the template availability shows.
func (s *Service) DeleteServer(ctx context.Context, sid string) error {
	if err := requireAdmin(ctx); err != nil {
		return err
	}
	srv, err := s.Store.MCPServers.Get(ctx, sid)
	if err != nil {
		return err
	}
	if err := s.Store.MCPServers.Delete(ctx, sid); err != nil {
		return err
	}
	if srv.SecretRef != "" {
		_ = s.Secrets.Delete(ctx, srv.SecretRef)
	}
	s.MCP.Invalidate(ctx, srv.Name)
	s.audit(ctx, "mcp.delete", sid, map[string]any{"name": srv.Name})
	return nil
}

// changed drops cached sessions and, when a queue is configured, schedules a
// background sync so the tool list follows the configuration.
func (s *Service) changed(ctx context.Context, srv *types.MCPServer) {
	s.MCP.Invalidate(ctx, srv.Name)
	if s.Queue == nil || !srv.Enabled {
		return
	}
	j, err := queue.NewJob(queue.TypeMCPSync, srv.TenantID, SyncJob{ServerID: srv.ID})
	if err != nil {
		return
	}
	j.UniqueKey = "mcp.sync:" + srv.ID
	j.MaxRetry = 2
	if err := s.Queue.Enqueue(ctx, j); err != nil && s.Log != nil {
		s.Log.Warn("enqueue mcp sync", "server", srv.Name, "err", err)
	}
}

// SyncJob is the payload of queue.TypeMCPSync.
type SyncJob struct {
	ServerID string `json:"server_id"`
}

// HandleSyncJob is the queue handler for background syncs. Connection
// failures are recorded on the server, not retried forever.
func (s *Service) HandleSyncJob(ctx context.Context, j queue.Job) error {
	var p SyncJob
	if err := j.Decode(&p); err != nil {
		return queue.SkipRetry(err)
	}
	_, err := s.SyncServer(ctx, p.ServerID)
	if errs.Is(err, errs.CodeNotFound) {
		return nil // deleted meanwhile
	}
	return err
}

// SyncServer connects, lists the tools and stores them with the status. A
// connection failure is a recorded state (Status=error), not an API error.
func (s *Service) SyncServer(ctx context.Context, sid string) (*ServerView, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	cur, err := s.Store.MCPServers.Get(ctx, sid)
	if err != nil {
		return nil, err
	}
	// A fresh session: sync is the moment an admin wants the truth, and it
	// works for disabled servers too (sync before enabling).
	tools, perr := s.MCP.Probe(ctx, cur)
	now := s.now()
	srv, err := repo.Update(ctx, s.Store.MCPServers, sid, func(v *types.MCPServer) error {
		v.SyncedAt = &now
		if perr != nil {
			// Keep the last known tool list: a transient outage should not
			// make every template that depends on it "needs configuration".
			v.Status, v.LastError = "error", perr.Error()
			return nil
		}
		v.Status, v.LastError, v.Tools = "ok", "", tools
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.MCP.Invalidate(ctx, srv.Name)
	detail := map[string]any{"name": srv.Name, "status": srv.Status, "tools": len(srv.Tools)}
	if perr != nil {
		detail["error"] = srv.LastError
	}
	s.audit(ctx, "mcp.sync", sid, detail)
	return viewServer(srv), nil
}

// TestResult is the outcome of a connection test.
type TestResult struct {
	OK    bool     `json:"ok"`
	Error string   `json:"error,omitempty"`
	Tools []string `json:"tools,omitempty"`
	MS    int64    `json:"duration_ms"`
}

// TestServer opens a throwaway session and lists tools without storing anything.
func (s *Service) TestServer(ctx context.Context, sid string) (*TestResult, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	srv, err := s.Store.MCPServers.Get(ctx, sid)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	tools, perr := s.MCP.Probe(ctx, srv)
	res := &TestResult{OK: perr == nil, MS: time.Since(start).Milliseconds(), Tools: []string{}}
	if perr != nil {
		res.Error = perr.Error()
	}
	for _, t := range tools {
		res.Tools = append(res.Tools, t.Name)
	}
	return res, nil
}

// ---------------------------------------------------------- retrieval sources

// SourceInput is the writable part of a retrieval source.
type SourceInput struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Server      string          `json:"server"`
	Tool        string          `json:"tool"`
	FixedArgs   json.RawMessage `json:"fixed_args,omitempty"`
	QueryField  string          `json:"query_field,omitempty"`
}

func (s *Service) validateSource(ctx context.Context, in *SourceInput) error {
	if !nameRE.MatchString(in.Name) {
		return errs.Invalid("name must be 1-40 characters of letters, digits, '_' or '-'")
	}
	if in.Server == "" || in.Tool == "" {
		return errs.Invalid("server and tool are required")
	}
	if in.QueryField == "" {
		in.QueryField = retrieval.DefaultQueryField
	}
	if len(in.FixedArgs) > 0 && string(in.FixedArgs) != "null" {
		var m map[string]any
		if err := json.Unmarshal(in.FixedArgs, &m); err != nil {
			return errs.Invalid("fixed_args must be a JSON object")
		}
	}
	srv, err := s.Store.MCPServers.FindOne(ctx, repo.Filter{"name": in.Server})
	if errs.Is(err, errs.CodeNotFound) {
		return errs.Invalid("MCP server %q does not exist", in.Server)
	}
	if err != nil {
		return err
	}
	if srv.SyncedAt != nil && srv.Status == "ok" && !hasTool(srv, in.Tool) {
		return errs.Invalid("MCP server %q has no tool %q (sync the server if it was added recently)", in.Server, in.Tool)
	}
	return nil
}

func hasTool(srv *types.MCPServer, name string) bool {
	for _, t := range srv.Tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

// ListSources lists the tenant's retrieval sources.
func (s *Service) ListSources(ctx context.Context) ([]*types.RetrievalSource, error) {
	all, err := repo.All(ctx, s.Store.Retrievals, repo.Query{}, 0)
	if err != nil {
		return nil, err
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	if all == nil {
		all = []*types.RetrievalSource{}
	}
	return all, nil
}

// GetSource returns one retrieval source.
func (s *Service) GetSource(ctx context.Context, sid string) (*types.RetrievalSource, error) {
	return s.Store.Retrievals.Get(ctx, sid)
}

// CreateSource adds a retrieval source.
func (s *Service) CreateSource(ctx context.Context, in SourceInput) (*types.RetrievalSource, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if err := s.validateSource(ctx, &in); err != nil {
		return nil, err
	}
	src := &types.RetrievalSource{Name: in.Name, Description: in.Description, Server: in.Server, Tool: in.Tool, FixedArgs: in.FixedArgs, QueryField: in.QueryField}
	if err := s.Store.Retrievals.Insert(ctx, src); err != nil {
		if errs.Is(err, errs.CodeConflict) {
			return nil, errs.Conflict("a retrieval source named %q already exists", in.Name)
		}
		return nil, err
	}
	s.audit(ctx, "retrieval.create", src.ID, map[string]any{"name": src.Name, "server": src.Server, "tool": src.Tool})
	return src, nil
}

// UpdateSource replaces a source's binding. The name is immutable because
// workflow templates refer to it.
func (s *Service) UpdateSource(ctx context.Context, sid string, in SourceInput) (*types.RetrievalSource, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	cur, err := s.Store.Retrievals.Get(ctx, sid)
	if err != nil {
		return nil, err
	}
	if in.Name == "" {
		in.Name = cur.Name
	}
	if in.Name != cur.Name {
		return nil, errs.Invalid("the name of a retrieval source cannot change; templates refer to it")
	}
	if err := s.validateSource(ctx, &in); err != nil {
		return nil, err
	}
	src, err := repo.Update(ctx, s.Store.Retrievals, sid, func(v *types.RetrievalSource) error {
		v.Description, v.Server, v.Tool, v.FixedArgs, v.QueryField = in.Description, in.Server, in.Tool, in.FixedArgs, in.QueryField
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.audit(ctx, "retrieval.update", sid, map[string]any{"name": src.Name, "server": src.Server, "tool": src.Tool})
	return src, nil
}

// DeleteSource removes a source; templates using it become "needs configuration".
func (s *Service) DeleteSource(ctx context.Context, sid string) error {
	if err := requireAdmin(ctx); err != nil {
		return err
	}
	src, err := s.Store.Retrievals.Get(ctx, sid)
	if err != nil {
		return err
	}
	if err := s.Store.Retrievals.Delete(ctx, sid); err != nil {
		return err
	}
	s.audit(ctx, "retrieval.delete", sid, map[string]any{"name": src.Name})
	return nil
}

// TryResult is the outcome of a console test query.
type TryResult struct {
	Content string `json:"content"`
	IsError bool   `json:"is_error"`
	MS      int64  `json:"duration_ms"`
}

// TrySource runs a query against a source from the console.
func (s *Service) TrySource(ctx context.Context, sid, query string, limit int) (*TryResult, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if query == "" {
		return nil, errs.Invalid("query is required")
	}
	src, err := s.Store.Retrievals.Get(ctx, sid)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	res, err := retrieval.SearchSource(ctx, s.MCP, src, query, limit)
	if err != nil {
		if errs.CodeOf(err) == errs.CodeInternal {
			return nil, err
		}
		return &TryResult{Content: err.Error(), IsError: true, MS: time.Since(start).Milliseconds()}, nil
	}
	return &TryResult{Content: res.Text, IsError: res.IsError, MS: time.Since(start).Milliseconds()}, nil
}

// ------------------------------------------------------ template availability

// AvailableSources reports, per retrieval source name, whether it can serve
// queries now: its server exists, is enabled, last synced fine and still
// offers the bound tool.
func (s *Service) AvailableSources(ctx context.Context) (map[string]bool, error) {
	srcs, err := repo.All(ctx, s.Store.Retrievals, repo.Query{}, 0)
	if err != nil {
		return nil, err
	}
	servers, err := repo.All(ctx, s.Store.MCPServers, repo.Query{}, 0)
	if err != nil {
		return nil, err
	}
	byName := map[string]*types.MCPServer{}
	for _, srv := range servers {
		byName[srv.Name] = srv
	}
	out := map[string]bool{}
	for _, src := range srcs {
		srv := byName[src.Server]
		out[src.Name] = srv != nil && srv.Enabled && srv.Status != "error" && hasTool(srv, src.Tool)
	}
	return out, nil
}

// TemplateStatus is the availability of one workflow template.
type TemplateStatus struct {
	Key         string   `json:"key"`
	Ver         int      `json:"ver"`
	Name        string   `json:"name"`
	Sources     []string `json:"sources"`
	Missing     []string `json:"missing"`
	NeedsConfig bool     `json:"needs_config"`
	BuiltIn     bool     `json:"built_in"`
}

// TemplateAvailability computes, per workflow definition key (newest
// non-disabled version, tenant layer over built-in), the missing retrieval
// sources. It is derived from the current configuration on every call, so
// it is recomputed whenever MCP or retrieval configuration changes.
func (s *Service) TemplateAvailability(ctx context.Context) ([]TemplateStatus, error) {
	avail, err := s.AvailableSources(ctx)
	if err != nil {
		return nil, err
	}
	best := map[string]*types.WorkflowDef{}
	builtIn := map[string]bool{}
	for i, cx := range []context.Context{repo.SystemLayer(ctx), ctx} {
		defs, err := repo.All(cx, s.Store.WorkflowDefs, repo.Query{}, 0)
		if err != nil {
			return nil, err
		}
		layer := map[string]*types.WorkflowDef{}
		for _, d := range defs {
			if d.Status == types.AssetDisabled {
				continue
			}
			if b := layer[d.Key]; b == nil || d.Ver > b.Ver {
				layer[d.Key] = d
			}
		}
		for k, d := range layer {
			best[k] = d // the tenant layer (second) wins
			builtIn[k] = i == 0
		}
	}
	out := make([]TemplateStatus, 0, len(best))
	for k, d := range best {
		missing := retrieval.MissingSources(d, avail)
		if missing == nil {
			missing = []string{}
		}
		// With nothing available, MissingSources lists every dependency.
		sources := retrieval.MissingSources(d, nil)
		if sources == nil {
			sources = []string{}
		}
		out = append(out, TemplateStatus{
			Key: k, Ver: d.Ver, Name: d.Name, Sources: sources,
			Missing: missing, NeedsConfig: len(missing) > 0, BuiltIn: builtIn[k],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
