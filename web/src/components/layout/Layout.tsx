import { Outlet } from 'react-router-dom';

import { Sidebar } from './Sidebar';
import { Header } from './Header';

// Layout is the chrome wrapper around every authenticated page.
// Sidebar is fixed at 16rem on desktop; the main column scrolls
// independently. Header sits across the top of the main column.
export function Layout() {
  return (
    <div className="flex h-screen bg-background text-foreground">
      <Sidebar />
      <div className="flex flex-1 flex-col overflow-hidden">
        <Header />
        <main className="flex-1 overflow-y-auto p-6">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
