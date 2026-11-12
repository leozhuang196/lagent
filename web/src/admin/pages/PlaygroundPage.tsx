import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Alert, Button, Card, Input, InputNumber, Select, Space, Tag, Typography, message } from 'antd';
import { ExperimentOutlined } from '@ant-design/icons';
import { useMutation, useQuery } from '@tanstack/react-query';
import { debugApi, errorText, memoryApi, rolesApi, type PlaygroundInput } from '../../api';

/**
 * Playground (§11): opens a debug session against a chosen role version
 * (drafts included) and hands over to the chat, where it behaves like any
 * other session but is excluded from usage statistics.
 */
export function PlaygroundPage() {
  const nav = useNavigate();
  const roles = useQuery({ queryKey: ['assets', '/admin/roles'], queryFn: () => rolesApi.list() });
  const spaces = useQuery({ queryKey: ['memory', 'spaces'], queryFn: () => memoryApi.listSpaces() });
  const [role, setRole] = useState('');
  const [version, setVersion] = useState<number | null>(null);
  const [title, setTitle] = useState('');
  const [memorySpaces, setMemorySpaces] = useState<string[]>([]);

  const create = useMutation({
    mutationFn: (input: PlaygroundInput) => debugApi.playground(input),
    onSuccess: (s) => nav(`/c/${s.id}`),
    onError: (e) => void message.error(errorText(e)),
  });

  const open = () => {
    const in_: PlaygroundInput = { role };
    if (version != null) in_.version = version;
    if (title.trim()) in_.title = title.trim();
    if (memorySpaces.length) in_.memory_spaces = memorySpaces;
    create.mutate(in_);
  };

  const selected = (roles.data ?? []).find((r) => r.key === role);
  return (
    <div className="page-card" style={{ maxWidth: 760 }}>
      <div className="page-toolbar">
        <Typography.Text strong>试验场</Typography.Text>
        <span className="muted small">
          用草稿版本（Ver 0）直接开始一场对话；调试会话不计入业务用量，也不发送到外部渠道。
        </span>
      </div>
      {roles.isError && <Alert type="error" showIcon message={errorText(roles.error)} style={{ marginBottom: 12 }} />}
      <Space direction="vertical" size={12} style={{ width: '100%' }}>
        <div>
          <div className="muted small">角色</div>
          <Select
            showSearch
            style={{ width: '100%' }}
            placeholder="选择角色"
            value={role || undefined}
            onChange={(v) => {
              setRole(v);
              setVersion(null);
            }}
            loading={roles.isLoading}
            options={(roles.data ?? []).map((r) => ({
              value: r.key,
              label: `${r.key}${r.has_draft ? ' · 有草稿' : ''}${r.latest_ver ? ` · v${r.latest_ver}` : ''}`,
            }))}
          />
        </div>
        {selected && (
          <Space size={4} wrap>
            {selected.builtin && <Tag>内置</Tag>}
            {selected.overridden && <Tag color="purple">覆盖内置</Tag>}
            {selected.has_draft && <Tag color="gold">草稿 Ver 0 可用</Tag>}
          </Space>
        )}
        <Space size={12} wrap align="end">
          <div>
            <div className="muted small">版本</div>
            <InputNumber
              style={{ width: 200 }}
              min={0}
              value={version ?? undefined}
              onChange={(v) => setVersion(v ?? null)}
              placeholder={selected?.has_draft ? '0 = 草稿' : '留空 = 最新发布'}
            />
          </div>
          <div>
            <div className="muted small">标题</div>
            <Input style={{ width: 260 }} value={title} onChange={(e) => setTitle(e.target.value)} placeholder="可选" />
          </div>
        </Space>
        <div>
          <div className="muted small">附加记忆空间</div>
          <Select
            mode="multiple"
            style={{ width: '100%' }}
            placeholder="角色自带的空间之外，再挂载的空间"
            value={memorySpaces}
            onChange={setMemorySpaces}
            options={(spaces.data ?? []).map((s) => ({ value: s.name, label: s.name }))}
          />
        </div>
        <Card size="small">
          <Typography.Text type="secondary" className="small">
            调试会话与普通会话共用同一套执行路径；区别只在记录上打 debug 标记。验证无误后回到「角色」页发布草稿。
            会话的权限模式由角色决定，创建后不可提升。
          </Typography.Text>
        </Card>
        <Button
          type="primary"
          icon={<ExperimentOutlined />}
          disabled={!role}
          loading={create.isPending}
          onClick={open}
        >
          开始调试会话
        </Button>
      </Space>
    </div>
  );
}