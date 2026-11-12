// Typed wrappers of the REST routes in internal/transport/httpapi. Chat
// routes live in server.go, admin routes in adminapi, workflows in
// workflowapi, integrations in integration.go.

import { request } from './client';
import type {
  APIKey,
  Artifact,
  ArtifactURL,
  AssetDetail,
  AssetSummary,
  AuditLog,
  BreakGlassSession,
  CancelResult,
  CatalogEntry,
  CreateSessionInput,
  CreateTenantInput,
  CreatedKey,
  DiffResult,
  DryRunInput,
  DryRunResult,
  EntryInput,
  FeishuChat,
  FeishuThread,
  ForkInput,
  LLMCall,
  MemoryEntry,
  MemorySpace,
  Page,
  Pending,
  PendingStatus,
  PlaygroundInput,
  PlatformStatus,
  Preview,
  PreviewInput,
  Principal,
  ResolveInput,
  ResolveResult,
  RetrievalSource,
  RiskOverride,
  RiskOverrideInput,
  Role,
  RoleAsset,
  RunView,
  Schedule,
  ScheduleInput,
  ScheduleRun,
  ServerInput,
  ServerTestResult,
  ServerView,
  Session,
  SkillAsset,
  Snapshot,
  SourceInput,
  SpaceInput,
  SpaceView,
  StartInput,
  SubmitResult,
  TemplateStatus,
  Tenant,
  TenantSettings,
  Timeline,
  ToolTestResult,
  TryResult,
  TurnView,
  UsageReport,
  WorkflowDef,
  WorkflowRun,
} from './types';

export const authApi = {
  login: (key: string) => request<Principal>('/v1/auth/login', { method: 'POST', body: { key }, quiet401: true }),
  logout: () => request<void>('/v1/auth/logout', { method: 'POST' }),
  me: () => request<Principal>('/v1/me', { quiet401: true }),
};

export interface PageQuery {
  after?: string;
  limit?: number;
}

export const sessionsApi = {
  list: (q: PageQuery & { all?: boolean } = {}) =>
    request<Page<Session>>('/v1/sessions', { query: { after: q.after, limit: q.limit, all: q.all } }),
  create: (input: CreateSessionInput) => request<Session>('/v1/sessions', { method: 'POST', body: input }),
  snapshot: (id: string) => request<Snapshot>(`/v1/sessions/${encodeURIComponent(id)}`),
  submit: (id: string, text: string, idempotencyKey?: string) =>
    request<SubmitResult>(`/v1/sessions/${encodeURIComponent(id)}/messages`, {
      method: 'POST',
      body: { text },
      headers: idempotencyKey ? { 'Idempotency-Key': idempotencyKey } : undefined,
    }),
  cancel: (id: string) => request<CancelResult>(`/v1/sessions/${encodeURIComponent(id)}/cancel`, { method: 'POST' }),
};

export const turnsApi = {
  get: (id: string) => request<TurnView>(`/v1/turns/${encodeURIComponent(id)}`),
};

export const pendingsApi = {
  list: (q: PageQuery & { status?: PendingStatus; mine?: boolean } = {}) =>
    request<Page<Pending>>('/v1/pendings', {
      query: { status: q.status, mine: q.mine, after: q.after, limit: q.limit },
    }),
  get: (id: string) => request<Pending>(`/v1/pendings/${encodeURIComponent(id)}`),
  resolve: (id: string, input: ResolveInput) =>
    request<ResolveResult>(`/v1/pendings/${encodeURIComponent(id)}/resolve`, {
      method: 'POST',
      body: { via: 'web', ...input },
    }),
};

export const artifactsApi = {
  get: (id: string) => request<Artifact>(`/v1/artifacts/${encodeURIComponent(id)}`),
  url: (id: string) => request<ArtifactURL>(`/v1/artifacts/${encodeURIComponent(id)}/url`),
};

// --- versioned assets: roles, skills, workflow definitions ---

/** The admin API answers lists as {items:[...]}. */
interface Items<T> {
  items: T[] | null;
}

const enc = encodeURIComponent;

/** A version number, or the literal "draft" for Ver 0. */
export type VerRef = number | 'draft';

function assetApi<E>(base: string) {
  return {
    list: async () => (await request<Items<AssetSummary<E>>>(base)).items ?? [],
    get: (key: string) => request<AssetDetail<E>>(`${base}/${enc(key)}`),
    version: (key: string, ver: VerRef) =>
      request<{ layer: string; asset: E }>(`${base}/${enc(key)}/versions/${enc(String(ver))}`),
    diff: (key: string, a: VerRef, b: VerRef) =>
      request<DiffResult>(`${base}/${enc(key)}/diff`, { query: { a: String(a), b: String(b) } }),
    saveDraft: (key: string, content: Partial<E>) =>
      request<E>(`${base}/${enc(key)}/draft`, { method: 'PUT', body: content }),
    deleteDraft: (key: string) => request<void>(`${base}/${enc(key)}/draft`, { method: 'DELETE' }),
    publish: (key: string, note?: string) =>
      request<E>(`${base}/${enc(key)}/publish`, { method: 'POST', body: { note: note ?? '' } }),
    rollback: (key: string, ver: number, note?: string) =>
      request<E>(`${base}/${enc(key)}/rollback`, { method: 'POST', body: { ver, note: note ?? '' } }),
    setDisabled: (key: string, ver: number, disabled: boolean) =>
      request<E>(`${base}/${enc(key)}/versions/${ver}/${disabled ? 'disable' : 'enable'}`, { method: 'POST' }),
  };
}

export const rolesApi = assetApi<RoleAsset>('/v1/roles');
export const skillsApi = assetApi<SkillAsset>('/v1/skills');
export const workflowDefsApi = assetApi<WorkflowDef>('/v1/workflow-defs');

// --- memory ---

export const memoryApi = {
  listSpaces: async () => (await request<Items<SpaceView>>('/v1/memory/spaces')).items ?? [],
  getSpace: (name: string) => request<SpaceView>(`/v1/memory/spaces/${enc(name)}`),
  createSpace: (input: SpaceInput) => request<MemorySpace>('/v1/memory/spaces', { method: 'POST', body: input }),
  updateSpace: (name: string, input: SpaceInput) =>
    request<MemorySpace>(`/v1/memory/spaces/${enc(name)}`, { method: 'PUT', body: input }),
  deleteSpace: (name: string) => request<void>(`/v1/memory/spaces/${enc(name)}`, { method: 'DELETE' }),
  listEntries: (space: string, q: PageQuery & { prefix?: string } = {}) =>
    request<Page<MemoryEntry>>(`/v1/memory/spaces/${enc(space)}/entries`, {
      query: { prefix: q.prefix, after: q.after, limit: q.limit },
    }),
  getEntry: (space: string, key: string) =>
    request<MemoryEntry>(`/v1/memory/spaces/${enc(space)}/entries/${enc(key)}`),
  putEntry: (space: string, input: EntryInput) =>
    request<MemoryEntry>(`/v1/memory/spaces/${enc(space)}/entries`, { method: 'POST', body: input }),
  updateEntry: (space: string, key: string, input: EntryInput) =>
    request<MemoryEntry>(`/v1/memory/spaces/${enc(space)}/entries/${enc(key)}`, { method: 'PUT', body: input }),
  deleteEntry: (space: string, key: string) =>
    request<void>(`/v1/memory/spaces/${enc(space)}/entries/${enc(key)}`, { method: 'DELETE' }),
};

// --- tenant, keys, usage, audit ---

export const tenantApi = {
  get: () => request<Tenant>('/v1/tenant'),
  update: (input: TenantSettings) => request<Tenant>('/v1/tenant', { method: 'PUT', body: input }),
};

export const keysApi = {
  list: async () => (await request<Items<APIKey>>('/v1/keys')).items ?? [],
  create: (input: { name: string; role: Role; max_permission_mode?: string }) =>
    request<CreatedKey>('/v1/keys', { method: 'POST', body: input }),
  setDisabled: (id: string, disabled: boolean) =>
    request<APIKey>(`/v1/keys/${enc(id)}/${disabled ? 'disable' : 'enable'}`, { method: 'POST' }),
};

export const usageApi = {
  get: (days?: number) => request<UsageReport>('/v1/usage', { query: { days } }),
};

export const auditApi = {
  list: (
    q: {
      action?: string;
      target?: string;
      actor?: string;
      from?: string;
      to?: string;
    } & PageQuery = {},
  ) =>
    request<Page<AuditLog>>('/v1/audit', {
      query: { action: q.action, target: q.target, actor: q.actor, from: q.from, to: q.to, after: q.after, limit: q.limit },
    }),
};

// --- debugger ---

export const debugApi = {
  timeline: (turnID: string) => request<Timeline>(`/v1/debug/turns/${enc(turnID)}`),
  llmCalls: async (turnID: string) =>
    (await request<Items<LLMCall>>(`/v1/debug/turns/${enc(turnID)}/llm-calls`)).items ?? [],
  assembly: (input: PreviewInput) => request<Preview>('/v1/debug/assembly', { method: 'POST', body: input }),
  fork: (sessionID: string, input: ForkInput) =>
    request<Session>(`/v1/debug/sessions/${enc(sessionID)}/fork`, { method: 'POST', body: input }),
  playground: (input: PlaygroundInput) => request<Session>('/v1/debug/playground', { method: 'POST', body: input }),
};

// --- workflows ---

export const workflowsApi = {
  dryRun: (input: DryRunInput) => request<DryRunResult>('/v1/workflows/dry-run', { method: 'POST', body: input }),
  start: (key: string, input: StartInput) =>
    request<WorkflowRun>(`/v1/workflows/${enc(key)}/runs`, { method: 'POST', body: input }),
  listRuns: (q: { def_key?: string; status?: string; schedule_id?: string; all?: boolean } & PageQuery = {}) =>
    request<Page<WorkflowRun>>('/v1/runs', {
      query: { def_key: q.def_key, status: q.status, schedule_id: q.schedule_id, all: q.all, after: q.after, limit: q.limit },
    }),
  getRun: (id: string) => request<RunView>(`/v1/runs/${enc(id)}`),
  continueRun: (id: string) => request<WorkflowRun>(`/v1/runs/${enc(id)}/continue`, { method: 'POST' }),
  rerun: (id: string) => request<WorkflowRun>(`/v1/runs/${enc(id)}/rerun`, { method: 'POST' }),
  cancelRun: (id: string) => request<WorkflowRun>(`/v1/runs/${enc(id)}/cancel`, { method: 'POST' }),
  promote: (sessionID: string) => request<WorkflowDef>(`/v1/sessions/${enc(sessionID)}/promote`, { method: 'POST' }),
};

export const schedulesApi = {
  list: (q: PageQuery & { all?: boolean } = {}) =>
    request<Page<Schedule>>('/v1/schedules', { query: { all: q.all, after: q.after, limit: q.limit } }),
  create: (input: ScheduleInput) => request<Schedule>('/v1/schedules', { method: 'POST', body: input }),
  get: (id: string) => request<Schedule>(`/v1/schedules/${enc(id)}`),
  update: (id: string, input: ScheduleInput) => request<Schedule>(`/v1/schedules/${enc(id)}`, { method: 'PUT', body: input }),
  remove: (id: string) => request<Schedule>(`/v1/schedules/${enc(id)}`, { method: 'DELETE' }),
  pause: (id: string) => request<Schedule>(`/v1/schedules/${enc(id)}/pause`, { method: 'POST' }),
  resume: (id: string) => request<Schedule>(`/v1/schedules/${enc(id)}/resume`, { method: 'POST' }),
  upcoming: async (id: string, n = 5) =>
    (await request<{ times: string[] | null }>(`/v1/schedules/${enc(id)}/upcoming`, { query: { n } })).times ?? [],
  runs: (id: string, q: PageQuery = {}) =>
    request<Page<ScheduleRun>>(`/v1/schedules/${enc(id)}/runs`, { query: { after: q.after, limit: q.limit } }),
};

// --- integrations ---

export const integrationApi = {
  listServers: async () => (await request<Items<ServerView>>('/v1/mcp-servers')).items ?? [],
  getServer: (id: string) => request<ServerView>(`/v1/mcp-servers/${enc(id)}`),
  createServer: (input: ServerInput) => request<ServerView>('/v1/mcp-servers', { method: 'POST', body: input }),
  updateServer: (id: string, input: ServerInput) =>
    request<ServerView>(`/v1/mcp-servers/${enc(id)}`, { method: 'PUT', body: input }),
  deleteServer: (id: string) => request<void>(`/v1/mcp-servers/${enc(id)}`, { method: 'DELETE' }),
  syncServer: (id: string) => request<ServerView>(`/v1/mcp-servers/${enc(id)}/sync`, { method: 'POST' }),
  testServer: (id: string) => request<ServerTestResult>(`/v1/mcp-servers/${enc(id)}/test`, { method: 'POST' }),

  listSources: async () => (await request<Items<RetrievalSource>>('/v1/retrieval-sources')).items ?? [],
  createSource: (input: SourceInput) => request<RetrievalSource>('/v1/retrieval-sources', { method: 'POST', body: input }),
  updateSource: (id: string, input: SourceInput) =>
    request<RetrievalSource>(`/v1/retrieval-sources/${enc(id)}`, { method: 'PUT', body: input }),
  deleteSource: (id: string) => request<void>(`/v1/retrieval-sources/${enc(id)}`, { method: 'DELETE' }),
  trySource: (id: string, query: string, limit?: number) =>
    request<TryResult>(`/v1/retrieval-sources/${enc(id)}/try`, { method: 'POST', body: { query, limit } }),

  catalog: async () => (await request<Items<CatalogEntry>>('/v1/tools')).items ?? [],
  testTool: (name: string, input?: unknown, confirm?: boolean) =>
    request<ToolTestResult>(`/v1/tools/${enc(name)}/test`, { method: 'POST', body: { input, confirm } }),

  listOverrides: async () => (await request<Items<RiskOverride>>('/v1/risk-overrides')).items ?? [],
  setOverride: (input: RiskOverrideInput) => request<RiskOverride>('/v1/risk-overrides', { method: 'POST', body: input }),
  deleteOverride: (id: string) => request<void>(`/v1/risk-overrides/${enc(id)}`, { method: 'DELETE' }),

  templateAvailability: async () => (await request<Items<TemplateStatus>>('/v1/templates/availability')).items ?? [],
};

export const feishuApi = {
  listChats: async () => (await request<Items<FeishuChat>>('/v1/feishu/chats')).items ?? [],
  bindChat: (input: { chat_id: string; name?: string; default_role?: string }) =>
    request<FeishuChat>('/v1/feishu/chats', { method: 'POST', body: input }),
  unbindChat: (id: string) => request<FeishuChat>(`/v1/feishu/chats/${enc(id)}`, { method: 'DELETE' }),
  listThreads: async (sessionID: string) =>
    (await request<Items<FeishuThread>>('/v1/feishu/threads', { query: { session_id: sessionID } })).items ?? [],
};

// --- platform (platform admins only) ---

export const platformApi = {
  listTenants: async () => (await request<Items<Tenant>>('/v1/platform/tenants')).items ?? [],
  createTenant: (input: CreateTenantInput) => request<Tenant>('/v1/platform/tenants', { method: 'POST', body: input }),
  status: () => request<PlatformStatus>('/v1/platform/status'),
  breakGlassSession: (id: string, reason: string) =>
    request<BreakGlassSession>(`/v1/platform/sessions/${enc(id)}`, { query: { reason } }),
  breakGlassTurn: (id: string, reason: string) =>
    request<Timeline>(`/v1/platform/turns/${enc(id)}`, { query: { reason } }),
};