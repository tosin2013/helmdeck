import { type HTMLAttributes } from 'react';
import { cn } from '@/lib/utils';

// Skeleton is the loading-state placeholder used by every panel
// while TanStack Query is fetching. The animation is a slow pulse
// (1500ms) which feels less hectic than the default Tailwind
// pulse animation.
export function Skeleton({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn('animate-pulse rounded-md bg-muted/50', className)}
      style={{ animationDuration: '1500ms' }}
      {...props}
    />
  );
}
