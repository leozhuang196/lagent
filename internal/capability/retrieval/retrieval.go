// Package retrieval exposes named retrieval sources ("an MCP tool + fixed
// arguments", §9.9) through one read-only search tool. Templates and roles
// reference source names only, never MCP server names, so swapping the
// backing server is a configuration change, not a template change.
package retrieval

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/eternityx/lagent/internal/capability/mcp"
	"github.com/eternityx/lagent/internal/capability/tool"
	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/repo"
)

// Caller is the slice of the MCP manager retrieval needs.
type Caller interface {
	Lookup(ctx context.Context, name string) (*types.MCPServer, error)
	CallTool(ctx context.Context, srv *types.MCPServer, toolName string, args json.RawMessage) (mcp.CallResult, error)
}

// DefaultQueryField is used when a source does not name its query field.
const DefaultQueryField = "query"

// Tools returns the search and list_sources tools.
func Tools(store *repo.Store, caller Caller) []tool.Tool {
	return []tool.Tool{searchTool(store, caller), listSourcesTool(store)}
}

// BuildArgs merges a source's fixed arguments with the query (and limit when
// the backing tool declares one). The query field wins over a fixed argument
// of the same name; nothing else the model sends reaches the tool, so fixed
// arguments cannot be overridden.
func BuildArgs(src *types.RetrievalSource, query string, limit int, toolSchema json.RawMessage) (json.RawMessage, error) {
	args := map[string]any{}
	if len(src.FixedArgs) > 0 && string(src.FixedArgs) != "null" {
		if err := json.Unmarshal(src.FixedArgs, &args); err != nil {
			return nil, errs.Invalid("fixed_args of source %q must be a JSON object", src.Name)
		}
	}
	field := src.QueryField
	if field == "" {
		field = DefaultQueryField
	}
	args[field] = query
	if limit > 0 && field != "limit" && schemaHas(toolSchema, "limit") {
		args["limit"] = limit
	}
	return json.Marshal(args)
}

func schemaHas(schema json.RawMessage, prop string) bool {
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(schema, &s) != nil {
		return false
	}
	_, ok := s.Properties[prop]
	return ok
}

// Search runs one query against a named source of the tenant in ctx. It is
// shared by the search tool and the console's "try query".
func Search(ctx context.Context, store *repo.Store, caller Caller, source, query string, limit int) (mcp.CallResult, error) {
	src, err := store.Retrievals.FindOne(ctx, repo.Filter{"name": source})
	if err != nil {
		if errs.Is(err, errs.CodeNotFound) {
			return mcp.CallResult{}, errs.NotFound("retrieval source", source)
		}
		return mcp.CallResult{}, err
	}
	return SearchSource(ctx, caller, src, query, limit)
}

// SearchSource runs a query against an already loaded source.
func SearchSource(ctx context.Context, caller Caller, src *types.RetrievalSource, query string, limit int) (mcp.CallResult, error) {
	srv, err := caller.Lookup(ctx, src.Server)
	if err != nil {
		if errs.Is(err, errs.CodeNotFound) {
			return mcp.CallResult{}, errs.Conflict("retrieval source %q is not configured: its server is missing", src.Name)
		}
		return mcp.CallResult{}, err
	}
	if !srv.Enabled {
		return mcp.CallResult{}, errs.Conflict("retrieval source %q is unavailable: its server is disabled", src.Name)
	}
	var schema json.RawMessage
	for _, t := range srv.Tools {
		if t.Name == src.Tool {
			schema = t.InputSchema
		}
	}
	args, err := BuildArgs(src, query, limit, schema)
	if err != nil {
		return mcp.CallResult{}, err
	}
	return caller.CallTool(ctx, srv, src.Tool, args)
}

func searchTool(store *repo.Store, caller Caller) tool.Tool {
	return &tool.Func{
		S: types.ToolSpec{
			Name:        "search",
			Description: "Search a configured retrieval source (use list_sources to see the available sources).",
			InputSchema: tool.Schema(map[string]any{
				"source": tool.Str("Retrieval source name"),
				"query":  tool.Str("What to look for"),
				"limit":  map[string]any{"type": "integer", "description": "Maximum number of results, if the source supports it"},
			}, "source", "query"),
		},
		// Retrieval is read-only by definition: an admin binding an MCP tool as
		// a retrieval source is the explicit statement that it only reads (§9.9).
		M: types.ToolMeta{Source: types.SourceBuiltin, Category: "retrieval", Risk: types.RiskReadOnly, ReadOnly: true},
		Fn: func(ctx context.Context, in json.RawMessage, env tool.Env) (tool.Result, error) {
			var a struct {
				Source string `json:"source"`
				Query  string `json:"query"`
				Limit  int    `json:"limit"`
			}
			if r := tool.Decode(in, &a); r != nil {
				return *r, nil
			}
			if strings.TrimSpace(a.Source) == "" || strings.TrimSpace(a.Query) == "" {
				return tool.Errorf("source and query are required"), nil
			}
			res, err := Search(ctx, store, caller, a.Source, a.Query, a.Limit)
			if err != nil {
				switch errs.CodeOf(err) {
				case errs.CodeNotFound, errs.CodeConflict, errs.CodeInvalid, errs.CodeUnavailable:
					return tool.Errorf("search on %q failed: %v", a.Source, err), nil
				}
				return tool.Result{}, err
			}
			return tool.Result{Content: tool.WrapData("retrieval:"+a.Source, res.Text), IsError: res.IsError}, nil
		},
	}
}

func listSourcesTool(store *repo.Store) tool.Tool {
	return &tool.Func{
		S: types.ToolSpec{
			Name:        "list_sources",
			Description: "List the retrieval sources available to the search tool.",
			InputSchema: tool.Schema(map[string]any{}),
		},
		M: types.ToolMeta{Source: types.SourceBuiltin, Category: "retrieval", Risk: types.RiskReadOnly, ReadOnly: true},
		Fn: func(ctx context.Context, in json.RawMessage, env tool.Env) (tool.Result, error) {
			srcs, err := repo.All(ctx, store.Retrievals, repo.Query{}, 500)
			if err != nil {
				return tool.Result{}, err
			}
			if len(srcs) == 0 {
				return tool.Result{Content: "(no retrieval sources configured)"}, nil
			}
			sort.Slice(srcs, func(i, j int) bool { return srcs[i].Name < srcs[j].Name })
			var b strings.Builder
			for _, s := range srcs {
				fmt.Fprintf(&b, "- %s", s.Name)
				if s.Description != "" {
					fmt.Fprintf(&b, ": %s", s.Description)
				}
				b.WriteByte('\n')
			}
			// Descriptions are admin-written, but still data, not instructions.
			return tool.Result{Content: tool.WrapData("retrieval:sources", strings.TrimRight(b.String(), "\n"))}, nil
		},
	}
}

// MissingSources returns the retrieval sources a workflow definition depends
// on that are not available, sorted and without duplicates. It is the only
// input to a template's "needs configuration" state.
func MissingSources(def *types.WorkflowDef, available map[string]bool) []string {
	if def == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, st := range def.Steps {
		for _, s := range st.Sources {
			if s == "" || seen[s] {
				continue
			}
			seen[s] = true
			if !available[s] {
				out = append(out, s)
			}
		}
	}
	sort.Strings(out)
	return out
}
