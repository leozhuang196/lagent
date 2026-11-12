import { useEffect, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import {
  Alert,
  Button,
  Descriptions,
  Input,
  Space,
  Table,
  Tabs,
  Tag,
  Typography,
  message,
  type TableColumnsType,
} from 'antd';
import { SafetyCertificateOutlined } from '@ant-design/icons';
import { useMutation } from '@tanstack/react-query';
import {
  errorText,
  platformApi,
  type BreakGlassSession,
  type Message,
  type Pending,
  type Timeline,
  type Turn,
} from '../../api';
import { Loading } from '../../components/States';
import { fmtTime } from '../../lib/format';
import { pendingKindLabel, turnStateMeta } from '../../lib/labels';
import { TurnStateTag } from '../../components/Tags';
import { useAuth } from '../../auth/AuthContext';

const MIN_REASON = 8;

/**
 * Break-glass reads (§11): platform admins can read any tenant's session or
 * turn, but a written reason is mandatory and the read is audited in the
 * owning tenant's log before anything is returned.
 */
export function BreakGlassPage() {
  const { hasRole } = useAuth();
  const [sp, setSp] = useSearchParams();
  const [turnID, setTurnID] = useState(sp.get('turn') ?? '');
  const [sessionID, setSessionID] = useState(sp.get('session') ?? '');
  const [reason, setReason] = useState('');
  const [session, setSession] = useState<BreakGlassSession | null>(null);
  const [timeline, setTimeline] = useState<Timeline | null>(null);

  useEffect(() => {
    const next = new URLSearchParams();
    if (turnID.trim()) next.set('turn', turnID.trim());
    if (sessionID.trim()) next.set('session', sessionID.trim());
    if (next.toString() !== sp.toString()) setSp(next, { replace: true });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [turnID, sessionID]);

  const whyOK = reason.trim().length >= MIN_REASON;

  const readSession = useMutation({
    mutationFn: () => platformApi.breakGlassSession(sessionID.trim(), reason.trim()),
    onSuccess: (v) => {
      setSession(v);
      setTimeline(null);
      void message.success('已读取会话；本次访问已记入该租户的审计日志');
    },
    onError: (e) => void message.error(errorText(e)),
  });
  const readTurn = useMutation({
    mutationFn: () => platformApi.breakGlassTurn(turnID.trim(), reason.trim()),
    onSuccess: (v) => {
      setTimeline(v);
      setSession(null);
      void message.success('已读取轮次；本次访问已记入该租户的审计日志');
    },
    onError: (e) => void message.error(errorText(e)),
  });

  if (!hasRole('platform_admin')) {
    return <Alert type="error" showIcon message="只有平台管理员可以使用跨租户排障。" />;
  }

  const busy = readSession.isPending || readTurn.isPending;

  return (
    <div>
      <Alert
        type="warning"
        showIcon
        icon={<SafetyCertificateOutlined />}
        message="跨租户读取需要理由，并且会在对方租户的审计日志里留下记录。"
        description="请只读取排障必需的内容，并在理由中写清故障或工单。"
        style={{ marginBottom: 12 }}
      />
      <div className="page-card">
        <Space direction="vertical" size={12} style={{ width: '100%' }}>
          <Space wrap>
            <div>
              <div className="muted small">会话 ID</div>
              <Input style={{ width: 320 }} value={sessionID} onChange={(e) => setSessionID(e.target.value)} placeholder="session id" />
            </div>
            <Button
              type="primary"
              disabled={!sessionID.trim() || !whyOK}
              loading={readSession.isPending}
              onClick={() => readSession.mutate()}
            >
              读取会话
            </Button>
          </Space>
          <Space wrap>
            <div>
              <div className="muted small">轮次 ID</div>
              <Input style={{ width: 320 }} value={turnID} onChange={(e) => setTurnID(e.target.value)} placeholder="turn id" />
            </div>
            <Button disabled={!turnID.trim() || !whyOK} loading={readTurn.isPending} onClick={() => readTurn.mutate()}>
              读取轮次
            </Button>
          </Space>
          <div>
            <div className="muted small">理由（至少 {MIN_REASON} 个字符）</div>
            <Input.TextArea
              rows={2}
              style={{ maxWidth: 640 }}
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              placeholder="例如：用户反馈 2026-10-20 的周报任务重复执行，工单 #1234"
              status={reason && !whyOK ? 'error' : undefined}
            />
          </div>
          {busy && <Loading />}
        </Space>
      </div>

      {session && <SessionView data={session} />}
      {timeline && <TurnView tl={timeline} />}
    </div>
  );
}

function SessionView({ data }: { data: BreakGlassSession }) {
  const s = data.session;
  const messages = data.messages ?? [];
  const turns = data.turns ?? [];
  const pendings = data.pendings ?? [];

  return (
    <div className="page-card" style={{ marginTop: 12 }}>
      <div className="page-toolbar">
        <Typography.Text strong>{s.title || '未命名会话'}</Typography.Text>
        <Tag color="purple">租户 {s.tenant_id}</Tag>
        <Tag>
          角色 {s.role_key} v{s.role_ver}
        </Tag>
        <Tag>{s.status}</Tag>
        {s.debug && <Tag color="purple">调试</Tag>}
      </div>
      <Descriptions size="small" column={4} bordered style={{ marginBottom: 12 }}>
        <Descriptions.Item label="会话 ID" span={2}>
          <Typography.Text code copyable className="small">
            {s.id}
          </Typography.Text>
        </Descriptions.Item>
        <Descriptions.Item label="来源">{s.origin}</Descriptions.Item>
        <Descriptions.Item label="权限模式">{s.permission_mode}</Descriptions.Item>
        <Descriptions.Item label="创建者">
          {s.created_by?.name || s.created_by?.id}（{s.created_by?.kind}）
        </Descriptions.Item>
        <Descriptions.Item label="创建时间">{fmtTime(s.created_at)}</Descriptions.Item>
        <Descriptions.Item label="最后更新">{fmtTime(s.updated_at)}</Descriptions.Item>
        <Descriptions.Item label="事件序号">{s.last_seq}</Descriptions.Item>
      </Descriptions>

      <Tabs
        items={[
          {
            key: 'messages',
            label: `消息（${messages.length}）`,
            children: <MessageTable messages={messages} />,
          },
          {
            key: 'turns',
            label: `轮次（${turns.length}）`,
            children: <TurnTable turns={turns} />,
          },
          {
            key: 'pendings',
            label: `待办（${pendings.length}）`,
            children: <PendingTable pendings={pendings} />,
          },
        ]}
      />
    </div>
  );
}

function MessageTable({ messages }: { messages: Message[] }) {
  const cols: TableColumnsType<Message> = [
    { title: 'seq', dataIndex: 'seq', width: 70 },
    { title: '角色', dataIndex: 'role', width: 90 },
    {
      title: '内容',
      render: (_, m) => (
        <Space direction="vertical" size={2} style={{ width: '100%' }}>
          {m.content.map((b, i) => (
            <div key={i} className="small">
              <Tag>{b.type}</Tag>
              {b.name && <Typography.Text code>{b.name}</Typography.Text>}
              {b.text && (
                <span className="pre-wrap" style={{ wordBreak: 'break-word' }}>
                  {b.text.slice(0, 400)}
                  {b.text.length > 400 ? '…' : ''}
                </span>
              )}
            </div>
          ))}
        </Space>
      ),
    },
    { title: '时间', width: 175, render: (_, m) => <span className="small">{fmtTime(m.created_at)}</span> },
  ];
  return <Table rowKey="id" size="small" columns={cols} dataSource={messages} pagination={{ pageSize: 20 }} />;
}

function TurnTable({ turns }: { turns: Turn[] }) {
  const cols: TableColumnsType<Turn> = [
    { title: '轮次', dataIndex: 'id', width: 110, render: (v: string) => <Typography.Text code className="small">{v.slice(0, 10)}…</Typography.Text> },
    { title: '触发', dataIndex: 'trigger', width: 90 },
    { title: '状态', dataIndex: 'status', width: 130, render: (s: Turn['status']) => <TurnStateTag state={s} /> },
    { title: '模型', dataIndex: 'model', width: 170, render: (v?: string) => <span className="small">{v || '—'}</span> },
    { title: '迭代', dataIndex: 'iterations', width: 70 },
    {
      title: 'Token',
      width: 130,
      render: (_, t) => (
        <span className="small">
          {t.usage.input_tokens} / {t.usage.output_tokens}
        </span>
      ),
    },
    { title: '错误', render: (_, t) => (t.error ? <Typography.Text type="danger">{t.error.code}</Typography.Text> : '—') },
    { title: '结束', width: 175, render: (_, t) => <span className="small">{fmtTime(t.ended_at)}</span> },
  ];
  return <Table rowKey="id" size="small" columns={cols} dataSource={turns} pagination={{ pageSize: 20 }} />;
}

function PendingTable({ pendings }: { pendings: Pending[] }) {
  const cols: TableColumnsType<Pending> = [
    { title: '类型', dataIndex: 'kind', width: 110, render: (k: Pending['kind']) => pendingKindLabel[k] ?? k },
    { title: '标题', dataIndex: 'title' },
    { title: '状态', dataIndex: 'status', width: 90 },
    { title: '决定', width: 100, render: (_, p) => p.resolution?.decision ?? '—' },
    { title: '处理时间', width: 175, render: (_, p) => <span className="small">{fmtTime(p.resolution?.at)}</span> },
  ];
  return <Table rowKey="id" size="small" columns={cols} dataSource={pendings} pagination={{ pageSize: 20 }} />;
}

function TurnView({ tl }: { tl: Timeline }) {
  const t = tl.turn;
  const entries = useMemo(() => tl.entries ?? [], [tl.entries]);
  return (
    <div className="page-card" style={{ marginTop: 12 }}>
      <div className="page-toolbar">
        <Typography.Text strong>轮次 {t.id.slice(0, 12)}…</Typography.Text>
        <Tag color={turnStateMeta[t.status]?.color}>{turnStateMeta[t.status]?.label ?? t.status}</Tag>
        <Tag color="purple">租户 {t.tenant_id}</Tag>
        <Tag>
          角色 {t.role_key} v{t.role_ver}
        </Tag>
        {t.model && <Tag>{t.model}</Tag>}
      </div>
      {t.error && <Alert type="error" showIcon message={`${t.error.message}（${t.error.code}）`} style={{ marginBottom: 12 }} />}
      <Descriptions size="small" column={4} bordered style={{ marginBottom: 12 }}>
        <Descriptions.Item label="迭代">{t.iterations}</Descriptions.Item>
        <Descriptions.Item label="模型调用">{tl.totals.llm_calls}</Descriptions.Item>
        <Descriptions.Item label="工具调用">{tl.totals.tool_calls}</Descriptions.Item>
        <Descriptions.Item label="待办">{tl.totals.pendings}</Descriptions.Item>
        <Descriptions.Item label="输入 Token">{tl.totals.usage.input_tokens}</Descriptions.Item>
        <Descriptions.Item label="输出 Token">{tl.totals.usage.output_tokens}</Descriptions.Item>
        <Descriptions.Item label="开始">{fmtTime(t.started_at)}</Descriptions.Item>
        <Descriptions.Item label="结束">{fmtTime(t.ended_at)}</Descriptions.Item>
      </Descriptions>
      <div className="muted small" style={{ marginBottom: 8 }}>
        时间线（{entries.length} 条）。完整调用细节请在对应租户内使用轮次调试器查看。
      </div>
      {entries.map((e, i) => (
        <div className="timeline-row" key={`${e.kind}-${e.id ?? i}`}>
          <span className="timeline-time">{fmtTime(e.at).slice(11)}</span>
          <Tag style={{ minWidth: 76, textAlign: 'center' }}>{e.kind}</Tag>
          <span style={{ flex: 1, minWidth: 0, wordBreak: 'break-word' }}>{e.title}</span>
          {e.status && <Tag>{e.status}</Tag>}
        </div>
      ))}
      {t.final_text && (
        <div style={{ marginTop: 12 }}>
          <div className="muted small">最终回复</div>
          <div className="pre-wrap part-text">{t.final_text}</div>
        </div>
      )}
    </div>
  );
}