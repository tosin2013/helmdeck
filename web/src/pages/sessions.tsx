import { Activity, Server } from 'lucide-react';

import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card';
import {
  Table,
  TableBody,
  TableCell,
  TableEmpty,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
import { Badge } from '@/components/ui/badge';
import { Skeleton } from '@/components/ui/skeleton';
import { useApi } from '@/lib/queries';
import { formatRelative } from '@/lib/format';

interface Session {
  id: string;
  container_id: string;
  status: string;
  cdp_endpoint?: string;
  created_at: string;
  spec?: {
    image?: string;
    label?: string;
    memory_limit?: string;
    cpu_limit?: number;
  };
}

interface SessionsResponse {
  sessions: Session[];
}

// SessionsPage (T603) — list of live and historical browser session
// containers spawned by the runtime. Read-only for v0.6.0; the New
// Session modal and Terminate confirm modal land in T603a.
export function SessionsPage() {
  const { data, isLoading, error } = useApi<SessionsResponse>(
    ['sessions'],
    '/api/v1/sessions',
    { refetchInterval: 5_000 }, // poll every 5s — sessions churn fast
  );

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Browser Sessions</h1>
          <p className="text-sm text-muted-foreground">
            Live ephemeral browser containers spawned by the session runtime.
            Polled every 5 seconds.
          </p>
        </div>
        <Badge variant="outline">
          <Activity className="mr-1 h-3 w-3" />
          {data?.sessions?.length ?? 0} sessions
        </Badge>
      </div>

      {error && (
        <Card>
          <CardHeader>
            <CardTitle className="text-destructive">Failed to load sessions</CardTitle>
            <CardDescription>{error.message}</CardDescription>
          </CardHeader>
          <CardContent className="text-sm text-muted-foreground">
            The session runtime may be disabled (the control plane was started
            with <code className="rounded bg-muted px-1.5 py-0.5">-disable-runtime</code>)
            or the Docker daemon is unreachable.
          </CardContent>
        </Card>
      )}

      {isLoading ? (
        <Card>
          <CardContent className="space-y-3 pt-6">
            <Skeleton className="h-8 w-full" />
            <Skeleton className="h-8 w-full" />
            <Skeleton className="h-8 w-full" />
          </CardContent>
        </Card>
      ) : (
        !error && (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>ID</TableHead>
                <TableHead>Label</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Image</TableHead>
                <TableHead>Created</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {!data?.sessions || data.sessions.length === 0 ? (
                <TableEmpty colSpan={5}>
                  <Server className="mx-auto mb-2 h-8 w-8 text-muted-foreground/50" />
                  No active sessions. Create one via the API or wait for an
                  agent call.
                </TableEmpty>
              ) : (
                data.sessions.map((s) => (
                  <TableRow key={s.id}>
                    <TableCell className="font-mono text-xs">
                      {s.id.slice(0, 12)}…
                    </TableCell>
                    <TableCell>{s.spec?.label ?? '—'}</TableCell>
                    <TableCell>
                      <StatusBadge status={s.status} />
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {s.spec?.image ?? '—'}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {formatRelative(s.created_at)}
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        )
      )}
    </div>
  );
}

function StatusBadge({ status }: { status: string }) {
  const v =
    status === 'running'
      ? 'success'
      : status === 'starting'
        ? 'warning'
        : status === 'terminated' || status === 'failed'
          ? 'destructive'
          : 'outline';
  return <Badge variant={v}>{status}</Badge>;
}
