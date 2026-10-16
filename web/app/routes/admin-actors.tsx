import {useEffect, useMemo, useState, type FormEvent} from 'react';
import {Link, useSearchParams} from 'react-router';
import {AdminHeader, AdminNotice, EmptyRow, MoreButton, NotDeployed, Pill, fmtNum, fmtTime, jsonText, statusTone} from '../components/AdminUI';
import {ApiError, isMissingEndpoint} from '../lib/api';
import {
  ALIAS_TYPES,
  ALIAS_TYPE_LABELS,
  COMPLETENESS_LABELS,
  I18N_LANGS,
  PERFORMER_FILTERS,
  REQUESTER_ROLE_LABELS,
  REQUEST_KIND_LABELS,
  REQUEST_STATUS_LABELS,
  RULE_LABELS,
  STATUS_LABELS,
  addPerformerAlias,
  clearPerformerOverride,
  decidePerformerRequest,
  getAdminPerformer,
  getAdminPerformers,
  getPerformerRequests,
  getSimilarityWeights,
  patchAdminPerformer,
  previewSimilarityWeights,
  putSimilarityWeights,
  removePerformerAlias,
  setPerformerStatus,
  adminErrorText,
  type AdminPerformer,
  type AdminPerformerSummary,
  type AdminPerformerWork,
  type AgeRecheck,
  type PerformerRequest,
  type PreviewMatch,
  type SimilarityPreview,
  type Weights,
} from '../lib/adminApi';
import {useAdmin, useAdminAction, useAdminLoad} from '../lib/adminContext';

export const meta = () => [{title: 'REEL · 演员'}];

// AdminActors (PRD 6, 11, 15; admin-performers backend): performer list with
// completeness filters and counts, the editor (field edits become manual
// overrides with 清除覆盖; birth_date_hidden; aliases; hide / unhide with a
// reason; works with admin-only age at filming; similar performers with
// evidence; duplicate candidates → /admin/dedupe; issues; review_events),
// the 更正与隐藏申请 queue (earliest due first) and the similarity weights
// editor, which must preview current vs proposed top 5 before saving.

const TABS = [
  {key: 'list', label: '演员'},
  {key: 'requests', label: '更正与隐藏申请'},
  {key: 'weights', label: '相似演员 · 权重'},
] as const;
type TabKey = (typeof TABS)[number]['key'];

const EDIT_FIELDS: {key: keyof AdminPerformer; column: string; label: string; kind?: 'date' | 'number' | 'textarea'; hint?: string}[] = [
  {key: 'name', column: 'canonical_name', label: '艺名'},
  {key: 'name_ja', column: 'canonical_name_ja', label: '日文名'},
  {key: 'birth_date', column: 'birth_date', label: '生日', kind: 'date', hint: '改生日会重算该演员全部作品的出演时年龄（R-AGE）'},
  {key: 'birth_place', column: 'birth_place', label: '出生地'},
  {key: 'debut_date', column: 'debut_date', label: '出道日期', kind: 'date'},
  {key: 'height_cm', column: 'height_cm', label: '身高（cm）', kind: 'number'},
  {key: 'cup_size', column: 'cup_size', label: '罩杯'},
  {key: 'shoe_size', column: 'shoe_size', label: '鞋码', kind: 'number'},
  {key: 'biography', column: 'biography', label: '简介', kind: 'textarea'},
];
const FIELD_LABEL: Record<string, string> = {
  ...Object.fromEntries(EDIT_FIELDS.map(f => [f.column, f.label])),
  birth_date_hidden: '隐藏生日',
};
const ROLE_LABELS: Record<string, string> = {lead: '主演', guest: '客串', support: '配角', cameo: '客串'};
const fieldLabel = (f: string | null | undefined) => (f ? (FIELD_LABEL[f] ?? f) : '—');

const missingText = (m: string[] | null) => (m ?? []).map(x => COMPLETENESS_LABELS[x] ?? x).join('、');
const isStale = (cause: unknown) => cause instanceof ApiError && cause.status === 409 && /version|stale|modified/i.test(cause.detail ?? '');

function recheckText(r?: AgeRecheck) {
  if (!r) return '';
  return `；R-AGE 重算 ${r.releases} 部：命中 ${r.flagged}，新暂停 ${r.held}${r.still_held ? `，${r.still_held} 部已通过但仍暂停（需人工解除）` : ''}`;
}

function Meter({value}: {value: number}) {
  const tone = value >= 80 ? '' : value >= 50 ? 'amber' : 'red';
  return (
    <span title={`${value}%`}>
      <span className={`adm-meter ${tone}`}>
        <span style={{width: `${Math.max(0, Math.min(100, value))}%`}} />
      </span>
      <span className="mono">{value}%</span>
    </span>
  );
}

function birthCell(p: {birth_date: string | null; birth_date_hidden: boolean}) {
  if (p.birth_date_hidden) return <Pill tone="purple">已隐藏{p.birth_date ? ` · ${p.birth_date}` : ''}</Pill>;
  if (!p.birth_date) return <Pill tone="red">未知</Pill>;
  return <span className="mono">{p.birth_date}</span>;
}

// ---------------------------------------------------------------- list

function flags(p: AdminPerformerSummary) {
  return (
    <>
      {p.open_requests > 0 && <Pill tone="amber">申请 {p.open_requests}</Pill>}
      {p.duplicate_candidates > 0 && <Pill tone="blue">疑似重复</Pill>}
      {p.open_issues > 0 && <Pill tone="plain">问题 {p.open_issues}</Pill>}
    </>
  );
}

function PerformerList({selected, onSelect, epochKey}: {selected: number; onSelect: (id: number) => void; epochKey: number}) {
  const compact = selected > 0;
  const [params, setParams] = useSearchParams();
  const {hasToken, epoch} = useAdmin();
  const filters = (params.get('filter') ?? '').split(',').filter(Boolean);
  const status = params.get('status') ?? '';
  const q = params.get('q') ?? '';
  const [draft, setDraft] = useState(q);
  const [items, setItems] = useState<AdminPerformerSummary[]>([]);
  const [counts, setCounts] = useState<Record<string, number>>({});
  const [next, setNext] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [missing, setMissing] = useState(false);

  const load = async (cursor: string | null) => {
    setBusy(true);
    setError('');
    try {
      const page = await getAdminPerformers({q, status, filter: filters.join(','), cursor, limit: 50});
      setItems(prev => (cursor ? [...prev, ...page.items] : page.items));
      setNext(page.next_cursor);
      setCounts(page.counts);
    } catch (cause) {
      if (isMissingEndpoint(cause)) setMissing(true);
      else setError(adminErrorText(cause));
    } finally {
      setBusy(false);
    }
  };

  useEffect(() => {
    if (hasToken) void load(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hasToken, epoch, q, status, filters.join(','), epochKey]);

  const set = (k: string, v: string) => {
    const p = new URLSearchParams(params);
    if (v) p.set(k, v);
    else p.delete(k);
    setParams(p, {replace: true});
  };
  const toggle = (f: string) => set('filter', (filters.includes(f) ? filters.filter(x => x !== f) : [...filters, f]).join(','));

  if (missing) return <NotDeployed what="演员管理接口（admin/performers）" />;

  return (
    <section className="adm-card">
      <div className="adm-card-head">
        <h2>
          演员 <span className="muted">{counts.all !== undefined ? `${fmtNum(counts.all)} 位` : ''}</span>
        </h2>
        <form
          className="adm-toolbar"
          onSubmit={e => {
            e.preventDefault();
            set('q', draft.trim());
          }}
        >
          <input value={draft} onChange={e => setDraft(e.target.value)} placeholder="名字 / 日文名 / 别名 / slug" aria-label="查找演员" />
          <select value={status} onChange={e => set('status', e.target.value)} aria-label="状态">
            <option value="">全部状态</option>
            <option value="active">正常</option>
            <option value="hidden">已隐藏</option>
          </select>
          <button type="submit" className="btn btn-secondary btn-sm">
            查找
          </button>
        </form>
      </div>
      <div className="adm-chips" role="group" aria-label="筛选（可多选）">
        <button type="button" className="adm-chip" aria-pressed={filters.length === 0} onClick={() => set('filter', '')}>
          全部
        </button>
        {PERFORMER_FILTERS.map(f => (
          <button key={f.v} type="button" className="adm-chip" aria-pressed={filters.includes(f.v)} onClick={() => toggle(f.v)}>
            {f.t} <span className="mono muted">{fmtNum(counts[f.v] ?? 0)}</span>
          </button>
        ))}
      </div>
      <AdminNotice error={error} />
      <div className="adm-table-wrap">
        <table className="adm-table">
          <thead>
            <tr>
              <th>演员</th>
              <th className="num">作品</th>
              <th>完整度</th>
              {!compact && <th>生日</th>}
              {!compact && <th>待办</th>}
            </tr>
          </thead>
          <tbody>
            {!busy && items.length === 0 && <EmptyRow cols={compact ? 3 : 5}>没有符合条件的演员</EmptyRow>}
            {items.map(p => (
              <tr key={p.id} className={`clickable ${selected === p.id ? 'sel' : ''}`} onClick={() => onSelect(p.id)}>
                <td>
                  <strong>{p.name}</strong>
                  {p.status === 'hidden' && (
                    <>
                      {' '}
                      <Pill tone="red">已隐藏</Pill>
                    </>
                  )}
                  <div className="muted">{[p.name_ja, `#${p.id}`].filter(Boolean).join(' · ')}</div>
                  {compact && (
                    <div className="adm-chips" style={{marginTop: 4}}>
                      {birthCell(p)}
                      {flags(p)}
                    </div>
                  )}
                </td>
                <td className="num">
                  {fmtNum(p.published_works)}
                  {p.upcoming_works > 0 && <div className="adm-field-hint">即将上线 {p.upcoming_works}</div>}
                </td>
                <td title={missingText(p.missing) ? `缺：${missingText(p.missing)}` : '信息完整'}>
                  <Meter value={p.completeness} />
                  {!p.has_photo && <div className="adm-field-hint">缺照片</div>}
                </td>
                {!compact && <td>{birthCell(p)}</td>}
                {!compact && (
                  <td>
                    <div className="adm-chips">{flags(p)}</div>
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <MoreButton next={next} busy={busy} onMore={() => void load(next)} />
    </section>
  );
}

// ---------------------------------------------------------------- detail

const toInput = (v: unknown) => (v === null || v === undefined ? '' : String(v));

function FieldsCard({p, onSaved}: {p: AdminPerformer; onSaved: () => void}) {
  const act = useAdminAction();
  const initial = useMemo(() => Object.fromEntries(EDIT_FIELDS.map(f => [f.column, toInput(p[f.key])])) as Record<string, string>, [p]);
  const [form, setForm] = useState(initial);
  const [reason, setReason] = useState('');
  const [stale, setStale] = useState(false);
  const [clearReason, setClearReason] = useState<Record<string, string>>({});
  useEffect(() => {
    setForm(initial);
    setStale(false);
  }, [initial]);

  const overridden = new Set((p.overrides ?? []).map(o => o.field));
  const changed = Object.keys(form).filter(k => form[k] !== initial[k]);

  const save = async (e: FormEvent) => {
    e.preventDefault();
    const body: Record<string, unknown> = {version: p.version};
    for (const k of changed) {
      const def = EDIT_FIELDS.find(f => f.column === k);
      const v = form[k].trim();
      body[k] = v === '' ? null : def?.kind === 'number' ? Number(v) : v;
    }
    if (reason.trim()) body.reason = reason.trim();
    try {
      await act.run('save', () => patchAdminPerformer(p.id, body), v => `已保存：${(v.changed ?? []).map(fieldLabel).join('、') || '无变化'}${recheckText(v.age_recheck)}`);
      setReason('');
      onSaved();
    } catch (cause) {
      if (isStale(cause)) setStale(true);
    }
  };

  const toggleHidden = async () => {
    try {
      await act.run(
        'hide-birth',
        () => patchAdminPerformer(p.id, {version: p.version, birth_date_hidden: !p.birth_date_hidden, reason: reason.trim() || undefined}),
        p.birth_date_hidden ? '已恢复显示生日' : '已隐藏生日：前台不再显示年龄和出演时年龄',
      );
      onSaved();
    } catch (cause) {
      if (isStale(cause)) setStale(true);
    }
  };

  const clear = async (field: string) => {
    const ok = await act.attempt(`clear-${field}`, () => clearPerformerOverride(p.id, field, (clearReason[field] ?? '').trim()), `已清除「${fieldLabel(field)}」的覆盖，下次抓取会重新写入`);
    if (ok) onSaved();
  };

  return (
    <form className="adm-card" onSubmit={e => void save(e)}>
      <div className="adm-card-head">
        <h2>资料</h2>
        <span className="muted mono">version {p.version}</span>
      </div>
      {stale && (
        <div className="adm-notice adm-notice-warn" role="alert">
          这位演员已被别人修改（版本不一致）。
          <button type="button" className="adm-link" onClick={onSaved}>
            重新加载最新版本
          </button>
        </div>
      )}
      <AdminNotice error={act.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />
      <div className="adm-form-grid">
        {EDIT_FIELDS.map(f => {
          const common = {value: form[f.column], onChange: (e: {target: {value: string}}) => setForm(prev => ({...prev, [f.column]: e.target.value}))};
          const isOver = overridden.has(f.column);
          return (
            <label key={f.column} className={`adm-field ${isOver ? 'overridden' : ''} ${f.kind === 'textarea' ? 'span-all' : ''}`}>
              <span className="label-row">
                {f.label} {isOver && <Pill tone="amber">已覆盖</Pill>} {form[f.column] !== initial[f.column] && <Pill tone="blue">未保存</Pill>}
              </span>
              {f.kind === 'textarea' ? (
                <textarea rows={4} {...common} />
              ) : f.kind === 'date' ? (
                <input type="date" {...common} />
              ) : f.kind === 'number' ? (
                <input type="number" step="0.5" {...common} />
              ) : (
                <input required={f.column === 'canonical_name'} {...common} />
              )}
              {f.hint && <span className="adm-field-hint warn">{f.hint}</span>}
            </label>
          );
        })}
      </div>
      <div className="adm-form-actions">
        <label className="adm-check">
          <input type="checkbox" checked={p.birth_date_hidden} disabled={act.pending === 'hide-birth'} onChange={() => void toggleHidden()} />
          隐藏生日
        </label>
        <span className="adm-field-hint">本人或经纪方申请后开启：前台不显示年龄和出演时年龄，年龄类筛选不包含该演员；后台仍可见。</span>
      </div>
      <div className="adm-form-actions">
        <input className="adm-input" placeholder="修改理由（可选，写入修改记录）" value={reason} onChange={e => setReason(e.target.value)} style={{flex: 1, minWidth: 220}} />
        <button type="button" className="btn btn-secondary btn-sm" disabled={!changed.length} onClick={() => setForm(initial)}>
          撤销
        </button>
        <button type="submit" className="btn btn-primary btn-sm" disabled={!changed.length || act.pending === 'save'}>
          保存{changed.length ? `（${changed.length}）` : ''}
        </button>
      </div>
      <p className="adm-field-hint">保存的字段成为「手动覆盖」，之后的抓取不会再改它，直到清除覆盖。</p>
      {(p.overrides ?? []).length > 0 && (
        <div className="adm-overrides">
          {(p.overrides ?? []).map(o => (
            <div key={o.field} className="adm-override">
              <Pill tone="amber">覆盖</Pill>
              <strong>{fieldLabel(o.field)}</strong>
              <span className="mono adm-clip" style={{maxWidth: 220}}>
                {jsonText(o.value)}
              </span>
              <span className="muted">
                {o.actor} · {fmtTime(o.updated_at)}
                {o.reason ? ` · ${o.reason}` : ''}
              </span>
              <span style={{flex: 1}} />
              <input
                className="adm-input"
                placeholder="理由（可选）"
                value={clearReason[o.field] ?? ''}
                onChange={e => setClearReason(prev => ({...prev, [o.field]: e.target.value}))}
                style={{width: 140}}
              />
              <button type="button" className="adm-link" disabled={act.pending === `clear-${o.field}`} onClick={() => void clear(o.field)}>
                清除覆盖
              </button>
            </div>
          ))}
        </div>
      )}
    </form>
  );
}

function AliasesCard({p, onSaved}: {p: AdminPerformer; onSaved: () => void}) {
  const act = useAdminAction();
  const [name, setName] = useState('');
  const [lang, setLang] = useState('');
  const [type, setType] = useState('stage');

  const add = async (e: FormEvent) => {
    e.preventDefault();
    const ok = await act.attempt('add', () => addPerformerAlias(p.id, {name: name.trim(), language: lang || undefined, alias_type: type}), v => `已添加别名「${v.name}」`);
    if (ok) {
      setName('');
      onSaved();
    }
  };
  const remove = async (aliasId: number, label: string) => {
    const why = window.prompt(`删除别名「${label}」的理由（可选）`);
    if (why === null) return;
    const ok = await act.attempt(`rm-${aliasId}`, () => removePerformerAlias(p.id, aliasId, why.trim()), '已删除别名');
    if (ok) onSaved();
  };

  const aliases = p.aliases ?? [];
  return (
    <section className="adm-card">
      <h2>别名（{aliases.length}）</h2>
      <AdminNotice error={act.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />
      <div className="adm-chips">
        {aliases.length === 0 && <span className="muted">没有别名</span>}
        {aliases.map(a => (
          <span key={a.id} className="adm-pill adm-pill-plain">
            {a.name}
            <span className="muted">
              {' '}
              · {ALIAS_TYPE_LABELS[a.alias_type] ?? a.alias_type}
              {a.language ? ` · ${a.language}` : ''}
            </span>
            <button type="button" className="adm-link" aria-label={`删除别名 ${a.name}`} onClick={() => void remove(a.id, a.name)}>
              ×
            </button>
          </span>
        ))}
      </div>
      <form className="adm-toolbar" onSubmit={e => void add(e)}>
        <input required value={name} onChange={e => setName(e.target.value)} placeholder="新别名，如 繁体名、罗马字" aria-label="别名" maxLength={200} />
        <select value={type} onChange={e => setType(e.target.value)} aria-label="别名类型">
          {ALIAS_TYPES.map(t => (
            <option key={t} value={t}>
              {ALIAS_TYPE_LABELS[t]}
            </option>
          ))}
        </select>
        <select value={lang} onChange={e => setLang(e.target.value)} aria-label="语言">
          <option value="">语言（可选）</option>
          {I18N_LANGS.map(l => (
            <option key={l} value={l}>
              {l}
            </option>
          ))}
        </select>
        <button type="submit" className="btn btn-secondary btn-sm" disabled={act.pending === 'add'}>
          添加
        </button>
      </form>
      <p className="adm-field-hint">别名参与搜索：繁体名、旧艺名、罗马字都加进来，用户才能搜到。</p>
    </section>
  );
}

function StatusCard({p, onSaved}: {p: AdminPerformer; onSaved: () => void}) {
  const act = useAdminAction();
  const [reason, setReason] = useState('');
  const hidden = p.status === 'hidden';
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const ok = await act.attempt(
      'status',
      () => setPerformerStatus(p.id, {status: hidden ? 'active' : 'hidden', reason: reason.trim() || undefined, version: p.version}),
      v => (v.to === 'hidden' ? '已隐藏：前台不再显示该演员页和搜索结果（不删除数据）' : '已恢复显示'),
    );
    if (ok) {
      setReason('');
      onSaved();
    }
  };
  return (
    <form className="adm-card" onSubmit={e => void submit(e)}>
      <h2>前台显示</h2>
      <AdminNotice error={act.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />
      <p>
        当前：<Pill tone={hidden ? 'red' : 'green'}>{hidden ? '已隐藏' : '正常显示'}</Pill>
        {hidden && p.hidden_reason && <span className="muted"> · {p.hidden_reason}</span>}
      </p>
      <div className="adm-toolbar">
        <input
          required={!hidden}
          value={reason}
          onChange={e => setReason(e.target.value)}
          placeholder={hidden ? '恢复理由（可选）' : '隐藏理由（必填，例如 本人申请下架）'}
          aria-label="理由"
          style={{flex: 1, minWidth: 220}}
        />
        <button type="submit" className={`btn btn-sm ${hidden ? 'btn-secondary' : 'btn-primary'}`} disabled={act.pending === 'status'}>
          {hidden ? '恢复显示' : '隐藏演员'}
        </button>
      </div>
      <p className="adm-field-hint">隐藏不删除任何数据，作品和别名都保留，随时可以恢复。</p>
    </form>
  );
}

function WorksTable({works, title}: {works: AdminPerformerWork[]; title: string}) {
  return (
    <section className="adm-card">
      <h2>
        {title}（{works.length}）
      </h2>
      <div className="adm-table-wrap">
        <table className="adm-table">
          <thead>
            <tr>
              <th>作品</th>
              <th>角色</th>
              <th>日期</th>
              <th>状态</th>
              <th title="只在后台显示">出演时年龄</th>
            </tr>
          </thead>
          <tbody>
            {works.length === 0 && <EmptyRow cols={5}>没有作品</EmptyRow>}
            {works.map(w => {
              const age = w.age_at_filming;
              return (
                <tr key={w.release_id} className={age && age.age < 18 ? 'flag-red' : ''}>
                  <td className="wrap">
                    <Link to={`/admin/videos/${w.release_id}`}>{w.title}</Link>
                    {w.credited_name && <div className="muted">署名 {w.credited_name}</div>}
                  </td>
                  <td>
                    {ROLE_LABELS[w.role] ?? w.role}
                    {w.is_primary && <span className="muted"> · 第一主演</span>}
                  </td>
                  <td className="mono">{w.upcoming && w.scheduled_at ? fmtTime(w.scheduled_at) : (w.release_date ?? '—')}</td>
                  <td>
                    <Pill tone={statusTone(w.status)}>{STATUS_LABELS[w.status] ?? w.status}</Pill>
                  </td>
                  <td>
                    {age ? (
                      <span className={age.age < 18 ? 'adm-kpi-value red' : ''} style={age.age < 18 ? {fontSize: 14} : undefined}>
                        {age.approx ? '约 ' : ''}
                        {age.age} 岁<span className="muted"> · {age.basis === 'shoot_year' ? '按拍摄年' : '按发布年'}</span>
                      </span>
                    ) : (
                      <span className="muted">—</span>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function SimilarCard({p}: {p: AdminPerformer}) {
  const items = p.similar ?? [];
  return (
    <section className="adm-card">
      <h2>相似演员（{items.length}）</h2>
      {items.length === 0 ? (
        <p className="muted">还没有相似演员（每晚离线计算）。</p>
      ) : (
        <div className="adm-table-wrap">
          <table className="adm-table">
            <thead>
              <tr>
                <th>演员</th>
                <th className="num">匹配</th>
                <th>理由</th>
                <th>证据</th>
              </tr>
            </thead>
            <tbody>
              {items.map(s => (
                <tr key={s.id}>
                  <td>
                    <Link to={`/admin/actors?id=${s.id}`}>{s.name}</Link>
                    {s.status !== 'active' && <Pill tone="red">{s.status}</Pill>}
                  </td>
                  <td className="num">{s.match_percent}%</td>
                  <td className="wrap">{s.reason}</td>
                  <td className="wrap">
                    元数据 {Math.round(s.evidence.metadata * 100)}% · 共演 {s.evidence.co_star} 部 · 观众重合 {Math.round(s.evidence.co_watch * 100)}%
                    {(s.evidence.shared_tags ?? []).length > 0 && <div className="muted">标签：{(s.evidence.shared_tags ?? []).join('、')}</div>}
                    {(s.evidence.shared_makers ?? []).length > 0 && <div className="muted">片商：{(s.evidence.shared_makers ?? []).join('、')}</div>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function SideFacts({p}: {p: AdminPerformer}) {
  const dups = p.duplicate_candidates ?? [];
  const issues = (p.audit_issues ?? []).filter(i => i.status === 'open');
  const requests = p.requests ?? [];
  return (
    <section className="adm-card">
      <h2>待办</h2>
      <dl className="adm-kv">
        <dt>完整度</dt>
        <dd>
          <Meter value={p.completeness} /> {missingText(p.missing) && <span className="muted">缺：{missingText(p.missing)}</span>}
        </dd>
        <dt>照片</dt>
        <dd>{p.photos > 0 ? `${p.photos} 张` : <Pill tone="red">缺照片</Pill>}</dd>
        <dt>疑似重复</dt>
        <dd>
          {dups.length === 0
            ? '—'
            : dups.map(d => (
                <div key={String(d.candidate_id)}>
                  <Link to={`/admin/actors?id=${String(d.performer_id)}`}>{String(d.name)}</Link>
                  <span className="muted"> · 相似度 {Math.round(Number(d.score) * 100)}% · 候选 #{String(d.candidate_id)} </span>
                  <Link to="/admin/dedupe" className="adm-link">
                    去归并
                  </Link>
                </div>
              ))}
        </dd>
        <dt>数据问题</dt>
        <dd>
          {issues.length === 0
            ? '—'
            : issues.map(i => (
                <Link key={i.id} to={`/admin/audit?rule=${i.rule}`} style={{marginRight: 6}}>
                  <Pill tone={i.rule === 'R-AGE' || i.severity === 'blocking' ? 'red' : 'amber'}>
                    {i.rule} · {RULE_LABELS[i.rule] ?? ((i.rule as string) === 'R-PERFORMER-NAME' ? '名称可疑' : '')}
                  </Pill>
                </Link>
              ))}
        </dd>
        <dt>申请</dt>
        <dd>
          {requests.length === 0
            ? '—'
            : requests.map(r => (
                <div key={r.id}>
                  #{r.id} {REQUEST_KIND_LABELS[r.kind] ?? r.kind} · <Pill tone={r.status === 'open' ? 'amber' : r.status === 'approved' ? 'green' : 'plain'}>{REQUEST_STATUS_LABELS[r.status] ?? r.status}</Pill>
                  {r.status === 'open' && <span className="muted"> · 截止 {fmtTime(r.due_at)}</span>}
                </div>
              ))}
        </dd>
      </dl>
    </section>
  );
}

function EventsCard({p}: {p: AdminPerformer}) {
  const events = p.review_events ?? [];
  return (
    <section className="adm-card">
      <h2>修改记录（{events.length}）</h2>
      <div className="adm-events">
        {events.length === 0 && <p className="muted">还没有修改记录。</p>}
        {events.map(e => (
          <div key={e.id} className="adm-event">
            <span className="adm-event-time">
              {fmtTime(e.created_at)}
              <br />#{e.id}
            </span>
            <div>
              <strong>{e.action}</strong> · {e.actor ?? '—'}
              {e.reason && <div className="muted">理由：{e.reason}</div>}
              {(e.before !== null || e.after !== null) && (
                <div className="adm-diff">
                  <pre aria-label="修改前">{e.before === null ? '—' : JSON.stringify(e.before, null, 2)}</pre>
                  <pre aria-label="修改后">{e.after === null ? '—' : JSON.stringify(e.after, null, 2)}</pre>
                </div>
              )}
            </div>
          </div>
        ))}
      </div>
    </section>
  );
}

function PerformerDetail({id, onChanged}: {id: number; onChanged: () => void}) {
  const res = useAdminLoad(() => getAdminPerformer(id), [id]);
  const p = res.data && res.data.id === id ? res.data : null;
  const reload = () => {
    res.reload();
    onChanged();
  };
  if (res.failure && isMissingEndpoint(res.failure) && !(res.failure instanceof ApiError && res.failure.detail === 'not found')) {
    return <NotDeployed what="演员编辑接口（admin/performers/{id}）" />;
  }
  if (!p) return <AdminNotice error={res.error} notice={res.busy ? '加载中…' : ''} />;
  const works = (p.works ?? []).filter(w => !w.upcoming);
  const upcoming = p.upcoming ?? (p.works ?? []).filter(w => w.upcoming);
  return (
    <div className="adm-detail">
      <header className="adm-head">
        <div className="adm-head-text">
          <h1>{p.name}</h1>
          <p className="adm-sub">
            <Pill tone={p.status === 'hidden' ? 'red' : 'green'}>{p.status === 'hidden' ? '已隐藏' : '正常'}</Pill> · ID {p.id} · {p.name_ja ?? '—'} · {p.slug} · 更新于 {fmtTime(p.updated_at)}
          </p>
        </div>
        <div className="adm-head-actions">
          <Link to={`/performers/${encodeURIComponent(p.slug)}`} target="_blank" className="btn btn-secondary btn-sm">
            前台预览
          </Link>
          <button type="button" className="btn btn-secondary btn-sm" disabled={res.busy} onClick={res.reload}>
            刷新
          </button>
        </div>
      </header>
      <AdminNotice error={res.error} />
      <SideFacts p={p} />
      <FieldsCard p={p} onSaved={reload} />
      <AliasesCard p={p} onSaved={reload} />
      <StatusCard p={p} onSaved={reload} />
      {upcoming.length > 0 && <WorksTable works={upcoming} title="即将上线" />}
      <WorksTable works={works} title="作品" />
      <SimilarCard p={p} />
      <EventsCard p={p} />
    </div>
  );
}

// ---------------------------------------------------------------- requests

function proposedText(v: unknown) {
  if (v === null || v === undefined) return '—';
  return typeof v === 'string' ? v : JSON.stringify(v);
}

function RequestCard({r, onDone, onOpen}: {r: PerformerRequest; onDone: (msg: string) => void; onOpen: (id: number) => void}) {
  const act = useAdminAction();
  const [note, setNote] = useState('');
  const [value, setValue] = useState(r.kind === 'correct_field' ? proposedText(r.proposed_value) : '');
  const open = r.status === 'open';
  const links = r.evidence?.links ?? [];

  const decide = async (to: 'approve' | 'reject') => {
    if (to === 'reject' && !note.trim()) {
      act.setError('驳回需要填写说明（会回复给申请人）');
      return;
    }
    const body: {note?: string; value?: unknown} = {note: note.trim() || undefined};
    if (to === 'approve' && r.kind === 'correct_field' && value.trim() && value.trim() !== proposedText(r.proposed_value)) body.value = value.trim();
    const v = await act.attempt(to, () => decidePerformerRequest(r.id, to, body));
    if (v)
      onDone(
        to === 'approve'
          ? `已批准 #${r.id}（${r.performer_name} · ${REQUEST_KIND_LABELS[r.kind] ?? r.kind}）${v.applied ? `：${Object.keys(v.applied).map(fieldLabel).join('、')}` : ''}${recheckText(v.age_recheck)}`
          : `已驳回 #${r.id}（${r.performer_name}）`,
      );
  };

  return (
    <article className={`adm-request ${r.overdue && open ? 'overdue' : ''}`}>
      <div className="adm-toolbar" style={{alignItems: 'center'}}>
        <strong>#{r.id}</strong>
        <button type="button" className="adm-link" onClick={() => onOpen(r.performer_id)}>
          {r.performer_name}
        </button>
        <Pill tone={r.kind === 'hide_birth_date' || r.kind === 'remove_photo' ? 'purple' : 'blue'}>{REQUEST_KIND_LABELS[r.kind] ?? r.kind}</Pill>
        <Pill tone={open ? (r.overdue ? 'red' : 'amber') : r.status === 'approved' ? 'green' : 'plain'}>{REQUEST_STATUS_LABELS[r.status] ?? r.status}</Pill>
        <span style={{flex: 1}} />
        <span className={`adm-due ${r.overdue && open ? 'overdue' : ''}`}>
          {open ? `截止 ${fmtTime(r.due_at)}${r.overdue ? '（已超时）' : ''}` : `${r.resolved_by ?? '—'} · ${fmtTime(r.resolved_at)}`}
        </span>
      </div>
      <dl className="adm-kv">
        {r.kind === 'correct_field' && (
          <>
            <dt>更正</dt>
            <dd>
              {fieldLabel(r.field_name)} → <strong>{proposedText(r.proposed_value)}</strong>
            </dd>
          </>
        )}
        <dt>联系方式</dt>
        <dd>{r.requester_contact}</dd>
        <dt>身份</dt>
        <dd>{REQUESTER_ROLE_LABELS[r.evidence?.role ?? ''] ?? r.evidence?.role ?? '—'}</dd>
        <dt>说明</dt>
        <dd>{r.evidence?.message || '—'}</dd>
        <dt>证明</dt>
        <dd>
          {links.length === 0
            ? '—'
            : links.map(l => (
                <div key={l}>
                  <a href={l} target="_blank" rel="noopener noreferrer nofollow" className="mono">
                    {l}
                  </a>
                </div>
              ))}
        </dd>
        <dt>提交</dt>
        <dd>{fmtTime(r.created_at)}</dd>
        {r.resolution_note && (
          <>
            <dt>处理说明</dt>
            <dd>{r.resolution_note}</dd>
          </>
        )}
      </dl>
      <AdminNotice error={act.error} notice={act.notice} />
      {open && (
        <div className="adm-toolbar">
          {r.kind === 'correct_field' && (
            <label className="adm-field">
              <span>批准时写入的值</span>
              <input value={value} onChange={e => setValue(e.target.value)} aria-label="批准时写入的值" />
            </label>
          )}
          <input value={note} onChange={e => setNote(e.target.value)} placeholder="处理说明（驳回必填）" aria-label="处理说明" style={{flex: 1, minWidth: 220}} />
          <button type="button" className="btn btn-primary btn-sm" disabled={!!act.pending} onClick={() => void decide('approve')}>
            {r.kind === 'hide_birth_date' ? '批准并隐藏生日' : r.kind === 'correct_field' ? '批准并更正' : '批准'}
          </button>
          <button type="button" className="btn btn-secondary btn-sm" disabled={!!act.pending} onClick={() => void decide('reject')}>
            驳回
          </button>
        </div>
      )}
    </article>
  );
}

function RequestsTab({onOpen}: {onOpen: (id: number) => void}) {
  const {hasToken, epoch} = useAdmin();
  const [status, setStatus] = useState('open');
  const [kind, setKind] = useState('');
  const [items, setItems] = useState<PerformerRequest[]>([]);
  const [next, setNext] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [missing, setMissing] = useState(false);
  const [tick, setTick] = useState(0);
  const [notice, setNotice] = useState('');

  const load = async (cursor: string | null) => {
    setBusy(true);
    setError('');
    try {
      const page = await getPerformerRequests({status, kind, cursor, limit: 30});
      setItems(prev => (cursor ? [...prev, ...page.items] : page.items));
      setNext(page.next_cursor);
    } catch (cause) {
      if (isMissingEndpoint(cause)) setMissing(true);
      else setError(adminErrorText(cause));
    } finally {
      setBusy(false);
    }
  };
  useEffect(() => {
    if (hasToken) void load(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hasToken, epoch, status, kind, tick]);

  if (missing) return <NotDeployed what="更正与隐藏申请接口（admin/performer-requests）" />;
  return (
    <section className="adm-card">
      <div className="adm-card-head">
        <h2>更正与隐藏申请</h2>
        <div className="adm-toolbar">
          <div className="adm-chips" role="group" aria-label="状态">
            {Object.entries(REQUEST_STATUS_LABELS).map(([v, t]) => (
              <button key={v} type="button" className="adm-chip" aria-pressed={status === v} onClick={() => setStatus(v)}>
                {t}
              </button>
            ))}
          </div>
          <select value={kind} onChange={e => setKind(e.target.value)} aria-label="类型">
            <option value="">全部类型</option>
            {Object.entries(REQUEST_KIND_LABELS).map(([v, t]) => (
              <option key={v} value={v}>
                {t}
              </option>
            ))}
          </select>
        </div>
      </div>
      <p className="adm-field-hint">按截止时间排序：隐藏生日、删除照片 3 天内处理，资料更正 7 天内。驳回的说明会回复给申请人。证明材料只在后台可见。</p>
      <AdminNotice error={error} notice={notice} onClose={() => setNotice('')} />
      <div className="adm-requests">
        {!busy && items.length === 0 && <p className="adm-empty">没有{REQUEST_STATUS_LABELS[status]}的申请</p>}
        {items.map(r => (
          <RequestCard key={`${r.id}-${r.status}`} r={r} onOpen={onOpen} onDone={msg => (setNotice(msg), setTick(t => t + 1))} />
        ))}
      </div>
      <MoreButton next={next} busy={busy} onMore={() => void load(next)} />
    </section>
  );
}

// ---------------------------------------------------------------- weights

const WEIGHT_FIELDS: {k: keyof Weights; t: string; hint: string}[] = [
  {k: 'metadata', t: '元数据相似', hint: '标签、片商、系列'},
  {k: 'co_star', t: '作品重合', hint: '共同出演'},
  {k: 'co_watch', t: '观众共现', hint: '同一批观众都看'},
  {k: 'visual', t: '画面向量', hint: '尚未计算，保持 0'},
];
const sameWeights = (a: Weights, b: Weights) => WEIGHT_FIELDS.every(f => Math.abs(a[f.k] - b[f.k]) < 1e-9);

function PreviewList({items, other}: {items: PreviewMatch[]; other: PreviewMatch[]}) {
  if (!items.length) return <p className="muted">—</p>;
  return (
    <ol>
      {items.map((m, i) => {
        const j = other.findIndex(o => o.id === m.id);
        const cls = j < 0 ? 'new' : j !== i ? 'moved' : '';
        return (
          <li key={m.id} className={cls}>
            {m.name} <span className="muted mono">{m.match_percent}%</span>
            {cls === 'new' && <span className="muted"> · 新进</span>}
            {cls === 'moved' && <span className="muted"> · 原第 {j + 1}</span>}
          </li>
        );
      })}
    </ol>
  );
}

function WeightsTab() {
  const current = useAdminLoad(() => getSimilarityWeights());
  const act = useAdminAction();
  const [draft, setDraft] = useState<Weights | null>(null);
  const [preview, setPreview] = useState<SimilarityPreview | null>(null);
  const [previewMissing, setPreviewMissing] = useState(false);

  useEffect(() => {
    if (current.data) setDraft({metadata: current.data.metadata, co_star: current.data.co_star, co_watch: current.data.co_watch, visual: current.data.visual});
  }, [current.data]);

  if (current.failure && isMissingEndpoint(current.failure)) return <NotDeployed what="相似演员权重接口（admin/similarity-weights）" />;
  if (!draft || !current.data) return <AdminNotice error={current.error} notice={current.busy ? '加载中…' : ''} />;

  const saved: Weights = {metadata: current.data.metadata, co_star: current.data.co_star, co_watch: current.data.co_watch, visual: current.data.visual};
  const dirty = !sameWeights(draft, saved);
  const previewed = !!preview && sameWeights(preview.weights, draft);
  const samples = preview?.samples ?? [];
  const changedCount = samples.filter(s => s.changed).length;

  const runPreview = async () => {
    try {
      const v = await act.run('preview', () => previewSimilarityWeights({...draft, sample: 20, top: 5}));
      if (v) setPreview(v);
    } catch (cause) {
      if (isMissingEndpoint(cause)) {
        setPreviewMissing(true);
        act.setError('');
      }
    }
  };
  const save = async () => {
    const ok = await act.attempt('save', () => putSimilarityWeights(draft), '已保存：下一次夜间离线任务生效');
    if (ok) {
      setPreview(null);
      current.reload();
    }
  };

  return (
    <section className="adm-card">
      <div className="adm-card-head">
        <h2>相似演员 · 权重</h2>
        <span className="muted">
          当前 {current.data.updated_by ? `${current.data.updated_by} · ` : ''}
          {fmtTime(current.data.updated_at)}
        </span>
      </div>
      <p className="adm-field-hint">改权重后先预览 20 位演员的前 5 名变化，确认后保存；第二天夜间离线任务生效。预览只能重排已存的候选。</p>
      <AdminNotice error={act.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />
      <div className="adm-weights">
        {WEIGHT_FIELDS.map(f => (
          <label key={f.k} className="adm-field">
            <span className="label-row">
              {f.t} <span className="mono">{draft[f.k].toFixed(2)}</span>
              {draft[f.k] !== saved[f.k] && <Pill tone="blue">原 {saved[f.k].toFixed(2)}</Pill>}
            </span>
            <input
              type="range"
              min={0}
              max={1}
              step={0.05}
              value={draft[f.k]}
              onChange={e => setDraft({...draft, [f.k]: Number(e.target.value)})}
              aria-label={f.t}
            />
            <span className="adm-field-hint">{f.hint}</span>
          </label>
        ))}
      </div>
      <div className="adm-form-actions">
        <button type="button" className="btn btn-secondary btn-sm" disabled={!dirty} onClick={() => (setDraft(saved), setPreview(null))}>
          恢复当前值
        </button>
        <button type="button" className="btn btn-secondary btn-sm" disabled={act.pending === 'preview' || previewMissing} onClick={() => void runPreview()}>
          {act.pending === 'preview' ? '预览中…' : '预览'}
        </button>
        <button type="button" className="btn btn-primary btn-sm" disabled={!dirty || (!previewed && !previewMissing) || act.pending === 'save'} onClick={() => void save()}>
          保存权重
        </button>
        {dirty && !previewed && !previewMissing && <span className="adm-field-hint warn">先预览当前改动再保存</span>}
        {previewMissing && <span className="adm-field-hint warn">后端未部署预览接口：保存前无法比较结果</span>}
      </div>
      {preview && (
        <>
          <p className="adm-field-hint">
            {previewed ? '' : '权重已改动，下面是上一次预览的结果。'}样本 {samples.length} 位，前 5 名有变化的 {changedCount} 位。
          </p>
          <div className="adm-preview">
            {samples.map(s => (
              <div key={s.performer.id} className={`adm-preview-card ${s.changed ? 'changed' : ''}`}>
                <strong>
                  <Link to={`/admin/actors?id=${s.performer.id}`}>{s.performer.name}</Link>
                </strong>{' '}
                <span className="muted">
                  {s.changed ? `有变化 · 重合 ${s.overlap}` : '无变化'}
                </span>
                <div className="adm-preview-cols">
                  <div>
                    <span className="muted">当前</span>
                    <PreviewList items={s.current ?? []} other={s.proposed ?? []} />
                  </div>
                  <div>
                    <span className="muted">调整后</span>
                    <PreviewList items={s.proposed ?? []} other={s.current ?? []} />
                  </div>
                </div>
              </div>
            ))}
          </div>
        </>
      )}
    </section>
  );
}

// ---------------------------------------------------------------- page

export default function AdminActors() {
  const [params, setParams] = useSearchParams();
  const tab = (TABS.find(t => t.key === params.get('tab'))?.key ?? 'list') as TabKey;
  const id = Number(params.get('id')) || 0;
  const [listKey, setListKey] = useState(0);

  const set = (patch: Record<string, string | null>) => {
    const p = new URLSearchParams(params);
    for (const [k, v] of Object.entries(patch)) {
      if (v) p.set(k, v);
      else p.delete(k);
    }
    setParams(p, {replace: true});
  };
  const open = (pid: number) => set({tab: null, id: String(pid)});

  return (
    <>
      <AdminHeader
        eyebrow="ADMIN · ACTORS"
        title="演员"
        sub="补全资料、处理本人或经纪方的更正与隐藏申请、调整相似演员权重。生日只在后台可见时也用于 R-AGE 合规检查。"
      />
      <div className="adm-tabs" role="tablist" aria-label="演员">
        {TABS.map(t => (
          <button key={t.key} type="button" role="tab" className="adm-tab" aria-selected={tab === t.key} onClick={() => set({tab: t.key === 'list' ? null : t.key})}>
            {t.label}
          </button>
        ))}
      </div>
      {tab === 'list' && (
        <div className={id ? 'adm-split' : ''}>
          <PerformerList selected={id} onSelect={open} epochKey={listKey} />
          {id > 0 && <PerformerDetail key={id} id={id} onChanged={() => setListKey(k => k + 1)} />}
        </div>
      )}
      {tab === 'requests' && <RequestsTab onOpen={open} />}
      {tab === 'weights' && <WeightsTab />}
    </>
  );
}
