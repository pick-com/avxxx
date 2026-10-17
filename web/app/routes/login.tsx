import {useEffect, useRef, useState} from 'react';
import {useSearchParams} from 'react-router';
import {Link, useNavigate} from '../lib/nav';
import {AuthForm, type AuthMode} from '../components/AuthForm';
import {ThemeToggle} from '../components/TopBar';
import {exchangeOAuthCode, type SignInResult} from '../lib/account';
import {useAuth} from '../lib/auth';
import {t, useT} from '../lib/i18n';
import {clearPending, takePromptOAuth} from '../lib/pendingAction';
import type {MessageKey} from '../lib/messages.zh-CN';
import '../styles/account.css';

export const meta = () => [{title: t('title.login')}];

/** Only same-site paths are accepted as a post-login destination. */
function safeNext(value: string | null): string {
  return value && value.startsWith('/') && !value.startsWith('//') && !value.startsWith('/login') ? value : '/';
}

// Account artboard: the shared AuthForm (components/AuthForm.tsx: email +
// password, 记住我, 忘记密码, terms and 18+ confirmation on registration,
// Google / Apple when configured) beside the brand aside. ?mode=register
// and ?mode=forgot open those tabs. The OAuth callback lands back here with
// #oauth_code=… (exchanged once for a session) or ?oauth_error=…, including
// a provider sign-in started from the in-page sign-in sheet (AuthPrompt). A
// new account goes to /welcome (Onboarding) before `next`.

const OAUTH_ERRORS: Record<string, MessageKey> = {
  email_taken: 'login.oauthEmailTaken',
  no_email: 'login.oauthNoEmail',
  cancelled: 'login.oauthCancelled',
  expired: 'login.oauthExpired',
  age_required: 'login.oauthAgeRequired',
};

const initialMode = (value: string | null): AuthMode => (value === 'register' || value === 'forgot' ? value : 'login');

export default function LoginPage() {
  const t = useT();
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const {signIn} = useAuth();
  const [mode, setMode] = useState<AuthMode>(() => initialMode(params.get('mode')));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(() => {
    const code = params.get('oauth_error');
    return code ? t(OAUTH_ERRORS[code] ?? 'login.oauthFailed') : '';
  });
  const [notice, setNotice] = useState('');
  const exchanged = useRef(false);
  const next = safeNext(params.get('next'));

  const finish = (result: SignInResult, rememberThis: boolean, created: boolean) => {
    signIn(result, rememberThis);
    if (result.deletion_cancelled) {
      // AuthForm shows the notice and 继续 for its own sign-ins; this covers the OAuth exchange.
      setNotice(t('login.deletionCancelled'));
      setBusy(false);
      return;
    }
    if (created) navigate(`/welcome?next=${encodeURIComponent(next)}`, {replace: true});
    else navigate(next, {replace: true});
  };

  // Back from Google / Apple: #oauth_code=… is exchanged once, then removed
  // from the address bar and history.
  // A failed provider sign-in drops the action the sign-in sheet left waiting.
  useEffect(() => {
    if (!params.get('oauth_error')) return;
    clearPending();
    takePromptOAuth();
  }, [params]);

  useEffect(() => {
    const hash = new URLSearchParams(window.location.hash.slice(1));
    const code = hash.get('oauth_code');
    if (!code || exchanged.current) return;
    exchanged.current = true;
    window.history.replaceState(null, '', window.location.pathname + window.location.search);
    setBusy(true);
    exchangeOAuthCode(code).then(
      result => finish(result, result.remember !== false, !!result.created),
      () => {
        clearPending();
        takePromptOAuth();
        setError(t('login.oauthExpired'));
        setBusy(false);
      },
    );
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <div className="auth-page">
      <section className="auth-aside" aria-hidden="true">
        <Link to="/" className="logo logo-lg" tabIndex={-1}>
          REEL<span className="logo-dot">.</span>
        </Link>
        <div className="auth-tiles">
          {Array.from({length: 9}, (_, i) => (
            <span key={i} className={`thumb ph-${i}`} />
          ))}
        </div>
        <div className="auth-pitch">
          <strong>{t('login.pitchTitle')}</strong>
          <span>{t('login.pitchBody')}</span>
        </div>
      </section>

      <section className="auth-main">
        <div className="auth-top">
          <Link to="/" className="logo show-mobile-flex">
            REEL<span className="logo-dot">.</span>
          </Link>
          <span className="section-spacer" />
          <ThemeToggle />
        </div>
        <div className="auth-card">
          <AuthForm
            mode={mode}
            onMode={m => {
              setMode(m);
              setError('');
            }}
            next={next}
            busy={busy}
            error={error}
            notice={notice}
            idPrefix="reg"
            onSignedIn={(result, remember, method) => finish(result, remember, method === 'register')}
            onContinue={() => navigate(next, {replace: true})}
            footer={
              <Link to={next} className="auth-skip">
                {t('login.skip')}
              </Link>
            }
          />
        </div>
      </section>
    </div>
  );
}
