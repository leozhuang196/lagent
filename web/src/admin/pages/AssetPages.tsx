import { useEffect, useMemo, useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import {
  Alert,
  Button,
  Descriptions,
  Empty,
  Input,
  Modal,
  Popconfirm,
  Select,
  Space,
  Table,
  Tabs,
  Tag,
  Tooltip,
  Typography,
  message,
  type TableColumnsType,
} from 'antd';
import {
  DeleteOutlined,
  DiffOutlined,
  EditOutlined,
  HistoryOutlined,
  PlusOutlined,
  ReloadOutlined,
  SaveOutlined,
  SendOutlined,
} from '@ant-design/icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { errorText, type DiffResult } from '../../api';
import { Loading } from '../../components/States';
import { fmtTime } from '../../lib/format';
import { AssetStatusTag, BuiltinTag, LayerTag } from '../components/AssetTags';
import { DiffView } from '../components/DiffView';

/** The subset of a versioned asset the release UI needs. */
export interface Versionable {
  id: string;
  key: string;
  ver: number;
  status: string;
  updated_at: string;
  published_at?: string;
  published_by?: { id: string; name?: string };
  note?: string;
}

export interface AssetSummary<E> {
  key: string;
  layer: string;
  builtin: boolean;
  overridden: boolean;
  latest?: E;
  latest_ver: number;
  has_draft: boolean;
  versions: number;
}

export interface AssetDetail<E> {
  key: string;
  builtin: boolean;
  effective?: { layer: string; asset: E };
  draft?: E;
  versions: { layer: string; asset: E }[];
  effective_ver: number;
}

export interface AssetApi<E> {
  list(): Promise<AssetSummary<E>[]>;
  get(key: string): Promise<AssetDetail<E>>;
  diff(key: string, a: number | 'draft', b: number | 'draft'): Promise<DiffResult>;
  saveDraft(key: string, content: Partial<E>): Promise<E>;
  deleteDraft(key: string): Promise<void>;
  publish(key: string, note?: string): Promise<E>;
  rollback(key: string, ver: number, note?: string): Promise<E>;
  setDisabled(key: string, ver: number, disabled: boolean): Promise<E>;
}

export interface AssetPageProps<E extends Versionable> {
  /** Kind for UI copy ("角色" / "技能" / "工作流模板"). */
  kind: string;
  base: string;
  api: AssetApi<E>;
  nameOf(e: E): string;
  descOf?(e: E): string | undefined;
  editor(props: { value: E; onChange(v: E): void }): React.ReactNode;
  /** Content of a brand-new asset with the given key. */
  createEmpty(key: string): E;
}

const DRAFT = 'draft';

/** List page: every key with its effective version and draft state. */
export function AssetListPage<E extends Versionable>(p: AssetPageProps<E>) {
  const nav = useNavigate();
  const qc = useQueryClient();
  const list = useQuery({ queryKey: ['assets', p.base], queryFn: () => p.api.list() });
  const [creating, setCreating] = useState(false);
  const [newKey, setNewKey] = useState('');
  const validKey = /^[a-z0-9][a-z0-9_.-]{0,63}$/.test(newKey);

  const columns: TableColumnsType<AssetSummary<E>> = [
    {
      title: 'Key',
      dataIndex: 'key',
      render: (k: string, row) => (
        <Space size={6} wrap>
          <Link to={`${p.base}/${encodeURIComponent(k)}`} style={{ fontWeight: 600 }}>
            {k}
          </Link>
          <BuiltinTag builtin={row.builtin} overridden={row.overridden} />
        </Space>
      ),
    },
    {
      title: '名称',
      render: (_, row) => (row.latest ? p.nameOf(row.latest) : <span className="muted">（仅草稿）</span>),
    },
    {
      title: '生效版本',
      width: 110,
      render: (_, row) => (row.latest_ver ? <Tag color="green">v{row.latest_ver}</Tag> : <span className="muted">—</span>),
    },
    {
      title: '草稿',
      width: 80,
      render: (_, row) => (row.has_draft ? <Tag color="gold">有</Tag> : <span className="muted">无</span>),
    },
    { title: '版本数', dataIndex: 'versions', width: 80 },
  ];

  const create = () => {
    void qc.invalidateQueries({ queryKey: ['asset', p.base, newKey] });
    setCreating(false);
    setNewKey('');
    nav(`${p.base}/${encodeURIComponent(newKey)}?new=1`);
  };

  return (
    <div className="page-card">
      <div className="page-toolbar">
        <Typography.Text strong>{p.kind}列表</Typography.Text>
        <span className="muted small">草稿 → 发布为新版本；回滚会把旧内容发布成新版本；不再使用请停用，历史不删除。</span>
        <div style={{ flex: 1 }} />
        <Tooltip title="重新加载">
          <Button icon={<ReloadOutlined />} onClick={() => void list.refetch()} aria-label="重新加载" />
        </Tooltip>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setCreating(true)}>
          新建{p.kind}
        </Button>
      </div>
      {list.isLoading ? (
        <Loading />
      ) : list.isError ? (
        <Alert type="error" showIcon message={errorText(list.error)} />
      ) : (
        <Table
          rowKey="key"
          size="middle"
          columns={columns}
          dataSource={list.data ?? []}
          pagination={false}
          onRow={(row) => ({ onClick: () => nav(`${p.base}/${encodeURIComponent(row.key)}`), style: { cursor: 'pointer' } })}
        />
      )}
      <Modal
        title={`新建${p.kind}`}
        open={creating}
        onCancel={() => {
          setCreating(false);
          setNewKey('');
        }}
        okText="开始编辑"
        cancelText="取消"
        okButtonProps={{ disabled: !validKey }}
        onOk={create}
      >
        <Typography.Paragraph type="secondary">
          先保存为草稿（Ver 0），在调试器中验证后再发布。Key 只能包含小写字母、数字、点、下划线和中划线。
        </Typography.Paragraph>
        <Input
          className="mono-input"
          autoFocus
          value={newKey}
          onChange={(e) => setNewKey(e.target.value.trim())}
          placeholder="例如 weekly-report"
          status={newKey && !validKey ? 'error' : undefined}
        />
      </Modal>
    </div>
  );
}

/** Detail page: content, draft editor, versions, diff and release actions. */
export function AssetDetailPage<E extends Versionable>(p: AssetPageProps<E>) {
  const { key = '' } = useParams();
  const [sp, setSp] = useSearchParams();
  const qc = useQueryClient();
  const detail = useQuery({ queryKey: ['asset', p.base, key], queryFn: () => p.api.get(key), enabled: !!key });
  const [draft, setDraft] = useState<E | null>(null);
  const [diffA, setDiffA] = useState<string>('');
  const [diffB, setDiffB] = useState<string>('');
  const [diffOpen, setDiffOpen] = useState(false);
  const [publishing, setPublishing] = useState(false);
  const [note, setNote] = useState('');
  const [tab, setTab] = useState('content');

  const d = detail.data;
  const draftVersion = d?.draft?.ver;
  const effectiveVersion = d?.effective?.asset.ver;
  const isNew = sp.get('new') === '1';

  // Start from the draft when there is one; a new key starts from an empty
  // template. Everything else is read-only until "edit" is pressed.
  useEffect(() => {
    if (!d) return;
    if (d.draft) setDraft(d.draft);
    else if (isNew || !d.effective) setDraft(p.createEmpty(key));
    else setDraft(null);
    setTab('content');
    if (isNew) setSp({}, { replace: true });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [d?.key, draftVersion, effectiveVersion, isNew]);

  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ['asset', p.base, key] });
    void qc.invalidateQueries({ queryKey: ['assets', p.base] });
  };
  const afterWrite = (msg: string) => {
    void message.success(msg);
    refresh();
  };
  const fail = (e: unknown) => void message.error(errorText(e));

  const save = useMutation({ mutationFn: (v: E) => p.api.saveDraft(key, v as never), onSuccess: () => afterWrite('草稿已保存'), onError: fail });
  const publish = useMutation({ mutationFn: (n: string) => p.api.publish(key, n), onSuccess: () => afterWrite('已发布新版本'), onError: fail });
  const discard = useMutation({ mutationFn: () => p.api.deleteDraft(key), onSuccess: () => afterWrite('草稿已删除'), onError: fail });
  const rollback = useMutation({ mutationFn: (v: number) => p.api.rollback(key, v, ''), onSuccess: () => afterWrite('已把旧版本内容发布为新版本'), onError: fail });
  const toggle = useMutation({
    mutationFn: (a: { ver: number; disabled: boolean }) => p.api.setDisabled(key, a.ver, a.disabled),
    onSuccess: () => afterWrite('版本状态已更新'),
    onError: fail,
  });
  const diff = useMutation({
    mutationFn: () => p.api.diff(key, diffA === DRAFT ? DRAFT : Number(diffA), diffB === DRAFT ? DRAFT : Number(diffB)),
    onSuccess: () => setDiffOpen(true),
    onError: fail,
  });

  const versions = useMemo(() => d?.versions ?? [], [d]);
  const opts = useMemo(() => {
    if (!d) return [];
    const out = versions.map((v) => ({
      value: String(v.asset.ver),
      label: `v${v.asset.ver} · ${v.layer === 'builtin' ? '内置' : '租户'}`,
    }));
    if (d.draft) out.unshift({ value: DRAFT, label: '草稿（Ver 0）' });
    return out;
  }, [d, versions]);

  if (detail.isLoading) return <Loading />;
  if (detail.isError) return <Alert type="error" showIcon message={errorText(detail.error)} />;
  if (!d) return <Empty />;

  const startEditing = () => {
    setDraft(d.draft ?? (d.effective ? { ...d.effective.asset } : p.createEmpty(key)));
  };

  const versionColumns: TableColumnsType<E> = [
    { title: '版本', dataIndex: 'ver', width: 70, render: (v: number) => <strong>v{v}</strong> },
    { title: '层', width: 80, render: (_, v) => <LayerTag layer={versions.find((x) => x.asset === v)?.layer} /> },
    { title: '状态', width: 100, render: (_, v) => <AssetStatusTag status={v.status as never} /> },
    { title: '发布时间', width: 170, render: (_, v) => <span className="small">{fmtTime(v.published_at)}</span> },
    {
      title: '发布人',
      width: 120,
      render: (_, v) => <span className="small">{v.published_by?.name || v.published_by?.id || '—'}</span>,
    },
    { title: '说明', render: (_, v) => <span className="small">{v.note || '—'}</span> },
    {
      title: '操作',
      width: 230,
      render: (_, v) => {
        const layer = versions.find((x) => x.asset === v)?.layer;
        if (layer === 'builtin') {
          return (
            <Tooltip title="内置版本只读；发布同 key 的租户版本即可覆盖它">
              <span className="muted small">只读 · 可回滚到它</span>
            </Tooltip>
          );
        }
        return (
          <Space size={4}>
            <Popconfirm title={`把 v${v.ver} 的内容作为新版本发布？`} okText="回滚" cancelText="取消" onConfirm={() => rollback.mutate(v.ver)}>
              <Button size="small" icon={<HistoryOutlined />}>
                回滚
              </Button>
            </Popconfirm>
            <Button size="small" onClick={() => toggle.mutate({ ver: v.ver, disabled: v.status !== 'disabled' })}>
              {v.status === 'disabled' ? '启用' : '停用'}
            </Button>
          </Space>
        );
      },
    },
  ];

  const contentTab = draft ? (
    <div>
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 12 }}
        message={d.draft ? '正在编辑租户草稿。保存不影响线上，发布后才生效。' : '正在编辑新草稿。可先在试验场验证，再发布。'}
      />
      {p.editor({ value: draft, onChange: (v) => setDraft(v) })}
      <Space style={{ marginTop: 12 }} wrap>
        <Button type="primary" icon={<SaveOutlined />} loading={save.isPending} onClick={() => save.mutate(draft)}>
          保存草稿
        </Button>
        {d.draft && (
          <>
            <Button type="primary" ghost icon={<SendOutlined />} onClick={() => setPublishing(true)}>
              发布草稿
            </Button>
            <Popconfirm title="丢弃草稿？" okText="丢弃" cancelText="取消" onConfirm={() => discard.mutate()}>
              <Button danger icon={<DeleteOutlined />} loading={discard.isPending}>
                丢弃草稿
              </Button>
            </Popconfirm>
          </>
        )}
        <Button
          onClick={() => {
            setDraft(d.draft ?? null);
            if (!d.draft) void detail.refetch();
          }}
        >
          取消编辑
        </Button>
      </Space>
    </div>
  ) : d.effective ? (
    <div>
      <div className="page-toolbar">
        <span className="muted small">
          正在查看 v{d.effective_ver}（{d.effective.layer === 'builtin' ? '内置' : '租户'}）。编辑会创建新草稿，不影响此版本。
        </span>
        <div style={{ flex: 1 }} />
        <Button icon={<EditOutlined />} onClick={startEditing}>
          基于此内容编辑草稿
        </Button>
      </div>
      <ReadonlyContent value={d.effective.asset} nameOf={p.nameOf} descOf={p.descOf} />
    </div>
  ) : (
    <Empty description="暂无生效版本" />
  );

  return (
    <div className="page-card">
      <div className="page-toolbar">
        <Link to={p.base}>← 返回{p.kind}列表</Link>
        <Typography.Title level={5} style={{ margin: 0 }}>
          {key}
        </Typography.Title>
        {d.builtin && <Tag>内置 key</Tag>}
        {d.effective && (
          <Tag color="green">
            生效 v{d.effective_ver}（{d.effective.layer === 'builtin' ? '内置' : '租户'}）
          </Tag>
        )}
        <div style={{ flex: 1 }} />
        <Button icon={<ReloadOutlined />} onClick={() => void detail.refetch()} aria-label="重新加载" />
      </div>

      <Tabs
        activeKey={tab}
        onChange={setTab}
        items={[
          { key: 'content', label: draft ? '草稿编辑' : '当前生效内容', children: contentTab },
          {
            key: 'versions',
            label: `版本（${versions.length}）`,
            children: <Table rowKey="id" size="small" columns={versionColumns} dataSource={versions.map((v) => v.asset)} pagination={false} />,
          },
          {
            key: 'diff',
            label: (
              <span>
                <DiffOutlined /> 版本对比
              </span>
            ),
            children:
              opts.length < 2 ? (
                <Empty description="至少需要两个版本（含草稿）才能对比" />
              ) : (
                <div>
                  <Space wrap style={{ marginBottom: 12 }}>
                    <Select style={{ width: 200 }} placeholder="版本 A" options={opts} value={diffA || undefined} onChange={setDiffA} />
                    <span>→</span>
                    <Select style={{ width: 200 }} placeholder="版本 B" options={opts} value={diffB || undefined} onChange={setDiffB} />
                    <Button type="primary" disabled={!diffA || !diffB || diffA === diffB} loading={diff.isPending} onClick={() => diff.mutate()}>
                      对比
                    </Button>
                  </Space>
                  <DiffView changes={diff.data?.changes ?? null} />
                </div>
              ),
          },
          {
            key: 'meta',
            label: '元信息',
            children: (
              <Descriptions column={1} size="small" bordered>
                <Descriptions.Item label="key">{key}</Descriptions.Item>
                <Descriptions.Item label="生效版本">{d.effective_ver || '—'}</Descriptions.Item>
                <Descriptions.Item label="内置">{d.builtin ? '是' : '否'}</Descriptions.Item>
                <Descriptions.Item label="草稿">
                  {d.draft ? `有（Ver 0，更新于 ${fmtTime(d.draft.updated_at)}）` : '无'}
                </Descriptions.Item>
                <Descriptions.Item label="版本数">{versions.length}</Descriptions.Item>
              </Descriptions>
            ),
          },
        ]}
      />

      <Modal
        title={`发布 ${key} 的新版本？`}
        open={publishing}
        onCancel={() => setPublishing(false)}
        okText="确认发布"
        cancelText="取消"
        confirmLoading={publish.isPending}
        onOk={() => {
          publish.mutate(note);
          setPublishing(false);
          setNote('');
        }}
      >
        <Typography.Paragraph type="secondary">
          发布后新会话立即使用新版本；已开始的会话按其钉住的版本继续。发布记录会写入审计日志。
        </Typography.Paragraph>
        <Input placeholder="变更说明（可选）" value={note} onChange={(e) => setNote(e.target.value)} />
      </Modal>

      <Modal title="版本对比" open={diffOpen} onCancel={() => setDiffOpen(false)} footer={null} width={760}>
        <DiffView changes={diff.data?.changes ?? null} />
      </Modal>
    </div>
  );
}

function ReadonlyContent<E extends Versionable>({
  value,
  nameOf,
  descOf,
}: {
  value: E;
  nameOf(e: E): string;
  descOf?(e: E): string | undefined;
}) {
  return (
    <Descriptions column={1} size="small" bordered style={{ maxWidth: 860 }}>
      <Descriptions.Item label="名称">{nameOf(value)}</Descriptions.Item>
      {descOf && <Descriptions.Item label="描述">{descOf(value) || '—'}</Descriptions.Item>}
      <Descriptions.Item label="内容">
        <pre className="code" style={{ maxHeight: 360 }}>
          {JSON.stringify(value, null, 2)}
        </pre>
      </Descriptions.Item>
    </Descriptions>
  );
}