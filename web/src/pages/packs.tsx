import { useMemo, useState } from 'react';
import { Package, Play, Search } from 'lucide-react';

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
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Skeleton } from '@/components/ui/skeleton';
import { Textarea } from '@/components/ui/textarea';
import { useApi, useAuthFetch } from '@/lib/queries';

interface PackInfo {
  name: string;
  description: string;
  versions?: string[];
  latest?: string;
}

// PacksPage (T606 — list view) shows the entire built-in pack
// catalog grouped by namespace. The Test Runner panel (T606a MVP)
// is the click-into-row drawer below. Model Success Rates view
// (T607) lands later.
export function PacksPage() {
  const { data, isLoading, error } = useApi<PackInfo[]>(['packs'], '/api/v1/packs');
  const [query, setQuery] = useState('');
  const [selectedPack, setSelectedPack] = useState<PackInfo | null>(null);

  const grouped = useMemo(() => {
    if (!data) return new Map<string, PackInfo[]>();
    const filtered = data.filter((p) => {
      if (!query) return true;
      const q = query.toLowerCase();
      return (
        p.name.toLowerCase().includes(q) || p.description.toLowerCase().includes(q)
      );
    });
    const m = new Map<string, PackInfo[]>();
    for (const p of filtered) {
      const ns = p.name.split('.')[0] ?? 'misc';
      const arr = m.get(ns) ?? [];
      arr.push(p);
      m.set(ns, arr);
    }
    return m;
  }, [data, query]);

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Capability Packs</h1>
          <p className="text-sm text-muted-foreground">
            Built-in packs registered with the engine. Each pack is a typed,
            schema-validated tool agents call by name.
          </p>
        </div>
        <Badge variant="outline">
          <Package className="mr-1 h-3 w-3" />
          {data?.length ?? 0} packs
        </Badge>
      </div>

      <div className="relative max-w-sm">
        <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          placeholder="Search packs by name or description…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          className="pl-9"
        />
      </div>

      {error && (
        <Card>
          <CardHeader>
            <CardTitle className="text-destructive">Failed to load packs</CardTitle>
            <CardDescription>{error.message}</CardDescription>
          </CardHeader>
        </Card>
      )}

      {isLoading ? (
        <Card>
          <CardContent className="space-y-3 pt-6">
            <Skeleton className="h-8 w-full" />
            <Skeleton className="h-8 w-full" />
            <Skeleton className="h-8 w-full" />
            <Skeleton className="h-8 w-full" />
          </CardContent>
        </Card>
      ) : (
        !error && (
          <div className="space-y-6">
            {grouped.size === 0 ? (
              <Card>
                <CardContent className="py-12 text-center text-sm text-muted-foreground">
                  No packs match your search.
                </CardContent>
              </Card>
            ) : (
              Array.from(grouped.entries()).map(([ns, packs]) => (
                <div key={ns} className="space-y-2">
                  <div className="flex items-center gap-2">
                    <h2 className="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
                      {ns}
                    </h2>
                    <Badge variant="outline">{packs.length}</Badge>
                  </div>
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead className="w-1/3">Pack</TableHead>
                        <TableHead>Description</TableHead>
                        <TableHead className="w-24">Version</TableHead>
                        <TableHead className="w-16 text-right">Run</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {packs.length === 0 ? (
                        <TableEmpty colSpan={4}>No packs in this namespace</TableEmpty>
                      ) : (
                        packs.map((p) => (
                          <TableRow
                            key={p.name}
                            className="cursor-pointer"
                            onClick={() => setSelectedPack(p)}
                          >
                            <TableCell className="font-mono text-sm">{p.name}</TableCell>
                            <TableCell className="text-sm text-muted-foreground">
                              {p.description}
                            </TableCell>
                            <TableCell>
                              <Badge variant="outline">{p.latest ?? p.versions?.[0] ?? '—'}</Badge>
                            </TableCell>
                            <TableCell className="text-right">
                              <Button
                                variant="ghost"
                                size="sm"
                                onClick={(e) => {
                                  e.stopPropagation();
                                  setSelectedPack(p);
                                }}
                                aria-label={`Run ${p.name}`}
                              >
                                <Play className="h-4 w-4" />
                              </Button>
                            </TableCell>
                          </TableRow>
                        ))
                      )}
                    </TableBody>
                  </Table>
                </div>
              ))
            )}
          </div>
        )
      )}
      <PackRunnerDialog
        pack={selectedPack}
        onClose={() => setSelectedPack(null)}
      />
    </div>
  );
}

// PackRunnerDialog (T606a MVP) — modal panel that POSTs a JSON body
// to /api/v1/packs/{name} and renders the response. Schema-derived
// form rendering is intentionally not in the MVP — operators paste
// JSON directly. The schema-form upgrade is tracked as a v0.13.0
// follow-up.
function PackRunnerDialog({
  pack,
  onClose,
}: {
  pack: PackInfo | null;
  onClose: () => void;
}) {
  const authFetch = useAuthFetch();
  const [input, setInput] = useState('{}');
  const [response, setResponse] = useState<PackRunResult | null>(null);
  const [errorMsg, setErrorMsg] = useState<string | null>(null);
  const [running, setRunning] = useState(false);

  // Reset state when the dialog opens for a new pack so a stale
  // response from a prior run doesn't leak between packs.
  const dialogOpen = pack !== null;
  const packName = pack?.name ?? '';

  function handleClose() {
    setInput('{}');
    setResponse(null);
    setErrorMsg(null);
    setRunning(false);
    onClose();
  }

  async function handleRun() {
    if (!pack) return;
    setRunning(true);
    setResponse(null);
    setErrorMsg(null);

    // Lightweight client-side parse check — surface a friendly
    // error before round-tripping invalid JSON to the server, which
    // would return invalid_input anyway.
    try {
      JSON.parse(input);
    } catch (err) {
      setErrorMsg(`Invalid JSON: ${(err as Error).message}`);
      setRunning(false);
      return;
    }

    try {
      const res = await authFetch(`/api/v1/packs/${packName}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: input,
      });
      const text = await res.text();
      if (!res.ok) {
        setErrorMsg(`HTTP ${res.status}: ${text}`);
        return;
      }
      try {
        setResponse(JSON.parse(text) as PackRunResult);
      } catch {
        // Server should always return JSON, but if it doesn't,
        // surface the raw body for debugging.
        setErrorMsg(`Non-JSON response: ${text}`);
      }
    } catch (err) {
      setErrorMsg(`Request failed: ${(err as Error).message}`);
    } finally {
      setRunning(false);
    }
  }

  return (
    <Dialog open={dialogOpen} onOpenChange={(open) => !open && handleClose()}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle className="font-mono">{packName}</DialogTitle>
          <DialogDescription>{pack?.description}</DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <div>
            <label className="text-sm font-medium" htmlFor="pack-input">
              Input JSON
            </label>
            <p className="text-xs text-muted-foreground mb-1">
              Paste the pack's input body. See the pack reference at{' '}
              <code className="text-xs">/reference/packs/{packName.replace('.', '/')}</code> for the schema.
            </p>
            <Textarea
              id="pack-input"
              value={input}
              onChange={(e) => setInput(e.target.value)}
              rows={8}
              placeholder='{"key": "value"}'
            />
          </div>
          <div className="flex justify-end gap-2">
            <Button variant="outline" onClick={handleClose} disabled={running}>
              Close
            </Button>
            <Button onClick={handleRun} disabled={running}>
              {running ? 'Running…' : 'Run'}
            </Button>
          </div>
          {errorMsg && (
            <Card>
              <CardContent className="pt-4 text-sm text-destructive whitespace-pre-wrap font-mono">
                {errorMsg}
              </CardContent>
            </Card>
          )}
          {response && (
            <Card>
              <CardHeader className="pb-2">
                <CardTitle className="text-sm">Response</CardTitle>
                <CardDescription>
                  Duration: {formatDuration(response.duration_ms)}
                  {response.output && extractCostHint(response.output) && (
                    <> · Est. cost: {extractCostHint(response.output)}</>
                  )}
                  {response.session_id && <> · Session: <code>{response.session_id}</code></>}
                </CardDescription>
              </CardHeader>
              <CardContent>
                <pre className="overflow-auto text-xs bg-muted p-3 rounded-md max-h-[300px]">
                  {JSON.stringify(response, null, 2)}
                </pre>
              </CardContent>
            </Card>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}

interface PackRunResult {
  pack: string;
  version: string;
  output: Record<string, unknown> | null;
  artifacts?: Array<{ key: string; size: number; content_type: string }>;
  duration_ms: number;
  session_id?: string;
}

// Server's Result.Duration is a Go time.Duration serialised as
// nanoseconds via the default JSON encoding (NOT milliseconds —
// despite the `duration_ms` JSON key the field is ns). Convert to
// human-readable form.
function formatDuration(ns: number | undefined): string {
  if (!ns || ns <= 0) return '—';
  if (ns < 1_000_000) return `${(ns / 1_000).toFixed(1)}µs`;
  if (ns < 1_000_000_000) return `${(ns / 1_000_000).toFixed(1)}ms`;
  return `${(ns / 1_000_000_000).toFixed(2)}s`;
}

// Several content packs (podcast.generate, slides.narrate)
// surface `estimated_cost_usd` in their output map. Pull it out for
// the response header so operators can see cost without scanning
// the JSON blob.
function extractCostHint(out: Record<string, unknown>): string | null {
  const cost = out.estimated_cost_usd;
  if (typeof cost === 'number' && cost > 0) {
    return `$${cost.toFixed(4)}`;
  }
  return null;
}
