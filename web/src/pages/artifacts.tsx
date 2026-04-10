import { Archive, Download, Eye, Image } from 'lucide-react';

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
import { Button } from '@/components/ui/button';
import { useApi } from '@/lib/queries';
import { formatRelative } from '@/lib/format';
import { useState } from 'react';

interface ArtifactEntry {
  key: string;
  url: string;
  size: number;
  content_type: string;
  created_at: string;
  pack: string;
}

interface ArtifactsResponse {
  artifacts: ArtifactEntry[];
  count: number;
}

// ArtifactsPage (T613, ADR 032) — standalone Artifact Explorer panel.
// Lists recent artifacts with inline image preview and download.
export function ArtifactsPage() {
  const { data, isLoading, error, refetch } = useApi<ArtifactsResponse>(
    ['artifacts'],
    '/api/v1/artifacts?limit=50',
  );

  const [previewKey, setPreviewKey] = useState<string | null>(null);

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Artifacts</h1>
          <p className="text-sm text-muted-foreground">
            Files produced by capability pack runs — screenshots, PDFs, OCR
            source images, rendered slides. Signed URLs expire in 15 minutes;
            click Refresh for fresh links.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Badge variant="outline">
            <Archive className="mr-1 h-3 w-3" />
            {data?.count ?? 0} artifacts
          </Badge>
          <Button variant="outline" size="sm" onClick={() => refetch()}>
            Refresh
          </Button>
        </div>
      </div>

      {error && (
        <Card>
          <CardHeader>
            <CardTitle className="text-destructive">
              Failed to load artifacts
            </CardTitle>
            <CardDescription>{error.message}</CardDescription>
          </CardHeader>
          <CardContent className="text-sm text-muted-foreground">
            The S3 artifact store may be unavailable or{' '}
            <code className="rounded bg-muted px-1.5 py-0.5">
              HELMDECK_S3_ENDPOINT
            </code>{' '}
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
          <>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-12"></TableHead>
                  <TableHead>Pack</TableHead>
                  <TableHead>Key</TableHead>
                  <TableHead>Type</TableHead>
                  <TableHead className="text-right">Size</TableHead>
                  <TableHead>Created</TableHead>
                  <TableHead className="w-24">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {!data?.artifacts || data.artifacts.length === 0 ? (
                  <TableEmpty colSpan={7}>
                    <Archive className="mx-auto mb-2 h-8 w-8 text-muted-foreground/50" />
                    No artifacts yet. Run a pack that produces output
                    (e.g.{' '}
                    <code className="rounded bg-muted px-1.5 py-0.5">
                      browser.screenshot_url
                    </code>
                    ) and refresh.
                  </TableEmpty>
                ) : (
                  data.artifacts.map((a) => (
                    <TableRow key={a.key}>
                      <TableCell>
                        {isImage(a.content_type) ? (
                          <Image className="h-4 w-4 text-muted-foreground" />
                        ) : (
                          <Archive className="h-4 w-4 text-muted-foreground" />
                        )}
                      </TableCell>
                      <TableCell>
                        <Badge variant="outline">{a.pack}</Badge>
                      </TableCell>
                      <TableCell className="max-w-[200px] truncate font-mono text-xs text-muted-foreground">
                        {a.key.split('/').pop()}
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        {a.content_type}
                      </TableCell>
                      <TableCell className="text-right text-xs">
                        {formatBytes(a.size)}
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        {formatRelative(a.created_at)}
                      </TableCell>
                      <TableCell>
                        <div className="flex items-center gap-1">
                          {isImage(a.content_type) && (
                            <Button
                              variant="ghost"
                              size="icon"
                              className="h-7 w-7"
                              title="Preview"
                              onClick={() =>
                                setPreviewKey(
                                  previewKey === a.key ? null : a.key,
                                )
                              }
                            >
                              <Eye className="h-3.5 w-3.5" />
                            </Button>
                          )}
                          <a href={a.url} target="_blank" rel="noreferrer">
                            <Button
                              variant="ghost"
                              size="icon"
                              className="h-7 w-7"
                              title="Download"
                            >
                              <Download className="h-3.5 w-3.5" />
                            </Button>
                          </a>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))
                )}
              </TableBody>
            </Table>

            {previewKey && data?.artifacts && (
              <Card>
                <CardHeader>
                  <CardTitle className="text-sm">Preview</CardTitle>
                  <CardDescription className="font-mono text-xs">
                    {previewKey}
                  </CardDescription>
                </CardHeader>
                <CardContent>
                  <img
                    src={
                      data.artifacts.find((a) => a.key === previewKey)?.url ??
                      ''
                    }
                    alt={previewKey}
                    className="max-h-[400px] rounded border"
                  />
                </CardContent>
              </Card>
            )}
          </>
        )
      )}
    </div>
  );
}

function isImage(ct: string): boolean {
  return ct.startsWith('image/');
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}
