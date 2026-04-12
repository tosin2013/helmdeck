import { Cookie, Github, Globe, Key, KeyRound, Lock, Plus, ShieldCheck, Terminal } from 'lucide-react';
import { useState } from 'react';

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
  DialogTrigger,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
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

// Presets for common credential types — each fills the form with
// sensible defaults so the operator doesn't have to look up the
// right type + host_pattern combination.
const PRESETS = [
  {
    id: 'github-pat',
    label: 'GitHub PAT',
    icon: Github,
    defaults: { name: 'github-token', type: 'api_key', host_pattern: 'github.com' },
    description: 'Personal access token for cloning/pushing private repos via HTTPS. Required scopes: repo.',
    placeholder: 'ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx',
  },
  {
    id: 'api-key',
    label: 'API Key',
    icon: Key,
    defaults: { name: '', type: 'api_key', host_pattern: '' },
    description: 'Generic API key or bearer token. Used by http.fetch via ${vault:NAME} placeholder substitution.',
    placeholder: 'sk-...',
  },
  {
    id: 'ssh-key',
    label: 'SSH Key',
    icon: Terminal,
    defaults: { name: 'deploy-key', type: 'ssh', host_pattern: 'github.com' },
    description: 'SSH private key for git clone/push over SSH. The key is written to a temp file inside the session container, used for one operation, then shredded.',
    placeholder: '-----BEGIN OPENSSH PRIVATE KEY-----',
  },
  {
    id: 'login',
    label: 'Login',
    icon: Lock,
    defaults: { name: '', type: 'login', host_pattern: '' },
    description: 'Username + password credentials. Store the password as the credential value; put the username in the host_pattern or as JSON metadata.',
    placeholder: 'password123',
  },
  {
    id: 'cookie',
    label: 'Cookie',
    icon: Cookie,
    defaults: { name: '', type: 'cookie', host_pattern: '' },
    description: 'Browser session cookie(s) for CDP cookie injection (T503). Paste the cookie value or a JSON array of cookie objects.',
    placeholder: 'session_id=abc123; path=/; domain=.example.com',
  },
  {
    id: 'oauth',
    label: 'OAuth Token',
    icon: ShieldCheck,
    defaults: { name: '', type: 'oauth', host_pattern: '' },
    description: 'OAuth access + refresh token pair. Store the access token as the credential value. Refresh token support is a follow-on (T501b).',
    placeholder: 'ya29.a0AfB_...',
  },
  {
    id: 'openrouter',
    label: 'OpenRouter',
    icon: Globe,
    defaults: { name: 'openrouter-key', type: 'api_key', host_pattern: 'openrouter.ai' },
    description: 'OpenRouter API key for the AI gateway. Used by the helmdeck LLM gateway to route chat completions through OpenRouter.',
    placeholder: 'sk-or-v1-...',
  },
];

// VaultPage (T610 + T610a) — credential list + Add Credential modal
// with GitHub PAT preset button.
export function VaultPage() {
  const { data, isLoading, error, refetch } = useApi<CredentialsResponse>(
    ['vault-credentials'],
    '/api/v1/vault/credentials',
  );
  const [modalOpen, setModalOpen] = useState(false);

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">
            Credential Vault
          </h1>
          <p className="text-sm text-muted-foreground">
            Encrypted credentials available to pack handlers via the
            <code className="mx-1 rounded bg-muted px-1.5 py-0.5">
              ${'{vault:NAME}'}
            </code>
            placeholder syntax. Plaintext is never returned over the API.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Badge variant="outline">
            <KeyRound className="mr-1 h-3 w-3" />
            {data?.count ?? 0} credentials
          </Badge>
          <Dialog open={modalOpen} onOpenChange={setModalOpen}>
            <DialogTrigger asChild>
              <Button size="sm">
                <Plus className="mr-1 h-4 w-4" />
                Add Credential
              </Button>
            </DialogTrigger>
            <DialogContent>
              <AddCredentialModal
                onSuccess={() => {
                  setModalOpen(false);
                  refetch();
                }}
              />
            </DialogContent>
          </Dialog>
        </div>
      </div>

      {error && (
        <Card>
          <CardHeader>
            <CardTitle className="text-destructive">
              Failed to load credentials
            </CardTitle>
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
                  No credentials in the vault. Click{' '}
                  <strong>Add Credential</strong> to get started.
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
                        <span className="text-muted-foreground">
                          {' '}
                          {c.path_pattern}
                        </span>
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

function AddCredentialModal({ onSuccess }: { onSuccess: () => void }) {
  const authFetch = useAuthFetch();
  const [name, setName] = useState('');
  const [type, setType] = useState('api_key');
  const [hostPattern, setHostPattern] = useState('');
  const [plaintext, setPlaintext] = useState('');
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const [placeholder, setPlaceholder] = useState('');

  const applyPreset = (preset: (typeof PRESETS)[number]) => {
    setName(preset.defaults.name);
    setType(preset.defaults.type);
    setHostPattern(preset.defaults.host_pattern);
    setPlaceholder(preset.placeholder);
    setError('');
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name || !plaintext) {
      setError('Name and credential value are required');
      return;
    }
    setSaving(true);
    setError('');
    try {
      const resp = await authFetch('/api/v1/vault/credentials', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          name,
          type,
          host_pattern: hostPattern || undefined,
          plaintext_b64: btoa(plaintext),
        }),
      });
      if (resp.ok) {
        onSuccess();
      } else {
        const body = await resp.json().catch(() => ({}));
        setError(
          body.message ||
            `Failed to create credential (HTTP ${resp.status})`,
        );
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Network error');
    } finally {
      setSaving(false);
    }
  };

  return (
    <>
      <DialogHeader>
        <DialogTitle>Add Credential</DialogTitle>
        <DialogDescription>
          Store an encrypted credential in the vault. Use it in pack calls
          via{' '}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">
            ${'{vault:' + (name || 'NAME') + '}'}
          </code>
        </DialogDescription>
      </DialogHeader>

      {/* Preset buttons */}
      <div className="flex gap-2">
        {PRESETS.map((p) => (
          <Button
            key={p.id}
            variant="outline"
            size="sm"
            onClick={() => applyPreset(p)}
            className="gap-1.5"
          >
            <p.icon className="h-3.5 w-3.5" />
            {p.label}
          </Button>
        ))}
      </div>

      <form onSubmit={handleSubmit} className="space-y-4">
        <div className="space-y-2">
          <Label htmlFor="cred-name">Name</Label>
          <Input
            id="cred-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="github-token"
          />
          <p className="text-xs text-muted-foreground">
            Referenced as{' '}
            <code className="rounded bg-muted px-1 py-0.5">
              ${'{vault:' + (name || '...') + '}'}
            </code>{' '}
            in pack inputs
          </p>
        </div>

        <div className="grid grid-cols-2 gap-4">
          <div className="space-y-2">
            <Label htmlFor="cred-type">Type</Label>
            <Input
              id="cred-type"
              value={type}
              onChange={(e) => setType(e.target.value)}
              placeholder="api_key"
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="cred-host">Host pattern</Label>
            <Input
              id="cred-host"
              value={hostPattern}
              onChange={(e) => setHostPattern(e.target.value)}
              placeholder="github.com"
            />
          </div>
        </div>

        <div className="space-y-2">
          <Label htmlFor="cred-value">Credential value</Label>
          <Input
            id="cred-value"
            type="password"
            value={plaintext}
            onChange={(e) => setPlaintext(e.target.value)}
            placeholder={placeholder || 'paste your token or key here'}
          />
          <p className="text-xs text-muted-foreground">
            Encrypted with AES-256-GCM before storage. Never returned in
            plaintext over the API.
          </p>
        </div>

        {error && (
          <p className="text-sm text-destructive">{error}</p>
        )}

        <Button type="submit" className="w-full" disabled={saving}>
          {saving ? 'Saving...' : 'Add to Vault'}
        </Button>
      </form>
    </>
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
