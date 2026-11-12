import { useEffect, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import {
  Alert,
  Button,
  Collapse,
  Descriptions,
  Popconfirm,
  Space,
  Table,
  Tabs,
  Tag,
  Tooltip,
  Typography,
  message,
  type TableColumnsType,
} from 'antd';
import { ArrowLeftOutlined, PauseCircleOutlined, ReloadOutlined, RedoOutlined, StepForwardOutlined } from '@ant-design/icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  errorText,
  openRunStream,
  replayRunEvents,
  workflowsApi,
  type LagentEvent,
  type RunStatus,
  type StepRun,
  type StepStatus,
} from '../../api';
import { Loading } from '../../components/States';
import { fmtTime, prettyJson } from '../../lib/format';
import { isTerminal, runStatusMeta } from '../runMeta';
import { RunParamsCard } from './RunsPage';

const stepMeta: Record<StepStatus, { label: string; color: string }> = {
  pending: { label: '待运行', color: 'default' },
  dispatched: { label: '已调度', color: 'cyan' },
  running: { label: '运行中', color: 'processing' },
  succeeded: { label: '成功', color: 'success' },
  failed: { label: '失败', color: 'error' },
  skipped: { label: '已跳过', color: 'default' },
  cancelled: { label: '已取消', color: 'default' },
  reused: { label: '复用', color: 'purple' },
};

/** One run: frozen inputs, live events, step graph and the continue/rerun controls. */
export function RunDetailPage() {
  const { id = '' } = useParams();
  const qc = useQueryClient();
  const nav = useNavigate();
  const run = useQuery({ queryKey: ['run', id], queryFn: () => workflowsApi.getRun(id), enabled: !!id });
  const [events, setEvents] = useState<LagentEvent[]>([]);

  const status: RunStatus | undefined = run.data?.run.status;
  const live = status !== undefined && !isTerminal(status);

  // Follow the run while it is live; the stream replays its backlog first.
  useEffect(() => {
    if (!live || !id) return;
    setEvents([]);
    const h = openRunStream(id, {
      after: 0,
      onEvent: (ev) => {
        setEvents((prev) => [...prev, ev].slice(-500));
        if (ev.type === 'run.completed' || ev.type === 'step.completed' || ev.type === 'step.reused') {
          void qc.invalidateQueries({ queryKey: ['run', id] });
        }
      },
    });
    return () => h.close();
  }, [live, id, qc]);

  // A finished run gets no live feed, but its log is still buffered; read it
  // once so "事件" is not empty for every run that ended before the page opened.
  useEffect(() => {
    if (live || !id || status === undefined) return;
    const ctrl = new AbortController();
    let cancelled = false;
    replayRunEvents(id, 800, ctrl.signal)
      .then((evs) => {
        if (!cancelled) setEvents(evs.slice(-500));
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
      ctrl.abort();
    };
  }, [live, id, status]);

  const act = useMutation({
    mutationFn: (a: 'continue' | 'rerun' | 'cancel') =>
      a === 'cancel' ? workflowsApi.cancelRun(id) : a === 'rerun' ? workflowsApi.rerun(id) : workflowsApi.continueRun(id),
    onSuccess: (r, a) => {
      if (a === 'cancel') void message.success('已请求取消；已发生的操作不会撤回');
      else {
        void message.success(a === 'rerun' ? '已重新冻结并创建新运行' : '已创建续跑运行');
        nav(`/admin/runs/${r.id}`);
      }
      void qc.invalidateQueries({ queryKey: ['runs'] });
      void qc.invalidateQueries({ queryKey: ['run', id] });
    },
    onError: (e) => void message.error(errorText(e)),
  });

  if (run.isLoading) return <Loading />;
  if (run.isError) return <Alert type="error" showIcon message={errorText(run.error)} />;
  const view = run.data;
  if (!view) return null;
  const r = view.run;
  const steps = view.steps ?? [];
  const counts: Partial<Record<StepStatus, number>> = {};
  for (const s of steps) counts[s.status] = (counts[s.status] ?? 0) + 1;

  const stepColumns: TableColumnsType<StepRun> = [
    { title: '#', dataIndex: 'index', width: 56 },
    {
      title: '步骤',
      dataIndex: 'step_id',
      width: 170,
      render: (v: string, s) => (
        <Space direction="vertical" size={0}>
          <strong>{v}</strong>
          {s.item !== undefined && <span className="small muted">{JSON.stringify(s.item).slice(0, 40)}</span>}
        </Space>
      ),
    },
    {
      title: '状态',
      dataIndex: 'status',
      width: 130,
      render: (s: StepStatus, step) => {
        const m = stepMeta[s] ?? { label: s, color: 'default' };
        return (
          <Space size={4}>
            <Tag color={m.color}>{m.label}</Tag>
            {step.reused_from && (
              <Tooltip title={`复用自 ${step.reused_from}`}>
                <Tag color="purple">复用</Tag>
              </Tooltip>
            )}
          </Space>
        );
      },
    },
    {
      title: '角色',
      width: 130,
      render: (_, s) => (
        <span className="small">
          {s.role_key} v{s.role_ver}
        </span>
      ),
    },
    { title: '尝试', dataIndex: 'attempts', width: 70, render: (n: number, s) => `${n}/${s.max_retries + 1}` },
    {
      title: '会话',
      width: 100,
      render: (_, s) =>
        s.session_id ? (
          <Link to={`/c/${s.session_id}`} onClick={(e) => e.stopPropagation()}>
            打开
          </Link>
        ) : (
          <span className="muted">—</span>
        ),
    },
    {
      title: '耗时',
      width: 90,
      render: (_, s) => {
        if (!s.started_at) return '—';
        const end = s.ended_at ? new Date(s.ended_at).getTime() : Date.now();
        return `${Math.max(0, Math.round((end - new Date(s.started_at).getTime()) / 1000))}s`;
      },
    },
    {
      title: '结果',
      render: (_, s) =>
        s.error ? (
          <Typography.Text type="danger" className="small">
            {s.error.message}（{s.error.code}）
          </Typography.Text>
        ) : s.result?.summary ? (
          <span className="small">{s.result.summary.slice(0, 120)}</span>
        ) : (
          <span className="muted">—</span>
        ),
    },
  ];

  const statusMeta = runStatusMeta[r.status] ?? { label: r.status, color: 'default' };

  return (
    <div className="page-card">
      <div className="page-toolbar" style={{ flexWrap: 'wrap', gap: 8 }}>
        <Link to="/admin/runs">
          <Button size="small" icon={<ArrowLeftOutlined />}>
            返回
          </Button>
        </Link>
        <Typography.Title level={5} style={{ margin: 0 }}>
          {r.def_key} · {r.id.slice(0, 10)}…
        </Typography.Title>
        <Tag color={statusMeta.color}>{statusMeta.label}</Tag>
        <Tag color={r.mode === 'rerun' ? 'orange' : r.mode === 'continue' ? 'blue' : 'default'}>{r.mode}</Tag>
        {r.debug && <Tag color="purple">调试</Tag>}
        {r.origin_run_id && (
          <Link to={`/admin/runs/${r.origin_run_id}`} className="small">
            来源运行 {r.origin_run_id.slice(0, 8)}…
          </Link>
        )}
        <div style={{ flex: 1 }} />
        <Tooltip title="重新加载">
          <Button icon={<ReloadOutlined />} onClick={() => void run.refetch()} aria-label="重新加载" />
        </Tooltip>
        {live && (
          <Popconfirm title="取消这个运行？已发生的操作不会撤回。" okText="取消运行" cancelText="返回" onConfirm={() => act.mutate('cancel')}>
            <Button danger icon={<PauseCircleOutlined />} loading={act.isPending && act.variables === 'cancel'}>
              取消
            </Button>
          </Popconfirm>
        )}
        {isTerminal(r.status) && (
          <>
            <Tooltip title="复用已完成且指纹一致的步骤，只运行剩余部分">
              <Button icon={<StepForwardOutlined />} loading={act.isPending && act.variables === 'continue'} onClick={() => act.mutate('continue')}>
                继续
              </Button>
            </Tooltip>
            <Tooltip title="重新冻结参数并从头运行（不复用）">
              <Button icon={<RedoOutlined />} loading={act.isPending && act.variables === 'rerun'} onClick={() => act.mutate('rerun')}>
                重跑
              </Button>
            </Tooltip>
          </>
        )}
      </div>

      {r.error && <Alert type="error" showIcon message={`${r.error.message}（${r.error.code}）`} style={{ marginBottom: 12 }} />}

      <Tabs
        defaultActiveKey="steps"
        items={[
          {
            key: 'steps',
            label: `步骤（${steps.length}）`,
            children: (
              <>
                <Space size={6} wrap style={{ marginBottom: 8 }}>
                  {(Object.keys(counts) as StepStatus[]).map((s) => (
                    <Tag key={s} color={stepMeta[s]?.color}>
                      {stepMeta[s]?.label ?? s} × {counts[s]}
                    </Tag>
                  ))}
                  {steps.length === 0 && <span className="muted">这一步运行还没有展开步骤。</span>}
                </Space>
                <Table
                  rowKey="id"
                  size="small"
                  columns={stepColumns}
                  dataSource={steps}
                  pagination={false}
                  expandable={{
                    expandedRowRender: (s) => <StepDetail step={s} />,
                  }}
                />
              </>
            ),
          },
          {
            key: 'info',
            label: '运行信息',
            children: (
              <div>
                <Descriptions size="small" column={2} bordered style={{ marginBottom: 12 }}>
                  <Descriptions.Item label="运行 ID" span={2}>
                    <Typography.Text code copyable>
                      {r.id}
                    </Typography.Text>
                  </Descriptions.Item>
                  <Descriptions.Item label="模板">
                    {r.def_key} v{r.def_ver}
                  </Descriptions.Item>
                  <Descriptions.Item label="角色">
                    {r.role_key} v{r.role_ver}
                  </Descriptions.Item>
                  <Descriptions.Item label="权限模式">{r.permission_mode}</Descriptions.Item>
                  <Descriptions.Item label="发起者">
                    {r.started_by?.name || r.started_by?.id}（{r.started_by?.kind}）
                  </Descriptions.Item>
                  <Descriptions.Item label="开始">{fmtTime(r.created_at)}</Descriptions.Item>
                  <Descriptions.Item label="结束">{fmtTime(r.ended_at)}</Descriptions.Item>
                  <Descriptions.Item label="截止">{fmtTime(r.deadline)}</Descriptions.Item>
                  <Descriptions.Item label="调度">{r.schedule_id || '—'}</Descriptions.Item>
                  <Descriptions.Item label="Token">
                    {r.usage.input_tokens} 入 / {r.usage.output_tokens} 出 / {r.usage.cache_read_tokens} 缓存读
                  </Descriptions.Item>
                  <Descriptions.Item label="常设授权">
                    {(r.standing_grants ?? []).length
                      ? (r.standing_grants ?? []).map((g) => `${g.tool}:${g.field}=${g.value}`).join('，')
                      : '—'}
                  </Descriptions.Item>
                </Descriptions>
                <RunParamsCard run={r} />
              </div>
            ),
          },
          {
            key: 'events',
            label: `事件（${events.length}）`,
            children: <EventLog events={events} />,
          },
        ]}
      />
    </div>
  );
}

function StepDetail({ step }: { step: StepRun }) {
  return (
    <div>
      <Descriptions size="small" column={4} bordered style={{ marginBottom: 8 }}>
        <Descriptions.Item label="步骤键" span={2}>
          <Typography.Text code className="small">
            {step.step_key}
          </Typography.Text>
        </Descriptions.Item>
        <Descriptions.Item label="失败策略">{step.on_failure || 'abort'}</Descriptions.Item>
        <Descriptions.Item label="外部影响">{step.side_effect ? '是' : '否'}</Descriptions.Item>
        <Descriptions.Item label="依赖">{(step.depends_on ?? []).join('、') || '无'}</Descriptions.Item>
        <Descriptions.Item label="复用自" span={3}>
          {step.reused_from || '—'}
        </Descriptions.Item>
        <Descriptions.Item label="开始">{fmtTime(step.started_at)}</Descriptions.Item>
        <Descriptions.Item label="结束">{fmtTime(step.ended_at)}</Descriptions.Item>
        <Descriptions.Item label="调度于">{fmtTime(step.dispatched_at)}</Descriptions.Item>
        <Descriptions.Item label="最早开始">{fmtTime(step.not_before)}</Descriptions.Item>
      </Descriptions>
      <div className="muted small">步骤输入</div>
      <div className="pre-wrap part-text">{step.input}</div>
      {step.result && (
        <div style={{ marginTop: 8 }}>
          <div className="muted small">结果</div>
          <div className="pre-wrap part-text">{step.result.summary}</div>
          {step.result.data !== undefined && <pre className="code">{prettyJson(step.result.data)}</pre>}
          {(step.result.artifacts ?? []).length > 0 && (
            <Space size={4} wrap>
              {(step.result.artifacts ?? []).map((a) => (
                <Tag key={a} color="cyan">
                  交付物 {a}
                </Tag>
              ))}
            </Space>
          )}
        </div>
      )}
    </div>
  );
}

function EventLog({ events }: { events: LagentEvent[] }) {
  const [open, setOpen] = useState<string[]>([]);
  if (events.length === 0) return <span className="muted">没有事件记录（事件缓冲区只覆盖最近一段；步骤表始终完整）。</span>;
  return (
    <Collapse
      activeKey={open}
      onChange={(k) => setOpen(Array.isArray(k) ? k : [k])}
      items={events
        .slice()
        .reverse()
        .map((e) => ({
          key: String(e.seq),
          label: (
            <Space size={8}>
              <span className="timeline-time">{fmtTime(e.ts).slice(11)}</span>
              <Tag>{e.type}</Tag>
              <span className="small muted">seq {e.seq}</span>
            </Space>
          ),
          children: <pre className="code">{prettyJson(e.data as never)}</pre>,
        }))}
    />
  );
}