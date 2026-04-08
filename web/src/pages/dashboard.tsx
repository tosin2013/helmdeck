import { Link } from 'react-router-dom';
import {
  Activity,
  Boxes,
  KeyRound,
  type LucideIcon,
  Package,
  Server,
} from 'lucide-react';

import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { useApi } from '@/lib/queries';
import { useAuth } from '@/lib/auth';

// DashboardPage (T602) — landing page after login. Shows live
// counts pulled from the panels' own endpoints. Recharts memory
// graph and the activity feed land in T602a once the audit log
// has a read endpoint.

interface PackInfo { name: string }
interface SessionsResponse { sessions?: { id: string }[] }
interface MCPResponse { servers?: { id: string }[] }
interface VaultResponse { count: number }

export function DashboardPage() {
  const { subject } = useAuth();
  const sessions = useApi<SessionsResponse>(['sessions'], '/api/v1/sessions');
  const packs = useApi<PackInfo[]>(['packs'], '/api/v1/packs');
  const mcp = useApi<MCPResponse>(['mcp-servers'], '/api/v1/mcp/servers');
  const vault = useApi<VaultResponse>(['vault-credentials'], '/api/v1/vault/credentials');

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Dashboard</h1>
        <p className="text-sm text-muted-foreground">
          Welcome back, {subject}. Live counts across the helmdeck control
          plane.
        </p>
      </div>

      <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-4">
        <StatCard
          to="/sessions"
          label="Active sessions"
          icon={Server}
          value={sessions.data?.sessions?.length}
          loading={sessions.isLoading}
          error={!!sessions.error}
        />
        <StatCard
          to="/packs"
          label="Capability packs"
          icon={Package}
          value={packs.data?.length}
          loading={packs.isLoading}
          error={!!packs.error}
        />
        <StatCard
          to="/mcp"
          label="MCP servers"
          icon={Boxes}
          value={mcp.data?.servers?.length}
          loading={mcp.isLoading}
          error={!!mcp.error}
        />
        <StatCard
          to="/vault"
          label="Vault credentials"
          icon={KeyRound}
          value={vault.data?.count}
          loading={vault.isLoading}
          error={!!vault.error}
        />
      </div>

      <div className="grid gap-4 md:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>Phase 6 status</CardTitle>
            <CardDescription>Management UI rollout</CardDescription>
          </CardHeader>
          <CardContent className="space-y-2 text-sm">
            <div className="flex items-center justify-between">
              <span className="text-muted-foreground">T601 — shell + login</span>
              <span className="text-emerald-400">shipped</span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-muted-foreground">T602 — dashboard (counts)</span>
              <span className="text-emerald-400">shipped</span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-muted-foreground">T603 — sessions</span>
              <span className="text-emerald-400">shipped (read-only)</span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-muted-foreground">T605 — MCP registry</span>
              <span className="text-emerald-400">shipped (read-only)</span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-muted-foreground">T606 — capability packs</span>
              <span className="text-emerald-400">shipped (read-only)</span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-muted-foreground">T610 — credential vault</span>
              <span className="text-emerald-400">shipped (read-only)</span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-muted-foreground">T604 — AI providers</span>
              <span className="text-amber-400">pending</span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-muted-foreground">T611 — audit log</span>
              <span className="text-amber-400">pending</span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-muted-foreground">T607/T608 — pack authoring + success rates</span>
              <span className="text-amber-400">pending</span>
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Resources</CardTitle>
            <CardDescription>Documentation & community</CardDescription>
          </CardHeader>
          <CardContent className="space-y-2 text-sm">
            <div>
              <a
                href="https://github.com/tosin2013/helmdeck"
                className="text-primary hover:underline"
                target="_blank"
                rel="noreferrer"
              >
                GitHub repository
              </a>
            </div>
            <div>
              <a
                href="https://github.com/tosin2013/helmdeck/blob/main/docs/SECURITY-HARDENING.md"
                className="text-primary hover:underline"
                target="_blank"
                rel="noreferrer"
              >
                Security hardening guide
              </a>
            </div>
            <div>
              <a
                href="https://github.com/tosin2013/helmdeck/blob/main/docs/SIDECAR-LANGUAGES.md"
                className="text-primary hover:underline"
                target="_blank"
                rel="noreferrer"
              >
                Sidecar language images
              </a>
            </div>
            <div>
              <a
                href="https://github.com/tosin2013/helmdeck/blob/main/CONTRIBUTING.md"
                className="text-primary hover:underline"
                target="_blank"
                rel="noreferrer"
              >
                Contribution guide (write your own pack)
              </a>
            </div>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}

interface StatCardProps {
  to: string;
  label: string;
  icon: LucideIcon;
  value?: number;
  loading?: boolean;
  error?: boolean;
}

function StatCard({ to, label, icon: Icon, value, loading, error }: StatCardProps) {
  return (
    <Link to={to}>
      <Card className="transition-colors hover:border-primary/50 hover:bg-accent/30">
        <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
          <CardTitle className="text-sm font-medium text-muted-foreground">{label}</CardTitle>
          <Icon className="h-4 w-4 text-muted-foreground" />
        </CardHeader>
        <CardContent>
          {loading ? (
            <Skeleton className="h-8 w-16" />
          ) : error ? (
            <div className="text-2xl font-bold text-muted-foreground">—</div>
          ) : (
            <div className="text-2xl font-bold">{value ?? 0}</div>
          )}
          <Activity className="mt-1 inline h-3 w-3 text-emerald-400" />{' '}
          <span className="text-xs text-muted-foreground">live</span>
        </CardContent>
      </Card>
    </Link>
  );
}
