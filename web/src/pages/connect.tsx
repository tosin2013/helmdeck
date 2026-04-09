import { useState } from 'react';
import { Check, Copy, Plug, Terminal } from 'lucide-react';

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
import { useApi } from '@/lib/queries';

interface ConnectSnippet {
  client: string;
  install_path: string;
  format?: 'json' | 'yaml';
  config: Record<string, unknown>;
}

// One card per supported client. The list mirrors the closed set
// in internal/api/connect.go's connectSnippet switch — keep them in
// sync. NemoClaw is intentionally absent: it reuses OpenClaw's
// schema inside the NemoClaw sandbox (see docs/integrations/nemoclaw.md).
const CLIENTS: { id: string; label: string; doc: string }[] = [
  { id: 'claude-code', label: 'Claude Code', doc: 'docs/integrations/claude-code.md' },
  { id: 'claude-desktop', label: 'Claude Desktop', doc: 'docs/integrations/claude-desktop.md' },
  { id: 'openclaw', label: 'OpenClaw', doc: 'docs/integrations/openclaw.md' },
  { id: 'gemini-cli', label: 'Gemini CLI', doc: 'docs/integrations/gemini-cli.md' },
  { id: 'hermes-agent', label: 'Hermes Agent', doc: 'docs/integrations/hermes-agent.md' },
];

// ConnectPage (T612) — operator-facing index of MCP client config
// snippets. Each card fetches GET /api/v1/connect/{client} and
// renders the install path + a copy-pasteable JSON (or YAML for
// Hermes) snippet that operators paste into the named client's
// MCP config file. The Phase 6 vision (T612a) is to add OS-detected
// one-liner buttons that fetch + write the config in place; for
// v0.6.0 the manual copy flow is enough to unblock real installs.
export function ConnectPage() {
  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">
            Connect Clients
          </h1>
          <p className="text-sm text-muted-foreground">
            One config snippet per supported MCP client. Each spawns
            the <code className="rounded bg-muted px-1.5 py-0.5">helmdeck-mcp</code>{' '}
            stdio bridge with the right environment so the client can
            call every helmdeck pack. See <code>docs/integrations/</code>{' '}
            for setup walkthroughs.
          </p>
        </div>
        <Badge variant="outline">
          <Plug className="mr-1 h-3 w-3" />
          {CLIENTS.length} clients
        </Badge>
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        {CLIENTS.map((c) => (
          <ClientCard key={c.id} {...c} />
        ))}
      </div>
    </div>
  );
}

function ClientCard({ id, label, doc }: { id: string; label: string; doc: string }) {
  const { data, isLoading, error } = useApi<ConnectSnippet>(
    ['connect', id],
    `/api/v1/connect/${id}`,
  );

  return (
    <Card>
      <CardHeader>
        <div className="flex items-start justify-between">
          <div>
            <CardTitle className="text-base">{label}</CardTitle>
            <CardDescription>
              <code className="rounded bg-muted px-1.5 py-0.5 text-xs">
                {data?.install_path ?? doc}
              </code>
            </CardDescription>
          </div>
          {data?.format === 'yaml' && <Badge variant="warning">YAML</Badge>}
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        {error && (
          <p className="text-xs text-destructive">
            Failed to load snippet: {error.message}
          </p>
        )}
        {isLoading ? (
          <Skeleton className="h-32 w-full" />
        ) : data ? (
          <SnippetBlock data={data} />
        ) : null}
        <p className="text-xs text-muted-foreground">
          Setup walkthrough:{' '}
          <code className="rounded bg-muted px-1.5 py-0.5">{doc}</code>
        </p>
      </CardContent>
    </Card>
  );
}

function SnippetBlock({ data }: { data: ConnectSnippet }) {
  const text = JSON.stringify(data.config, null, 2);
  const [copied, setCopied] = useState(false);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // Some browsers (or non-HTTPS contexts) reject clipboard
      // writes. Fall through silently — the operator can still
      // select the text manually.
    }
  };

  return (
    <div className="relative">
      <pre className="max-h-64 overflow-auto rounded-md border bg-muted/40 p-3 text-xs leading-relaxed">
        <code>{text}</code>
      </pre>
      <Button
        size="sm"
        variant="outline"
        className="absolute right-2 top-2 h-7 px-2"
        onClick={copy}
      >
        {copied ? (
          <>
            <Check className="mr-1 h-3 w-3" /> Copied
          </>
        ) : (
          <>
            <Copy className="mr-1 h-3 w-3" /> Copy
          </>
        )}
      </Button>
      {data.format === 'yaml' && (
        <p className="mt-2 flex items-center gap-1 text-xs text-muted-foreground">
          <Terminal className="h-3 w-3" />
          This client expects YAML — translate the JSON above before
          pasting into{' '}
          <code className="rounded bg-muted px-1 py-0.5">{data.install_path}</code>.
        </p>
      )}
    </div>
  );
}
