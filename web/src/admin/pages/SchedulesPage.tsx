import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import {
  Alert,
  Button,
  Form,
  Input,
  Modal,
  Popconfirm,
  Select,
  Space,
  Switch,
  Table,
  Tabs,
  Tag,
  Tooltip,
  Typography,
  message,
  type TableColumnsType,
} from 'antd';
import { PauseCircleOutlined, PlayCircleOutlined, PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  errorText,
  rolesApi,
  schedulesApi,
  workflowDefsApi,
  type JsonObject,
  type JsonValue,
  type OverlapPolicy,
  type PermissionMode,
  type Schedule,
  type ScheduleInput,
  type ScheduleRun,
} from '../../api';
import { Loading } from '../../components/States';
import { fmtTime } from '../../lib/format';
import { scheduleOutcomeMeta } from '../../lib/labels';
import { JsonInput } from '../components/JsonInput';

const overlapOptions: { value: OverlapPolicy; label: string }[] = [
  { value: 'skip', label: '跳过（skip）' },
  { value: 'queue', label: '排队（queue）' },
  { value: 'parallel', label: '并行（parallel）' },
];

const permissionOptions: { value: PermissionMode; label: string }[] = [
  { value: 'ask_all', label: '每次询问' },
  { value: 'ask_external', label: '仅对外询问' },
  { value: 'auto', label: '自动执行' },
];

/** Schedules (§9.10): cron + timezone + target + explicit permission mode. */
export function SchedulesPage() {
  const qc = useQueryClient();
  const [all, setAll] = useState(true);
  const list = useQuery({ queryKey: ['schedules', all], queryFn: () => schedulesApi.list({ all }) });
  const [editing, setEditing] = useState<Schedule | 'new' | null>(null);
  const [inspecting, setInspecting] = useState<Schedule | null>(null);

  const toggle = useMutation({
    mutationFn: (a: { id: string; enabled: boolean }) => (a.enabled ? schedulesApi.resume(a.id) : schedulesApi.pause(a.id)),
    onSuccess: () => {
      void message.success('已更新');
      void qc.invalidateQueries({ queryKey: ['schedules'] });
    },
    onError: (e) => void message.error(errorText(e)),
  });
  const remove = useMutation({
    mutationFn: (id: string) => schedulesApi.remove(id),
    onSuccess: () => {
      void message.success('已删除');
      void qc.invalidateQueries({ queryKey: ['schedules'] });
    },
    onError: (e) => void message.error(errorText(e)),
  });

  const columns: TableColumnsType<Schedule> = [
    {
      title: '名称',
      render: (_, s) => (
        <Space direction="vertical" size={0}>
          <strong>{s.name}</strong>
          <span className="small muted">
            {s.target.workflow_key ? `工作流 ${s.target.workflow_key}` : `提示词 ${truncate(s.target.prompt ?? '', 40)}`}
          </span>
        </Space>
      ),
    },
    {
      title: '计划',
      width: 200,
      render: (_, s) => (
        <Space direction="vertical" size={0}>
          <Typography.Text code>{s.cron}</Typography.Text>
          <span className="small muted">{s.tz}</span>
        </Space>
      ),
    },
    {
      title: '状态',
      width: 110,
      render: (_, s) => (s.enabled ? <Tag color="green">启用</Tag> : <Tag>已暂停</Tag>),
    },
    { title: '下次运行', width: 180, render: (_, s) => <span className="small">{fmtTime(s.next_run_at)}</span> },
    { title: '上次运行', width: 180, render: (_, s) => <span className="small">{fmtTime(s.last_run_at)}</span> },
    {
      title: '重叠',
      dataIndex: 'overlap',
      width: 100,
      render: (v: OverlapPolicy) => overlapOptions.find((o) => o.value === v)?.label ?? v,
    },
    {
      title: '模板版本',
      width: 130,
      render: (_, s) =>
        s.target.workflow_key ? (
          s.def_updated ? (
            <Tooltip title="模板已发布新版本；下次运行会使用新版本。">
              <Tag color="orange">v{s.last_def_ver ?? 0} → 新版本</Tag>
            </Tooltip>
          ) : (
            <span className="small muted">v{s.last_def_ver ?? 0}</span>
          )
        ) : (
          <span className="muted">—</span>
        ),
    },
    {
      title: '操作',
      width: 240,
      render: (_, s) => (
        <Space size={4} wrap>
          <Button size="small" onClick={() => setInspecting(s)}>
            运行记录
          </Button>
          <Button size="small" type="text" onClick={() => setEditing(s)}>
            编辑
          </Button>
          <Button
            size="small"
            icon={s.enabled ? <PauseCircleOutlined /> : <PlayCircleOutlined />}
            loading={toggle.isPending && toggle.variables?.id === s.id}
            onClick={() => toggle.mutate({ id: s.id, enabled: !s.enabled })}
          >
            {s.enabled ? '暂停' : '恢复'}
          </Button>
          <Popconfirm title={`删除计划 ${s.name}？已创建的运行不受影响。`} okText="删除" cancelText="取消" onConfirm={() => remove.mutate(s.id)}>
            <Button size="small" type="text" danger>
              删除
            </Button>
          </Popconfirm>
        </Space>
      ),
    },
  ];

  return (
    <div className="page-card">
      <div className="page-toolbar" style={{ flexWrap: 'wrap', gap: 8 }}>
        <Typography.Text strong>定时任务</Typography.Text>
        <span className="muted small">
          没人盯着运行时，审批策略必须显式选择；对外操作建议使用「仅对外询问」或常设授权。
        </span>
        <div style={{ flex: 1 }} />
        <Select
          style={{ width: 140 }}
          value={all ? 'all' : 'mine'}
          onChange={(v) => setAll(v === 'all')}
          options={[
            { value: 'all', label: '全租户' },
            { value: 'mine', label: '我创建的' },
          ]}
        />
        <Button icon={<ReloadOutlined />} onClick={() => void list.refetch()} aria-label="重新加载" />
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setEditing('new')}>
          新建计划
        </Button>
      </div>
      {list.isLoading ? (
        <Loading />
      ) : list.isError ? (
        <Alert type="error" showIcon message={errorText(list.error)} />
      ) : (
        <Table rowKey="id" size="small" columns={columns} dataSource={list.data?.items ?? []} pagination={false} />
      )}
      <ScheduleModal editing={editing} onClose={() => setEditing(null)} />
      <InspectorModal schedule={inspecting} onClose={() => setInspecting(null)} />
    </div>
  );
}

function truncate(s: string, n: number) {
  return s.length > n ? `${s.slice(0, n)}…` : s;
}

interface FormValues {
  name: string;
  cron: string;
  tz?: string;
  overlap: OverlapPolicy;
  kind: 'prompt' | 'workflow';
  prompt?: string;
  role_key?: string;
  workflow_key?: string;
  params?: JsonValue;
  permission_mode: PermissionMode;
  enabled: boolean;
}

function toTarget(v: FormValues): ScheduleInput['target'] {
  if (v.kind === 'workflow') {
    return {
      workflow_key: v.workflow_key?.trim() || undefined,
      params: (v.params as JsonObject | undefined) ?? undefined,
    };
  }
  return {
    prompt: v.prompt?.trim() || undefined,
    role_key: v.role_key?.trim() || undefined,
  };
}

function ScheduleModal({ editing, onClose }: { editing: Schedule | 'new' | null; onClose(): void }) {
  const qc = useQueryClient();
  const [form] = Form.useForm<FormValues>();
  const isNew = editing === 'new';
  const kind = Form.useWatch('kind', form);

  const roles = useQuery({ queryKey: ['assets', '/admin/roles'], queryFn: () => rolesApi.list() });
  const defs = useQuery({ queryKey: ['assets', '/admin/workflow-defs'], queryFn: () => workflowDefsApi.list() });

  useEffect(() => {
    if (!editing) return;
    if (isNew) {
      form.setFieldsValue({
        name: '',
        cron: '0 9 * * 1',
        tz: 'Asia/Shanghai',
        overlap: 'skip',
        kind: 'workflow',
        prompt: '',
        role_key: '',
        workflow_key: '',
        params: undefined,
        permission_mode: 'ask_all',
        enabled: true,
      });
    } else {
      form.setFieldsValue({
        name: editing.name,
        cron: editing.cron,
        tz: editing.tz,
        overlap: editing.overlap,
        kind: editing.target.workflow_key ? 'workflow' : 'prompt',
        prompt: editing.target.prompt ?? '',
        role_key: editing.target.role_key ?? '',
        workflow_key: editing.target.workflow_key ?? '',
        params: (editing.target.params ?? undefined) as JsonValue | undefined,
        permission_mode: editing.permission_mode,
        enabled: editing.enabled,
      });
    }
  }, [editing, isNew, form]);

  const save = useMutation({
    mutationFn: (v: FormValues) => {
      const input: ScheduleInput = {
        name: v.name.trim(),
        cron: v.cron.trim(),
        tz: v.tz?.trim() || undefined,
        overlap: v.overlap,
        target: toTarget(v),
        permission_mode: v.permission_mode,
        enabled: v.enabled,
      };
      return isNew ? schedulesApi.create(input) : schedulesApi.update(editing ? editing.id : '', input);
    },
    onSuccess: () => {
      void message.success('已保存');
      void qc.invalidateQueries({ queryKey: ['schedules'] });
      onClose();
    },
    onError: (e) => void message.error(errorText(e)),
  });

  return (
    <Modal
      title={isNew ? '新建定时任务' : '编辑定时任务'}
      open={editing !== null}
      onCancel={onClose}
      width={720}
      okText="保存"
      cancelText="取消"
      confirmLoading={save.isPending}
      onOk={() => form.submit()}
      destroyOnClose
    >
      <Form form={form} layout="vertical" preserve={false} onFinish={(v) => save.mutate(v)}>
        <Space size={12} wrap align="start">
          <Form.Item name="name" label="名称" rules={[{ required: true, message: '请输入名称' }]}>
            <Input style={{ width: 260 }} />
          </Form.Item>
          <Form.Item name="cron" label="Cron（5 段）" rules={[{ required: true, message: '请输入 cron 表达式' }]} extra="例如 0 9 * * 1 = 每周一 09:00。">
            <Input className="mono-input" style={{ width: 200 }} />
          </Form.Item>
          <Form.Item name="tz" label="时区">
            <Input style={{ width: 180 }} placeholder="Asia/Shanghai" />
          </Form.Item>
        </Space>
        <Space size={12} wrap align="start">
          <Form.Item name="kind" label="目标类型">
            <Select
              style={{ width: 160 }}
              options={[
                { value: 'workflow', label: '运行工作流' },
                { value: 'prompt', label: '发送提示词' },
              ]}
            />
          </Form.Item>
          <Form.Item name="overlap" label="重叠策略" extra="上一次还没结束时怎么办。">
            <Select style={{ width: 190 }} options={overlapOptions} />
          </Form.Item>
          <Form.Item name="permission_mode" label="权限模式" required extra="必须显式选择。">
            <Select style={{ width: 170 }} options={permissionOptions} />
          </Form.Item>
          <Form.Item name="enabled" label="启用" valuePropName="checked">
            <Switch />
          </Form.Item>
        </Space>
        {kind === 'workflow' ? (
          <>
            <Form.Item name="workflow_key" label="工作流模板" rules={[{ required: true, message: '请选择模板' }]}>
              <Select
                showSearch
                options={(defs.data ?? []).map((d) => ({
                  value: d.key,
                  label: `${d.key}${d.latest_ver ? ` · v${d.latest_ver}` : ' · 仅草稿'}`,
                }))}
              />
            </Form.Item>
            <Form.Item name="params" label="参数（JSON 对象）" extra="必须是已发布模板声明的参数。">
              <JsonInput rows={5} placeholder='{"week":"last_week"}' />
            </Form.Item>
          </>
        ) : (
          <>
            <Form.Item name="prompt" label="提示词" rules={[{ required: true, message: '请输入提示词' }]}>
              <Input.TextArea rows={4} placeholder="例如：整理本周群里的需求，生成周报草稿。" />
            </Form.Item>
            <Form.Item name="role_key" label="角色" extra="留空使用租户默认角色。">
              <Select
                allowClear
                showSearch
                style={{ width: 300 }}
                options={(roles.data ?? []).map((r) => ({ value: r.key, label: r.key }))}
              />
            </Form.Item>
          </>
        )}
      </Form>
    </Modal>
  );
}

function InspectorModal({ schedule, onClose }: { schedule: Schedule | null; onClose(): void }) {
  const runs = useQuery({
    queryKey: ['schedules', 'runs', schedule?.id],
    queryFn: () => schedulesApi.runs(schedule!.id, { limit: 50 }),
    enabled: !!schedule,
  });
  const upcoming = useQuery({
    queryKey: ['schedules', 'upcoming', schedule?.id],
    queryFn: () => schedulesApi.upcoming(schedule!.id, 8),
    enabled: !!schedule,
  });

  const cols: TableColumnsType<ScheduleRun> = [
    { title: '计划时间', dataIndex: 'planned_at', width: 180, render: (v: string) => <span className="small">{fmtTime(v)}</span> },
    {
      title: '结果',
      dataIndex: 'outcome',
      width: 100,
      render: (v: ScheduleRun['outcome']) => (
        <Tag color={scheduleOutcomeMeta[v]?.color}>{scheduleOutcomeMeta[v]?.label ?? v}</Tag>
      ),
    },
    { title: '说明', dataIndex: 'reason', render: (v?: string) => <span className="small muted">{v || '—'}</span> },
    {
      title: '运行',
      width: 120,
      render: (_, r) =>
        r.run_id ? (
          <Link to={`/admin/runs/${r.run_id}`} className="small">
            {r.run_id.slice(0, 8)}…
          </Link>
        ) : r.session_id ? (
          <Link to={`/c/${r.session_id}`} className="small">
            会话
          </Link>
        ) : (
          <span className="muted">—</span>
        ),
    },
  ];

  return (
    <Modal title={`运行记录 · ${schedule?.name ?? ''}`} open={!!schedule} onCancel={onClose} footer={null} width={840}>
      <Tabs
        items={[
          {
            key: 'upcoming',
            label: '接下来',
            children: upcoming.isLoading ? (
              <Loading />
            ) : upcoming.isError ? (
              <Alert type="error" showIcon message={errorText(upcoming.error)} />
            ) : (
              <Space direction="vertical">
                {(upcoming.data ?? []).map((t) => (
                  <span key={t} className="small">
                    {fmtTime(t)}
                  </span>
                ))}
                {(upcoming.data ?? []).length === 0 && <span className="muted">已暂停或没有下一次。</span>}
              </Space>
            ),
          },
          {
            key: 'runs',
            label: '历史',
            children: runs.isLoading ? (
              <Loading />
            ) : runs.isError ? (
              <Alert type="error" showIcon message={errorText(runs.error)} />
            ) : (
              <Table rowKey="id" size="small" columns={cols} dataSource={runs.data?.items ?? []} pagination={{ pageSize: 10 }} />
            ),
          },
        ]}
      />
    </Modal>
  );
}