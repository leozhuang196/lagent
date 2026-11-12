// Package repo defines persistence interfaces. Implementations live in
// sub-packages (memory, mongo, postgres) and are only imported by app/.
//
// Interfaces take only the common subset of every backend (P5): equality and
// range filters, ordering by id, cursor pagination, optimistic versioning.
// The tenant always comes from the context and is never a parameter.
package repo

import (
	"context"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/types"
)

// OpKind is a filter operator.
type OpKind string

const (
	OpEq     OpKind = "eq"
	OpNe     OpKind = "ne"
	OpIn     OpKind = "in"
	OpLt     OpKind = "lt"
	OpLte    OpKind = "lte"
	OpGt     OpKind = "gt"
	OpGte    OpKind = "gte"
	OpExists OpKind = "exists"
)

// Op is a non-equality condition. A bare value in a Filter means equality.
type Op struct {
	Kind  OpKind
	Value any
}

func In[T any](vs ...T) Op {
	a := make([]any, len(vs))
	for i, v := range vs {
		a[i] = v
	}
	return Op{OpIn, a}
}
func Ne(v any) Op      { return Op{OpNe, v} }
func Lt(v any) Op      { return Op{OpLt, v} }
func Lte(v any) Op     { return Op{OpLte, v} }
func Gt(v any) Op      { return Op{OpGt, v} }
func Gte(v any) Op     { return Op{OpGte, v} }
func Exists(b bool) Op { return Op{OpExists, b} }

// Filter maps a field path (bson names, dots for nesting, "_id" for the id) to
// a value or an Op. All conditions are ANDed.
type Filter map[string]any

// Query lists entities ordered by id.
type Query struct {
	Filter Filter
	Desc   bool
	After  string // cursor: the last id of the previous page
	Limit  int    // 0 means DefaultLimit
}

const (
	DefaultLimit = 50
	MaxLimit     = 500
)

// Page is one page of results; Next is empty on the last page.
type Page[E any] struct {
	Items []*E   `json:"items"`
	Next  string `json:"next,omitempty"`
}

// Collection stores one entity type. E is the struct type; methods use *E,
// which must implement types.Entity.
type Collection[E any] interface {
	Get(ctx context.Context, id string) (*E, error)
	FindOne(ctx context.Context, f Filter) (*E, error)
	List(ctx context.Context, q Query) (Page[E], error)
	Count(ctx context.Context, f Filter) (int64, error)
	// Insert assigns id (if empty), tenant, version 1 and timestamps.
	// A unique-index violation returns errs.CodeConflict.
	Insert(ctx context.Context, v *E) error
	// Replace writes v only if the stored version equals v's version, then bumps
	// it. A lost race returns errs.CodeVersionConflict.
	Replace(ctx context.Context, v *E) error
	// DeleteIf deletes id only if its stored version equals version: the atomic
	// "claim" used when a record may be consumed by exactly one caller (e.g.
	// publishing a draft). A stale version returns errs.CodeVersionConflict;
	// a missing record returns errs.CodeNotFound.
	DeleteIf(ctx context.Context, id string, version int64) error
	Delete(ctx context.Context, id string) error
}

// Index declares a secondary index; Unique ones are enforced by every backend.
type Index struct {
	Fields []string
	Unique bool
}

// Store bundles every collection.
type Store struct {
	Tenants       Collection[types.Tenant]
	APIKeys       Collection[types.APIKey]
	Sessions      Collection[types.Session]
	Messages      Collection[types.Message]
	Turns         Collection[types.Turn]
	Pendings      Collection[types.Pending]
	ToolCalls     Collection[types.ToolCall]
	Compactions   Collection[types.Compaction]
	Artifacts     Collection[types.Artifact]
	LLMCalls      Collection[types.LLMCall]
	Roles         Collection[types.Role]
	Skills        Collection[types.Skill]
	MemorySpaces  Collection[types.MemorySpace]
	MemoryEntries Collection[types.MemoryEntry]
	MCPServers    Collection[types.MCPServer]
	Retrievals    Collection[types.RetrievalSource]
	RiskOverrides Collection[types.RiskOverride]
	FeishuChats   Collection[types.FeishuChat]
	FeishuThreads Collection[types.FeishuThread]
	Audit         Collection[types.AuditLog]
	WorkflowDefs  Collection[types.WorkflowDef]
	Runs          Collection[types.WorkflowRun]
	StepRuns      Collection[types.StepRun]
	Schedules     Collection[types.Schedule]
	ScheduleRuns  Collection[types.ScheduleRun]
}

// CollectionSpec names a collection and its indexes; backends build from it.
type CollectionSpec struct {
	Name    string
	Indexes []Index
}

// Specs lists every collection with its indexes. Every index implicitly starts
// with tenant_id.
var Specs = map[string]CollectionSpec{
	"tenants":           {Name: "tenants"},
	"api_keys":          {Name: "api_keys", Indexes: []Index{{Fields: []string{"prefix"}, Unique: true}}},
	"sessions":          {Name: "sessions", Indexes: []Index{{Fields: []string{"status"}}, {Fields: []string{"created_by.id"}}}},
	"messages":          {Name: "messages", Indexes: []Index{{Fields: []string{"session_id", "seq"}, Unique: true}}},
	"turns":             {Name: "turns", Indexes: []Index{{Fields: []string{"session_id"}}, {Fields: []string{"idem_key"}}}},
	"pendings":          {Name: "pendings", Indexes: []Index{{Fields: []string{"status"}}, {Fields: []string{"turn_id"}}}},
	"tool_calls":        {Name: "tool_calls", Indexes: []Index{{Fields: []string{"turn_id", "tool_use_id"}, Unique: true}}},
	"compactions":       {Name: "compactions", Indexes: []Index{{Fields: []string{"session_id", "from_seq", "to_seq"}, Unique: true}}},
	"artifacts":         {Name: "artifacts", Indexes: []Index{{Fields: []string{"session_id"}}, {Fields: []string{"run_id"}}}},
	"llm_calls":         {Name: "llm_calls", Indexes: []Index{{Fields: []string{"turn_id"}}}},
	"roles":             {Name: "roles", Indexes: []Index{{Fields: []string{"key", "ver"}, Unique: true}}},
	"skills":            {Name: "skills", Indexes: []Index{{Fields: []string{"key", "ver"}, Unique: true}}},
	"memory_spaces":     {Name: "memory_spaces", Indexes: []Index{{Fields: []string{"name"}, Unique: true}}},
	"memory_entries":    {Name: "memory_entries", Indexes: []Index{{Fields: []string{"space", "key"}, Unique: true}}},
	"mcp_servers":       {Name: "mcp_servers", Indexes: []Index{{Fields: []string{"name"}, Unique: true}}},
	"retrieval_sources": {Name: "retrieval_sources", Indexes: []Index{{Fields: []string{"name"}, Unique: true}}},
	"risk_overrides":    {Name: "risk_overrides", Indexes: []Index{{Fields: []string{"tool", "user_id"}, Unique: true}}},
	"feishu_chats":      {Name: "feishu_chats", Indexes: []Index{{Fields: []string{"chat_id"}, Unique: true}}},
	"feishu_threads":    {Name: "feishu_threads", Indexes: []Index{{Fields: []string{"root_msg_id"}, Unique: true}}},
	"audit_logs":        {Name: "audit_logs", Indexes: []Index{{Fields: []string{"action"}}}},
	"workflow_defs":     {Name: "workflow_defs", Indexes: []Index{{Fields: []string{"key", "ver"}, Unique: true}}},
	"workflow_runs":     {Name: "workflow_runs", Indexes: []Index{{Fields: []string{"status"}}}},
	"step_runs":         {Name: "step_runs", Indexes: []Index{{Fields: []string{"run_id", "step_id", "index"}, Unique: true}, {Fields: []string{"step_key"}}}},
	"schedules":         {Name: "schedules", Indexes: []Index{{Fields: []string{"enabled", "next_run_at"}}}},
	"schedule_runs":     {Name: "schedule_runs", Indexes: []Index{{Fields: []string{"schedule_id", "planned_at"}, Unique: true}}},
}

// Update re-reads, mutates and conditionally writes until it wins or fn
// refuses. fn sees fresh state on every attempt and validates the transition
// itself; returning an error (e.g. CodeAlreadyResolved) aborts with the
// current state. This is the only state-transition write path (§5.2).
func Update[E any](ctx context.Context, c Collection[E], id string, fn func(*E) error) (*E, error) {
	var lastErr error
	for attempt := 0; attempt < 16; attempt++ {
		v, err := c.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if err := fn(v); err != nil {
			return v, err
		}
		err = c.Replace(ctx, v)
		if err == nil {
			return v, nil
		}
		if !errs.Is(err, errs.CodeVersionConflict) {
			return v, err
		}
		lastErr = err
	}
	return nil, errs.Wrap(errs.CodeUnavailable, lastErr, "too much contention on %s", id)
}

// All iterates every page of q and returns all items (bounded by max).
func All[E any](ctx context.Context, c Collection[E], q Query, max int) ([]*E, error) {
	var out []*E
	if q.Limit == 0 {
		q.Limit = MaxLimit
	}
	for {
		p, err := c.List(ctx, q)
		if err != nil {
			return nil, err
		}
		out = append(out, p.Items...)
		if p.Next == "" || (max > 0 && len(out) >= max) {
			if max > 0 && len(out) > max {
				out = out[:max]
			}
			return out, nil
		}
		q.After = p.Next
	}
}
