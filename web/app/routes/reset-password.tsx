import {useEffect, useState, type FormEvent} from 'react';
import {Link} from '../lib/nav';
import {ThemeToggle} from '../components/TopBar';
import {ApiError} from '../lib/api';
import {resetPassword} from '../lib/account';
import {t, useT} from '../lib/i18n';
import '../styles/account.css';

export const meta = () => [{title: t('title.resetPassword')}, {name: 'robots', content: 'noindex'}];

// 忘记密码 › 设置新密码. The emailed link is /reset-password#token=… (the
// token is in the fragment so it never reaches a server log or Referer). It
// is read once and removed from the address bar; POST
// /api/auth/password/reset consumes it and signs out every device.

export default function ResetPasswordPage() {
  const t = useT();
  const [token, setToken] = useState<string | null>(null);
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [done, setDone] = useState(false);

  useEffect(() => {
    const value = new URLSearchParams(window.location.hash.slice(1)).get('token');
    setToken(value ?? '');
    if (value) window.history.replaceState(null, '', window.location.pathname);
  }, []);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setError('');
    if (password.length < 8) {
      setError(t('login.passwordShort'));
      return;
    }
    if (password !== confirm) {
      setError(t('reset.mismatch'));
      return;
    }
    setBusy(true);
    try {
      await resetPassword(token ?? '', password);
      setDone(true);
    } catch (cause) {
      setError(cause instanceof ApiError && cause.status === 400 ? t('reset.invalid') : cause instanceof ApiError && cause.status === 429 ? t('login.tooMany') : t('reset.failed'));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="auth-page">
      <section className="auth-main" style={{margin: '0 auto'}}>
        <div className="auth-top">
          <Link to="/" className="logo">
            REEL<span className="logo-dot">.</span>
          </Link>
          <span className="section-spacer" />
          <ThemeToggle />
        </div>
        <div className="auth-card">
          <h1 className="auth-title">{t('reset.title')}</h1>
          {token === '' && !done && (
            <>
              <p className="form-error" role="alert">
                {t('reset.missing')}
              </p>
              <Link to="/login" className="btn btn-secondary btn-block btn-lg">
                {t('reset.requestAgain')}
              </Link>
            </>
          )}
          {done && (
            <>
              <p className="form-ok" role="status">
                {t('reset.done')}
              </p>
              <Link to="/login" className="btn btn-primary btn-block btn-lg">
                {t('auth.login')}
              </Link>
            </>
          )}
          {token && !done && (
            <form className="auth-form" onSubmit={submit}>
              <p className="auth-sub">{t('reset.sub')}</p>
              <label className="field">
                <span>{t('reset.newPassword')}</span>
                <input type="password" value={password} onChange={e => setPassword(e.target.value)} autoComplete="new-password" minLength={8} required placeholder={t('login.passwordPlaceholder')} />
              </label>
              <label className="field">
                <span>{t('reset.confirm')}</span>
                <input type="password" value={confirm} onChange={e => setConfirm(e.target.value)} autoComplete="new-password" minLength={8} required />
              </label>
              {error && (
                <p className="form-error" role="alert">
                  {error}
                </p>
              )}
              <button type="submit" className="btn btn-primary btn-block btn-lg" disabled={busy}>
                {busy ? t('login.wait') : t('reset.submit')}
              </button>
              {error === t('reset.invalid') && (
                <Link to="/login" className="text-btn">
                  {t('reset.requestAgain')}
                </Link>
              )}
            </form>
          )}
        </div>
      </section>
    </div>
  );
}
