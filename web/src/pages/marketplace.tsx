// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

// Marketplace panel (T813 / #31). Browse, search, and install
// community capability packs from the configured marketplace
// (default: tosin2013/helmdeck-marketplace).
//
// API surface:
//   GET  /api/v1/marketplace/catalog       — list IndexEntry[]
//   GET  /api/v1/marketplace/installed     — list InstalledPack[]
//   GET  /api/v1/marketplace/packs/{name}  — full Manifest for detail
//   POST /api/v1/marketplace/install       — body {name}
//   POST /api/v1/marketplace/uninstall     — body {name}
//   POST /api/v1/marketplace/refresh       — clear cache + re-fetch
//
// Trust badges per ADR 034:
//   Core      — pack ships in the helmdeck binary (not surfaced here;
//               core packs aren't in the marketplace catalog).
//   Signed    — manifest carries a `trust:` block with signed_by + sha256.
//               cosign verification at install time confirms; for
//               v0.13.0 beta the badge shows "Signed (unverified)"
//               because the actual cosign call is still a stub.
//   Unsigned  — no trust block. UI shows a confirm dialog before install.

import { useCallback, useMemo, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import {
  AlertTriangle,
  CheckCircle2,
  ExternalLink,
  Package,
  RefreshCw,
  Search,
  ShieldCheck,
  ShieldQuestion,
  Tag,
  Trash2,
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
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Skeleton } from '@/components/ui/skeleton';
import { useApi, useAuthFetch } from '@/lib/queries';
import { cn } from '@/lib/utils';

// --- types matching the Go-side internal/marketplace shapes --------------

interface IndexEntry {
  name: string;
  version: string;
  path: string;
  description: string;
  author: string;
  category?: string;
  tags?: string[];
  installs?: number;
  stars?: number;
}

interface CatalogMeta {
  source: string;
  resolved_url?: string;
  fetched_at?: string;
  last_error?: string;
}

interface CatalogResponse {
  index: { catalog_version?: string; packs: IndexEntry[] };
  meta: CatalogMeta;
}

interface InstalledPack {
  name: string;
  version: string;
  source: string;
  path: string;
  installed_at: string;
  install_dir: string;
  trust_verified: boolean;
  trust_note?: string;
}

interface InstalledResponse {
  installed: InstalledPack[];
}

interface ManifestResponse {
  entry: IndexEntry;
  manifest: {
    name: string;
    version: string;
    author: string;
    license?: string;
    description: string;
    category?: string;
    tags?: string[];
    input_schema: { required?: string[]; properties?: Record<string, { type: string; description?: string }> };
    output_schema: { required?: string[]; properties?: Record<string, { type: string; description?: string }> };
    handler: { type: string; command?: string[]; sidecar?: { image: string }; timeout_s?: number; env?: string[] };
    examples?: { name: string; description?: string; input: Record<string, unknown>; expected_output_subset?: Record<string, unknown> }[];
    trust?: { signed_by?: string; sha256?: string };
  };
}

// --- helper: derive a trust label + color from manifest + install state ---

type TrustState = {
  label: string;
  tone: 'verified' | 'unsigned' | 'pending';
  detail?: string;
};

function trustFromManifest(m?: ManifestResponse['manifest'] | null, installed?: InstalledPack | null): TrustState {
  if (installed) {
    if (installed.trust_verified) {
      return { label: 'Signed', tone: 'verified', detail: installed.trust_note };
    }
    return { label: 'Unsigned', tone: 'unsigned', detail: installed.trust_note };
  }
  if (m?.trust?.signed_by) {
    return {
      label: `Signed by ${m.trust.signed_by}`,
      tone: 'pending',
      detail: 'cosign verification runs at install time; the badge will move to "Signed" after a successful install.',
    };
  }
  return { label: 'Unsigned', tone: 'unsigned' };
}

// --- main page -----------------------------------------------------------

export function MarketplacePage() {
  const [query, setQuery] = useState('');
  const [activeCategory, setActiveCategory] = useState<string | null>(null);
  const [detailFor, setDetailFor] = useState<string | null>(null);

  const catalog = useApi<CatalogResponse>(['marketplace', 'catalog'], '/api/v1/marketplace/catalog');
  const installed = useApi<InstalledResponse>(['marketplace', 'installed'], '/api/v1/marketplace/installed', {
    // Tolerate 503 (install endpoints not configured) without surfacing
    // as a fatal page error. The UI just shows no "Installed" badges.
    retry: false,
  });

  const installedByName = useMemo(() => {
    const m = new Map<string, InstalledPack>();
    for (const p of installed.data?.installed ?? []) m.set(p.name, p);
    return m;
  }, [installed.data]);

  const allCategories = useMemo(() => {
    const set = new Set<string>();
    for (const p of catalog.data?.index?.packs ?? []) {
      if (p.category) set.add(p.category);
    }
    return Array.from(set).sort();
  }, [catalog.data]);

  const visiblePacks = useMemo(() => {
    const packs = catalog.data?.index?.packs ?? [];
    const q = query.trim().toLowerCase();
    return packs.filter((p) => {
      if (activeCategory && p.category !== activeCategory) return false;
      if (!q) return true;
      return (
        p.name.toLowerCase().includes(q) ||
        p.description.toLowerCase().includes(q) ||
        (p.tags ?? []).some((t) => t.toLowerCase().includes(q))
      );
    });
  }, [catalog.data, query, activeCategory]);

  // --- mutations ----------------------------------------------------------

  const queryClient = useQueryClient();
  const authFetch = useAuthFetch();
  const [mutating, setMutating] = useState<string | null>(null);
  const [mutationError, setMutationError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    setMutating('refresh');
    setMutationError(null);
    try {
      const resp = await authFetch('/api/v1/marketplace/refresh', { method: 'POST' });
      if (!resp.ok) {
        const body = await resp.json().catch(() => ({}));
        throw new Error((body as { message?: string }).message ?? `HTTP ${resp.status}`);
      }
      await queryClient.invalidateQueries({ queryKey: ['marketplace', 'catalog'] });
    } catch (e) {
      setMutationError((e as Error).message);
    } finally {
      setMutating(null);
    }
  }, [authFetch, queryClient]);

  const install = useCallback(
    async (name: string) => {
      setMutating(name);
      setMutationError(null);
      try {
        const resp = await authFetch('/api/v1/marketplace/install', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ name }),
        });
        if (!resp.ok) {
          const body = await resp.json().catch(() => ({}));
          throw new Error((body as { message?: string }).message ?? `HTTP ${resp.status}`);
        }
        await Promise.all([
          queryClient.invalidateQueries({ queryKey: ['marketplace', 'installed'] }),
          queryClient.invalidateQueries({ queryKey: ['packs'] }),
        ]);
      } catch (e) {
        setMutationError((e as Error).message);
      } finally {
        setMutating(null);
      }
    },
    [authFetch, queryClient],
  );

  const uninstall = useCallback(
    async (name: string) => {
      setMutating(name);
      setMutationError(null);
      try {
        const resp = await authFetch('/api/v1/marketplace/uninstall', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ name }),
        });
        if (!resp.ok) {
          const body = await resp.json().catch(() => ({}));
          throw new Error((body as { message?: string }).message ?? `HTTP ${resp.status}`);
        }
        await Promise.all([
          queryClient.invalidateQueries({ queryKey: ['marketplace', 'installed'] }),
          queryClient.invalidateQueries({ queryKey: ['packs'] }),
        ]);
      } catch (e) {
        setMutationError((e as Error).message);
      } finally {
        setMutating(null);
      }
    },
    [authFetch, queryClient],
  );

  // --- render ------------------------------------------------------------

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Marketplace</h1>
          <p className="text-sm text-muted-foreground">
            Browse community packs. Click <span className="font-medium">Install</span> to hot-load
            into the running control plane — no restart, immediately callable via{' '}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">tools/list</code>.
          </p>
          {catalog.data?.meta?.source && (
            <p className="mt-1 text-xs text-muted-foreground">
              source:{' '}
              <a
                href={catalog.data.meta.source.replace(/\.git$/, '')}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-0.5 underline-offset-2 hover:underline"
              >
                {catalog.data.meta.source}
                <ExternalLink className="h-3 w-3" />
              </a>
              {catalog.data.meta.fetched_at && (
                <>
                  {' · '}
                  refreshed {new Date(catalog.data.meta.fetched_at).toLocaleString()}
                </>
              )}
            </p>
          )}
        </div>
        <div className="flex items-center gap-2">
          <Badge variant="outline">
            <Package className="mr-1 h-3 w-3" />
            {catalog.data?.index?.packs?.length ?? 0} packs
          </Badge>
          <Button
            variant="outline"
            size="sm"
            onClick={refresh}
            disabled={mutating === 'refresh'}
            aria-label="Refresh catalog"
          >
            <RefreshCw
              className={cn('mr-2 h-4 w-4', mutating === 'refresh' && 'animate-spin')}
            />
            Refresh
          </Button>
        </div>
      </div>

      {/* mutation error banner */}
      {mutationError && (
        <Card className="border-destructive">
          <CardHeader className="py-3">
            <CardTitle className="flex items-center gap-2 text-sm text-destructive">
              <AlertTriangle className="h-4 w-4" />
              {mutationError}
            </CardTitle>
          </CardHeader>
        </Card>
      )}

      {/* catalog meta error (e.g. last refresh failed but cached still served) */}
      {catalog.data?.meta?.last_error && (
        <Card className="border-amber-500/50">
          <CardHeader className="py-3">
            <CardTitle className="flex items-center gap-2 text-sm text-amber-600">
              <AlertTriangle className="h-4 w-4" />
              Last refresh failed: {catalog.data.meta.last_error}
            </CardTitle>
            <CardDescription>
              Showing the previously-cached catalog. Try refresh again or check the upstream.
            </CardDescription>
          </CardHeader>
        </Card>
      )}

      {/* hard failure: catalog endpoint not reachable */}
      {catalog.error && (
        <Card>
          <CardHeader>
            <CardTitle className="text-destructive">Failed to load catalog</CardTitle>
            <CardDescription>{catalog.error.message}</CardDescription>
          </CardHeader>
          <CardContent className="text-sm text-muted-foreground">
            The marketplace endpoint may be disabled (
            <code>HELMDECK_MARKETPLACE_DISABLE=1</code>) or unreachable. See{' '}
            <a
              href="https://github.com/tosin2013/helmdeck/blob/main/docs/reference/marketplace/catalog.md"
              className="underline"
              target="_blank"
              rel="noopener noreferrer"
            >
              docs/reference/marketplace/catalog.md
            </a>
            .
          </CardContent>
        </Card>
      )}

      {/* search + category filter */}
      {!catalog.error && (
        <div className="space-y-3">
          <div className="relative max-w-sm">
            <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              placeholder="Search by name, description, or tag…"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              className="pl-9"
            />
          </div>
          {allCategories.length > 0 && (
            <div className="flex flex-wrap gap-2">
              <CategoryChip
                label="All"
                active={activeCategory === null}
                onClick={() => setActiveCategory(null)}
              />
              {allCategories.map((c) => (
                <CategoryChip
                  key={c}
                  label={c}
                  active={activeCategory === c}
                  onClick={() => setActiveCategory(c)}
                />
              ))}
            </div>
          )}
        </div>
      )}

      {/* loading skeleton */}
      {catalog.isLoading && (
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {[0, 1, 2, 3, 4, 5].map((i) => (
            <Card key={i}>
              <CardHeader>
                <Skeleton className="h-5 w-2/3" />
                <Skeleton className="h-3 w-full" />
              </CardHeader>
              <CardContent>
                <Skeleton className="h-8 w-24" />
              </CardContent>
            </Card>
          ))}
        </div>
      )}

      {/* empty state */}
      {!catalog.isLoading && !catalog.error && visiblePacks.length === 0 && (
        <Card>
          <CardContent className="py-12 text-center text-sm text-muted-foreground">
            {query || activeCategory ? 'No packs match your filters.' : 'No packs in the catalog yet.'}
          </CardContent>
        </Card>
      )}

      {/* grid */}
      {!catalog.isLoading && visiblePacks.length > 0 && (
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {visiblePacks.map((p) => {
            const isInstalled = installedByName.has(p.name);
            const busy = mutating === p.name;
            return (
              <Card
                key={p.name}
                className="flex flex-col cursor-pointer transition-shadow hover:shadow-md"
                onClick={() => setDetailFor(p.name)}
              >
                <CardHeader className="space-y-1">
                  <div className="flex items-start justify-between gap-2">
                    <CardTitle className="font-mono text-base">{p.name}</CardTitle>
                    <Badge variant="outline" className="font-mono text-xs">
                      {p.version}
                    </Badge>
                  </div>
                  <CardDescription className="line-clamp-3 text-sm">
                    {p.description}
                  </CardDescription>
                </CardHeader>
                <CardContent className="mt-auto flex flex-col gap-3">
                  <div className="flex flex-wrap items-center gap-1">
                    {p.category && (
                      <Badge variant="secondary" className="text-xs">
                        {p.category}
                      </Badge>
                    )}
                    {(p.tags ?? []).slice(0, 3).map((t) => (
                      <Badge key={t} variant="outline" className="text-xs">
                        <Tag className="mr-1 h-3 w-3" />
                        {t}
                      </Badge>
                    ))}
                  </div>
                  <div className="flex items-center justify-between gap-2">
                    <span className="text-xs text-muted-foreground">by {p.author}</span>
                    {isInstalled ? (
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={(e) => {
                          e.stopPropagation();
                          uninstall(p.name);
                        }}
                        disabled={busy}
                      >
                        <Trash2 className="mr-2 h-4 w-4" />
                        {busy ? 'Uninstalling…' : 'Uninstall'}
                      </Button>
                    ) : (
                      <Button
                        size="sm"
                        onClick={(e) => {
                          e.stopPropagation();
                          install(p.name);
                        }}
                        disabled={busy}
                      >
                        {busy ? 'Installing…' : 'Install'}
                      </Button>
                    )}
                  </div>
                </CardContent>
              </Card>
            );
          })}
        </div>
      )}

      {/* detail dialog */}
      <PackDetailDialog
        packName={detailFor}
        onClose={() => setDetailFor(null)}
        installed={detailFor ? installedByName.get(detailFor) ?? null : null}
        onInstall={install}
        onUninstall={uninstall}
        mutating={mutating}
      />
    </div>
  );
}

// --- subcomponents -------------------------------------------------------

function CategoryChip({
  label,
  active,
  onClick,
}: {
  label: string;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        'rounded-full border px-3 py-1 text-xs transition-colors',
        active
          ? 'border-primary bg-primary text-primary-foreground'
          : 'border-input bg-background hover:bg-accent',
      )}
    >
      {label}
    </button>
  );
}

function PackDetailDialog({
  packName,
  onClose,
  installed,
  onInstall,
  onUninstall,
  mutating,
}: {
  packName: string | null;
  onClose: () => void;
  installed: InstalledPack | null;
  onInstall: (name: string) => void;
  onUninstall: (name: string) => void;
  mutating: string | null;
}) {
  const detail = useApi<ManifestResponse>(
    ['marketplace', 'packs', packName ?? ''],
    `/api/v1/marketplace/packs/${packName}`,
    { enabled: !!packName, retry: false },
  );
  const m = detail.data?.manifest;
  const trust = trustFromManifest(m, installed);
  const busy = packName !== null && mutating === packName;
  const [confirmUnsigned, setConfirmUnsigned] = useState(false);

  const handleInstallClick = () => {
    if (!packName) return;
    if (trust.tone === 'unsigned' && !confirmUnsigned) {
      setConfirmUnsigned(true);
      return;
    }
    setConfirmUnsigned(false);
    onInstall(packName);
  };

  return (
    <Dialog open={packName !== null} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-w-3xl max-h-[85vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle className="font-mono text-base">{packName}</DialogTitle>
          <DialogDescription>{m?.description}</DialogDescription>
        </DialogHeader>

        {detail.isLoading && (
          <div className="space-y-3">
            <Skeleton className="h-6 w-1/2" />
            <Skeleton className="h-6 w-full" />
            <Skeleton className="h-6 w-3/4" />
          </div>
        )}

        {detail.error && (
          <Card className="border-destructive">
            <CardHeader>
              <CardTitle className="text-destructive text-sm">
                Failed to load manifest
              </CardTitle>
              <CardDescription>{detail.error.message}</CardDescription>
            </CardHeader>
          </Card>
        )}

        {m && (
          <div className="space-y-4">
            <div className="flex flex-wrap items-center gap-2">
              <TrustBadge trust={trust} />
              {installed && (
                <Badge variant="default" className="bg-emerald-600">
                  <CheckCircle2 className="mr-1 h-3 w-3" />
                  Installed
                </Badge>
              )}
              <Badge variant="outline" className="font-mono">
                {m.version}
              </Badge>
              {m.license && <Badge variant="outline">{m.license}</Badge>}
              <span className="text-xs text-muted-foreground">by {m.author}</span>
            </div>

            {trust.detail && (
              <p className="text-xs text-muted-foreground">{trust.detail}</p>
            )}

            <SchemaSection title="Input schema" schema={m.input_schema} />
            <SchemaSection title="Output schema" schema={m.output_schema} />

            {m.handler && (
              <div>
                <h3 className="text-sm font-semibold">Handler</h3>
                <div className="mt-2 rounded-md bg-muted p-3 text-xs">
                  <div className="font-mono">
                    type: <span className="text-primary">{m.handler.type}</span>
                  </div>
                  {m.handler.command && (
                    <div className="font-mono">
                      command: <span className="text-primary">{m.handler.command.join(' ')}</span>
                    </div>
                  )}
                  {m.handler.sidecar?.image && (
                    <div className="font-mono">
                      sidecar: <span className="text-primary">{m.handler.sidecar.image}</span>
                    </div>
                  )}
                  {m.handler.timeout_s && (
                    <div className="font-mono">
                      timeout: <span className="text-primary">{m.handler.timeout_s}s</span>
                    </div>
                  )}
                </div>
              </div>
            )}

            {m.examples && m.examples.length > 0 && (
              <div>
                <h3 className="text-sm font-semibold">Examples ({m.examples.length})</h3>
                <div className="mt-2 space-y-2">
                  {m.examples.map((ex) => (
                    <div key={ex.name} className="rounded-md border bg-muted/30 p-3">
                      <div className="text-sm font-medium">{ex.name}</div>
                      {ex.description && (
                        <div className="text-xs text-muted-foreground">{ex.description}</div>
                      )}
                      <div className="mt-2 grid gap-2 text-xs sm:grid-cols-2">
                        <CodeBlock label="input" value={ex.input} />
                        {ex.expected_output_subset && (
                          <CodeBlock label="expected output ⊇" value={ex.expected_output_subset} />
                        )}
                      </div>
                    </div>
                  ))}
                </div>
              </div>
            )}
          </div>
        )}

        {confirmUnsigned && trust.tone === 'unsigned' && (
          <Card className="border-amber-500">
            <CardHeader className="py-3">
              <CardTitle className="flex items-center gap-2 text-sm text-amber-600">
                <ShieldQuestion className="h-4 w-4" />
                Install unsigned pack?
              </CardTitle>
              <CardDescription className="text-xs">
                This pack has no <code>trust:</code> block in its manifest, so cosign cannot
                verify the author identity. Install only if you trust the source.
              </CardDescription>
            </CardHeader>
          </Card>
        )}

        <div className="mt-4 flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
          {installed ? (
            <Button
              variant="outline"
              onClick={() => packName && onUninstall(packName)}
              disabled={busy}
            >
              <Trash2 className="mr-2 h-4 w-4" />
              {busy ? 'Uninstalling…' : 'Uninstall'}
            </Button>
          ) : (
            <Button onClick={handleInstallClick} disabled={busy || !m}>
              {busy
                ? 'Installing…'
                : confirmUnsigned
                  ? 'Yes, install anyway'
                  : trust.tone === 'unsigned'
                    ? 'Install (unsigned)'
                    : 'Install'}
            </Button>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}

function TrustBadge({ trust }: { trust: TrustState }) {
  const toneClass =
    trust.tone === 'verified'
      ? 'bg-emerald-600 text-white'
      : trust.tone === 'pending'
        ? 'bg-sky-600 text-white'
        : 'bg-amber-500 text-white';
  const Icon =
    trust.tone === 'verified' ? ShieldCheck : trust.tone === 'pending' ? ShieldCheck : ShieldQuestion;
  return (
    <Badge variant="default" className={cn('gap-1', toneClass)}>
      <Icon className="h-3 w-3" />
      {trust.label}
    </Badge>
  );
}

function SchemaSection({
  title,
  schema,
}: {
  title: string;
  schema?: { required?: string[]; properties?: Record<string, { type: string; description?: string }> };
}) {
  if (!schema || (!schema.properties && !schema.required)) return null;
  const required = new Set(schema.required ?? []);
  const props = Object.entries(schema.properties ?? {});
  return (
    <div>
      <h3 className="text-sm font-semibold">{title}</h3>
      {props.length === 0 ? (
        <p className="text-xs text-muted-foreground">No properties declared (accepts any JSON object).</p>
      ) : (
        <div className="mt-2 space-y-1">
          {props.map(([k, v]) => (
            <div key={k} className="flex items-baseline gap-2 text-xs">
              <code className="rounded bg-muted px-1.5 py-0.5 font-mono">{k}</code>
              <Badge variant="outline" className="text-[10px]">
                {v.type}
              </Badge>
              {required.has(k) && (
                <Badge variant="outline" className="text-[10px] border-amber-500 text-amber-600">
                  required
                </Badge>
              )}
              {v.description && <span className="text-muted-foreground">{v.description}</span>}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function CodeBlock({ label, value }: { label: string; value: unknown }) {
  return (
    <div>
      <div className="text-[10px] uppercase tracking-wide text-muted-foreground">{label}</div>
      <pre className="mt-1 max-h-32 overflow-auto rounded bg-background p-2 font-mono text-[11px]">
        {JSON.stringify(value, null, 2)}
      </pre>
    </div>
  );
}
