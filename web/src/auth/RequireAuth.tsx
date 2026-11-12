import type { ReactNode } from 'react';
import { Navigate, useLocation } from 'react-router-dom';
import { Result, Spin } from 'antd';
import type { Role } from '../api';
import { useAuth } from './AuthContext';

export function RequireAuth({ children, role }: { children: ReactNode; role?: Role }) {
  const { principal, loading, hasRole } = useAuth();
  const loc = useLocation();
  if (loading) {
    return (
      <div style={{ height: '100vh', display: 'grid', placeItems: 'center' }}>
        <Spin size="large" />
      </div>
    );
  }
  if (!principal) return <Navigate to="/login" replace state={{ from: loc.pathname + loc.search }} />;
  if (role && !hasRole(role)) {
    return <Result status="403" title="无权访问" subTitle="当前账号的角色不能访问此页面。" />;
  }
  return <>{children}</>;
}
