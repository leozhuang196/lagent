import { useEffect, useMemo, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import {
  Alert,
  Button,
  Descriptions,
  Form,
  Input,
  Modal,
  Select,
  Space,
  Table,
  Tag,
  Typography,
  message,
  type TableColumnsType,
} from 'antd';
import { PlayCircleOutlined, PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  errorText,
  openRunStream,
  workflowDefsApi,
  workflowsApi,
  type DryRunResult,
  type JsonValue,
  type PermissionMode,
  type RunStatus,
  type WorkflowDef,
  type WorkflowRun,
} from '../../api';
import { Loading } from '../../components/States';
import { fmtTime } from '../../lib/format';
import { isTerminal, runStatusMeta, runStatusOptions } from '../runMeta';
import { JsonInput } from '../components/JsonInput';
import { JsonBlock } from '../../components/JsonBlock';

/**
 * Workflow runs: every execution of a definition, newest first. A run's
 * identity is the definition version plus the frozen parameters (§9.7).
 */
export function RunsPage() {
  const nav = useNavigate();
  const [defKey, setDefKey] = useState<string>('');
  const [status, setStatus] = useState<RunStatus | ''>('');
  const [all, setAll] = useState(true);
  const qc = useQueryClient();
  const [starting, setStarting] = useState(false);
  const [promoting, setPromoting] = useState(false);

  const defs = useQuery({ queryKey: ['assets', '/admin/workflow-defs'], queryFn: () => workflowDefsApi.list() });
  const runs = useQuery({
    queryKey: ['runs', defKey, status, all],
    queryFn: () => workflowsApi.listRuns({ def_key: defKey || undefined, status: status || undefined, all }),
  });

  // Live refresh while anything is running: runs change without user action.
  const hasActive = useMemo(() => (runs.data?.items ?? []).some((r) => !isTerminal(r.status)), [runs.data]);

  const [events, setEvents] = useState<string[]>([]);
  useEffect(() => {
    const live = (runs.data?.items ?? []).filter((r) => !isTerminal(r.status)).slice(0, 3);
    if (live.length === 0) {
      setEvents([]);
      return;
    }
    const handles = live.map((r) =>
      openRunStream(r.id, {
        after: 0,
        onEvent: (ev) => {
          if (ev.type === 'run.completed') {
            void qc.invalidateQueries({ queryKey: ['runs'] });
          }
          setEvents((prev) => {
            const line = `${r.def_key}: ${ev.type}`;
            const next = [...prev, line];
            return next.slice(-20);
          });
        },
      }),
    );
    return () => handles.forEach((h) => h.close());
  }, [runs.data, qc]);

  const columns: TableColumnsType<WorkflowRun> = [
    {
      title: '运行',
      width: 120,
      render: (_, r) => (
        <Link to={`/admin/runs/${r.id}`} style={{ fontWeight: 600 }}>
          {r.id.slice(0, 8)}…
        </Link>
      ),
    },
    {
      title: '模板',
      render: (_, r) => (
        <Space direction="vertical" size={0}>
          <Typography.Text>{r.def_key}</Typography.Text>
          <span className="small muted">
            v{r.def_ver} · 角色 {r.role_key} v{r.role_ver}
          </span>
        </Space>
      ),
    },
    { title: '状态', dataIndex: 'status', width: 130, render: (s: RunStatus) => <RunStatusTag status={s} /> },
    {
      title: '模式',
      dataIndex: 'mode',
      width: 90,
      render: (m: WorkflowRun['mode']) => (
        <Tag color={m === 'rerun' ? 'orange' : m === 'continue' ? 'blue' : 'default'}>{m}</Tag>
      ),
    },
    {
      title: '参数',
      render: (_, r) => <span className="small muted">{(r.frozen.params ?? undefined) ? shortParams(r) : '—'}</span>,
    },
    {
      title: '发起者',
      width: 150,
      render: (_, r) => <span className="small">{r.started_by?.name || r.started_by?.id}</span>,
    },
    { title: '开始', width: 175, render: (_, r) => <span className="small">{fmtTime(r.created_at)}</span> },
    { title: '结束', width: 175, render: (_, r) => <span className="small">{fmtTime(r.ended_at)}</span> },
  ];

  const defOptions = (defs.data ?? []).map((d) => ({ value: d.key, label: `${d.key}${d.latest_ver ? ` · v${d.latest_ver}` : ' · 仅草稿'}` }));

  return (
    <div className="page-card">
      <div className="page-toolbar" style={{ flexWrap: 'wrap', gap: 8 }}>
        <Typography.Text strong>工作流运行</Typography.Text>
        <Select
          allowClear
          showSearch
          placeholder="全部模板"
          style={{ width: 220 }}
          value={defKey || undefined}
          onChange={(v) => setDefKey(v ?? '')}
          options={defOptions}
        />
        <Select
          allowClear
          placeholder="全部状态"
          style={{ width: 150 }}
          value={status || undefined}
          onChange={(v) => setStatus(v ?? '')}
          options={runStatusOptions}
        />
        <Select
          style={{ width: 150 }}
          value={all ? 'all' : 'mine'}
          onChange={(v) => setAll(v === 'all')}
          options={[
            { value: 'all', label: '全租户' },
            { value: 'mine', label: '我发起的' },
          ]}
        />
        <div style={{ flex: 1 }} />
        <Button icon={<ReloadOutlined />} onClick={() => void runs.refetch()} aria-label="重新加载" />
        <Button icon={<PlusOutlined />} onClick={() => setPromoting(true)}>
          从会话沉淀
        </Button>
        <Button type="primary" icon={<PlayCircleOutlined />} onClick={() => setStarting(true)}>
          开始运行
        </Button>
      </div>

      {events.length > 0 && hasActive && (
        <div className="readonly-banner small">
          活动运行事件：{events.slice(-3).join(' · ')}
        </div>
      )}

      {runs.isLoading ? (
        <Loading />
      ) : runs.isError ? (
        <Alert type="error" showIcon message={errorText(runs.error)} />
      ) : (
        <Table
          rowKey="id"
          size="small"
          columns={columns}
          dataSource={runs.data?.items ?? []}
          pagination={false}
          onRow={(r) => ({ onClick: () => nav(`/admin/runs/${r.id}`), style: { cursor: 'pointer' } })}
        />
      )}
      {runs.data?.next && (
        <Button style={{ marginTop: 12 }} onClick={() => void runs.refetch()}>
          刷新以查看更多
        </Button>
      )}

      <StartRunModal open={starting} onClose={() => setStarting(false)} defOptions={defOptions} />
      <PromoteModal open={promoting} onClose={() => setPromoting(false)} />
    </div>
  );
}

function RunStatusTag({ status }: { status: RunStatus }) {
  const m = runStatusMeta[status] ?? { label: status, color: 'default' };
  return <Tag color={m.color}>{m.label}</Tag>;
}

function shortParams(r: WorkflowRun): string {
  const p = r.frozen.params ?? {};
  const s = JSON.stringify(p);
  return s.length > 60 ? `${s.slice(0, 60)}…` : s;
}

interface StartValues {
  def_key: string;
  def_ver?: number;
  draft?: boolean;
  params?: JsonValue;
  permission_mode: PermissionMode;
  debug?: boolean;
  tz?: string;
}

function StartRunModal({
  open,
  onClose,
  defOptions,
}: {
  open: boolean;
  onClose(): void;
  defOptions: { value: string; label: string }[];
}) {
  const nav = useNavigate();
  const qc = useQueryClient();
  const [form] = Form.useForm<StartValues>();
  const [preview, setPreview] = useState<DryRunResult | null>(null);
  const draft = Form.useWatch('draft', form);

  const dry = useMutation({
    mutationFn: (v: StartValues) =>
      workflowsApi.dryRun({
        def_key: v.def_key,
        def_ver: v.def_ver,
        draft: v.draft,
        params: toParams(v.params),
        tz: v.tz,
      }),
    onSuccess: setPreview,
    onError: (e) => void message.error(errorText(e)),
  });
  const start = useMutation({
    mutationFn: (v: StartValues) =>
      workflowsApi.start(v.def_key, {
        def_ver: v.def_ver,
        draft: v.draft,
        params: toParams(v.params),
        permission_mode: v.permission_mode,
        debug: v.debug,
        tz: v.tz,
      }),
    onSuccess: (run) => {
      void message.success('运行已创建');
      void qc.invalidateQueries({ queryKey: ['runs'] });
      onClose();
      form.resetFields();
      setPreview(null);
      nav(`/admin/runs/${run.id}`);
    },
    onError: (e) => void message.error(errorText(e)),
  });

  useEffect(() => {
    if (!open) setPreview(null);
  }, [open]);

  return (
    <Modal
      title="开始工作流运行"
      open={open}
      onCancel={onClose}
      width={760}
      footer={
        <Space>
          <Button onClick={onClose}>取消</Button>
          <Button onClick={() => form.submit()} loading={dry.isPending}>
            预览实例
          </Button>
          <Button type="primary" loading={start.isPending} onClick={() => start.mutate(form.getFieldsValue(true))}>
            确认开始
          </Button>
        </Space>
      }
      destroyOnClose
    >
      <Form form={form} layout="vertical" preserve={false} initialValues={{ permission_mode: 'ask_all', debug: false }}>
        <Form.Item name="def_key" label="工作流模板" rules={[{ required: true, message: '请选择模板' }]}>
          <Select showSearch options={defOptions} placeholder="选择已发布的模板" />
        </Form.Item>
        <Space size={12} wrap align="start">
          <Form.Item name="def_ver" label="版本" extra="留空 = 最新发布版本。">
            <Input type="number" style={{ width: 150 }} />
          </Form.Item>
          <Form.Item name="draft" label="使用草稿" valuePropName="checked" extra="草稿只能以调试方式运行。">
            <Select
              style={{ width: 140 }}
              options={[
                { value: false, label: '发布会话' },
                { value: true, label: '草稿（Ver 0）' },
              ]}
            />
          </Form.Item>
          <Form.Item name="debug" label="调试运行" extra="调试运行不计入业务用量。">
            <Select
              style={{ width: 150 }}
              options={[
                { value: false, label: '正式' },
                { value: true, label: '调试' },
              ]}
            />
          </Form.Item>
          <Form.Item name="permission_mode" label="权限模式">
            <Select
              style={{ width: 190 }}
              disabled={!!draft}
              options={[
                { value: 'ask_all', label: '每次询问' },
                { value: 'ask_external', label: '仅对外询问' },
                { value: 'auto', label: '自动执行' },
              ]}
            />
          </Form.Item>
        </Space>
        <Form.Item name="tz" label="时区" extra="影响周期参数的解释，默认租户时区。">
          <Input style={{ width: 200 }} placeholder="Asia/Shanghai" />
        </Form.Item>
        <Form.Item name="params" label="参数（JSON 对象）">
          <JsonInput rows={5} placeholder='{"week":"last_week"}' />
        </Form.Item>
      </Form>

      {preview && (
        <div style={{ marginTop: 8 }}>
          <Alert
            type="info"
            showIcon
            style={{ marginBottom: 8 }}
            message={`借用 v${preview.def_ver} 冻结于 ${fmtTime(preview.frozen.now)}，展开 ${preview.instances?.length ?? 0} 个步骤实例`}
            description={`参数：${JSON.stringify(preview.frozen.params)}`}
          />
          <Table
            rowKey={(i) => `${i.step_id}-${i.index}`}
            size="small"
            pagination={false}
            dataSource={preview.instances ?? []}
            columns={[
              { title: '步骤', dataIndex: 'step_id', width: 150 },
              { title: '#', dataIndex: 'index', width: 50 },
              { title: '实例项', width: 160, render: (_, i) => <span className="small muted">{i.item === undefined ? '—' : JSON.stringify(i.item)}</span> },
              { title: '角色', width: 130, render: (_, i) => `${i.role_key} v${i.role_ver}` },
              { title: '输入', render: (_, i) => <span className="small">{i.input.slice(0, 120)}</span> },
              {
                title: '复用',
                width: 100,
                render: (_, i) => (i.would_reuse ? <Tag color="purple">复用</Tag> : <span className="muted">—</span>),
              },
            ]}
          />
        </div>
      )}
    </Modal>
  );
}

function toParams(v: JsonValue | undefined): Record<string, JsonValue> | undefined {
  if (!v || typeof v !== 'object' || Array.isArray(v)) return undefined;
  return v as Record<string, JsonValue>;
}

function PromoteModal({ open, onClose }: { open: boolean; onClose(): void }) {
  const [sessionID, setSessionID] = useState('');
  const nav = useNavigate();
  const promote = useMutation({
    mutationFn: () => workflowsApi.promote(sessionID.trim()),
    onSuccess: (def: WorkflowDef) => {
      void message.success(`已生成草稿模板 ${def.key}（Ver 0），请在模板页检查后发布`);
      onClose();
      setSessionID('');
      nav(`/admin/workflow-defs/${encodeURIComponent(def.key)}`);
    },
    onError: (e) => void message.error(errorText(e)),
  });
  return (
    <Modal
      title="从会话沉淀工作流"
      open={open}
      onCancel={onClose}
      okText="生成草稿"
      cancelText="取消"
      confirmLoading={promote.isPending}
      okButtonProps={{ disabled: !sessionID.trim() }}
      onOk={() => promote.mutate()}
      destroyOnClose
    >
      <Typography.Paragraph type="secondary">
        把一次已经跑通的会话（含子任务）转成草稿模板：每个子任务或用户轮次成为一步，默认串行。
        具体参数值会保留在步骤里，并在描述中列出，方便你之后参数化。
      </Typography.Paragraph>
      <Input
        autoFocus
        className="mono-input"
        placeholder="会话 ID"
        value={sessionID}
        onChange={(e) => setSessionID(e.target.value)}
        onPressEnter={() => sessionID.trim() && promote.mutate()}
      />
    </Modal>
  );
}

export function RunParamsCard({ run }: { run: WorkflowRun }) {
  const [open, setOpen] = useState(false);
  return (
    <Descriptions size="small" column={2} bordered>
      <Descriptions.Item label="冻结时间">{fmtTime(run.frozen.now)}</Descriptions.Item>
      <Descriptions.Item label="时区">{run.frozen.tz}</Descriptions.Item>
      <Descriptions.Item label="随机种子">{run.frozen.seed}</Descriptions.Item>
      <Descriptions.Item label="参数">
        <Space>
          <span className="small">{shortParams(run)}</span>
          <Button size="small" type="link" onClick={() => setOpen(true)}>
            查看
          </Button>
        </Space>
      </Descriptions.Item>
      <Modal title="冻结参数" open={open} onCancel={() => setOpen(false)} footer={null} width={640}>
        <JsonBlock value={run.frozen.params as JsonValue} maxHeight={400} />
      </Modal>
    </Descriptions>
  );
}