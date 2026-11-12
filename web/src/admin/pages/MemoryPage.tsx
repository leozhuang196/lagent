import { useEffect, useState } from 'react';
import {
  Alert,
  Button,
  Descriptions,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Typography,
  message,
  type TableColumnsType,
} from 'antd';
import { DeleteOutlined, EditOutlined, PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { errorText, memoryApi, type MemoryEntry, type SpaceInput, type SpaceView } from '../../api';
import { fmtTime } from '../../lib/format';
import { Loading } from '../../components/States';

/** Memory spaces: admins maintain read-only spaces; agents may write the rest. */
export function MemoryPage() {
  const qc = useQueryClient();
  const spaces = useQuery({ queryKey: ['memory', 'spaces'], queryFn: memoryApi.listSpaces });
  const [selected, setSelected] = useState<string | null>(null);
  const [editing, setEditing] = useState<SpaceView | 'new' | null>(null);

  const current = selected ?? spaces.data?.[0]?.name ?? null;

  return (
    <div style={{ display: 'grid', gridTemplateColumns: '300px 1fr', gap: 16, alignItems: 'start' }}>
      <div className="page-card">
        <div className="page-toolbar">
          <Typography.Text strong>记忆空间</Typography.Text>
          <div style={{ flex: 1 }} />
          <Button size="small" icon={<ReloadOutlined />} onClick={() => void spaces.refetch()} aria-label="重新加载" />
          <Button size="small" type="primary" icon={<PlusOutlined />} onClick={() => setEditing('new')}>
            新建
          </Button>
        </div>
        {spaces.isLoading ? (
          <Loading />
        ) : spaces.isError ? (
          <Alert type="error" showIcon message={errorText(spaces.error)} />
        ) : (spaces.data ?? []).length === 0 ? (
          <Alert type="info" showIcon message="还没有记忆空间。角色通过 memory_spaces 使用它们。" />
        ) : (
          <Table
            rowKey="name"
            size="small"
            pagination={false}
            dataSource={spaces.data ?? []}
            onRow={(row) => ({ onClick: () => setSelected(row.name), style: { cursor: 'pointer' } })}
            rowClassName={(row) => (row.name === current ? 'session-item active' : '')}
            columns={[
              {
                title: '名称',
                dataIndex: 'name',
                render: (n: string) => (
                  <Space size={4} wrap>
                    <strong>{n}</strong>
                    {rowBadge(spaces.data ?? [], n)}
                  </Space>
                ),
              },
              {
                title: '条目',
                dataIndex: 'entries',
                width: 70,
              },
              {
                title: '',
                width: 50,
                render: (_, row) => (
                  <Button
                    size="small"
                    type="text"
                    icon={<EditOutlined />}
                    aria-label={`编辑 ${row.name}`}
                    onClick={(e) => {
                      e.stopPropagation();
                      setEditing(row);
                    }}
                  />
                ),
              },
            ]}
          />
        )}
      </div>
      <div style={{ minWidth: 0 }}>
        {current ? (
          <SpaceDetail space={current} onEdit={() => setEditing(spaces.data?.find((s) => s.name === current) ?? null)} />
        ) : (
          <div className="page-card muted">选择一个记忆空间。</div>
        )}
      </div>

      <SpaceModal
        editing={editing}
        onClose={() => setEditing(null)}
        onSaved={(name) => {
          setEditing(null);
          setSelected(name);
          void qc.invalidateQueries({ queryKey: ['memory'] });
        }}
      />
    </div>
  );
}

function rowBadge(spaces: SpaceView[], name: string) {
  const sp = spaces.find((s) => s.name === name);
  if (!sp) return null;
  return (
    <>
      {sp.scope === 'user' ? <Tag color="purple">用户 {sp.owner_id}</Tag> : <Tag>租户</Tag>}
      {sp.read_only && <Tag>只读</Tag>}
    </>
  );
}

function SpaceDetail({ space, onEdit }: { space: string; onEdit(): void }) {
  const qc = useQueryClient();
  const [prefix, setPrefix] = useState('');
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const [all, setAll] = useState<MemoryEntry[]>([]);
  const entries = useQuery({
    queryKey: ['memory', 'entries', space, prefix, cursor],
    queryFn: () => memoryApi.listEntries(space, { prefix: prefix || undefined, after: cursor, limit: 50 }),
  });
  useEffect(() => {
    setCursor(undefined);
    setAll([]);
  }, [space, prefix]);
  useEffect(() => {
    if (!entries.data) return;
    // Pages are appended in cursor order; a refetch of the first page replaces them.
    if (!cursor) setAll(entries.data.items ?? []);
    else setAll((prev) => [...prev, ...(entries.data.items ?? [])]);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [entries.data]);

  const [editing, setEditing] = useState<MemoryEntry | 'new' | null>(null);
  const del = useMutation({
    mutationFn: (key: string) => memoryApi.deleteEntry(space, key),
    onSuccess: () => {
      void message.success('已删除');
      void qc.invalidateQueries({ queryKey: ['memory'] });
    },
    onError: (e) => void message.error(errorText(e)),
  });

  const columns: TableColumnsType<MemoryEntry> = [
    { title: 'Key', dataIndex: 'key', width: 220, render: (k: string) => <Typography.Text code>{k}</Typography.Text> },
    {
      title: '内容',
      dataIndex: 'content',
      render: (c: string) => <div className="pre-wrap" style={{ maxHeight: 96, overflow: 'auto', fontSize: 13 }}>{c}</div>,
    },
    { title: '优先级', dataIndex: 'priority', width: 80 },
    { title: '来源回合', width: 130, render: (_, e) => <span className="small muted">{e.source_turn_id || '管理员'}</span> },
    { title: '更新', width: 170, render: (_, e) => <span className="small muted">{fmtTime(e.updated_at)}</span> },
    {
      title: '',
      width: 100,
      render: (_, e) => (
        <Space size={2}>
          <Button size="small" type="text" icon={<EditOutlined />} onClick={() => setEditing(e)} aria-label="编辑" />
          <Popconfirm title="删除这个条目？" okText="删除" cancelText="取消" onConfirm={() => del.mutate(e.key)}>
            <Button size="small" type="text" danger icon={<DeleteOutlined />} aria-label="删除" />
          </Popconfirm>
        </Space>
      ),
    },
  ];

  return (
    <div className="page-card">
      <div className="page-toolbar">
        <Typography.Text strong>{space}</Typography.Text>
        <Button size="small" onClick={onEdit}>
          空间设置
        </Button>
        <div style={{ flex: 1 }} />
        <Input.Search allowClear placeholder="按 key 前缀筛选" style={{ width: 240 }} onSearch={setPrefix} />
        <Button size="small" icon={<ReloadOutlined />} onClick={() => void entries.refetch()} aria-label="重新加载" />
        <Button type="primary" size="small" icon={<PlusOutlined />} onClick={() => setEditing('new')}>
          新增条目
        </Button>
      </div>
      {entries.isLoading && all.length === 0 ? (
        <Loading />
      ) : entries.isError ? (
        <Alert type="error" showIcon message={errorText(entries.error)} />
      ) : (
        <>
          <Table rowKey="id" size="small" columns={columns} dataSource={all} pagination={false} />
          {entries.data?.next && (
            <Button style={{ marginTop: 12 }} loading={entries.isFetching} onClick={() => setCursor(entries.data?.next)}>
              加载更多
            </Button>
          )}
        </>
      )}
      <EntryModal space={space} editing={editing} onClose={() => setEditing(null)} />
    </div>
  );
}

function EntryModal({ space, editing, onClose }: { space: string; editing: MemoryEntry | 'new' | null; onClose(): void }) {
  const qc = useQueryClient();
  const [form] = Form.useForm<{ key: string; content: string; priority: number }>();
  const isNew = editing === 'new';
  useEffect(() => {
    if (!editing) return;
    if (isNew) form.setFieldsValue({ key: '', content: '', priority: 0 });
    else form.setFieldsValue({ key: editing.key, content: editing.content, priority: editing.priority });
  }, [editing, isNew, form]);
  const save = useMutation({
    mutationFn: (v: { key: string; content: string; priority: number }) =>
      isNew ? memoryApi.putEntry(space, v) : memoryApi.updateEntry(space, v.key, v),
    onSuccess: () => {
      void message.success('已保存');
      void qc.invalidateQueries({ queryKey: ['memory'] });
      onClose();
    },
    onError: (e) => void message.error(errorText(e)),
  });
  return (
    <Modal
      title={isNew ? `新增条目 · ${space}` : `编辑 ${editing ? editing.key : ''}`}
      open={editing !== null}
      onCancel={onClose}
      okText="保存"
      cancelText="取消"
      confirmLoading={save.isPending}
      onOk={() => form.submit()}
    >
      <Form form={form} layout="vertical" onFinish={(v) => save.mutate(v)}>
        <Form.Item name="key" label="Key" rules={[{ required: true, message: '请输入 key' }]}>
          <Input className="mono-input" disabled={!isNew} />
        </Form.Item>
        <Form.Item name="content" label="内容" rules={[{ required: true, message: '请输入内容' }]}>
          <Input.TextArea rows={8} />
        </Form.Item>
        <Form.Item name="priority" label="优先级" extra="数值越大越靠前进入上下文。">
          <InputNumber min={-1000} max={1000} />
        </Form.Item>
      </Form>
    </Modal>
  );
}

function SpaceModal({
  editing,
  onClose,
  onSaved,
}: {
  editing: SpaceView | 'new' | null;
  onClose(): void;
  onSaved(name: string): void;
}) {
  const qc = useQueryClient();
  const [form] = Form.useForm<{ name: string; scope: string; owner_id?: string; read_only: boolean; priority: number }>();
  const isNew = editing === 'new';
  useEffect(() => {
    if (!editing) return;
    if (isNew) form.setFieldsValue({ name: '', scope: 'tenant', owner_id: '', read_only: false, priority: 0 });
    else form.setFieldsValue({
      name: editing.name,
      scope: editing.scope,
      owner_id: editing.owner_id,
      read_only: editing.read_only,
      priority: editing.priority,
    });
  }, [editing, isNew, form]);
  const save = useMutation({
    mutationFn: (v: { name: string; scope: string; owner_id?: string; read_only: boolean; priority: number }) => {
      const input: SpaceInput = {
        scope: v.scope as 'tenant' | 'user',
        owner_id: v.scope === 'user' ? v.owner_id : '',
        read_only: v.read_only,
        priority: v.priority,
      };
      return isNew ? memoryApi.createSpace({ ...input, name: v.name }) : memoryApi.updateSpace(v.name, input);
    },
    onSuccess: (sp) => {
      void message.success('已保存');
      void qc.invalidateQueries({ queryKey: ['memory'] });
      onSaved(sp.name);
    },
    onError: (e) => void message.error(errorText(e)),
  });
  const del = useMutation({
    mutationFn: (name: string) => memoryApi.deleteSpace(name),
    onSuccess: () => {
      void message.success('空间已删除');
      void qc.invalidateQueries({ queryKey: ['memory'] });
      onClose();
    },
    onError: (e) => void message.error(errorText(e)),
  });
  const scope = Form.useWatch('scope', form);
  return (
    <Modal
      title={isNew ? '新建记忆空间' : '记忆空间设置'}
      open={editing !== null}
      onCancel={onClose}
      okText="保存"
      cancelText="取消"
      confirmLoading={save.isPending}
      onOk={() => form.submit()}
      footer={(_, { OkBtn, CancelBtn }) => (
        <Space style={{ width: '100%', justifyContent: 'space-between' }}>
          <span>
            {!isNew && editing && (
              <Popconfirm title={`删除空间 ${editing.name}？其条目将一并删除，不可恢复。`} okText="删除" cancelText="取消" onConfirm={() => del.mutate(editing.name)}>
                <Button danger icon={<DeleteOutlined />}>
                  删除空间
                </Button>
              </Popconfirm>
            )}
          </span>
          <span>
            <CancelBtn />
            <OkBtn />
          </span>
        </Space>
      )}
    >
      <Form form={form} layout="vertical" onFinish={(v) => save.mutate(v)}>
        <Form.Item name="name" label="名称" rules={[{ required: true, pattern: /^[a-z0-9][a-z0-9_.-]{0,63}$/, message: '小写字母、数字、点、下划线、中划线' }]}>
          <Input className="mono-input" disabled={!isNew} />
        </Form.Item>
        <Form.Item name="scope" label="归属">
          <Select
            options={[
              { value: 'tenant', label: '租户共享' },
              { value: 'user', label: '单个用户' },
            ]}
          />
        </Form.Item>
        {scope === 'user' && (
          <Form.Item name="owner_id" label="用户 ID" rules={[{ required: true, message: '用户空间必须指定 owner_id' }]}>
            <Input className="mono-input" />
          </Form.Item>
        )}
        <Form.Item name="read_only" label="只读" valuePropName="checked" extra="只读空间只能由管理员维护，remember 工具不能写入。">
          <Switch />
        </Form.Item>
        <Form.Item name="priority" label="优先级">
          <InputNumber min={-1000} max={1000} />
        </Form.Item>
        {!isNew && editing && (
          <Descriptions size="small" column={1}>
            <Descriptions.Item label="条目数">{editing.entries}</Descriptions.Item>
            <Descriptions.Item label="创建时间">{fmtTime(editing.created_at)}</Descriptions.Item>
          </Descriptions>
        )}
      </Form>
    </Modal>
  );
}