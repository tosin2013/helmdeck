import { KeyRound } from 'lucide-react';

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
import { formatRelative, truncate } from '@/lib/format';

interface Credential {
  id: string;
  name: string;
  type: string;
  host_pattern: string;
  path_pattern?: string;
  fingerprint: string;
  created_at: string;
  updated_at: string;
  last_used_at?: string | null;
  metadata?: Record<string, unknown>;
}

interface CredentialsResponse {
  credentials: Credential[];
  count: number;
}

// VaultPage (T610 — list view) shows the redacted credential
// catalog. Add Credential modal, ACL grants, and the per-credential
// Usage Log tab land in T610a.
export function VaultPage() {
  const { data, isLoading, error } = useApi<CredentialsResponse>(
    ['vault-credentials'],
    '/api/v1/vault/credentials',
  );

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Credential Vault</h1>
          <p className="text-sm text-muted-foreground">
            Encrypted credentials available to pack handlers via the
            <code className="mx-1 rounded bg-muted px-1.5 py-0.5">${'{vault:NAME}'}</code>
            placeholder syntax. Plaintext is never returned over the API.
          </p>
        </div>
        <Badge variant="outline">
          <KeyRound className="mr-1 h-3 w-3" />
          {data?.count ?? 0} credentials
        </Badge>
      </div>

      {error && (
        <Card>
          <CardHeader>
            <CardTitle className="text-destructive">Failed to load credentials</CardTitle>
            <CardDescription>{error.message}</CardDescription>
          </CardHeader>
        </Card>
      )}

      {isLoading ? (
        <Card>
          <CardContent className="space-y-3 pt-6">
            <Skeleton className="h-8 w-full" />
            <Skeleton className="h-8 w-full" />
          </CardContent>
        </Card>
      ) : (
        !error && (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Type</TableHead>
                <TableHead>Host pattern</TableHead>
                <TableHead>Fingerprint</TableHead>
                <TableHead>Last used</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {!data?.credentials || data.credentials.length === 0 ? (
                <TableEmpty colSpan={5}>
                  <KeyRound className="mx-auto mb-2 h-8 w-8 text-muted-foreground/50" />
                  No credentials in the vault. Add one via{' '}
                  <code className="rounded bg-muted px-1.5 py-0.5">POST /api/v1/vault/credentials</code>
                </TableEmpty>
              ) : (
                data.credentials.map((c) => (
                  <TableRow key={c.id}>
                    <TableCell className="font-medium">{c.name}</TableCell>
                    <TableCell>
                      <CredentialTypeBadge type={c.type} />
                    </TableCell>
                    <TableCell className="font-mono text-xs">
                      {c.host_pattern}
                      {c.path_pattern && (
                        <span className="text-muted-foreground"> {c.path_pattern}</span>
                      )}
                    </TableCell>
                    <TableCell className="font-mono text-xs text-muted-foreground">
                      {truncate(c.fingerprint, 16)}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {formatRelative(c.last_used_at)}
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

function CredentialTypeBadge({ type }: { type: string }) {
  const v =
    type === 'ssh' || type === 'oauth'
      ? 'success'
      : type === 'login' || type === 'cookie'
        ? 'warning'
        : 'default';
  return <Badge variant={v}>{type}</Badge>;
}
