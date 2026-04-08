import { Navigate, Route, Routes } from 'react-router-dom';

import { useAuth } from '@/lib/auth';
import { Layout } from '@/components/layout/Layout';
import { LoginPage } from '@/pages/login';
import { DashboardPage } from '@/pages/dashboard';
import { SessionsPage } from '@/pages/sessions';
import { VaultPage } from '@/pages/vault';
import { PacksPage } from '@/pages/packs';
import { McpPage } from '@/pages/mcp';
import { ProvidersPage } from '@/pages/providers';
import { SecurityPage } from '@/pages/security';
import { AuditPage } from '@/pages/audit';
import { ConnectPage } from '@/pages/connect';

// App is the routing root. Auth-gated routes wrap in <Layout> which
// owns the sidebar and header chrome; the login route is a standalone
// page with no chrome at all.
export function App() {
  const { token } = useAuth();
  return (
    <Routes>
      <Route
        path="/login"
        element={token ? <Navigate to="/" replace /> : <LoginPage />}
      />
      <Route
        path="/"
        element={token ? <Layout /> : <Navigate to="/login" replace />}
      >
        <Route index element={<DashboardPage />} />
        <Route path="sessions" element={<SessionsPage />} />
        <Route path="vault" element={<VaultPage />} />
        <Route path="packs" element={<PacksPage />} />
        <Route path="mcp" element={<McpPage />} />
        <Route path="providers" element={<ProvidersPage />} />
        <Route path="security" element={<SecurityPage />} />
        <Route path="audit" element={<AuditPage />} />
        <Route path="connect" element={<ConnectPage />} />
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  );
}
