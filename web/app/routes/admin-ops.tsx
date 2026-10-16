import {useMemo, useState, type DragEvent, type FormEvent} from 'react';
import {Link, useSearchParams} from 'react-router';
import {AdminHeader, AdminNotice, EmptyRow, NotDeployed, Pill, fmtTime, type Tone} from '../components/AdminUI';
import {ReleasePicker, type Picked} from '../components/AdminPickers';
import {
  SEVERITY_LABELS,
  SLOT_TYPE_LABELS,
  TARGET_LABELS,
  createAnnouncement,
  createHomeSlot,
  deleteAnnouncement,
  deleteHomeSlot,
  getAdminAnnouncements,
  getAdminHomeSlots,
  previewHome,
  reorderHomeSlots,
  updateAnnouncement,
  updateHomeSlot,
  type AdminAnnouncement,
  type AdminHomeSlot,
  type AnnouncementInput,
  type HomeSlotInput,
  type OpsText,
  type OpsTexts,
  type SlotType,
  type TargetType,
} from '../lib/adminOpsApi';
import {routeMissing} from '../lib/adminGovApi';
import {useAdminAction, useAdminLoad} from '../lib/adminContext';
import {LANGS, LANG_LABELS} from '../lib/langs';
import type {Severity} from '../lib/home';
import '../styles/admin-ops.css';

export const meta = () => [{title: 'REEL · 运营排期'}];

// AdminOps (PRD 11; internal/api/home.go): 首页按天排期 (home_slots: 主推横幅
// and 编辑精选 with per-language titles, language / region targeting, a time
// window and priority), drag reorder, a preview of what the public home and
// announcement bar serve at any moment, and the 公告条. Every write is a
// review_events row with the signed-in admin as 操作人.

const DAY = 24 * 60 * 60 * 1000;
const TIMELINE_DAYS = 14;
const SLOT_TYPES: SlotType[] = ['hero', 'shelf'];
const TARGETS: TargetType[] = ['release', 'series', 'link'];
const SEVERITIES: Severity[] = ['info', 'warning', 'critical'];

type Tab = 'schedule' | 'announcements' | 'preview';

// ---- time helpers (datetime-local inputs are local time) ----

const pad = (n: number) => String(n).padStart(2, '0');
function toLocalInput(iso?: string | null): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}
const fromLocalInput = (v: string): string | null => (v ? new Date(v).toISOString() : null);
function startOfDay(d: Date) {
  const x = new Date(d);
  x.setHours(0, 0, 0, 0);
  return x;
}
const dateInput = (d: Date) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
const dayLabel = new Intl.DateTimeFormat('zh-CN', {month: 'numeric', day: 'numeric', weekday: 'short'});

const live = (s: {starts_at: string; ends_at: string | null; enabled: boolean}, at: number) =>
  s.enabled && new Date(s.starts_at).getTime() <= at && (!s.ends_at || new Date(s.ends_at).getTime() > at);

function slotState(s: AdminHomeSlot, now: number): {label: string; tone: Tone} {
  if (!s.enabled) return {label: '已停用', tone: 'plain'};
  if (!s.target_public) return {label: '目标未公开', tone: 'red'};
  if (s.ends_at && new Date(s.ends_at).getTime() <= now) return {label: '已结束', tone: 'plain'};
  if (new Date(s.starts_at).getTime() > now) return {label: '待上线', tone: 'blue'};
  return {label: '上线中', tone: 'green'};
}

const slotTarget = (s: AdminHomeSlot) =>
  s.target_type === 'release' ? (s.release?.canonical_title ?? `#${s.release_id}`) : s.target_type === 'series' ? (s.series?.name ?? `#${s.series_id}`) : (s.link_url ?? '');
const slotTitle = (s: AdminHomeSlot) => s.texts['zh-CN']?.title || Object.values(s.texts).find(t => t.title)?.title || slotTarget(s);
const targeting = (langs: string[], regions: string[]) =>
  [langs.length ? langs.join(' / ') : '全部语言', regions.length ? regions.join(' / ') : '全部地区'].join(' · ');

export default function AdminOps() {
  const [params, setParams] = useSearchParams();
  const tab = (params.get('tab') as Tab) || 'schedule';
  const setTab = (t: Tab) => {
    const p = new URLSearchParams(params);
    if (t === 'schedule') p.delete('tab');
    else p.set('tab', t);
    setParams(p, {replace: true});
  };
  return (
    <>
      <AdminHeader
        eyebrow="ADMIN · OPS"
        title="运营排期"
        sub="首页主推横幅与编辑精选按时间排期，可按语言 / 地区定向；公告条显示在全站顶部。每次修改都会记录操作人。"
      />
      <div className="adm-tabs" role="tablist" aria-label="运营排期">
        {(
          [
            ['schedule', '首页排期'],
            ['announcements', '公告条'],
            ['preview', '上线预览'],
          ] as [Tab, string][]
        ).map(([k, label]) => (
          <button key={k} type="button" role="tab" aria-selected={tab === k} className="adm-tab" onClick={() => setTab(k)}>
            {label}
          </button>
        ))}
      </div>
      {tab === 'schedule' && <Schedule />}
      {tab === 'announcements' && <Announcements />}
      {tab === 'preview' && <Preview />}
    </>
  );
}

// ---------------------------------------------------------------- schedule

function Schedule() {
  const [from, setFrom] = useState(() => dateInput(startOfDay(new Date(Date.now() - 2 * DAY))));
  const rangeStart = startOfDay(new Date(`${from}T00:00:00`)).getTime();
  const rangeEnd = rangeStart + TIMELINE_DAYS * DAY;
  const list = useAdminLoad(() => getAdminHomeSlots({}), []);
  const act = useAdminAction();
  const [editing, setEditing] = useState<AdminHomeSlot | 'new' | null>(null);
  const now = list.data ? new Date(list.data.now).getTime() : Date.now();
  const items = list.data?.items ?? [];
  const missing = list.failure && routeMissing(list.failure);

  const saved = (s: AdminHomeSlot, created: boolean) => {
    act.setNotice(`${created ? '已新建' : '已保存'}「${slotTitle(s)}」`);
    setEditing(null);
    list.reload();
  };
  const remove = (s: AdminHomeSlot) => {
    if (!window.confirm(`删除排期「${slotTitle(s)}」？删除后可在修改记录里查到原内容。`)) return;
    void act.attempt(`del-${s.id}`, () => deleteHomeSlot(s.id), '已删除').then(v => {
      if (v) {
        setEditing(null);
        list.reload();
      }
    });
  };
  const reorder = (type: SlotType, ids: number[]) =>
    void act.attempt(`reorder-${type}`, () => reorderHomeSlots(type, ids), v => `已调整${SLOT_TYPE_LABELS[type]}顺序（${v.changed} 条）`).then(v => v && list.reload());

  return (
    <>
      {missing ? <NotDeployed what="首页排期接口（admin/home-slots）" /> : <AdminNotice error={list.error || act.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />}

      {editing && (
        <SlotForm key={editing === 'new' ? 'new' : editing.id} initial={editing === 'new' ? null : editing} onSaved={saved} onCancel={() => setEditing(null)} onDelete={remove} busy={act.pending} />
      )}

      <section className="adm-card">
        <div className="adm-card-head">
          <h2>排期时间线</h2>
          <div className="adm-toolbar">
            <label className="adm-field">
              起始日期
              <input type="date" value={from} onChange={e => e.target.value && setFrom(e.target.value)} />
            </label>
            <button type="button" className="btn btn-secondary btn-sm" onClick={() => setFrom(dateInput(new Date(rangeStart - 7 * DAY)))}>
              前一周
            </button>
            <button type="button" className="btn btn-secondary btn-sm" onClick={() => setFrom(dateInput(new Date(rangeStart + 7 * DAY)))}>
              后一周
            </button>
            <button type="button" className="btn btn-primary btn-sm" onClick={() => setEditing('new')} disabled={!!missing}>
              新建排期
            </button>
          </div>
        </div>
        <Timeline items={items} rangeStart={rangeStart} rangeEnd={rangeEnd} now={now} onPick={setEditing} />
      </section>

      {SLOT_TYPES.map(type => (
        <OrderList
          key={type}
          type={type}
          items={items.filter(s => s.slot_type === type && (!s.ends_at || new Date(s.ends_at).getTime() > now))}
          now={now}
          busy={act.pending === `reorder-${type}`}
          onEdit={setEditing}
          onReorder={ids => reorder(type, ids)}
        />
      ))}

      <section className="adm-card">
        <h2>已结束</h2>
        <div className="adm-table-wrap">
          <table className="adm-table">
            <thead>
              <tr>
                <th>位置</th>
                <th>标题</th>
                <th>目标</th>
                <th>时间</th>
                <th>操作人</th>
              </tr>
            </thead>
            <tbody>
              {items.filter(s => s.ends_at && new Date(s.ends_at).getTime() <= now).length === 0 && <EmptyRow cols={5}>没有已结束的排期</EmptyRow>}
              {items
                .filter(s => s.ends_at && new Date(s.ends_at).getTime() <= now)
                .map(s => (
                  <tr key={s.id}>
                    <td>{SLOT_TYPE_LABELS[s.slot_type]}</td>
                    <td>
                      <button type="button" className="adm-link" onClick={() => setEditing(s)}>
                        {slotTitle(s)}
                      </button>
                    </td>
                    <td className="wrap">{TARGET_LABELS[s.target_type]} · {slotTarget(s)}</td>
                    <td>
                      {fmtTime(s.starts_at)} – {fmtTime(s.ends_at)}
                    </td>
                    <td>{s.updated_by || s.created_by}</td>
                  </tr>
                ))}
            </tbody>
          </table>
        </div>
      </section>
    </>
  );
}

/** Two-week Gantt of every slot overlapping the range; click a bar to edit. */
function Timeline({items, rangeStart, rangeEnd, now, onPick}: {items: AdminHomeSlot[]; rangeStart: number; rangeEnd: number; now: number; onPick: (s: AdminHomeSlot) => void}) {
  const span = rangeEnd - rangeStart;
  const days = Array.from({length: TIMELINE_DAYS}, (_, i) => new Date(rangeStart + i * DAY));
  const rows = SLOT_TYPES.map(type => ({
    type,
    slots: items
      .filter(s => s.slot_type === type)
      .filter(s => new Date(s.starts_at).getTime() < rangeEnd && (!s.ends_at || new Date(s.ends_at).getTime() > rangeStart))
      .sort((a, b) => b.priority - a.priority || a.id - b.id),
  }));
  const nowPct = now >= rangeStart && now < rangeEnd ? ((now - rangeStart) / span) * 100 : null;
  return (
    <div className="ops-tl" role="group" aria-label="排期时间线">
      <div className="ops-tl-head">
        <span className="ops-tl-label" />
        <div className="ops-tl-days">
          {days.map(d => (
            <span key={d.getTime()} className={`ops-tl-day${d.getDay() === 0 || d.getDay() === 6 ? ' is-weekend' : ''}`}>
              {dayLabel.format(d)}
            </span>
          ))}
        </div>
      </div>
      {rows.map(row => (
        <div key={row.type} className="ops-tl-group">
          <div className="ops-tl-group-title">{SLOT_TYPE_LABELS[row.type]}</div>
          {row.slots.length === 0 && <div className="ops-tl-empty muted">这段时间没有排期</div>}
          {row.slots.map(s => {
            const a = Math.max(new Date(s.starts_at).getTime(), rangeStart);
            const b = Math.min(s.ends_at ? new Date(s.ends_at).getTime() : rangeEnd, rangeEnd);
            const st = slotState(s, now);
            return (
              <div key={s.id} className="ops-tl-row">
                <span className="ops-tl-label" title={slotTitle(s)}>
                  {slotTitle(s)}
                </span>
                <div className="ops-tl-track">
                  {nowPct !== null && <span className="ops-tl-now" style={{left: `${nowPct}%`}} aria-hidden="true" />}
                  <button
                    type="button"
                    className={`ops-tl-bar tone-${st.tone}`}
                    style={{left: `${((a - rangeStart) / span) * 100}%`, width: `${Math.max(((b - a) / span) * 100, 0.8)}%`}}
                    title={`${slotTitle(s)}\n${fmtTime(s.starts_at)} – ${s.ends_at ? fmtTime(s.ends_at) : '长期'}\n${st.label} · 优先级 ${s.priority}`}
                    onClick={() => onPick(s)}
                  >
                    {slotTitle(s)}
                  </button>
                </div>
              </div>
            );
          })}
        </div>
      ))}
    </div>
  );
}

/** Current and upcoming slots of one type by priority: drag (or ↑ / ↓) to reorder. */
function OrderList({
  type,
  items,
  now,
  busy,
  onEdit,
  onReorder,
}: {
  type: SlotType;
  items: AdminHomeSlot[];
  now: number;
  busy: boolean;
  onEdit: (s: AdminHomeSlot) => void;
  onReorder: (ids: number[]) => void;
}) {
  const sorted = useMemo(() => [...items].sort((a, b) => b.priority - a.priority || a.id - b.id), [items]);
  const [dragId, setDragId] = useState<number | null>(null);
  const move = (from: number, to: number) => {
    if (from === to || to < 0 || to >= sorted.length) return;
    const ids = sorted.map(s => s.id);
    const [x] = ids.splice(from, 1);
    ids.splice(to, 0, x);
    onReorder(ids);
  };
  const onDrop = (e: DragEvent, targetIndex: number) => {
    e.preventDefault();
    const from = sorted.findIndex(s => s.id === dragId);
    setDragId(null);
    if (from >= 0) move(from, targetIndex);
  };
  return (
    <section className="adm-card">
      <div className="adm-card-head">
        <h2>
          {SLOT_TYPE_LABELS[type]} · 顺序
        </h2>
        <span className="muted">拖动调整先后（同一时间上线的排期按此顺序展示）。{busy && ' 保存中…'}</span>
      </div>
      <div className="adm-table-wrap">
        <table className="adm-table">
          <thead>
            <tr>
              <th aria-label="拖动" />
              <th className="num">优先级</th>
              <th>标题</th>
              <th>目标</th>
              <th>定向</th>
              <th>时间</th>
              <th>状态</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {sorted.length === 0 && <EmptyRow cols={8}>没有上线中或待上线的{SLOT_TYPE_LABELS[type]}</EmptyRow>}
            {sorted.map((s, i) => {
              const st = slotState(s, now);
              return (
                <tr
                  key={s.id}
                  draggable={!busy}
                  onDragStart={e => {
                    setDragId(s.id);
                    e.dataTransfer.effectAllowed = 'move';
                    e.dataTransfer.setData('text/plain', String(s.id));
                  }}
                  onDragEnd={() => setDragId(null)}
                  onDragOver={e => {
                    if (dragId !== null) e.preventDefault();
                  }}
                  onDrop={e => onDrop(e, i)}
                  className={dragId === s.id ? 'ops-dragging' : ''}
                >
                  <td>
                    <span className="adm-grip" aria-hidden="true">
                      ⋮⋮
                    </span>
                  </td>
                  <td className="num mono">{s.priority}</td>
                  <td className="wrap" style={{maxWidth: 260}}>
                    {slotTitle(s)}
                    {s.texts['zh-CN']?.subtitle && <div className="adm-source-meta">{s.texts['zh-CN'].subtitle}</div>}
                  </td>
                  <td className="wrap" style={{maxWidth: 240}}>
                    {TARGET_LABELS[s.target_type]} ·{' '}
                    {s.release ? (
                      <Link to={`/admin/videos/${s.release.id}`}>{s.release.canonical_title}</Link>
                    ) : s.series ? (
                      <Link to={`/series/${encodeURIComponent(s.series.slug)}`} target="_blank">
                        {s.series.name}
                      </Link>
                    ) : (
                      <span className="mono">{s.link_url}</span>
                    )}
                  </td>
                  <td className="wrap">{targeting(s.langs, s.regions)}</td>
                  <td>
                    {fmtTime(s.starts_at)}
                    <div className="adm-source-meta">至 {s.ends_at ? fmtTime(s.ends_at) : '长期'}</div>
                  </td>
                  <td>
                    <Pill tone={st.tone}>{st.label}</Pill>
                  </td>
                  <td>
                    <div className="adm-form-actions">
                      <button type="button" className="adm-link" disabled={busy || i === 0} onClick={() => move(i, i - 1)} aria-label={`上移 ${slotTitle(s)}`}>
                        ↑
                      </button>
                      <button type="button" className="adm-link" disabled={busy || i === sorted.length - 1} onClick={() => move(i, i + 1)} aria-label={`下移 ${slotTitle(s)}`}>
                        ↓
                      </button>
                      <button type="button" className="adm-link" onClick={() => onEdit(s)}>
                        编辑
                      </button>
                    </div>
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

// ---- shared form pieces ----

/** Per-language copy editor: one tab per language, the fields given. */
function TextsEditor({value, onChange, fields}: {value: OpsTexts; onChange: (v: OpsTexts) => void; fields: {key: keyof OpsText; label: string; max: number; multiline?: boolean}[]}) {
  const [lang, setLang] = useState<string>('zh-CN');
  const cur = value[lang] ?? {};
  const set = (k: keyof OpsText, v: string) => onChange({...value, [lang]: {...cur, [k]: v}});
  const filled = (l: string) => fields.some(f => (value[l]?.[f.key] ?? '').trim() !== '');
  return (
    <div className="span-all ops-texts">
      <div className="adm-tabs" role="tablist" aria-label="文案语言">
        {LANGS.map(l => (
          <button key={l} type="button" role="tab" aria-selected={lang === l} className="adm-tab" onClick={() => setLang(l)}>
            {LANG_LABELS[l]}
            {filled(l) && <span className="ops-dot" aria-label="已填写" />}
          </button>
        ))}
      </div>
      <div className="adm-form-grid">
        {fields.map(f => (
          <label key={f.key} className={`adm-field${f.multiline ? ' span-all' : ''}`}>
            {f.label}（{LANG_LABELS[lang as keyof typeof LANG_LABELS]}）
            {f.multiline ? (
              <textarea value={cur[f.key] ?? ''} maxLength={f.max} onChange={e => set(f.key, e.target.value)} />
            ) : (
              <input value={cur[f.key] ?? ''} maxLength={f.max} onChange={e => set(f.key, e.target.value)} />
            )}
          </label>
        ))}
      </div>
      <p className="adm-field-hint">没有填写的语言依次回退到：繁体中文 → 简体中文 → 英文。</p>
    </div>
  );
}

function TargetingFields({langs, regions, onLangs, onRegions}: {langs: string[]; regions: string; onLangs: (v: string[]) => void; onRegions: (v: string) => void}) {
  return (
    <>
      <fieldset className="adm-field span-all ops-langs">
        <legend>投放语言（都不选 = 全部语言）</legend>
        <div className="adm-toolbar">
          {LANGS.map(l => (
            <label key={l} className="adm-check">
              <input type="checkbox" checked={langs.includes(l)} onChange={e => onLangs(e.target.checked ? [...langs, l] : langs.filter(x => x !== l))} />
              {LANG_LABELS[l]}
            </label>
          ))}
        </div>
      </fieldset>
      <label className="adm-field">
        投放地区（ISO 国家代码，逗号分隔；留空 = 全部）
        <input value={regions} onChange={e => onRegions(e.target.value)} placeholder="例如 JP, TW, US" />
      </label>
    </>
  );
}

const splitRegions = (v: string) =>
  v
    .split(/[\s,，、]+/)
    .map(x => x.trim().toUpperCase())
    .filter(Boolean);

function cleanTexts(t: OpsTexts): OpsTexts {
  const out: OpsTexts = {};
  for (const [lang, v] of Object.entries(t)) {
    const e = Object.fromEntries(Object.entries(v).filter(([, x]) => typeof x === 'string' && x.trim() !== '')) as OpsText;
    if (Object.keys(e).length > 0) out[lang] = e;
  }
  return out;
}

// ---- slot form ----

function SlotForm({
  initial,
  onSaved,
  onCancel,
  onDelete,
  busy,
}: {
  initial: AdminHomeSlot | null;
  onSaved: (s: AdminHomeSlot, created: boolean) => void;
  onCancel: () => void;
  onDelete: (s: AdminHomeSlot) => void;
  busy: string | null;
}) {
  const act = useAdminAction();
  const [slotType, setSlotType] = useState<SlotType>(initial?.slot_type ?? 'hero');
  const [target, setTarget] = useState<TargetType>(initial?.target_type ?? 'release');
  const [release, setRelease] = useState<Picked | null>(
    initial?.release && initial.target_type === 'release' ? {id: initial.release.id, slug: initial.release.slug, name: initial.release.canonical_title} : null,
  );
  const [seriesSlug, setSeriesSlug] = useState(initial?.series?.slug ?? '');
  const [link, setLink] = useState(initial?.link_url ?? '');
  const [texts, setTexts] = useState<OpsTexts>(initial?.texts ?? {});
  const [langs, setLangs] = useState<string[]>(initial?.langs ?? []);
  const [regions, setRegions] = useState((initial?.regions ?? []).join(', '));
  const [starts, setStarts] = useState(toLocalInput(initial?.starts_at ?? new Date().toISOString()));
  const [ends, setEnds] = useState(toLocalInput(initial?.ends_at));
  const [priority, setPriority] = useState(String(initial?.priority ?? 0));
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (target === 'release' && !release) {
      act.setError('请选择视频');
      return;
    }
    const body: HomeSlotInput = {
      slot_type: slotType,
      target_type: target,
      release_id: target === 'release' ? release?.id : undefined,
      series_slug: target === 'series' ? seriesSlug.trim() : undefined,
      link_url: target === 'link' ? link.trim() : undefined,
      texts: cleanTexts(texts),
      langs,
      regions: splitRegions(regions),
      starts_at: fromLocalInput(starts) ?? '',
      ends_at: fromLocalInput(ends),
      priority: Number(priority) || 0,
      enabled,
    };
    void act.attempt('save', () => (initial ? updateHomeSlot(initial.id, body) : createHomeSlot(body))).then(v => v && onSaved(v, !initial));
  };

  return (
    <section className="adm-card ops-form">
      <div className="adm-card-head">
        <h2>{initial ? `编辑排期 #${initial.id}` : '新建排期'}</h2>
        {initial && (
          <span className="muted">
            {initial.created_by && `创建 ${initial.created_by} · `}最后修改 {initial.updated_by || '—'} · {fmtTime(initial.updated_at)}
          </span>
        )}
      </div>
      <AdminNotice error={act.error} onClose={() => act.setError('')} />
      <form className="adm-form-grid" onSubmit={submit}>
        <label className="adm-field">
          位置
          <select value={slotType} onChange={e => setSlotType(e.target.value as SlotType)}>
            {SLOT_TYPES.map(t => (
              <option key={t} value={t}>
                {SLOT_TYPE_LABELS[t]}
              </option>
            ))}
          </select>
        </label>
        <label className="adm-field">
          指向
          <select value={target} onChange={e => setTarget(e.target.value as TargetType)}>
            {TARGETS.map(t => (
              <option key={t} value={t}>
                {TARGET_LABELS[t]}
              </option>
            ))}
          </select>
        </label>
        {target === 'release' && (
          <div className="span-all">
            <ReleasePicker value={release} onPick={setRelease} />
          </div>
        )}
        {target === 'series' && (
          <label className="adm-field">
            系列 slug
            <input value={seriesSlug} onChange={e => setSeriesSlug(e.target.value)} placeholder="例如 kawaii-series" required />
          </label>
        )}
        {target === 'link' && (
          <label className="adm-field span-all">
            链接（站内路径如 /discover?tag=x，或 https:// 地址）
            <input value={link} onChange={e => setLink(e.target.value)} placeholder="/discover?tag=" required />
          </label>
        )}
        <TextsEditor
          value={texts}
          onChange={setTexts}
          fields={[
            {key: 'title', label: target === 'link' ? '标题（必填）' : '标题（留空用作品 / 系列名）', max: 80},
            {key: 'subtitle', label: '副标题', max: 160},
          ]}
        />
        <TargetingFields langs={langs} regions={regions} onLangs={setLangs} onRegions={setRegions} />
        <label className="adm-field">
          开始时间
          <input type="datetime-local" value={starts} onChange={e => setStarts(e.target.value)} required />
        </label>
        <label className="adm-field">
          结束时间（留空 = 长期）
          <input type="datetime-local" value={ends} onChange={e => setEnds(e.target.value)} />
        </label>
        <label className="adm-field">
          优先级（越大越靠前）
          <input type="number" value={priority} onChange={e => setPriority(e.target.value)} step={1} />
        </label>
        <label className="adm-check">
          <input type="checkbox" checked={enabled} onChange={e => setEnabled(e.target.checked)} />
          启用
        </label>
        <div className="adm-form-actions span-all">
          <button type="submit" className="btn btn-primary btn-sm" disabled={act.pending === 'save'}>
            {act.pending === 'save' ? '保存中…' : '保存'}
          </button>
          <button type="button" className="btn btn-secondary btn-sm" onClick={onCancel}>
            取消
          </button>
          {initial && (
            <button type="button" className="btn btn-secondary btn-sm ops-danger" disabled={busy === `del-${initial.id}`} onClick={() => onDelete(initial)}>
              删除
            </button>
          )}
        </div>
      </form>
    </section>
  );
}

// ---------------------------------------------------------------- announcements

const sevTone = (s: Severity): Tone => (s === 'critical' ? 'red' : s === 'warning' ? 'amber' : 'blue');

function Announcements() {
  const list = useAdminLoad(signal => getAdminAnnouncements(signal), []);
  const act = useAdminAction();
  const [editing, setEditing] = useState<AdminAnnouncement | 'new' | null>(null);
  const missing = list.failure && routeMissing(list.failure);
  const now = Date.now();
  const body = (a: AdminAnnouncement) => a.texts['zh-CN']?.body || Object.values(a.texts).find(t => t.body)?.body || '';

  const remove = (a: AdminAnnouncement) => {
    if (!window.confirm(`删除公告「${body(a).slice(0, 30)}」？`)) return;
    void act.attempt(`del-${a.id}`, () => deleteAnnouncement(a.id), '已删除公告').then(v => {
      if (v) {
        setEditing(null);
        list.reload();
      }
    });
  };

  return (
    <>
      {missing ? <NotDeployed what="公告接口（admin/announcements）" /> : <AdminNotice error={list.error || act.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />}
      {editing && (
        <AnnouncementForm
          key={editing === 'new' ? 'new' : editing.id}
          initial={editing === 'new' ? null : editing}
          onSaved={created => {
            act.setNotice(created ? '已新建公告' : '已保存公告');
            setEditing(null);
            list.reload();
          }}
          onCancel={() => setEditing(null)}
          onDelete={remove}
        />
      )}
      <section className="adm-card">
        <div className="adm-card-head">
          <h2>公告条</h2>
          <button type="button" className="btn btn-primary btn-sm" onClick={() => setEditing('new')} disabled={!!missing}>
            新建公告
          </button>
        </div>
        <div className="adm-table-wrap">
          <table className="adm-table">
            <thead>
              <tr>
                <th>类型</th>
                <th>内容</th>
                <th>定向</th>
                <th>时间</th>
                <th>状态</th>
                <th>操作人</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {list.data && list.data.length === 0 && <EmptyRow cols={7}>还没有公告</EmptyRow>}
              {(list.data ?? []).map(a => {
                const on = live(a, now);
                const ended = !!a.ends_at && new Date(a.ends_at).getTime() <= now;
                return (
                  <tr key={a.id}>
                    <td>
                      <Pill tone={sevTone(a.severity)}>{SEVERITY_LABELS[a.severity]}</Pill>
                    </td>
                    <td className="wrap" style={{maxWidth: 360}}>
                      {body(a)}
                      <div className="adm-source-meta">
                        {Object.keys(a.texts).join(' / ')}
                        {a.link_url && ` · ${a.link_url}`}
                        {!a.dismissible && ' · 不可关闭'}
                      </div>
                    </td>
                    <td className="wrap">{targeting(a.langs, a.regions)}</td>
                    <td>
                      {fmtTime(a.starts_at)}
                      <div className="adm-source-meta">至 {a.ends_at ? fmtTime(a.ends_at) : '长期'}</div>
                    </td>
                    <td>
                      <Pill tone={on ? 'green' : ended || !a.enabled ? 'plain' : 'blue'}>{on ? '展示中' : !a.enabled ? '已停用' : ended ? '已结束' : '待上线'}</Pill>
                    </td>
                    <td>{a.updated_by || a.created_by}</td>
                    <td>
                      <button type="button" className="adm-link" onClick={() => setEditing(a)}>
                        编辑
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </section>
    </>
  );
}

function AnnouncementForm({
  initial,
  onSaved,
  onCancel,
  onDelete,
}: {
  initial: AdminAnnouncement | null;
  onSaved: (created: boolean) => void;
  onCancel: () => void;
  onDelete: (a: AdminAnnouncement) => void;
}) {
  const act = useAdminAction();
  const [severity, setSeverity] = useState<Severity>(initial?.severity ?? 'info');
  const [texts, setTexts] = useState<OpsTexts>(initial?.texts ?? {});
  const [link, setLink] = useState(initial?.link_url ?? '');
  const [langs, setLangs] = useState<string[]>(initial?.langs ?? []);
  const [regions, setRegions] = useState((initial?.regions ?? []).join(', '));
  const [starts, setStarts] = useState(toLocalInput(initial?.starts_at ?? new Date().toISOString()));
  const [ends, setEnds] = useState(toLocalInput(initial?.ends_at));
  const [dismissible, setDismissible] = useState(initial?.dismissible ?? true);
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const body: AnnouncementInput = {
      severity,
      texts: cleanTexts(texts),
      link_url: link.trim(),
      langs,
      regions: splitRegions(regions),
      starts_at: fromLocalInput(starts) ?? '',
      ends_at: fromLocalInput(ends),
      dismissible,
      enabled,
    };
    void act.attempt('save', () => (initial ? updateAnnouncement(initial.id, body) : createAnnouncement(body))).then(v => v && onSaved(!initial));
  };

  return (
    <section className="adm-card ops-form">
      <div className="adm-card-head">
        <h2>{initial ? `编辑公告 #${initial.id}` : '新建公告'}</h2>
        {initial && (
          <span className="muted">
            最后修改 {initial.updated_by || '—'} · {fmtTime(initial.updated_at)}（修改后已关闭的访客会重新看到）
          </span>
        )}
      </div>
      <AdminNotice error={act.error} onClose={() => act.setError('')} />
      <form className="adm-form-grid" onSubmit={submit}>
        <label className="adm-field">
          类型
          <select value={severity} onChange={e => setSeverity(e.target.value as Severity)}>
            {SEVERITIES.map(s => (
              <option key={s} value={s}>
                {SEVERITY_LABELS[s]}
              </option>
            ))}
          </select>
        </label>
        <label className="adm-field">
          链接（可选：站内路径或 https:// 地址）
          <input value={link} onChange={e => setLink(e.target.value)} placeholder="/legal/terms" />
        </label>
        <TextsEditor
          value={texts}
          onChange={setTexts}
          fields={[
            {key: 'body', label: '公告内容', max: 300, multiline: true},
            {key: 'link_label', label: '链接文字（默认「了解详情」）', max: 40},
          ]}
        />
        <TargetingFields langs={langs} regions={regions} onLangs={setLangs} onRegions={setRegions} />
        <label className="adm-field">
          开始时间
          <input type="datetime-local" value={starts} onChange={e => setStarts(e.target.value)} required />
        </label>
        <label className="adm-field">
          结束时间（留空 = 长期）
          <input type="datetime-local" value={ends} onChange={e => setEnds(e.target.value)} />
        </label>
        <label className="adm-check">
          <input type="checkbox" checked={dismissible} onChange={e => setDismissible(e.target.checked)} />
          用户可关闭
        </label>
        <label className="adm-check">
          <input type="checkbox" checked={enabled} onChange={e => setEnabled(e.target.checked)} />
          启用
        </label>
        <div className="adm-form-actions span-all">
          <button type="submit" className="btn btn-primary btn-sm" disabled={act.pending === 'save'}>
            {act.pending === 'save' ? '保存中…' : '保存'}
          </button>
          <button type="button" className="btn btn-secondary btn-sm" onClick={onCancel}>
            取消
          </button>
          {initial && (
            <button type="button" className="btn btn-secondary btn-sm ops-danger" onClick={() => onDelete(initial)}>
              删除
            </button>
          )}
        </div>
      </form>
    </section>
  );
}

// ---------------------------------------------------------------- preview

function Preview() {
  const [at, setAt] = useState(() => toLocalInput(new Date().toISOString()));
  const [lang, setLang] = useState('zh-CN');
  const [region, setRegion] = useState('');
  const [query, setQuery] = useState({at, lang, region});
  const data = useAdminLoad(signal => previewHome({at: fromLocalInput(query.at) ?? undefined, lang: query.lang, region: query.region.trim().toUpperCase()}, signal), [
    query.at,
    query.lang,
    query.region,
  ]);
  const p = data.data;
  return (
    <>
      <AdminNotice error={data.error} />
      <section className="adm-card">
        <form
          className="adm-toolbar"
          onSubmit={e => {
            e.preventDefault();
            setQuery({at, lang, region});
          }}
        >
          <label className="adm-field">
            时间
            <input type="datetime-local" value={at} onChange={e => setAt(e.target.value)} />
          </label>
          <label className="adm-field">
            语言
            <select value={lang} onChange={e => setLang(e.target.value)}>
              {LANGS.map(l => (
                <option key={l} value={l}>
                  {LANG_LABELS[l]}
                </option>
              ))}
            </select>
          </label>
          <label className="adm-field">
            地区
            <input value={region} onChange={e => setRegion(e.target.value)} placeholder="例如 JP（留空 = 未知地区）" maxLength={2} />
          </label>
          <button type="submit" className="btn btn-primary btn-sm" disabled={data.busy}>
            {data.busy ? '加载中…' : '预览'}
          </button>
        </form>
        <p className="adm-field-hint">与前台接口同一套规则：只含启用、在时间窗内、语言 / 地区匹配、且目标已公开的排期。</p>
      </section>
      {p && (
        <>
          <section className="adm-card">
            <h2>公告条（{p.announcements.length}）</h2>
            {p.announcements.length === 0 && <p className="muted">此时没有公告。</p>}
            {p.announcements.map(a => (
              <div key={a.id} className={`ops-preview-ann sev-${a.severity}`}>
                <Pill tone={sevTone(a.severity)}>{SEVERITY_LABELS[a.severity]}</Pill> {a.body}
                {a.link_url && <span className="muted"> · {a.link_label || '了解详情'} → {a.link_url}</span>}
              </div>
            ))}
          </section>
          {(['hero', 'shelf'] as const).map(type => (
            <section key={type} className="adm-card">
              <h2>
                {SLOT_TYPE_LABELS[type]}（{p[type].length}）{type === 'hero' && p.hero.length === 0 && <span className="muted">：前台用新上线带封面的作品补位</span>}
              </h2>
              <div className="ops-preview-grid">
                {p[type].map((s, i) => (
                  <div key={s.id} className="ops-preview-card">
                    {s.cover ? <img src={s.cover.url} alt="" loading="lazy" /> : <span className="ops-preview-ph">无封面</span>}
                    <div>
                      <strong>
                        {i + 1}. {s.title}
                      </strong>
                      {s.subtitle && <div className="muted">{s.subtitle}</div>}
                      <div className="adm-source-meta mono">{s.href}</div>
                    </div>
                  </div>
                ))}
              </div>
            </section>
          ))}
        </>
      )}
    </>
  );
}
