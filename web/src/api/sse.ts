// Server-sent events over fetch (EventSource cannot send the client and
// tenant headers). Reconnects with Last-Event-ID; the server answers a cursor
// older than its buffer with stream.reset.

import { baseHeaders, decodeError, type ApiError } from './client';
import type { LagentEvent } from './types';

export type StreamState = 'connecting' | 'open' | 'reconnecting' | 'closed';

export interface StreamOptions {
  /** Resume after this sequence number (snapshot last_seq). */
  after: number;
  onEvent: (ev: LagentEvent) => void;
  onState?: (s: StreamState) => void;
  /** Fatal errors (401/403/404): the stream stops. */
  onFatal?: (e: ApiError) => void;
}

export interface StreamHandle {
  close(): void;
  /** Current cursor (last delivered seq). */
  cursor(): number;
  /** Move the cursor (after a snapshot reload) and reconnect. */
  resetTo(seq: number): void;
}

interface RawEvent {
  id?: string;
  event?: string;
  data: string;
}

/** Splits an SSE byte stream into events; comments (": ping") are skipped. */
export async function* parseSSE(body: ReadableStream<Uint8Array>, signal: AbortSignal): AsyncGenerator<RawEvent> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buf = '';
  const onAbort = () => void reader.cancel().catch(() => undefined);
  signal.addEventListener('abort', onAbort);
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) return;
      // Normalise line endings; the server writes \n, proxies may rewrite.
      buf += decoder.decode(value, { stream: true }).replace(/\r\n?/g, '\n');
      let idx: number;
      while ((idx = buf.indexOf('\n\n')) >= 0) {
        const chunk = buf.slice(0, idx);
        buf = buf.slice(idx + 2);
        const ev: RawEvent = { data: '' };
        const data: string[] = [];
        for (const line of chunk.split('\n')) {
          if (!line || line.startsWith(':')) continue;
          const colon = line.indexOf(':');
          const field = colon < 0 ? line : line.slice(0, colon);
          let val = colon < 0 ? '' : line.slice(colon + 1);
          if (val.startsWith(' ')) val = val.slice(1);
          if (field === 'id') ev.id = val;
          else if (field === 'event') ev.event = val;
          else if (field === 'data') data.push(val);
        }
        if (data.length === 0) continue;
        ev.data = data.join('\n');
        yield ev;
      }
    }
  } finally {
    signal.removeEventListener('abort', onAbort);
  }
}

function decodeEvent(raw: RawEvent): LagentEvent | null {
  try {
    return JSON.parse(raw.data) as LagentEvent;
  } catch {
    return null;
  }
}

/** Follows an SSE stream with automatic reconnect. */
function openStream(url: string, opts: StreamOptions): StreamHandle {
  let cursor = opts.after;
  let closed = false;
  let ctrl: AbortController | null = null;
  let attempt = 0;
  let timer: ReturnType<typeof setTimeout> | null = null;

  const setState = (s: StreamState) => opts.onState?.(s);

  const connect = async () => {
    if (closed) return;
    ctrl = new AbortController();
    const myCtrl = ctrl;
    setState(attempt === 0 ? 'connecting' : 'reconnecting');
    try {
      const headers = baseHeaders({ Accept: 'text/event-stream' });
      if (cursor > 0) headers['Last-Event-ID'] = String(cursor);
      const sep = url.includes('?') ? '&' : '?';
      const res = await fetch(`${url}${sep}after=${cursor}`, {
        headers,
        credentials: 'include',
        signal: myCtrl.signal,
        cache: 'no-store',
      });
      if (!res.ok || !res.body) {
        const err = await decodeError(res);
        if ([401, 403, 404].includes(res.status)) {
          closed = true;
          setState('closed');
          opts.onFatal?.(err);
          return;
        }
        throw err;
      }
      attempt = 0;
      setState('open');
      for await (const raw of parseSSE(res.body, myCtrl.signal)) {
        const ev = decodeEvent(raw);
        if (!ev) continue;
        if (ev.type !== 'stream.reset' && ev.seq <= cursor) continue;
        cursor = ev.seq;
        opts.onEvent(ev);
      }
    } catch (e) {
      if (myCtrl.signal.aborted) return;
      void e;
    }
    if (closed || myCtrl.signal.aborted) return;
    attempt += 1;
    setState('reconnecting');
    const delay = Math.min(10_000, 500 * 2 ** Math.min(attempt, 5));
    timer = setTimeout(() => void connect(), delay);
  };

  void connect();

  return {
    close() {
      closed = true;
      if (timer) clearTimeout(timer);
      ctrl?.abort();
      setState('closed');
    },
    cursor: () => cursor,
    resetTo(seq: number) {
      cursor = seq;
      if (timer) clearTimeout(timer);
      ctrl?.abort();
      attempt = 0;
      void connect();
    },
  };
}

/** Follows GET /v1/sessions/{id}/events with automatic reconnect. */
export function openSessionStream(sessionId: string, opts: StreamOptions): StreamHandle {
  return openStream(`/v1/sessions/${encodeURIComponent(sessionId)}/events`, opts);
}

/**
 * Replays the buffered backlog of a session and stops once the stream goes
 * quiet. Used where no list endpoint exists yet (artifacts of a session).
 */
export async function replaySessionEvents(sessionId: string, idleMs = 800, signal?: AbortSignal): Promise<LagentEvent[]> {
  const ctrl = new AbortController();
  signal?.addEventListener('abort', () => ctrl.abort());
  const res = await fetch(`/v1/sessions/${encodeURIComponent(sessionId)}/events?after=0`, {
    headers: baseHeaders({ Accept: 'text/event-stream' }),
    credentials: 'include',
    signal: ctrl.signal,
    cache: 'no-store',
  });
  if (!res.ok || !res.body) throw await decodeError(res);
  const out: LagentEvent[] = [];
  let timer = setTimeout(() => ctrl.abort(), idleMs * 3);
  try {
    for await (const raw of parseSSE(res.body, ctrl.signal)) {
      const ev = decodeEvent(raw);
      if (ev) out.push(ev);
      clearTimeout(timer);
      timer = setTimeout(() => ctrl.abort(), idleMs);
    }
  } catch (e) {
    if (!ctrl.signal.aborted) throw e;
  } finally {
    clearTimeout(timer);
  }
  return out;
}

/** Follows GET /v1/runs/{id}/events (same resume contract as session streams). */
export function openRunStream(runId: string, opts: StreamOptions): StreamHandle {
  return openStream(`/v1/runs/${encodeURIComponent(runId)}/events`, opts);
}

/**
 * Reads the buffered run log once and stops (the endpoint keeps the connection
 * open, so an idle timer ends it). Used for runs that already finished: their
 * live subscription would start at the current cursor and see nothing.
 */
export async function replayRunEvents(runId: string, idleMs = 800, signal?: AbortSignal): Promise<LagentEvent[]> {
  const ctrl = new AbortController();
  signal?.addEventListener('abort', () => ctrl.abort());
  const res = await fetch(`/v1/runs/${encodeURIComponent(runId)}/events?after=0`, {
    headers: baseHeaders({ Accept: 'text/event-stream' }),
    credentials: 'include',
    signal: ctrl.signal,
    cache: 'no-store',
  });
  if (!res.ok || !res.body) throw await decodeError(res);
  const out: LagentEvent[] = [];
  let timer = setTimeout(() => ctrl.abort(), idleMs * 3);
  try {
    for await (const raw of parseSSE(res.body, ctrl.signal)) {
      const ev = decodeEvent(raw);
      if (ev) out.push(ev);
      clearTimeout(timer);
      timer = setTimeout(() => ctrl.abort(), idleMs);
    }
  } catch (e) {
    if (!ctrl.signal.aborted) throw e;
  } finally {
    clearTimeout(timer);
  }
  return out;
}
