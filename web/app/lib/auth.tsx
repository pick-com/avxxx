import {createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode} from 'react';
import {tokens, type AuthResult} from './api';
import {
  acceptTerms,
  clearSessionKind,
  currentTermsVersion,
  ephemeralTokenExpired,
  getAccount,
  logoutServer,
  markSessionKind,
  type Me,
} from './account';
import {Link} from './nav';
import {useT} from './i18n';

type AuthStatus = 'unknown' | 'anon' | 'user';

type AuthValue = {
  status: AuthStatus;
  user: Me | null;
  /** remember=false keeps the token for this browser session only (记住我 off). */
  signIn: (result: AuthResult, remember?: boolean) => void;
  /** Revokes the session on the server (POST /api/auth/logout) and clears it here. */
  signOut: () => void;
  /** Reloads GET /api/auth/me (after a profile or terms change). */
  refresh: () => void;
};

const AuthContext = createContext<AuthValue>({status: 'unknown', user: null, signIn: () => {}, signOut: () => {}, refresh: () => {}});

export function AuthProvider({children}: {children: ReactNode}) {
  const [status, setStatus] = useState<AuthStatus>('unknown');
  const [user, setUser] = useState<Me | null>(null);
  const [nonce, setNonce] = useState(0);

  useEffect(() => {
    // A browser-session sign-in (记住我 off) ends when the browser closes.
    if (tokens.user() && ephemeralTokenExpired()) {
      void logoutServer(tokens.user());
      tokens.setUser(null);
      clearSessionKind();
    }
    if (!tokens.user()) {
      setStatus('anon');
      return;
    }
    const controller = new AbortController();
    getAccount(controller.signal).then(
      me => {
        setUser(me);
        setStatus('user');
      },
      () => {
        if (controller.signal.aborted) return;
        // An expired, revoked or unknown token: treated as signed out.
        tokens.setUser(null);
        setUser(null);
        setStatus('anon');
      },
    );
    return () => controller.abort();
  }, [nonce]);

  const signIn = useCallback((result: AuthResult, remember = true) => {
    tokens.setUser(result.token);
    markSessionKind(remember);
    setUser(result.user);
    setStatus('user');
    setNonce(n => n + 1); // load the full profile (nickname, terms version)
  }, []);

  const signOut = useCallback(() => {
    void logoutServer(tokens.user());
    tokens.setUser(null);
    clearSessionKind();
    setUser(null);
    setStatus('anon');
  }, []);

  const refresh = useCallback(() => setNonce(n => n + 1), []);

  const value = useMemo(() => ({status, user, signIn, signOut, refresh}), [status, user, signIn, signOut, refresh]);
  return (
    <AuthContext.Provider value={value}>
      {children}
      {status === 'user' && user && <TermsPrompt user={user} onAccepted={refresh} onDecline={signOut} />}
    </AuthContext.Provider>
  );
}

export const useAuth = () => useContext(AuthContext);

/**
 * Re-prompt when the accepted terms version differs from the current one
 * (terms_acceptances). Not shown on backends without the field.
 */
function TermsPrompt({user, onAccepted, onDecline}: {user: Me; onAccepted: () => void; onDecline: () => void}) {
  const t = useT();
  const [current, setCurrent] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    let live = true;
    void currentTermsVersion().then(v => live && setCurrent(v));
    return () => {
      live = false;
    };
  }, []);

  if (user.terms_version === undefined || current === null || user.terms_version === current) return null;
  const accept = async () => {
    setBusy(true);
    setError('');
    try {
      await acceptTerms(current);
      onAccepted();
    } catch {
      setError(t('terms.failed'));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section className="consent-banner" role="dialog" aria-labelledby="terms-title" aria-live="polite">
      <div className="consent-text">
        <strong id="terms-title">{user.terms_version ? t('terms.updatedTitle') : t('terms.firstTitle')}</strong>
        <span>
          {t('terms.body')}
          <Link to="/legal/terms" className="meta-link">
            {t('terms.read')}
          </Link>
        </span>
        {error && (
          <span className="field-error" role="alert">
            {error}
          </span>
        )}
      </div>
      <div className="consent-actions">
        <button type="button" className="btn btn-secondary" onClick={onDecline} disabled={busy}>
          {t('terms.decline')}
        </button>
        <button type="button" className="btn btn-primary" onClick={accept} disabled={busy}>
          {t('terms.accept')}
        </button>
      </div>
    </section>
  );
}
