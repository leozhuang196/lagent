import { useState } from 'react';
import { Link, Outlet, useNavigate, useParams } from 'react-router-dom';
import { Avatar, Button, Dropdown, Layout, List, Skeleton, Space, Typography } from 'antd';
import { LogoutOutlined, MessageOutlined, PlusOutlined, SettingOutlined, UserOutlined } from '@ant-design/icons';
import { errorText } from '../api';
import { useAuth } from '../auth/AuthContext';
import { SessionStatusTag } from '../components/Tags';
import { fromNow } from '../lib/format';
import { roleLabel } from '../lib/labels';
import { NewSessionModal } from './NewSessionModal';
import { useSessionList } from './useSessionList';

export function ChatLayout() {
  const { principal, logout } = useAuth();
  const nav = useNavigate();
  const { sessionId } = useParams();
  const [creating, setCreating] = useState(false);
  const list = useSessionList(false);

  return (
    <Layout className="chat-layout">
      <Layout.Sider width={280} theme="light" className="chat-sider">
        <div className="sider-top">
          <Link to="/" className="brand">
            lagent
          </Link>
          <Button type="primary" icon={<PlusOutlined />} block onClick={() => setCreating(true)}>
            新会话
          </Button>
        </div>
        <div className="session-list">
          {list.isLoading ? (
            <div style={{ padding: 16 }}>
              <Skeleton active paragraph={{ rows: 6 }} />
            </div>
          ) : list.isError ? (
            <div style={{ padding: 16 }}>
              <Typography.Text type="danger">{errorText(list.error)}</Typography.Text>
              <Button size="small" onClick={() => void list.refetch()} style={{ marginTop: 8 }}>
                重试
              </Button>
            </div>
          ) : list.items.length === 0 ? (
            <div style={{ padding: 24 }} className="muted">
              还没有会话，点击「新会话」开始。
            </div>
          ) : (
            <List
              dataSource={list.items}
              split={false}
              renderItem={(s) => (
                <List.Item
                  className={`session-item ${s.id === sessionId ? 'active' : ''}`}
                  onClick={() => nav(`/c/${s.id}`)}
                  tabIndex={0}
                  onKeyDown={(e) => e.key === 'Enter' && nav(`/c/${s.id}`)}
                >
                  <div className="session-item-inner">
                    <Space size={6}>
                      <MessageOutlined />
                      <Typography.Text ellipsis style={{ maxWidth: 170 }}>
                        {s.title || '未命名会话'}
                      </Typography.Text>
                    </Space>
                    <div className="session-meta">
                      <span className="muted small">{fromNow(s.updated_at)}</span>
                      {s.status !== 'idle' && <SessionStatusTag status={s.status} />}
                    </div>
                  </div>
                </List.Item>
              )}
            />
          )}
          {list.hasNextPage && (
            <Button type="link" block loading={list.isFetchingNextPage} onClick={() => void list.fetchNextPage()}>
              加载更多
            </Button>
          )}
        </div>
        <div className="sider-bottom">
          <Dropdown
            trigger={['click']}
            menu={{
              items: [
                { key: 'admin', icon: <SettingOutlined />, label: <Link to="/admin">管理控制台</Link> },
                { key: 'logout', icon: <LogoutOutlined />, label: '退出登录', onClick: () => void logout() },
              ],
            }}
          >
            <Space className="user-chip" tabIndex={0}>
              <Avatar size="small" icon={<UserOutlined />} />
              <div>
                <div>{principal?.name || principal?.id}</div>
                <div className="muted small">
                  {principal ? roleLabel[principal.role] : ''} · {principal?.tenant_id}
                </div>
              </div>
            </Space>
          </Dropdown>
        </div>
      </Layout.Sider>
      <Layout.Content className="chat-content">
        <Outlet />
      </Layout.Content>
      <NewSessionModal
        open={creating}
        onClose={() => setCreating(false)}
        onCreated={(s) => {
          setCreating(false);
          nav(`/c/${s.id}`);
        }}
      />
    </Layout>
  );
}
