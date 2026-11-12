package postgres

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/eternityx/lagent/internal/core/clock"
	"github.com/eternityx/lagent/internal/core/id"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/repo"
)

// TableDDL returns the idempotent statements creating the table of spec:
//   - id is COLLATE "C" so ORDER BY id matches Go string order (ULID cursors);
//   - (tenant_id, id) serves tenant-scoped listing;
//   - a GIN jsonb_path_ops index serves the containment equality filters;
//   - every declared index is prefixed by tenant_id (unique keys are per
//     tenant) and indexes the text of each field.
func TableDDL(spec repo.CollectionSpec) []string {
	t := pgx.Identifier{spec.Name}.Sanitize()
	out := []string{
		fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (\n"+
			"\tid text COLLATE \"C\" PRIMARY KEY,\n"+
			"\ttenant_id text NOT NULL,\n"+
			"\tversion bigint NOT NULL,\n"+
			"\tdoc jsonb NOT NULL\n)", t),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (tenant_id, id)", pgx.Identifier{spec.Name + "_tenant_idx"}.Sanitize(), t),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s USING gin (doc jsonb_path_ops)", pgx.Identifier{spec.Name + "_doc_gin"}.Sanitize(), t),
	}
	for _, ix := range spec.Indexes {
		cols := []string{"tenant_id"}
		for _, f := range ix.Fields {
			parts, err := splitPath(f)
			if err != nil {
				panic(fmt.Sprintf("postgres: index %s.%v: %v", spec.Name, ix.Fields, err))
			}
			cols = append(cols, "("+textPath(parts)+")")
		}
		name := spec.Name + "_" + strings.ReplaceAll(strings.Join(ix.Fields, "_"), ".", "_")
		if ix.Unique {
			out = append(out, fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s (%s) NULLS NOT DISTINCT",
				pgx.Identifier{name + "_uq"}.Sanitize(), t, strings.Join(cols, ", ")))
		} else {
			out = append(out, fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (%s)",
				pgx.Identifier{name + "_idx"}.Sanitize(), t, strings.Join(cols, ", ")))
		}
	}
	return out
}

// DDL returns the statements for every collection in repo.Specs, ordered by
// collection name.
func DDL() []string {
	names := make([]string, 0, len(repo.Specs))
	for n := range repo.Specs {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []string
	for _, n := range names {
		out = append(out, TableDDL(repo.Specs[n])...)
	}
	return out
}

// MigrationSQL renders DDL as the reference migration kept in
// migrations/postgres/0001_init.sql (a test keeps the two in sync).
func MigrationSQL() string {
	var b strings.Builder
	b.WriteString("-- Generated from internal/infra/repo/postgres.DDL(); NewStore applies the same\n")
	b.WriteString("-- statements idempotently at startup. Requires PostgreSQL 15+.\n")
	for _, s := range DDL() {
		b.WriteString("\n")
		b.WriteString(s)
		b.WriteString(";\n")
	}
	return b.String()
}

// schemaLock serializes concurrent NewStore calls (several processes
// starting at once) so CREATE ... IF NOT EXISTS cannot race.
const schemaLock = 0x6c6167656e74 // "lagent"

// Migrate creates every table and index; it is idempotent.
func Migrate(ctx context.Context, db DB) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: migrate: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(schemaLock)); err != nil {
		return fmt.Errorf("postgres: migrate lock: %w", err)
	}
	for _, s := range DDL() {
		if _, err := tx.Exec(ctx, s); err != nil {
			return fmt.Errorf("postgres: migrate %q: %w", s, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: migrate commit: %w", err)
	}
	return nil
}

// NewStore migrates the schema and builds a Store with the same id prefixes
// as memory.NewStore.
func NewStore(ctx context.Context, db DB, clk clock.Clock) (*repo.Store, error) {
	if err := Migrate(ctx, db); err != nil {
		return nil, err
	}
	s := repo.Specs
	return &repo.Store{
		Tenants:       NewCollection[types.Tenant](db, s["tenants"], id.Tenant, clk),
		APIKeys:       NewCollection[types.APIKey](db, s["api_keys"], id.APIKey, clk),
		Sessions:      NewCollection[types.Session](db, s["sessions"], id.Session, clk),
		Messages:      NewCollection[types.Message](db, s["messages"], id.Message, clk),
		Turns:         NewCollection[types.Turn](db, s["turns"], id.Turn, clk),
		Pendings:      NewCollection[types.Pending](db, s["pendings"], id.Pending, clk),
		ToolCalls:     NewCollection[types.ToolCall](db, s["tool_calls"], id.ToolCall, clk),
		Compactions:   NewCollection[types.Compaction](db, s["compactions"], id.Compaction, clk),
		Artifacts:     NewCollection[types.Artifact](db, s["artifacts"], id.Artifact, clk),
		LLMCalls:      NewCollection[types.LLMCall](db, s["llm_calls"], id.LLMCall, clk),
		Roles:         NewCollection[types.Role](db, s["roles"], id.Role, clk),
		Skills:        NewCollection[types.Skill](db, s["skills"], id.Skill, clk),
		MemorySpaces:  NewCollection[types.MemorySpace](db, s["memory_spaces"], id.Memory, clk),
		MemoryEntries: NewCollection[types.MemoryEntry](db, s["memory_entries"], id.Memory, clk),
		MCPServers:    NewCollection[types.MCPServer](db, s["mcp_servers"], id.MCPServer, clk),
		Retrievals:    NewCollection[types.RetrievalSource](db, s["retrieval_sources"], id.Retrieval, clk),
		RiskOverrides: NewCollection[types.RiskOverride](db, s["risk_overrides"], id.RiskOverride, clk),
		FeishuChats:   NewCollection[types.FeishuChat](db, s["feishu_chats"], id.FeishuChat, clk),
		FeishuThreads: NewCollection[types.FeishuThread](db, s["feishu_threads"], id.FeishuThread, clk),
		Audit:         NewCollection[types.AuditLog](db, s["audit_logs"], id.Audit, clk),
		WorkflowDefs:  NewCollection[types.WorkflowDef](db, s["workflow_defs"], id.Workflow, clk),
		Runs:          NewCollection[types.WorkflowRun](db, s["workflow_runs"], id.Run, clk),
		StepRuns:      NewCollection[types.StepRun](db, s["step_runs"], id.StepRun, clk),
		Schedules:     NewCollection[types.Schedule](db, s["schedules"], id.Schedule, clk),
		ScheduleRuns:  NewCollection[types.ScheduleRun](db, s["schedule_runs"], id.ScheduleRun, clk),
	}, nil
}
