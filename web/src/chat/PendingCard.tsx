import { useState } from 'react';
import { Alert, Button, Card, Descriptions, Input, Space, Tag, Typography, message } from 'antd';
import { CheckOutlined, CloseOutlined, EditOutlined, QuestionCircleOutlined, SafetyOutlined } from '@ant-design/icons';
import { errorText, pendingsApi, type Decision, type Pending } from '../api';
import { JsonBlock } from '../components/JsonBlock';
import { RiskTag } from '../components/Tags';
import { decisionLabel, pendingKindLabel } from '../lib/labels';
import { fromNow } from '../lib/format';
import type { ResolvedNote } from './useConversation';

interface Props {
  pending: Pending;
  resolved?: ResolvedNote;
  readOnly?: boolean;
  onResolved?: (id: string, note: ResolvedNote) => void;
  compact?: boolean;
}

/** One human-in-the-loop item. Every channel shows a view of the same record. */
export function PendingCard({ pending: p, resolved, readOnly, onResolved, compact }: Props) {
  const [busy, setBusy] = useState<Decision | null>(null);
  const [mode, setMode] = useState<'idle' | 'redirect'>('idle');
  const [text, setText] = useState('');

  const done = resolved ?? (p.status === 'resolved' && p.resolution ? { decision: p.resolution.decision, via: p.resolution.via } : undefined);

  const resolve = async (decision: Decision, answer?: string) => {
    setBusy(decision);
    try {
      const r = await pendingsApi.resolve(p.id, { decision, answer });
      const note: ResolvedNote = r.already_resolved
        ? {
            decision: r.pending.resolution?.decision ?? decision,
            via: r.pending.resolution?.via,
            alreadyResolved: true,
          }
        : { decision, via: 'web' };
      if (r.already_resolved) void message.info('已由其他通道处理');
      onResolved?.(p.id, note);
    } catch (e) {
      void message.error(errorText(e));
    } finally {
      setBusy(null);
    }
  };

  const disabled = readOnly || !!busy;

  const header = (
    <Space size={6} wrap>
      {p.kind === 'question' ? <QuestionCircleOutlined /> : <SafetyOutlined />}
      <span>{pendingKindLabel[p.kind] ?? p.kind}</span>
      <Typography.Text strong>{p.title}</Typography.Text>
      {p.kind === 'approval' && <RiskTag risk={p.risk} />}
    </Space>
  );

  let body: JSX.Element | null = null;
  let actions: JSX.Element | null = null;

  switch (p.kind) {
    case 'approval':
      body = (
        <>
          {p.detail && <Typography.Paragraph className="pre-wrap">{p.detail}</Typography.Paragraph>}
          <Descriptions size="small" column={1} bordered className="pending-desc">
            <Descriptions.Item label="工具">
              <Typography.Text code>{p.tool_name ?? '—'}</Typography.Text>
            </Descriptions.Item>
            <Descriptions.Item label="触发来源">
              {p.trigger_source ? (
                <Typography.Text className="pre-wrap">{p.trigger_source}</Typography.Text>
              ) : (
                <span className="muted">未记录</span>
              )}
            </Descriptions.Item>
            <Descriptions.Item label="参数（已脱敏）">
              <JsonBlock value={p.args} maxHeight={compact ? 160 : 240} />
            </Descriptions.Item>
          </Descriptions>
        </>
      );
      actions =
        mode === 'redirect' ? (
          <Space direction="vertical" style={{ width: '100%' }}>
            <Input.TextArea
              autoFocus
              autoSize={{ minRows: 2, maxRows: 6 }}
              placeholder="拒绝此操作，并告诉助手接下来应该怎么做"
              value={text}
              onChange={(e) => setText(e.target.value)}
            />
            <Space>
              <Button
                type="primary"
                disabled={disabled || !text.trim()}
                loading={busy === 'answer'}
                onClick={() => void resolve('answer', text.trim())}
              >
                拒绝并发送新指示
              </Button>
              <Button onClick={() => setMode('idle')} disabled={!!busy}>
                取消
              </Button>
            </Space>
          </Space>
        ) : (
          <Space wrap>
            <Button type="primary" icon={<CheckOutlined />} disabled={disabled} loading={busy === 'approve'} onClick={() => void resolve('approve')}>
              批准
            </Button>
            <Button danger icon={<CloseOutlined />} disabled={disabled} loading={busy === 'reject'} onClick={() => void resolve('reject')}>
              拒绝
            </Button>
            <Button icon={<EditOutlined />} disabled={disabled} onClick={() => setMode('redirect')}>
              拒绝并给出新指示
            </Button>
          </Space>
        );
      break;
    case 'question':
      body = (
        <>
          <Typography.Paragraph className="pre-wrap">{p.question || p.detail}</Typography.Paragraph>
        </>
      );
      actions = (
        <Space direction="vertical" style={{ width: '100%' }}>
          {p.options && p.options.length > 0 && (
            <Space wrap>
              {p.options.map((o) => (
                <Button key={o} disabled={disabled} loading={busy === 'answer' && text === o} onClick={() => { setText(o); void resolve('answer', o); }}>
                  {o}
                </Button>
              ))}
            </Space>
          )}
          <Space.Compact style={{ width: '100%' }}>
            <Input
              placeholder={p.options?.length ? '或输入其他回答' : '输入回答'}
              value={text}
              onChange={(e) => setText(e.target.value)}
              onPressEnter={() => text.trim() && void resolve('answer', text.trim())}
              disabled={disabled}
            />
            <Button type="primary" disabled={disabled || !text.trim()} loading={busy === 'answer'} onClick={() => void resolve('answer', text.trim())}>
              回答
            </Button>
          </Space.Compact>
        </Space>
      );
      break;
    case 'budget':
      body = <Typography.Paragraph className="pre-wrap">{p.detail || '本轮已达到预算上限，是否继续？'}</Typography.Paragraph>;
      actions = (
        <Space>
          <Button type="primary" disabled={disabled} loading={busy === 'continue'} onClick={() => void resolve('continue')}>
            继续
          </Button>
          <Button disabled={disabled} loading={busy === 'stop'} onClick={() => void resolve('stop')}>
            停止
          </Button>
        </Space>
      );
      break;
    case 'outcome_unknown':
      body = (
        <>
          <Alert
            type="warning"
            showIcon
            message="一个有副作用的操作在执行中断，平台无法确定它是否已生效"
            description="请到目标系统核实后选择。选择「未发生」会允许重试。"
            style={{ marginBottom: 8 }}
          />
          {p.detail && <Typography.Paragraph className="pre-wrap">{p.detail}</Typography.Paragraph>}
          {p.tool_name && (
            <Descriptions size="small" column={1} bordered className="pending-desc">
              <Descriptions.Item label="工具">
                <Typography.Text code>{p.tool_name}</Typography.Text> <RiskTag risk={p.risk} />
              </Descriptions.Item>
              <Descriptions.Item label="参数（已脱敏）">
                <JsonBlock value={p.args} maxHeight={160} />
              </Descriptions.Item>
            </Descriptions>
          )}
        </>
      );
      actions = (
        <Space wrap>
          <Button disabled={disabled} loading={busy === 'happened'} onClick={() => void resolve('happened')}>
            已发生
          </Button>
          <Button disabled={disabled} loading={busy === 'not_happened'} onClick={() => void resolve('not_happened')}>
            未发生（可重试）
          </Button>
          <Button danger disabled={disabled} loading={busy === 'stop'} onClick={() => void resolve('stop')}>
            停止
          </Button>
        </Space>
      );
      break;
    default:
      body = (
        <Typography.Paragraph type="secondary">
          {p.kind === 'subtasks' ? '等待子任务完成，完成后自动继续。' : `等待外部任务完成${p.external_ref ? `（${p.external_ref}）` : ''}。`}
        </Typography.Paragraph>
      );
  }

  return (
    <Card size="small" className={`pending-card ${done ? 'is-done' : ''}`} title={header} extra={<span className="muted">{fromNow(p.created_at)}</span>}>
      {body}
      {done ? (
        <Space>
          <Tag color={done.decision === 'approve' || done.decision === 'continue' ? 'green' : 'default'}>
            {decisionLabel[done.decision] ?? done.decision}
          </Tag>
          {done.alreadyResolved || (done.via && done.via !== 'web') ? (
            <Typography.Text type="secondary">已由其他通道处理{done.via ? `（${done.via}）` : ''}</Typography.Text>
          ) : (
            done.via && <Typography.Text type="secondary">经由 {done.via}</Typography.Text>
          )}
        </Space>
      ) : readOnly ? (
        <Typography.Text type="secondary">只读视图：请在待办中心或会话中处理</Typography.Text>
      ) : (
        actions
      )}
    </Card>
  );
}
