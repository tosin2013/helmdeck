import { Key, KeyRound } from 'lucide-react';

import { Badge } from '@/components/ui/badge';
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
import { useApi } from '@/lib/queries';
import { formatRelative } from '@/lib/format';

interface ProviderKey {
  id: string;
  provider: string;
  label: string;
  fingerprint: string;
  last4: string;
  created_at: string;
  updated_at: string;
  last_used_at?: string;
}

// ProvidersPage (T604) — operator view of every AI provider key
// stored in the AES-256-GCM keystore (T203). Reads from
// GET /api/v1/providers/keys; the same endpoint backs the Add and
// Rotate flows landing in T604a. The actual key bytes never leave
// the keystore — the table only ever shows fingerprint + last4 so
// operators can identify which key is which without leaking material.
export function ProvidersPage() {
  const { data, isLoading, error } = useApi<ProviderKey[]>(
    ['providers', 'keys'],
    '/api/v1/providers/keys',
    { refetchInterval: 15_000 },
  );

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">AI Providers</h1>
          <p className="text-sm text-muted-foreground">
            API keys for the OpenAI-compatible facade at{' '}
            <code className="rounded bg-muted px-1.5 py-0.5">/v1/chat/completions</code>.
            Keys are stored AES-256-GCM encrypted in the keystore (ADR
            011); only fingerprint and last 4 chars are shown here.
          </p>
        </div>
        <Badge variant="outline">
          <KeyRound className="mr-1 h-3 w-3" />
          {data?.length ?? 0} keys
        </Badge>
      </div>

      {error && (
        <Card>
          <CardHeader>
            <CardTitle className="text-destructive">
              Failed to load provider keys
            </CardTitle>
            <CardDescription>{error.message}</CardDescription>
          </CardHeader>
          <CardContent className="text-sm text-muted-foreground">
            The keystore may be unavailable or{' '}
            <code className="rounded bg-muted px-1.5 py-0.5">HELMDECK_KEYSTORE_KEY</code>{' '}
            was not set when the control plane started.
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
                <TableHead>Provider</TableHead>
                <TableHead>Label</TableHead>
                <TableHead>Fingerprint</TableHead>
                <TableHead>Last 4</TableHead>
                <TableHead>Created</TableHead>
                <TableHead>Last used</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {!data || data.length === 0 ? (
                <TableEmpty colSpan={6}>
                  <Key className="mx-auto mb-2 h-8 w-8 text-muted-foreground/50" />
                  No provider keys configured. Add one via{' '}
                  <code className="rounded bg-muted px-1.5 py-0.5">
                    POST /api/v1/providers/keys
                  </code>
                  . The Add Key modal lands in T604a.
                </TableEmpty>
              ) : (
                data.map((k) => (
                  <TableRow key={k.id}>
                    <TableCell>
                      <Badge variant="outline">{k.provider}</Badge>
                    </TableCell>
                    <TableCell>{k.label || '—'}</TableCell>
                    <TableCell className="font-mono text-xs text-muted-foreground">
                      {k.fingerprint.slice(0, 12)}…
                    </TableCell>
                    <TableCell className="font-mono text-xs">
                      …{k.last4}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {formatRelative(k.created_at)}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {k.last_used_at ? formatRelative(k.last_used_at) : 'never'}
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
