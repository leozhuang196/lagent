// Conversation state: load the snapshot, then follow the event stream.
//
// The stream is opened from seq 0 so the buffered history fills in what the
// snapshot does not carry (tool risk levels, artifacts, past turn outcomes);
// events up to the snapshot's last_seq are applied in "replay" mode, which
// skips streaming text. Messages are de-duplicated by id.

import { useCallback, useEffect, useReducer, useRef } from 'react';
import {
  errorText,
  openSessionStream,
  sessionsApi,
  turnsApi,
  type Artifact,
  type Decision,
  type JsonValue,
  type LagentEvent,
  type Message,
  type Pending,
  type RiskLevel,
  type Session,
  type Snapshot,
  type StreamHandle,
  type StreamState,
  type TurnError,
  type TurnStatus,
  type Usage,
} from '../api';

export type ToolPhase = 'requested' | 'awaiting_approval' | 'denied' | 'started' | 'succeeded' | 'failed';

export interface ToolActivity {
  id: string;
  turnId: string;
  tool: string;
  risk?: RiskLevel;
  phase: ToolPhase;
  input?: JsonValue;
  durationMs?: number;
}

export interface TurnInfo {
  id: string;
  status: TurnStatus;
  error?: TurnError | null;
  usage?: Usage;
  /** Seq of the event (or snapshot cursor) this state came from. */
  seq: number;
}

export interface ResolvedNote {
  decision: Decision;
  via?: string;
  alreadyResolved?: boolean;
}

export interface ConversationState {
  phase: 'loading' | 'ready' | 'error';
  error?: string;
  session?: Session;
  messages: Message[];
  pendings: Pending[];
  resolved: Record<string, ResolvedNote>;
  turns: Record<string, TurnInfo>;
  activeTurnId?: string;
  streaming: { turnId: string; text: string } | null;
  tools: ToolActivity[];
  artifacts: Artifact[];
  stream: StreamState;
}

type Action =
  | { type: 'reset' }
  | { type: 'snapshot'; snap: Snapshot }
  | { type: 'event'; ev: LagentEvent; replay: boolean }
  | { type: 'stream'; state: StreamState }
  | { type: 'error'; error: string }
  | { type: 'turn'; turn: TurnInfo }
  | { type: 'resolved'; id: string; note: ResolvedNote }
  | { type: 'optimistic'; message: Message }
  | { type: 'track'; turnId: string; status: TurnStatus };

const initial: ConversationState = {
  phase: 'loading',
  messages: [],
  pendings: [],
  resolved: {},
  turns: {},
  streaming: null,
  tools: [],
  artifacts: [],
  stream: 'connecting',
};

function plain(m: Message): string {
  return m.content
    .filter((b) => b.type === 'text')
    .map((b) => b.text ?? '')
    .join('\n');
}

function mergeMessages(cur: Message[], add: Message[]): Message[] {
  const byId = new Map<string, Message>();
  for (const m of cur) byId.set(m.id, m);
  for (const m of add) {
    // The stored copy of a message we showed optimistically replaces it.
    for (const [k, v] of byId) {
      if (k.startsWith('local-') && v.turn_id === m.turn_id && v.role === m.role && plain(v) === plain(m)) byId.delete(k);
    }
    byId.set(m.id, m);
  }
  return [...byId.values()].sort((a, b) => a.seq - b.seq || a.created_at.localeCompare(b.created_at));
}

function upsertPending(list: Pending[], p: Pending): Pending[] {
  const i = list.findIndex((x) => x.id === p.id);
  if (i < 0) return [...list, p];
  const next = list.slice();
  next[i] = p;
  return next;
}

function setTurn(s: ConversationState, t: TurnInfo): ConversationState {
  const prev = s.turns[t.id];
  if (prev && prev.seq > t.seq) return s;
  return { ...s, turns: { ...s.turns, [t.id]: { ...prev, ...t } } };
}

function upsertTool(list: ToolActivity[], t: Partial<ToolActivity> & { id: string }): ToolActivity[] {
  const i = list.findIndex((x) => x.id === t.id);
  if (i < 0) {
    return [...list, { turnId: '', tool: '', phase: 'requested', ...t } as ToolActivity];
  }
  const next = list.slice();
  next[i] = { ...next[i], ...t };
  return next;
}

function reqPhase(status: string): ToolPhase {
  if (status === 'awaiting_approval') return 'awaiting_approval';
  if (status === 'denied') return 'denied';
  return 'requested';
}

function applyEvent(s: ConversationState, ev: LagentEvent, replay: boolean): ConversationState {
  const turnId = ev.turn_id ?? '';
  switch (ev.type) {
    case 'turn.started':
      s = setTurn(s, { id: turnId, status: 'running', seq: ev.seq });
      return replay ? s : { ...s, activeTurnId: turnId, streaming: null };
    case 'message.delta': {
      if (replay) return s;
      const cur = s.streaming && s.streaming.turnId === turnId ? s.streaming.text : '';
      return { ...s, streaming: { turnId, text: cur + ev.data.text } };
    }
    case 'message.completed': {
      const m = ev.data.message;
      const streaming = m.role === 'assistant' && s.streaming?.turnId === m.turn_id ? null : s.streaming;
      return { ...s, messages: mergeMessages(s.messages, [m]), streaming };
    }
    case 'tool.requested':
      return {
        ...s,
        tools: upsertTool(s.tools, {
          id: ev.data.tool_call_id,
          turnId,
          tool: ev.data.tool,
          risk: ev.data.risk,
          input: ev.data.input,
          phase: reqPhase(ev.data.status),
        }),
      };
    case 'tool.started':
      return { ...s, tools: upsertTool(s.tools, { id: ev.data.tool_call_id, turnId, tool: ev.data.tool, phase: 'started' }) };
    case 'tool.completed':
      return {
        ...s,
        tools: upsertTool(s.tools, {
          id: ev.data.tool_call_id,
          turnId,
          tool: ev.data.tool,
          phase: ev.data.is_error ? 'failed' : 'succeeded',
          durationMs: ev.data.duration_ms,
        }),
      };
    case 'pending.created': {
      const p = ev.data.pending;
      const next = { ...s, pendings: upsertPending(s.pendings, p) };
      return setTurn(next, { id: p.turn_id || turnId, status: 'suspended', seq: ev.seq });
    }
    case 'pending.resolved':
      return {
        ...s,
        resolved: { ...s.resolved, [ev.data.pending_id]: { decision: ev.data.decision, via: ev.data.via } },
      };
    case 'artifact.created': {
      const a = ev.data.artifact;
      if (s.artifacts.some((x) => x.id === a.id)) return s;
      return { ...s, artifacts: [...s.artifacts, a] };
    }
    case 'turn.suspended':
      return setTurn(s, { id: turnId, status: 'suspended', seq: ev.seq });
    case 'turn.completed':
    case 'turn.failed':
    case 'turn.cancelled': {
      const status: TurnStatus =
        ev.type === 'turn.completed' ? 'completed' : ev.type === 'turn.failed' ? 'failed' : 'cancelled';
      s = setTurn(s, { id: turnId, status, error: ev.data.error, usage: ev.data.usage, seq: ev.seq });
      return s.streaming?.turnId === turnId ? { ...s, streaming: null } : s;
    }
    default:
      return s;
  }
}

function reducer(s: ConversationState, a: Action): ConversationState {
  switch (a.type) {
    case 'reset':
      return initial;
    case 'snapshot': {
      const { snap } = a;
      let next: ConversationState = {
        ...s,
        phase: 'ready',
        error: undefined,
        session: snap.session,
        messages: mergeMessages(s.messages, snap.messages ?? []),
        // Open items come from the snapshot; resolved ones we saw stay visible.
        pendings: [
          ...(snap.pendings ?? []),
          ...s.pendings.filter((p) => !(snap.pendings ?? []).some((x) => x.id === p.id) && s.resolved[p.id]),
        ],
        activeTurnId: snap.session.active_turn_id || s.activeTurnId,
      };
      if (snap.active_turn) {
        next = setTurn(next, {
          id: snap.active_turn.id,
          status: snap.active_turn.status,
          error: snap.active_turn.error,
          usage: snap.active_turn.usage,
          seq: snap.last_seq,
        });
      }
      return next;
    }
    case 'event':
      return applyEvent(s, a.ev, a.replay);
    case 'stream':
      return { ...s, stream: a.state };
    case 'error':
      return { ...s, phase: s.phase === 'ready' ? 'ready' : 'error', error: a.error };
    case 'turn':
      return setTurn(s, a.turn);
    case 'resolved':
      return { ...s, resolved: { ...s.resolved, [a.id]: a.note } };
    case 'optimistic':
      return { ...s, messages: mergeMessages(s.messages, [a.message]) };
    case 'track':
      // seq -1: any event about this turn supersedes the local guess.
      return { ...setTurn(s, { id: a.turnId, status: a.status, seq: -1 }), activeTurnId: a.turnId };
  }
}

const REFRESH_ON = new Set<LagentEvent['type']>(['turn.started', 'turn.completed', 'turn.failed', 'turn.cancelled', 'stream.reset']);

export interface Conversation {
  state: ConversationState;
  reload(): void;
  markResolved(id: string, note: ResolvedNote): void;
  addOptimistic(m: Message): void;
  trackTurn(turnId: string, status: TurnStatus): void;
}

export function useConversation(sessionId: string): Conversation {
  const [state, dispatch] = useReducer(reducer, initial);
  const refreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const alive = useRef(true);

  const loadSnapshot = useCallback(async (): Promise<Snapshot | null> => {
    try {
      const snap = await sessionsApi.snapshot(sessionId);
      if (!alive.current) return null;
      dispatch({ type: 'snapshot', snap });
      // The snapshot only carries the active turn; settle a turn we last saw
      // running if the stream missed its end.
      return snap;
    } catch (e) {
      if (alive.current) dispatch({ type: 'error', error: errorText(e) });
      return null;
    }
  }, [sessionId]);

  const scheduleRefresh = useCallback(() => {
    if (refreshTimer.current) clearTimeout(refreshTimer.current);
    refreshTimer.current = setTimeout(() => void loadSnapshot(), 250);
  }, [loadSnapshot]);

  useEffect(() => {
    alive.current = true;
    dispatch({ type: 'reset' });
    let handle: StreamHandle | null = null;
    void (async () => {
      const snap = await loadSnapshot();
      if (!snap || !alive.current) return;
      const replayUntil = snap.last_seq;
      // A turn the snapshot no longer lists as active may have ended while we
      // were away; ask for its state.
      handle = openSessionStream(sessionId, {
        after: 0,
        onEvent: (ev) => {
          const replay = ev.seq <= replayUntil && ev.type !== 'stream.reset';
          dispatch({ type: 'event', ev, replay });
          if (!replay && REFRESH_ON.has(ev.type)) scheduleRefresh();
        },
        onState: (st) => dispatch({ type: 'stream', state: st }),
        onFatal: (e) => dispatch({ type: 'error', error: errorText(e) }),
      });
    })();
    return () => {
      alive.current = false;
      handle?.close();
      if (refreshTimer.current) clearTimeout(refreshTimer.current);
    };
  }, [sessionId, loadSnapshot, scheduleRefresh]);

  // Settle the active turn when the session went idle without us seeing the end.
  const active = state.activeTurnId ? state.turns[state.activeTurnId] : undefined;
  const sessionIdle = state.session?.status === 'idle';
  useEffect(() => {
    if (!active || !sessionIdle) return;
    if (active.status === 'completed' || active.status === 'failed' || active.status === 'cancelled') return;
    let stop = false;
    turnsApi
      .get(active.id)
      .then((t) => {
        if (!stop && alive.current) {
          dispatch({ type: 'turn', turn: { id: t.id, status: t.status, error: t.error, usage: t.usage, seq: active.seq } });
        }
      })
      .catch(() => undefined);
    return () => {
      stop = true;
    };
  }, [active, sessionIdle]);

  return {
    state,
    reload: () => void loadSnapshot(),
    markResolved: (id, note) => dispatch({ type: 'resolved', id, note }),
    addOptimistic: (m) => dispatch({ type: 'optimistic', message: m }),
    trackTurn: (turnId, status) => dispatch({ type: 'track', turnId, status }),
  };
}

/** Effective state of the session's current turn, for the status line. */
export function currentTurn(s: ConversationState): TurnInfo | undefined {
  return s.activeTurnId ? s.turns[s.activeTurnId] : undefined;
}

export function isTerminal(status?: TurnStatus): boolean {
  return status === 'completed' || status === 'failed' || status === 'cancelled';
}

/** Open pendings that are not yet known to be resolved. */
export function openPendings(s: ConversationState): Pending[] {
  return s.pendings.filter((p) => p.status === 'open' && !s.resolved[p.id]);
}
