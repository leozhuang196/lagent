import { useEffect, useMemo, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import {
  Alert,
  Button,
  Collapse,
  Descriptions,
  Input,
  Select,
  Space,
  Table,
  Tag,
  Tooltip,
  Typography,
  type TableColumnsType,
} from 'antd';
import { BugOutlined, ReloadOutlined, SearchOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import {
  debugApi,
  errorText,
  sessionsApi,
  type LLMCall,
  type StoredEvent,
  type Timeline,
  type TimelineEntry,
  type ToolCall,
} from '../../api';
import { Loading } from '../../components/States';
import { fmtTime, prettyJson } from '../../lib/format';
import { riskMeta, turnStateMeta } from '../../lib/labels';
import { JsonBlock } from '../../components/JsonBlock';

const kindMeta: Record<string, { label: string; color: string }> = {
  llm_call: { label: '模型调用', color: 'blue' },
  tool_call: { label: '工具调用', color: 'purple' },
  pending: { label: '待办', color: 'orange' },
  event: { label: '事件', color: 'default' },
};

/** Turn debugger: timeline, LLM calls, tools, pendings and artifacts of one turn. */
export function TurnDebuggerPage() {
  const [sp, setSp] = useSearchParams();
  const sessionID = sp.get('session') ?? '';
  const turnID = sp.get('turn') ?? '';
  const [sessionInput, setSessionInput] = useState(sessionID);
  const [turnInput, setTurnInput] = useState(turnID);

  // Keep the URL in step with the inputs so a debug view is linkable.
  useEffect(() => {
    const next = new URLSearchParams();
    if (sessionInput.trim()) next.set('session', sessionInput.trim());
    if (turnInput.trim()) next.set('turn', turnInput.trim());
    if (next.toString() !== sp.toString()) setSp(next, { replace: true });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sessionInput, turnInput]);

  const snap = useQuery({
    queryKey: ['debug', 'session', sessionInput],
    queryFn: () => sessionsApi.snapshot(sessionInput.trim()),
    enabled: !!sessionInput.trim(),
  });

  // A session's turn ids come from its messages; the API has no turn list.
  const turns = useMemo(() => {
    const seen = new Map<string, { id: string; firstText?: string; at: string }>();
    for (const m of snap.data?.messages ?? []) {
      if (!m.turn_id || seen.has(m.turn_id)) continue;
      const text = m.content.find((b) => b.type === 'text' && b.text)?.text;
      seen.set(m.turn_id, { id: m.turn_id, firstText: text, at: m.created_at });
    }
    if (snap.data?.active_turn && !seen.has(snap.data.active_turn.id)) {
      seen.set(snap.data.active_turn.id, { id: snap.data.active_turn.id, at: snap.data.active_turn.created_at });
    }
    return [...seen.values()];
  }, [snap.data]);

  const active = turnInput.trim() || (turns.length ? turns[turns.length - 1].id : '');
  const timeline = useQuery({
    queryKey: ['debug', 'timeline', active],
    queryFn: () => debugApi.timeline(active),
    enabled: !!active,
  });

  return (
    <div className="page-card">
      <div className="page-toolbar" style={{ flexWrap: 'wrap', gap: 8 }}>
        <Typography.Text strong>轮次调试器</Typography.Text>
        <span className="muted small">按会话或轮次 ID 定位。会话链接会带上两者。</span>
        <div style={{ flex: 1 }} />
        <Input
          allowClear
          prefix={<SearchOutlined />}
          placeholder="会话 ID"
          style={{ width: 240 }}
          value={sessionInput}
          onChange={(e) => setSessionInput(e.target.value)}
        />
        <Input
          allowClear
          placeholder="轮次 ID"
          style={{ width: 240 }}
          value={turnInput}
          onChange={(e) => setTurnInput(e.target.value)}
        />
        {turns.length > 0 && (
          <Select
            style={{ width: 240 }}
            placeholder="选择该会话的轮次"
            value={active || undefined}
            onChange={(v) => setTurnInput(v)}
            options={turns.map((t, i) => ({
              value: t.id,
              label: `#${i + 1} ${t.firstText ? t.firstText.slice(0, 18) : t.id.slice(0, 10)}`,
            }))}
          />
        )}
        <Tooltip title="重新加载">
          <Button icon={<ReloadOutlined />} onClick={() => void timeline.refetch()} aria-label="重新加载" />
        </Tooltip>
      </div>

      {snap.isError && <Alert type="error" showIcon message={`会话读取失败：${errorText(snap.error)}`} style={{ marginBottom: 12 }} />}

      {snap.data && (
        <Descriptions size="small" column={4} bordered style={{ marginBottom: 12 }}>
          <Descriptions.Item label="标题" span={2}>
            {snap.data.session.title || '未命名会话'}
          </Descriptions.Item>
          <Descriptions.Item label="角色">
            {snap.data.session.role_key} v{snap.data.session.role_ver}
          </Descriptions.Item>
          <Descriptions.Item label="状态">{snap.data.session.status}</Descriptions.Item>
          <Descriptions.Item label="来源" span={2}>
            {snap.data.session.origin}
            {snap.data.session.debug && <Tag color="purple" style={{ marginLeft: 6 }}>调试</Tag>}
          </Descriptions.Item>
          <Descriptions.Item label="权限模式">{snap.data.session.permission_mode}</Descriptions.Item>
          <Descriptions.Item label="会话 ID">
            <Typography.Text code copyable className="small">
              {snap.data.session.id}
            </Typography.Text>
          </Descriptions.Item>
        </Descriptions>
      )}

      {!active ? (
        <Alert type="info" showIcon message="输入轮次 ID，或先输入会话 ID 再选择轮次。" />
      ) : timeline.isLoading ? (
        <Loading />
      ) : timeline.isError ? (
        <Alert type="error" showIcon message={errorText(timeline.error)} />
      ) : timeline.data ? (
        <TimelineView tl={timeline.data} turnID={active} sessionID={sessionID || snap.data?.session.id || ''} />
      ) : null}
    </div>
  );
}

function TimelineView({ tl, turnID, sessionID }: { tl: Timeline; turnID: string; sessionID: string }) {
  const t = tl.turn;
  const entries = tl.entries ?? [];
  const toolStatus = Object.entries(tl.totals.tool_status ?? {});
  const artifacts = useMemo(() => {
    const out: { id: string; name?: string }[] = [];
    for (const e of tl.events ?? []) {
      const d = e.data as { artifact?: { id: string; name: string } } | undefined;
      if (e.type === 'artifact.created' && d?.artifact) out.push(d.artifact);
    }
    return out;
  }, [tl.events]);

  return (
    <div>
      <Space size={12} wrap style={{ marginBottom: 12 }}>
        <Tag color={turnStateMeta[t.status]?.color}>{turnStateMeta[t.status]?.label ?? t.status}</Tag>
        <span className="small muted">
          {t.iterations} 轮迭代 · {tl.totals.llm_calls} 次模型调用 · {tl.totals.tool_calls} 次工具调用
        </span>
        <span className="small muted">
          tokens 输入 {tl.totals.usage.input_tokens} / 输出 {tl.totals.usage.output_tokens} / 缓存读{' '}
          {tl.totals.usage.cache_read_tokens}
        </span>
        {toolStatus.map(([s, n]) => (
          <Tag key={s}>
            {s} × {n}
          </Tag>
        ))}
        {sessionID && (
          <Link to={`/c/${sessionID}`}>
            <Button size="small" icon={<BugOutlined />}>
              打开会话
            </Button>
          </Link>
        )}
      </Space>

      {t.error && <Alert type="error" showIcon message={`${t.error.message}（${t.error.code}）`} style={{ marginBottom: 12 }} />}
      {t.final_text && (
        <Collapse
          size="small"
          style={{ marginBottom: 12 }}
          items={[{ key: 'final', label: '最终回复', children: <div className="pre-wrap part-text">{t.final_text}</div> }]}
        />
      )}

      <Collapse
        defaultActiveKey={['timeline', 'llm', 'tools']}
        items={[
          {
            key: 'timeline',
            label: `时间线（${entries.length}）`,
            children:
              entries.length === 0 ? (
                <span className="muted">没有记录。</span>
              ) : (
                <div>
                  {entries.map((e, i) => (
                    <TimelineRow key={`${e.kind}-${e.id ?? i}`} e={e} />
                  ))}
                </div>
              ),
          },
          {
            key: 'llm',
            label: `模型调用（${tl.totals.llm_calls}）`,
            children: <LLMCallTable calls={tl.llm_calls ?? []} />,
          },
          {
            key: 'tools',
            label: `工具调用（${tl.totals.tool_calls}）`,
            children: <ToolCallTable calls={tl.tool_calls ?? []} />,
          },
          {
            key: 'pendings',
            label: `待办（${tl.pendings?.length ?? 0}）`,
            children: (tl.pendings ?? []).length === 0 ? (
              <span className="muted">该轮次没有待办。</span>
            ) : (
              <Table
                rowKey="id"
                size="small"
                pagination={false}
                dataSource={tl.pendings ?? []}
                columns={[
                  { title: '类型', dataIndex: 'kind', width: 110 },
                  { title: '标题', dataIndex: 'title' },
                  { title: '状态', dataIndex: 'status', width: 90 },
                  { title: '决定', render: (_, p) => p.resolution?.decision ?? '—', width: 110 },
                  { title: '处理时间', width: 175, render: (_, p) => <span className="small">{fmtTime(p.resolution?.at)}</span> },
                ]}
              />
            ),
          },
          {
            key: 'events',
            label: `事件（${tl.events?.length ?? 0}）`,
            children: <EventTable events={tl.events ?? []} />,
          },
          ...(artifacts.length
            ? [{ key: 'artifacts', label: `交付物（${artifacts.length}）`, children: <ArtifactList items={artifacts} /> }]
            : []),
        ]}
      />

      <div className="muted small" style={{ marginTop: 12 }}>
        轮次 {turnID} · 开始 {fmtTime(t.started_at)} · 结束 {fmtTime(t.ended_at)}
        {tl.totals.duration_ms !== undefined && ` · 耗时 ${(tl.totals.duration_ms / 1000).toFixed(1)}s`}
      </div>
    </div>
  );
}

function TimelineRow({ e }: { e: TimelineEntry }) {
  const m = kindMeta[e.kind] ?? { label: e.kind, color: 'default' };
  return (
    <div className="timeline-row">
      <span className="timeline-time">{fmtTime(e.at).slice(11)}</span>
      <Tag color={m.color} style={{ minWidth: 76, textAlign: 'center' }}>
        {m.label}
      </Tag>
      <span style={{ flex: 1, minWidth: 0, wordBreak: 'break-word' }}>{e.title}</span>
      {e.status && <Tag>{e.status}</Tag>}
      {e.decision && <Tag color="orange">{e.decision}</Tag>}
      {e.duration_ms !== undefined && <span className="small muted">{(e.duration_ms / 1000).toFixed(2)}s</span>}
    </div>
  );
}

function LLMCallTable({ calls }: { calls: LLMCall[] }) {
  const columns: TableColumnsType<LLMCall> = [
    { title: '#', dataIndex: 'iteration', width: 56 },
    { title: '模型', dataIndex: 'model', width: 190, render: (v: string) => <span className="small">{v}</span> },
    {
      title: 'Token（入/出/缓存）',
      width: 200,
      render: (_, c) => (
        <span className="small">
          {c.usage.input_tokens} / {c.usage.output_tokens} / {c.usage.cache_read_tokens}
        </span>
      ),
    },
    { title: '耗时', dataIndex: 'latency_ms', width: 90, render: (v: number) => `${(v / 1000).toFixed(2)}s` },
    { title: '错误', dataIndex: 'error', render: (v?: string) => (v ? <Typography.Text type="danger">{v}</Typography.Text> : <span className="muted">—</span>) },
  ];
  return (
    <Table
      rowKey="id"
      size="small"
      columns={columns}
      dataSource={calls}
      pagination={false}
      expandable={{
        expandedRowRender: (c) => (
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
            <div>
              <div className="muted small">请求</div>
              <JsonBlock value={c.request} maxHeight={360} />
            </div>
            <div>
              <div className="muted small">响应</div>
              <JsonBlock value={c.response} maxHeight={360} />
            </div>
          </div>
        ),
      }}
    />
  );
}

function ToolCallTable({ calls }: { calls: ToolCall[] }) {
  const columns: TableColumnsType<ToolCall> = [
    { title: '工具', dataIndex: 'tool', width: 190, render: (v: string) => <Typography.Text code>{v}</Typography.Text> },
    {
      title: '风险',
      dataIndex: 'risk',
      width: 100,
      render: (r: ToolCall['risk']) => <Tag color={riskMeta[r]?.color}>{riskMeta[r]?.label ?? r}</Tag>,
    },
    { title: '状态', dataIndex: 'status', width: 130 },
    { title: '决定', dataIndex: 'decision', width: 100, render: (v?: string) => v ?? '—' },
    { title: '耗时', width: 90, render: (_, c) => (c.duration_ms !== undefined ? `${(c.duration_ms / 1000).toFixed(2)}s` : '—') },
    { title: '错误', width: 80, render: (_, c) => (c.is_error ? <Tag color="red">是</Tag> : <span className="muted">—</span>) },
  ];
  return (
    <Table
      rowKey="id"
      size="small"
      columns={columns}
      dataSource={calls}
      pagination={false}
      expandable={{
        expandedRowRender: (c) => (
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
            <div>
              <div className="muted small">输入</div>
              <JsonBlock value={c.input} maxHeight={280} />
            </div>
            <div>
              <div className="muted small">输出</div>
              <pre className="code" style={{ maxHeight: 280 }}>
                {c.output ?? '（无输出）'}
              </pre>
            </div>
          </div>
        ),
      }}
    />
  );
}

function EventTable({ events }: { events: StoredEvent[] }) {
  if (events.length === 0) return <span className="muted">没有事件。</span>;
  return (
    <Table
      rowKey="seq"
      size="small"
      pagination={{ pageSize: 50 }}
      dataSource={events}
      columns={[
        { title: 'seq', dataIndex: 'seq', width: 80 },
        { title: '类型', dataIndex: 'type', width: 220, render: (v: string) => <Typography.Text code>{v}</Typography.Text> },
        { title: '时间', dataIndex: 'ts', width: 180, render: (v: string) => <span className="small">{fmtTime(v)}</span> },
        {
          title: '数据',
          render: (_, e) => (
            <Typography.Text className="small muted" ellipsis style={{ maxWidth: 620 }}>
              {e.data ? prettyJson(e.data).replace(/\s+/g, ' ').slice(0, 200) : '—'}
            </Typography.Text>
          ),
        },
      ]}
    />
  );
}

function ArtifactList({ items }: { items: { id: string; name?: string }[] }) {
  return (
    <Space direction="vertical">
      {items.map((a) => (
        <Space key={a.id}>
          <Tag>交付物</Tag>
          <span>{a.name ?? a.id}</span>
          <Typography.Text code className="small">
            {a.id}
          </Typography.Text>
        </Space>
      ))}
    </Space>
  );
}