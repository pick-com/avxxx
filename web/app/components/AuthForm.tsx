import {useEffect, useState, type FormEvent, type ReactNode} from 'react';
import {useRouteLoaderData} from 'react-router';
import {BirthMonthPicker} from './AgeGate';
import {isAgeRestricted} from '../lib/ageGate';
import {ApiError} from '../lib/api';
import {
  currentTermsVersion,
  forgotPassword,
  getAuthProviders,
  loginWith,
  registerWith,
  startOAuth,
  type AdultInput,
  type OAuthProvider,
  type SignInResult,
} from '../lib/account';
import {Link} from '../lib/nav';
import {useT} from '../lib/i18n';
import type {RootData} from '../root';
import '../styles/account.css';

export type AuthMode = 'login' | 'register' | 'forgot';
/** How a sign-in finished: password login, a new account, or a provider redirect. */
export type AuthMethod = 'password' | 'register' | OAuthProvider;

// The email / password / Google form shared by the /login page and the
// in-page sign-in sheet (AuthPrompt). Email + password (POST /api/auth/login,
// /register) with 记住我 (30-day session vs this browser session only),
// 忘记密码 (emailed single-use link), terms acceptance on registration
// (terms_acceptances) and Google / Apple sign-in when the server has them
// configured (GET /api/auth/providers; buttons hidden otherwise).
//
// 18+ only (PRD 15, DESIGN_ALIGNMENT #8): registration needs the 「已年满 18
// 周岁」 checkbox, plus birth year and month when the site-entry gate cookie
// is not already ok. The API checks it (internal/api/agegate.go); an under-18
// birth date answers 403 and sets the 24-hour denied cookie, and the page
// reloads into the restricted page. Google / Apple sign-up passes the same
// confirmation from the 注册 tab; from the 登录 tab only existing accounts
// can sign in (a new one comes back as age_required).
//
// With `forgotHref` (the sheet) 忘记密码 is a link to that page instead of an
// in-form mode. A provider sign-in is a full-page redirect; `onOAuthStart`
// runs just before it (the sheet stores the pending action there) and the
// callback lands on /login, which exchanges the code.

export function AuthForm({
  mode,
  onMode,
  next,
  onSignedIn,
  onOAuthStart,
  onContinue,
  forgotHref,
  idPrefix = 'auth',
  busy: outerBusy = false,
  error: outerError = '',
  notice: outerNotice = '',
  footer,
  ssoFirst = false,
}: {
  mode: AuthMode;
  onMode: (mode: AuthMode) => void;
  /** Same-site path the provider callback returns to. */
  next: string;
  onSignedIn: (result: SignInResult, remember: boolean, method: AuthMethod) => void;
  onOAuthStart?: (provider: OAuthProvider) => void;
  /** 继续 after a sign-in that cancelled a pending account deletion. */
  onContinue: () => void;
  forgotHref?: string;
  idPrefix?: string;
  busy?: boolean;
  error?: string;
  /** A notice from the parent (the OAuth exchange on /login). */
  notice?: string;
  footer?: ReactNode;
  /** Provider buttons above the email form (the sheet), not below it. */
  ssoFirst?: boolean;
}) {
  const t = useT();
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
  const [ownBusy, setBusy] = useState(false);
  const [ownError, setError] = useState('');
  const [ownNotice, setNotice] = useState('');
  const [providers, setProviders] = useState<OAuthProvider[]>([]);
  const busy = ownBusy || outerBusy;
  const error = ownError || outerError;
  const notice = ownNotice || outerNotice;

  useEffect(() => {
    const controller = new AbortController();
    getAuthProviders(controller.signal).then(setProviders, () => {});
    return () => controller.abort();
  }, []);

  useEffect(() => {
    setError('');
    setNotice('');
  }, [mode]);

  const finish = (result: SignInResult, method: AuthMethod) => {
    // A sign-in that cancelled a pending deletion says so before moving on.
    if (result.deletion_cancelled) {
      setNotice(t('login.deletionCancelled'));
      setBusy(false);
    }
    onSignedIn(result, remember, method);
  };

  const adultInput = (): AdultInput =>
    needBirth && birthYear && birthMonth
      ? {adult_confirmed: adultOk, birth_year: Number(birthYear), birth_month: Number(birthMonth)}
      : {adult_confirmed: adultOk};

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
      finish(result, mode === 'register' ? 'register' : 'password');
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
      onOAuthStart?.(provider);
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

  const ssoButtons = mode !== 'forgot' && providers.length > 0 && (
    <>
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
  );

  return (
    <>
      {ssoFirst && ssoButtons && (
        <>
          {ssoButtons}
          <div className="divider-text">
            <span>{t('login.or')}</span>
          </div>
        </>
      )}
      {mode === 'forgot' ? (
        <>
          <h1 className="auth-title">{t('login.forgotTitle')}</h1>
          <p className="auth-sub">{t('login.forgotSub')}</p>
        </>
      ) : (
        <div className="segmented segmented-lg" role="tablist" aria-label={t('login.modeTabs')}>
          <button type="button" role="tab" aria-selected={mode === 'login'} className="segmented-item" onClick={() => onMode('login')}>
            {t('auth.login')}
          </button>
          <button type="button" role="tab" aria-selected={mode === 'register'} className="segmented-item" onClick={() => onMode('register')}>
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
            <BirthMonthPicker year={birthYear} month={birthMonth} onYear={setBirthYear} onMonth={setBirthMonth} idPrefix={`${idPrefix}-birth`} />
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
            {mode === 'login' &&
              (forgotHref ? (
                <Link to={forgotHref} className="text-btn">
                  {t('login.forgot')}
                </Link>
              ) : (
                <button type="button" className="text-btn" onClick={() => onMode('forgot')}>
                  {t('login.forgot')}
                </button>
              ))}
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
          <button type="button" className="btn btn-primary btn-block btn-lg" onClick={onContinue}>
            {t('login.continue')}
          </button>
        ) : (
          <button type="submit" className="btn btn-primary btn-block btn-lg" disabled={busy}>
            {busy ? t('login.wait') : mode === 'login' ? t('auth.login') : mode === 'register' ? t('login.createAccount') : t('login.sendReset')}
          </button>
        )}
        {mode === 'forgot' && (
          <button type="button" className="text-btn" onClick={() => onMode('login')}>
            {t('login.backToLogin')}
          </button>
        )}
      </form>

      {!ssoFirst && ssoButtons && (
        <>
          <div className="divider-text">
            <span>{t('login.or')}</span>
          </div>
          {ssoButtons}
        </>
      )}
      {footer}
    </>
  );
}
