import {useEffect, useRef, useState, type FormEvent} from 'react';
import {useRouteLoaderData, useSearchParams} from 'react-router';
import {Link, useNavigate} from '../lib/nav';
import {BirthMonthPicker} from '../components/AgeGate';
import {isAgeRestricted} from '../lib/ageGate';
import {ThemeToggle} from '../components/TopBar';
import {ApiError} from '../lib/api';
import {
  currentTermsVersion,
  exchangeOAuthCode,
  forgotPassword,
  getAuthProviders,
  loginWith,
  registerWith,
  startOAuth,
  type AdultInput,
  type OAuthProvider,
  type SignInResult,
} from '../lib/account';
import type {RootData} from '../root';
import {useAuth} from '../lib/auth';
import {t, useT} from '../lib/i18n';
import type {MessageKey} from '../lib/messages.zh-CN';
import '../styles/account.css';

export const meta = () => [{title: t('title.login')}];

type Mode = 'login' | 'register' | 'forgot';

/** Only same-site paths are accepted as a post-login destination. */
function safeNext(value: string | null): string {
  return value && value.startsWith('/') && !value.startsWith('//') && !value.startsWith('/login') ? value : '/';
}

// Account artboard. Email + password (POST /api/auth/login, /register) with
// 记住我 (30-day session vs this browser session only), 忘记密码 (emailed
// single-use link, routes/reset-password.tsx), terms acceptance on
// registration (terms_acceptances) and Google / Apple sign-in when the server
// has them configured (GET /api/auth/providers; buttons hidden otherwise).
// The OAuth callback lands back here with #oauth_code=… (exchanged once for a
// session) or ?oauth_error=….
//
// 18+ only (PRD 15, DESIGN_ALIGNMENT #8): registration needs the 「已年满 18
// 周岁」 checkbox, plus birth year and month when the site-entry gate cookie
// is not already ok (it normally is: this page sits behind the gate). The
// API checks it (internal/api/agegate.go) and stores users.adult_confirmed_at;
// an under-18 birth date answers 403 and sets the 24-hour denied cookie, and
// the page reloads into the restricted page. Google / Apple sign-up passes
// the same confirmation from the 注册 tab; from the 登录 tab only existing
// accounts can sign in (a new one comes back as age_required). A new account
// goes to /welcome (Onboarding) before `next`.

const OAUTH_ERRORS: Record<string, MessageKey> = {
  email_taken: 'login.oauthEmailTaken',
  no_email: 'login.oauthNoEmail',
  cancelled: 'login.oauthCancelled',
  expired: 'login.oauthExpired',
  age_required: 'login.oauthAgeRequired',
};

export default function LoginPage() {
  const t = useT();
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const {signIn} = useAuth();
  const [mode, setMode] = useState<Mode>(params.get('mode') === 'register' ? 'register' : 'login');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [birthYear, setBirthYear] = useState('');
  const [birthMonth, setBirthMonth] = useState('');
  const [remember, setRemember] = useState(true);
  const [agreed, setAgreed] = useState(false);
  const [adultOk, setAdultOk] = useState(false);
  // Birth year and month only when the gate cookie is not already ok.
  const needBirth = useRouteLoaderData<RootData>('root')?.gate !== 'ok';
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(() => {
    const code = params.get('oauth_error');
    return code ? t(OAUTH_ERRORS[code] ?? 'login.oauthFailed') : '';
  });
  const [notice, setNotice] = useState('');
  const [providers, setProviders] = useState<OAuthProvider[]>([]);
  const exchanged = useRef(false);
  const next = safeNext(params.get('next'));

  useEffect(() => {
    const controller = new AbortController();
    getAuthProviders(controller.signal).then(setProviders, () => {});
    return () => controller.abort();
  }, []);

  const finish = (result: SignInResult, rememberThis: boolean, created: boolean) => {
    signIn(result, rememberThis);
    if (result.deletion_cancelled) {
      setNotice(t('login.deletionCancelled'));
      setBusy(false);
      return;
    }
    if (created) navigate(`/welcome?next=${encodeURIComponent(next)}`, {replace: true});
    else navigate(next, {replace: true});
  };

  // Back from Google / Apple: #oauth_code=… is exchanged once, then removed
  // from the address bar and history.
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
        setError(t('login.oauthExpired'));
        setBusy(false);
      },
    );
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const switchMode = (m: Mode) => {
    setMode(m);
    setError('');
    setNotice('');
  };

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setError('');
    setNotice('');
    if (mode === 'forgot') {
      setBusy(true);
      try {
        await forgotPassword(email.trim());
        // Same message whether or not the address has an account.
        setNotice(t('login.resetSent'));
      } catch (cause) {
        setError(cause instanceof ApiError && cause.status === 429 ? t('login.tooMany') : t('login.resetFailed'));
      } finally {
        setBusy(false);
      }
      return;
    }
    if (mode === 'register') {
      if (!adultOk) {
        setError(t('age.adultRequired'));
        return;
      }
      if (needBirth && (!birthYear || !birthMonth)) {
        setError(t('login.pickBirth'));
        return;
      }
      if (!agreed) {
        setError(t('login.mustAgree'));
        return;
      }
    }
    if (password.length < 8) {
      setError(t('login.passwordShort'));
      return;
    }
    setBusy(true);
    try {
      const result =
        mode === 'login'
          ? await loginWith(email, password, remember)
          : await registerWith(email, password, displayName, remember, await currentTermsVersion(), adultInput());
      finish(result, remember, mode === 'register');
    } catch (cause) {
      if (mode === 'register' && isAgeRestricted(cause)) {
        window.location.reload();
        return;
      }
      if (cause instanceof ApiError && cause.status === 0) setError(cause.message);
      else if (cause instanceof ApiError && cause.status === 429) setError(t('login.tooMany'));
      else if (mode === 'login') setError(t('login.loginFailed'));
      else setError(cause instanceof ApiError && cause.status === 409 ? t('login.emailTaken') : t('login.registerFailed'));
      setBusy(false);
    }
  };

  const adultInput = (): AdultInput =>
    needBirth && birthYear && birthMonth
      ? {adult_confirmed: adultOk, birth_year: Number(birthYear), birth_month: Number(birthMonth)}
      : {adult_confirmed: adultOk};

  const sso = async (provider: OAuthProvider) => {
    setError('');
    // Signing up with a provider needs the same 18+ confirmation first.
    if (mode === 'register') {
      if (!adultOk) {
        setError(t('age.adultRequired'));
        return;
      }
      if (needBirth && (!birthYear || !birthMonth)) {
        setError(t('login.pickBirth'));
        return;
      }
    }
    setBusy(true);
    try {
      await startOAuth(provider, {next, remember, adult: mode === 'register' ? adultInput() : undefined});
    } catch (cause) {
      if (isAgeRestricted(cause)) {
        window.location.reload();
        return;
      }
      setError(t('login.oauthFailed'));
      setBusy(false);
    }
  };

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
          {mode === 'forgot' ? (
            <>
              <h1 className="auth-title">{t('login.forgotTitle')}</h1>
              <p className="auth-sub">{t('login.forgotSub')}</p>
            </>
          ) : (
            <div className="segmented segmented-lg" role="tablist" aria-label={t('login.modeTabs')}>
              <button type="button" role="tab" aria-selected={mode === 'login'} className="segmented-item" onClick={() => switchMode('login')}>
                {t('auth.login')}
              </button>
              <button type="button" role="tab" aria-selected={mode === 'register'} className="segmented-item" onClick={() => switchMode('register')}>
                {t('login.register')}
              </button>
            </div>
          )}

          <form className="auth-form" onSubmit={submit} noValidate={false}>
            {mode === 'register' && (
              <label className="field">
                <span>{t('login.nickname')}</span>
                <input value={displayName} onChange={e => setDisplayName(e.target.value)} placeholder={t('login.nicknamePlaceholder')} autoComplete="nickname" maxLength={40} />
              </label>
            )}
            <label className="field">
              <span>{t('login.email')}</span>
              <input type="email" value={email} onChange={e => setEmail(e.target.value)} placeholder="name@example.com" autoComplete="email" required />
            </label>
            {mode === 'register' && needBirth && (
              <div className="field">
                <BirthMonthPicker year={birthYear} month={birthMonth} onYear={setBirthYear} onMonth={setBirthMonth} idPrefix="reg-birth" />
                <span className="field-hint">{t('login.birthHint')}</span>
              </div>
            )}
            {mode !== 'forgot' && (
              <label className="field">
                <span>{t('login.password')}</span>
                <input
                  type="password"
                  value={password}
                  onChange={e => setPassword(e.target.value)}
                  placeholder={t('login.passwordPlaceholder')}
                  autoComplete={mode === 'login' ? 'current-password' : 'new-password'}
                  minLength={8}
                  required
                />
              </label>
            )}
            {mode !== 'forgot' && (
              <div className="auth-row">
                <label className="auth-check">
                  <input type="checkbox" checked={remember} onChange={e => setRemember(e.target.checked)} />
                  <span>{t('login.remember')}</span>
                </label>
                {mode === 'login' && (
                  <button type="button" className="text-btn" onClick={() => switchMode('forgot')}>
                    {t('login.forgot')}
                  </button>
                )}
              </div>
            )}
            {mode === 'register' && (
              <label className="auth-check">
                <input type="checkbox" checked={adultOk} onChange={e => setAdultOk(e.target.checked)} required />
                <span>{t('age.adultConfirm')}</span>
              </label>
            )}
            {mode === 'register' && (
              <label className="auth-check">
                <input type="checkbox" checked={agreed} onChange={e => setAgreed(e.target.checked)} required />
                <span>
                  {t('login.agreePrefix')}
                  <Link to="/legal/terms" target="_blank">
                    {t('login.agreeTerms')}
                  </Link>
                  {t('login.agreeAnd')}
                  <Link to="/legal/privacy" target="_blank">
                    {t('login.agreePrivacy')}
                  </Link>
                </span>
              </label>
            )}
            {error && (
              <p className="form-error" role="alert">
                {error}
              </p>
            )}
            {notice && (
              <p className="form-ok" role="status">
                {notice}
              </p>
            )}
            {notice && mode !== 'forgot' ? (
              <button type="button" className="btn btn-primary btn-block btn-lg" onClick={() => navigate(next, {replace: true})}>
                {t('login.continue')}
              </button>
            ) : (
              <button type="submit" className="btn btn-primary btn-block btn-lg" disabled={busy}>
                {busy ? t('login.wait') : mode === 'login' ? t('auth.login') : mode === 'register' ? t('login.createAccount') : t('login.sendReset')}
              </button>
            )}
            {mode === 'forgot' && (
              <button type="button" className="text-btn" onClick={() => switchMode('login')}>
                {t('login.backToLogin')}
              </button>
            )}
          </form>

          {mode !== 'forgot' && providers.length > 0 && (
            <>
              <div className="divider-text">
                <span>{t('login.or')}</span>
              </div>
              {providers.includes('google') && (
                <button type="button" className="btn btn-secondary btn-block btn-lg sso-btn" disabled={busy} onClick={() => void sso('google')}>
                  {t('login.google')}
                </button>
              )}
              {providers.includes('apple') && (
                <button type="button" className="btn btn-secondary btn-block btn-lg sso-btn" disabled={busy} onClick={() => void sso('apple')}>
                  {t('login.apple')}
                </button>
              )}
              {mode === 'register' && <p className="field-hint">{t('login.ssoTerms')}</p>}
            </>
          )}
          <Link to={next} className="auth-skip">
            {t('login.skip')}
          </Link>
        </div>
      </section>
    </div>
  );
}
