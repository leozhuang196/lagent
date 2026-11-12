package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/eternityx/lagent/internal/capability/tool"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/repo"
)

// MaxToolName is the longest tool name model providers accept.
const MaxToolName = 64

// ToolName is the registry name of an MCP tool: "<server>__<tool>",
// restricted to [a-zA-Z0-9_-] and at most 64 characters. Names that must be
// shortened keep a hash suffix so two long names cannot collide.
func ToolName(server, toolName string) string {
	full := sanitize(server) + "__" + sanitize(toolName)
	if len(full) <= MaxToolName {
		return full
	}
	sum := sha256.Sum256([]byte(server + "\x00" + toolName))
	suffix := "_" + hex.EncodeToString(sum[:])[:8]
	return full[:MaxToolName-len(suffix)] + suffix
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// BaseRisk is the risk of an MCP tool before tenant overrides: external
// (needs approval) unless the server configuration explicitly lowers it for
// this tool (§9.9). The override may be keyed by the MCP tool name or by the
// registry name. Unknown levels never lower anything.
func BaseRisk(srv *types.MCPServer, toolName string) types.RiskLevel {
	for _, k := range []string{toolName, ToolName(srv.Name, toolName)} {
		if lvl, ok := srv.RiskOverrides[k]; ok && lvl.Valid() {
			return lvl
		}
	}
	return types.RiskExternal
}

// MetaFor builds the platform metadata of one MCP tool.
func MetaFor(srv *types.MCPServer, toolName string) types.ToolMeta {
	risk := BaseRisk(srv, toolName)
	return types.ToolMeta{
		Name:       ToolName(srv.Name, toolName),
		Category:   "mcp",
		Source:     types.SourceMCP,
		Server:     srv.Name,
		Risk:       risk,
		SideEffect: risk != types.RiskReadOnly,
		ReadOnly:   risk == types.RiskReadOnly,
	}
}

// Provider returns the tool.Provider exposing every cached tool of every
// enabled server of the tenant in ctx.
func (m *Manager) Provider() tool.Provider { return provider{m} }

type provider struct{ m *Manager }

func (p provider) Tools(ctx context.Context) ([]tool.Tool, error) {
	servers, err := p.m.enabledServers(ctx)
	if err != nil {
		return nil, err
	}
	var out []tool.Tool
	for _, srv := range servers {
		for _, spec := range srv.Tools {
			out = append(out, &mcpTool{m: p.m, srv: srv, orig: spec, meta: MetaFor(srv, spec.Name)})
		}
	}
	return out, nil
}

// enabledServers reads the tenant's enabled servers, cached briefly because
// the registry is consulted on every turn.
func (m *Manager) enabledServers(ctx context.Context) ([]*types.MCPServer, error) {
	tid, err := tenant.ID(ctx)
	if err != nil {
		return nil, err
	}
	now := m.opts.Clock.Now()
	m.mu.Lock()
	c, ok := m.lists[tid]
	m.mu.Unlock()
	if ok && now.Sub(c.at) < m.opts.ProviderTTL {
		return c.servers, nil
	}
	servers, err := repo.All(ctx, m.store.MCPServers, repo.Query{Filter: repo.Filter{"enabled": true}}, 0)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.lists[tid] = cachedServers{at: now, servers: servers}
	m.mu.Unlock()
	return servers, nil
}

type mcpTool struct {
	m    *Manager
	srv  *types.MCPServer
	orig types.ToolSpec
	meta types.ToolMeta
}

func (t *mcpTool) Spec() types.ToolSpec {
	desc := "[" + t.srv.Name + "] " + t.orig.Description
	schema := t.orig.InputSchema
	if len(schema) == 0 {
		schema = json.RawMessage(`{"type":"object"}`)
	}
	return types.ToolSpec{Name: t.meta.Name, Description: desc, InputSchema: schema}
}

func (t *mcpTool) Meta() types.ToolMeta { return t.meta }

func (t *mcpTool) Call(ctx context.Context, input json.RawMessage, env tool.Env) (tool.Result, error) {
	res, err := t.m.CallTool(ctx, t.srv, t.orig.Name, input)
	if err != nil {
		// Shown to the model so it can adapt; the error carries no credential.
		return tool.Errorf("MCP tool %s failed: %v", t.meta.Name, err), nil
	}
	return tool.Result{Content: tool.WrapData("mcp:"+t.srv.Name+"/"+t.orig.Name, res.Text), IsError: res.IsError}, nil
}
