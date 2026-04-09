import { Activity, KeyRound, Shield, ShieldCheck, ShieldOff } from 'lucide-react';

import { Badge } from '@/components/ui/badge';
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { useApi } from '@/lib/queries';

interface SecurityPolicy {
  egress: {
    allowlist: string[];
    default_block_list: string[];
    description: string;
  };
  sandbox: {
    seccomp_profile: string;
    pids_limit: number;
    pids_limit_default: number;
    description: string;
  };
  auth: {
    admin_login_enabled: boolean;
    admin_username: string;
    jwt_secret_configured: boolean;
  };
  telemetry: {
    otel_enabled: boolean;
    otel_endpoint: string;
  };
}

// SecurityPage (T609) — read-only snapshot of the control plane's
// security posture: egress allowlist, sandbox baseline, auth
// configuration, telemetry. Reads GET /api/v1/security which
// reflects the env vars the control plane was started with.
//
// This panel is intentionally read-only for v0.6.0. Editing these
// settings means restarting the control plane (env vars are read
// at startup), and editing them through a UI without that nuance
// would be a footgun. The full edit + reload-config story lands
// in T609a once the control plane has a SIGHUP-style reload path.
export function SecurityPage() {
  const { data, isLoading, error } = useApi<SecurityPolicy>(
    ['security'],
    '/api/v1/security',
  );

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">
          Security Policies
        </h1>
        <p className="text-sm text-muted-foreground">
          Snapshot of the egress guard, sandbox baseline, auth, and
          telemetry configuration applied at control-plane startup.
          Changes require a restart — see{' '}
          <code className="rounded bg-muted px-1.5 py-0.5">
            docs/SECURITY-HARDENING.md
          </code>
          .
        </p>
      </div>

      {error && (
        <Card>
          <CardHeader>
            <CardTitle className="text-destructive">
              Failed to load security snapshot
            </CardTitle>
            <CardDescription>{error.message}</CardDescription>
          </CardHeader>
        </Card>
      )}

      {isLoading || !data ? (
        <Card>
          <CardContent className="space-y-3 pt-6">
            <Skeleton className="h-8 w-full" />
            <Skeleton className="h-8 w-3/4" />
            <Skeleton className="h-8 w-5/6" />
          </CardContent>
        </Card>
      ) : (
        <div className="grid gap-4 lg:grid-cols-2">
          <EgressCard egress={data.egress} />
          <SandboxCard sandbox={data.sandbox} />
          <AuthCard auth={data.auth} />
          <TelemetryCard telemetry={data.telemetry} />
        </div>
      )}
    </div>
  );
}

function EgressCard({ egress }: { egress: SecurityPolicy['egress'] }) {
  return (
    <Card>
      <CardHeader>
        <div className="flex items-start justify-between">
          <div>
            <CardTitle className="flex items-center gap-2 text-base">
              <Shield className="h-4 w-4 text-muted-foreground" />
              Egress Guard
            </CardTitle>
            <CardDescription className="mt-1">
              {egress.description}
            </CardDescription>
          </div>
          <Badge variant={egress.allowlist.length > 0 ? 'success' : 'outline'}>
            {egress.allowlist.length > 0
              ? `${egress.allowlist.length} allowed`
              : 'block-list only'}
          </Badge>
        </div>
      </CardHeader>
      <CardContent className="space-y-3 text-xs">
        <div>
          <p className="font-semibold mb-1">Allowlist</p>
          {egress.allowlist.length === 0 ? (
            <p className="text-muted-foreground">
              None — only the default block list applies. Set{' '}
              <code className="rounded bg-muted px-1 py-0.5">
                HELMDECK_EGRESS_ALLOWLIST
              </code>{' '}
              to add CIDRs.
            </p>
          ) : (
            <ul className="space-y-1 font-mono text-muted-foreground">
              {egress.allowlist.map((cidr) => (
                <li key={cidr}>{cidr}</li>
              ))}
            </ul>
          )}
        </div>
        <div>
          <p className="font-semibold mb-1">Default block list</p>
          <div className="flex flex-wrap gap-1">
            {egress.default_block_list.map((cidr) => (
              <code
                key={cidr}
                className="rounded bg-muted px-1.5 py-0.5 font-mono"
              >
                {cidr}
              </code>
            ))}
          </div>
        </div>
      </CardContent>
    </Card>
  );
}

function SandboxCard({ sandbox }: { sandbox: SecurityPolicy['sandbox'] }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <ShieldCheck className="h-4 w-4 text-muted-foreground" />
          Sandbox Baseline
        </CardTitle>
        <CardDescription className="mt-1">
          {sandbox.description}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-2 text-xs">
        <Row
          label="Seccomp profile"
          value={
            sandbox.seccomp_profile || (
              <span className="text-muted-foreground">
                docker default (curated)
              </span>
            )
          }
        />
        <Row
          label="PIDs limit"
          value={
            <>
              {sandbox.pids_limit > 0
                ? sandbox.pids_limit
                : `${sandbox.pids_limit_default} (default)`}
            </>
          }
        />
        <Row label="Run as nonroot" value={<Badge variant="success">yes</Badge>} />
        <Row label="Drop all caps" value={<Badge variant="success">yes</Badge>} />
      </CardContent>
    </Card>
  );
}

function AuthCard({ auth }: { auth: SecurityPolicy['auth'] }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <KeyRound className="h-4 w-4 text-muted-foreground" />
          Authentication
        </CardTitle>
        <CardDescription className="mt-1">
          Management UI login credentials and JWT signing posture.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-2 text-xs">
        <Row
          label="Admin login"
          value={
            auth.admin_login_enabled ? (
              <Badge variant="success">enabled</Badge>
            ) : (
              <Badge variant="destructive">disabled (CLI mint-token only)</Badge>
            )
          }
        />
        <Row label="Admin username" value={auth.admin_username} />
        <Row
          label="JWT signing secret"
          value={
            auth.jwt_secret_configured ? (
              <Badge variant="success">configured</Badge>
            ) : (
              <Badge variant="destructive">ephemeral (not persisted)</Badge>
            )
          }
        />
      </CardContent>
    </Card>
  );
}

function TelemetryCard({
  telemetry,
}: {
  telemetry: SecurityPolicy['telemetry'];
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <Activity className="h-4 w-4 text-muted-foreground" />
          Telemetry
        </CardTitle>
        <CardDescription className="mt-1">
          OpenTelemetry tracing exporter (T510). No-op when neither
          env var is set.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-2 text-xs">
        <Row
          label="OTel exporter"
          value={
            telemetry.otel_enabled ? (
              <Badge variant="success">enabled</Badge>
            ) : (
              <Badge variant="outline">
                <ShieldOff className="mr-1 h-3 w-3" />
                disabled
              </Badge>
            )
          }
        />
        {telemetry.otel_endpoint && (
          <Row
            label="Endpoint"
            value={
              <code className="rounded bg-muted px-1.5 py-0.5 font-mono">
                {telemetry.otel_endpoint}
              </code>
            }
          />
        )}
      </CardContent>
    </Card>
  );
}

function Row({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-2">
      <span className="text-muted-foreground">{label}</span>
      <span className="text-right">{value}</span>
    </div>
  );
}
