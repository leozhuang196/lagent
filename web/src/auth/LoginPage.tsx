import { useState } from 'react';
import { Navigate, useLocation, useNavigate } from 'react-router-dom';
import { Alert, Button, Card, Form, Input, Typography } from 'antd';
import { KeyOutlined } from '@ant-design/icons';
import { errorText } from '../api';
import { useAuth } from './AuthContext';

export function LoginPage() {
  const { principal, login } = useAuth();
  const nav = useNavigate();
  const loc = useLocation();
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const from = (loc.state as { from?: string } | null)?.from ?? '/';

  if (principal) return <Navigate to={from} replace />;

  const onFinish = async (v: { key: string }) => {
    setBusy(true);
    setErr(null);
    try {
      await login(v.key.trim());
      nav(from, { replace: true });
    } catch (e) {
      setErr(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="login-page">
      <Card className="login-card">
        <Typography.Title level={3} style={{ marginTop: 0 }}>
          登录 lagent
        </Typography.Title>
        <Typography.Paragraph type="secondary">使用管理员发放的 API Key 登录。登录状态保存在浏览器 Cookie 中，Key 本身不会被保存。</Typography.Paragraph>
        {err && <Alert type="error" showIcon message={err} style={{ marginBottom: 16 }} />}
        <Form layout="vertical" onFinish={onFinish} requiredMark={false}>
          <Form.Item name="key" label="API Key" rules={[{ required: true, message: '请输入 API Key' }]}>
            <Input.Password prefix={<KeyOutlined />} placeholder="lk_xxxxxx_…" autoFocus autoComplete="off" />
          </Form.Item>
          <Button type="primary" htmlType="submit" block loading={busy}>
            登录
          </Button>
        </Form>
      </Card>
    </div>
  );
}
