// The single place that talks HTTP to lagent: credentials, client headers,
// tenant override and error decoding.

import type { JsonObject } from './types';

const TENANT_KEY = 'lagent.tenant';

/** Error decoded from {error:{code,message,details}} (or synthesised for network failures). */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly details?: JsonObject;

  constructor(status: number, code: string, message: string, details?: JsonObject) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.details = details;
  }
}

export function isApiError(e: unknown): e is ApiError {
  return e instanceof ApiError;
}

/** Human-readable error text for toasts and error states. */
export function errorText(e: unknown): string {
  if (isApiError(e)) return e.code ? `${e.message}（${e.code}）` : e.message;
  if (e instanceof Error) return e.message;
  return String(e);
}

// --- tenant override (platform admins act inside another tenant) ---

function readTenant(): string {
  try {
    return localStorage.getItem(TENANT_KEY) ?? '';
  } catch {
    return '';
  }
}

let tenantOverride = readTenant();

export function getTenantOverride(): string {
  return tenantOverride;
}

export function setTenantOverride(t: string): void {
  tenantOverride = t;
  try {
    if (t) localStorage.setItem(TENANT_KEY, t);
    else localStorage.removeItem(TENANT_KEY);
  } catch {
    /* storage unavailable: keep it in memory only */
  }
}

/** Headers every request carries. */
export function baseHeaders(extra?: Record<string, string>): Record<string, string> {
  const h: Record<string, string> = { 'X-Lagent-Client': 'web', ...extra };
  if (tenantOverride) h['X-Lagent-Tenant'] = tenantOverride;
  return h;
}

// --- unauthorised hook (AuthProvider redirects to login) ---

let onUnauthorized: (() => void) | null = null;
export function setUnauthorizedHandler(fn: (() => void) | null): void {
  onUnauthorized = fn;
}

interface ErrorEnvelope {
  error?: { code?: string; message?: string; details?: JsonObject };
}

export async function decodeError(res: Response): Promise<ApiError> {
  let body: ErrorEnvelope | null = null;
  try {
    body = (await res.json()) as ErrorEnvelope;
  } catch {
    body = null;
  }
  const code = body?.error?.code ?? `http_${res.status}`;
  const message = body?.error?.message ?? (res.statusText || `请求失败（HTTP ${res.status}）`);
  return new ApiError(res.status, code, message, body?.error?.details);
}

export interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';
  body?: unknown;
  query?: Record<string, string | number | boolean | undefined | null>;
  headers?: Record<string, string>;
  signal?: AbortSignal;
  /** Do not trigger the global login redirect on 401 (used by the login probe). */
  quiet401?: boolean;
}

export function buildPath(path: string, query?: RequestOptions['query']): string {
  if (!query) return path;
  const qs = new URLSearchParams();
  for (const [k, v] of Object.entries(query)) {
    if (v === undefined || v === null || v === '' || v === false) continue;
    qs.set(k, v === true ? '1' : String(v));
  }
  const s = qs.toString();
  return s ? `${path}?${s}` : path;
}

export async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const headers = baseHeaders(opts.headers);
  let body: BodyInit | undefined;
  if (opts.body !== undefined) {
    headers['Content-Type'] = 'application/json';
    body = JSON.stringify(opts.body);
  }
  let res: Response;
  try {
    res = await fetch(buildPath(path, opts.query), {
      method: opts.method ?? (body ? 'POST' : 'GET'),
      headers,
      body,
      credentials: 'include',
      signal: opts.signal,
    });
  } catch (e) {
    if (e instanceof DOMException && e.name === 'AbortError') throw e;
    throw new ApiError(0, 'network', '无法连接服务器，请检查网络');
  }
  if (!res.ok) {
    const err = await decodeError(res);
    if (res.status === 401 && !opts.quiet401) onUnauthorized?.();
    throw err;
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}
