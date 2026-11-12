import { Collapse, Space, Spin, Tag, Typography } from 'antd';
import { CheckCircleOutlined, CloseCircleOutlined, LoadingOutlined, PauseCircleOutlined, ToolOutlined, UserOutlined, RobotOutlined } from '@ant-design/icons';
import type { Block, Message } from '../api';
import { JsonBlock } from '../components/JsonBlock';
import { Markdown } from '../components/Markdown';
import { RiskTag } from '../components/Tags';
import { unwrapToolData } from '../lib/format';
import type { ToolActivity, ToolPhase } from './useConversation';

const phaseMeta: Record<ToolPhase, { label: string; color: string; icon: JSX.Element }> = {
  requested: { label: '已请求', color: 'default', icon: <LoadingOutlined /> },
  awaiting_approval: { label: '等待审批', color: 'warning', icon: <PauseCircleOutlined /> },
  denied: { label: '已拒绝', color: 'default', icon: <CloseCircleOutlined /> },
  started: { label: '执行中', color: 'processing', icon: <LoadingOutlined /> },
  succeeded: { label: '成功', color: 'success', icon: <CheckCircleOutlined /> },
  failed: { label: '失败', color: 'error', icon: <CloseCircleOutlined /> },
};

function ToolResultBody({ block }: { block: Block }) {
  const parts = unwrapToolData(block.text ?? '');
  return (
    <>
      {parts.map((p, i) => (
        <div key={i}>
          {p.source && <div className="muted small">来源：{p.source}</div>}
          <pre className={`code ${block.is_error ? 'code-error' : ''}`}>{p.body || '（空）'}</pre>
        </div>
      ))}
    </>
  );
}

interface ToolCallProps {
  use?: Block;
  result?: Block;
  activity?: ToolActivity;
}

/** A tool call: name, risk, live phase, input and (collapsed) result. */
export function ToolCallView({ use, result, activity }: ToolCallProps) {
  const name = use?.name ?? activity?.tool ?? '工具';
  let phase: ToolPhase | undefined = activity?.phase;
  if (result) phase = result.is_error ? 'failed' : (phase === 'denied' ? 'denied' : 'succeeded');
  const pm = phase ? phaseMeta[phase] : undefined;
  const items = [
    {
      key: 'input',
      label: '参数',
      children: <JsonBlock value={use?.input ?? activity?.input} />,
    },
    ...(result
      ? [
          {
            key: 'result',
            label: result.is_error ? '结果（错误）' : '结果',
            children: <ToolResultBody block={result} />,
          },
        ]
      : []),
  ];
  return (
    <div className="tool-call">
      <Space size={6} wrap className="tool-head">
        <ToolOutlined />
        <Typography.Text code>{name}</Typography.Text>
        <RiskTag risk={activity?.risk} />
        {pm && (
          <Tag color={pm.color} icon={pm.icon}>
            {pm.label}
          </Tag>
        )}
        {activity?.durationMs !== undefined && <span className="muted small">{activity.durationMs} ms</span>}
      </Space>
      <Collapse size="small" ghost items={items} />
    </div>
  );
}

function isToolResultOnly(m: Message): boolean {
  return m.role === 'user' && m.content.length > 0 && m.content.every((b) => b.type === 'tool_result');
}

interface ListProps {
  messages: Message[];
  /** tool_use_id → ledger activity (matched by order within a turn). */
  activities: Map<string, ToolActivity>;
  results: Map<string, Block>;
}

export function MessageItem({ m, activities, results }: { m: Message; activities: ListProps['activities']; results: ListProps['results'] }) {
  if (isToolResultOnly(m)) return null;
  const isUser = m.role === 'user';
  const texts = m.content.filter((b) => b.type === 'text' && b.text);
  const uses = m.content.filter((b) => b.type === 'tool_use');
  const pending = m.id.startsWith('local-');
  return (
    <div className={`msg ${isUser ? 'msg-user' : 'msg-assistant'}`}>
      <div className="msg-avatar">{isUser ? <UserOutlined /> : <RobotOutlined />}</div>
      <div className="msg-body">
        {texts.map((b, i) =>
          isUser ? (
            <div key={i} className="bubble pre-wrap">
              {b.text}
              {pending && <Spin size="small" style={{ marginLeft: 8 }} />}
            </div>
          ) : (
            <div key={i} className="assistant-text">
              <Markdown text={b.text ?? ''} />
            </div>
          ),
        )}
        {uses.map((b) => (
          <ToolCallView key={b.tool_use_id} use={b} result={b.tool_use_id ? results.get(b.tool_use_id) : undefined} activity={b.tool_use_id ? activities.get(b.tool_use_id) : undefined} />
        ))}
      </div>
    </div>
  );
}

/**
 * Pairs tool_use blocks with ledger activities. Events carry the ledger id,
 * not the model's tool_use_id, so they are matched by tool name in call order
 * within each turn.
 */
export function matchActivities(messages: Message[], tools: ToolActivity[]): {
  activities: Map<string, ToolActivity>;
  results: Map<string, Block>;
  unmatched: ToolActivity[];
} {
  const activities = new Map<string, ToolActivity>();
  const results = new Map<string, Block>();
  const used = new Set<string>();
  const byTurn = new Map<string, ToolActivity[]>();
  for (const t of tools) {
    const l = byTurn.get(t.turnId) ?? [];
    l.push(t);
    byTurn.set(t.turnId, l);
  }
  for (const m of messages) {
    for (const b of m.content) {
      if (b.type === 'tool_result' && b.tool_use_id) results.set(b.tool_use_id, b);
      if (b.type !== 'tool_use' || !b.tool_use_id) continue;
      const cand = (byTurn.get(m.turn_id) ?? []).find((t) => !used.has(t.id) && t.tool === b.name);
      if (cand) {
        used.add(cand.id);
        activities.set(b.tool_use_id, cand);
      }
    }
  }
  return { activities, results, unmatched: tools.filter((t) => !used.has(t.id)) };
}
