import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card';

// PanelStub is the placeholder shape every Phase 6 panel ships
// behind in T601. The corresponding T6xx task replaces the stub
// with the real panel content. The stub itself is intentionally
// pretty so operators evaluating the UI in v0.6.0-preview can see
// the navigation works even before the panels are filled in.

interface PanelStubProps {
  title: string;
  task: string;
  description: string;
}

export function PanelStub({ title, task, description }: PanelStubProps) {
  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
        <p className="text-sm text-muted-foreground">{description}</p>
      </div>
      <Card>
        <CardHeader>
          <CardTitle>Coming in {task}</CardTitle>
          <CardDescription>
            This panel is a placeholder shipped with the T601 shell. The
            full implementation lands in {task} as a separate commit.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">
            See <code className="rounded bg-muted px-1.5 py-0.5">docs/MILESTONES.md</code>{' '}
            for the panel-by-panel ship plan and{' '}
            <code className="rounded bg-muted px-1.5 py-0.5">docs/TASKS.md</code> for the
            acceptance criteria.
          </p>
        </CardContent>
      </Card>
    </div>
  );
}
