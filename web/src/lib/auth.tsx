import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react';

import { api, setUnauthorizedHandler } from '@/lib/api';

const TOKEN_KEY = 'helmdeck.token';
const SUBJECT_KEY = 'helmdeck.subject';

interface AuthContextValue {
  token: string | null;
  subject: string | null;
  login: (username: string, password: string) => Promise<void>;
  logout: () => void;
}

const AuthContext = createContext<AuthContextValue | null>(null);

interface LoginResponse {
  token: string;
  subject: string;
  expires_at: string;
}

// AuthProvider owns the JWT and the "who am I" subject. It hydrates
// from localStorage on first mount and clears both on 401 from any
// API call (the api wrapper installs a handler via setUnauthorizedHandler).
export function AuthProvider({ children }: { children: ReactNode }) {
  const [token, setToken] = useState<string | null>(() =>
    typeof window === 'undefined' ? null : localStorage.getItem(TOKEN_KEY),
  );
  const [subject, setSubject] = useState<string | null>(() =>
    typeof window === 'undefined' ? null : localStorage.getItem(SUBJECT_KEY),
  );

  const logout = useCallback(() => {
    setToken(null);
    setSubject(null);
    localStorage.removeItem(TOKEN_KEY);
    localStorage.removeItem(SUBJECT_KEY);
  }, []);

  const login = useCallback(
    async (username: string, password: string) => {
      const resp = await api<LoginResponse>('/api/v1/auth/login', null, {
        method: 'POST',
        body: JSON.stringify({ username, password }),
      });
      setToken(resp.token);
      setSubject(resp.subject);
      localStorage.setItem(TOKEN_KEY, resp.token);
      localStorage.setItem(SUBJECT_KEY, resp.subject);
    },
    [],
  );

  // Wire the api wrapper's 401 hook to our logout. The wrapper
  // calls onUnauthorized() when any request returns 401, which
  // clears the token here and triggers the router redirect.
  useEffect(() => {
    setUnauthorizedHandler(() => logout());
    return () => setUnauthorizedHandler(null);
  }, [logout]);

  const value = useMemo<AuthContextValue>(
    () => ({ token, subject, login, logout }),
    [token, subject, login, logout],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth() {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error('useAuth must be used inside AuthProvider');
  return ctx;
}
