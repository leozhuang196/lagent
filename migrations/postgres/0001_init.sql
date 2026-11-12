-- Generated from internal/infra/repo/postgres.DDL(); NewStore applies the same
-- statements idempotently at startup. Requires PostgreSQL 15+.

CREATE TABLE IF NOT EXISTS "api_keys" (
	id text COLLATE "C" PRIMARY KEY,
	tenant_id text NOT NULL,
	version bigint NOT NULL,
	doc jsonb NOT NULL
);

CREATE INDEX IF NOT EXISTS "api_keys_tenant_idx" ON "api_keys" (tenant_id, id);

CREATE INDEX IF NOT EXISTS "api_keys_doc_gin" ON "api_keys" USING gin (doc jsonb_path_ops);

CREATE UNIQUE INDEX IF NOT EXISTS "api_keys_prefix_uq" ON "api_keys" (tenant_id, (doc #>> '{prefix}')) NULLS NOT DISTINCT;

CREATE TABLE IF NOT EXISTS "artifacts" (
	id text COLLATE "C" PRIMARY KEY,
	tenant_id text NOT NULL,
	version bigint NOT NULL,
	doc jsonb NOT NULL
);

CREATE INDEX IF NOT EXISTS "artifacts_tenant_idx" ON "artifacts" (tenant_id, id);

CREATE INDEX IF NOT EXISTS "artifacts_doc_gin" ON "artifacts" USING gin (doc jsonb_path_ops);

CREATE INDEX IF NOT EXISTS "artifacts_session_id_idx" ON "artifacts" (tenant_id, (doc #>> '{session_id}'));

CREATE INDEX IF NOT EXISTS "artifacts_run_id_idx" ON "artifacts" (tenant_id, (doc #>> '{run_id}'));

CREATE TABLE IF NOT EXISTS "audit_logs" (
	id text COLLATE "C" PRIMARY KEY,
	tenant_id text NOT NULL,
	version bigint NOT NULL,
	doc jsonb NOT NULL
);

CREATE INDEX IF NOT EXISTS "audit_logs_tenant_idx" ON "audit_logs" (tenant_id, id);

CREATE INDEX IF NOT EXISTS "audit_logs_doc_gin" ON "audit_logs" USING gin (doc jsonb_path_ops);

CREATE INDEX IF NOT EXISTS "audit_logs_action_idx" ON "audit_logs" (tenant_id, (doc #>> '{action}'));

CREATE TABLE IF NOT EXISTS "compactions" (
	id text COLLATE "C" PRIMARY KEY,
	tenant_id text NOT NULL,
	version bigint NOT NULL,
	doc jsonb NOT NULL
);

CREATE INDEX IF NOT EXISTS "compactions_tenant_idx" ON "compactions" (tenant_id, id);

CREATE INDEX IF NOT EXISTS "compactions_doc_gin" ON "compactions" USING gin (doc jsonb_path_ops);

CREATE UNIQUE INDEX IF NOT EXISTS "compactions_session_id_from_seq_to_seq_uq" ON "compactions" (tenant_id, (doc #>> '{session_id}'), (doc #>> '{from_seq}'), (doc #>> '{to_seq}')) NULLS NOT DISTINCT;

CREATE TABLE IF NOT EXISTS "feishu_chats" (
	id text COLLATE "C" PRIMARY KEY,
	tenant_id text NOT NULL,
	version bigint NOT NULL,
	doc jsonb NOT NULL
);

CREATE INDEX IF NOT EXISTS "feishu_chats_tenant_idx" ON "feishu_chats" (tenant_id, id);

CREATE INDEX IF NOT EXISTS "feishu_chats_doc_gin" ON "feishu_chats" USING gin (doc jsonb_path_ops);

CREATE UNIQUE INDEX IF NOT EXISTS "feishu_chats_chat_id_uq" ON "feishu_chats" (tenant_id, (doc #>> '{chat_id}')) NULLS NOT DISTINCT;

CREATE TABLE IF NOT EXISTS "feishu_threads" (
	id text COLLATE "C" PRIMARY KEY,
	tenant_id text NOT NULL,
	version bigint NOT NULL,
	doc jsonb NOT NULL
);

CREATE INDEX IF NOT EXISTS "feishu_threads_tenant_idx" ON "feishu_threads" (tenant_id, id);

CREATE INDEX IF NOT EXISTS "feishu_threads_doc_gin" ON "feishu_threads" USING gin (doc jsonb_path_ops);

CREATE UNIQUE INDEX IF NOT EXISTS "feishu_threads_root_msg_id_uq" ON "feishu_threads" (tenant_id, (doc #>> '{root_msg_id}')) NULLS NOT DISTINCT;

CREATE TABLE IF NOT EXISTS "llm_calls" (
	id text COLLATE "C" PRIMARY KEY,
	tenant_id text NOT NULL,
	version bigint NOT NULL,
	doc jsonb NOT NULL
);

CREATE INDEX IF NOT EXISTS "llm_calls_tenant_idx" ON "llm_calls" (tenant_id, id);

CREATE INDEX IF NOT EXISTS "llm_calls_doc_gin" ON "llm_calls" USING gin (doc jsonb_path_ops);

CREATE INDEX IF NOT EXISTS "llm_calls_turn_id_idx" ON "llm_calls" (tenant_id, (doc #>> '{turn_id}'));

CREATE TABLE IF NOT EXISTS "mcp_servers" (
	id text COLLATE "C" PRIMARY KEY,
	tenant_id text NOT NULL,
	version bigint NOT NULL,
	doc jsonb NOT NULL
);

CREATE INDEX IF NOT EXISTS "mcp_servers_tenant_idx" ON "mcp_servers" (tenant_id, id);

CREATE INDEX IF NOT EXISTS "mcp_servers_doc_gin" ON "mcp_servers" USING gin (doc jsonb_path_ops);

CREATE UNIQUE INDEX IF NOT EXISTS "mcp_servers_name_uq" ON "mcp_servers" (tenant_id, (doc #>> '{name}')) NULLS NOT DISTINCT;

CREATE TABLE IF NOT EXISTS "memory_entries" (
	id text COLLATE "C" PRIMARY KEY,
	tenant_id text NOT NULL,
	version bigint NOT NULL,
	doc jsonb NOT NULL
);

CREATE INDEX IF NOT EXISTS "memory_entries_tenant_idx" ON "memory_entries" (tenant_id, id);

CREATE INDEX IF NOT EXISTS "memory_entries_doc_gin" ON "memory_entries" USING gin (doc jsonb_path_ops);

CREATE UNIQUE INDEX IF NOT EXISTS "memory_entries_space_key_uq" ON "memory_entries" (tenant_id, (doc #>> '{space}'), (doc #>> '{key}')) NULLS NOT DISTINCT;

CREATE TABLE IF NOT EXISTS "memory_spaces" (
	id text COLLATE "C" PRIMARY KEY,
	tenant_id text NOT NULL,
	version bigint NOT NULL,
	doc jsonb NOT NULL
);

CREATE INDEX IF NOT EXISTS "memory_spaces_tenant_idx" ON "memory_spaces" (tenant_id, id);

CREATE INDEX IF NOT EXISTS "memory_spaces_doc_gin" ON "memory_spaces" USING gin (doc jsonb_path_ops);

CREATE UNIQUE INDEX IF NOT EXISTS "memory_spaces_name_uq" ON "memory_spaces" (tenant_id, (doc #>> '{name}')) NULLS NOT DISTINCT;

CREATE TABLE IF NOT EXISTS "messages" (
	id text COLLATE "C" PRIMARY KEY,
	tenant_id text NOT NULL,
	version bigint NOT NULL,
	doc jsonb NOT NULL
);

CREATE INDEX IF NOT EXISTS "messages_tenant_idx" ON "messages" (tenant_id, id);

CREATE INDEX IF NOT EXISTS "messages_doc_gin" ON "messages" USING gin (doc jsonb_path_ops);

CREATE UNIQUE INDEX IF NOT EXISTS "messages_session_id_seq_uq" ON "messages" (tenant_id, (doc #>> '{session_id}'), (doc #>> '{seq}')) NULLS NOT DISTINCT;

CREATE TABLE IF NOT EXISTS "pendings" (
	id text COLLATE "C" PRIMARY KEY,
	tenant_id text NOT NULL,
	version bigint NOT NULL,
	doc jsonb NOT NULL
);

CREATE INDEX IF NOT EXISTS "pendings_tenant_idx" ON "pendings" (tenant_id, id);

CREATE INDEX IF NOT EXISTS "pendings_doc_gin" ON "pendings" USING gin (doc jsonb_path_ops);

CREATE INDEX IF NOT EXISTS "pendings_status_idx" ON "pendings" (tenant_id, (doc #>> '{status}'));

CREATE INDEX IF NOT EXISTS "pendings_turn_id_idx" ON "pendings" (tenant_id, (doc #>> '{turn_id}'));

CREATE TABLE IF NOT EXISTS "retrieval_sources" (
	id text COLLATE "C" PRIMARY KEY,
	tenant_id text NOT NULL,
	version bigint NOT NULL,
	doc jsonb NOT NULL
);

CREATE INDEX IF NOT EXISTS "retrieval_sources_tenant_idx" ON "retrieval_sources" (tenant_id, id);

CREATE INDEX IF NOT EXISTS "retrieval_sources_doc_gin" ON "retrieval_sources" USING gin (doc jsonb_path_ops);

CREATE UNIQUE INDEX IF NOT EXISTS "retrieval_sources_name_uq" ON "retrieval_sources" (tenant_id, (doc #>> '{name}')) NULLS NOT DISTINCT;

CREATE TABLE IF NOT EXISTS "risk_overrides" (
	id text COLLATE "C" PRIMARY KEY,
	tenant_id text NOT NULL,
	version bigint NOT NULL,
	doc jsonb NOT NULL
);

CREATE INDEX IF NOT EXISTS "risk_overrides_tenant_idx" ON "risk_overrides" (tenant_id, id);

CREATE INDEX IF NOT EXISTS "risk_overrides_doc_gin" ON "risk_overrides" USING gin (doc jsonb_path_ops);

CREATE UNIQUE INDEX IF NOT EXISTS "risk_overrides_tool_user_id_uq" ON "risk_overrides" (tenant_id, (doc #>> '{tool}'), (doc #>> '{user_id}')) NULLS NOT DISTINCT;

CREATE TABLE IF NOT EXISTS "roles" (
	id text COLLATE "C" PRIMARY KEY,
	tenant_id text NOT NULL,
	version bigint NOT NULL,
	doc jsonb NOT NULL
);

CREATE INDEX IF NOT EXISTS "roles_tenant_idx" ON "roles" (tenant_id, id);

CREATE INDEX IF NOT EXISTS "roles_doc_gin" ON "roles" USING gin (doc jsonb_path_ops);

CREATE UNIQUE INDEX IF NOT EXISTS "roles_key_ver_uq" ON "roles" (tenant_id, (doc #>> '{key}'), (doc #>> '{ver}')) NULLS NOT DISTINCT;

CREATE TABLE IF NOT EXISTS "schedule_runs" (
	id text COLLATE "C" PRIMARY KEY,
	tenant_id text NOT NULL,
	version bigint NOT NULL,
	doc jsonb NOT NULL
);

CREATE INDEX IF NOT EXISTS "schedule_runs_tenant_idx" ON "schedule_runs" (tenant_id, id);

CREATE INDEX IF NOT EXISTS "schedule_runs_doc_gin" ON "schedule_runs" USING gin (doc jsonb_path_ops);

CREATE UNIQUE INDEX IF NOT EXISTS "schedule_runs_schedule_id_planned_at_uq" ON "schedule_runs" (tenant_id, (doc #>> '{schedule_id}'), (doc #>> '{planned_at}')) NULLS NOT DISTINCT;

CREATE TABLE IF NOT EXISTS "schedules" (
	id text COLLATE "C" PRIMARY KEY,
	tenant_id text NOT NULL,
	version bigint NOT NULL,
	doc jsonb NOT NULL
);

CREATE INDEX IF NOT EXISTS "schedules_tenant_idx" ON "schedules" (tenant_id, id);

CREATE INDEX IF NOT EXISTS "schedules_doc_gin" ON "schedules" USING gin (doc jsonb_path_ops);

CREATE INDEX IF NOT EXISTS "schedules_enabled_next_run_at_idx" ON "schedules" (tenant_id, (doc #>> '{enabled}'), (doc #>> '{next_run_at}'));

CREATE TABLE IF NOT EXISTS "sessions" (
	id text COLLATE "C" PRIMARY KEY,
	tenant_id text NOT NULL,
	version bigint NOT NULL,
	doc jsonb NOT NULL
);

CREATE INDEX IF NOT EXISTS "sessions_tenant_idx" ON "sessions" (tenant_id, id);

CREATE INDEX IF NOT EXISTS "sessions_doc_gin" ON "sessions" USING gin (doc jsonb_path_ops);

CREATE INDEX IF NOT EXISTS "sessions_status_idx" ON "sessions" (tenant_id, (doc #>> '{status}'));

CREATE INDEX IF NOT EXISTS "sessions_created_by_id_idx" ON "sessions" (tenant_id, (doc #>> '{created_by,id}'));

CREATE TABLE IF NOT EXISTS "skills" (
	id text COLLATE "C" PRIMARY KEY,
	tenant_id text NOT NULL,
	version bigint NOT NULL,
	doc jsonb NOT NULL
);

CREATE INDEX IF NOT EXISTS "skills_tenant_idx" ON "skills" (tenant_id, id);

CREATE INDEX IF NOT EXISTS "skills_doc_gin" ON "skills" USING gin (doc jsonb_path_ops);

CREATE UNIQUE INDEX IF NOT EXISTS "skills_key_ver_uq" ON "skills" (tenant_id, (doc #>> '{key}'), (doc #>> '{ver}')) NULLS NOT DISTINCT;

CREATE TABLE IF NOT EXISTS "step_runs" (
	id text COLLATE "C" PRIMARY KEY,
	tenant_id text NOT NULL,
	version bigint NOT NULL,
	doc jsonb NOT NULL
);

CREATE INDEX IF NOT EXISTS "step_runs_tenant_idx" ON "step_runs" (tenant_id, id);

CREATE INDEX IF NOT EXISTS "step_runs_doc_gin" ON "step_runs" USING gin (doc jsonb_path_ops);

CREATE UNIQUE INDEX IF NOT EXISTS "step_runs_run_id_step_id_index_uq" ON "step_runs" (tenant_id, (doc #>> '{run_id}'), (doc #>> '{step_id}'), (doc #>> '{index}')) NULLS NOT DISTINCT;

CREATE INDEX IF NOT EXISTS "step_runs_step_key_idx" ON "step_runs" (tenant_id, (doc #>> '{step_key}'));

CREATE TABLE IF NOT EXISTS "tenants" (
	id text COLLATE "C" PRIMARY KEY,
	tenant_id text NOT NULL,
	version bigint NOT NULL,
	doc jsonb NOT NULL
);

CREATE INDEX IF NOT EXISTS "tenants_tenant_idx" ON "tenants" (tenant_id, id);

CREATE INDEX IF NOT EXISTS "tenants_doc_gin" ON "tenants" USING gin (doc jsonb_path_ops);

CREATE TABLE IF NOT EXISTS "tool_calls" (
	id text COLLATE "C" PRIMARY KEY,
	tenant_id text NOT NULL,
	version bigint NOT NULL,
	doc jsonb NOT NULL
);

CREATE INDEX IF NOT EXISTS "tool_calls_tenant_idx" ON "tool_calls" (tenant_id, id);

CREATE INDEX IF NOT EXISTS "tool_calls_doc_gin" ON "tool_calls" USING gin (doc jsonb_path_ops);

CREATE UNIQUE INDEX IF NOT EXISTS "tool_calls_turn_id_tool_use_id_uq" ON "tool_calls" (tenant_id, (doc #>> '{turn_id}'), (doc #>> '{tool_use_id}')) NULLS NOT DISTINCT;

CREATE TABLE IF NOT EXISTS "turns" (
	id text COLLATE "C" PRIMARY KEY,
	tenant_id text NOT NULL,
	version bigint NOT NULL,
	doc jsonb NOT NULL
);

CREATE INDEX IF NOT EXISTS "turns_tenant_idx" ON "turns" (tenant_id, id);

CREATE INDEX IF NOT EXISTS "turns_doc_gin" ON "turns" USING gin (doc jsonb_path_ops);

CREATE INDEX IF NOT EXISTS "turns_session_id_idx" ON "turns" (tenant_id, (doc #>> '{session_id}'));

CREATE INDEX IF NOT EXISTS "turns_idem_key_idx" ON "turns" (tenant_id, (doc #>> '{idem_key}'));

CREATE TABLE IF NOT EXISTS "workflow_defs" (
	id text COLLATE "C" PRIMARY KEY,
	tenant_id text NOT NULL,
	version bigint NOT NULL,
	doc jsonb NOT NULL
);

CREATE INDEX IF NOT EXISTS "workflow_defs_tenant_idx" ON "workflow_defs" (tenant_id, id);

CREATE INDEX IF NOT EXISTS "workflow_defs_doc_gin" ON "workflow_defs" USING gin (doc jsonb_path_ops);

CREATE UNIQUE INDEX IF NOT EXISTS "workflow_defs_key_ver_uq" ON "workflow_defs" (tenant_id, (doc #>> '{key}'), (doc #>> '{ver}')) NULLS NOT DISTINCT;

CREATE TABLE IF NOT EXISTS "workflow_runs" (
	id text COLLATE "C" PRIMARY KEY,
	tenant_id text NOT NULL,
	version bigint NOT NULL,
	doc jsonb NOT NULL
);

CREATE INDEX IF NOT EXISTS "workflow_runs_tenant_idx" ON "workflow_runs" (tenant_id, id);

CREATE INDEX IF NOT EXISTS "workflow_runs_doc_gin" ON "workflow_runs" USING gin (doc jsonb_path_ops);

CREATE INDEX IF NOT EXISTS "workflow_runs_status_idx" ON "workflow_runs" (tenant_id, (doc #>> '{status}'));
