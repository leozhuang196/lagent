import { useMemo, useState } from 'react';
import { Link, Outlet, useLocation, useNavigate } from 'react-router-dom';
import { Button, Layout, Menu, Space, Tag, Tooltip, Typography, type MenuProps } from 'antd';
import {
  ApiOutlined,
  ArrowLeftOutlined,
  AuditOutlined,
  BookOutlined,
  BugOutlined,
  ClusterOutlined,
  DashboardOutlined,
  ExperimentOutlined,
  FileTextOutlined,
  KeyOutlined,
  LogoutOutlined,
  PartitionOutlined,
  PlayCircleOutlined,
  RobotOutlined,
  ScheduleOutlined,
  SettingOutlined,
  TeamOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons';
import type { Role } from '../api';
import { useAuth } from '../auth/AuthContext';
import { roleLabel } from '../lib/labels';

interface NavItem {
  path: string;
  label: string;
  icon: React.ReactNode;
  min?: Role;
}

interface NavGroup {
  key: string;
  label: string;
  min?: Role;
  items: NavItem[];
}

const groups: NavGroup[] = [
  {
    key: 'overview',
    label: '概览',
    items: [{ path: '/admin', label: '平台状态', icon: <DashboardOutlined />, min: 'platform_admin' }],
  },
  {
    key: 'assets',
    label: '发布与配置',
    items: [
      { path: '/admin/roles', label: '角色', icon: <RobotOutlined /> },
      { path: '/admin/skills', label: '技能', icon: <BookOutlined /> },
      { path: '/admin/workflow-defs', label: '工作流模板', icon: <PartitionOutlined /> },
      { path: '/admin/memory', label: '记忆空间', icon: <ThunderboltOutlined /> },
      { path: '/admin/integrations', label: '集成与工具', icon: <ApiOutlined /> },
    ],
  },
  {
    key: 'run',
    label: '运行',
    items: [
      { path: '/admin/runs', label: '工作流运行', icon: <PlayCircleOutlined /> },
      { path: '/admin/schedules', label: '定时任务', icon: <ScheduleOutlined /> },
      { path: '/admin/feishu', label: '飞书绑定', icon: <TeamOutlined /> },
    ],
  },
  {
    key: 'debug',
    label: '调试',
    items: [
      { path: '/admin/debug/turns', label: '轮次调试器', icon: <BugOutlined /> },
      { path: '/admin/debug/assembly', label: '组装预览', icon: <FileTextOutlined /> },
      { path: '/admin/debug/playground', label: '试验场', icon: <ExperimentOutlined /> },
    ],
  },
  {
    key: 'tenant',
    label: '租户',
    items: [
      { path: '/admin/keys', label: 'API Key', icon: <KeyOutlined /> },
      { path: '/admin/usage', label: '用量', icon: <ClusterOutlined /> },
      { path: '/admin/audit', label: '审计日志', icon: <AuditOutlined /> },
      { path: '/admin/tenant', label: '租户设置', icon: <SettingOutlined /> },
    ],
  },
  {
    key: 'platform',
    label: '平台',
    min: 'platform_admin',
    items: [
      { path: '/admin/platform', label: '租户总览', icon: <ClusterOutlined /> },
      { path: '/admin/platform/breakglass', label: '跨租户排障', icon: <BugOutlined /> },
    ],
  },
];

/** Best matching nav entry: the longest path that prefixes the location. */
function selectedKey(pathname: string): string {
  let best = '';
  for (const g of groups) {
    for (const it of g.items) {
      if ((pathname === it.path || pathname.startsWith(it.path + '/')) && it.path.length > best.length) best = it.path;
    }
  }
  return best || '/admin';
}

const titleOf: Record<string, string> = Object.fromEntries(
  groups.flatMap((g) => g.items.map((i) => [i.path, i.label] as const)),
);

export function AdminLayout() {
  const { principal, tenant, logout, hasRole } = useAuth();
  const loc = useLocation();
  const nav = useNavigate();
  const [collapsed, setCollapsed] = useState(false);

  const items = useMemo<MenuProps['items']>(
    () =>
      groups
        .filter((g) => !g.min || hasRole(g.min))
        .map((g) => ({
          key: g.key,
          label: g.label,
          type: 'group' as const,
          children: g.items
            .filter((i) => !i.min || hasRole(i.min))
            .map((i) => ({
              key: i.path,
              icon: i.icon,
              label: <Link to={i.path}>{i.label}</Link>,
            })),
        })),
    [hasRole],
  );

  const selected = selectedKey(loc.pathname);

  return (
    <Layout className="admin-layout">
      <Layout.Sider width={232} theme="light" className="admin-sider" collapsible collapsed={collapsed} onCollapse={setCollapsed}>
        <Link to="/" className="admin-brand">
          <ApiOutlined /> {!collapsed && 'lagent 控制台'}
        </Link>
        <Menu mode="inline" selectedKeys={[selected]} items={items} style={{ borderInlineEnd: 'none' }} />
      </Layout.Sider>
      <Layout>
        <Layout.Header className="admin-header">
          <Space size={10}>
            <Typography.Text strong className="admin-title">
              {titleOf[selected] ?? '管理控制台'}
            </Typography.Text>
            {hasRole('platform_admin') && <Tag color="purple">租户 {tenant}</Tag>}
          </Space>
          <Space size={6}>
            <Tooltip title="返回对话">
              <Button type="text" icon={<ArrowLeftOutlined />} onClick={() => nav('/')}>
                回到对话
              </Button>
            </Tooltip>
            <span className="muted small">
              {principal?.name || principal?.id} · {principal ? roleLabel[principal.role] : ''}
            </span>
            <Button type="text" icon={<LogoutOutlined />} onClick={() => void logout()} aria-label="退出登录" />
          </Space>
        </Layout.Header>
        <Layout.Content className="admin-content">
          <Outlet />
        </Layout.Content>
      </Layout>
    </Layout>
  );
}