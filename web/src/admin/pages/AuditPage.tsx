import { useState } from 'react';
import { Alert, Button, DatePicker, Input, Space, Table, Tag, Typography, type TableColumnsType } from 'antd';
import { ReloadOutlined, SearchOutlined } from '@ant-design/icons';
import type { Dayjs } from 'dayjs';
import { auditApi, errorText, type AuditLog } from '../../api';
import { Loading } from '../../components/States';
import { fmtTime, prettyJson } from '../../lib/format';
import { usePagedList } from '../hooks';
import { roleLabel } from '../../lib/labels';
import { JsonBlock } from '../../components/JsonBlock';

interface Filters {
  action: string;
  target: string;
  actor: string;
  from?: string;
  to?: string;
}

/** Audit log: who did what, when (§11). Newest first. */
export function AuditPage() {
  const [draft, setDraft] = useState<Filters>({ action: '', target: '', actor: '' });
  const [filters, setFilters] = useState<Filters>(draft);
  const [range, setRange] = useState<[Dayjs | null, Dayjs | null] | null>(null);

  const list = usePagedList<AuditLog>(['audit', filters], (after) =>
    auditApi.list({ ...filters, after, limit: 50 }),
  );

  const search = () => setFilters({ ...draft });

  const columns: TableColumnsType<AuditLog> = [
    { title: '时间', dataIndex: 'created_at', width: 175, render: (v: string) => <span className="small">{fmtTime(v)}</span> },
    {
      title: '操作者',
      width: 170,
      render: (_, a) => (
        <Space size={4} direction="vertical">
          <span>{a.actor?.name || a.actor?.id || '—'}</span>
          <span className="small muted">{a.actor ? roleLabel[a.actor.role] ?? a.actor.role : ''}</span>
        </Space>
      ),
    },
    { title: '动作', dataIndex: 'action', width: 200, render: (v: string) => <Typography.Text code>{v}</Typography.Text> },
    {
      title: '对象',
      dataIndex: 'target',
      render: (v: string) => <span className="mono-input" style={{ wordBreak: 'break-all' }}>{v || '—'}</span>,
    },
    {
      title: '说明',
      render: (_, a) => (
        <Space size={4} wrap>
          {a.reason && <Tag color="orange">理由：{a.reason}</Tag>}
          {a.turn_id && <Tag>轮次 {a.turn_id.slice(0, 8)}…</Tag>}
          {a.detail && Object.keys(a.detail).length > 0 && (
            <Typography.Text type="secondary" className="small" ellipsis style={{ maxWidth: 360 }}>
              {prettyJson(a.detail).replace(/\s+/g, ' ').slice(0, 120)}
            </Typography.Text>
          )}
        </Space>
      ),
    },
  ];

  return (
    <div className="page-card">
      <div className="page-toolbar" style={{ flexWrap: 'wrap', gap: 8 }}>
        <Typography.Text strong>审计日志</Typography.Text>
        <div style={{ flex: 1 }} />
        <Input
          allowClear
          prefix={<SearchOutlined />}
          placeholder="动作，例如 role.publish"
          style={{ width: 220 }}
          value={draft.action}
          onChange={(e) => setDraft({ ...draft, action: e.target.value })}
          onPressEnter={search}
        />
        <Input
          allowClear
          placeholder="对象 ID 或 key"
          style={{ width: 200 }}
          value={draft.target}
          onChange={(e) => setDraft({ ...draft, target: e.target.value })}
          onPressEnter={search}
        />
        <Input
          allowClear
          placeholder="操作者 ID"
          style={{ width: 170 }}
          value={draft.actor}
          onChange={(e) => setDraft({ ...draft, actor: e.target.value })}
          onPressEnter={search}
        />
        <DatePicker.RangePicker
          showTime
          value={range}
          onChange={(v) => {
            setRange(v);
            setDraft({
              ...draft,
              from: v?.[0] ? v[0].toISOString() : undefined,
              to: v?.[1] ? v[1].toISOString() : undefined,
            });
          }}
        />
        <Button type="primary" onClick={search}>
          查询
        </Button>
        <Button icon={<ReloadOutlined />} onClick={() => void list.refetch()} aria-label="重新加载" />
      </div>
      {list.isLoading ? (
        <Loading />
      ) : list.isError ? (
        <Alert type="error" showIcon message={errorText(list.error)} />
      ) : (
        <>
          <Table
            rowKey="id"
            size="small"
            columns={columns}
            dataSource={list.items}
            pagination={false}
            expandable={{
              expandedRowRender: (a) => (
                <div>
                  <div className="muted small">
                    {a.actor?.kind} · {a.actor?.tenant_id} · {a.id}
                  </div>
                  <JsonBlock value={a.detail} maxHeight={240} />
                </div>
              ),
              rowExpandable: (a) => !!a.detail && Object.keys(a.detail).length > 0,
            }}
          />
          {list.next && (
            <Button style={{ marginTop: 12 }} loading={list.loadingMore} onClick={() => void list.loadMore()}>
              加载更多
            </Button>
          )}
        </>
      )}
    </div>
  );
}