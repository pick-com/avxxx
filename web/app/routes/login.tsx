import {useState, type FormEvent} from 'react';
import {Link, useNavigate, useSearchParams} from 'react-router';
import {ThemeToggle} from '../components/TopBar';
import {ApiError, login, register} from '../lib/api';
import {useAuth} from '../lib/auth';

export const meta = () => [{title: 'REEL · 登录'}];

type Mode = 'login' | 'register';

/** Only same-site paths are accepted as a post-login destination. */
function safeNext(value: string | null): string {
  return value && value.startsWith('/') && !value.startsWith('//') && !value.startsWith('/login') ? value : '/';
}

// Account artboard. Email + password against POST /api/auth/login and /register.
// TODO(tech plan 3.6): Google / Apple sign-in, 记住我, 忘记密码, terms acceptance (terms_acceptances).
export default function LoginPage() {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const {signIn} = useAuth();
  const [mode, setMode] = useState<Mode>(params.get('mode') === 'register' ? 'register' : 'login');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const next = safeNext(params.get('next'));

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setError('');
    if (password.length < 8) {
      setError('密码至少 8 位');
      return;
    }
    setBusy(true);
    try {
      const result = mode === 'login' ? await login(email, password) : await register(email, password, displayName);
      signIn(result);
      navigate(next, {replace: true});
    } catch (cause) {
      // The API reports every auth failure as a generic 500/409 without a reason.
      if (cause instanceof ApiError && cause.status === 0) setError(cause.message);
      else if (mode === 'login') setError('登录失败：邮箱或密码不正确，或服务暂时不可用。');
      else setError(cause instanceof ApiError && cause.status === 409 ? '这个邮箱已经注册过了，请直接登录。' : '注册失败：邮箱可能已被注册，或服务暂时不可用。');
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
          <strong>登录后，继续你没看完的故事</strong>
          <span>同步观看进度和收藏，在手机和电脑上无缝接着看。</span>
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
          <div className="segmented segmented-lg" role="tablist" aria-label="登录或注册">
            <button type="button" role="tab" aria-selected={mode === 'login'} className="segmented-item" onClick={() => setMode('login')}>
              登录
            </button>
            <button type="button" role="tab" aria-selected={mode === 'register'} className="segmented-item" onClick={() => setMode('register')}>
              注册
            </button>
          </div>

          <form className="auth-form" onSubmit={submit} noValidate={false}>
            {mode === 'register' && (
              <label className="field">
                <span>昵称</span>
                <input value={displayName} onChange={e => setDisplayName(e.target.value)} placeholder="你的昵称" autoComplete="nickname" />
              </label>
            )}
            <label className="field">
              <span>邮箱</span>
              <input type="email" value={email} onChange={e => setEmail(e.target.value)} placeholder="name@example.com" autoComplete="email" required />
            </label>
            <label className="field">
              <span>密码</span>
              <input
                type="password"
                value={password}
                onChange={e => setPassword(e.target.value)}
                placeholder="至少 8 位"
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
              {busy ? '请稍候…' : mode === 'login' ? '登录' : '创建账号'}
            </button>
          </form>

          <div className="divider-text">
            <span>或</span>
          </div>
          <button type="button" className="btn btn-secondary btn-block btn-lg" disabled aria-describedby="sso-soon">
            使用 Google 继续 · 即将支持
          </button>
          <button type="button" className="btn btn-secondary btn-block btn-lg" disabled aria-describedby="sso-soon">
            使用 Apple 继续 · 即将支持
          </button>
          <span id="sso-soon" className="sr-only">
            第三方登录即将支持
          </span>
          <Link to={next} className="auth-skip">
            先逛逛，稍后登录
          </Link>
        </div>
      </section>
    </div>
  );
}
