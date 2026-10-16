import {useEffect, useState, type ReactNode} from 'react';
import {Link} from 'react-router';
import {Icon} from '../components/Icon';
import {ErrorBanner} from '../components/States';
import {
  ApiError,
  adminGenerateCandidates,
  adminIdentityCandidates,
  adminMergeCandidate,
  adminNotDuplicate,
  adminUndoMerge,
  isMissingEndpoint,
  type EntityType,
  type GenerateStats,
  type IdentityCandidate,
  type MergeResult,
} from '../lib/api';
import {useAdmin, useAdminLoad} from '../lib/adminContext';
import {adminErrorText} from '../lib/adminApi';
import {CatalogImage} from '../components/Cards';
import {getCandidateCompare, linkCandidateVersions, unlinkReleaseVersion, type EntitySummary} from '../lib/adminDedupeApi';
import '../styles/admin-data.css';

export const meta = () => [{title: 'REEL · 重复与归并'}];

// AdminDedupe (PRD 11): identity candidates from the nightly scan (or 生成候选),
// side-by-side evidence, then 合并 (choose which side survives) / 不是重复.
// Merges are undoable (POST admin/merges/{event}/undo). The API has no list of
// past merges, so 最近合并 is kept in this browser (localStorage).
//
// The compare panel loads both sides' summaries (GET
// admin/identity-candidates/{id}/compare: codes, dates, maker, performers,
// catalog sources, playback links, cover) side by side and highlights the
// fields that differ; the scoring evidence stays below. Videos get the three
// decisions of the artboard: 新片源 (same work: merge, the other side's
// sources become sources of the kept one), 其他版本 (link the two as versions
// of one work without merging; shown on the release page) and 不是重复.
// Every decision writes a review_events row.

const RECENT_KEY = 'admin_recent_merges';
const RECENT_MAX = 20;

type RecentMerge = MergeResult & {at: string};
type Rule = {rule: string; weight: number; detail?: unknown};
type Evidence = {score?: number; rules: Rule[]};

const KINDS: {value: EntityType; label: string; noun: string}[] = [
  {value: 'release', label: '视频', noun: '作品'},
  {value: 'performer', label: '演员', noun: '演员'},
];

const RULE_LABELS: Record<string, string> = {
  name_match: '名字相同',
  name_reordered: '名字顺序不同',
  birth_date_match: '生日相同',
  birth_date_differ: '生日不同',
  shared_releases: '共同作品',
  code_match: '番号相同',
  code_loose_match: '番号相近（去掉厂牌前缀）',
  code_differ: '番号不同',
  title_similarity: '标题相似度',
  runtime_match: '时长一致',
  runtime_differ: '时长不同',
  shared_performers: '共同演员',
};

function readJSON<T>(key: string, fallback: T): T {
  try {
    const raw = window.localStorage.getItem(key);
    return raw ? (JSON.parse(raw) as T) : fallback;
  } catch {
    return fallback;
  }
}

function writeJSON(key: string, value: unknown) {
  try {
    window.localStorage.setItem(key, JSON.stringify(value));
  } catch {
    /* storage unavailable; the list just won't persist */
  }
}

function parseEvidence(raw: unknown): Evidence | null {
  let value = raw;
  if (typeof value === 'string') {
    try {
      value = JSON.parse(value);
    } catch {
      return null;
    }
  }
  if (!value || typeof value !== 'object' || !Array.isArray((value as {rules?: unknown}).rules)) return null;
  const v = value as {score?: unknown; rules: unknown[]};
  const rules = v.rules
    .filter((r): r is Record<string, unknown> => !!r && typeof r === 'object' && typeof (r as {rule?: unknown}).rule === 'string')
    .map(r => ({rule: String(r.rule), weight: typeof r.weight === 'number' ? r.weight : 0, detail: r.detail}));
  return {score: typeof v.score === 'number' ? v.score : undefined, rules};
}

function detailText(detail: unknown): string {
  if (detail === null || detail === undefined) return '';
  if (Array.isArray(detail)) return detail.map(String).join(' · ');
  if (typeof detail === 'number') return Number.isInteger(detail) ? String(detail) : detail.toFixed(2);
  if (typeof detail === 'object') return JSON.stringify(detail);
  return String(detail);
}

type Fact = {k: string; a: string; b: string; tone: 'same' | 'differ'};

/** Per-side facts that the evidence actually states. */
function factsFrom(evidence: Evidence | null): Fact[] {
  const facts: Fact[] = [];
  for (const r of evidence?.rules ?? []) {
    const pair = Array.isArray(r.detail) && r.detail.length === 2 ? r.detail.map(String) : null;
    switch (r.rule) {
      case 'name_match':
      case 'name_reordered':
        facts.push({k: '名字', a: detailText(r.detail), b: detailText(r.detail), tone: 'same'});
        break;
      case 'birth_date_match':
        facts.push({k: '生日', a: detailText(r.detail), b: detailText(r.detail), tone: 'same'});
        break;
      case 'birth_date_differ':
        if (pair) facts.push({k: '生日', a: pair[0], b: pair[1], tone: 'differ'});
        break;
      case 'code_match':
      case 'code_loose_match':
        facts.push({k: '番号', a: detailText(r.detail), b: detailText(r.detail), tone: 'same'});
        break;
      case 'runtime_match':
      case 'runtime_differ':
        if (pair) facts.push({k: '时长', a: `${pair[0]} 分钟`, b: `${pair[1]} 分钟`, tone: r.rule === 'runtime_match' ? 'same' : 'differ'});
        break;
      case 'shared_releases':
        facts.push({k: '共同作品', a: `${detailText(r.detail)} 部`, b: `${detailText(r.detail)} 部`, tone: 'same'});
        break;
      case 'shared_performers':
        facts.push({k: '共同演员', a: `${detailText(r.detail)} 位`, b: `${detailText(r.detail)} 位`, tone: 'same'});
        break;
    }
  }
  return facts;
}

const scoreTone = (score: number) => (score >= 0.9 ? 'high' : score >= 0.7 ? 'mid' : 'low');

function failureText(cause: unknown, action: string): string {
  if (isMissingEndpoint(cause)) return `后端还没有部署${action}接口（identity-merge），暂时只能查看候选。`;
  if (cause instanceof ApiError) {
    if (cause.status === 401 || cause.status === 403) return adminErrorText(cause);
    return cause.detail ? `${cause.message}：${cause.detail}` : cause.message;
  }
  return cause instanceof Error ? cause.message : String(cause);
}

function entityHref(type: string, slug: string) {
  return type === 'performer' ? `/performers/${encodeURIComponent(slug)}` : `/releases/${encodeURIComponent(slug)}`;
}

export default function AdminDedupe() {
  const [kind, setKind] = useState<EntityType>('release');
  const [candidates, setCandidates] = useState<IdentityCandidate[]>([]);
  const [selected, setSelected] = useState<number | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [message, setMessage] = useState<ReactNode>(null);
  const [stats, setStats] = useState<Record<string, GenerateStats> | null>(null);
  const [recent, setRecent] = useState<RecentMerge[]>([]);
  const {hasToken, epoch, me} = useAdmin();
  // The actor is the signed-in admin (the API ignores any actor in the body).
  const actor = me?.actor ?? '';
  const [reason, setReason] = useState('');
  const [undoId, setUndoId] = useState('');

  useEffect(() => {
    setRecent(readJSON<RecentMerge[]>(RECENT_KEY, []));
  }, []);

  const load = async (type: EntityType = kind) => {
    setError('');
    setBusy(true);
    try {
      const list = await adminIdentityCandidates(100, type);
      // Older backends ignore entity_type; filter here too.
      const mine = list.filter(c => !c.entity_type || c.entity_type === type);
      setCandidates(mine);
      setSelected(prev => (prev !== null && mine.some(c => c.id === prev) ? prev : mine[0]?.id ?? null));
      setLoaded(true);
    } catch (cause) {
      setError(failureText(cause, '候选'));
    } finally {
      setBusy(false);
    }
  };

  useEffect(() => {
    if (hasToken) void load(kind);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [kind, hasToken, epoch]);

  const body = () => ({reason: reason.trim() || undefined});

  const dropCandidate = (id: number) => {
    const index = candidates.findIndex(c => c.id === id);
    const next = candidates.filter(c => c.id !== id);
    setCandidates(next);
    setSelected(next[Math.min(Math.max(index, 0), next.length - 1)]?.id ?? null);
    setReason('');
  };

  const pushRecent = (result: MergeResult) => {
    setRecent(list => {
      const next = [{...result, at: new Date().toISOString()}, ...list.filter(r => r.review_event_id !== result.review_event_id)].slice(0, RECENT_MAX);
      writeJSON(RECENT_KEY, next);
      return next;
    });
  };

  const dropRecent = (eventId: number) => {
    setRecent(list => {
      const next = list.filter(r => r.review_event_id !== eventId);
      writeJSON(RECENT_KEY, next);
      return next;
    });
  };

  const merge = async (candidate: IdentityCandidate, targetId: number) => {
    const sourceId = targetId === candidate.left_id ? candidate.right_id : candidate.left_id;
    const noun = KINDS.find(k => k.value === candidate.entity_type)?.noun ?? '条目';
    if (!window.confirm(`把${noun} #${sourceId} 合并到 #${targetId}？\n#${sourceId} 的关联会转到 #${targetId}，旧链接 301 跳转。可以在「最近合并」里撤销。`)) return;
    setBusy(true);
    setError('');
    setMessage(null);
    try {
      const result = await adminMergeCandidate(candidate.id, targetId, body());
      pushRecent(result);
      dropCandidate(candidate.id);
      setMessage(
        <>
          已合并：<code>{result.source_slug}</code> → <Link to={entityHref(result.entity_type, result.target_slug)}>{result.target_slug}</Link>
          （事件 #{result.review_event_id}），旧链接会 301 跳转。
        </>,
      );
    } catch (cause) {
      setError(failureText(cause, '合并'));
    } finally {
      setBusy(false);
    }
  };

  const notDuplicate = async (candidate: IdentityCandidate) => {
    setBusy(true);
    setError('');
    setMessage(null);
    try {
      const result = await adminNotDuplicate(candidate.id, body());
      dropCandidate(candidate.id);
      setMessage(`已标记 #${candidate.left_id} 和 #${candidate.right_id} 不是重复（事件 #${result.review_event_id}），以后不再提示这一对。`);
    } catch (cause) {
      setError(failureText(cause, '标记'));
    } finally {
      setBusy(false);
    }
  };

  const linkVersions = async (candidate: IdentityCandidate) => {
    if (!window.confirm(`把 #${candidate.left_id} 和 #${candidate.right_id} 关联为同一作品的其他版本？\n两条都保留，作品页互相显示「其他版本」。`)) return;
    setBusy(true);
    setError('');
    setMessage(null);
    try {
      const result = await linkCandidateVersions(candidate.id, reason.trim() || undefined);
      dropCandidate(candidate.id);
      setMessage(`已关联为其他版本：#${candidate.left_id} ↔ #${candidate.right_id}（事件 #${result.review_event_id}）。`);
    } catch (cause) {
      setError(failureText(cause, '关联版本'));
    } finally {
      setBusy(false);
    }
  };

  const undo = async (eventId: number) => {
    if (!Number.isInteger(eventId) || eventId <= 0) {
      setError('请输入合并事件编号');
      return;
    }
    if (!window.confirm(`撤销合并事件 #${eventId}？被合并的条目会恢复，旧链接不再跳转。`)) return;
    setBusy(true);
    setError('');
    setMessage(null);
    try {
      const result = await adminUndoMerge(eventId, body());
      dropRecent(eventId);
      setUndoId('');
      setMessage(
        <>
          已撤销合并 #{result.reverted_event_id}：恢复 <Link to={entityHref(result.entity_type, result.restored_slug)}>{result.restored_slug}</Link>
          （事件 #{result.review_event_id}）。
        </>,
      );
      if (result.entity_type === kind) void load(kind);
    } catch (cause) {
      setError(failureText(cause, '撤销'));
    } finally {
      setBusy(false);
    }
  };

  const generate = async () => {
    setBusy(true);
    setError('');
    setMessage(null);
    try {
      const result = await adminGenerateCandidates(kind, {});
      setStats(result ?? {});
      await load(kind);
    } catch (cause) {
      setError(failureText(cause, '生成候选'));
    } finally {
      setBusy(false);
    }
  };

  const current = candidates.find(c => c.id === selected) ?? null;
  const kindInfo = KINDS.find(k => k.value === kind)!;

  return (
    <div className="admin-main admin-main-wide">
        <p className="eyebrow">ADMIN · DEDUPE</p>
        <h1>重复与归并</h1>
        <p className="muted">
          {kind === 'release' ? '同一部作品的重复条目归到一个作品，播放记录、收藏都转到保留的一方。' : '演员重复会让关注、出演作品和相似演员推荐都失真。'}
          按相似度排序。
        </p>

        <div className="admin-fields">
          <button type="button" className="btn btn-secondary btn-sm" disabled={busy} onClick={() => void load(kind)}>
            {busy ? '加载中…' : '刷新候选'}
          </button>
          <span className="muted">操作人：{actor}</span>
        </div>

        {error && <ErrorBanner error={error} onRetry={() => void load(kind)} />}
        {message && (
          <div className="banner banner-ok" role="status">
            <Icon name="check" size={18} />
            <span className="banner-text">{message}</span>
          </div>
        )}

        <div className="dedupe-toolbar">
          <div className="segmented segmented-sm" role="tablist" aria-label="类型">
            {KINDS.map(k => (
              <button
                key={k.value}
                type="button"
                role="tab"
                aria-selected={k.value === kind}
                className="segmented-item"
                onClick={() => {
                  setKind(k.value);
                  setStats(null);
                  setMessage(null);
                }}
              >
                {k.label}
                {k.value === kind && loaded ? ` ${candidates.length}` : ''}
              </button>
            ))}
          </div>
          <span className="section-spacer" />
          <button type="button" className="btn btn-secondary" disabled={busy} onClick={() => void generate()}>
            生成{kindInfo.label}候选
          </button>
        </div>

        {stats && <GenerateSummary stats={stats} />}

        <div className="dedupe">
          <section className="admin-card dedupe-list" aria-label="候选列表">
            <h2>待处理（{candidates.length}）</h2>
            {candidates.length === 0 ? (
              <p className="muted">{loaded ? '暂无待处理的候选。可以点「生成候选」重新扫描。' : busy ? '加载中…' : ''}</p>
            ) : (
              <div className="pair-list" role="listbox" aria-label="候选">
                {candidates.map(c => {
                  const ev = parseEvidence(c.evidence);
                  return (
                    <button
                      key={c.id}
                      type="button"
                      role="option"
                      aria-selected={c.id === selected}
                      className="pair-item"
                      onClick={() => setSelected(c.id)}
                    >
                      <span className="pair-head">
                        <span>
                          #{c.left_id} ↔ #{c.right_id}
                        </span>
                        <span className={`pair-score score-${scoreTone(c.score)}`}>{c.score.toFixed(2)}</span>
                      </span>
                      <span className="pair-hint">{ev ? ev.rules.map(r => RULE_LABELS[r.rule] ?? r.rule).join(' · ') || '无相似信号' : `候选 #${c.id}`}</span>
                    </button>
                  );
                })}
              </div>
            )}
          </section>

          <section className="admin-card dedupe-detail" aria-label="对比">
            {current ? (
              <Compare
                candidate={current}
                noun={kindInfo.noun}
                busy={busy}
                reason={reason}
                onReason={setReason}
                onMerge={target => void merge(current, target)}
                onNotDuplicate={() => void notDuplicate(current)}
                onLinkVersions={() => void linkVersions(current)}
                onSkip={() => {
                  const index = candidates.findIndex(c => c.id === current.id);
                  setSelected(candidates[(index + 1) % candidates.length]?.id ?? null);
                }}
              />
            ) : (
              <p className="muted">选择左侧的一对候选查看对比。</p>
            )}
          </section>
        </div>

        <section className="admin-card">
          <h2>最近合并</h2>
          <p className="muted">在这台设备上执行的合并。撤销后被合并的条目恢复，旧链接不再跳转。</p>
          {recent.length === 0 ? (
            <p className="muted">还没有合并记录。</p>
          ) : (
            <div className="review-list">
              {recent.map(r => (
                <article key={r.review_event_id} className="review-item recent-merge">
                  <div className="review-heading">
                    <strong>
                      事件 #{r.review_event_id} · {KINDS.find(k => k.value === r.entity_type)?.noun ?? r.entity_type}
                    </strong>
                    <span className="review-tag">{new Date(r.at).toLocaleString('zh-CN')}</span>
                  </div>
                  <p>
                    <code>{r.source_slug}</code>（#{r.source_id}）→ <Link to={entityHref(r.entity_type, r.target_slug)}>{r.target_slug}</Link>（#{r.target_id}）
                  </p>
                  <div className="action-row">
                    <button type="button" className="btn btn-secondary btn-sm" disabled={busy} onClick={() => void undo(r.review_event_id)}>
                      <Icon name="undo" size={16} />
                      撤销合并
                    </button>
                    <button type="button" className="text-btn" onClick={() => dropRecent(r.review_event_id)}>
                      从列表移除
                    </button>
                  </div>
                </article>
              ))}
            </div>
          )}
          <form
            className="admin-token"
            onSubmit={event => {
              event.preventDefault();
              void undo(Number(undoId));
            }}
          >
            <input inputMode="numeric" value={undoId} onChange={e => setUndoId(e.target.value.replace(/\D/g, ''))} placeholder="合并事件编号" aria-label="合并事件编号" />
            <button type="submit" className="btn btn-secondary" disabled={busy || !undoId}>
              按编号撤销
            </button>
          </form>
        </section>
    </div>
  );
}

type SummaryRow = {k: string; a: ReactNode; b: ReactNode; same: boolean | null};

const day = (v?: string | null) => (v ? v.slice(0, 10) : '');
const norm = (v: string) => v.trim().toLowerCase().replace(/\s+/g, ' ');

/** One compare row; same is null when either side is empty (nothing to compare). */
function textRow(k: string, a: string | null | undefined, b: string | null | undefined): SummaryRow {
  const x = a ?? '';
  const y = b ?? '';
  return {k, a: x || '—', b: y || '—', same: x && y ? norm(x) === norm(y) : x || y ? false : null};
}

function listRow(k: string, a: string[] | undefined, b: string[] | undefined): SummaryRow {
  const x = a ?? [];
  const y = b ?? [];
  const key = (l: string[]) => Array.from(new Set(l.map(norm))).sort().join('|');
  return {k, a: x.join(' · ') || '—', b: y.join(' · ') || '—', same: x.length && y.length ? key(x) === key(y) : x.length || y.length ? false : null};
}

function linkList(items: {href?: string; text: string; sub?: string}[]): ReactNode {
  if (items.length === 0) return '—';
  return (
    <ul className="compare-links">
      {items.map((it, i) => (
        <li key={i}>
          {it.href ? (
            <a href={it.href} target="_blank" rel="noopener noreferrer">
              {it.text}
            </a>
          ) : (
            it.text
          )}
          {it.sub && <span className="muted"> · {it.sub}</span>}
        </li>
      ))}
    </ul>
  );
}

function summaryRows(a: EntitySummary, b: EntitySummary): SummaryRow[] {
  const rows: SummaryRow[] = [];
  const cover = (e: EntitySummary) => (e.cover ? <CatalogImage image={e.cover} alt={e.title} className="compare-cover" sizes="160px" /> : '—');
  rows.push({k: e2(a) ? '封面' : '照片', a: cover(a), b: cover(b), same: null});
  rows.push(textRow(e2(a) ? '标题' : '名字', a.title, b.title));
  rows.push(textRow(e2(a) ? '日文标题' : '日文名', a.title_ja, b.title_ja));
  if (e2(a)) {
    rows.push(listRow('番号', a.codes, b.codes));
    rows.push(textRow('发布日期', day(a.release_date), day(b.release_date)));
    rows.push(textRow('时长', a.runtime_minutes ? `${a.runtime_minutes} 分钟` : '', b.runtime_minutes ? `${b.runtime_minutes} 分钟` : ''));
    rows.push(textRow('片商', a.maker, b.maker));
    rows.push(textRow('厂牌', a.label, b.label));
    rows.push(textRow('系列', a.series, b.series));
    rows.push(textRow('版本类型', a.edition_type, b.edition_type));
    rows.push(listRow('演员', a.performers, b.performers));
    const play = (e: EntitySummary) =>
      linkList((e.playback ?? []).map(p => ({href: p.url || undefined, text: `${p.kind === 'playback_source' ? '片源' : '映射'} ${p.provider} · ${p.label}`, sub: p.status})));
    rows.push({k: '播放链接', a: play(a), b: play(b), same: null});
  } else {
    rows.push(textRow('生日', day(a.birth_date), day(b.birth_date)));
    rows.push(textRow('作品数', a.works?.toString(), b.works?.toString()));
    rows.push(listRow('别名', a.aliases, b.aliases));
  }
  const src = (e: EntitySummary) => linkList(e.sources.map(x => ({href: x.url, text: x.source})));
  rows.push({k: '目录来源', a: src(a), b: src(b), same: listRow('', a.sources.map(x => x.source), b.sources.map(x => x.source)).same});
  rows.push(textRow('状态', a.merged_into_id ? `已并入 #${a.merged_into_id}` : a.status, b.merged_into_id ? `已并入 #${b.merged_into_id}` : b.status));
  return rows;
}

const e2 = (e: EntitySummary) => e.entity_type === 'release';

function VersionsList({side, onUnlink, busy}: {side: EntitySummary; onUnlink: (other: number) => void; busy: boolean}) {
  const list = side.versions ?? [];
  if (list.length === 0) return <span className="muted">—</span>;
  return (
    <ul className="compare-links">
      {list.map(v => (
        <li key={v.id}>
          <Link to={`/admin/videos/${v.id}`}>
            #{v.id} {v.title}
          </Link>
          <span className="muted"> · {[v.code, day(v.release_date), v.status].filter(Boolean).join(' · ')}</span>{' '}
          <button type="button" className="text-btn" disabled={busy} onClick={() => onUnlink(v.id)} title="只删除这两条之间的直接关联">
            取消关联
          </button>
        </li>
      ))}
    </ul>
  );
}

function Compare({candidate, noun, busy, reason, onReason, onMerge, onNotDuplicate, onLinkVersions, onSkip}: {
  candidate: IdentityCandidate;
  noun: string;
  busy: boolean;
  reason: string;
  onReason: (value: string) => void;
  onMerge: (targetId: number) => void;
  onNotDuplicate: () => void;
  onLinkVersions: () => void;
  onSkip: () => void;
}) {
  const evidence = parseEvidence(candidate.evidence);
  const facts = factsFrom(evidence);
  const summary = useAdminLoad(signal => getCandidateCompare(candidate.id, signal), [candidate.id]);
  const [unlinkError, setUnlinkError] = useState('');
  const sides = [
    {label: 'A', id: candidate.left_id, pick: (f: Fact) => f.a},
    {label: 'B', id: candidate.right_id, pick: (f: Fact) => f.b},
  ];
  const maxWeight = Math.max(0.6, ...(evidence?.rules ?? []).map(r => Math.abs(r.weight)));
  const cmp = summary.data;
  const rows = cmp ? summaryRows(cmp.left, cmp.right) : [];
  const differ = rows.filter(r => r.same === false).length;
  const isRelease = candidate.entity_type === 'release' || noun === '作品';

  const unlink = async (from: EntitySummary, other: number) => {
    if (!window.confirm(`取消 #${from.id} 与 #${other} 的其他版本关联？`)) return;
    setUnlinkError('');
    try {
      await unlinkReleaseVersion(from.id, other, reason.trim() || undefined);
      summary.reload();
    } catch (cause) {
      setUnlinkError(failureText(cause, '取消关联'));
    }
  };

  return (
    <div className="compare">
      <div className="review-heading">
        <h2>
          候选 #{candidate.id} · 相似度 <span className={`pair-score score-${scoreTone(candidate.score)}`}>{candidate.score.toFixed(2)}</span>
        </h2>
        <span className="review-tag">{cmp ? `${differ} 项不同` : candidate.status}</span>
      </div>

      {cmp ? (
        <div className="adm-table-wrap">
          <table className="adm-table compare-table">
            <thead>
              <tr>
                <th />
                {[cmp.left, cmp.right].map((e, i) => (
                  <th key={e.id}>
                    {noun} {i === 0 ? 'A' : 'B'} ·{' '}
                    <Link to={e2(e) ? `/admin/videos/${e.id}` : `/admin/actors?id=${e.id}`}>#{e.id}</Link>{' '}
                    <Link to={entityHref(e.entity_type, e.slug)} target="_blank" className="muted">
                      前台
                    </Link>
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map(r => (
                <tr key={r.k} className={r.same === false ? 'compare-differ' : r.same ? 'compare-same' : ''}>
                  <th scope="row">
                    {r.k}
                    {r.same === false && <span className="compare-flag">不同</span>}
                  </th>
                  <td>{r.a}</td>
                  <td>{r.b}</td>
                </tr>
              ))}
              {isRelease && (
                <tr>
                  <th scope="row">其他版本</th>
                  <td>
                    <VersionsList side={cmp.left} busy={busy} onUnlink={other => void unlink(cmp.left, other)} />
                  </td>
                  <td>
                    <VersionsList side={cmp.right} busy={busy} onUnlink={other => void unlink(cmp.right, other)} />
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      ) : (
        <>
          {summary.busy && <p className="muted">正在加载两边的资料…</p>}
          {summary.error && <p className="muted">资料摘要不可用（{summary.error}），只显示打分证据。</p>}
          <div className="compare-sides">
            {sides.map(side => (
              <div key={side.label} className="compare-side">
                <span className="compare-tag">
                  {noun} {side.label} · #{side.id}
                </span>
                {facts.length === 0 ? (
                  <p className="muted">证据里没有可对比的字段。</p>
                ) : (
                  <dl className="compare-facts">
                    {facts.map(f => (
                      <div key={f.k} className={`compare-fact tone-${f.tone}`}>
                        <dt>{f.k}</dt>
                        <dd>{side.pick(f) || '—'}</dd>
                      </div>
                    ))}
                  </dl>
                )}
              </div>
            ))}
          </div>
        </>
      )}
      {unlinkError && <p className="adm-notice adm-notice-err">{unlinkError}</p>}

      <div className="signals">
        <h3>相似信号</h3>
        {evidence ? (
          evidence.rules.length === 0 ? (
            <p className="muted">没有命中任何规则。</p>
          ) : (
            evidence.rules.map((r, i) => (
              <div key={`${r.rule}-${i}`} className="signal">
                <span className="signal-name">{RULE_LABELS[r.rule] ?? r.rule}</span>
                <span className="signal-detail">{detailText(r.detail)}</span>
                <span className="signal-bar" aria-hidden="true">
                  <span className={r.weight < 0 ? 'signal-fill signal-neg' : 'signal-fill'} style={{width: `${Math.round((Math.abs(r.weight) / maxWeight) * 100)}%`}} />
                </span>
                <span className="signal-weight">{r.weight > 0 ? `+${r.weight}` : r.weight}</span>
              </div>
            ))
          )
        ) : (
          <pre className="evidence-raw">{JSON.stringify(candidate.evidence, null, 2)}</pre>
        )}
      </div>

      <label className="field-inline field-block">
        <span>备注（可选，写入修改记录）</span>
        <textarea value={reason} onChange={e => onReason(e.target.value)} rows={2} maxLength={2000} placeholder="例如：同一番号，标题含画质词" />
      </label>

      {isRelease ? (
        <div className="dedupe-decisions">
          <div className="dedupe-decision">
            <strong>新片源</strong>
            <span className="muted">同一部作品：合并，另一条的片源、番号、收藏和播放记录转到保留的一条。</span>
            <span className="action-row">
              <button type="button" className="btn btn-primary btn-sm" disabled={busy} onClick={() => onMerge(candidate.left_id)}>
                保留 A（B 并入 A）
              </button>
              <button type="button" className="btn btn-secondary btn-sm" disabled={busy} onClick={() => onMerge(candidate.right_id)}>
                保留 B（A 并入 B）
              </button>
            </span>
          </div>
          <div className="dedupe-decision">
            <strong>其他版本</strong>
            <span className="muted">同一作品的不同版本（复刻、无码、4K 等）：两条都保留，作品页互相显示「其他版本」。</span>
            <span className="action-row">
              <button type="button" className="btn btn-secondary btn-sm" disabled={busy} onClick={onLinkVersions}>
                关联为其他版本
              </button>
            </span>
          </div>
          <div className="dedupe-decision">
            <strong>不是重复</strong>
            <span className="muted">不同的作品：以后不再提示这一对。</span>
            <span className="action-row">
              <button type="button" className="btn btn-secondary btn-sm" disabled={busy} onClick={onNotDuplicate}>
                不是重复
              </button>
              <button type="button" className="text-btn" onClick={onSkip}>
                稍后
              </button>
            </span>
          </div>
        </div>
      ) : (
        <div className="action-row action-row-wrap">
          <button type="button" className="btn btn-primary" disabled={busy} onClick={() => onMerge(candidate.left_id)}>
            合并到 A（B 并入 A）
          </button>
          <button type="button" className="btn btn-secondary" disabled={busy} onClick={() => onMerge(candidate.right_id)}>
            合并到 B（A 并入 B）
          </button>
          <button type="button" className="btn btn-secondary" disabled={busy} onClick={onNotDuplicate}>
            不是同一人
          </button>
          <button type="button" className="text-btn" onClick={onSkip}>
            稍后
          </button>
        </div>
      )}
    </div>
  );
}

function GenerateSummary({stats}: {stats: Record<string, GenerateStats>}) {
  const rows = Object.entries(stats);
  if (rows.length === 0) return <p className="muted">扫描完成。</p>;
  return (
    <div className="banner banner-ok" role="status">
      <Icon name="check" size={18} />
      <span className="banner-text">
        {rows
          .map(([type, s]) => {
            const noun = KINDS.find(k => k.value === type)?.label ?? type;
            const parts = [
              s.compared !== undefined && `比较 ${s.compared} 对`,
              s.inserted !== undefined && `新增 ${s.inserted} 个候选`,
              s.skipped_existing ? `已存在 ${s.skipped_existing}` : '',
              s.skipped_not_duplicate ? `跳过已判不是重复 ${s.skipped_not_duplicate}` : '',
            ].filter(Boolean);
            return `${noun}：${parts.join('，') || '完成'}`;
          })
          .join('；')}
      </span>
    </div>
  );
}
