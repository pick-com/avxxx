import {useEffect, useState, type FormEvent} from 'react';
import {Link, useNavigate, useSearchParams} from 'react-router';
import {AdminHeader, AdminNotice, EmptyRow, MoreButton, NotDeployed, Pill, fmtTime, statusTone} from '../components/AdminUI';
import {isMissingEndpoint, listReleasePage, type Release} from '../lib/api';
import {RELEASE_STATUSES, STATUS_LABELS, adminErrorText, getAdminReleases, type AdminReleaseSummary} from '../lib/adminApi';
import {useAdmin} from '../lib/adminContext';

export const meta = () => [{title: 'REEL · 视频'}];

// AdminVideo list (PRD 11 A3): GET admin/releases?q=&status=&cursor= (PR #61),
// every status including drafts and held releases. Without that route the
// page falls back to the public catalog list, read-only.

export default function AdminVideos() {
  const [params, setParams] = useSearchParams();
  const navigate = useNavigate();
  const q = params.get('q') ?? '';
  const status = params.get('status') ?? '';
  const {hasToken, epoch} = useAdmin();
  const [draft, setDraft] = useState(q);
  const [items, setItems] = useState<AdminReleaseSummary[]>([]);
  const [fallback, setFallback] = useState<Release[] | null>(null);
  const [next, setNext] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [loaded, setLoaded] = useState(false);

  useEffect(() => setDraft(q), [q]);

  const load = async (cursor: string | null) => {
    setBusy(true);
    setError('');
    try {
      const page = await getAdminReleases({q, status, cursor, limit: 50});
      setFallback(null);
      setItems(prev => (cursor ? [...prev, ...page.items] : page.items));
      setNext(page.next_cursor);
    } catch (cause) {
      if (isMissingEndpoint(cause)) {
        try {
          const page = await listReleasePage({q: q || undefined}, 50, cursor);
          setFallback(prev => (cursor && prev ? [...prev, ...page.items] : page.items));
          setNext(page.nextCursor);
        } catch (inner) {
          setError(adminErrorText(inner));
        }
      } else {
        setError(adminErrorText(cause));
      }
    } finally {
      setBusy(false);
      setLoaded(true);
    }
  };

  useEffect(() => {
    if (hasToken) void load(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hasToken, epoch, q, status]);

  const search = (e: FormEvent) => {
    e.preventDefault();
    const p = new URLSearchParams(params);
    if (draft.trim()) p.set('q', draft.trim());
    else p.delete('q');
    setParams(p, {replace: true});
  };
  const setStatus = (s: string) => {
    const p = new URLSearchParams(params);
    if (s) p.set('status', s);
    else p.delete('status');
    setParams(p, {replace: true});
  };

  return (
    <>
      <AdminHeader eyebrow="ADMIN · VIDEOS" title="视频" sub="搜索标题、日文标题、slug、番号或 ID；草稿与合规暂停的作品也在这里。" />
      {fallback && <NotDeployed what="视频编辑接口（admin/releases）" />}
      <AdminNotice error={error} />

      <section className="adm-card">
        <form className="adm-toolbar" onSubmit={search}>
          <input
            type="search"
            value={draft}
            onChange={e => setDraft(e.target.value)}
            placeholder="标题 / 番号 / slug / ID"
            aria-label="搜索视频"
            style={{flex: 1, minWidth: 260}}
          />
          <button type="submit" className="btn btn-secondary btn-sm">
            搜索
          </button>
          {!fallback && (
            <div className="adm-chips" role="group" aria-label="状态">
              {['', ...RELEASE_STATUSES].map(s => (
                <button key={s || 'all'} type="button" className="adm-chip" aria-pressed={status === s} onClick={() => setStatus(s)}>
                  {s ? STATUS_LABELS[s] : '全部'}
                </button>
              ))}
            </div>
          )}
        </form>

        <div className="adm-table-wrap">
          {fallback ? (
            <table className="adm-table">
              <thead>
                <tr>
                  <th className="num">ID</th>
                  <th>标题</th>
                  <th>发布日期</th>
                  <th>状态</th>
                </tr>
              </thead>
              <tbody>
                {fallback.length === 0 && <EmptyRow cols={4}>没有结果</EmptyRow>}
                {fallback.map(r => (
                  <tr key={r.id}>
                    <td className="num">{r.id}</td>
                    <td className="wrap">
                      <Link to={`/releases/${r.slug}`} target="_blank">
                        {r.canonical_title}
                      </Link>
                    </td>
                    <td>{r.release_date?.slice(0, 10) ?? '—'}</td>
                    <td>
                      <Pill tone={statusTone(r.status)}>{STATUS_LABELS[r.status] ?? r.status}</Pill>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          ) : (
            <table className="adm-table">
              <thead>
                <tr>
                  <th className="num">ID</th>
                  <th>标题</th>
                  <th>番号</th>
                  <th>状态</th>
                  <th>发布日期</th>
                  <th className="num">片源</th>
                  <th>待处理</th>
                  <th>更新</th>
                </tr>
              </thead>
              <tbody>
                {loaded && items.length === 0 && <EmptyRow cols={8}>没有结果</EmptyRow>}
                {items.map(r => (
                  <tr
                    key={r.id}
                    className={`clickable ${r.status === 'held_review' ? 'flag-red' : ''}`}
                    onClick={() => navigate(`/admin/videos/${r.id}`)}
                  >
                    <td className="num">{r.id}</td>
                    <td className="wrap">
                      <Link to={`/admin/videos/${r.id}`} onClick={e => e.stopPropagation()}>
                        {r.title}
                      </Link>
                      {r.title_ja && <div className="adm-source-meta">{r.title_ja}</div>}
                    </td>
                    <td className="mono">{(r.identifiers ?? []).join(' · ') || '—'}</td>
                    <td>
                      <Pill tone={statusTone(r.status)}>{STATUS_LABELS[r.status] ?? r.status}</Pill>
                      {r.status === 'upcoming' && r.scheduled_at && <div className="adm-source-meta">{fmtTime(r.scheduled_at)}</div>}
                    </td>
                    <td>{r.release_date ?? '—'}</td>
                    <td className="num">{r.source_count}</td>
                    <td>
                      <div className="adm-chips">
                        {r.open_issues > 0 && <Pill tone="amber">问题 {r.open_issues}</Pill>}
                        {r.pending_conflicts > 0 && <Pill tone="blue">冲突 {r.pending_conflicts}</Pill>}
                        {!r.open_issues && !r.pending_conflicts && <span className="muted">—</span>}
                      </div>
                    </td>
                    <td>{fmtTime(r.updated_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
        <MoreButton next={next} busy={busy} onMore={() => void load(next)} />
      </section>
    </>
  );
}
