// Shared formatting helpers used by every panel.

// formatRelative converts an ISO timestamp into a "5m ago" / "2h ago"
// string. Centralised so every panel formats timestamps the same way.
export function formatRelative(iso: string | null | undefined): string {
  if (!iso) return '—';
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return '—';
  const now = Date.now();
  const seconds = Math.floor((now - then) / 1000);
  if (seconds < 5) return 'just now';
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  return `${days}d ago`;
}

// truncate cuts a string at maxLen and adds an ellipsis. For
// long IDs, fingerprints, etc. shown in tables.
export function truncate(s: string, maxLen: number): string {
  if (s.length <= maxLen) return s;
  return s.slice(0, maxLen) + '…';
}
