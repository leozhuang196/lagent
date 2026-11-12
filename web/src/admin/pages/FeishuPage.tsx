import { useState } from 'react';
import { Link } from 'react-router-dom';
import {
  Alert,
  Button,
  Form,
  Input,
  Modal,
  Popconfirm,
  Space,
  Table,
  Tag,
  Typography,
  message,
  type TableColumnsType,
} from 'antd';
import { PlusOutlined, ReloadOutlined, SearchOutlined } from '@ant-design/icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { errorText, feishuApi, rolesApi, type FeishuChat, type FeishuThread } from '../../api';
import { Loading } from '../../components/States';
import { fmtTime } from '../../lib/format';

/**
 * Feishu chat bindings: a bound group routes each thread to a session with
 * the chat's default role (§9.4). Threads listed by session make it possible
 * to trace a Feishu conversation from the console.
 */
export function FeishuPage() {
  const qc = useQueryClient();
  const chats = useQuery({ queryKey: ['feishu', 'chats'], queryFn: feishuApi.listChats });
  const roles = useQuery({ queryKey: ['assets', '/admin/roles'], queryFn: () => rolesApi.list() });
  const [binding, setBinding] = useState(false);
  const [threadSession, setThreadSession] = useState('');
  const [threadQuery, setThreadQuery] = useState('');
  const [form] = Form.useForm<{ chat_id: string; name?: string; default_role?: string }>();

  const bind = useMutation({
    mutationFn: (v: { chat_id: string; name?: string; default_role?: string }) =>
      feishuApi.bindChat({ chat_id: v.chat_id.trim(), name: v.name?.trim() || undefined, default_role: v.default_role?.trim() || undefined }),
    onSuccess: () => {
      void message.success('已绑定');
      setBinding(false);
      form.resetFields();
      void qc.invalidateQueries({ queryKey: ['feishu'] });
    },
    onError: (e) => void message.error(errorText(e)),
  });
  const unbind = useMutation({
    mutationFn: (id: string) => feishuApi.unbindChat(id),
    onSuccess: () => {
      void message.success('已解绑；之后的群消息不会再进入本租户');
      void qc.invalidateQueries({ queryKey: ['feishu'] });
    },
    onError: (e) => void message.error(errorText(e)),
  });

  const threads = useQuery({
    queryKey: ['feishu', 'threads', threadQuery],
    queryFn: () => feishuApi.listThreads(threadQuery.trim()),
    enabled: !!threadQuery.trim(),
  });

  const columns: TableColumnsType<FeishuChat> = [
    {
      title: '群',
      render: (_, c) => (
        <Space direction="vertical" size={0}>
          <strong>{c.name || c.chat_id}</strong>
          <Typography.Text code className="small">
            {c.chat_id}
          </Typography.Text>
        </Space>
      ),
    },
    { title: '默认角色', dataIndex: 'default_role', width: 180, render: (v?: string) => v || <span className="muted">租户默认</span> },
    {
      title: '状态',
      width: 100,
      render: (_, c) => (c.bound ? <Tag color="green">已绑定</Tag> : <Tag>已解绑</Tag>),
    },
    { title: '绑定时间', dataIndex: 'created_at', width: 180, render: (v: string) => <span className="small">{fmtTime(v)}</span> },
    {
      title: '操作',
      width: 100,
      render: (_, c) =>
        c.bound ? (
          <Popconfirm title="解绑这个群？" okText="解绑" cancelText="取消" onConfirm={() => unbind.mutate(c.id)}>
            <Button size="small">解绑</Button>
          </Popconfirm>
        ) : (
          <Button
            size="small"
            onClick={() =>
              void feishuApi
                .bindChat({ chat_id: c.chat_id, name: c.name, default_role: c.default_role })
                .then(() => qc.invalidateQueries({ queryKey: ['feishu'] }))
                .catch((e) => message.error(errorText(e)))
            }
          >
            重新绑定
          </Button>
        ),
    },
  ];

  return (
    <div>
      <div className="page-card">
        <div className="page-toolbar">
          <Typography.Text strong>飞书群绑定</Typography.Text>
          <span className="muted small">绑定后该群的每个话题（thread）对应一个会话。</span>
          <div style={{ flex: 1 }} />
          <Button icon={<ReloadOutlined />} onClick={() => void chats.refetch()} aria-label="重新加载" />
          <Button type="primary" icon={<PlusOutlined />} onClick={() => setBinding(true)}>
            绑定群
          </Button>
        </div>
        {chats.isLoading ? (
          <Loading />
        ) : chats.isError ? (
          <Alert type="error" showIcon message={errorText(chats.error)} />
        ) : (
          <Table rowKey="id" size="small" columns={columns} dataSource={chats.data ?? []} pagination={false} />
        )}
      </div>

      <div className="page-card" style={{ marginTop: 12 }}>
        <div className="page-toolbar">
          <Typography.Text strong>话题绑定</Typography.Text>
          <span className="muted small">按会话 ID 查询飞书话题。</span>
          <Input
            allowClear
            prefix={<SearchOutlined />}
            placeholder="会话 ID"
            style={{ width: 320 }}
            value={threadSession}
            onChange={(e) => setThreadSession(e.target.value)}
            onPressEnter={() => setThreadQuery(threadSession)}
          />
          <Button type="primary" disabled={!threadSession.trim()} onClick={() => setThreadQuery(threadSession)}>
            查询
          </Button>
        </div>
        {!threadQuery ? (
          <span className="muted">输入会话 ID 查询该会话关联的飞书话题。</span>
        ) : threads.isLoading ? (
          <Loading />
        ) : threads.isError ? (
          <Alert type="error" showIcon message={errorText(threads.error)} />
        ) : (
          <Table<FeishuThread>
            rowKey="id"
            size="small"
            pagination={false}
            dataSource={threads.data ?? []}
            columns={[
              { title: '群 ID', dataIndex: 'chat_id', width: 260, render: (v: string) => <Typography.Text code>{v}</Typography.Text> },
              { title: '根消息', dataIndex: 'root_msg_id', width: 220, render: (v: string) => <span className="small">{v}</span> },
              {
                title: '会话',
                dataIndex: 'session_id',
                render: (v: string) => (
                  <Link to={`/c/${v}`}>
                    <Typography.Text code className="small">
                      {v.slice(0, 12)}…
                    </Typography.Text>
                  </Link>
                ),
              },
            ]}
          />
        )}
      </div>

      <Modal
        title="绑定飞书群"
        open={binding}
        onCancel={() => {
          setBinding(false);
          form.resetFields();
        }}
        okText="绑定"
        cancelText="取消"
        confirmLoading={bind.isPending}
        onOk={() => form.submit()}
        destroyOnClose
      >
        <Form form={form} layout="vertical" preserve={false} onFinish={(v) => bind.mutate(v)}>
          <Form.Item name="chat_id" label="群 ID" rules={[{ required: true, message: '请输入群 ID' }]} extra="飞书的 chat_id（oc_…）。">
            <Input className="mono-input" autoFocus placeholder="oc_xxxxxxxx" />
          </Form.Item>
          <Form.Item name="name" label="显示名称">
            <Input placeholder="可选，例如：产品周报群" />
          </Form.Item>
          <Form.Item name="default_role" label="默认角色" extra="该群新话题使用的角色；留空使用租户默认角色。">
            <Input className="mono-input" placeholder="角色 key" />
          </Form.Item>
          {(roles.data ?? []).length > 0 && (
            <div className="small muted">可用角色：{(roles.data ?? []).map((r) => r.key).join('、')}</div>
          )}
        </Form>
      </Modal>
    </div>
  );
}