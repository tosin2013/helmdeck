import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import {
  Brain,
  RefreshCw,
  Trash2,
  Sparkles,
  History,
  AlertTriangle,
} from 'lucide-react';

import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import {
  Table,
  TableBody,
  TableCell,
  TableEmpty,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
import { useApi, useAuthFetch } from '@/lib/queries';
import { formatRelative } from '@/lib/format';

interface ProjectedEntry {
  id: string;
  calls?: number;
  runs?: number;
  last_used_unix: number;
  common_inputs?: Record<string, string>;
}

interface RecentRow {
  kind: 'pack' | 'pipeline';
  key: string;
  id: string;
  outcome: string;
  at_unix: number;
  duration_ms?: number;
  learn_inputs?: Record<string, string>;
}

interface MemoryDefaultsResponse {
  scope: string;
  fetched_at: string;
  packs: ProjectedEntry[];
  pipelines: ProjectedEntry[];
  recent: RecentRow[];
  note?: string;
}

// MemoryPage surfaces the per-caller routing memory the audit hooks
// in *packs.Engine.Execute and *pipelines.Runner.RunSync populate.
// Two questions an operator answers here:
//
//   1. "What does helmdeck currently 'know' about me?"
//      → Top packs/pipelines + common_inputs chips.
//   2. "How do I make it forget?"
//      → Per-row forget, per-pack-id forget, global clear.
//
// Backed by GET /api/v1/memory/defaults and POST /api/v1/memory/forget
// (internal/api/memory.go). Same data, different transport, as the
// MCP resource helmdeck://my-defaults (ADR 047 PR #2/4).
interface CallerEntry {
  namespace: string;
  count: number;
}

interface CallersResponse {
  callers: CallerEntry[];
}

export function MemoryPage() {
  // Caller selector (#569). Admin operators can switch to inspect what
  // another caller (typically an agent like openclaw-configure) has
  // been doing. Non-admins get back just their own caller from the
  // endpoint, so the dropdown shows only one entry and switching is a
  // no-op (defense in depth — the /defaults endpoint also blocks the
  // override server-side).
  const { data: callersData } = useApi<CallersResponse>(
    ['memory-callers'],
    '/api/v1/memory/callers',
  );
  const callers = callersData?.callers ?? [];
  const [selectedCaller, setSelectedCaller] = useState<string>('');
  const defaultsURL = selectedCaller
    ? `/api/v1/memory/defaults?caller=${encodeURIComponent(selectedCaller)}`
    : '/api/v1/memory/defaults';
  const { data, isLoading, error, refetch, isFetching } =
    useApi<MemoryDefaultsResponse>(
      ['memory-defaults', selectedCaller || 'self'],
      defaultsURL,
    );
  const fetchWithAuth = useAuthFetch();
  const qc = useQueryClient();
  const [busyScope, setBusyScope] = useState<string | null>(null);
  const [forgetErr, setForgetErr] = useState<string | null>(null);

  async function forget(scope: string) {
    setBusyScope(scope);
    setForgetErr(null);
    try {
      const res = await fetchWithAuth('/api/v1/memory/forget', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ scope }),
      });
      if (!res.ok) {
        const body = await res.text().catch(() => '');
        setForgetErr(`forget failed (${res.status}): ${body || res.statusText}`);
        return;
      }
      await qc.invalidateQueries({ queryKey: ['memory-defaults'] });
    } catch (e) {
      setForgetErr(String(e));
    } finally {
      setBusyScope(null);
    }
  }

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Routing Memory</h1>
          <p className="text-sm text-muted-foreground">
            What helmdeck has learned from your pack and pipeline calls.
            The chat agent reads these defaults before asking you for
            inputs you've answered before. Audit rows expire automatically
            after 30 days; this page lets you clear sooner. ADR 047.
          </p>
        </div>
        <div className="flex items-center gap-2">
          {callers.length > 1 && (
            <label className="flex items-center gap-2 text-sm text-muted-foreground">
              <span>View caller:</span>
              <select
                value={selectedCaller}
                onChange={(e) => setSelectedCaller(e.target.value)}
                className="rounded-md border bg-background px-2 py-1.5 text-sm"
              >
                <option value="">(self)</option>
                {callers.map((c) => (
                  <option key={c.namespace} value={c.namespace}>
                    {c.namespace} ({c.count})
                  </option>
                ))}
              </select>
            </label>
          )}
          <Button
            variant="outline"
            onClick={() => refetch()}
            disabled={isFetching}
          >
            <RefreshCw
              className={`mr-2 h-4 w-4 ${isFetching ? 'animate-spin' : ''}`}
            />
            Refresh
          </Button>
          <Button
            variant="destructive"
            onClick={() => forget('all')}
            disabled={busyScope !== null}
          >
            <Trash2 className="mr-2 h-4 w-4" />
            Clear all history
          </Button>
        </div>
      </div>

      {forgetErr && (
        <Card className="border-destructive bg-destructive/5">
          <CardContent className="flex items-start gap-3 py-4">
            <AlertTriangle className="mt-0.5 h-4 w-4 text-destructive" />
            <div className="text-sm">{forgetErr}</div>
          </CardContent>
        </Card>
      )}

      {error && (
        <Card className="border-destructive bg-destructive/5">
          <CardContent className="flex items-start gap-3 py-4">
            <AlertTriangle className="mt-0.5 h-4 w-4 text-destructive" />
            <div className="text-sm">
              {error instanceof Error ? error.message : String(error)}
            </div>
          </CardContent>
        </Card>
      )}

      {data?.note && (
        <Card>
          <CardContent className="flex items-start gap-3 py-4 text-sm text-muted-foreground">
            <Brain className="mt-0.5 h-4 w-4" />
            {data.note}
          </CardContent>
        </Card>
      )}

      <div className="grid gap-6 lg:grid-cols-2">
        <DefaultsCard
          title="Learned pack defaults"
          icon={<Sparkles className="h-4 w-4" />}
          countLabel="calls"
          rows={data?.packs ?? []}
          loading={isLoading}
          busyScope={busyScope}
          onForget={(id) => forget(`pack:${id}`)}
          scopeAllLabel="all packs"
          onForgetAll={() => forget('packs')}
        />
        <DefaultsCard
          title="Learned pipeline defaults"
          icon={<Sparkles className="h-4 w-4" />}
          countLabel="runs"
          rows={data?.pipelines ?? []}
          loading={isLoading}
          busyScope={busyScope}
          onForget={(id) => forget(`pipeline:${id}`)}
          scopeAllLabel="all pipelines"
          onForgetAll={() => forget('pipelines')}
        />
      </div>

      <Card>
        <CardHeader>
          <div className="flex items-center justify-between">
            <div>
              <CardTitle className="flex items-center gap-2 text-base">
                <History className="h-4 w-4" />
                Recent activity
              </CardTitle>
              <CardDescription>
                The last few hundred audit rows under this caller. Useful
                for spotting where a "stuck" default came from.
              </CardDescription>
            </div>
          </div>
        </CardHeader>
        <CardContent className="p-0">
          {isLoading ? (
            <div className="space-y-2 p-4">
              <Skeleton className="h-4 w-full" />
              <Skeleton className="h-4 w-full" />
              <Skeleton className="h-4 w-2/3" />
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-24">Kind</TableHead>
                  <TableHead>ID</TableHead>
                  <TableHead className="w-24">Outcome</TableHead>
                  <TableHead className="w-32">When</TableHead>
                  <TableHead>Inputs learned</TableHead>
                  <TableHead className="w-24"></TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {(data?.recent ?? []).length === 0 ? (
                  <TableEmpty colSpan={6}>
                    No audit history yet — call a pack or pipeline and it
                    will land here.
                  </TableEmpty>
                ) : (
                  (data?.recent ?? []).map((r) => (
                    <TableRow key={r.key}>
                      <TableCell>
                        <Badge
                          variant={r.kind === 'pipeline' ? 'default' : 'secondary'}
                        >
                          {r.kind}
                        </Badge>
                      </TableCell>
                      <TableCell className="font-mono text-xs">{r.id}</TableCell>
                      <TableCell>
                        <Badge
                          variant={r.outcome === 'ok' || r.outcome === 'succeeded' ? 'outline' : 'destructive'}
                        >
                          {r.outcome}
                        </Badge>
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        {r.at_unix ? formatRelative(new Date(r.at_unix * 1000).toISOString()) : '—'}
                      </TableCell>
                      <TableCell>
                        <InputChips inputs={r.learn_inputs} />
                      </TableCell>
                      <TableCell className="text-right">
                        <Button
                          variant="ghost"
                          size="sm"
                          disabled={busyScope !== null}
                          onClick={() => forget(`key:${r.key}`)}
                          title="Forget this run"
                        >
                          <Trash2 className="h-3.5 w-3.5" />
                        </Button>
                      </TableCell>
                    </TableRow>
                  ))
                )}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

interface DefaultsCardProps {
  title: string;
  icon: React.ReactNode;
  countLabel: string;
  rows: ProjectedEntry[];
  loading: boolean;
  busyScope: string | null;
  onForget: (id: string) => void;
  scopeAllLabel: string;
  onForgetAll: () => void;
}

function DefaultsCard({
  title,
  icon,
  countLabel,
  rows,
  loading,
  busyScope,
  onForget,
  scopeAllLabel,
  onForgetAll,
}: DefaultsCardProps) {
  return (
    <Card>
      <CardHeader>
        <div className="flex items-start justify-between">
          <div>
            <CardTitle className="flex items-center gap-2 text-base">
              {icon}
              {title}
            </CardTitle>
            <CardDescription>
              Ranked by frequency. The chips below each row are the
              most-used input value for that field — the agent pre-fills
              from these.
            </CardDescription>
          </div>
          {rows.length > 0 && (
            <Button
              variant="ghost"
              size="sm"
              onClick={onForgetAll}
              disabled={busyScope !== null}
              title={`Forget ${scopeAllLabel}`}
            >
              <Trash2 className="mr-1 h-3.5 w-3.5" />
              Clear {scopeAllLabel}
            </Button>
          )}
        </div>
      </CardHeader>
      <CardContent>
        {loading ? (
          <Skeleton className="h-16 w-full" />
        ) : rows.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No history yet.
          </p>
        ) : (
          <ul className="space-y-3">
            {rows.map((row) => {
              const count = row.calls ?? row.runs ?? 0;
              return (
                <li
                  key={row.id}
                  className="flex flex-col gap-2 rounded-md border bg-card/50 p-3"
                >
                  <div className="flex items-center justify-between">
                    <div className="flex items-center gap-2">
                      <span className="font-mono text-sm">{row.id}</span>
                      <Badge variant="outline">
                        {count} {countLabel}
                      </Badge>
                    </div>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => onForget(row.id)}
                      disabled={busyScope !== null}
                      title="Forget this entry"
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </Button>
                  </div>
                  <InputChips inputs={row.common_inputs} />
                </li>
              );
            })}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}

function InputChips({ inputs }: { inputs?: Record<string, string> }) {
  const entries = Object.entries(inputs ?? {});
  if (entries.length === 0) {
    return <span className="text-xs text-muted-foreground">—</span>;
  }
  return (
    <div className="flex flex-wrap gap-1">
      {entries.map(([k, v]) => (
        <Badge key={k} variant="secondary" className="font-mono text-[10px]">
          {k}: {v}
        </Badge>
      ))}
    </div>
  );
}
