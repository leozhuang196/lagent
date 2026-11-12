import { Tag } from 'antd';
import type { AssetStatus, RunStatus, StepStatus } from '../../api';

const assetMeta: Record<AssetStatus, { label: string; color: string }> = {
  draft: { label: '草稿', color: 'gold' },
  published: { label: '已发布', color: 'green' },
  disabled: { label: '已停用', color: 'default' },
};

export function AssetStatusTag({ status }: { status?: AssetStatus }) {
  if (!status) return null;
  const m = assetMeta[status] ?? { label: status, color: 'default' };
  return <Tag color={m.color}>{m.label}</Tag>;
}

export function LayerTag({ layer }: { layer?: string }) {
  if (!layer) return null;
  return layer === 'builtin' ? <Tag>内置</Tag> : <Tag color="blue">租户</Tag>;
}

const runMeta: Record<RunStatus, { label: string; color: string }> = {
  pending: { label: '待运行', color: 'default' },
  running: { label: '运行中', color: 'processing' },
  succeeded: { label: '成功', color: 'success' },
  partial: { label: '部分成功', color: 'warning' },
  failed: { label: '失败', color: 'error' },
  cancelled: { label: '已取消', color: 'default' },
};

export function RunStatusTag({ status }: { status: RunStatus }) {
  const m = runMeta[status] ?? { label: status, color: 'default' };
  return <Tag color={m.color}>{m.label}</Tag>;
}

const stepMeta: Record<StepStatus, { label: string; color: string }> = {
  pending: { label: '待运行', color: 'default' },
  dispatched: { label: '已调度', color: 'cyan' },
  running: { label: '运行中', color: 'processing' },
  succeeded: { label: '成功', color: 'success' },
  failed: { label: '失败', color: 'error' },
  skipped: { label: '已跳过', color: 'default' },
  cancelled: { label: '已取消', color: 'default' },
  reused: { label: '复用', color: 'purple' },
};

export function StepStatusTag({ status, reused }: { status: StepStatus; reused?: boolean }) {
  if (reused) return <Tag color="purple">复用自上游运行</Tag>;
  const m = stepMeta[status] ?? { label: status, color: 'default' };
  return <Tag color={m.color}>{m.label}</Tag>;
}

export function BuiltinTag({ builtin, overridden }: { builtin: boolean; overridden?: boolean }) {
  if (!builtin) return <Tag color="blue">租户</Tag>;
  if (overridden) return <Tag color="purple">覆盖内置</Tag>;
  return <Tag>内置</Tag>;
}