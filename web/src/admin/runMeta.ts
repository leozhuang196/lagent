import type { RunStatus } from '../api';

/** Shared labels for workflow runs and their steps. */
export const runStatusMeta: Record<RunStatus, { label: string; color: string }> = {
  pending: { label: '待运行', color: 'default' },
  running: { label: '运行中', color: 'processing' },
  succeeded: { label: '成功', color: 'success' },
  partial: { label: '部分成功', color: 'warning' },
  failed: { label: '失败', color: 'error' },
  cancelled: { label: '已取消', color: 'default' },
};

export const runStatusOptions = (Object.keys(runStatusMeta) as RunStatus[]).map((s) => ({
  value: s,
  label: runStatusMeta[s].label,
}));

/** A run that will not change any more. */
export function isTerminal(s: RunStatus): boolean {
  return s === 'succeeded' || s === 'partial' || s === 'failed' || s === 'cancelled';
}