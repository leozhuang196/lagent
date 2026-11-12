// JSON shapes of the lagent HTTP API. Mirrors internal/core/types and
// internal/orch/session; keep field names identical to the Go json tags.

/** Arbitrary JSON value (json.RawMessage / map[string]any on the Go side). */
export type JsonValue = string | number | boolean | null | JsonValue[] | { [k: string]: JsonValue };
export type JsonObject = { [k: string]: JsonValue };

export type Role = 'member' | 'tenant_admin' | 'platform_admin';
export type PrincipalKind = 'api_key' | 'feishu_user' | 'system';

export interface Principal {
  kind: PrincipalKind;
  id: string;
  tenant_id: string;
  role: Role;
  name?: string;
  max_permission_mode?: string;
}

export interface Meta {
  id: string;
  tenant_id: string;
  version: number;
  created_at: string;
  updated_at: string;
}

export interface Page<T> {
  items: T[] | null;
  next?: string;
}

export interface Usage {
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// --- sessions ---

export type SessionStatus = 'idle' | 'queued' | 'running' | 'waiting';
export type PermissionMode = 'ask_all' | 'ask_external' | 'auto';
export type ChannelKind = 'stream' | 'feishu' | 'inbox' | 'webhook';
export type Origin = 'web' | 'feishu' | 'api' | 'schedule' | 'workflow' | 'subtask' | 'debug';

export interface Channel {
  kind: ChannelKind;
  target?: string;
}

export interface InboxItem {
  text: string;
  author?: string;
  at: string;
}

export interface StandingGrant {
  tool: string;
  field: string;
  value: string;
}

export interface Session extends Meta {
  title?: string;
  role_key: string;
  role_ver: number;
  permission_mode: PermissionMode;
  memory_spaces?: string[];
  channels: Channel[];
  standing_grants?: StandingGrant[];
  status: SessionStatus;
  active_turn_id?: string;
  cancel_requested?: boolean;
  inbox?: InboxItem[];
  last_seq: number;
  origin: Origin;
  created_by: Principal;
  debug?: boolean;
  parent_turn_id?: string;
  run_id?: string;
  step_run_id?: string;
  tool_deny?: string[];
  labels?: Record<string, string>;
}

export interface CreateSessionInput {
  title?: string;
  role?: string;
  role_version?: number;
  permission_mode?: PermissionMode;
  memory_spaces?: string[];
  labels?: Record<string, string>;
}

// --- messages ---

export type MsgRole = 'user' | 'assistant';
export type BlockType = 'text' | 'tool_use' | 'tool_result' | 'thinking';

export interface Block {
  type: BlockType;
  text?: string;
  tool_use_id?: string;
  name?: string;
  input?: JsonValue;
  is_error?: boolean;
  raw?: JsonValue;
  provider?: string;
}

export interface Message extends Meta {
  session_id: string;
  turn_id: string;
  seq: number;
  role: MsgRole;
  content: Block[];
  epoch: number;
  author?: string;
}

// --- turns ---

export type TurnStatus = 'queued' | 'running' | 'suspended' | 'completed' | 'failed' | 'cancelled';
export type TurnTrigger = 'message' | 'schedule' | 'step' | 'subtask';

export interface TurnError {
  code: string;
  message: string;
}

export interface StepResult {
  summary: string;
  data?: JsonValue;
  artifacts?: string[];
}

export interface Turn extends Meta {
  session_id: string;
  trigger: TurnTrigger;
  status: TurnStatus;
  role_key: string;
  role_ver: number;
  model?: string;
  epoch: number;
  iterations: number;
  usage: Usage;
  token_budget?: number;
  max_iterations?: number;
  error?: TurnError;
  waiting_on?: string[];
  parent_turn_id?: string;
  step_run_id?: string;
  result?: StepResult;
  final_text?: string;
  debug?: boolean;
  started_at?: string;
  ended_at?: string;
}

/** GET /v1/turns/{id}: waiting_human / waiting_subtasks are distinct from running. */
export interface TurnView extends Turn {
  state: TurnStatus | 'waiting_human' | 'waiting_subtasks';
}

export interface SubmitResult {
  turn: Turn;
  queued: boolean;
}

// --- pendings ---

export type RiskLevel = 'read_only' | 'local_write' | 'command' | 'external';
export type PendingKind = 'approval' | 'question' | 'subtasks' | 'external_job' | 'budget' | 'outcome_unknown';
export type PendingStatus = 'open' | 'resolved';
export type Decision =
  | 'approve'
  | 'reject'
  | 'answer'
  | 'continue'
  | 'stop'
  | 'done'
  | 'happened'
  | 'not_happened';

export interface Resolution {
  decision: Decision;
  answer?: string;
  by: Principal;
  via: string;
  at: string;
}

export interface Pending extends Meta {
  session_id: string;
  turn_id: string;
  kind: PendingKind;
  status: PendingStatus;
  title: string;
  detail?: string;
  tool_call_id?: string;
  tool_name?: string;
  risk?: RiskLevel;
  args?: JsonValue;
  trigger_source?: string;
  question?: string;
  options?: string[];
  child_turn_ids?: string[];
  external_ref?: string;
  assignee?: string;
  resolution?: Resolution;
  remind_at?: string;
}

export interface ResolveInput {
  decision: Decision;
  answer?: string;
  via?: string;
}

export interface ResolveResult {
  pending: Pending;
  already_resolved: boolean;
}

// --- artifacts ---

export interface Artifact extends Meta {
  session_id?: string;
  turn_id?: string;
  run_id?: string;
  step_run_id?: string;
  name: string;
  object_key: string;
  size: number;
  sha256: string;
  content_type?: string;
}

export interface ArtifactURL {
  url: string;
  expires_at: string;
  name: string;
}

// --- snapshot ---

export interface Snapshot {
  session: Session;
  messages: Message[] | null;
  pendings: Pending[] | null;
  active_turn?: Turn | null;
  last_seq: number;
}

export interface CancelResult {
  session: Session;
  note: string;
}

// --- events (internal/core/types/event.go) ---

interface EventBase {
  seq: number;
  stream: string;
  tenant_id: string;
  turn_id?: string;
  ts: string;
}

type Ev<T extends string, D> = EventBase & { type: T; data: D };

export type LagentEvent =
  | Ev<'turn.started', { iterations: number }>
  | Ev<'message.delta', { text: string }>
  | Ev<'message.completed', { message: Message }>
  | Ev<'tool.requested', { tool_call_id: string; tool: string; risk: RiskLevel; status: string; input?: JsonValue }>
  | Ev<'tool.started', { tool_call_id: string; tool: string }>
  | Ev<
      'tool.completed',
      { tool_call_id: string; tool: string; is_error: boolean; duration_ms: number; artifacts?: JsonValue }
    >
  | Ev<'pending.created', { pending: Pending }>
  | Ev<'pending.resolved', { pending_id: string; decision: Decision; via?: string; by?: string }>
  | Ev<'artifact.created', { artifact: Artifact }>
  | Ev<'turn.suspended', { waiting_on: string[] }>
  | Ev<
      'turn.completed' | 'turn.failed' | 'turn.cancelled',
      { status: TurnStatus; error?: TurnError | null; usage?: Usage; result?: StepResult | null; final_text?: string }
    >
  | Ev<'stream.reset', JsonObject | undefined>
  | Ev<'run.created' | 'run.completed' | 'step.started' | 'step.completed' | 'step.reused', JsonObject | undefined>;

export type EventType = LagentEvent['type'];

// --- administration: versioned assets (roles, skills, workflow definitions) ---

export type AssetStatus = 'draft' | 'published' | 'disabled';

/** Embedded version bookkeeping of a role, skill or workflow definition. */
export interface Versioned {
  key: string;
  ver: number;
  status: AssetStatus;
  published_at?: string;
  published_by?: Principal;
  note?: string;
}

/** A role as stored (draft Ver 0 or published Ver 1..n). */
export interface RoleAsset extends Meta, Versioned {
  name: string;
  description?: string;
  prompt: string;
  tools: string[];
  skills?: string[];
  memory_spaces?: string[];
  templates?: string[];
  default_permission_mode: PermissionMode;
  model?: string;
  fallback_model?: string;
  max_iterations?: number;
  token_budget?: number;
  effort?: string;
}

export interface SkillAsset extends Meta, Versioned {
  name: string;
  description: string;
  body: string;
  attachments?: string[];
  priority?: number;
}

export type ParamType = 'string' | 'number' | 'list' | 'period';
export type FailurePolicy = 'abort' | 'skip_dependents' | 'continue';

export interface ParamDef {
  name: string;
  type: ParamType;
  required?: boolean;
  default?: JsonValue;
  label?: string;
}

export interface StepDef {
  id: string;
  name?: string;
  role?: string;
  instruction: string;
  depends_on?: string[];
  fan_out?: string;
  retries?: number;
  timeout_sec?: number;
  on_failure?: FailurePolicy;
  side_effect?: boolean;
  sources?: string[];
}

export interface WorkflowDef extends Meta, Versioned {
  name: string;
  description?: string;
  role?: string;
  params?: ParamDef[];
  steps: StepDef[];
  timeout_sec?: number;
  max_parallel?: number;
  token_budget?: number;
}

export type AssetContent = RoleAsset | SkillAsset | WorkflowDef;

export interface AssetSummary<E = AssetContent> {
  key: string;
  /** Effective layer of the latest published version (a draft-only key reports tenant). */
  layer: 'tenant' | 'builtin';
  builtin: boolean;
  overridden: boolean;
  latest?: E;
  latest_ver: number;
  has_draft: boolean;
  versions: number;
}

export interface AssetVersion<E = AssetContent> {
  layer: 'tenant' | 'builtin';
  asset: E;
}

export interface AssetDetail<E = AssetContent> {
  key: string;
  builtin: boolean;
  effective?: AssetVersion<E>;
  draft?: E;
  versions: AssetVersion<E>[];
  effective_ver: number;
}

export interface Change {
  path: string;
  before?: JsonValue;
  after?: JsonValue;
}

export interface DiffResult {
  a: number;
  b: number;
  changes: Change[] | null;
}

// --- memory ---

export type MemoryScope = 'tenant' | 'user';

export interface MemorySpace extends Meta {
  name: string;
  scope: MemoryScope;
  owner_id?: string;
  read_only: boolean;
  priority: number;
}

export interface SpaceView extends MemorySpace {
  entries: number;
}

export interface SpaceInput {
  name?: string;
  scope?: MemoryScope;
  owner_id?: string;
  read_only?: boolean;
  priority?: number;
}

export interface MemoryEntry extends Meta {
  space: string;
  key: string;
  content: string;
  priority: number;
  source_turn_id?: string;
}

export interface EntryInput {
  key: string;
  content: string;
  priority?: number;
}

// --- tenant, keys, usage, audit ---

export interface Tenant extends Meta {
  name: string;
  status: string; // active | disabled
  daily_token_budget?: number;
  max_concurrent_turns?: number;
  default_role?: string;
  artifact_retention_days?: number;
  llm_call_retention_days?: number;
}

export interface TenantSettings {
  name?: string;
  daily_token_budget?: number;
  max_concurrent_turns?: number;
  default_role?: string;
  artifact_retention_days?: number;
  llm_call_retention_days?: number;
}

export interface APIKey extends Meta {
  name: string;
  prefix: string;
  /** Only present because the list handler returns the stored record; never render it. */
  hash?: string;
  role: Role;
  max_permission_mode: PermissionMode;
  disabled: boolean;
  last_used_at?: string;
}

/** The plaintext key is shown exactly once, at creation. */
export interface CreatedKey {
  key: APIKey;
  plaintext: string;
}

export interface UsageRow {
  day: string;
  role: string;
  model: string;
  turns: number;
  failures: number;
  usage: Usage;
  tokens: number;
}

export interface UsageReport {
  days: number;
  from: string;
  rows: UsageRow[] | null;
  total_tokens: number;
  total_turns: number;
  debug_tokens: number;
  debug_turns: number;
  today: { day: string; live_tokens: number; daily_budget: number };
  truncated?: boolean;
}

export interface AuditLog extends Meta {
  actor: Principal;
  action: string;
  target: string;
  turn_id?: string;
  detail?: JsonObject;
  reason?: string;
}

// --- debugger ---

export interface LLMCall extends Meta {
  session_id: string;
  turn_id: string;
  iteration: number;
  model: string;
  request?: JsonValue;
  response?: JsonValue;
  usage: Usage;
  latency_ms: number;
  error?: string;
}

export type ToolCallStatus = 'decided' | 'awaiting_approval' | 'started' | 'succeeded' | 'failed' | 'denied' | 'outcome_unknown';

export interface ToolCall extends Meta {
  session_id: string;
  turn_id: string;
  tool_use_id: string;
  tool: string;
  input?: JsonValue;
  risk: RiskLevel;
  side_effect: boolean;
  status: ToolCallStatus;
  decision?: string;
  idempotency_key?: string;
  pending_id?: string;
  output?: string;
  is_error?: boolean;
  duration_ms?: number;
  started_at?: string;
  ended_at?: string;
}

export interface StoredEvent {
  seq: number;
  stream: string;
  tenant_id: string;
  turn_id?: string;
  type: EventType;
  ts: string;
  data?: JsonObject;
}

export interface TimelineEntry {
  at: string;
  kind: 'llm_call' | 'tool_call' | 'pending' | 'event' | string;
  id?: string;
  title: string;
  status?: string;
  decision?: string;
  duration_ms?: number;
}

export interface TimelineTotals {
  iterations: number;
  usage: Usage;
  tokens: number;
  llm_calls: number;
  llm_latency_ms: number;
  tool_calls: number;
  tool_status: Record<string, number>;
  pendings: number;
  duration_ms?: number;
}

export interface Timeline {
  turn: Turn;
  entries: TimelineEntry[] | null;
  llm_calls: LLMCall[] | null;
  tool_calls: ToolCall[] | null;
  pendings: Pending[] | null;
  events: StoredEvent[] | null;
  totals: TimelineTotals;
}

export interface PreviewInput {
  role: string;
  version?: number | null;
  user_id?: string;
  memory_spaces?: string[];
}

export interface AssemblyPart {
  name: string;
  text: string;
  cache?: boolean;
}

export interface PreviewSpace {
  name: string;
  source: 'role' | 'session' | 'user' | string;
  found: boolean;
  entries: number;
}

export interface Preview {
  role_key: string;
  role_ver: number;
  role_status: string;
  model: string;
  parts: AssemblyPart[] | null;
  tools: string[] | null;
  missing_tools?: string[];
  skills: string[] | null;
  memory_spaces: PreviewSpace[] | null;
  truncated?: string[];
  chars_total: number;
  tool_chars: number;
  tokens_estimate: number;
}

export interface PlaygroundInput {
  role: string;
  version?: number | null;
  memory_spaces?: string[];
  title?: string;
}

export interface ForkInput {
  upto_seq: number;
  role_version?: number | null;
}

// --- workflow runs and schedules ---

export type RunStatus = 'pending' | 'running' | 'succeeded' | 'partial' | 'failed' | 'cancelled';
export type RunMode = 'start' | 'continue' | 'rerun';
export type StepStatus = 'pending' | 'dispatched' | 'running' | 'succeeded' | 'failed' | 'skipped' | 'cancelled' | 'reused';
export type OverlapPolicy = 'skip' | 'queue' | 'parallel';
export type ScheduleOutcome = 'ran' | 'skipped' | 'missed';

export interface Frozen {
  now: string;
  tz: string;
  seed: number;
  params: Record<string, JsonValue>;
}

export interface WorkflowRun extends Meta {
  def_key: string;
  def_ver: number;
  def_tenant: string;
  role_key: string;
  role_ver: number;
  frozen: Frozen;
  status: RunStatus;
  mode: RunMode;
  origin_run_id?: string;
  schedule_id?: string;
  started_by: Principal;
  permission_mode: PermissionMode;
  standing_grants?: StandingGrant[];
  error?: TurnError;
  debug?: boolean;
  usage: Usage;
  deadline?: string;
  ended_at?: string;
  params?: Record<string, JsonValue>;
}

export interface StepRun extends Meta {
  run_id: string;
  step_id: string;
  index: number;
  item?: JsonValue;
  depends_on?: string[];
  step_key: string;
  input: string;
  role_key: string;
  role_ver: number;
  status: StepStatus;
  attempts: number;
  max_retries: number;
  on_failure?: FailurePolicy;
  side_effect?: boolean;
  timeout_sec?: number;
  session_id?: string;
  turn_id?: string;
  result?: StepResult;
  reused_from?: string;
  error?: TurnError;
  not_before?: string;
  started_at?: string;
  ended_at?: string;
  dispatched_at?: string;
}

export interface RunView {
  run: WorkflowRun;
  steps: StepRun[] | null;
}

export interface StartInput {
  def_ver?: number;
  draft?: boolean;
  params?: Record<string, JsonValue>;
  permission_mode?: PermissionMode;
  debug?: boolean;
  tz?: string;
}

export interface DryInstance {
  step_id: string;
  index: number;
  item?: JsonValue;
  depends_on?: string[];
  input: string;
  role_key: string;
  role_ver: number;
  side_effect?: boolean;
  step_key: string;
  would_reuse?: boolean;
  reuse_from?: string;
}

export interface DryRunResult {
  def_key: string;
  def_ver: number;
  frozen: Frozen;
  instances: DryInstance[] | null;
}

export interface DryRunInput {
  def_key?: string;
  def_ver?: number;
  draft?: boolean;
  params?: Record<string, JsonValue>;
  tz?: string;
  origin_run_id?: string;
}

export interface ScheduleTarget {
  prompt?: string;
  role_key?: string;
  workflow_key?: string;
  params?: Record<string, JsonValue>;
}

export interface Schedule extends Meta {
  name: string;
  cron: string;
  tz: string;
  overlap: OverlapPolicy;
  target: ScheduleTarget;
  permission_mode: PermissionMode;
  standing_grants?: StandingGrant[];
  enabled: boolean;
  next_run_at?: string;
  last_run_at?: string;
  active_ref?: string;
  last_def_ver?: number;
  def_updated?: boolean;
  owner: Principal;
}

export interface ScheduleInput {
  name: string;
  cron: string;
  tz?: string;
  overlap?: OverlapPolicy;
  target: ScheduleTarget;
  permission_mode: PermissionMode;
  standing_grants?: StandingGrant[];
  enabled?: boolean;
}

export interface ScheduleRun extends Meta {
  schedule_id: string;
  planned_at: string;
  outcome: ScheduleOutcome;
  reason?: string;
  session_id?: string;
  run_id?: string;
}

// --- integrations ---

export type MCPTransport = 'stdio' | 'http' | 'sse';
export type ToolSource = 'builtin' | 'connector' | 'mcp' | 'platform';

export interface ServerTool {
  name: string;
  registry_name: string;
  description: string;
  input_schema?: JsonValue;
  risk: RiskLevel;
}

/** An MCP server as the API returns it: never includes the secret. */
export interface ServerView {
  id: string;
  version: number;
  name: string;
  transport: MCPTransport;
  url?: string;
  command?: string[];
  env?: Record<string, string>;
  auth_header?: string;
  has_secret: boolean;
  enabled: boolean;
  status: string;
  last_error?: string;
  tools: ServerTool[] | null;
  risk_overrides?: Record<string, RiskLevel>;
  synced_at?: string;
  created_at: string;
  updated_at: string;
}

export interface ServerInput {
  name: string;
  transport: MCPTransport;
  url?: string;
  command?: string[];
  env?: Record<string, string>;
  auth_header?: string;
  enabled?: boolean;
  risk_overrides?: Record<string, RiskLevel>;
  /** Write-only; omit to keep the stored credential. */
  secret?: string;
}

export interface RetrievalSource extends Meta {
  name: string;
  description?: string;
  server: string;
  tool: string;
  fixed_args?: JsonValue;
  query_field: string;
}

export interface SourceInput {
  name: string;
  description?: string;
  server: string;
  tool: string;
  fixed_args?: JsonValue;
  query_field?: string;
}

export interface CatalogEntry {
  name: string;
  category?: string;
  source: ToolSource;
  risk: RiskLevel;
  side_effect: boolean;
  read_only: boolean;
  needs_review?: boolean;
  idempotent?: boolean;
  server?: string;
  description: string;
  input_schema?: JsonValue;
  effective_risk: RiskLevel;
  overrides: RiskOverride[] | null;
}

export interface RiskOverride extends Meta {
  tool: string;
  level: RiskLevel;
  user_id?: string;
}

export interface RiskOverrideInput {
  tool: string;
  level: RiskLevel;
  user_id?: string;
}

export interface ServerTestResult {
  ok: boolean;
  error?: string;
  tools?: string[];
  duration_ms: number;
}

export interface TryResult {
  content: string;
  is_error: boolean;
  duration_ms: number;
}

export interface ToolTestResult {
  tool: string;
  risk: RiskLevel;
  content: string;
  is_error: boolean;
  artifacts?: string[];
  control?: string;
  duration_ms: number;
}

export interface TemplateStatus {
  key: string;
  ver: number;
  name: string;
  sources: string[] | null;
  missing: string[] | null;
  needs_config: boolean;
  built_in: boolean;
}

export interface FeishuChat extends Meta {
  chat_id: string;
  name?: string;
  default_role?: string;
  bound: boolean;
}

export interface FeishuThread extends Meta {
  chat_id: string;
  root_msg_id: string;
  session_id: string;
}

// --- platform (platform admins only) ---

export interface PlatformInfo {
  store_driver: string;
  queue_driver?: string;
  redis: boolean;
  process_roles: string[] | null;
  default_model?: string;
}

export interface TenantStatus {
  tenant_id: string;
  name: string;
  status: string;
  queued: number;
  running: number;
  suspended: number;
  open_pendings: number;
  today_tokens: number;
  daily_budget: number;
}

export interface FailedTurn {
  id: string;
  tenant_id: string;
  session_id: string;
  role_key: string;
  role_ver: number;
  model?: string;
  error?: TurnError;
  ended_at?: string;
  debug?: boolean;
}

export interface PlatformStatus {
  info: PlatformInfo;
  now: string;
  tenants: TenantStatus[] | null;
  recent_failures: FailedTurn[] | null;
  notes?: string[];
}

export interface BreakGlassSession {
  session: Session;
  messages: Message[] | null;
  turns: Turn[] | null;
  pendings: Pending[] | null;
}

export interface CreateTenantInput {
  id: string;
  name: string;
  daily_token_budget?: number;
  max_concurrent_turns?: number;
  default_role?: string;
}
