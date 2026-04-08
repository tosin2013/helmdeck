import { Boxes } from 'lucide-react';

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

interface MCPServer {
  id: string;
  name: string;
  transport: string;
  endpoint: string;
  status?: string;
  tool_count?: number;
  created_at?: string;
}

interface MCPListResponse {
  servers: MCPServer[];
}

// McpPage (T605) — registered MCP servers and the auto-derived
// tool catalog. Add Server modal + Tool Inspector drawer land in
// T605a.
export function McpPage() {
  const { data, isLoading, error } = useApi<MCPListResponse>(
    ['mcp-servers'],
    '/api/v1/mcp/servers',
  );

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">MCP Registry</h1>
          <p className="text-sm text-muted-foreground">
            External Model Context Protocol servers helmdeck proxies tools
            from. The built-in helmdeck-as-MCP server is always available
            and not listed here.
          </p>
        </div>
        <Badge variant="outline">
          <Boxes className="mr-1 h-3 w-3" />
          {data?.servers?.length ?? 0} servers
        </Badge>
      </div>

      {error && (
        <Card>
          <CardHeader>
            <CardTitle className="text-destructive">Failed to load servers</CardTitle>
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
                <TableHead>Transport</TableHead>
                <TableHead>Endpoint</TableHead>
                <TableHead>Tools</TableHead>
                <TableHead>Added</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {!data?.servers || data.servers.length === 0 ? (
                <TableEmpty colSpan={5}>
                  <Boxes className="mx-auto mb-2 h-8 w-8 text-muted-foreground/50" />
                  No external MCP servers registered. Add one via{' '}
                  <code className="rounded bg-muted px-1.5 py-0.5">POST /api/v1/mcp/servers</code>
                </TableEmpty>
              ) : (
                data.servers.map((s) => (
                  <TableRow key={s.id}>
                    <TableCell className="font-medium">{s.name}</TableCell>
                    <TableCell>
                      <Badge variant="outline">{s.transport}</Badge>
                    </TableCell>
                    <TableCell className="font-mono text-xs text-muted-foreground">
                      {s.endpoint}
                    </TableCell>
                    <TableCell>{s.tool_count ?? '—'}</TableCell>
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
