import { NavLink } from 'react-router-dom';
import {
  Activity,
  Boxes,
  FileText,
  KeyRound,
  LayoutDashboard,
  LinkIcon,
  Network,
  Package,
  Server,
  Shield,
} from 'lucide-react';

import { cn } from '@/lib/utils';

// Sidebar lives at the left edge on desktop. Each entry maps to one
// route in App.tsx. The icons come from lucide-react which ships
// tree-shakable individual SVGs — bundling cost is one icon per
// entry, not the entire library.
const navItems = [
  { to: '/', label: 'Dashboard', icon: LayoutDashboard, end: true },
  { to: '/sessions', label: 'Sessions', icon: Server },
  { to: '/packs', label: 'Capability Packs', icon: Package },
  { to: '/mcp', label: 'MCP Registry', icon: Boxes },
  { to: '/providers', label: 'AI Providers', icon: Activity },
  { to: '/vault', label: 'Credential Vault', icon: KeyRound },
  { to: '/security', label: 'Security Policies', icon: Shield },
  { to: '/audit', label: 'Audit Log', icon: FileText },
  { to: '/connect', label: 'Connect Clients', icon: LinkIcon },
];

export function Sidebar() {
  return (
    <aside className="hidden w-64 flex-col border-r bg-card md:flex">
      <div className="flex h-14 items-center gap-2 border-b px-4">
        <Network className="h-5 w-5 text-primary" />
        <span className="font-semibold tracking-tight">helmdeck</span>
      </div>
      <nav className="flex-1 space-y-1 p-2">
        {navItems.map((item) => (
          <NavLink
            key={item.to}
            to={item.to}
            end={item.end}
            className={({ isActive }) =>
              cn(
                'flex items-center gap-3 rounded-md px-3 py-2 text-sm transition-colors',
                isActive
                  ? 'bg-secondary text-secondary-foreground'
                  : 'text-muted-foreground hover:bg-accent hover:text-accent-foreground',
              )
            }
          >
            <item.icon className="h-4 w-4 shrink-0" />
            {item.label}
          </NavLink>
        ))}
      </nav>
      <div className="border-t p-3 text-xs text-muted-foreground">
        v0.6.0 · Phase 6
      </div>
    </aside>
  );
}
