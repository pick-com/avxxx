import {useState, type FormEvent} from 'react';
import {Link, useNavigate, useSearchParams} from 'react-router';
import {ThemeToggle} from '../components/TopBar';
import {ApiError} from '../lib/api';
import {adminPasswordStep, adminSecondFactor} from '../lib/adminUsersApi';
import '../styles/admin.css';
import '../styles/admin-users.css';

export const meta = () => [{title: 'REEL · 后台登录'}, {name: 'robots', content: 'noindex'}];

// Back-office sign-in (AdminUsers, tech plan 3.6): username or email +
// password (POST /api/admin-auth/login), then a 6-digit authenticator code
// or a one-time recovery code (POST /api/admin-auth/verify), which sets the
// HttpOnly session cookie. Failed attempts lock the account with backoff;
// the API also limits attempts per address.

/** Only back-office paths are accepted as a post-login destination. */
function safeNext(v: string | null): string {
  return v && v.startsWith('/admin') && !v.startsWith('/admin/login') && !v.startsWith('//') ? v : '/admin';
}

function errorText(cause: unknown): string {
  if (cause instanceof ApiError) {
    if (cause.status === 429) return cause.detail?.includes('locked') || cause.detail?.includes('failed attempts') ? '失败次数过多，账号已暂时锁定，请稍后再试' : '尝试过于频繁，请稍后再试';
    if (cause.status === 401) return cause.detail?.includes('expired') ? '登录步骤已过期，请重新输入密码' : '账号、密码或验证码不正确';
    if (cause.status === 403) return '请求被拒绝，请刷新页面后重试';
    return cause.detail || cause.message;
  }
  return cause instanceof Error ? cause.message : String(cause);
}

export default function AdminLogin() {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const [step, setStep] = useState<'password' | 'code'>('password');
  const [login, setLogin] = useState('');
  const [password, setPassword] = useState('');
  const [challenge, setChallenge] = useState('');
  const [code, setCode] = useState('');
  const [useRecovery, setUseRecovery] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');

  const submitPassword = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError('');
    try {
      const r = await adminPasswordStep(login.trim(), password);
      setChallenge(r.challenge);
      setPassword('');
      setStep('code');
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setBusy(false);
    }
  };

  const submitCode = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError('');
    try {
      const r = await adminSecondFactor(challenge, useRecovery ? {recovery_code: code.trim()} : {code: code.replace(/\s/g, '')});
      if (r.method === 'recovery_code') {
        setNotice(`已使用一个恢复码，还剩 ${r.recovery_codes_left ?? '?'} 个。请在「用户与权限」里重新生成恢复码。`);
        window.setTimeout(() => navigate(safeNext(params.get('next')), {replace: true}), 2500);
        return;
      }
      navigate(safeNext(params.get('next')), {replace: true});
    } catch (cause) {
      setError(errorText(cause));
      setCode('');
      if (cause instanceof ApiError && cause.status === 401 && cause.detail?.includes('expired')) {
        setStep('password');
        setChallenge('');
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="adm-login">
      <div className="adm-login-card adm-card">
        <div className="adm-login-head">
          <span className="adm-logo">
            REEL<span className="adm-dot">.</span>
          </span>
          <span className="adm-brand-tag">后台</span>
          <span className="adm-login-tools">
            <ThemeToggle />
          </span>
        </div>
        {step === 'password' ? (
          <form className="adm-login-form" onSubmit={submitPassword}>
            <h1>登录后台</h1>
            <label className="adm-field">
              用户名或邮箱
              <input value={login} onChange={e => setLogin(e.target.value)} autoComplete="username" autoFocus required maxLength={254} />
            </label>
            <label className="adm-field">
              密码
              <input type="password" value={password} onChange={e => setPassword(e.target.value)} autoComplete="current-password" required maxLength={1024} />
            </label>
            {error && (
              <p className="adm-login-error" role="alert">
                {error}
              </p>
            )}
            <button type="submit" className="btn btn-primary" disabled={busy}>
              {busy ? '验证中…' : '下一步'}
            </button>
            <p className="muted adm-login-hint">没有账号？请超级管理员在「用户与权限」里邀请你。</p>
          </form>
        ) : (
          <form className="adm-login-form" onSubmit={submitCode}>
            <h1>两步验证</h1>
            {useRecovery ? (
              <label className="adm-field">
                恢复码（每个只能用一次）
                <input value={code} onChange={e => setCode(e.target.value)} autoComplete="off" autoFocus required placeholder="xxxxx-xxxxx" maxLength={20} />
              </label>
            ) : (
              <label className="adm-field">
                身份验证器 App 中的 6 位验证码
                <input
                  value={code}
                  onChange={e => setCode(e.target.value)}
                  inputMode="numeric"
                  autoComplete="one-time-code"
                  pattern="[0-9 ]{6,7}"
                  autoFocus
                  required
                  maxLength={7}
                />
              </label>
            )}
            {error && (
              <p className="adm-login-error" role="alert">
                {error}
              </p>
            )}
            {notice && (
              <p className="adm-login-notice" role="status">
                {notice}
              </p>
            )}
            <button type="submit" className="btn btn-primary" disabled={busy || !!notice}>
              {busy ? '验证中…' : '登录'}
            </button>
            <div className="adm-login-links">
              <button type="button" className="adm-link" onClick={() => (setUseRecovery(v => !v), setCode(''), setError(''))}>
                {useRecovery ? '改用验证码' : '手机不在身边？使用恢复码'}
              </button>
              <button type="button" className="adm-link" onClick={() => (setStep('password'), setChallenge(''), setCode(''), setError(''))}>
                返回
              </button>
            </div>
          </form>
        )}
        <p className="adm-login-foot muted">
          <Link to="/admin">紧急令牌（break-glass）</Link> · <Link to="/">返回前台</Link>
        </p>
      </div>
    </div>
  );
}
