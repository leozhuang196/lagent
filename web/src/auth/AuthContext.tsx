import { createContext, useCallback, useContext, useEffect, useMemo, type ReactNode } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { authApi, getTenantOverride, isApiError, setTenantOverride, setUnauthorizedHandler, type Principal, type Role } from '../api';

interface AuthValue {
  principal: Principal | null;
  loading: boolean;
  /** Tenant the console is acting in (override for platform admins). */
  tenant: string;
  login(key: string): Promise<Principal>;
  logout(): Promise<void>;
  switchTenant(tenantId: string): void;
  hasRole(min: Role): boolean;
}

const AuthCtx = createContext<AuthValue | null>(null);

const rank: Record<Role, number> = { member: 0, tenant_admin: 1, platform_admin: 2 };

export function roleAtLeast(p: Principal | null, min: Role): boolean {
  return !!p && rank[p.role] >= rank[min];
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const qc = useQueryClient();
  const me = useQuery({
    queryKey: ['me'],
    queryFn: async () => {
      try {
        return await authApi.me();
      } catch (e) {
        if (isApiError(e) && e.status === 401) return null;
        throw e;
      }
    },
    staleTime: 60_000,
    retry: false,
  });

  useEffect(() => {
    setUnauthorizedHandler(() => {
      qc.setQueryData(['me'], null);
    });
    return () => setUnauthorizedHandler(null);
  }, [qc]);

  const login = useCallback(
    async (key: string) => {
      setTenantOverride('');
      const p = await authApi.login(key);
      qc.clear();
      qc.setQueryData(['me'], p);
      return p;
    },
    [qc],
  );

  const logout = useCallback(async () => {
    try {
      await authApi.logout();
    } finally {
      setTenantOverride('');
      qc.clear();
      qc.setQueryData(['me'], null);
    }
  }, [qc]);

  const switchTenant = useCallback(
    (t: string) => {
      setTenantOverride(t.trim());
      // Everything cached belongs to the previous tenant.
      qc.removeQueries({ predicate: (q) => q.queryKey[0] !== 'me' });
      void qc.invalidateQueries({ queryKey: ['me'] });
    },
    [qc],
  );

  const principal = me.data ?? null;
  const value = useMemo<AuthValue>(
    () => ({
      principal,
      loading: me.isLoading,
      tenant: principal?.tenant_id ?? getTenantOverride(),
      login,
      logout,
      switchTenant,
      hasRole: (min: Role) => roleAtLeast(principal, min),
    }),
    [principal, me.isLoading, login, logout, switchTenant],
  );
  return <AuthCtx.Provider value={value}>{children}</AuthCtx.Provider>;
}

export function useAuth(): AuthValue {
  const v = useContext(AuthCtx);
  if (!v) throw new Error('useAuth outside AuthProvider');
  return v;
}
