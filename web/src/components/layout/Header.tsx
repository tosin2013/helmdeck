import { LogOut, User } from 'lucide-react';

import { Button } from '@/components/ui/button';
import { useAuth } from '@/lib/auth';

export function Header() {
  const { subject, logout } = useAuth();
  return (
    <header className="flex h-14 items-center justify-between border-b bg-card px-6">
      <div className="text-sm text-muted-foreground">
        Helmdeck control plane
      </div>
      <div className="flex items-center gap-3">
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <User className="h-4 w-4" />
          {subject ?? 'unknown'}
        </div>
        <Button variant="ghost" size="sm" onClick={logout}>
          <LogOut className="h-4 w-4" />
          Sign out
        </Button>
      </div>
    </header>
  );
}
