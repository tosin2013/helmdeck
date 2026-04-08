import { useState, type FormEvent } from 'react';
import { Network } from 'lucide-react';

import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card';
import { useAuth } from '@/lib/auth';
import { APIError } from '@/lib/api';

export function LoginPage() {
  const { login } = useAuth();
  const [username, setUsername] = useState('admin');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setPending(true);
    try {
      await login(username, password);
    } catch (err) {
      if (err instanceof APIError) {
        setError(err.status === 401 ? 'Invalid username or password' : err.message);
      } else if (err instanceof Error) {
        setError(err.message);
      } else {
        setError('Login failed');
      }
    } finally {
      setPending(false);
    }
  }

  return (
    <div className="grid min-h-screen place-items-center bg-background p-6">
      <Card className="w-full max-w-md">
        <CardHeader className="space-y-3">
          <div className="flex items-center gap-2">
            <Network className="h-6 w-6 text-primary" />
            <span className="text-lg font-semibold tracking-tight">helmdeck</span>
          </div>
          <div>
            <CardTitle>Sign in</CardTitle>
            <CardDescription>
              Use the admin credentials configured on the control plane.
            </CardDescription>
          </div>
        </CardHeader>
        <CardContent>
          <form onSubmit={onSubmit} className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="username">Username</Label>
              <Input
                id="username"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                autoComplete="username"
                required
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="password">Password</Label>
              <Input
                id="password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoComplete="current-password"
                required
              />
            </div>
            {error && (
              <div className="rounded-md border border-destructive/50 bg-destructive/10 px-3 py-2 text-sm text-destructive">
                {error}
              </div>
            )}
            <Button type="submit" className="w-full" disabled={pending}>
              {pending ? 'Signing in…' : 'Sign in'}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
