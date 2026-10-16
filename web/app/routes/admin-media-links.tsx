import {useState} from 'react';
import {Link, useSearchParams} from 'react-router';
import {AdminHeader, AdminNotice, EmptyRow, MoreButton, NotDeployed, Pill, fmtTime, type Tone} from '../components/AdminUI';
import {
  LINK_PROVIDERS,
  LINK_STATUSES,
  LINK_STATUS_LABELS,
  chooseMediaLink,
  getMediaLinks,
  mediaURL,
  retryMediaLink,
  routeMissing,
  runMediaLinks,
  type MediaLinkRow,
} from '../lib/adminGovApi';
import {useAdminAction, useAdminLoad} from '../lib/adminContext';

export const meta = () => [{title: 'REEL · 片源映射'}];

// 片源映射 (internal/medialink): the offline release -> provider media-id
// linking. GET admin/media-links?provider&status&cursor, POST
// admin/media-links/{release_id}/retry?provider, .../choose {provider,
// media_id}, POST admin/media-links/run (enqueue the media_link job now).
// The provider-wide backoff (429/403, error streak) is not exposed by the
// API; rate-limit errors in the latest attempts are flagged instead.

const statusTone = (s: string): Tone => (s === 'found' ? 'green' : s === 'ambiguous' ? 'amber' : s === 'error' ? 'red' : 'plain');
const RATE_LIMIT = /\b(429|403)\b|rate|too many|forbidden/i;

function Candidates({row, busy, onChoose}: {row: MediaLinkRow; busy: boolean; onChoose: (id: string) => void}) {
  const list = row.candidates ?? [];
  const linked = row.linked_media_id || row.matched_media_id;
  if (list.length === 0) return <span className="muted">—</span>;
  return (
    <ul className="adm-near">
      {list.map(c => {
        const url = mediaURL(row.provider, c);
        const isLinked = c === linked;
        return (
          <li key={c}>
            {url ? (
              <a href={url} target="_blank" rel="noreferrer noopener" className="mono">
                {c}
              </a>
            ) : (
              <span className="mono">{c}</span>
            )}{' '}
            {isLinked ? (
              <Pill tone="green">已关联</Pill>
            ) : (
              <button type="button" className="adm-link" disabled={busy} onClick={() => onChoose(c)}>
                选择
              </button>
            )}
          </li>
        );
      })}
    </ul>
  );
}

export default function AdminMediaLinks() {
  const [params, setParams] = useSearchParams();
  const status = params.get('status') ?? '';
  const provider = params.get('provider') ?? 'javtiful';
  const [extra, setExtra] = useState<{items: MediaLinkRow[]; next: string | null} | null>(null);
  const [moreBusy, setMoreBusy] = useState(false);
  const [manual, setManual] = useState<Record<number, string>>({});
  const list = useAdminLoad(() => getMediaLinks({provider, status, limit: 50}), [provider, status]);
  const act = useAdminAction();

  const items = [...(list.data?.items ?? []), ...(extra?.items ?? [])];
  const next = extra ? extra.next : list.data?.next_cursor ?? null;
  const reload = () => {
    setExtra(null);
    list.reload();
  };
  const more = async () => {
    if (!next) return;
    setMoreBusy(true);
    try {
      const page = await getMediaLinks({provider, status, cursor: next, limit: 50});
      setExtra(prev => ({items: [...(prev?.items ?? []), ...page.items], next: page.next_cursor}));
    } catch (cause) {
      act.setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setMoreBusy(false);
    }
  };
  const setFilter = (k: string, v: string) => {
    const p = new URLSearchParams(params);
    if (v) p.set(k, v);
    else p.delete(k);
    setExtra(null);
    setParams(p, {replace: true});
  };

  const choose = (row: MediaLinkRow, mediaId: string) =>
    void act
      .attempt(`choose-${row.release_id}`, () => chooseMediaLink(row.release_id, row.provider, mediaId), `已把 ${row.codes?.[0] ?? row.slug} 关联到 ${mediaId}`)
      .then(v => {
        if (v) {
          setManual(m => ({...m, [row.release_id]: ''}));
          reload();
        }
      });
  const retry = (row: MediaLinkRow) =>
    void act
      .attempt(`retry-${row.release_id}`, () => retryMediaLink(row.release_id, row.provider), `已把 ${row.codes?.[0] ?? row.slug} 设为立即可重试；下次运行时会重新搜索`)
      .then(v => v && reload());
  const run = () =>
    void act.attempt('run', () => runMediaLinks(), v => (v.enqueued ? `已排入映射任务 #${v.job_id}` : `映射任务 #${v.job_id} 已在排队或运行中`));

  const recent = items.slice(0, 10);
  const rateLimited = recent.filter(r => r.status === 'error' && RATE_LIMIT.test(r.last_error ?? '')).length;
  const lastError = recent.find(r => r.status === 'error');
  const missing = list.failure && routeMissing(list.failure);

  return (
    <>
      <AdminHeader
        eyebrow="ADMIN · MEDIA LINKS"
        title="片源映射"
        sub="按番号把作品预先映射到播放提供方的媒体 ID。多个候选时由人工选择；未找到的按退避时间自动重试。"
        actions={
          <>
            <button type="button" className="btn btn-primary btn-sm" disabled={act.pending === 'run' || !!missing} onClick={run}>
              {act.pending === 'run' ? '排入中…' : '立即运行'}
            </button>
            <button type="button" className="btn btn-secondary btn-sm" disabled={list.busy} onClick={reload}>
              {list.busy ? '刷新中…' : '刷新'}
            </button>
          </>
        }
      />
      {missing ? <NotDeployed what="片源映射接口（admin/media-links）" /> : <AdminNotice error={list.error || act.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />}

      {rateLimited > 0 && (
        <div className="adm-notice adm-notice-warn" role="status">
          最近 10 次尝试里有 {rateLimited} 次被提供方限流（403 / 429）：提供方整体退避中（限流后 1 小时、连续 3 次出错后 30 分钟），期间运行会跳过。
        </div>
      )}

      <section className="adm-card">
        <div className="adm-toolbar">
          <label className="adm-field">
            提供方
            <select value={provider} onChange={e => setFilter('provider', e.target.value === 'javtiful' ? '' : e.target.value)}>
              {Object.keys(LINK_PROVIDERS).map(p => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))}
            </select>
          </label>
          <div className="adm-seg" role="tablist" aria-label="状态">
            <button type="button" role="tab" aria-selected={!status} className={`adm-seg-item ${!status ? 'on' : ''}`} onClick={() => setFilter('status', '')}>
              全部
            </button>
            {LINK_STATUSES.map(s => (
              <button key={s} type="button" role="tab" aria-selected={status === s} className={`adm-seg-item ${status === s ? 'on' : ''}`} onClick={() => setFilter('status', s)}>
                {LINK_STATUS_LABELS[s]}
              </button>
            ))}
          </div>
          {lastError && (
            <span className="muted" title={lastError.last_error}>
              最近错误 {fmtTime(lastError.tried_at)}：{(lastError.last_error ?? '').slice(0, 60)}
            </span>
          )}
        </div>
        <div className="adm-table-wrap">
          <table className="adm-table">
            <thead>
              <tr>
                <th>作品</th>
                <th>番号</th>
                <th>状态</th>
                <th className="num">次数</th>
                <th>上次尝试</th>
                <th>下次自动重试</th>
                <th>候选 / 关联</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {list.data && items.length === 0 && <EmptyRow cols={8}>没有符合条件的映射记录</EmptyRow>}
              {items.map(r => {
                const due = r.next_try_at ? new Date(r.next_try_at).getTime() <= Date.now() : false;
                return (
                  <tr key={`${r.provider}/${r.release_id}`} className={r.status === 'error' ? 'flag-red' : ''}>
                    <td className="wrap" style={{maxWidth: 320}}>
                      <Link to={`/admin/videos/${r.release_id}?tab=sources`}>{r.title || r.slug}</Link>
                      <div className="adm-source-meta">
                        #{r.release_id} ·{' '}
                        <Link to={`/releases/${encodeURIComponent(r.slug)}`} target="_blank">
                          前台
                        </Link>
                      </div>
                    </td>
                    <td className="mono">{(r.codes ?? []).join(', ') || '—'}</td>
                    <td>
                      <Pill tone={statusTone(r.status)}>{LINK_STATUS_LABELS[r.status] ?? r.status}</Pill>
                      {r.last_error && (
                        <div className="adm-source-meta mono" title={r.last_error}>
                          {r.last_error.slice(0, 80)}
                        </div>
                      )}
                    </td>
                    <td className="num">{r.attempts}</td>
                    <td>{fmtTime(r.tried_at)}</td>
                    <td>{r.next_try_at ? (due ? <Pill tone="blue">已到期</Pill> : fmtTime(r.next_try_at)) : <span className="muted">不自动重试</span>}</td>
                    <td className="wrap">
                      <Candidates row={r} busy={act.pending !== null} onChoose={id => choose(r, id)} />
                      {r.linked_media_id && !(r.candidates ?? []).includes(r.linked_media_id) && (
                        <div className="adm-source-meta">
                          已关联 <span className="mono">{r.linked_media_id}</span>
                        </div>
                      )}
                      <form
                        className="adm-toolbar"
                        style={{marginTop: 6}}
                        onSubmit={e => {
                          e.preventDefault();
                          const v = (manual[r.release_id] ?? '').trim();
                          if (v) choose(r, v);
                        }}
                      >
                        <input
                          className="adm-input"
                          aria-label="手动填写媒体 ID"
                          placeholder="手动填媒体 ID，如 video/1/abc-123"
                          value={manual[r.release_id] ?? ''}
                          onChange={e => setManual(m => ({...m, [r.release_id]: e.target.value}))}
                          style={{minWidth: 200}}
                        />
                        <button type="submit" className="adm-link" disabled={!(manual[r.release_id] ?? '').trim() || act.pending !== null}>
                          关联
                        </button>
                      </form>
                    </td>
                    <td>
                      <button type="button" className="adm-link" disabled={act.pending !== null} onClick={() => retry(r)}>
                        {act.pending === `retry-${r.release_id}` ? '…' : '重试'}
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
        <MoreButton next={next} busy={moreBusy} onMore={() => void more()} />
      </section>
    </>
  );
}
