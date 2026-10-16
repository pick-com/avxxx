import {createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode} from 'react';
import {getMe, tokens, type AuthResult, type User} from './api';

type AuthStatus = 'unknown' | 'anon' | 'user';

type AuthValue = {
  status: AuthStatus;
  user: User | null;
  signIn: (result: AuthResult) => void;
  signOut: () => void;
};

const AuthContext = createContext<AuthValue>({status: 'unknown', user: null, signIn: () => {}, signOut: () => {}});

export function AuthProvider({children}: {children: ReactNode}) {
  const [status, setStatus] = useState<AuthStatus>('unknown');
  const [user, setUser] = useState<User | null>(null);

  useEffect(() => {
    if (!tokens.user()) {
      setStatus('anon');
      return;
    }
    const controller = new AbortController();
    getMe(controller.signal).then(
      me => {
        setUser(me);
        setStatus('user');
      },
      () => {
        if (controller.signal.aborted) return;
        // GET /api/auth/me answers 500 for an expired or unknown token (no 401
        // yet), so any failure is treated as signed out.
        tokens.setUser(null);
        setUser(null);
        setStatus('anon');
      },
    );
    return () => controller.abort();
  }, []);

  const signIn = useCallback((result: AuthResult) => {
    tokens.setUser(result.token);
    setUser(result.user);
    setStatus('user');
  }, []);

  const signOut = useCallback(() => {
    // TODO(tech plan 3.6): call a server-side logout once sessions move to cookies.
    tokens.setUser(null);
    setUser(null);
    setStatus('anon');
  }, []);

  const value = useMemo(() => ({status, user, signIn, signOut}), [status, user, signIn, signOut]);
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export const useAuth = () => useContext(AuthContext);
