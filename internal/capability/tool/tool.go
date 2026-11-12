// Package tool defines the single shape every tool takes (builtin, connector,
// MCP, platform) and the per-tenant registry. The engine sees only this shape.
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/sandbox"
)

// ControlKind lets a tool steer the turn instead of returning plain output.
type ControlKind string

const (
	ControlNone        ControlKind = ""
	ControlQuestion    ControlKind = "question"     // suspend on a question to a person
	ControlSubtasks    ControlKind = "subtasks"     // suspend until child turns finish
	ControlExternalJob ControlKind = "external_job" // suspend until an external job ends
	ControlResult      ControlKind = "result"       // structured result of a step/subtask
)

// Control is a turn-steering request from a tool.
type Control struct {
	Kind         ControlKind
	Question     string
	Options      []string
	ChildTurnIDs []string
	ExternalRef  string
	Result       *types.StepResult
}

// Result is a tool's output.
type Result struct {
	Content   string
	IsError   bool
	Artifacts []string // artifact ids produced
	Control   *Control
}

// Errorf builds an error result shown to the model (not a Go error).
func Errorf(format string, args ...any) Result {
	return Result{Content: fmt.Sprintf(format, args...), IsError: true}
}

// Env is what a tool may use during a call. Tools never see orchestration objects.
type Env struct {
	TenantID       string
	SessionID      string
	TurnID         string
	ToolUseID      string
	IdempotencyKey string
	Principal      tenant.Principal
	Workspace      *sandbox.Workspace
	RoleSkills     []string
	MemorySpaces   []string
	Channels       []types.Channel
	Debug          bool
	// Archive stores a workspace file as a deliverable and returns its id.
	Archive func(ctx context.Context, relPath, name string) (string, error)
	// Secret reads a credential of the tenant.
	Secret func(ctx context.Context, ref string) (string, error)
}

// Tool is a callable the model can choose.
type Tool interface {
	Spec() types.ToolSpec
	Meta() types.ToolMeta
	Call(ctx context.Context, input json.RawMessage, env Env) (Result, error)
}

// Provider supplies tenant-specific tools (MCP servers, retrieval sources).
type Provider interface {
	Tools(ctx context.Context) ([]Tool, error)
}

// Registry resolves tool names to implementations for the tenant in ctx.
type Registry struct {
	mu        sync.RWMutex
	global    map[string]Tool
	providers []Provider
}

func NewRegistry() *Registry { return &Registry{global: map[string]Tool{}} }

// Register adds a global tool (builtin, connector, platform).
func (r *Registry) Register(ts ...Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range ts {
		name := t.Spec().Name
		if _, dup := r.global[name]; dup {
			panic("duplicate tool " + name)
		}
		r.global[name] = t
	}
}

// AddProvider adds a source of tenant tools.
func (r *Registry) AddProvider(p Provider) {
	r.mu.Lock()
	r.providers = append(r.providers, p)
	r.mu.Unlock()
}

func (r *Registry) all(ctx context.Context) (map[string]Tool, error) {
	r.mu.RLock()
	out := make(map[string]Tool, len(r.global))
	for k, v := range r.global {
		out[k] = v
	}
	providers := append([]Provider(nil), r.providers...)
	r.mu.RUnlock()
	for _, p := range providers {
		ts, err := p.Tools(ctx)
		if err != nil {
			return nil, err
		}
		for _, t := range ts {
			if _, dup := out[t.Spec().Name]; !dup {
				out[t.Spec().Name] = t
			}
		}
	}
	return out, nil
}

// List returns the metadata of every tool available to the tenant, by name.
func (r *Registry) List(ctx context.Context) ([]types.ToolMeta, error) {
	all, err := r.all(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]types.ToolMeta, 0, len(all))
	for _, t := range all {
		out = append(out, t.Meta())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Resolve returns the tools for names, in name order, and the names it could not find.
func (r *Registry) Resolve(ctx context.Context, names []string) ([]Tool, []string, error) {
	all, err := r.all(ctx)
	if err != nil {
		return nil, nil, err
	}
	var tools []Tool
	var missing []string
	for _, n := range names {
		if t, ok := all[n]; ok {
			tools = append(tools, t)
		} else {
			missing = append(missing, n)
		}
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Spec().Name < tools[j].Spec().Name })
	return tools, missing, nil
}

// Get returns one tool.
func (r *Registry) Get(ctx context.Context, name string) (Tool, error) {
	all, err := r.all(ctx)
	if err != nil {
		return nil, err
	}
	t, ok := all[name]
	if !ok {
		return nil, errs.NotFound("tool", name)
	}
	return t, nil
}

// WrapData marks tool output as data, never instructions (§8.2).
func WrapData(source, content string) string {
	content = strings.ReplaceAll(content, "</tool_data>", "</tool_data_>")
	return fmt.Sprintf("<tool_data source=%q>\n%s\n</tool_data>", source, content)
}

// Func is a convenience Tool built from a function.
type Func struct {
	S  types.ToolSpec
	M  types.ToolMeta
	Fn func(ctx context.Context, input json.RawMessage, env Env) (Result, error)
}

func (f *Func) Spec() types.ToolSpec { return f.S }
func (f *Func) Meta() types.ToolMeta {
	m := f.M
	m.Name = f.S.Name
	return m
}
func (f *Func) Call(ctx context.Context, in json.RawMessage, env Env) (Result, error) {
	return f.Fn(ctx, in, env)
}

// Schema builds a JSON schema object from properties and required names.
func Schema(props map[string]any, required ...string) json.RawMessage {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	raw, _ := json.Marshal(s)
	return raw
}

// Str is a string property.
func Str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

// Decode parses tool input strictly into v and reports problems as a model-facing error.
func Decode(input json.RawMessage, v any) *Result {
	if len(input) == 0 {
		input = []byte("{}")
	}
	if err := json.Unmarshal(input, v); err != nil {
		r := Errorf("invalid input: %v", err)
		return &r
	}
	return nil
}
