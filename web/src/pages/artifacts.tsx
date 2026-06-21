import { Archive, Copy, Download, Eye, Image, Trash2, Upload as UploadIcon } from 'lucide-react';

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
import { useApi, useAuthFetch } from '@/lib/queries';
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
  const authFetch = useAuthFetch();
  const [deletingKey, setDeletingKey] = useState<string | null>(null);
  const [deleteError, setDeleteError] = useState<string | null>(null);

  // Operator upload state. Drag-drop OR file input both pipe into the
  // same handler; isDragging just drives the visual highlight on the
  // drop zone. uploadedKey is the most-recent success — surfaced with
  // a copy button so the operator can paste it into a pipeline call
  // (e.g. builtin.byo-audio-narrated-video's audio_artifact_key input).
  const [isDragging, setIsDragging] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [uploadError, setUploadError] = useState<string | null>(null);
  const [uploadedKey, setUploadedKey] = useState<string | null>(null);
  const [uploadedFilename, setUploadedFilename] = useState<string | null>(null);
  const [keyCopied, setKeyCopied] = useState(false);

  async function handleUpload(file: File) {
    setUploading(true);
    setUploadError(null);
    setUploadedKey(null);
    setKeyCopied(false);
    try {
      const form = new FormData();
      form.append('file', file);
      const resp = await authFetch('/api/v1/artifacts/upload', {
        method: 'POST',
        body: form,
      });
      if (!resp.ok) {
        const body = await resp.json().catch(() => ({}));
        throw new Error(
          (body as { message?: string }).message ?? `HTTP ${resp.status}`,
        );
      }
      const out = (await resp.json()) as {
        artifact_key: string;
        filename: string;
      };
      setUploadedKey(out.artifact_key);
      setUploadedFilename(out.filename);
      await refetch();
    } catch (e) {
      setUploadError((e as Error).message);
    } finally {
      setUploading(false);
    }
  }

  async function copyKey() {
    if (!uploadedKey) return;
    try {
      await navigator.clipboard.writeText(uploadedKey);
      setKeyCopied(true);
      setTimeout(() => setKeyCopied(false), 2000);
    } catch {
      // clipboard API may be unavailable on http:// origins — fall
      // back to leaving the key visible for manual copy.
    }
  }

  async function handleDelete(key: string) {
    if (!window.confirm(`Delete artifact?\n\n${key}\n\nThis cannot be undone.`)) {
      return;
    }
    setDeletingKey(key);
    setDeleteError(null);
    try {
      // Key extraction on the server is a path-prefix trim, mirroring
      // the download route — pass the raw key as path segments.
      const resp = await authFetch(`/api/v1/artifacts/${key}`, {
        method: 'DELETE',
      });
      if (!resp.ok) {
        const body = await resp.json().catch(() => ({}));
        throw new Error(
          (body as { message?: string }).message ?? `HTTP ${resp.status}`,
        );
      }
      if (previewKey === key) setPreviewKey(null);
      await refetch();
    } catch (e) {
      setDeleteError((e as Error).message);
    } finally {
      setDeletingKey(null);
    }
  }

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

      {/* Upload card — operator drops a file, gets an artifact_key
          they can paste into pipeline calls (e.g. the BYO-audio
          pipeline's audio_artifact_key input). */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">Upload an artifact</CardTitle>
          <CardDescription>
            Drag a file here or click to choose. Returns an
            <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
              artifact_key
            </code>
            you can paste into pipeline inputs — e.g.
            <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
              audio_artifact_key
            </code>
            on
            <code className="mx-1 rounded bg-muted px-1 py-0.5 text-xs">
              builtin.byo-audio-narrated-video
            </code>
            . 100 MiB cap.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <div
            onDragOver={(e) => {
              e.preventDefault();
              setIsDragging(true);
            }}
            onDragLeave={() => setIsDragging(false)}
            onDrop={(e) => {
              e.preventDefault();
              setIsDragging(false);
              const file = e.dataTransfer.files?.[0];
              if (file) void handleUpload(file);
            }}
            className={`flex flex-col items-center justify-center gap-2 rounded-md border-2 border-dashed p-6 text-sm transition-colors ${
              isDragging
                ? 'border-primary bg-primary/5'
                : 'border-muted-foreground/25 bg-muted/30'
            } ${uploading ? 'opacity-60' : ''}`}
          >
            <UploadIcon className="h-6 w-6 text-muted-foreground" />
            <p className="text-muted-foreground">
              {uploading
                ? 'Uploading…'
                : 'Drag a file here, or click below to choose'}
            </p>
            <label className="cursor-pointer">
              <input
                type="file"
                className="hidden"
                disabled={uploading}
                onChange={(e) => {
                  const file = e.target.files?.[0];
                  if (file) void handleUpload(file);
                  e.target.value = ''; // allow re-selecting the same file
                }}
              />
              <span className="text-primary underline-offset-2 hover:underline">
                choose file…
              </span>
            </label>
          </div>
          {uploadError && (
            <p className="text-sm text-destructive">{uploadError}</p>
          )}
          {uploadedKey && (
            <div className="space-y-1 rounded-md border border-green-500/30 bg-green-500/5 p-3 text-sm">
              <p className="font-medium text-green-700 dark:text-green-300">
                Uploaded {uploadedFilename ?? 'file'}
              </p>
              <div className="flex items-center gap-2">
                <code className="flex-1 overflow-x-auto rounded bg-muted px-2 py-1 text-xs">
                  {uploadedKey}
                </code>
                <Button size="sm" variant="outline" onClick={() => void copyKey()}>
                  <Copy className="mr-1 h-3 w-3" />
                  {keyCopied ? 'Copied' : 'Copy'}
                </Button>
              </div>
              <p className="text-xs text-muted-foreground">
                Paste this value into a pipeline call's
                <code className="mx-1 rounded bg-muted px-1 py-0.5">
                  audio_artifact_key
                </code>
                (or matching) input.
              </p>
            </div>
          )}
        </CardContent>
      </Card>

      {deleteError && (
        <Card>
          <CardHeader>
            <CardTitle className="text-destructive">
              Failed to delete artifact
            </CardTitle>
            <CardDescription>{deleteError}</CardDescription>
          </CardHeader>
        </Card>
      )}

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
                          <Button
                            variant="ghost"
                            size="icon"
                            className="h-7 w-7 text-muted-foreground hover:text-destructive"
                            title="Delete"
                            disabled={deletingKey === a.key}
                            onClick={() => handleDelete(a.key)}
                          >
                            <Trash2 className="h-3.5 w-3.5" />
                          </Button>
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
