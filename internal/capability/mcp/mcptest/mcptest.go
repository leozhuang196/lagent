// Package mcptest provides an in-process MCP server with two tools for tests:
// "docs.search" echoes its arguments (a read-only lookup) and
// "create_ticket" pretends to create something (a side effect).
package mcptest

import (
	"context"
	"encoding/json"
	"sync"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/eternityx/lagent/internal/core/types"
)

// Fake is an MCP server reachable through in-memory transports.
type Fake struct {
	Server *sdk.Server

	mu       sync.Mutex
	dials    int
	secrets  []string
	sessions []*sdk.ServerSession
	calls    []Call
}

// Call records one tool call the server received.
type Call struct {
	Tool string
	Args map[string]any
}

// New builds the fake server.
func New() *Fake {
	f := &Fake{Server: sdk.NewServer(&sdk.Implementation{Name: "fake", Version: "1"}, nil)}
	f.Server.AddTool(&sdk.Tool{
		Name:        "docs.search",
		Description: "Search the docs",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"query": map[string]any{"type": "string"}, "index": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"},
		}},
	}, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		args := f.record(req)
		b, _ := json.Marshal(args)
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "results for"}, &sdk.TextContent{Text: string(b)}}}, nil
	})
	f.Server.AddTool(&sdk.Tool{
		Name:        "create_ticket",
		Description: "Create a ticket",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"title": map[string]any{"type": "string"}}},
	}, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		args := f.record(req)
		if args["title"] == nil {
			return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: "title is required"}}}, nil
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "created ticket"}}}, nil
	})
	return f
}

func (f *Fake) record(req *sdk.CallToolRequest) map[string]any {
	var args map[string]any
	_ = json.Unmarshal(req.Params.Arguments, &args)
	f.mu.Lock()
	f.calls = append(f.calls, Call{Tool: req.Params.Name, Args: args})
	f.mu.Unlock()
	return args
}

// Dial matches mcp.Options.Dial: every dial opens a new in-memory session.
func (f *Fake) Dial(ctx context.Context, srv *types.MCPServer, secret string) (sdk.Transport, error) {
	ct, st := sdk.NewInMemoryTransports()
	ss, err := f.Server.Connect(context.Background(), st, nil)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.dials++
	f.secrets = append(f.secrets, secret)
	f.sessions = append(f.sessions, ss)
	f.mu.Unlock()
	return ct, nil
}

// Dials counts connections opened.
func (f *Fake) Dials() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dials
}

// Secrets lists the secret passed to each dial.
func (f *Fake) Secrets() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.secrets...)
}

// Calls lists the tool calls received.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// DropSessions closes every server-side session, simulating a server restart.
func (f *Fake) DropSessions() {
	f.mu.Lock()
	ss := f.sessions
	f.sessions = nil
	f.mu.Unlock()
	for _, s := range ss {
		_ = s.Close()
	}
}
