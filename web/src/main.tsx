import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { App as AntApp, ConfigProvider } from 'antd';
import zhCN from 'antd/locale/zh_CN';
import dayjs from 'dayjs';
import 'dayjs/locale/zh-cn';
import 'antd/dist/reset.css';
import './styles.css';

import { ApiError } from './api';
import { AuthProvider } from './auth/AuthContext';
import { LoginPage } from './auth/LoginPage';
import { RequireAuth } from './auth/RequireAuth';
import { ChatLayout } from './chat/ChatLayout';
import { ChatHome, ChatSession } from './chat/ChatPages';
import { AdminLayout } from './admin/AdminLayout';
import {
  RoleDetailPage,
  RolesPage,
  SkillDetailPage,
  SkillsPage,
  WorkflowDefDetailPage,
  WorkflowDefsPage,
} from './admin/pages/Assets';
import { MemoryPage } from './admin/pages/MemoryPage';
import { TenantPage } from './admin/pages/TenantPage';
import { KeysPage } from './admin/pages/KeysPage';
import { UsagePage } from './admin/pages/UsagePage';
import { AuditPage } from './admin/pages/AuditPage';
import { TurnDebuggerPage } from './admin/pages/TurnDebuggerPage';
import { AssemblyPage } from './admin/pages/AssemblyPage';
import { PlaygroundPage } from './admin/pages/PlaygroundPage';
import { PlatformPage } from './admin/pages/PlatformPage';
import { BreakGlassPage } from './admin/pages/BreakGlassPage';
import { IntegrationsPage } from './admin/pages/IntegrationsPage';
import { RunsPage } from './admin/pages/RunsPage';
import { RunDetailPage } from './admin/pages/RunDetailPage';
import { SchedulesPage } from './admin/pages/SchedulesPage';
import { FeishuPage } from './admin/pages/FeishuPage';

dayjs.locale('zh-cn');

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: (count, err) => !(err instanceof ApiError && err.status < 500) && count < 1,
      refetchOnWindowFocus: false,
      staleTime: 15_000,
    },
  },
});

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ConfigProvider
      locale={zhCN}
      theme={{
        token: { colorPrimary: '#2f6fed', borderRadius: 8, fontSize: 14 },
      }}
    >
      <AntApp>
        <QueryClientProvider client={queryClient}>
          <AuthProvider>
            <BrowserRouter>
              <Routes>
                <Route path="/login" element={<LoginPage />} />
                <Route
                  element={
                    <RequireAuth>
                      <ChatLayout />
                    </RequireAuth>
                  }
                >
                  <Route index element={<ChatHome />} />
                  <Route path="c/:sessionId" element={<ChatSession />} />
                </Route>
                <Route
                  path="/admin"
                  element={
                    <RequireAuth role="tenant_admin">
                      <AdminLayout />
                    </RequireAuth>
                  }
                >
                  <Route index element={<PlatformPage />} />
                  <Route path="roles" element={<RolesPage />} />
                  <Route path="roles/:key" element={<RoleDetailPage />} />
                  <Route path="skills" element={<SkillsPage />} />
                  <Route path="skills/:key" element={<SkillDetailPage />} />
                  <Route path="workflow-defs" element={<WorkflowDefsPage />} />
                  <Route path="workflow-defs/:key" element={<WorkflowDefDetailPage />} />
                  <Route path="memory" element={<MemoryPage />} />
                  <Route path="integrations" element={<IntegrationsPage />} />
                  <Route path="runs" element={<RunsPage />} />
                  <Route path="runs/:id" element={<RunDetailPage />} />
                  <Route path="schedules" element={<SchedulesPage />} />
                  <Route path="feishu" element={<FeishuPage />} />
                  <Route path="debug/turns" element={<TurnDebuggerPage />} />
                  <Route path="debug/assembly" element={<AssemblyPage />} />
                  <Route path="debug/playground" element={<PlaygroundPage />} />
                  <Route path="keys" element={<KeysPage />} />
                  <Route path="usage" element={<UsagePage />} />
                  <Route path="audit" element={<AuditPage />} />
                  <Route path="tenant" element={<TenantPage />} />
                  <Route path="platform" element={<PlatformPage />} />
                  <Route path="platform/breakglass" element={<BreakGlassPage />} />
                  <Route path="*" element={<Navigate to="/admin" replace />} />
                </Route>
                <Route path="*" element={<Navigate to="/" replace />} />
              </Routes>
            </BrowserRouter>
          </AuthProvider>
        </QueryClientProvider>
      </AntApp>
    </ConfigProvider>
  </StrictMode>,
);