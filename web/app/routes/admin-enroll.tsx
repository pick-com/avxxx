import {useEffect, useState, type FormEvent} from 'react';
import {Link} from 'react-router';
import {ApiError} from '../lib/api';
import {completeAdminEnrollment, getAdminInvite, type AdminInvite} from '../lib/adminUsersApi';
import '../styles/admin.css';
import '../styles/admin-users.css';

export const meta = () => [{title: 'REEL · 启用后台账号'}, {name: 'robots', content: 'noindex'}];

// Enrollment for an invited admin, or after a 2FA reset (AdminUsers): the
// link is /admin/enroll#<token> (the fragment never reaches a server log).
// Shows the TOTP key to add to an authenticator app, sets the password,
// checks one code and then shows the ten recovery codes once.

const MIN_PASSWORD = 12;

/** "ABCD EFGH ..." for manual entry. */
const groupKey = (s: string) => s.replace(/(.{4})/g, '$1 ').trim();

export default function AdminEnroll() {
  const [token, setToken] = useState('');
  const [invite, setInvite] = useState<AdminInvite | null>(null);
  const [loadError, setLoadError] = useState('');
  const [password, setPassword] = useState('');
  const [repeat, setRepeat] = useState('');
  const [code, setCode] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [done, setDone] = useState<{username: string; codes: string[]} | null>(null);

  useEffect(() => {
    const t = window.location.hash.replace(/^#/, '').trim();
    // Drop the token from the address bar and history once read.
    window.history.replaceState(null, '', window.location.pathname);
    if (!t) {
      setLoadError('链接不完整：请使用邀请人发给你的完整链接。');
      return;
    }
    setToken(t);
    getAdminInvite(t)
      .then(setInvite)
      .catch(cause => setLoadError(cause instanceof ApiError && cause.status === 404 ? '链接无效或已过期，请联系超级管理员重新发送。' : cause instanceof Error ? cause.message : String(cause)));
  }, []);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (password.length < MIN_PASSWORD) return setError(`密码至少 ${MIN_PASSWORD} 个字符`);
    if (password !== repeat) return setError('两次输入的密码不一致');
    setBusy(true);
    setError('');
    try {
      const r = await completeAdminEnrollment(token, password, code.replace(/\s/g, ''));
      setDone({username: r.username, codes: r.recovery_codes});
      setPassword('');
      setRepeat('');
    } catch (cause) {
      setError(cause instanceof ApiError ? cause.detail || cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  };

  const downloadCodes = () => {
    if (!done) return;
    const blob = new Blob([`REEL 后台恢复码（${done.username}）\n每个只能使用一次。\n\n${done.codes.join('\n')}\n`], {type: 'text/plain'});
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = `reel-admin-recovery-${done.username}.txt`;
    a.click();
    URL.revokeObjectURL(a.href);
  };

  return (
    <div className="adm-login">
      <div className="adm-login-card adm-card adm-enroll">
        <div className="adm-login-head">
          <span className="adm-logo">
            REEL<span className="adm-dot">.</span>
          </span>
          <span className="adm-brand-tag">后台</span>
        </div>
        {done ? (
          <div className="adm-login-form">
            <h1>账号已启用</h1>
            <p>请保存下面的恢复码。手机丢失时用它登录，每个只能用一次，离开本页后不会再显示。</p>
            <ol className="adm-codes mono">
              {done.codes.map(c => (
                <li key={c}>{c}</li>
              ))}
            </ol>
            <div className="adm-row-actions">
              <button type="button" className="btn btn-secondary btn-sm" onClick={() => void navigator.clipboard?.writeText(done.codes.join('\n'))}>
                复制
              </button>
              <button type="button" className="btn btn-secondary btn-sm" onClick={downloadCodes}>
                下载 .txt
              </button>
            </div>
            <Link to="/admin/login" className="btn btn-primary">
              去登录
            </Link>
          </div>
        ) : loadError ? (
          <div className="adm-login-form">
            <h1>无法启用账号</h1>
            <p className="adm-login-error">{loadError}</p>
          </div>
        ) : !invite ? (
          <p className="muted">正在读取邀请…</p>
        ) : (
          <form className="adm-login-form" onSubmit={submit}>
            <h1>启用后台账号</h1>
            <p className="muted">
              账号 <strong>{invite.username}</strong>（{invite.email}）。链接有效期至 {new Date(invite.expires_at).toLocaleString('zh-CN')}。
            </p>
            <h2>1. 添加到身份验证器</h2>
            <p className="muted">
              在 Google Authenticator、1Password、Authy 等 App 中选择「输入设置密钥」，类型选「基于时间」；手机上也可以直接点下面的链接。
            </p>
            <p className="adm-totp-key mono" aria-label="设置密钥">
              {groupKey(invite.secret)}
            </p>
            <p>
              <a href={invite.otpauth_uri}>在本机的验证器 App 中打开</a>
            </p>
            <h2>2. 设置密码</h2>
            <label className="adm-field">
              密码（至少 {MIN_PASSWORD} 个字符）
              <input type="password" value={password} onChange={e => setPassword(e.target.value)} autoComplete="new-password" required minLength={MIN_PASSWORD} maxLength={1024} />
            </label>
            <label className="adm-field">
              再输入一次
              <input type="password" value={repeat} onChange={e => setRepeat(e.target.value)} autoComplete="new-password" required />
            </label>
            <h2>3. 输入验证码</h2>
            <label className="adm-field">
              App 显示的 6 位验证码
              <input value={code} onChange={e => setCode(e.target.value)} inputMode="numeric" autoComplete="one-time-code" pattern="[0-9 ]{6,7}" required maxLength={7} />
            </label>
            {error && (
              <p className="adm-login-error" role="alert">
                {error}
              </p>
            )}
            <button type="submit" className="btn btn-primary" disabled={busy}>
              {busy ? '提交中…' : '启用账号'}
            </button>
          </form>
        )}
      </div>
    </div>
  );
}
