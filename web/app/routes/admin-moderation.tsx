import {useEffect, useState, type FormEvent} from 'react';
import {Link, useSearchParams} from 'react-router';
import {AdminHeader, AdminNotice, EmptyRow, MoreButton, Pill, fmtTime} from '../components/AdminUI';
import {
  KEYWORD_CATEGORIES,
  addModerationWord,
  adminErrorText,
  deleteModerationWord,
  getModerationOverview,
  getModerationQueue,
  getModerationWords,
  moderate,
  removeRatingFlood,
  resolveReport,
  unmuteUser,
  type ModerationAction,
  type ModerationTarget,
  type QueueKind,
  type QueuedComment,
  type QueuedDanmaku,
  type QueuedReview,
  type RatingFlood,
  type Report,
} from '../lib/adminApi';
import {useAdmin, useAdminAction, useAdminLoad} from '../lib/adminContext';

export const meta = () => [{title: 'REEL · 举报与审核'}];

// AdminOps moderation (PRD 5.5 / 11 / 19, tech plan 3.8): the queue tabs
// (reports by deadline, shadowed danmaku, shadowed reviews, suspected rating
// floods), moderator actions, the keyword list and unmute. Every write is
// recorded with the signed-in admin. Removing a rating flood needs a second
// admin's approval (the API answers 202; /admin/approvals). The backend has no list of current mutes, so 解除禁言 takes a
// user id (from a queue row).

const TABS: {kind: QueueKind; label: string}[] = [
  {kind: 'reports', label: '举报'},
  {kind: 'danmaku', label: '待审弹幕'},
  {kind: 'reviews', label: '待审短评'},
  {kind: 'comments', label: '待审评论'},
  {kind: 'rating_floods', label: '疑似刷分'},
];

const REPORT_TARGET: Record<string, ModerationTarget | undefined> = {danmaku: 'danmaku', review: 'reviews', rating: 'ratings', comment: 'comments'};
const TARGET_LABELS: Record<string, string> = {
  danmaku: '弹幕',
  review: '短评',
  comment: '评论',
  rating: '评分',
  release: '视频',
  performer_info: '演员资料',
  source: '片源',
};
const CATEGORY_LABELS: Record<string, string> = {ad: '广告', abuse: '辱骂', spoiler: '剧透', illegal: '违法', political: '政治', other: '其他'};

type Row = Report | QueuedDanmaku | QueuedReview | QueuedComment | RatingFlood;

function dueText(r: Report) {
  const ms = new Date(r.due_at).getTime() - Date.now();
  if (Number.isNaN(ms)) return r.due_at;
  const h = Math.round(Math.abs(ms) / 3600000);
  if (ms < 0) return `已超时 ${h < 1 ? '<1' : h} 小时`;
  return h < 1 ? '1 小时内到期' : `${h} 小时后到期`;
}

function fmtMs(ms: number) {
  const s = Math.floor(ms / 1000);
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
}

function targetPreview(t: Record<string, unknown> | null | undefined) {
  if (!t) return null;
  const body = t.body ?? t.title ?? t.label ?? t.name;
  return (
    <div className="adm-mod-body">
      {body !== undefined ? String(body) : <span className="mono adm-source-meta">{JSON.stringify(t)}</span>}
      {t.release_slug ? (
        <div className="adm-source-meta">
          <Link to={`/releases/${String(t.release_slug)}`} target="_blank">
            {String(t.release_slug)}
          </Link>
          {t.user_id ? ` · 用户 #${String(t.user_id)}` : ''}
        </div>
      ) : null}
    </div>
  );
}

function ActionBar({target, id, busy, onAct, allowFold = true}: {
  target: ModerationTarget;
  id: number;
  busy: boolean;
  onAct: (target: ModerationTarget, id: number, action: ModerationAction, muteDays?: number) => void;
  allowFold?: boolean;
}) {
  const [days, setDays] = useState(7);
  const ratings = target === 'ratings';
  return (
    <div className="actions">
      <button type="button" className="adm-link" disabled={busy} onClick={() => onAct(target, id, 'approve')}>
        通过
      </button>
      {!ratings && allowFold && (
        <button type="button" className="adm-link" disabled={busy} onClick={() => onAct(target, id, 'fold')}>
          折叠
        </button>
      )}
      <button type="button" className="adm-link danger" disabled={busy} onClick={() => onAct(target, id, 'delete')}>
        删除
      </button>
      {!ratings && (
        <span className="adm-check">
          <button type="button" className="adm-link danger" disabled={busy} onClick={() => onAct(target, id, 'delete_mute', days)}>
            删除并禁言
          </button>
          <select aria-label="禁言天数" value={days} onChange={e => setDays(Number(e.target.value))} className="adm-input" style={{minHeight: 28, padding: '2px 6px'}}>
            {[1, 3, 7, 30, 90, 365].map(d => (
              <option key={d} value={d}>
                {d} 天
              </option>
            ))}
          </select>
        </span>
      )}
    </div>
  );
}

export default function AdminModeration() {
  const [params, setParams] = useSearchParams();
  const kind = (params.get('tab') as QueueKind) || 'reports';
  const {hasToken, epoch} = useAdmin();
  const overview = useAdminLoad(() => getModerationOverview(1));
  const words = useAdminLoad(() => getModerationWords());
  const act = useAdminAction();
  const [status, setStatus] = useState('');
  const [rows, setRows] = useState<Row[]>([]);
  const [next, setNext] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [loaded, setLoaded] = useState(false);
  const [reason, setReason] = useState('');
  const [word, setWord] = useState('');
  const [category, setCategory] = useState('other');
  const [muteUser, setMuteUser] = useState('');
  const [muteReason, setMuteReason] = useState('');

  const load = async (cursor: string | null) => {
    setBusy(true);
    setError('');
    try {
      const page = await getModerationQueue<Row>(kind, {status, cursor, limit: 50});
      setRows(prev => (cursor ? [...prev, ...page.items] : page.items));
      setNext(page.next_cursor);
      setLoaded(true);
    } catch (cause) {
      setError(adminErrorText(cause));
    } finally {
      setBusy(false);
    }
  };

  useEffect(() => {
    if (hasToken) void load(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hasToken, epoch, kind, status]);

  const setTab = (k: QueueKind) => {
    setStatus('');
    setRows([]);
    setLoaded(false);
    setParams(k === 'reports' ? {} : {tab: k}, {replace: true});
  };

  const after = () => {
    overview.reload();
    void load(null);
  };

  const onAct = async (target: ModerationTarget, id: number, action: ModerationAction, muteDays?: number) => {
    const label = {approve: '通过', fold: '折叠', delete: '删除', delete_mute: `删除并禁言 ${muteDays} 天`}[action];
    const ok = await act.attempt(
      `${target}-${id}`,
      () => moderate(target, id, {action, mute_days: action === 'delete_mute' ? muteDays : undefined, reason: reason.trim() || undefined}),
      v => `已${label}（${v.before} → ${v.after}${v.reports_resolved ? `，关闭 ${v.reports_resolved} 个举报` : ''}${v.muted_until ? `，禁言至 ${fmtTime(v.muted_until)}` : ''}）`,
    );
    if (ok) after();
  };

  const onResolve = async (r: Report, s: 'resolved' | 'dismissed') => {
    const ok = await act.attempt(`report-${r.id}`, () => resolveReport(r.id, s, reason.trim()), s === 'resolved' ? '举报已处理' : '举报已驳回');
    if (ok) after();
  };

  const onFlood = async (f: RatingFlood) => {
    const ok = await act.attempt(
      `flood-${f.release_id}-${f.score}-${f.window_start}`,
      () => removeRatingFlood({release_id: f.release_id, score: f.score, window_start: f.window_start, reason: reason.trim() || undefined}),
      v => ('approval_required' in v ? `已提交审批 #${v.approval.id}：另一位管理员在「待审批」中批准后才会删除` : `已移除 ${v.removed} 条评分`),
    );
    if (ok) after();
  };

  const addWord = async (e: FormEvent) => {
    e.preventDefault();
    const ok = await act.attempt('word-add', () => addModerationWord(word.trim(), category), `已添加关键词「${word.trim()}」`);
    if (ok) {
      setWord('');
      words.reload();
    }
  };

  const removeWord = async (w: string) => {
    const ok = await act.attempt(`word-${w}`, () => deleteModerationWord(w), `已删除关键词「${w}」`);
    if (ok) words.reload();
  };

  const unmute = async (e: FormEvent) => {
    e.preventDefault();
    const id = Number(muteUser);
    if (!Number.isInteger(id) || id <= 0) {
      act.setError('请输入用户 ID');
      return;
    }
    const ok = await act.attempt('unmute', () => unmuteUser(id, muteReason.trim()), `已解除用户 #${id} 的禁言`);
    if (ok) {
      setMuteUser('');
      setMuteReason('');
    }
  };

  const counts = overview.data?.counts;
  const countOf = (k: QueueKind) =>
    !counts
      ? null
      : k === 'reports'
        ? counts.reports
        : k === 'danmaku'
          ? counts.danmaku
          : k === 'reviews'
            ? counts.reviews
            : k === 'comments'
              ? (counts.comments ?? 0)
              : counts.rating_floods;
  const statusOptions =
    kind === 'reports'
      ? [
          ['', '待处理'],
          ['resolved', '已处理'],
          ['dismissed', '已驳回'],
        ]
      : kind === 'rating_floods'
        ? []
        : [
            ['', '待审（shadow）'],
            ['visible', '可见'],
            ['folded', '已折叠'],
            ['deleted', '已删除'],
          ];

  return (
    <>
      <AdminHeader
        eyebrow="ADMIN · MODERATION"
        title="举报与审核"
        sub="举报按处理时限排序；关键词命中的弹幕、短评和评论先对他人隐藏，等这里审核。"
      />
      <AdminNotice error={act.error || overview.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />

      <div className="adm-tabs" role="tablist" aria-label="审核队列">
        {TABS.map(t => {
          const n = countOf(t.kind);
          return (
            <button key={t.kind} type="button" role="tab" className="adm-tab" aria-selected={kind === t.kind} onClick={() => setTab(t.kind)}>
              {t.label}
              {n !== null && n > 0 && (
                <span className={`adm-badge ${t.kind === 'reports' && counts?.reports_overdue ? 'adm-badge-red' : 'adm-badge-amber'}`}>{n}</span>
              )}
            </button>
          );
        })}
      </div>

      <section className="adm-card">
        <div className="adm-toolbar">
          {statusOptions.length > 0 && (
            <label className="adm-field">
              状态
              <select value={status} onChange={e => setStatus(e.target.value)}>
                {statusOptions.map(([v, t]) => (
                  <option key={v} value={v}>
                    {t}
                  </option>
                ))}
              </select>
            </label>
          )}
          <label className="adm-field" style={{flex: 1, minWidth: 260}}>
            处理理由（写入审核记录，可选）
            <input value={reason} onChange={e => setReason(e.target.value)} maxLength={2000} placeholder="例如 广告引流" />
          </label>
          {kind === 'reports' && counts && counts.reports_overdue > 0 && <Pill tone="red">超时 {counts.reports_overdue}</Pill>}
        </div>
        <AdminNotice error={error} />

        <div className="adm-table-wrap">
          {kind === 'reports' && (
            <table className="adm-table">
              <thead>
                <tr>
                  <th>时限</th>
                  <th>对象</th>
                  <th>内容</th>
                  <th>理由</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                {loaded && rows.length === 0 && <EmptyRow cols={5}>没有举报</EmptyRow>}
                {(rows as Report[]).map(r => {
                  const target = REPORT_TARGET[r.target_type];
                  const open = r.status === 'open';
                  return (
                    <tr key={r.id} className={r.overdue && open ? 'flag-red' : ''}>
                      <td>
                        <span className={`adm-due ${r.overdue && open ? 'overdue' : ''}`}>{open ? dueText(r) : r.status}</span>
                        <div className="adm-source-meta">{fmtTime(r.due_at)}</div>
                      </td>
                      <td>
                        {TARGET_LABELS[r.target_type] ?? r.target_type} <span className="muted mono">#{r.target_id}</span>
                        {r.target_type === 'release' && (
                          <div>
                            <Link className="adm-link" to={`/admin/videos/${r.target_id}`}>
                              打开视频
                            </Link>
                          </div>
                        )}
                      </td>
                      <td className="wrap">{targetPreview(r.target)}</td>
                      <td className="wrap">
                        {r.reason}
                        {r.note && <div className="adm-source-meta">{r.note}</div>}
                        {r.resolution && <div className="adm-source-meta">处理：{r.resolution}</div>}
                      </td>
                      <td>
                        {open && target ? (
                          <ActionBar target={target} id={r.target_id} busy={act.pending === `${target}-${r.target_id}`} onAct={onAct} />
                        ) : null}
                        {open && (
                          <div className="actions" style={{marginTop: 6}}>
                            {!target && (
                              <button type="button" className="adm-link" disabled={act.pending === `report-${r.id}`} onClick={() => void onResolve(r, 'resolved')}>
                                标为已处理
                              </button>
                            )}
                            <button type="button" className="adm-link" disabled={act.pending === `report-${r.id}`} onClick={() => void onResolve(r, 'dismissed')}>
                              驳回举报
                            </button>
                          </div>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}

          {kind === 'danmaku' && (
            <table className="adm-table">
              <thead>
                <tr>
                  <th>弹幕</th>
                  <th>位置</th>
                  <th>用户</th>
                  <th>举报</th>
                  <th>时间</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                {loaded && rows.length === 0 && <EmptyRow cols={6}>队列为空</EmptyRow>}
                {(rows as QueuedDanmaku[]).map(d => (
                  <tr key={d.id}>
                    <td className="wrap adm-mod-body">{d.body}</td>
                    <td>
                      <Link to={`/releases/${d.release_slug}`} target="_blank">
                        {d.release_slug}
                      </Link>
                      <div className="adm-source-meta mono">
                        {fmtMs(d.t_ms)} · {d.version_key}
                        {d.peak !== null ? ` · 热度 ${d.peak}` : ''}
                      </div>
                    </td>
                    <td className="mono">
                      <button type="button" className="adm-link" title="填入解除禁言" onClick={() => setMuteUser(String(d.user_id))}>
                        #{d.user_id}
                      </button>
                    </td>
                    <td>{d.open_reports ? <Pill tone="red">{d.open_reports}</Pill> : '—'}</td>
                    <td>{fmtTime(d.created_at)}</td>
                    <td>
                      <ActionBar target="danmaku" id={d.id} busy={act.pending === `danmaku-${d.id}`} onAct={onAct} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}

          {kind === 'reviews' && (
            <table className="adm-table">
              <thead>
                <tr>
                  <th>短评</th>
                  <th>视频</th>
                  <th>用户</th>
                  <th>举报 / 赞</th>
                  <th>时间</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                {loaded && rows.length === 0 && <EmptyRow cols={6}>队列为空</EmptyRow>}
                {(rows as QueuedReview[]).map(r => (
                  <tr key={r.id}>
                    <td className="wrap adm-mod-body">
                      {r.spoiler && <Pill tone="amber">剧透</Pill>} {r.body}
                    </td>
                    <td>
                      <Link to={`/releases/${r.release_slug}`} target="_blank">
                        {r.release_slug}
                      </Link>
                    </td>
                    <td className="mono">
                      <button type="button" className="adm-link" title="填入解除禁言" onClick={() => setMuteUser(String(r.user_id))}>
                        #{r.user_id}
                      </button>
                    </td>
                    <td>
                      {r.open_reports ? <Pill tone="red">{r.open_reports}</Pill> : '—'} / {r.likes}
                    </td>
                    <td>{fmtTime(r.created_at)}</td>
                    <td>
                      <ActionBar target="reviews" id={r.id} busy={act.pending === `reviews-${r.id}`} onAct={onAct} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}

          {kind === 'comments' && (
            <table className="adm-table">
              <thead>
                <tr>
                  <th>评论</th>
                  <th>视频</th>
                  <th>用户</th>
                  <th>举报 / 赞</th>
                  <th>时间</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                {loaded && rows.length === 0 && <EmptyRow cols={6}>队列为空</EmptyRow>}
                {(rows as QueuedComment[]).map(c => (
                  <tr key={c.id}>
                    <td className="wrap adm-mod-body">
                      {c.parent_id !== null && <Pill>回复 #{c.parent_id}</Pill>} {c.spoiler && <Pill tone="amber">剧透</Pill>} {c.body}
                    </td>
                    <td>
                      <Link to={`/releases/${c.release_slug}`} target="_blank">
                        {c.release_slug}
                      </Link>
                    </td>
                    <td className="mono">
                      <button type="button" className="adm-link" title="填入解除禁言" onClick={() => setMuteUser(String(c.user_id))}>
                        #{c.user_id}
                      </button>
                    </td>
                    <td>
                      {c.open_reports ? <Pill tone="red">{c.open_reports}</Pill> : '—'} / {c.likes}
                    </td>
                    <td>{fmtTime(c.created_at)}</td>
                    <td>
                      <ActionBar target="comments" id={c.id} busy={act.pending === `comments-${c.id}`} onAct={onAct} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}

          {kind === 'rating_floods' && (
            <table className="adm-table">
              <thead>
                <tr>
                  <th>视频</th>
                  <th className="num">分数</th>
                  <th>时间窗</th>
                  <th className="num">低信任票</th>
                  <th className="num">匿名</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                {loaded && rows.length === 0 && <EmptyRow cols={6}>没有疑似刷分</EmptyRow>}
                {(rows as RatingFlood[]).map(f => (
                  <tr key={`${f.release_id}-${f.score}-${f.window_start}`}>
                    <td>
                      <Link to={`/admin/videos/${f.release_id}`}>{f.release_slug}</Link>
                    </td>
                    <td className="num">{f.score} 星</td>
                    <td>
                      {fmtTime(f.window_start)} – {fmtTime(f.window_end)}
                    </td>
                    <td className="num">{f.votes}</td>
                    <td className="num">{f.anonymous}</td>
                    <td>
                      <button
                        type="button"
                        className="adm-link danger"
                        disabled={act.pending === `flood-${f.release_id}-${f.score}-${f.window_start}`}
                        onClick={() => void onFlood(f)}
                      >
                        移除这批评分
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
        <MoreButton next={next} busy={busy} onMore={() => void load(next)} />
      </section>

      <div className="adm-grid-2">
        <section className="adm-card">
          <div className="adm-card-head">
            <h2>关键词（{words.data?.length ?? 0}）</h2>
          </div>
          <p className="adm-field-hint">命中的弹幕、短评和评论先只对发布者可见，进入上面的待审队列。</p>
          <AdminNotice error={words.error} />
          <div className="adm-words">
            {words.data?.length === 0 && <span className="muted">还没有关键词</span>}
            {words.data?.map(w => (
              <span key={w.word} className="adm-word">
                {w.word}
                <span className="muted">{CATEGORY_LABELS[w.category] ?? w.category}</span>
                <button type="button" aria-label={`删除 ${w.word}`} disabled={act.pending === `word-${w.word}`} onClick={() => void removeWord(w.word)}>
                  ×
                </button>
              </span>
            ))}
          </div>
          <form className="adm-toolbar" onSubmit={addWord}>
            <input required value={word} onChange={e => setWord(e.target.value)} placeholder="关键词" maxLength={64} aria-label="关键词" />
            <select value={category} onChange={e => setCategory(e.target.value)} aria-label="类别">
              {KEYWORD_CATEGORIES.map(c => (
                <option key={c} value={c}>
                  {CATEGORY_LABELS[c]}
                </option>
              ))}
            </select>
            <button type="submit" className="btn btn-secondary btn-sm" disabled={act.pending === 'word-add'}>
              添加
            </button>
          </form>
        </section>

        <section className="adm-card">
          <h2>解除禁言</h2>
          <p className="adm-field-hint">后端暂无禁言名单接口：从队列里点用户 ID 填入，或直接输入。</p>
          <form className="adm-toolbar" onSubmit={unmute}>
            <label className="adm-field">
              用户 ID
              <input required inputMode="numeric" value={muteUser} onChange={e => setMuteUser(e.target.value.replace(/\D/g, ''))} />
            </label>
            <label className="adm-field" style={{flex: 1, minWidth: 200}}>
              理由
              <input value={muteReason} onChange={e => setMuteReason(e.target.value)} maxLength={500} />
            </label>
            <button type="submit" className="btn btn-secondary btn-sm" disabled={act.pending === 'unmute'}>
              解除禁言
            </button>
          </form>
        </section>
      </div>
    </>
  );
}
