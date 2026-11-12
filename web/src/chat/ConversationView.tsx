import { Fragment, useEffect, useMemo, useRef } from 'react';
import { Link } from 'react-router-dom';
import { Alert, Badge, Button, Space, Tag, Tooltip, Typography, message } from 'antd';
import { BugOutlined, ReloadOutlined } from '@ant-design/icons';
import { errorText, sessionsApi, type Message, type Pending } from '../api';
import { ErrorState, Loading } from '../components/States';
import { TurnStateTag } from '../components/Tags';
import { CANCEL_NOTE } from '../lib/labels';
import { newIdempotencyKey } from '../lib/format';
import { ArtifactChip } from './ArtifactChip';
import { Composer } from './Composer';
import { Markdown } from '../components/Markdown';
import { MessageItem, ToolCallView, matchActivities } from './MessageList';
import { PendingCard } from './PendingCard';
import { currentTurn, isTerminal, openPendings, useConversation, type ConversationState, type TurnInfo } from './useConversation';

interface Props {
  sessionId: string;
  /** Console view: no composer, no decisions. */
  readOnly?: boolean;
}

const streamLabel = { connecting: '连接中', open: '实时', reconnecting: '重连中', closed: '已断开' } as const;
const streamColor = { connecting: 'processing', open: 'success', reconnecting: 'warning', closed: 'default' } as const;

function turnDisplayState(t: TurnInfo, pendings: Pending[]) {
  if (t.status === 'suspended') {
    const waitingChildren = pendings.some(
      (p) => p.turn_id === t.id && p.status === 'open' && (p.kind === 'subtasks' || p.kind === 'external_job'),
    );
    return waitingChildren ? 'waiting_subtasks' : 'suspended';
  }
  return t.status;
}

function TurnFooter({ turn, pendings, current }: { turn: TurnInfo; pendings: Pending[]; current: boolean }) {
  if (!current && turn.status === 'completed') return null;
  const st = turnDisplayState(turn, pendings);
  return (
    <div className="turn-footer">
      <TurnStateTag state={st} />
      {turn.status === 'cancelled' && <Typography.Text type="secondary">{CANCEL_NOTE}</Typography.Text>}
      {turn.error && (
        <Typography.Text type="danger">
          {turn.error.message}（{turn.error.code}）
        </Typography.Text>
      )}
      {turn.usage && isTerminal(turn.status) && (
        <span className="muted small">
          tokens 输入 {turn.usage.input_tokens} / 输出 {turn.usage.output_tokens}
        </span>
      )}
    </div>
  );
}

/** Turn ids in display order: message order, then the active turn, then turns only pendings mention. */
function turnOrder(s: ConversationState): string[] {
  const order: string[] = [];
  const seen = new Set<string>();
  const add = (id?: string) => {
    if (id && !seen.has(id)) {
      seen.add(id);
      order.push(id);
    }
  };
  s.messages.forEach((m) => add(m.turn_id));
  s.pendings.forEach((p) => add(p.turn_id));
  add(s.activeTurnId);
  return order;
}

export function ConversationView({ sessionId, readOnly }: Props) {
  const conv = useConversation(sessionId);
  const { state } = conv;
  const scrollRef = useRef<HTMLDivElement>(null);
  const stickToBottom = useRef(true);

  const { activities, results, unmatched } = useMemo(() => matchActivities(state.messages, state.tools), [state.messages, state.tools]);
  const order = useMemo(() => turnOrder(state), [state]);
  const turn = currentTurn(state);
  const busy = !!turn && !isTerminal(turn.status);
  const open = openPendings(state);

  // Follow new content unless the reader scrolled up.
  useEffect(() => {
    const el = scrollRef.current;
    if (el && stickToBottom.current) el.scrollTop = el.scrollHeight;
  }, [state.messages, state.streaming, state.pendings, state.tools, state.turns]);

  if (state.phase === 'loading') return <Loading />;
  if (state.phase === 'error' && !state.session) return <ErrorState error={state.error ?? '未知错误'} onRetry={conv.reload} />;
  const session = state.session!;

  const onSend = async (text: string): Promise<boolean> => {
    try {
      const res = await sessionsApi.submit(sessionId, text, newIdempotencyKey());
      const lastSeq = state.messages.length ? state.messages[state.messages.length - 1].seq : 0;
      const local: Message = {
        id: `local-${newIdempotencyKey()}`,
        tenant_id: session.tenant_id,
        version: 0,
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
        session_id: sessionId,
        turn_id: res.turn.id,
        seq: lastSeq + 0.5,
        role: 'user',
        content: [{ type: 'text', text }],
        epoch: 0,
      };
      conv.addOptimistic(local);
      if (res.queued) void message.info('助手正在处理，这条消息已并入当前轮次');
      else conv.trackTurn(res.turn.id, res.turn.status);
      return true;
    } catch (e) {
      void message.error(`发送失败：${errorText(e)}`);
      return false;
    }
  };

  const onCancel = async () => {
    try {
      const r = await sessionsApi.cancel(sessionId);
      void message.success(r.note || CANCEL_NOTE);
      conv.reload();
    } catch (e) {
      void message.error(errorText(e));
    }
  };

  return (
    <div className="conversation">
      <div className="conv-header">
        <div className="conv-title">
          <Typography.Title level={5} style={{ margin: 0 }} ellipsis>
            {session.title || '未命名会话'}
          </Typography.Title>
          <Space size={4} wrap>
            <Tag>
              角色 {session.role_key} v{session.role_ver}
            </Tag>
            <Tag>权限 {session.permission_mode}</Tag>
            {session.debug && <Tag color="purple">调试</Tag>}
            {readOnly && <Tag>来源 {session.origin}</Tag>}
          </Space>
        </div>
        <Space>
          {turn && <TurnStateTag state={turnDisplayState(turn, state.pendings)} />}
          <Tooltip title="事件流状态">
            <Badge status={streamColor[state.stream]} text={streamLabel[state.stream]} />
          </Tooltip>
          <Tooltip title="重新加载快照">
            <Button size="small" type="text" icon={<ReloadOutlined />} onClick={conv.reload} aria-label="重新加载" />
          </Tooltip>
          {readOnly && (
            <Link to={`/admin/debug/turns?session=${encodeURIComponent(sessionId)}${turn ? `&turn=${encodeURIComponent(turn.id)}` : ''}`}>
              <Button size="small" icon={<BugOutlined />}>
                轮次调试器
              </Button>
            </Link>
          )}
        </Space>
      </div>
      {state.error && state.session && <Alert type="error" showIcon closable message={state.error} style={{ margin: '8px 16px 0' }} />}
      <div
        className="conv-scroll"
        ref={scrollRef}
        onScroll={(e) => {
          const el = e.currentTarget;
          stickToBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
        }}
      >
        <div className="conv-inner">
          {order.length === 0 && (
            <div className="conv-empty">
              <Typography.Title level={4}>有什么可以帮你？</Typography.Title>
              <Typography.Text type="secondary">发送消息开始对话。需要确认的操作会在这里以卡片形式出现。</Typography.Text>
            </div>
          )}
          {order.map((tid) => {
            const msgs = state.messages.filter((m) => m.turn_id === tid);
            const info = state.turns[tid];
            const loose = unmatched.filter((a) => a.turnId === tid);
            const pend = state.pendings.filter((p) => p.turn_id === tid);
            const arts = state.artifacts.filter((a) => a.turn_id === tid);
            const streaming = state.streaming?.turnId === tid ? state.streaming.text : '';
            return (
              <Fragment key={tid}>
                {msgs.map((m) => (
                  <MessageItem key={m.id} m={m} activities={activities} results={results} />
                ))}
                {(loose.length > 0 || streaming) && (
                  <div className="msg msg-assistant">
                    <div className="msg-avatar" />
                    <div className="msg-body">
                      {loose.map((a) => (
                        <ToolCallView key={a.id} activity={a} />
                      ))}
                      {streaming && (
                        <div className="assistant-text streaming">
                          <Markdown text={streaming} />
                          <span className="caret" />
                        </div>
                      )}
                    </div>
                  </div>
                )}
                {(pend.length > 0 || arts.length > 0) && (
                  <div className="turn-extras">
                    {pend.map((p) => (
                      <PendingCard key={p.id} pending={p} resolved={state.resolved[p.id]} readOnly={readOnly} onResolved={conv.markResolved} />
                    ))}
                    {arts.length > 0 && (
                      <div className="artifacts">
                        <div className="muted small">交付物</div>
                        {arts.map((a) => (
                          <ArtifactChip key={a.id} artifact={a} />
                        ))}
                      </div>
                    )}
                  </div>
                )}
                {info && <TurnFooter turn={info} pendings={state.pendings} current={tid === state.activeTurnId} />}
              </Fragment>
            );
          })}
        </div>
      </div>
      {!readOnly && (
        <div className="conv-composer">
          {open.length > 0 && (
            <Alert
              type="warning"
              showIcon
              banner
              message={`有 ${open.length} 项待你确认，处理后助手会继续`}
              style={{ marginBottom: 8 }}
            />
          )}
          <Composer busy={busy} running={busy} onSend={onSend} onCancel={onCancel} disabled={state.stream === 'closed' && state.phase === 'error'} />
        </div>
      )}
    </div>
  );
}
