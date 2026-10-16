import {useEffect, useState, type ReactNode} from 'react';
import {Link} from 'react-router';
import {AdminBar, AdminTokenForm} from '../components/AdminChrome';
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
  tokens,
  type EntityType,
  type GenerateStats,
  type IdentityCandidate,
  type MergeResult,
} from '../lib/api';

export const meta = () => [{title: 'REEL · 重复与归并'}];

// AdminDedupe (PRD 11): identity candidates from the nightly scan (or 生成候选),
// side-by-side evidence, then 合并 (choose which side survives) / 不是重复.
// Merges are undoable (POST admin/merges/{event}/undo). The API has no list of
// past merges, so 最近合并 is kept in this browser (localStorage).
//
// The API returns only ids and scoring evidence for a candidate, so the
// compare panel shows what the evidence says about each side, nothing more.
// TODO(tech plan 3.11): entity summaries (title, cover, works) by id for the compare panel.
// TODO(PRD 11): 合并为新片源 vs 合并为其他版本 for videos (the backend has one merge).

const RECENT_KEY = 'admin_recent_merges';
const ACTOR_KEY = 'admin_actor';
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
    if (cause.status === 401) return '管理员令牌无效或未设置';
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
  const [actor, setActor] = useState('');
  const [reason, setReason] = useState('');
  const [undoId, setUndoId] = useState('');

  useEffect(() => {
    setRecent(readJSON<RecentMerge[]>(RECENT_KEY, []));
    setActor(readJSON<string>(ACTOR_KEY, ''));
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
    if (tokens.admin()) void load(kind);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [kind]);

  const body = () => ({actor: actor.trim() || undefined, reason: reason.trim() || undefined});
  const rememberActor = () => writeJSON(ACTOR_KEY, actor.trim());

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
    rememberActor();
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
    rememberActor();
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

  const undo = async (eventId: number) => {
    if (!Number.isInteger(eventId) || eventId <= 0) {
      setError('请输入合并事件编号');
      return;
    }
    if (!window.confirm(`撤销合并事件 #${eventId}？被合并的条目会恢复，旧链接不再跳转。`)) return;
    setBusy(true);
    setError('');
    setMessage(null);
    rememberActor();
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
    rememberActor();
    try {
      const result = await adminGenerateCandidates(kind, {actor: actor.trim() || undefined});
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
    <div className="admin">
      <AdminBar />
      <main className="admin-main admin-main-wide">
        <p className="eyebrow">ADMIN · DEDUPE</p>
        <h1>重复与归并</h1>
        <p className="muted">
          {kind === 'release' ? '同一部作品的重复条目归到一个作品，播放记录、收藏都转到保留的一方。' : '演员重复会让关注、出演作品和相似演员推荐都失真。'}
          按相似度排序。
        </p>

        <AdminTokenForm busy={busy} onSubmit={() => void load(kind)} submitLabel="加载候选">
          <div className="admin-fields">
            <label className="field-inline">
              <span>操作人</span>
              <input value={actor} onChange={e => setActor(e.target.value)} placeholder="admin" maxLength={100} />
            </label>
          </div>
        </AdminTokenForm>

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
              <p className="muted">{loaded ? '暂无待处理的候选。可以点「生成候选」重新扫描。' : '请先输入管理员令牌。'}</p>
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
      </main>
    </div>
  );
}

function Compare({candidate, noun, busy, reason, onReason, onMerge, onNotDuplicate, onSkip}: {
  candidate: IdentityCandidate;
  noun: string;
  busy: boolean;
  reason: string;
  onReason: (value: string) => void;
  onMerge: (targetId: number) => void;
  onNotDuplicate: () => void;
  onSkip: () => void;
}) {
  const evidence = parseEvidence(candidate.evidence);
  const facts = factsFrom(evidence);
  const sides = [
    {label: 'A', id: candidate.left_id, pick: (f: Fact) => f.a},
    {label: 'B', id: candidate.right_id, pick: (f: Fact) => f.b},
  ];
  const maxWeight = Math.max(0.6, ...(evidence?.rules ?? []).map(r => Math.abs(r.weight)));

  return (
    <div className="compare">
      <div className="review-heading">
        <h2>
          候选 #{candidate.id} · 相似度 <span className={`pair-score score-${scoreTone(candidate.score)}`}>{candidate.score.toFixed(2)}</span>
        </h2>
        <span className="review-tag">{candidate.status}</span>
      </div>

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
        <span>备注（可选）</span>
        <textarea value={reason} onChange={e => onReason(e.target.value)} rows={2} maxLength={2000} placeholder="例如：同一番号，标题含画质词" />
      </label>

      <div className="action-row action-row-wrap">
        <button type="button" className="btn btn-primary" disabled={busy} onClick={() => onMerge(candidate.left_id)}>
          合并到 A（B 并入 A）
        </button>
        <button type="button" className="btn btn-secondary" disabled={busy} onClick={() => onMerge(candidate.right_id)}>
          合并到 B（A 并入 B）
        </button>
        <button type="button" className="btn btn-secondary" disabled={busy} onClick={onNotDuplicate}>
          {noun === '演员' ? '不是同一人' : '不是重复'}
        </button>
        <button type="button" className="text-btn" onClick={onSkip}>
          稍后
        </button>
      </div>
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
