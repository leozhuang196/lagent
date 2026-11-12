import { useState } from 'react';
import { Link } from 'react-router-dom';
import { Alert, Badge, Button, Card, Col, Descriptions, Form, Input, InputNumber, Modal, Row, Space, Table, Typography, message } from 'antd';
import { PlusOutlined, ReloadOutlined, SafetyCertificateOutlined } from '@ant-design/icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { errorText, platformApi, type CreateTenantInput, type TenantStatus } from '../../api';
import { Loading } from '../../components/States';
import { fmtTime } from '../../lib/format';
import { useAuth } from '../../auth/AuthContext';

/**
 * Platform overview (platform admins only). Also mounted as the /admin index
 * so tenant admins who land there see something useful instead of a 403.
 */
export function PlatformPage() {
  const { hasRole, switchTenant, tenant } = useAuth();
  const isPlatform = hasRole('platform_admin');
  const qc = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [form] = Form.useForm<CreateTenantInput>();

  const status = useQuery({
    queryKey: ['platform', 'status'],
    queryFn: platformApi.status,
    enabled: isPlatform,
    refetchInterval: 30_000,
  });

  const create = useMutation({
    mutationFn: (input: CreateTenantInput) => platformApi.createTenant(input),
    onSuccess: (t) => {
      void message.success(`租户 ${t.id} 已创建`);
      setCreating(false);
      form.resetFields();
      void qc.invalidateQueries({ queryKey: ['platform'] });
    },
    onError: (e) => void message.error(errorText(e)),
  });

  if (!isPlatform) {
    return (
      <div className="page-card">
        <Alert
          type="info"
          showIcon
          message="这里是租户管理控制台"
          description={
            <span>
              左侧可以维护角色、技能、工作流、记忆空间与集成，在「运行」「调试」「租户」分组里查看运行情况和设置。
              平台级信息仅平台管理员可见。
            </span>
          }
        />
      </div>
    );
  }

  if (status.isLoading) return <Loading />;
  if (status.isError) return <Alert type="error" showIcon message={errorText(status.error)} />;
  const st = status.data;
  if (!st) return <Loading />;

  const tenantCols = [
    {
      title: '租户',
      render: (_: unknown, t: TenantStatus) => (
        <Space direction="vertical" size={0}>
          <Typography.Text strong>{t.tenant_id}</Typography.Text>
          <span className="small muted">{t.name}</span>
        </Space>
      ),
    },
    {
      title: '状态',
      dataIndex: 'status',
      width: 100,
      render: (s: string) => <Badge status={s === 'active' ? 'success' : 'default'} text={s === 'active' ? '启用' : s} />,
    },
    { title: '排队', dataIndex: 'queued', width: 70 },
    { title: '运行', dataIndex: 'running', width: 70 },
    { title: '等待确认', dataIndex: 'suspended', width: 90 },
    { title: '待办', dataIndex: 'open_pendings', width: 70 },
    {
      title: '今日 Token',
      width: 140,
      render: (_: unknown, t: TenantStatus) => (
        <span>
          {t.today_tokens}
          {t.daily_budget > 0 && <span className="muted small"> / {t.daily_budget}</span>}
        </span>
      ),
    },
    {
      title: '操作',
      width: 160,
      render: (_: unknown, t: TenantStatus) => (
        <Button size="small" disabled={tenant === t.tenant_id} onClick={() => switchTenant(t.tenant_id)}>
          {tenant === t.tenant_id ? '当前租户' : '切换到此租户'}
        </Button>
      ),
    },
  ];

  const content = (
    <>
      <Row gutter={[12, 12]}>
        <Col xs={24} md={12} lg={8}>
          <Card size="small">
            <Descriptions size="small" column={1} title="平台信息">
              <Descriptions.Item label="存储">{st.info.store_driver || '—'}</Descriptions.Item>
              <Descriptions.Item label="队列">{st.info.queue_driver || '—'}</Descriptions.Item>
              <Descriptions.Item label="Redis">{st.info.redis ? '已连接' : '未使用'}</Descriptions.Item>
              <Descriptions.Item label="进程角色">
                {(st.info.process_roles ?? []).join('、') || '—'}
              </Descriptions.Item>
              <Descriptions.Item label="默认模型">{st.info.default_model || '—'}</Descriptions.Item>
              <Descriptions.Item label="服务器时间">{fmtTime(st.now)}</Descriptions.Item>
            </Descriptions>
          </Card>
        </Col>
        <Col xs={24} md={12} lg={16}>
          <Card size="small">
            <div className="page-toolbar" style={{ marginBottom: 8 }}>
              <Typography.Text strong>租户</Typography.Text>
              <div style={{ flex: 1 }} />
              <Button size="small" icon={<ReloadOutlined />} onClick={() => void status.refetch()} aria-label="重新加载" />
              <Button size="small" type="primary" icon={<PlusOutlined />} onClick={() => setCreating(true)}>
                创建租户
              </Button>
            </div>
            <Table rowKey="tenant_id" size="small" columns={tenantCols} dataSource={st.tenants ?? []} pagination={false} />
          </Card>
        </Col>
      </Row>
      {(st.notes ?? []).length > 0 && (
        <Alert
          type="warning"
          showIcon
          style={{ marginTop: 12 }}
          message="部署提示"
          description={
            <ul style={{ margin: 0, paddingLeft: 18 }}>
              {(st.notes ?? []).map((n) => (
                <li key={n}>{n}</li>
              ))}
            </ul>
          }
        />
      )}
      <div className="page-card" style={{ marginTop: 12 }}>
        <div className="page-toolbar">
          <Typography.Text strong>最近的失败轮次</Typography.Text>
          <span className="muted small">只显示失败事实与错误码，不含会话内容。</span>
          <div style={{ flex: 1 }} />
          <Link to="/admin/platform/breakglass">
            <Button size="small" icon={<SafetyCertificateOutlined />}>
              跨租户排障
            </Button>
          </Link>
        </div>
        <Table
          rowKey="id"
          size="small"
          pagination={false}
          dataSource={st.recent_failures ?? []}
          columns={[
            { title: '轮次', dataIndex: 'id', width: 120, render: (v: string) => <Typography.Text code className="small">{v.slice(0, 10)}…</Typography.Text> },
            { title: '租户', dataIndex: 'tenant_id', width: 140 },
            { title: '角色', width: 140, render: (_, t) => `${t.role_key} v${t.role_ver}` },
            { title: '模型', dataIndex: 'model', width: 170, render: (v?: string) => <span className="small">{v || '—'}</span> },
            { title: '错误', render: (_, t) => (t.error ? <Typography.Text type="danger">{t.error.message}（{t.error.code}）</Typography.Text> : '—') },
            { title: '结束时间', width: 175, render: (_, t) => <span className="small">{fmtTime(t.ended_at)}</span> },
            {
              title: '',
              width: 90,
              render: (_, t) => (
                <Link to={`/admin/platform/breakglass?turn=${encodeURIComponent(t.id)}`}>
                  <Button size="small">查看</Button>
                </Link>
              ),
            },
          ]}
        />
      </div>
    </>
  );

  return (
    <div>
      {content}
      <Modal
        title="创建租户"
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
        <Form form={form} layout="vertical" preserve={false} onFinish={(v) => create.mutate(v)}>
          <Form.Item
            name="id"
            label="租户 ID"
            rules={[
              { required: true, message: '请输入租户 ID' },
              { pattern: /^[a-z0-9][a-z0-9_-]{1,63}$/, message: '小写字母、数字、下划线和中划线' },
            ]}
            extra="全局唯一，创建后不可修改。"
          >
            <Input className="mono-input" />
          </Form.Item>
          <Form.Item name="name" label="名称" rules={[{ required: true, message: '请输入名称' }]}>
            <Input />
          </Form.Item>
          <Form.Item name="daily_token_budget" label="每日 Token 预算" extra="0 = 不限。">
            <InputNumber min={0} step={100_000} style={{ width: 220 }} />
          </Form.Item>
          <Form.Item name="max_concurrent_turns" label="最大并发轮次" extra="0 = 平台默认。">
            <InputNumber min={0} max={10_000} style={{ width: 220 }} />
          </Form.Item>
          <Form.Item name="default_role" label="默认角色" extra="必须是该租户已发布版本的角色。">
            <Input className="mono-input" placeholder="例如 general" />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
}