import {useEffect, useState, type FormEvent} from 'react';
import {Link} from 'react-router';
import {ErrorBanner} from '../components/States';
import {ThemeToggle} from '../components/TopBar';
import {adminConflicts, adminIdentityCandidates, adminResolveConflict, tokens, type Conflict, type IdentityCandidate} from '../lib/api';

export const meta = () => [{title: 'REEL · 数据审核'}];

// Admin review queue, ported from the previous single-file client.
// Uses only the admin token (never the user token).
// TODO(tech plan 3.11): replace with the AdminNav / AdminDedupe / AdminAudit workbench.
export default function AdminReview() {
  const [token, setToken] = useState('');
  const [conflicts, setConflicts] = useState<Conflict[]>([]);
  const [candidates, setCandidates] = useState<IdentityCandidate[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const load = async () => {
    setError('');
    setBusy(true);
    try {
      const [c, i] = await Promise.all([adminConflicts(50), adminIdentityCandidates(50)]);
      setConflicts(c);
      setCandidates(i);
      setLoaded(true);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  };

  useEffect(() => {
    const saved = tokens.admin();
    if (saved) {
      setToken(saved);
      void load();
    }
  }, []);

  const submit = (event: FormEvent) => {
    event.preventDefault();
    tokens.setAdmin(token.trim() || null);
    void load();
  };

  const resolve = async (id: number, value: unknown) => {
    try {
      await adminResolveConflict(id, value, 'Resolved from admin review');
      await load();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    }
  };

  return (
    <div className="admin">
      <header className="admin-bar">
        <strong>REEL · 管理审核</strong>
        <span className="section-spacer" />
        <ThemeToggle />
        <Link to="/" className="btn btn-secondary btn-sm">
          返回前台
        </Link>
      </header>
      <main className="admin-main">
        <p className="eyebrow">ADMIN · REVIEW QUEUE</p>
        <h1>数据审核</h1>

        <section className="admin-card">
          <h2>管理员令牌</h2>
          <form className="admin-token" onSubmit={submit}>
            <input type="password" value={token} onChange={e => setToken(e.target.value)} placeholder="ADMIN_TOKEN" aria-label="管理员令牌" autoComplete="off" />
            <button type="submit" className="btn btn-primary" disabled={busy}>
              {busy ? '加载中…' : '加载审核队列'}
            </button>
          </form>
          {error && <ErrorBanner error={error.includes('需要登录') ? '管理员令牌无效或未设置' : error} onRetry={load} />}
        </section>

        <section className="admin-card">
          <h2>待处理字段冲突（{conflicts.length}）</h2>
          {conflicts.length === 0 ? (
            <p className="muted">{loaded ? '暂无待处理冲突。' : '请先输入管理员令牌。'}</p>
          ) : (
            <div className="review-list">
              {conflicts.map(item => (
                <article key={item.id} className="review-item">
                  <div className="review-heading">
                    <strong>
                      #{item.id} · {item.entity_type} {item.entity_id} · {item.field_name}
                    </strong>
                    <span className="review-tag">{item.severity}</span>
                  </div>
                  <p>
                    当前值：<code>{JSON.stringify(item.current_value)}</code>
                  </p>
                  <pre>{JSON.stringify(item.candidates, null, 2)}</pre>
                  <div className="action-row">
                    <button type="button" className="btn btn-primary btn-sm" onClick={() => resolve(item.id, Array.isArray(item.candidates) ? item.candidates[0] : item.candidates)}>
                      采用首个候选值
                    </button>
                    <button type="button" className="btn btn-secondary btn-sm" onClick={() => resolve(item.id, item.current_value)}>
                      保留当前值
                    </button>
                  </div>
                </article>
              ))}
            </div>
          )}
        </section>

        <section className="admin-card">
          <h2>身份候选（{candidates.length}）</h2>
          {candidates.length === 0 ? (
            <p className="muted">{loaded ? '暂无身份候选。' : '请先输入管理员令牌。'}</p>
          ) : (
            <div className="review-list">
              {candidates.map(item => (
                <article key={item.id} className="review-item">
                  <div className="review-heading">
                    <strong>
                      #{item.id} · {item.entity_type} {item.left_id} ↔ {item.right_id}
                    </strong>
                    <span className="review-tag">匹配度 {item.score}</span>
                  </div>
                  <p>状态：{item.status}</p>
                  <pre>{JSON.stringify(item.evidence, null, 2)}</pre>
                </article>
              ))}
            </div>
          )}
        </section>
      </main>
    </div>
  );
}
