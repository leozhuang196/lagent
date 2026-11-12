import { useMemo, useState } from 'react';
import { Alert, Button, Card, Col, Empty, Progress, Row, Select, Statistic, Table, Typography, type TableColumnsType } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { errorText, usageApi, type UsageRow } from '../../api';
import { Loading } from '../../components/States';
import { fmtTime } from '../../lib/format';

function fmtTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(2)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`;
  return String(n);
}

/** Usage per day / role / model (§11). Debug turns are reported separately. */
export function UsagePage() {
  const [days, setDays] = useState(7);
  const usage = useQuery({ queryKey: ['usage', days], queryFn: () => usageApi.get(days) });
  const rep = usage.data;

  // Day roll-up keeps the table readable when several roles share a day.
  const byDay = useMemo(() => {
    const m = new Map<string, { day: string; turns: number; failures: number; tokens: number }>();
    for (const r of rep?.rows ?? []) {
      const cur = m.get(r.day) ?? { day: r.day, turns: 0, failures: 0, tokens: 0 };
      cur.turns += r.turns;
      cur.failures += r.failures;
      cur.tokens += r.tokens;
      m.set(r.day, cur);
    }
    return [...m.values()].sort((a, b) => (a.day < b.day ? 1 : -1));
  }, [rep?.rows]);

  const columns: TableColumnsType<UsageRow> = [
    { title: '日期', dataIndex: 'day', width: 120 },
    { title: '角色', dataIndex: 'role', width: 160, render: (v: string) => <span className="mono-input">{v || '—'}</span> },
    { title: '模型', dataIndex: 'model', render: (v: string) => <span className="small">{v || '—'}</span> },
    { title: '轮次', dataIndex: 'turns', width: 80 },
    { title: '失败', dataIndex: 'failures', width: 80, render: (v: number) => (v ? <Typography.Text type="danger">{v}</Typography.Text> : 0) },
    {
      title: 'Token',
      dataIndex: 'tokens',
      width: 110,
      render: (v: number, r) => (
        <span title={`输入 ${r.usage.input_tokens} / 输出 ${r.usage.output_tokens} / 缓存读 ${r.usage.cache_read_tokens} / 缓存写 ${r.usage.cache_write_tokens}`}>
          {fmtTokens(v)}
        </span>
      ),
    },
    {
      title: '输入 / 输出 / 缓存',
      render: (_, r) => (
        <span className="small muted">
          {fmtTokens(r.usage.input_tokens)} / {fmtTokens(r.usage.output_tokens)} / {fmtTokens(r.usage.cache_read_tokens)}
        </span>
      ),
    },
  ];

  return (
    <div>
      <div className="page-toolbar" style={{ marginBottom: 12 }}>
        <Typography.Text strong>用量</Typography.Text>
        <div style={{ flex: 1 }} />
        <Select
          value={days}
          onChange={setDays}
          style={{ width: 130 }}
          options={[
            { value: 1, label: '今天' },
            { value: 7, label: '最近 7 天' },
            { value: 14, label: '最近 14 天' },
            { value: 30, label: '最近 30 天' },
            { value: 90, label: '最近 90 天' },
          ]}
        />
        <Button icon={<ReloadOutlined />} onClick={() => void usage.refetch()} aria-label="重新加载" />
      </div>
      {usage.isLoading ? (
        <Loading />
      ) : usage.isError ? (
        <Alert type="error" showIcon message={errorText(usage.error)} />
      ) : !rep ? (
        <Empty />
      ) : (
        <>
          {rep.truncated && (
            <Alert type="warning" showIcon message="扫描的轮次超过上限，统计只覆盖最近的一部分。" style={{ marginBottom: 12 }} />
          )}
          <Row gutter={[12, 12]}>
            <Col xs={24} sm={12} lg={6}>
              <Card size="small">
                <Statistic title="今日实时用量（预算口径）" value={fmtTokens(rep.today.live_tokens)} />
                {rep.today.daily_budget > 0 ? (
                  <Progress
                    percent={Math.min(100, Math.round((rep.today.live_tokens / rep.today.daily_budget) * 100))}
                    status={rep.today.live_tokens >= rep.today.daily_budget ? 'exception' : 'normal'}
                    format={() => `${fmtTokens(rep.today.live_tokens)} / ${fmtTokens(rep.today.daily_budget)}`}
                  />
                ) : (
                  <span className="muted small">未设置每日预算</span>
                )}
              </Card>
            </Col>
            <Col xs={24} sm={12} lg={6}>
              <Card size="small">
                <Statistic title={`业务轮次（${rep.days} 天）`} value={rep.total_turns} />
                <span className="muted small">不含调试轮次</span>
              </Card>
            </Col>
            <Col xs={24} sm={12} lg={6}>
              <Card size="small">
                <Statistic title={`业务 Token（${rep.days} 天）`} value={fmtTokens(rep.total_tokens)} />
                <span className="muted small">从 {fmtTime(rep.from)} 起</span>
              </Card>
            </Col>
            <Col xs={24} sm={12} lg={6}>
              <Card size="small">
                <Statistic title="调试轮次 / Token" value={`${rep.debug_turns} / ${fmtTokens(rep.debug_tokens)}`} />
                <span className="muted small">试验场与调试运行，独立统计</span>
              </Card>
            </Col>
          </Row>

          <div className="page-card" style={{ marginTop: 12 }}>
            <div className="page-toolbar">
              <Typography.Text strong>按天</Typography.Text>
              <span className="muted small">日期为 UTC。</span>
            </div>
            <Table
              rowKey="day"
              size="small"
              pagination={false}
              dataSource={byDay}
              columns={[
                { title: '日期', dataIndex: 'day' },
                { title: '轮次', dataIndex: 'turns', width: 100 },
                { title: '失败', dataIndex: 'failures', width: 100 },
                { title: 'Token', dataIndex: 'tokens', width: 140, render: (v: number) => fmtTokens(v) },
              ]}
            />
          </div>

          <div className="page-card" style={{ marginTop: 12 }}>
            <div className="page-toolbar">
              <Typography.Text strong>按天 × 角色 × 模型</Typography.Text>
            </div>
            <Table rowKey={(r) => `${r.day}/${r.role}/${r.model}`} size="small" columns={columns} dataSource={rep.rows ?? []} pagination={{ pageSize: 20 }} />
          </div>
        </>
      )}
    </div>
  );
}