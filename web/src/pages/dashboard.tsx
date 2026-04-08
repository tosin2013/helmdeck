import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card';
import { useAuth } from '@/lib/auth';

export function DashboardPage() {
  const { subject } = useAuth();
  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Dashboard</h1>
        <p className="text-sm text-muted-foreground">
          Welcome, {subject}. The full metric panel lands with T602.
        </p>
      </div>

      <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
        <Card>
          <CardHeader>
            <CardTitle>Phase 6 status</CardTitle>
            <CardDescription>Management UI shell</CardDescription>
          </CardHeader>
          <CardContent>
            <div className="text-sm text-muted-foreground">
              T601 — shell + login + go:embed wiring is now complete. The
              remaining panels (T602–T612) ship one at a time as separate
              commits. Each link in the sidebar maps to a stub that the
              corresponding task fills in.
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Quick links</CardTitle>
            <CardDescription>Operator entry points</CardDescription>
          </CardHeader>
          <CardContent className="space-y-2 text-sm">
            <div>
              <span className="text-muted-foreground">Sessions:</span>{' '}
              <a href="/sessions" className="text-primary hover:underline">
                Browser sessions panel (T603)
              </a>
            </div>
            <div>
              <span className="text-muted-foreground">Vault:</span>{' '}
              <a href="/vault" className="text-primary hover:underline">
                Credential vault (T610)
              </a>
            </div>
            <div>
              <span className="text-muted-foreground">Packs:</span>{' '}
              <a href="/packs" className="text-primary hover:underline">
                Capability packs (T606 / T607)
              </a>
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Resources</CardTitle>
            <CardDescription>Documentation & repos</CardDescription>
          </CardHeader>
          <CardContent className="space-y-2 text-sm">
            <div>
              <a
                href="https://github.com/tosin2013/helmdeck"
                className="text-primary hover:underline"
                target="_blank"
                rel="noreferrer"
              >
                GitHub repository
              </a>
            </div>
            <div>
              <a
                href="https://github.com/tosin2013/helmdeck/blob/main/docs/SECURITY-HARDENING.md"
                className="text-primary hover:underline"
                target="_blank"
                rel="noreferrer"
              >
                Security hardening guide
              </a>
            </div>
            <div>
              <a
                href="https://github.com/tosin2013/helmdeck/blob/main/CONTRIBUTING.md"
                className="text-primary hover:underline"
                target="_blank"
                rel="noreferrer"
              >
                Contribution guide
              </a>
            </div>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
