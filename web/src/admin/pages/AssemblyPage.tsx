import { useState } from 'react';
import {
  Alert,
  Button,
  Card,
  Col,
  Descriptions,
  Empty,
  Input,
  InputNumber,
  Row,
  Select,
  Space,
  Statistic,
  Tag,
  Tooltip,
  Typography,
  message,
} from 'antd';
import { PlayCircleOutlined } from '@ant-design/icons';
import { useMutation, useQuery } from '@tanstack/react-query';
import { debugApi, errorText, memoryApi, rolesApi, type Preview, type PreviewInput } from '../../api';
import { Loading } from '../../components/States';

/**
 * Assembly preview (§6): what the model will see for a role, before anything
 * runs. Read-only; nothing is recorded.
 */
export function AssemblyPage() {
  const roles = useQuery({ queryKey: ['assets', '/admin/roles'], queryFn: () => rolesApi.list() });
  const spaces = useQuery({ queryKey: ['memory', 'spaces'], queryFn: memoryApi.listSpaces });
  const [role, setRole] = useState('');
  const [version, setVersion] = useState<number | null>(null);
  const [userID, setUserID] = useState('');
  const [memorySpaces, setMemorySpaces] = useState<string[]>([]);
  const [preview, setPreview] = useState<Preview | null>(null);

  const run = useMutation({
    mutationFn: (input: PreviewInput) => debugApi.assembly(input),
    onSuccess: (p) => setPreview(p),
    onError: (e) => void message.error(errorText(e)),
  });

  const submit = () => {
    const input: PreviewInput = { role };
    if (version != null) input.version = version;
    if (userID.trim()) input.user_id = userID.trim();
    if (memorySpaces.length) input.memory_spaces = memorySpaces;
    run.mutate(input);
  };

  const roleOptions = (roles.data ?? []).map((r) => ({
    value: r.key,
    label: `${r.key}${r.latest_ver ? ` · v${r.latest_ver}` : ' · 仅草稿'}`,
  }));

  return (
    <div>
      <Card size="small" className="page-card">
        <div className="page-toolbar" style={{ flexWrap: 'wrap', gap: 8 }}>
          <Typography.Text strong>组装预览</Typography.Text>
          <span className="muted small">按角色、版本与用户预览系统提示、工具与记忆。不产生会话，不写记录。</span>
        </div>
        <Space wrap align="end">
          <div>
            <div className="muted small">角色</div>
            <Select
              showSearch
              style={{ width: 260 }}
              placeholder="选择角色"
              value={role || undefined}
              onChange={(v) => {
                setRole(v);
                setVersion(null);
              }}
              options={roleOptions}
              loading={roles.isLoading}
            />
          </div>
          <div>
            <div className="muted small">版本（留空 = 最新发布）</div>
            <InputNumber
              style={{ width: 180 }}
              min={0}
              value={version ?? undefined}
              onChange={(v) => setVersion(v ?? null)}
              placeholder="draft=0"
            />
          </div>
          <div>
            <div className="muted small">用户 ID（用户记忆空间）</div>
            <Input style={{ width: 200 }} value={userID} onChange={(e) => setUserID(e.target.value)} placeholder="可选" />
          </div>
          <div>
            <div className="muted small">附加记忆空间</div>
            <Select
              mode="multiple"
              style={{ minWidth: 240 }}
              placeholder="可选"
              value={memorySpaces}
              onChange={setMemorySpaces}
              options={(spaces.data ?? []).map((s) => ({ value: s.name, label: s.name }))}
            />
          </div>
          <Button type="primary" icon={<PlayCircleOutlined />} disabled={!role} loading={run.isPending} onClick={submit}>
            预览
          </Button>
        </Space>
      </Card>

      {run.isError && <Alert style={{ marginTop: 12 }} type="error" showIcon message={errorText(run.error)} />}

      {run.isPending && !preview ? (
        <Loading />
      ) : preview ? (
        <PreviewView p={preview} />
      ) : (
        <div className="page-card" style={{ marginTop: 12 }}>
          <Empty description="选择角色后点击预览" />
        </div>
      )}
    </div>
  );
}

function PreviewView({ p }: { p: Preview }) {
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  return (
    <div style={{ marginTop: 12 }}>
      <Row gutter={[12, 12]}>
        <Col xs={12} md={6}>
          <Card size="small">
            <Statistic title="字符总数" value={p.chars_total} />
            <span className="muted small">模型调用计入的内容</span>
          </Card>
        </Col>
        <Col xs={12} md={6}>
          <Card size="small">
            <Statistic title="估算 Token" value={p.tokens_estimate} />
            <span className="muted small">粗略估算，仅供比较</span>
          </Card>
        </Col>
        <Col xs={12} md={6}>
          <Card size="small">
            <Statistic title="工具描述字符" value={p.tool_chars} />
            <span className="muted small">{p.tools?.length ?? 0} 个工具</span>
          </Card>
        </Col>
        <Col xs={12} md={6}>
          <Card size="small">
            <Statistic title="角色" value={`${p.role_key} v${p.role_ver}`} valueStyle={{ fontSize: 20 }} />
            <Tag color={p.role_status === 'published' ? 'green' : 'gold'}>{p.role_status}</Tag>
          </Card>
        </Col>
      </Row>

      <div className="page-card" style={{ marginTop: 12 }}>
        <Descriptions size="small" column={3} bordered>
          <Descriptions.Item label="模型">{p.model || '（租户默认）'}</Descriptions.Item>
          <Descriptions.Item label="技能">{p.skills?.length ? p.skills.join('、') : '—'}</Descriptions.Item>
          <Descriptions.Item label="截断">{p.truncated?.length ? p.truncated.join('、') : '无'}</Descriptions.Item>
          <Descriptions.Item label="工具" span={2}>
            <Space size={4} wrap>
              {(p.tools ?? []).map((t) => (
                <Tag key={t}>{t}</Tag>
              ))}
              {(p.tools ?? []).length === 0 && <span className="muted">无</span>}
            </Space>
          </Descriptions.Item>
          <Descriptions.Item label="缺失工具">
            {p.missing_tools?.length ? (
              <Space size={4} wrap>
                {p.missing_tools.map((t) => (
                  <Tooltip key={t} title="角色引用了未注册的工具；运行时会从工具集里消失。">
                    <Tag color="red">{t}</Tag>
                  </Tooltip>
                ))}
              </Space>
            ) : (
              <span className="muted">无</span>
            )}
          </Descriptions.Item>
        </Descriptions>
      </div>

      <div className="page-card" style={{ marginTop: 12 }}>
        <div className="page-toolbar">
          <Typography.Text strong>记忆空间</Typography.Text>
        </div>
        {(p.memory_spaces ?? []).length === 0 ? (
          <span className="muted">没有记忆空间。</span>
        ) : (
          <Space size={8} wrap>
            {(p.memory_spaces ?? []).map((s) => (
              <Tag key={`${s.source}-${s.name}`} color={s.found ? 'blue' : 'red'}>
                {s.name} · {s.source} · {s.found ? `${s.entries} 条` : '未找到'}
              </Tag>
            ))}
          </Space>
        )}
      </div>

      <div className="page-card" style={{ marginTop: 12 }}>
        <div className="page-toolbar">
          <Typography.Text strong>组装片段</Typography.Text>
          <div style={{ flex: 1 }} />
          <Button
            size="small"
            onClick={() =>
              setExpanded(Object.fromEntries((p.parts ?? []).map((part, i) => [`${part.name}-${i}`, true])))
            }
          >
            全部展开
          </Button>
          <Button size="small" onClick={() => setExpanded({})}>
            全部折叠
          </Button>
        </div>
        {(p.parts ?? []).length === 0 ? (
          <span className="muted">没有片段。</span>
        ) : (
          (p.parts ?? []).map((part, i) => {
            const k = `${part.name}-${i}`;
            const open = !!expanded[k];
            return (
              <div key={k} style={{ borderTop: '1px solid var(--border)', padding: '8px 0' }}>
                <Space style={{ cursor: 'pointer' }} onClick={() => setExpanded({ ...expanded, [k]: !open })}>
                  <Typography.Text strong>{part.name}</Typography.Text>
                  <span className="muted small">{part.text.length} 字符</span>
                  {part.cache && <Tag color="cyan">缓存</Tag>}
                  <Button size="small" type="link">
                    {open ? '收起' : '展开'}
                  </Button>
                </Space>
                {open && <pre className="part-text">{part.text}</pre>}
              </div>
            );
          })
        )}
      </div>
    </div>
  );
}