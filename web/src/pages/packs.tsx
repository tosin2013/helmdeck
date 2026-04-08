import { useMemo, useState } from 'react';
import { Package, Search } from 'lucide-react';

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
import { Input } from '@/components/ui/input';
import { Skeleton } from '@/components/ui/skeleton';
import { useApi } from '@/lib/queries';

interface PackInfo {
  name: string;
  description: string;
  versions?: string[];
  latest?: string;
}

// PacksPage (T606 — list view) shows the entire built-in pack
// catalog grouped by namespace. The Test Runner tab and Model
// Success Rates view (T607) land in T606a/T607.
export function PacksPage() {
  const { data, isLoading, error } = useApi<PackInfo[]>(['packs'], '/api/v1/packs');
  const [query, setQuery] = useState('');

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
            Built-in packs registered with the engine. Test runner and per-model
            success rates land with T606a/T607.
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
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {packs.length === 0 ? (
                        <TableEmpty colSpan={3}>No packs in this namespace</TableEmpty>
                      ) : (
                        packs.map((p) => (
                          <TableRow key={p.name}>
                            <TableCell className="font-mono text-sm">{p.name}</TableCell>
                            <TableCell className="text-sm text-muted-foreground">
                              {p.description}
                            </TableCell>
                            <TableCell>
                              <Badge variant="outline">{p.latest ?? p.versions?.[0] ?? '—'}</Badge>
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
    </div>
  );
}
