import { Tag } from 'antd';
import type { RiskLevel, SessionStatus } from '../api';
import { riskMeta, sessionStatusMeta, turnStateMeta, type TurnState } from '../lib/labels';

export function RiskTag({ risk }: { risk?: RiskLevel }) {
  if (!risk) return null;
  const m = riskMeta[risk] ?? { label: risk, color: 'red' };
  return <Tag color={m.color}>{m.label}</Tag>;
}

export function TurnStateTag({ state }: { state: TurnState }) {
  const m = turnStateMeta[state] ?? { label: state, color: 'default' };
  return <Tag color={m.color}>{m.label}</Tag>;
}

export function SessionStatusTag({ status }: { status: SessionStatus }) {
  const m = sessionStatusMeta[status] ?? { label: status, color: 'default' };
  return <Tag color={m.color}>{m.label}</Tag>;
}
