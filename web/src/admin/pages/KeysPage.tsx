import { useState } from 'react';
import {
  Alert,
  Button,
  Form,
  Input,
  Modal,
  Popconfirm,
  Select,
  Space,
  Table,
  Tag,
  Tooltip,
  Typography,
  message,
  type TableColumnsType,
} from 'antd';
import { CopyOutlined, PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { errorText, keysApi, type APIKey, type CreatedKey, type PermissionMode, type Role } from '../../api';
import { Loading } from '../../components/States';
import { fmtTime } from '../../lib/format';
import { roleLabel } from '../../lib/labels';

const roleOptions: Role[] = ['member', 'tenant_admin'];

function permissionLabel(m?: PermissionMode) {
  switch (m) {
    case 'auto':
      return '自动执行';
    case 'ask_external':
      return '仅对外询问';
    case 'ask_all':
    default:
      return '每次询问';
  }
}

/**
 * API keys. The plaintext value exists only in the creation response; the
 * stored record's hash is never rendered.
 */
export function KeysPage() {
  const qc = useQueryClient();
  const keys = useQuery({ queryKey: ['keys'], queryFn: keysApi.list });
  const [creating, setCreating] = useState(false);
  const [created, setCreated] = useState<CreatedKey | null>(null);
  const [form] = Form.useForm<{ name: string; role: Role; max_permission_mode: PermissionMode }>();

  const create = useMutation({
    mutationFn: (v: { name: string; role: Role; max_permission_mode: PermissionMode }) => keysApi.create(v),
    onSuccess: (k) => {
      setCreating(false);
      form.resetFields();
      setCreated(k);
      void qc.invalidateQueries({ queryKey: ['keys'] });
    },
    onError: (e) => void message.error(errorText(e)),
  });
  const toggle = useMutation({
    mutationFn: (a: { id: string; disabled: boolean }) => keysApi.setDisabled(a.id, a.disabled),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['keys'] }),
    onError: (e) => void message.error(errorText(e)),
  });

  const columns: TableColumnsType<APIKey> = [
    {
      title: '名称',
      dataIndex: 'name',
      render: (n: string, k) => (
        <Space size={6}>
          <strong>{n}</strong>
          {k.disabled && <Tag>已停用</Tag>}
        </Space>
      ),
    },
    { title: '前缀', dataIndex: 'prefix', width: 140, render: (p: string) => <Typography.Text code>{p}…</Typography.Text> },
    { title: '角色', dataIndex: 'role', width: 120, render: (r: Role) => roleLabel[r] ?? r },
    {
      title: '权限上限',
      dataIndex: 'max_permission_mode',
      width: 130,
      render: (m: PermissionMode) => permissionLabel(m),
    },
    { title: '最近使用', dataIndex: 'last_used_at', width: 180, render: (v?: string) => <span className="small muted">{v ? fmtTime(v) : '从未使用'}</span> },
    { title: '创建时间', dataIndex: 'created_at', width: 180, render: (v: string) => <span className="small muted">{fmtTime(v)}</span> },
    {
      title: '操作',
      width: 120,
      render: (_, k) => (
        <Popconfirm
          title={k.disabled ? '重新启用这个 Key？' : '停用这个 Key？使用它的客户端将立即无法认证。'}
          okText={k.disabled ? '启用' : '停用'}
          cancelText="取消"
          onConfirm={() => toggle.mutate({ id: k.id, disabled: !k.disabled })}
        >
          <Button size="small" danger={!k.disabled}>
            {k.disabled ? '启用' : '停用'}
          </Button>
        </Popconfirm>
      ),
    },
  ];

  return (
    <div className="page-card">
      <div className="page-toolbar">
        <Typography.Text strong>API Key</Typography.Text>
        <span className="muted small">Key 只在创建时显示一次；服务端只保存哈希。</span>
        <div style={{ flex: 1 }} />
        <Button icon={<ReloadOutlined />} onClick={() => void keys.refetch()} aria-label="重新加载" />
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setCreating(true)}>
          创建 Key
        </Button>
      </div>
      {keys.isLoading ? (
        <Loading />
      ) : keys.isError ? (
        <Alert type="error" showIcon message={errorText(keys.error)} />
      ) : (
        <Table rowKey="id" size="middle" columns={columns} dataSource={keys.data ?? []} pagination={false} />
      )}

      <Modal
        title="创建 API Key"
        open={creating}
        onCancel={() => {
          setCreating(false);
          form.resetFields();
        }}
        okText="创建"
        cancelText="取消"
        confirmLoading={create.isPending}
        onOk={() => form.submit()}
        destroyOnClose
      >
        <Form
          form={form}
          layout="vertical"
          preserve={false}
          initialValues={{ role: 'member', max_permission_mode: 'ask_all' }}
          onFinish={(v) => create.mutate(v)}
        >
          <Form.Item name="name" label="名称" rules={[{ required: true, message: '请输入名称' }]} extra="用于识别调用方，例如 ci-bot。">
            <Input autoFocus maxLength={80} />
          </Form.Item>
          <Form.Item name="role" label="角色">
            <Select options={roleOptions.map((r) => ({ value: r, label: roleLabel[r] }))} />
          </Form.Item>
          <Form.Item
            name="max_permission_mode"
            label="权限上限"
            extra="该 Key 发起的会话不能超过这个权限模式；ask_all 最严格。"
          >
            <Select
              options={[
                { value: 'ask_all', label: '每次询问（ask_all）' },
                { value: 'ask_external', label: '仅对外操作询问（ask_external）' },
                { value: 'auto', label: '自动执行（auto）' },
              ]}
            />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        title="Key 已创建"
        open={created !== null}
        onCancel={() => setCreated(null)}
        footer={
          <Space>
            <Button
              icon={<CopyOutlined />}
              onClick={() => {
                if (!created) return;
                void navigator.clipboard
                  .writeText(created.plaintext)
                  .then(() => message.success('已复制'))
                  .catch(() => message.error('复制失败，请手动选择文本'));
              }}
            >
              复制
            </Button>
            <Button type="primary" onClick={() => setCreated(null)}>
              我已保存
            </Button>
          </Space>
        }
      >
        <Alert
          type="warning"
          showIcon
          message="这是唯一一次显示明文。关闭后无法再次获取，只能重新创建。"
          style={{ marginBottom: 12 }}
        />
        <Typography.Paragraph copyable={{ text: created?.plaintext ?? '' }} code style={{ wordBreak: 'break-all' }}>
          {created?.plaintext}
        </Typography.Paragraph>
        <Tooltip title="请保存在密钥管理系统或 CI 的加密变量中。">
          <span className="muted small">名称：{created?.key.name}</span>
        </Tooltip>
      </Modal>
    </div>
  );
}