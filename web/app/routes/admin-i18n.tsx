import {useEffect, useMemo, useState, type FormEvent} from 'react';
import {Link, useSearchParams} from 'react-router';
import {AdminHeader, AdminNotice, EmptyRow, MoreButton, Pill, fmtTime, statusTone} from '../components/AdminUI';
import {
  I18N_LANGS,
  LANG_NAMES,
  TRANSLATION_LABELS,
  TRANSLATION_STATUSES,
  adminErrorText,
  createGlossary,
  deleteGlossary,
  getGlossary,
  getTranslationStats,
  getTranslationTasks,
  getUIStrings,
  markStale,
  putUIString,
  reviewTranslation,
  saveTranslation,
  submitTranslation,
  updateGlossary,
  type GlossaryEntry,
  type GlossaryInput,
  type TranslationTask,
  type UIString,
} from '../lib/adminApi';
import {useAdmin, useAdminAction, useAdminLoad} from '../lib/adminContext';

export const meta = () => [{title: 'REEL · 翻译管理'}];

// AdminI18n (PRD 8, tech plan 3.10 / 3.13): translation tasks with filters,
// a side-by-side source / translation editor with glossary hints, the
// glossary and the UI strings. Flow: 机器翻译 → 译者修改（草稿）→ 提交审校 →
// 审校通过才上线; a reviewed translation whose source changed becomes 需更新.

type View = 'tasks' | 'glossary' | 'ui';
const TARGET_LANGS = I18N_LANGS.filter(l => l !== 'zh-CN');
const ENTITY_LABELS: Record<string, string> = {release: '视频', performer: '演员', tag: '标签'};
const FIELD_LABELS: Record<string, string> = {title: '标题', synopsis: '简介', name: '名字', biography: '简介'};

const taskKey = (t: TranslationTask) => `${t.entity_type}/${t.entity_id}/${t.field}/${t.lang}`;

function taskStatus(t: TranslationTask) {
  if (t.status === 'draft' && t.submitted) return {label: '审校中', tone: 'blue' as const};
  if (t.outdated && t.status !== 'missing') return {label: '需更新', tone: 'amber' as const};
  return {label: TRANSLATION_LABELS[t.status] ?? t.status, tone: statusTone(t.status)};
}

function entityHref(t: TranslationTask) {
  if (t.entity_type === 'release') return `/admin/videos/${t.entity_id}?tab=i18n`;
  return null;
}

function Editor({task, glossary, onSaved}: {task: TranslationTask; glossary: GlossaryEntry[]; onSaved: (t: TranslationTask) => void}) {
  const act = useAdminAction();
  const [value, setValue] = useState(task.value ?? '');
  useEffect(() => setValue(task.value ?? ''), [task]);
  const hints = useMemo(() => {
    const src = (task.source ?? '').toLowerCase();
    return glossary.filter(g => g.lang === task.lang && src.includes(g.term.toLowerCase()));
  }, [glossary, task]);
  const dirty = value.trim() !== (task.value ?? '');
  const st = taskStatus(task);
  const canSubmit = !dirty && task.value && (task.status === 'machine' || (task.status === 'draft' && !task.submitted));
  const canReview = !dirty && task.value && ((task.status === 'draft' && task.submitted) || task.status === 'stale');

  const save = () => act.attempt('save', () => saveTranslation(task, value.trim()), '草稿已保存').then(t => t && onSaved(t));
  const submit = () => act.attempt('submit', () => submitTranslation(task), '已提交审校').then(t => t && onSaved(t));
  const review = () => act.attempt('review', () => reviewTranslation(task), '审校通过，已上线').then(t => t && onSaved(t));
  const href = entityHref(task);

  return (
    <section className="adm-card adm-editor" aria-label="翻译编辑">
      <div className="adm-card-head">
        <h2>
          {ENTITY_LABELS[task.entity_type] ?? task.entity_type} · {FIELD_LABELS[task.field] ?? task.field}
        </h2>
        <Pill tone={st.tone}>{st.label}</Pill>
      </div>
      <p className="adm-field-hint mono">
        {taskKey(task)}
        {href && (
          <>
            {' '}
            · <Link to={href}>打开视频</Link>
          </>
        )}
      </p>
      <div className="adm-field">
        原文
        <div className="adm-src">{task.source ?? <span className="muted">原文已不存在</span>}</div>
      </div>
      {task.reviewed_value && task.reviewed_value !== task.value && (
        <div className="adm-field">
          前台当前显示（已审核版本）
          <div className="adm-src">{task.reviewed_value}</div>
        </div>
      )}
      <label className="adm-field">
        {LANG_NAMES[task.lang] ?? task.lang}（你的翻译）
        <textarea rows={task.field === 'synopsis' || task.field === 'biography' ? 8 : 3} value={value} onChange={e => setValue(e.target.value)} maxLength={20000} />
        <span className="adm-field-hint">
          {value.length} 字符{task.status === 'machine' ? ' · 当前为机器翻译' : ''}
          {task.updated_by ? ` · 上次 ${task.updated_by} ${fmtTime(task.updated_at)}` : ''}
        </span>
      </label>
      <div className="adm-field">
        术语表
        <div className="adm-gloss">
          {hints.length === 0 && <span className="muted">原文里没有术语表中的词</span>}
          {hints.map(g => (
            <Pill key={g.id} tone="blue" title={g.note}>
              {g.term} → {g.translation}
            </Pill>
          ))}
        </div>
      </div>
      <AdminNotice error={act.error} notice={act.notice} />
      <div className="adm-form-actions">
        <button type="button" className="btn btn-secondary btn-sm" disabled={!dirty || !value.trim() || act.pending !== null} onClick={() => void save()}>
          保存草稿
        </button>
        <button type="button" className="btn btn-secondary btn-sm" disabled={!canSubmit || act.pending !== null} onClick={() => void submit()}>
          提交审校
        </button>
        <button type="button" className="btn btn-primary btn-sm" disabled={!canReview || act.pending !== null} onClick={() => void review()}>
          审校通过
        </button>
      </div>
      <p className="adm-field-hint">先保存草稿，再提交审校；审校通过后才在前台显示。原文改动后，已上线译文标为「需更新」，前台继续显示旧译文。</p>
    </section>
  );
}

function TasksView({glossary}: {glossary: GlossaryEntry[]}) {
  const [params, setParams] = useSearchParams();
  const lang = params.get('lang') ?? 'en';
  const status = params.get('status') ?? '';
  const entityType = params.get('entity_type') ?? '';
  const missing = params.get('missing') === '1' || params.get('missing') === 'true';
  const {hasToken, epoch} = useAdmin();
  const stats = useAdminLoad(() => getTranslationStats());
  const act = useAdminAction();
  const [items, setItems] = useState<TranslationTask[]>([]);
  const [next, setNext] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [loaded, setLoaded] = useState(false);
  const [sel, setSel] = useState<string | null>(null);

  const load = async (cursor: string | null) => {
    setBusy(true);
    setError('');
    try {
      const page = await getTranslationTasks({lang, status: missing ? '' : status, entity_type: entityType, missing, cursor, limit: 50});
      setItems(prev => (cursor ? [...prev, ...page.items] : page.items));
      setNext(page.next_cursor);
      if (!cursor) setSel(page.items[0] ? taskKey(page.items[0]) : null);
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
  }, [hasToken, epoch, lang, status, entityType, missing]);

  const setFilter = (k: string, v: string) => {
    const p = new URLSearchParams(params);
    if (v) p.set(k, v);
    else p.delete(k);
    if (k === 'status' && v) p.delete('missing');
    if (k === 'missing' && v) p.delete('status');
    setParams(p, {replace: true});
  };

  const total = stats.data?.total_fields ?? 0;
  const byLang = stats.data?.by_lang ?? {};
  const cur = items.find(t => taskKey(t) === sel) ?? null;
  const replace = (t: TranslationTask) => {
    setItems(prev => prev.map(x => (taskKey(x) === taskKey(t) ? t : x)));
    stats.reload();
  };

  return (
    <>
      <section className="adm-lang-cards" aria-label="语言">
        {TARGET_LANGS.map(l => {
          const s = byLang[l] ?? {};
          const pct = total ? Math.floor(((s.reviewed ?? 0) / total) * 100) : 0;
          return (
            <button key={l} type="button" className="adm-lang-card" aria-pressed={lang === l} onClick={() => setFilter('lang', l)}>
              <span>{LANG_NAMES[l]}</span>
              <span className="adm-lang-pct">{total ? `${pct}%` : '—'}</span>
              <span className="adm-field-hint">
                缺 {s.missing ?? 0} · 机翻 {s.machine ?? 0} · 需更新 {s.stale ?? 0}
              </span>
            </button>
          );
        })}
      </section>
      <AdminNotice error={stats.error || act.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />

      <div className="adm-toolbar">
        <div className="adm-chips" role="group" aria-label="状态">
          <button type="button" className="adm-chip" aria-pressed={!status && !missing} onClick={() => {
              const p = new URLSearchParams(params);
              p.delete('status');
              p.delete('missing');
              setParams(p, {replace: true});
            }}
          >
            全部
          </button>
          <button type="button" className="adm-chip" aria-pressed={missing} onClick={() => setFilter('missing', missing ? '' : '1')}>
            缺失 {byLang[lang]?.missing ?? ''}
          </button>
          {TRANSLATION_STATUSES.map(s => (
            <button key={s} type="button" className="adm-chip" aria-pressed={!missing && status === s} onClick={() => setFilter('status', status === s ? '' : s)}>
              {TRANSLATION_LABELS[s]} {byLang[lang]?.[s] ?? ''}
            </button>
          ))}
        </div>
        <select aria-label="类型" value={entityType} onChange={e => setFilter('entity_type', e.target.value)}>
          <option value="">全部类型</option>
          {Object.entries(ENTITY_LABELS).map(([k, v]) => (
            <option key={k} value={k}>
              {v}
            </option>
          ))}
        </select>
        <span style={{flex: 1}} />
        <button
          type="button"
          className="btn btn-secondary btn-sm"
          disabled={act.pending === 'stale'}
          onClick={() => void act.attempt('stale', () => markStale(), v => `已标记 ${v.marked_stale} 条为需更新`).then(v => v && (stats.reload(), load(null)))}
        >
          检查原文变动
        </button>
      </div>
      <AdminNotice error={error} />

      <div className="adm-i18n">
        <section className="adm-card">
          <div className="adm-table-wrap">
            <table className="adm-table">
              <thead>
                <tr>
                  <th>类型</th>
                  <th>原文</th>
                  <th>{LANG_NAMES[lang]}</th>
                  <th>状态</th>
                </tr>
              </thead>
              <tbody>
                {loaded && items.length === 0 && <EmptyRow cols={4}>没有符合条件的翻译</EmptyRow>}
                {items.map(t => {
                  const st = taskStatus(t);
                  const k = taskKey(t);
                  return (
                    <tr key={k} className={`clickable ${sel === k ? 'sel' : ''}`} onClick={() => setSel(k)}>
                      <td>
                        {ENTITY_LABELS[t.entity_type] ?? t.entity_type}
                        <div className="adm-source-meta">
                          {FIELD_LABELS[t.field] ?? t.field} · #{t.entity_id}
                        </div>
                      </td>
                      <td className="wrap">
                        <span className="adm-clip">{t.source ?? '—'}</span>
                      </td>
                      <td className="wrap">
                        <span className="adm-clip">{t.value || <span className="muted">—</span>}</span>
                      </td>
                      <td>
                        <Pill tone={st.tone}>{st.label}</Pill>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
          <MoreButton next={next} busy={busy} onMore={() => void load(next)} />
        </section>
        {cur ? (
          <Editor key={sel ?? ''} task={cur} glossary={glossary} onSaved={replace} />
        ) : (
          <section className="adm-card">
            <p className="muted">选择左侧一条开始翻译。</p>
          </section>
        )}
      </div>
    </>
  );
}

const blankGloss: GlossaryInput = {term: '', lang: 'en', translation: '', note: ''};

function GlossaryView({items, onChanged, error}: {items: GlossaryEntry[]; onChanged: () => void; error: string}) {
  const act = useAdminAction();
  const [editing, setEditing] = useState<number | 'new' | null>(null);
  const [form, setForm] = useState<GlossaryInput>(blankGloss);
  const [lang, setLang] = useState('');
  const [q, setQ] = useState('');

  const start = (g?: GlossaryEntry) => {
    setEditing(g ? g.id : 'new');
    setForm(g ? {term: g.term, lang: g.lang, translation: g.translation, note: g.note} : {...blankGloss, lang: lang || 'en'});
  };
  const save = async (e: FormEvent) => {
    e.preventDefault();
    const ok =
      editing === 'new'
        ? await act.attempt('save', () => createGlossary(form), `已保存「${form.term}」`)
        : await act.attempt('save', () => updateGlossary(editing as number, form), `已保存「${form.term}」`);
    if (ok) {
      setEditing(null);
      onChanged();
    }
  };
  const remove = async (g: GlossaryEntry) => {
    const ok = await act.attempt(`del-${g.id}`, () => deleteGlossary(g.id), `已删除「${g.term}」`);
    if (ok) onChanged();
  };

  const shown = items.filter(g => (!lang || g.lang === lang) && (!q || g.term.includes(q) || g.translation.toLowerCase().includes(q.toLowerCase())));

  return (
    <section className="adm-card">
      <div className="adm-card-head">
        <h2>术语表（{items.length}）</h2>
        <button type="button" className="btn btn-secondary btn-sm" onClick={() => start()}>
          + 新术语
        </button>
      </div>
      <p className="adm-field-hint">译者编辑时，原文里出现的术语会显示在编辑框下面。同一术语每种语言一条。</p>
      <AdminNotice error={error || act.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />
      <div className="adm-toolbar">
        <select aria-label="语言" value={lang} onChange={e => setLang(e.target.value)}>
          <option value="">全部语言</option>
          {I18N_LANGS.map(l => (
            <option key={l} value={l}>
              {LANG_NAMES[l]}
            </option>
          ))}
        </select>
        <input type="search" placeholder="搜索术语或译文" value={q} onChange={e => setQ(e.target.value)} aria-label="搜索术语" />
      </div>
      {editing !== null && (
        <form className="adm-panel" onSubmit={save}>
          <div className="adm-form-grid">
            <label className="adm-field">
              术语（原文）
              <input required maxLength={500} value={form.term} onChange={e => setForm({...form, term: e.target.value})} />
            </label>
            <label className="adm-field">
              语言
              <select value={form.lang} onChange={e => setForm({...form, lang: e.target.value})}>
                {I18N_LANGS.map(l => (
                  <option key={l} value={l}>
                    {LANG_NAMES[l]}
                  </option>
                ))}
              </select>
            </label>
            <label className="adm-field">
              译法
              <input required maxLength={2000} value={form.translation} onChange={e => setForm({...form, translation: e.target.value})} />
            </label>
            <label className="adm-field">
              备注
              <input maxLength={2000} value={form.note} onChange={e => setForm({...form, note: e.target.value})} />
            </label>
          </div>
          <div className="adm-form-actions">
            <button type="submit" className="btn btn-primary btn-sm" disabled={act.pending === 'save'}>
              保存
            </button>
            <button type="button" className="btn btn-secondary btn-sm" onClick={() => setEditing(null)}>
              取消
            </button>
          </div>
        </form>
      )}
      <div className="adm-table-wrap">
        <table className="adm-table">
          <thead>
            <tr>
              <th>术语</th>
              <th>语言</th>
              <th>译法</th>
              <th>备注</th>
              <th>更新</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {shown.length === 0 && <EmptyRow cols={6}>没有术语</EmptyRow>}
            {shown.map(g => (
              <tr key={g.id}>
                <td>{g.term}</td>
                <td>{LANG_NAMES[g.lang] ?? g.lang}</td>
                <td>{g.translation}</td>
                <td className="wrap muted">{g.note || '—'}</td>
                <td>
                  {g.updated_by ?? '—'} · {fmtTime(g.updated_at)}
                </td>
                <td>
                  <div className="actions">
                    <button type="button" className="adm-link" onClick={() => start(g)}>
                      编辑
                    </button>
                    <button type="button" className="adm-link danger" disabled={act.pending === `del-${g.id}`} onClick={() => void remove(g)}>
                      删除
                    </button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function UIRow({u, onSaved}: {u: UIString; onSaved: () => void}) {
  const act = useAdminAction();
  const [value, setValue] = useState(u.value);
  useEffect(() => setValue(u.value), [u]);
  const save = (status: 'draft' | 'reviewed') => act.attempt(status, () => putUIString(u.lang, u.key, value, status), status === 'reviewed' ? '已上线' : '已保存草稿').then(v => v && onSaved());
  return (
    <tr>
      <td className="mono wrap">{u.key}</td>
      <td>
        <textarea className="adm-input" rows={2} style={{width: '100%', minWidth: 260}} value={value} onChange={e => setValue(e.target.value)} aria-label={`${u.key} 译文`} />
        {act.error && <div className="adm-actor-err">{act.error}</div>}
      </td>
      <td>
        <Pill tone={statusTone(u.status)}>{TRANSLATION_LABELS[u.status] ?? u.status}</Pill>
        <div className="adm-source-meta">
          {u.updated_by ?? '—'} · {fmtTime(u.updated_at)}
        </div>
      </td>
      <td>
        <div className="actions">
          <button type="button" className="adm-link" disabled={!value || value === u.value || act.pending !== null} onClick={() => void save('draft')}>
            存草稿
          </button>
          <button type="button" className="adm-link" disabled={!value || (value === u.value && u.status === 'reviewed') || act.pending !== null} onClick={() => void save('reviewed')}>
            审校上线
          </button>
        </div>
      </td>
    </tr>
  );
}

function UIStringsView() {
  const [lang, setLang] = useState('en');
  const [status, setStatus] = useState('');
  const [prefix, setPrefix] = useState('');
  const [applied, setApplied] = useState('');
  const list = useAdminLoad(() => getUIStrings({lang, status, prefix: applied}), [lang, status, applied]);
  const act = useAdminAction();
  const [key, setKey] = useState('');
  const [value, setValue] = useState('');

  const add = async (e: FormEvent) => {
    e.preventDefault();
    const ok = await act.attempt('add', () => putUIString(lang, key.trim(), value, 'draft'), `已添加 ${key.trim()}`);
    if (ok) {
      setKey('');
      setValue('');
      list.reload();
    }
  };

  return (
    <section className="adm-card">
      <div className="adm-card-head">
        <h2>界面文字</h2>
        <span className="muted">只有「已上线」的会通过 /api/i18n/ui/{'{lang}'} 下发到前台</span>
      </div>
      <form
        className="adm-toolbar"
        onSubmit={e => {
          e.preventDefault();
          setApplied(prefix.trim());
        }}
      >
        <select aria-label="语言" value={lang} onChange={e => setLang(e.target.value)}>
          {I18N_LANGS.map(l => (
            <option key={l} value={l}>
              {LANG_NAMES[l]}
            </option>
          ))}
        </select>
        <select aria-label="状态" value={status} onChange={e => setStatus(e.target.value)}>
          <option value="">全部状态</option>
          {['machine', 'draft', 'reviewed'].map(s => (
            <option key={s} value={s}>
              {TRANSLATION_LABELS[s]}
            </option>
          ))}
        </select>
        <input placeholder="键前缀，例如 player." value={prefix} onChange={e => setPrefix(e.target.value)} aria-label="键前缀" />
        <button type="submit" className="btn btn-secondary btn-sm">
          筛选
        </button>
      </form>
      <AdminNotice error={list.error || act.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />
      <div className="adm-table-wrap">
        <table className="adm-table">
          <thead>
            <tr>
              <th>键</th>
              <th>{LANG_NAMES[lang]}</th>
              <th>状态</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {list.data?.length === 0 && <EmptyRow cols={4}>没有界面文字</EmptyRow>}
            {list.data?.map(u => <UIRow key={`${u.lang}-${u.key}`} u={u} onSaved={list.reload} />)}
          </tbody>
        </table>
      </div>
      <form className="adm-toolbar" onSubmit={add}>
        <label className="adm-field">
          新键
          <input required pattern="[A-Za-z0-9][A-Za-z0-9._\-]{0,199}" value={key} onChange={e => setKey(e.target.value)} placeholder="player.sources" />
        </label>
        <label className="adm-field" style={{flex: 1, minWidth: 260}}>
          {LANG_NAMES[lang]} 文字
          <input required maxLength={4000} value={value} onChange={e => setValue(e.target.value)} />
        </label>
        <button type="submit" className="btn btn-secondary btn-sm" disabled={act.pending === 'add'}>
          添加为草稿
        </button>
      </form>
    </section>
  );
}

export default function AdminI18n() {
  const [params, setParams] = useSearchParams();
  const view = (params.get('view') as View) || 'tasks';
  const glossary = useAdminLoad(() => getGlossary({limit: 1000}));
  const setView = (v: View) => setParams(v === 'tasks' ? {} : {view: v}, {replace: true});

  return (
    <>
      <AdminHeader eyebrow="ADMIN · I18N" title="翻译管理" sub="源语言：简体中文。机器翻译 → 译者修改 → 审校通过才上线。" />
      <div className="adm-tabs" role="tablist">
        {(
          [
            ['tasks', '翻译任务'],
            ['glossary', '术语表'],
            ['ui', '界面文字'],
          ] as [View, string][]
        ).map(([k, t]) => (
          <button key={k} type="button" role="tab" className="adm-tab" aria-selected={view === k} onClick={() => setView(k)}>
            {t}
          </button>
        ))}
      </div>
      {view === 'tasks' && <TasksView glossary={glossary.data ?? []} />}
      {view === 'glossary' && <GlossaryView items={glossary.data ?? []} onChanged={glossary.reload} error={glossary.error} />}
      {view === 'ui' && <UIStringsView />}
    </>
  );
}
