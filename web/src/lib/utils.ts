import { clsx, type ClassValue } from 'clsx';
import { twMerge } from 'tailwind-merge';

// cn is the standard shadcn class-merging helper. clsx handles the
// conditional logic, tailwind-merge dedupes conflicting Tailwind
// utilities so `cn("p-4", "p-2")` ends up as just "p-2" rather
// than the cascade-dependent answer.
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
