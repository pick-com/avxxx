import {useEffect, useState, type FormEvent, type ReactNode} from 'react';
import {NavLink, Link} from 'react-router';
import {tokens} from '../lib/api';
import {ThemeToggle} from './TopBar';

/** Admin header with the workbench pages that exist so far. */
export function AdminBar() {
  return (
    <header className="admin-bar">
      <strong>REEL · 管理</strong>
      <nav className="admin-nav" aria-label="管理后台">
        <NavLink to="/admin/review" className="admin-nav-link">
          数据审核
        </NavLink>
        <NavLink to="/admin/dedupe" className="admin-nav-link">
          重复与归并
        </NavLink>
      </nav>
      <span className="section-spacer" />
      <ThemeToggle />
      <Link to="/" className="btn btn-secondary btn-sm">
        返回前台
      </Link>
    </header>
  );
}

/**
 * Admin token entry (sent only to admin/* — tech plan 2.6). Saves the token to
 * localStorage and calls onSubmit to (re)load the page's data.
 */
export function AdminTokenForm({busy, onSubmit, submitLabel, children}: {
  busy: boolean;
  onSubmit: () => void;
  submitLabel: string;
  children?: ReactNode;
}) {
  const [token, setToken] = useState('');
  // Read storage after mount: route modules avoid browser globals during render.
  useEffect(() => setToken(tokens.admin() ?? ''), []);
  const submit = (event: FormEvent) => {
    event.preventDefault();
    tokens.setAdmin(token.trim() || null);
    onSubmit();
  };
  return (
    <section className="admin-card">
      <h2>管理员令牌</h2>
      <form className="admin-token" onSubmit={submit}>
        <input type="password" value={token} onChange={e => setToken(e.target.value)} placeholder="ADMIN_TOKEN" aria-label="管理员令牌" autoComplete="off" />
        <button type="submit" className="btn btn-primary" disabled={busy}>
          {busy ? '加载中…' : submitLabel}
        </button>
      </form>
      {children}
    </section>
  );
}
