import type { Decision, PendingKind, RiskLevel, Role, ScheduleOutcome, SessionStatus, TurnStatus, TurnView } from '../api';

export const roleLabel: Record<Role, string> = {
  member: '成员',
  tenant_admin: '租户管理员',
  platform_admin: '平台管理员',
};

export const riskMeta: Record<RiskLevel, { label: string; color: string }> = {
  read_only: { label: '只读', color: 'green' },
  local_write: { label: '本地写入', color: 'blue' },
  command: { label: '执行命令', color: 'orange' },
  external: { label: '对外操作', color: 'red' },
};

export type TurnState = TurnStatus | TurnView['state'];

export const turnStateMeta: Record<TurnState, { label: string; color: string }> = {
  queued: { label: '排队中', color: 'default' },
  running: { label: '运行中', color: 'processing' },
  suspended: { label: '等待人工确认', color: 'warning' },
  waiting_human: { label: '等待人工确认', color: 'warning' },
  waiting_subtasks: { label: '等待子任务', color: 'cyan' },
  completed: { label: '已完成', color: 'success' },
  failed: { label: '失败', color: 'error' },
  cancelled: { label: '已停止', color: 'default' },
};

export const sessionStatusMeta: Record<SessionStatus, { label: string; color: string }> = {
  idle: { label: '空闲', color: 'default' },
  queued: { label: '排队中', color: 'default' },
  running: { label: '运行中', color: 'processing' },
  waiting: { label: '等待人工确认', color: 'warning' },
};

export const pendingKindLabel: Record<PendingKind, string> = {
  approval: '操作审批',
  question: '问题',
  subtasks: '子任务',
  external_job: '外部任务',
  budget: '预算',
  outcome_unknown: '结果未知',
};

export const decisionLabel: Record<Decision, string> = {
  approve: '已批准',
  reject: '已拒绝',
  answer: '已回复',
  continue: '继续',
  stop: '停止',
  done: '已完成',
  happened: '已发生',
  not_happened: '未发生',
};

export const CANCEL_NOTE = '已停止，已发生的操作不会撤回';

export const scheduleOutcomeMeta: Record<ScheduleOutcome, { label: string; color: string }> = {
  ran: { label: '已运行', color: 'green' },
  skipped: { label: '已跳过', color: 'default' },
  missed: { label: '已错过', color: 'orange' },
};
