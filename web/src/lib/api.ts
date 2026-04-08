// Typed fetch wrapper for the helmdeck control plane.
//
// Every panel calls api(...) instead of raw fetch so the auth header,
// 401 handling, and error shape are uniform across the codebase.
// On 401, we clear the stored token and let the auth context redirect
// to /login on the next render.

export class APIError extends Error {
  constructor(
    message: string,
    public status: number,
    public code?: string,
    public body?: unknown,
  ) {
    super(message);
    this.name = 'APIError';
  }
}

// onUnauthorized is called when the API returns 401. The auth context
// installs a handler that clears the stored token and triggers a
// re-render so the router redirects to /login.
let onUnauthorized: (() => void) | null = null;
export function setUnauthorizedHandler(fn: (() => void) | null) {
  onUnauthorized = fn;
}

interface ApiOptions extends RequestInit {
  // When true, parse the response body as JSON. Default true.
  // Set false for endpoints that return non-JSON (PNG, plain text).
  json?: boolean;
}

export async function api<T = unknown>(
  path: string,
  token: string | null,
  opts: ApiOptions = {},
): Promise<T> {
  const { json = true, headers, ...rest } = opts;
  const finalHeaders: Record<string, string> = {
    Accept: 'application/json',
    ...(headers as Record<string, string> | undefined),
  };
  if (token) {
    finalHeaders.Authorization = `Bearer ${token}`;
  }
  if (rest.body && !finalHeaders['Content-Type']) {
    finalHeaders['Content-Type'] = 'application/json';
  }

  const res = await fetch(path, { ...rest, headers: finalHeaders });

  if (res.status === 401) {
    onUnauthorized?.();
    throw new APIError('unauthorized', 401);
  }

  if (!res.ok) {
    let body: unknown;
    let code: string | undefined;
    let msg = `request failed: ${res.status}`;
    try {
      body = await res.json();
      if (typeof body === 'object' && body !== null) {
        const b = body as { error?: string; message?: string };
        if (b.error) code = b.error;
        if (b.message) msg = b.message;
      }
    } catch {
      // body wasn't JSON; fall through with the default message
    }
    throw new APIError(msg, res.status, code, body);
  }

  if (!json) {
    return undefined as T;
  }
  return (await res.json()) as T;
}
