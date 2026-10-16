import {useState, type FormEvent} from 'react';
import {Link, useNavigate, useSearchParams} from 'react-router';
import {BirthMonthPicker, MIN_REGISTER_AGE, ageFromYearMonth} from '../components/AgeGate';
import {ThemeToggle} from '../components/TopBar';
import {ApiError, login, register} from '../lib/api';
import {useAuth} from '../lib/auth';
import {t, useT} from '../lib/i18n';

export const meta = () => [{title: t('title.login')}];

type Mode = 'login' | 'register';

/** Only same-site paths are accepted as a post-login destination. */
function safeNext(value: string | null): string {
  return value && value.startsWith('/') && !value.startsWith('//') && !value.startsWith('/login') ? value : '/';
}

// Account artboard. Email + password against POST /api/auth/login and /register.
// TODO(tech plan 3.6): Google / Apple sign-in, 记住我, 忘记密码, terms acceptance (terms_acceptances).
//
// Registration asks for birth year and month (PRD 15) and refuses under-13s
// before anything is sent. The birth date is not sent to the API (no field
// for it yet; TODO age_verifications). A refusal is remembered on this device
// for 24 hours so the form cannot simply be retried with another year.
// A new account goes to /welcome (Onboarding) before `next`.

const AGE_BLOCK_KEY = 'register_age_block';
const AGE_BLOCK_MS = 24 * 60 * 60 * 1000;

function ageBlocked(): boolean {
  try {
    const at = Number(window.localStorage.getItem(AGE_BLOCK_KEY) ?? 0);
    return at > 0 && Date.now() - at < AGE_BLOCK_MS;
  } catch {
    return false;
  }
}

const underAge = () => t('login.underAge', {age: MIN_REGISTER_AGE});
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
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const next = safeNext(params.get('next'));

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setError('');
    if (mode === 'register') {
      if (ageBlocked()) {
        setError(underAge());
        return;
      }
      if (!birthYear || !birthMonth) {
        setError(t('login.pickBirth'));
        return;
      }
      if (ageFromYearMonth(Number(birthYear), Number(birthMonth)) < MIN_REGISTER_AGE) {
        try {
          window.localStorage.setItem(AGE_BLOCK_KEY, String(Date.now()));
        } catch {
          /* not persisted */
        }
        setError(underAge());
        return;
      }
    }
    if (password.length < 8) {
      setError(t('login.passwordShort'));
      return;
    }
    setBusy(true);
    try {
      const result = mode === 'login' ? await login(email, password) : await register(email, password, displayName);
      signIn(result);
      if (mode === 'register') navigate(`/welcome?next=${encodeURIComponent(next)}`, {replace: true});
      else navigate(next, {replace: true});
    } catch (cause) {
      // The API reports every auth failure as a generic 500/409 without a reason.
      if (cause instanceof ApiError && cause.status === 0) setError(cause.message);
      else if (mode === 'login') setError(t('login.loginFailed'));
      else setError(cause instanceof ApiError && cause.status === 409 ? t('login.emailTaken') : t('login.registerFailed'));
    } finally {
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
          <div className="segmented segmented-lg" role="tablist" aria-label={t('login.modeTabs')}>
            <button type="button" role="tab" aria-selected={mode === 'login'} className="segmented-item" onClick={() => setMode('login')}>
              {t('auth.login')}
            </button>
            <button type="button" role="tab" aria-selected={mode === 'register'} className="segmented-item" onClick={() => setMode('register')}>
              {t('login.register')}
            </button>
          </div>

          <form className="auth-form" onSubmit={submit} noValidate={false}>
            {mode === 'register' && (
              <label className="field">
                <span>{t('login.nickname')}</span>
                <input value={displayName} onChange={e => setDisplayName(e.target.value)} placeholder={t('login.nicknamePlaceholder')} autoComplete="nickname" />
              </label>
            )}
            <label className="field">
              <span>{t('login.email')}</span>
              <input type="email" value={email} onChange={e => setEmail(e.target.value)} placeholder="name@example.com" autoComplete="email" required />
            </label>
            {mode === 'register' && (
              <div className="field">
                <BirthMonthPicker year={birthYear} month={birthMonth} onYear={setBirthYear} onMonth={setBirthMonth} idPrefix="reg-birth" />
                <span className="field-hint">{t('login.birthHint')}</span>
              </div>
            )}
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
            {error && (
              <p className="form-error" role="alert">
                {error}
              </p>
            )}
            <button type="submit" className="btn btn-primary btn-block btn-lg" disabled={busy}>
              {busy ? t('login.wait') : mode === 'login' ? t('auth.login') : t('login.createAccount')}
            </button>
          </form>

          <div className="divider-text">
            <span>{t('login.or')}</span>
          </div>
          <button type="button" className="btn btn-secondary btn-block btn-lg" disabled aria-describedby="sso-soon">
            {t('login.google')}
          </button>
          <button type="button" className="btn btn-secondary btn-block btn-lg" disabled aria-describedby="sso-soon">
            {t('login.apple')}
          </button>
          <span id="sso-soon" className="sr-only">
            {t('login.ssoSoon')}
          </span>
          <Link to={next} className="auth-skip">
            {t('login.skip')}
          </Link>
        </div>
      </section>
    </div>
  );
}
