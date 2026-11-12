import { useEffect, useMemo, useState } from 'react';
import {
  Alert,
  Button,
  Form,
  Input,
  InputNumber,
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
import { DeleteOutlined, EditOutlined, ExperimentOutlined, PlusOutlined, ReloadOutlined, SyncOutlined } from '@ant-design/icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  errorText,
  integrationApi,
  type CatalogEntry,
  type JsonObject,
  type JsonValue,
  type MCPTransport,
  type RetrievalSource,
  type RiskLevel,
  type ServerInput,
  type ServerView,
  type TemplateStatus,
  type ToolTestResult,
  type TryResult,
} from '../../api';
import { Loading } from '../../components/States';
import { JsonBlock } from '../../components/JsonBlock';
import { fmtTime } from '../../lib/format';
import { riskMeta } from '../../lib/labels';
import { JsonInput } from '../components/JsonInput';

const riskLevels: RiskLevel[] = ['read_only', 'local_write', 'command', 'external'];

/**
 * Integrations: MCP servers (credentials write-only), retrieval sources,
 * the tool catalog and its tester, risk overrides, template availability.
 */
export function IntegrationsPage() {
  return (
    <div className="page-card">
      <div className="page-toolbar">
        <Typography.Text strong>集成与工具</Typography.Text>
        <span className="muted small">
          MCP 凭据写入后不再回显；工具风险只能上调，降低风险要在服务器配置里显式声明。
        </span>
      </div>
      <Tabs
        items={[
          { key: 'servers', label: 'MCP 服务器', children: <ServersTab /> },
          { key: 'sources', label: '检索源', children: <SourcesTab /> },
          { key: 'catalog', label: '工具目录', children: <CatalogTab /> },
          { key: 'overrides', label: '风险覆盖', children: <OverridesTab /> },
          { key: 'templates', label: '模板可用性', children: <TemplatesTab /> },
        ]}
      />
    </div>
  );
}

// --- MCP servers ---

function ServersTab() {
  const qc = useQueryClient();
  const servers = useQuery({ queryKey: ['integrations', 'servers'], queryFn: integrationApi.listServers });
  const [editing, setEditing] = useState<ServerView | 'new' | null>(null);
  const [testing, setTesting] = useState<{ id: string; result?: { ok: boolean; error?: string; tools?: string[]; duration_ms: number } } | null>(null);

  const sync = useMutation({
    mutationFn: (id: string) => integrationApi.syncServer(id),
    onSuccess: (v) => {
      void message.success(`已同步 ${v.tools?.length ?? 0} 个工具`);
      void qc.invalidateQueries({ queryKey: ['integrations'] });
    },
    onError: (e) => void message.error(errorText(e)),
  });
  const test = useMutation({
    mutationFn: (id: string) => integrationApi.testServer(id),
    onSuccess: (r) => setTesting((t) => (t ? { ...t, result: r } : t)),
    onError: (e) => {
      void message.error(errorText(e));
      setTesting(null);
    },
  });
  const del = useMutation({
    mutationFn: (id: string) => integrationApi.deleteServer(id),
    onSuccess: () => {
      void message.success('已删除');
      void qc.invalidateQueries({ queryKey: ['integrations'] });
    },
    onError: (e) => void message.error(errorText(e)),
  });

  const columns: TableColumnsType<ServerView> = [
    {
      title: '名称',
      dataIndex: 'name',
      render: (n: string, s) => (
        <Space size={6} wrap>
          <strong>{n}</strong>
          <Tag>{s.transport}</Tag>
          {!s.enabled && <Tag>已停用</Tag>}
          {s.has_secret && <Tag color="cyan">有凭据</Tag>}
        </Space>
      ),
    },
    {
      title: '地址 / 命令',
      render: (_, s) => (
        <span className="small mono-input" style={{ wordBreak: 'break-all' }}>
          {s.transport === 'stdio' ? (s.command ?? []).join(' ') || '—' : s.url || '—'}
        </span>
      ),
    },
    {
      title: '状态',
      width: 120,
      render: (_, s) => (
        <Tooltip title={s.last_error}>
          <Tag color={s.status === 'ok' ? 'green' : s.status === 'error' ? 'red' : 'default'}>
            {s.status}
          </Tag>
        </Tooltip>
      ),
    },
    { title: '工具', width: 80, render: (_, s) => s.tools?.length ?? 0 },
    { title: '同步于', width: 170, render: (_, s) => <span className="small muted">{fmtTime(s.synced_at)}</span> },
    {
      title: '操作',
      width: 300,
      render: (_, s) => (
        <Space size={4} wrap>
          <Button size="small" icon={<SyncOutlined />} loading={sync.isPending && sync.variables === s.id} onClick={() => sync.mutate(s.id)}>
            同步
          </Button>
          <Button
            size="small"
            icon={<ExperimentOutlined />}
            onClick={() => {
              setTesting({ id: s.id });
              test.mutate(s.id);
            }}
          >
            测试
          </Button>
          <Button size="small" type="text" icon={<EditOutlined />} onClick={() => setEditing(s)} aria-label="编辑" />
          <Popconfirm title={`删除服务器 ${s.name}？其工具将从角色工具集中消失。`} okText="删除" cancelText="取消" onConfirm={() => del.mutate(s.id)}>
            <Button size="small" type="text" danger icon={<DeleteOutlined />} aria-label="删除" />
          </Popconfirm>
        </Space>
      ),
    },
  ];

  return (
    <div>
      <div className="page-toolbar" style={{ marginBottom: 8 }}>
        <div style={{ flex: 1 }} />
        <Button icon={<ReloadOutlined />} onClick={() => void servers.refetch()} aria-label="重新加载" />
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setEditing('new')}>
          新增服务器
        </Button>
      </div>
      {servers.isLoading ? (
        <Loading />
      ) : servers.isError ? (
        <Alert type="error" showIcon message={errorText(servers.error)} />
      ) : (
        <Table
          rowKey="id"
          size="small"
          columns={columns}
          dataSource={servers.data ?? []}
          pagination={false}
          expandable={{
            expandedRowRender: (s) => <ServerTools s={s} />,
            rowExpandable: (s) => (s.tools?.length ?? 0) > 0,
          }}
        />
      )}
      <ServerModal editing={editing} onClose={() => setEditing(null)} />
      <Modal
        title="连接测试"
        open={testing !== null}
        onCancel={() => setTesting(null)}
        footer={<Button onClick={() => setTesting(null)}>关闭</Button>}
      >
        {!testing?.result ? (
          <Loading tip="正在连接…" />
        ) : testing.result.ok ? (
          <>
            <Alert type="success" showIcon message={`连接成功，${testing.result.tools?.length ?? 0} 个工具（${testing.result.duration_ms}ms）`} />
            <Space size={4} wrap style={{ marginTop: 12 }}>
              {(testing.result.tools ?? []).map((t) => (
                <Tag key={t}>{t}</Tag>
              ))}
            </Space>
          </>
        ) : (
          <Alert type="error" showIcon message="连接失败" description={testing.result.error} />
        )}
      </Modal>
    </div>
  );
}

function ServerTools({ s }: { s: ServerView }) {
  return (
    <Table
      rowKey="registry_name"
      size="small"
      pagination={false}
      dataSource={s.tools ?? []}
      columns={[
        { title: '工具', dataIndex: 'name', width: 220, render: (v: string) => <Typography.Text code>{v}</Typography.Text> },
        { title: '注册名', dataIndex: 'registry_name', width: 260, render: (v: string) => <span className="small">{v}</span> },
        {
          title: '风险',
          dataIndex: 'risk',
          width: 100,
          render: (r: RiskLevel) => <Tag color={riskMeta[r]?.color}>{riskMeta[r]?.label ?? r}</Tag>,
        },
        { title: '说明', dataIndex: 'description', render: (v: string) => <span className="small">{v}</span> },
      ]}
    />
  );
}

interface ServerForm {
  name: string;
  transport: MCPTransport;
  url?: string;
  command?: string;
  env?: JsonValue;
  auth_header?: string;
  secret?: string;
  enabled: boolean;
  risk_overrides?: JsonValue;
}

function ServerModal({ editing, onClose }: { editing: ServerView | 'new' | null; onClose(): void }) {
  const qc = useQueryClient();
  const [form] = Form.useForm<ServerForm>();
  const isNew = editing === 'new';
  const transport = Form.useWatch('transport', form);

  useEffect(() => {
    if (!editing) return;
    if (isNew) {
      form.setFieldsValue({ name: '', transport: 'http', url: '', command: '', env: undefined, auth_header: '', secret: '', enabled: true, risk_overrides: undefined });
    } else {
      form.setFieldsValue({
        name: editing.name,
        transport: editing.transport,
        url: editing.url ?? '',
        command: (editing.command ?? []).join(' '),
        env: (editing.env ?? undefined) as JsonValue | undefined,
        auth_header: editing.auth_header ?? '',
        secret: '',
        enabled: editing.enabled,
        risk_overrides: (editing.risk_overrides ?? undefined) as JsonValue | undefined,
      });
    }
  }, [editing, isNew, form]);

  const save = useMutation({
    mutationFn: (v: ServerForm) => {
      const input: ServerInput = {
        name: v.name.trim(),
        transport: v.transport,
        url: v.transport === 'stdio' ? undefined : v.url?.trim(),
        command: v.transport === 'stdio' ? splitCommand(v.command ?? '') : undefined,
        env: (v.env as Record<string, string> | undefined) ?? undefined,
        auth_header: v.auth_header?.trim() || undefined,
        enabled: v.enabled,
        risk_overrides: (v.risk_overrides as Record<string, RiskLevel> | undefined) ?? undefined,
      };
      if (v.secret) input.secret = v.secret;
      return isNew ? integrationApi.createServer(input) : integrationApi.updateServer(editing ? editing.id : '', input);
    },
    onSuccess: () => {
      void message.success('已保存');
      void qc.invalidateQueries({ queryKey: ['integrations'] });
      onClose();
    },
    onError: (e) => void message.error(errorText(e)),
  });

  return (
    <Modal
      title={isNew ? '新增 MCP 服务器' : '编辑 MCP 服务器'}
      open={editing !== null}
      onCancel={onClose}
      okText="保存"
      cancelText="取消"
      width={680}
      confirmLoading={save.isPending}
      onOk={() => form.submit()}
      destroyOnClose
    >
      <Form form={form} layout="vertical" preserve={false} onFinish={(v) => save.mutate(v)}>
        <Space size={12} wrap align="start">
          <Form.Item name="name" label="名称" rules={[{ required: true, message: '请输入名称' }]} extra="工具注册名为 <名称>__<工具>。">
            <Input className="mono-input" style={{ width: 220 }} />
          </Form.Item>
          <Form.Item name="transport" label="传输">
            <Select
              style={{ width: 140 }}
              options={[
                { value: 'http', label: 'HTTP' },
                { value: 'sse', label: 'SSE' },
                { value: 'stdio', label: 'stdio' },
              ]}
            />
          </Form.Item>
          <Form.Item name="enabled" label="启用" valuePropName="checked">
            <Switch />
          </Form.Item>
        </Space>
        {transport === 'stdio' ? (
          <Form.Item name="command" label="命令" rules={[{ required: true, message: '请输入可执行命令' }]} extra="按空格拆分参数；凭据通过下方 secret 注入子进程环境。">
            <Input className="mono-input" placeholder="npx -y @modelcontextprotocol/server-filesystem /data" />
          </Form.Item>
        ) : (
          <Form.Item name="url" label="URL" rules={[{ required: true, message: '请输入 URL' }]}>
            <Input className="mono-input" placeholder="https://mcp.example.com/mcp" />
          </Form.Item>
        )}
        <Form.Item name="auth_header" label="认证头" extra="例如 Authorization。留空则用默认值。">
          <Input className="mono-input" placeholder="Authorization" />
        </Form.Item>
        <Form.Item
          name="secret"
          label="凭据（只写）"
          extra={isNew ? '保存在密钥存储中；连接时作为请求头传递，不进入提示词、记录或日志。' : '留空表示保持现有凭据不变。'}
        >
          <Input.Password className="mono-input" placeholder="Bearer …" autoComplete="new-password" />
        </Form.Item>
        <Form.Item name="env" label="子进程环境变量（stdio）">
          <JsonInput rows={4} placeholder='{"API_KEY":"…"}' />
        </Form.Item>
        <Form.Item name="risk_overrides" label="工具风险覆盖（只能上调）" extra='例如 {"delete_file":"external"}'>
          <JsonInput rows={4} placeholder='{"tool_name":"external"}' />
        </Form.Item>
      </Form>
    </Modal>
  );
}

function splitCommand(s: string): string[] {
  return s.trim() ? s.trim().split(/\s+/) : [];
}

// --- retrieval sources ---

function SourcesTab() {
  const qc = useQueryClient();
  const sources = useQuery({ queryKey: ['integrations', 'sources'], queryFn: integrationApi.listSources });
  const servers = useQuery({ queryKey: ['integrations', 'servers'], queryFn: integrationApi.listServers });
  const [editing, setEditing] = useState<RetrievalSource | 'new' | null>(null);
  const [trying, setTrying] = useState<RetrievalSource | null>(null);

  const del = useMutation({
    mutationFn: (id: string) => integrationApi.deleteSource(id),
    onSuccess: () => {
      void message.success('已删除');
      void qc.invalidateQueries({ queryKey: ['integrations'] });
    },
    onError: (e) => void message.error(errorText(e)),
  });

  const columns: TableColumnsType<RetrievalSource> = [
    { title: '名称', dataIndex: 'name', width: 180, render: (v: string) => <strong>{v}</strong> },
    { title: '说明', dataIndex: 'description', render: (v?: string) => <span className="small">{v || '—'}</span> },
    {
      title: '来源',
      width: 300,
      render: (_, s) => (
        <span className="small">
          <Typography.Text code>{s.server}</Typography.Text> / <Typography.Text code>{s.tool}</Typography.Text>
        </span>
      ),
    },
    { title: '查询字段', dataIndex: 'query_field', width: 120 },
    {
      title: '操作',
      width: 200,
      render: (_, s) => (
        <Space size={4}>
          <Button size="small" icon={<ExperimentOutlined />} onClick={() => setTrying(s)}>
            试查
          </Button>
          <Button size="small" type="text" icon={<EditOutlined />} onClick={() => setEditing(s)} aria-label="编辑" />
          <Popconfirm title={`删除检索源 ${s.name}？引用它的模板会显示缺失。`} okText="删除" cancelText="取消" onConfirm={() => del.mutate(s.id)}>
            <Button size="small" type="text" danger icon={<DeleteOutlined />} aria-label="删除" />
          </Popconfirm>
        </Space>
      ),
    },
  ];

  return (
    <div>
      <div className="page-toolbar" style={{ marginBottom: 8 }}>
        <span className="muted small">检索源把一个 MCP 工具包装成工作流步骤可用的命名来源。</span>
        <div style={{ flex: 1 }} />
        <Button icon={<ReloadOutlined />} onClick={() => void sources.refetch()} aria-label="重新加载" />
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setEditing('new')}>
          新增检索源
        </Button>
      </div>
      {sources.isLoading ? (
        <Loading />
      ) : sources.isError ? (
        <Alert type="error" showIcon message={errorText(sources.error)} />
      ) : (
        <Table rowKey="id" size="small" columns={columns} dataSource={sources.data ?? []} pagination={false} />
      )}
      <SourceModal editing={editing} onClose={() => setEditing(null)} serverNames={(servers.data ?? []).map((s) => s.name)} />
      <TryModal source={trying} onClose={() => setTrying(null)} />
    </div>
  );
}

function SourceModal({
  editing,
  onClose,
  serverNames,
}: {
  editing: RetrievalSource | 'new' | null;
  onClose(): void;
  serverNames: string[];
}) {
  const qc = useQueryClient();
  const [form] = Form.useForm<{ name: string; description?: string; server: string; tool: string; fixed_args?: JsonValue; query_field?: string }>();
  const isNew = editing === 'new';
  useEffect(() => {
    if (!editing) return;
    if (isNew) form.setFieldsValue({ name: '', description: '', server: '', tool: '', fixed_args: undefined, query_field: 'query' });
    else
      form.setFieldsValue({
        name: editing.name,
        description: editing.description ?? '',
        server: editing.server,
        tool: editing.tool,
        fixed_args: editing.fixed_args ?? undefined,
        query_field: editing.query_field,
      });
  }, [editing, isNew, form]);
  const save = useMutation({
    mutationFn: (v: { name: string; description?: string; server: string; tool: string; fixed_args?: JsonValue; query_field?: string }) => {
      const input = {
        name: v.name.trim(),
        description: v.description?.trim() || undefined,
        server: v.server,
        tool: v.tool,
        fixed_args: (v.fixed_args as JsonObject | undefined) ?? undefined,
        query_field: v.query_field?.trim() || undefined,
      };
      return isNew ? integrationApi.createSource(input) : integrationApi.updateSource(editing ? editing.id : '', input);
    },
    onSuccess: () => {
      void message.success('已保存');
      void qc.invalidateQueries({ queryKey: ['integrations'] });
      onClose();
    },
    onError: (e) => void message.error(errorText(e)),
  });
  return (
    <Modal
      title={isNew ? '新增检索源' : '编辑检索源'}
      open={editing !== null}
      onCancel={onClose}
      okText="保存"
      cancelText="取消"
      confirmLoading={save.isPending}
      onOk={() => form.submit()}
      destroyOnClose
    >
      <Form form={form} layout="vertical" preserve={false} onFinish={(v) => save.mutate(v)}>
        <Form.Item
          name="name"
          label="名称"
          rules={[{ required: true, pattern: /^[A-Za-z0-9_-]{1,40}$/, message: '1-40 位字母、数字、下划线或中划线' }]}
          extra="工作流步骤在 sources 里引用的名字。"
        >
          <Input className="mono-input" />
        </Form.Item>
        <Form.Item name="description" label="说明">
          <Input />
        </Form.Item>
        <Space size={12} wrap>
          <Form.Item name="server" label="MCP 服务器" rules={[{ required: true, message: '请选择服务器' }]}>
            <Select style={{ width: 220 }} options={serverNames.map((n) => ({ value: n, label: n }))} placeholder="选择服务器" />
          </Form.Item>
          <Form.Item name="tool" label="工具名" rules={[{ required: true, message: '请输入工具名' }]}>
            <Input className="mono-input" style={{ width: 220 }} />
          </Form.Item>
          <Form.Item name="query_field" label="查询字段" extra="调用时把查询放入这个参数。">
            <Input className="mono-input" style={{ width: 160 }} />
          </Form.Item>
        </Space>
        <Form.Item name="fixed_args" label="固定参数（JSON 对象）">
          <JsonInput rows={5} placeholder='{"space":"docs"}' />
        </Form.Item>
      </Form>
    </Modal>
  );
}

function TryModal({ source, onClose }: { source: RetrievalSource | null; onClose(): void }) {
  const [query, setQuery] = useState('');
  const [limit, setLimit] = useState(5);
  const [result, setResult] = useState<TryResult | null>(null);

  useEffect(() => {
    if (!source) {
      setQuery('');
      setResult(null);
    }
  }, [source]);

  const run = useMutation({
    mutationFn: () => integrationApi.trySource(source!.id, query.trim(), limit),
    onSuccess: setResult,
    onError: (e) => void message.error(errorText(e)),
  });

  return (
    <Modal
      title={`试查 · ${source?.name ?? ''}`}
      open={source !== null}
      onCancel={onClose}
      width={720}
      footer={<Button onClick={onClose}>关闭</Button>}
    >
      <Space.Compact style={{ width: '100%' }}>
        <Input
          autoFocus
          placeholder="输入查询"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onPressEnter={() => query.trim() && run.mutate()}
        />
        <InputNumber min={1} max={50} value={limit} onChange={(v) => setLimit(v ?? 5)} style={{ width: 90 }} />
        <Button type="primary" loading={run.isPending} disabled={!query.trim()} onClick={() => run.mutate()}>
          查询
        </Button>
      </Space.Compact>
      {result && (
        <div style={{ marginTop: 12 }}>
          <Space size={8} style={{ marginBottom: 8 }}>
            <Tag color={result.is_error ? 'red' : 'green'}>{result.is_error ? '错误' : '成功'}</Tag>
            <span className="muted small">{result.duration_ms}ms</span>
          </Space>
          <pre className={`code ${result.is_error ? 'code-error' : ''}`} style={{ maxHeight: 360 }}>
            {result.content}
          </pre>
        </div>
      )}
    </Modal>
  );
}

// --- tool catalog ---

function CatalogTab() {
  const catalog = useQuery({ queryKey: ['integrations', 'catalog'], queryFn: integrationApi.catalog });
  const [q, setQ] = useState('');
  const [source, setSource] = useState<string>('all');
  const [testing, setTesting] = useState<CatalogEntry | null>(null);

  const rows = useMemo(
    () =>
      (catalog.data ?? []).filter(
        (e) =>
          (source === 'all' || e.source === source) &&
          (!q.trim() || e.name.toLowerCase().includes(q.trim().toLowerCase()) || e.description.toLowerCase().includes(q.trim().toLowerCase())),
      ),
    [catalog.data, q, source],
  );

  const columns: TableColumnsType<CatalogEntry> = [
    {
      title: '工具',
      dataIndex: 'name',
      width: 240,
      render: (n: string, e) => (
        <Space direction="vertical" size={0}>
          <Typography.Text code>{n}</Typography.Text>
          {e.server && <span className="small muted">{e.server}</span>}
        </Space>
      ),
    },
    { title: '来源', dataIndex: 'source', width: 100, render: (v: string) => <Tag>{v}</Tag> },
    {
      title: '风险',
      width: 170,
      render: (_, e) => (
        <Space size={4}>
          <Tag color={riskMeta[e.effective_risk]?.color}>{riskMeta[e.effective_risk]?.label ?? e.effective_risk}</Tag>
          {e.effective_risk !== e.risk && <span className="small muted">基础 {riskMeta[e.risk]?.label ?? e.risk}</span>}
        </Space>
      ),
    },
    {
      title: '属性',
      width: 200,
      render: (_, e) => (
        <Space size={4} wrap>
          {e.read_only && <Tag>只读</Tag>}
          {e.side_effect && <Tag color="orange">有外部影响</Tag>}
          {e.needs_review && <Tag color="red">需审核</Tag>}
          {e.idempotent && <Tag color="cyan">幂等</Tag>}
        </Space>
      ),
    },
    { title: '说明', dataIndex: 'description', render: (v: string) => <span className="small">{v}</span> },
    {
      title: '',
      width: 90,
      render: (_, e) => (
        <Button size="small" onClick={() => setTesting(e)}>
          测试
        </Button>
      ),
    },
  ];

  return (
    <div>
      <div className="page-toolbar" style={{ marginBottom: 8 }}>
        <Input.Search allowClear placeholder="搜索工具名或说明" style={{ width: 280 }} value={q} onChange={(e) => setQ(e.target.value)} />
        <Select
          value={source}
          onChange={setSource}
          style={{ width: 140 }}
          options={[
            { value: 'all', label: '全部来源' },
            { value: 'builtin', label: '内置' },
            { value: 'connector', label: '连接器' },
            { value: 'mcp', label: 'MCP' },
            { value: 'platform', label: '平台' },
          ]}
        />
        <span className="muted small">{rows.length} 个工具</span>
        <div style={{ flex: 1 }} />
        <Button icon={<ReloadOutlined />} onClick={() => void catalog.refetch()} aria-label="重新加载" />
      </div>
      {catalog.isLoading ? (
        <Loading />
      ) : catalog.isError ? (
        <Alert type="error" showIcon message={errorText(catalog.error)} />
      ) : (
        <Table
          rowKey="name"
          size="small"
          columns={columns}
          dataSource={rows}
          pagination={{ pageSize: 20 }}
          expandable={{
            expandedRowRender: (e) => (
              <div>
                <div className="muted small">输入 schema</div>
                <JsonBlock value={e.input_schema} maxHeight={240} />
                {(e.overrides ?? []).length > 0 && (
                  <div style={{ marginTop: 8 }}>
                    <div className="muted small">风险覆盖</div>
                    <Space size={4} wrap>
                      {(e.overrides ?? []).map((o) => (
                        <Tag key={o.id} color="orange">
                          {o.user_id ? `用户 ${o.user_id}` : '全租户'} → {riskMeta[o.level]?.label ?? o.level}
                        </Tag>
                      ))}
                    </Space>
                  </div>
                )}
              </div>
            ),
          }}
        />
      )}
      <ToolTestModal entry={testing} onClose={() => setTesting(null)} />
    </div>
  );
}

function ToolTestModal({ entry, onClose }: { entry: CatalogEntry | null; onClose(): void }) {
  const [input, setInput] = useState<JsonValue | undefined>(undefined);
  const [confirm, setConfirm] = useState(false);
  const [result, setResult] = useState<ToolTestResult | null>(null);
  useEffect(() => {
    if (!entry) {
      setInput(undefined);
      setConfirm(false);
      setResult(null);
    }
  }, [entry]);
  const needsConfirm = !!entry && entry.effective_risk !== 'read_only';
  const run = useMutation({
    mutationFn: () => integrationApi.testTool(entry!.name, input, needsConfirm ? confirm : undefined),
    onSuccess: (r) => setResult(r),
    onError: (e) => void message.error(errorText(e)),
  });
  return (
    <Modal
      title={`测试工具 · ${entry?.name ?? ''}`}
      open={entry !== null}
      onCancel={onClose}
      width={720}
      okText="执行"
      cancelText="关闭"
      okButtonProps={{ disabled: needsConfirm && !confirm, loading: run.isPending }}
      onOk={() => run.mutate()}
    >
      {entry && (
        <>
          <Space size={6} wrap style={{ marginBottom: 8 }}>
            <Tag color={riskMeta[entry.effective_risk]?.color}>风险：{riskMeta[entry.effective_risk]?.label ?? entry.effective_risk}</Tag>
            {entry.side_effect && <Tag color="orange">有外部影响</Tag>}
            {entry.read_only && <Tag>只读</Tag>}
          </Space>
          {needsConfirm && (
            <Alert
              type="warning"
              showIcon
              style={{ marginBottom: 12 }}
              message="工具测试器绕过审批流程，直接以你的身份执行。"
              description={
                <Space>
                  <Switch size="small" checked={confirm} onChange={setConfirm} />
                  <span>我确认这是有意的测试调用；本次调用会写入审计日志。</span>
                </Space>
              }
            />
          )}
          <div className="muted small" style={{ marginBottom: 4 }}>
            输入参数（JSON）
          </div>
          <JsonInput key={entry.name} value={undefined} onChange={setInput} rows={6} placeholder='{"path":"/tmp/a.txt"}' />
          <div className="muted small" style={{ margin: '12px 0 4px' }}>
            输入 schema
          </div>
          <JsonBlock value={entry.input_schema} maxHeight={180} />
        </>
      )}
      {result && (
        <div style={{ marginTop: 12 }}>
          <Space size={8} style={{ marginBottom: 8 }} wrap>
            <Tag color={result.is_error ? 'red' : 'green'}>{result.is_error ? '错误' : '成功'}</Tag>
            <span className="muted small">{result.duration_ms}ms</span>
            {result.control && <Tag color="purple">控制：{result.control}</Tag>}
            {(result.artifacts ?? []).map((a) => (
              <Tag key={a} color="cyan">
                交付物 {a}
              </Tag>
            ))}
          </Space>
          <pre className={`code ${result.is_error ? 'code-error' : ''}`} style={{ maxHeight: 300 }}>
            {result.content}
          </pre>
        </div>
      )}
    </Modal>
  );
}

// --- risk overrides ---

function OverridesTab() {
  const qc = useQueryClient();
  const overrides = useQuery({ queryKey: ['integrations', 'overrides'], queryFn: integrationApi.listOverrides });
  const catalog = useQuery({ queryKey: ['integrations', 'catalog'], queryFn: integrationApi.catalog });
  const [adding, setAdding] = useState(false);
  const [form] = Form.useForm<{ tool: string; level: RiskLevel; user_id?: string }>();

  const save = useMutation({
    mutationFn: (v: { tool: string; level: RiskLevel; user_id?: string }) =>
      integrationApi.setOverride({ tool: v.tool, level: v.level, user_id: v.user_id?.trim() || undefined }),
    onSuccess: () => {
      void message.success('已保存');
      setAdding(false);
      form.resetFields();
      void qc.invalidateQueries({ queryKey: ['integrations'] });
    },
    onError: (e) => void message.error(errorText(e)),
  });
  const del = useMutation({
    mutationFn: (id: string) => integrationApi.deleteOverride(id),
    onSuccess: () => {
      void message.success('已删除');
      void qc.invalidateQueries({ queryKey: ['integrations'] });
    },
    onError: (e) => void message.error(errorText(e)),
  });

  return (
    <div>
      <div className="page-toolbar" style={{ marginBottom: 8 }}>
        <span className="muted small">覆盖只能把风险调高；按用户生效的覆盖优先于全租户覆盖。</span>
        <div style={{ flex: 1 }} />
        <Button icon={<ReloadOutlined />} onClick={() => void overrides.refetch()} aria-label="重新加载" />
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setAdding(true)}>
          新增覆盖
        </Button>
      </div>
      {overrides.isLoading ? (
        <Loading />
      ) : overrides.isError ? (
        <Alert type="error" showIcon message={errorText(overrides.error)} />
      ) : (
        <Table
          rowKey="id"
          size="small"
          pagination={false}
          dataSource={overrides.data ?? []}
          columns={[
            { title: '工具', dataIndex: 'tool', width: 260, render: (v: string) => <Typography.Text code>{v}</Typography.Text> },
            {
              title: '风险',
              dataIndex: 'level',
              width: 140,
              render: (v: RiskLevel) => <Tag color={riskMeta[v]?.color}>{riskMeta[v]?.label ?? v}</Tag>,
            },
            { title: '适用用户', dataIndex: 'user_id', width: 200, render: (v?: string) => v || <Tag>全租户</Tag> },
            { title: '创建时间', dataIndex: 'created_at', width: 180, render: (v: string) => <span className="small">{fmtTime(v)}</span> },
            {
              title: '',
              width: 80,
              render: (_, o) => (
                <Popconfirm title="删除这个覆盖？" okText="删除" cancelText="取消" onConfirm={() => del.mutate(o.id)}>
                  <Button size="small" type="text" danger icon={<DeleteOutlined />} aria-label="删除" />
                </Popconfirm>
              ),
            },
          ]}
        />
      )}
      <Modal
        title="新增风险覆盖"
        open={adding}
        onCancel={() => {
          setAdding(false);
          form.resetFields();
        }}
        okText="保存"
        cancelText="取消"
        confirmLoading={save.isPending}
        onOk={() => form.submit()}
        destroyOnClose
      >
        <Form form={form} layout="vertical" preserve={false} onFinish={(v) => save.mutate(v)}>
          <Form.Item name="tool" label="工具" rules={[{ required: true, message: '请选择工具' }]}>
            <Select
              showSearch
              placeholder="选择已注册的工具"
              options={(catalog.data ?? []).map((c) => ({
                value: c.name,
                label: `${c.name}（当前 ${riskMeta[c.effective_risk]?.label ?? c.effective_risk}）`,
              }))}
            />
          </Form.Item>
          <Form.Item name="level" label="新风险等级" rules={[{ required: true, message: '请选择等级' }]}>
            <Select options={riskLevels.map((r) => ({ value: r, label: riskMeta[r].label }))} />
          </Form.Item>
          <Form.Item name="user_id" label="适用用户" extra="留空表示全租户。">
            <Input className="mono-input" placeholder="可选" />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
}

// --- templates ---

function TemplatesTab() {
  const avail = useQuery({ queryKey: ['integrations', 'templates'], queryFn: integrationApi.templateAvailability });
  const cols: TableColumnsType<TemplateStatus> = [
    { title: '模板', dataIndex: 'key', width: 220, render: (v: string, t) => <Space size={6}><strong>{v}</strong>{t.built_in && <Tag>内置</Tag>}{t.needs_config && <Tag color="orange">需配置</Tag>}</Space> },
    { title: '版本', dataIndex: 'ver', width: 70, render: (v: number) => `v${v}` },
    { title: '名称', dataIndex: 'name', width: 200 },
    {
      title: '依赖的检索源',
      render: (_, t) => (
        <Space size={4} wrap>
          {(t.sources ?? []).map((s) => (
            <Tag key={s} color={(t.missing ?? []).includes(s) ? 'red' : 'green'}>
              {s}
            </Tag>
          ))}
          {(t.sources ?? []).length === 0 && <span className="muted">无</span>}
        </Space>
      ),
    },
    {
      title: '缺失',
      width: 220,
      render: (_, t) =>
        (t.missing ?? []).length ? (
          <Space size={4} wrap>
            {(t.missing ?? []).map((m) => (
              <Tag key={m} color="red">
                {m}
              </Tag>
            ))}
          </Space>
        ) : (
          <span className="muted">—</span>
        ),
    },
  ];
  return (
    <div>
      <div className="page-toolbar" style={{ marginBottom: 8 }}>
        <span className="muted small">
          由当前 MCP 与检索源配置推导：缺失的检索源会让对应模板在运行前失败。
        </span>
        <div style={{ flex: 1 }} />
        <Button icon={<ReloadOutlined />} onClick={() => void avail.refetch()} aria-label="重新加载" />
      </div>
      {avail.isLoading ? (
        <Loading />
      ) : avail.isError ? (
        <Alert type="error" showIcon message={errorText(avail.error)} />
      ) : (
        <Table rowKey={(t) => `${t.key}@${t.ver}`} size="small" columns={cols} dataSource={avail.data ?? []} pagination={false} />
      )}
    </div>
  );
}