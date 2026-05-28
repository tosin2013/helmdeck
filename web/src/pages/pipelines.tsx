import { useState, type MouseEvent } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { Bug, Check, ClipboardCopy, GitBranch, Play, RotateCw, Workflow } from 'lucide-react';

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
import { Skeleton } from '@/components/ui/skeleton';
import { Textarea } from '@/components/ui/textarea';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { useApi, useAuthFetch } from '@/lib/queries';
import { formatRelative } from '@/lib/format';

interface Step {
  id: string;
  pack: string;
  version?: string;
  // Raw step input; may contain ${{ inputs.* }} refs we scan to build the
  // copy-prompt template. The API returns it on the list response.
  input?: unknown;
}

interface Pipeline {
  id: string;
  name: string;
  description?: string;
  builtin: boolean;
  steps: Step[];
  updated_at: string;
}

interface RunStep {
  step_id: string;
  pack: string;
  status: string;
  error?: string;
  error_code?: string;
  failure_class?: string;
  failure_reason?: string;
}

interface Run {
  id: string;
  pipeline_id: string;
  status: string;
  error?: string;
  failure_class?: string;
  failure_reason?: string;
  steps: RunStep[];
  started_at: string;
  ended_at?: string;
}

// PipelinesPage (ADR 041) — list pipelines (built-in starters + the ones
// agents create via the helmdeck__pipeline-* MCP tools), trigger runs with
// inputs, and watch run status/history poll live. Definitions are created
// by agents/API; this panel is the operator's window into them.
export function PipelinesPage() {
  const { data: pipelines, isLoading, error } = useApi<Pipeline[]>(
    ['pipelines'],
    '/api/v1/pipelines',
  );
  // One cheap poll across all pipelines tells us which ones are running now,
  // so the list shows live activity without expanding each row.
  const { data: allRuns } = useApi<Run[]>(
    ['pipeline-runs-all'],
    '/api/v1/pipeline-runs',
    { refetchInterval: 3_000 },
  );
  const activeIds = new Set(
    (allRuns ?? [])
      .filter((r) => r.status === 'running' || r.status === 'pending')
      .map((r) => r.pipeline_id),
  );
  const [selected, setSelected] = useState<Pipeline | null>(null);
  const [runOpen, setRunOpen] = useState<Pipeline | null>(null);

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Pipelines</h1>
          <p className="text-sm text-muted-foreground">
            Saved, ordered sequences of pack steps. Built-in starters ship ready
            to run; agents create more via the <code className="rounded bg-muted px-1.5 py-0.5">helmdeck__pipeline-*</code> MCP tools.
          </p>
        </div>
        <div className="flex items-center gap-2">
          {activeIds.size > 0 && (
            <Badge variant="warning">
              <span className="mr-1 inline-block h-2 w-2 animate-pulse rounded-full bg-current" />
              {activeIds.size} running
            </Badge>
          )}
          <Badge variant="outline">
            <Workflow className="mr-1 h-3 w-3" />
            {pipelines?.length ?? 0} pipelines
          </Badge>
        </div>
      </div>

      {error && (
        <Card>
          <CardHeader>
            <CardTitle className="text-destructive">Failed to load pipelines</CardTitle>
            <CardDescription>{error.message}</CardDescription>
          </CardHeader>
          <CardContent className="text-sm text-muted-foreground">
            The pipeline engine may be disabled (the control plane has no
            database or pack engine wired).
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
                <TableHead>Name</TableHead>
                <TableHead>Steps</TableHead>
                <TableHead>Kind</TableHead>
                <TableHead>Updated</TableHead>
                <TableHead className="text-right">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {!pipelines || pipelines.length === 0 ? (
                <TableEmpty colSpan={5}>
                  <Workflow className="mx-auto mb-2 h-8 w-8 text-muted-foreground/50" />
                  No pipelines yet. Ask an agent to create one, or POST to{' '}
                  <code className="rounded bg-muted px-1 py-0.5">/api/v1/pipelines</code>.
                </TableEmpty>
              ) : (
                pipelines.map((p) => (
                  <TableRow
                    key={p.id}
                    className="cursor-pointer"
                    onClick={() => setSelected(p)}
                  >
                    <TableCell>
                      <div className="flex items-center gap-2 font-medium">
                        {p.name}
                        {activeIds.has(p.id) && (
                          <Badge variant="warning" className="gap-1">
                            <span className="inline-block h-1.5 w-1.5 animate-pulse rounded-full bg-current" />
                            running
                          </Badge>
                        )}
                      </div>
                      {p.description && (
                        <div className="text-xs text-muted-foreground">{p.description}</div>
                      )}
                    </TableCell>
                    <TableCell className="text-sm text-muted-foreground">
                      {p.steps?.map((s) => s.pack).join(' → ')}
                    </TableCell>
                    <TableCell>
                      <Badge variant={p.builtin ? 'secondary' : 'outline'}>
                        {p.builtin ? 'built-in' : 'custom'}
                      </Badge>
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {formatRelative(p.updated_at)}
                    </TableCell>
                    <TableCell className="text-right">
                      <div className="flex justify-end gap-2">
                        <CopyPromptButton pipeline={p} />
                        <Button
                          size="sm"
                          variant="outline"
                          onClick={(e) => {
                            e.stopPropagation();
                            setRunOpen(p);
                          }}
                        >
                          <Play className="mr-1 h-3 w-3" />
                          Run
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        )
      )}

      {selected && <RunHistory pipeline={selected} onClose={() => setSelected(null)} />}
      {runOpen && (
        <RunDialog pipeline={runOpen} onClose={() => setRunOpen(null)} onRan={() => setSelected(runOpen)} />
      )}
    </div>
  );
}

// RunHistory polls a pipeline's recent runs every 3s so an operator sees
// status advance live (pending → running → succeeded/failed).
function RunHistory({ pipeline, onClose }: { pipeline: Pipeline; onClose: () => void }) {
  const { data: runs, refetch } = useApi<Run[]>(
    ['pipeline-runs', pipeline.id],
    `/api/v1/pipelines/${pipeline.id}/runs`,
    { refetchInterval: 3_000 },
  );
  const authFetch = useAuthFetch();
  const [rerunning, setRerunning] = useState<string | null>(null);

  async function rerun(runID: string) {
    setRerunning(runID);
    try {
      await authFetch(`/api/v1/pipelines/${pipeline.id}/runs/${runID}/rerun`, {
        method: 'POST',
      });
      await refetch();
    } finally {
      setRerunning(null);
    }
  }

  return (
    <Card>
      <CardHeader className="flex flex-row items-start justify-between">
        <div>
          <CardTitle className="text-base">Runs — {pipeline.name}</CardTitle>
          <CardDescription>Polled every 3 seconds.</CardDescription>
        </div>
        <Button size="sm" variant="ghost" onClick={onClose}>
          Close
        </Button>
      </CardHeader>
      <CardContent>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Run</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Steps</TableHead>
              <TableHead>Started</TableHead>
              <TableHead>Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {!runs || runs.length === 0 ? (
              <TableEmpty colSpan={5}>No runs yet.</TableEmpty>
            ) : (
              runs.map((r) => (
                <TableRow key={r.id}>
                  <TableCell className="font-mono text-xs">{r.id}</TableCell>
                  <TableCell>
                    <div className="flex items-center gap-1">
                      <RunStatusBadge status={r.status} />
                      {r.status === 'failed' && r.failure_class && (
                        <FailureClassBadge cls={r.failure_class} />
                      )}
                    </div>
                    {r.status === 'failed' && (r.failure_reason || r.error) && (
                      <div className="mt-1 max-w-md text-xs text-muted-foreground">
                        {stripURL(r.failure_reason) || r.error}
                        {issueURL(r) && (
                          <a
                            href={issueURL(r)}
                            target="_blank"
                            rel="noreferrer"
                            className="ml-1 inline-flex items-center gap-0.5 text-destructive underline"
                          >
                            <Bug className="h-3 w-3" /> Report bug
                          </a>
                        )}
                      </div>
                    )}
                  </TableCell>
                  <TableCell className="text-xs">
                    {r.steps?.map((s) => (
                      <span key={s.step_id} className="mr-1 inline-flex">
                        <RunStatusBadge status={s.status} label={s.step_id} />
                      </span>
                    ))}
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">
                    {formatRelative(r.started_at)}
                  </TableCell>
                  <TableCell>
                    <Button
                      size="sm"
                      variant="ghost"
                      title="Re-run with the same inputs"
                      disabled={rerunning === r.id}
                      onClick={() => rerun(r.id)}
                    >
                      <RotateCw className="mr-1 h-3.5 w-3.5" />
                      Re-run
                    </Button>
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
  );
}

// RunDialog collects pipeline inputs as JSON and fires POST /{id}/run.
function RunDialog({
  pipeline,
  onClose,
  onRan,
}: {
  pipeline: Pipeline;
  onClose: () => void;
  onRan: () => void;
}) {
  const authFetch = useAuthFetch();
  const qc = useQueryClient();
  const [inputs, setInputs] = useState('{\n  \n}');
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit() {
    setErr(null);
    let parsed: unknown = {};
    if (inputs.trim()) {
      try {
        parsed = JSON.parse(inputs);
      } catch {
        setErr('Inputs must be valid JSON.');
        return;
      }
    }
    setBusy(true);
    try {
      const res = await authFetch(`/api/v1/pipelines/${pipeline.id}/run`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ inputs: parsed }),
      });
      if (!res.ok) {
        const body = (await res.json().catch(() => ({}))) as { message?: string };
        setErr(body.message ?? `Run failed (${res.status}).`);
        return;
      }
      qc.invalidateQueries({ queryKey: ['pipeline-runs', pipeline.id] });
      onRan();
      onClose();
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Run “{pipeline.name}”</DialogTitle>
          <DialogDescription>
            Provide values for the pipeline's <code>${'{{ inputs.* }}'}</code> references.
            Steps: {pipeline.steps?.map((s) => s.pack).join(' → ')}.
          </DialogDescription>
        </DialogHeader>
        <Textarea
          value={inputs}
          onChange={(e) => setInputs(e.target.value)}
          rows={8}
          className="font-mono text-xs"
          spellCheck={false}
        />
        {err && <p className="text-sm text-destructive">{err}</p>}
        <div className="flex justify-end gap-2">
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button onClick={submit} disabled={busy}>
            <Play className="mr-1 h-3 w-3" />
            {busy ? 'Starting…' : 'Run'}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function RunStatusBadge({ status, label }: { status: string; label?: string }) {
  const v =
    status === 'succeeded'
      ? 'success'
      : status === 'running' || status === 'pending'
        ? 'warning'
        : status === 'failed'
          ? 'destructive'
          : 'outline';
  return (
    <Badge variant={v}>
      <GitBranch className="mr-1 h-3 w-3" />
      {label ?? status}
    </Badge>
  );
}

// FailureClassBadge tags WHY a run failed: pack_bug / state_changed read
// as "not your input" (destructive), caller_fixable as "fix and retry"
// (warning), transient as "retry may work" (outline).
function FailureClassBadge({ cls }: { cls: string }) {
  const label = cls.replace(/_/g, ' ');
  const v =
    cls === 'pack_bug' || cls === 'state_changed'
      ? 'destructive'
      : cls === 'caller_fixable'
        ? 'warning'
        : 'outline';
  return <Badge variant={v}>{label}</Badge>;
}

// issueURL pulls the prefilled GitHub-issue link the runner embeds in a
// pack_bug failure_reason (only pack bugs carry one).
function issueURL(r: Run): string {
  const m = r.failure_reason?.match(/https:\/\/github\.com\/\S+\/issues\/new\?\S+/);
  return m ? m[0] : '';
}

// stripURL drops the embedded issue URL from the reason text so it isn't
// shown twice (the URL becomes the "Report bug" button instead).
function stripURL(reason?: string): string {
  if (!reason) return '';
  return reason.replace(/https:\/\/github\.com\/\S+/, '').replace(/:\s*$/, '').trim();
}

// pipelineInputVars scans every step's input for ${{ inputs.X }} references
// and returns the unique variable names in first-seen order — exactly the
// inputs a caller must supply. Generated from the live definition, so the
// copy-prompt can't drift from the pipeline.
function pipelineInputVars(p: Pipeline): string[] {
  const re = /\$\{\{\s*inputs\.([a-zA-Z_][\w]*)\s*\}\}/g;
  const seen = new Set<string>();
  const out: string[] = [];
  for (const s of p.steps ?? []) {
    if (s.input === undefined) continue;
    const text = typeof s.input === 'string' ? s.input : JSON.stringify(s.input);
    for (const m of text.matchAll(re)) {
      if (!seen.has(m[1])) {
        seen.add(m[1]);
        out.push(m[1]);
      }
    }
  }
  return out;
}

// buildPipelinePrompt produces a copy-paste prompt that asks an agent (e.g. in
// the OpenClaw chat UI) to run this pipeline, with a fill-in line per input.
function buildPipelinePrompt(p: Pipeline): string {
  const header = `Use helmdeck__pipeline-run to run the ${p.id} pipeline`;
  const vars = pipelineInputVars(p);
  if (vars.length === 0) return `${header} (it takes no inputs). Then poll helmdeck__pipeline-run-status with the run_id.`;
  const lines = vars.map((v) => `${v} = {{${v.toUpperCase()}}}`).join('\n');
  return `${header} with inputs:\n${lines}\n\nThen poll helmdeck__pipeline-run-status with the run_id until it's terminal.`;
}

// CopyPromptButton copies an agent prompt for the pipeline to the clipboard.
function CopyPromptButton({ pipeline }: { pipeline: Pipeline }) {
  const [copied, setCopied] = useState(false);
  async function copy(e: MouseEvent) {
    e.stopPropagation();
    try {
      await navigator.clipboard.writeText(buildPipelinePrompt(pipeline));
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      /* clipboard unavailable (e.g. non-secure context) — no-op */
    }
  }
  return (
    <Button
      size="sm"
      variant="ghost"
      title="Copy an agent prompt to run this pipeline"
      onClick={copy}
    >
      {copied ? <Check className="mr-1 h-3 w-3" /> : <ClipboardCopy className="mr-1 h-3 w-3" />}
      {copied ? 'Copied' : 'Copy prompt'}
    </Button>
  );
}
