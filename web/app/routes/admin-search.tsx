import {Fragment, useState, type FormEvent, type ReactNode} from 'react';
import {Link, useSearchParams} from 'react-router';
import {PerformerPicker, ReleasePicker, type Picked} from '../components/AdminPickers';
import {AdminHeader, AdminNotice, EmptyRow, NotDeployed, Pill, fmtNum, fmtTime, type Tone} from '../components/AdminUI';
import {isMissingEndpoint} from '../lib/api';
import {
  aliasFromTerm,
  blockTerm,
  createRedirect,
  createSynonym,
  deleteRedirect,
  deleteSynonym,
  englishTitleFromTerm,
  getBlocklist,
  getRedirects,
  getSearchStats,
  getSynonyms,
  getZeroResultTerms,
  unblockTerm,
  updateRedirect,
  updateSynonym,
  type NearMatch,
  type SearchStats,
  type ZeroResultTerm,
} from '../lib/adminApi';
import {useAdminAction, useAdminLoad} from '../lib/adminContext';

export const meta = () => [{title: 'REEL · 搜索词情报'}];

// AdminSearch (PRD 4.5, 11; tech plan 3.4, 3.13): what people search, what
// found nothing and why, and one-click fixes. Stats and zero-result terms
// come from search_query_daily (the search_stats job); every fix writes a
// rule (synonym / redirect / blocklist) or catalog metadata (performer alias,
// reviewed English title) with a review_events row. Without the search
// backend every route 404s and the page says 后端未部署.

const TABS = [
  {key: 'zero', label: '无结果搜索词'},
  {key: 'synonyms', label: '同义词'},
  {key: 'redirects', label: '跳转'},
  {key: 'blocklist', label: '屏蔽词'},
] as const;
type TabKey = (typeof TABS)[number]['key'];

const pct = (v?: number | null) => (v === null || v === undefined ? '—' : `${(v * 100).toFixed(1)}%`);

const CLASS_TONE: Record<string, Tone> = {not_in_library: 'red', in_library_missed: 'amber', not_video: 'plain'};
const ACTION_LABELS: Record<string, string> = {
  add_synonym: '加同义词',
  add_redirect: '建跳转',
  add_alias: '加演员别名',
  add_english_title: '补英文标题',
  add_release: '补片',
  create_upcoming: '建预约页',
  block: '屏蔽',
};
const FIELD_LABELS: Record<string, string> = {
  title: '标题',
  title_ja: '日文标题',
  name: '名字',
  name_ja: '日文名',
  alias: '别名',
};
const fieldLabel = (f: string) => FIELD_LABELS[f] ?? (f.startsWith('translation:') ? `译名 ${f.slice(12)}` : f);

function StatsHeader({stats}: {stats: SearchStats}) {
  const t = stats.totals;
  const zeroTone = t.searches === 0 ? '' : t.meets_target ? 'green' : 'amber';
  return (
    <section className="adm-kpis" aria-label="搜索指标">
      <div className="adm-kpi">
        <span className="adm-kpi-label">搜索次数（{stats.days} 天）</span>
        <span className="adm-kpi-value">{fmtNum(t.searches)}</span>
        <span className="adm-kpi-sub">无结果 {fmtNum(t.zero_results)} · 点击 {fmtNum(t.clicks)}</span>
      </div>
      <div className="adm-kpi">
        <span className="adm-kpi-label">无结果率</span>
        <span className={`adm-kpi-value ${zeroTone}`}>{pct(t.zero_result_rate)}</span>
        <span className="adm-kpi-sub">
          目标 &lt; {pct(t.zero_result_target || 0.05)}
          {t.searches > 0 && (t.meets_target ? ' · 达标' : ' · 未达标')}
        </span>
      </div>
      <div className="adm-kpi">
        <span className="adm-kpi-label">搜索后点击率</span>
        <span className="adm-kpi-value">{pct(t.ctr)}</span>
        <span className="adm-kpi-sub">点了第一页的 {pct(t.first_page_ctr)}</span>
      </div>
      <div className="adm-kpi">
        <span className="adm-kpi-label">二次搜索挽回</span>
        <span className={`adm-kpi-value ${t.rescued > 0 ? 'green' : ''}`}>{pct(t.rescue_rate)}</span>
        <span className="adm-kpi-sub">无结果的词去掉修饰词后找到了（{fmtNum(t.rescued)} 次）</span>
      </div>
      <div className="adm-kpi">
        <span className="adm-kpi-label">模糊匹配</span>
        <span className={`adm-kpi-value ${stats.trgm ? 'green' : 'amber'}`} style={{fontSize: 20}}>
          {stats.trgm ? 'pg_trgm' : 'ILIKE'}
        </span>
        <span className="adm-kpi-sub">{stats.trgm ? '已启用三元组近似匹配' : '未装 pg_trgm：只做包含匹配，错字搜不到'}</span>
      </div>
    </section>
  );
}

function NearMatches({items}: {items: NearMatch[]}) {
  if (!items.length) return <span className="muted">—</span>;
  return (
    <ul className="adm-near">
      {items.slice(0, 4).map(m => (
        <li key={`${m.type}-${m.id}-${m.field}`}>
          <Pill tone={m.type === 'performer' ? 'purple' : 'blue'}>{m.type === 'performer' ? '演员' : '视频'}</Pill>{' '}
          {m.type === 'release' ? <Link to={`/admin/videos/${m.id}`}>{m.name}</Link> : <Link to={`/admin/actors?id=${m.id}`}>{m.name}</Link>}{' '}
          <span className="muted">
            {fieldLabel(m.field)} · {Math.round(m.similarity * 100)}%{m.status && m.status !== 'published' && m.status !== 'active' ? ` · ${m.status}` : ''}
          </span>
        </li>
      ))}
    </ul>
  );
}

type FixKind = 'add_synonym' | 'add_redirect' | 'add_alias' | 'add_english_title' | 'block';

function FixForm({term, kind, onDone, onCancel}: {term: ZeroResultTerm; kind: FixKind; onDone: (msg: string) => void; onCancel: () => void}) {
  const act = useAdminAction();
  const near = term.near_matches ?? [];
  const firstRelease = near.find(m => m.type === 'release');
  const [canonical, setCanonical] = useState(term.stripped || near[0]?.name || '');
  const [target, setTarget] = useState('');
  const [reason, setReason] = useState('');
  const [value, setValue] = useState(term.term);
  const [performer, setPerformer] = useState<Picked | null>(null);
  const [release, setRelease] = useState<Picked | null>(null);
  const quick = (type: string): Picked[] => near.filter(m => m.type === type).map(m => ({id: m.id, slug: '', name: m.name, sub: fieldLabel(m.field)}));

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const go = async <T,>(fn: () => Promise<T>, msg: (v: T) => string) => {
      const v = await act.attempt('fix', fn);
      if (v) onDone(msg(v));
    };
    if (kind === 'add_synonym') await go(() => createSynonym(term.term, canonical.trim()), v => `已加同义词：${v.term} → ${v.canonical}`);
    else if (kind === 'add_redirect') await go(() => createRedirect(term.term, target.trim(), reason.trim()), v => `已建跳转：${v.term} → ${v.target_url}`);
    else if (kind === 'block') await go(() => blockTerm(term.term, reason.trim()), v => `已屏蔽「${v.term}」，不会再出现在热搜里`);
    else if (kind === 'add_alias') {
      if (!performer) return act.setError('先选择演员');
      await go(() => aliasFromTerm({term: term.term, performer_id: performer.id, alias: value.trim(), reason: reason.trim()}), v => `已给 ${performer.name} 加别名「${v.value}」`);
    } else {
      if (!release) return act.setError('先选择视频');
      await go(() => englishTitleFromTerm({term: term.term, release_id: release.id, title: value.trim(), reason: reason.trim()}), v => `已给 ${release.name} 补英文标题「${v.value}」`);
    }
  };

  let fields: ReactNode;
  if (kind === 'add_synonym') {
    fields = (
      <label className="adm-field">
        <span>搜「{term.term}」时改为搜</span>
        <input required value={canonical} onChange={e => setCanonical(e.target.value)} placeholder="规范词，例如片名或演员名" />
      </label>
    );
  } else if (kind === 'add_redirect') {
    fields = (
      <>
        <label className="adm-field">
          <span>跳转到</span>
          <input required value={target} onChange={e => setTarget(e.target.value)} placeholder="/legal/privacy 或 https://…" />
          <span className="adm-field-hint">站内路径以 / 开头；站外只允许 https</span>
        </label>
        <label className="adm-field">
          <span>原因（可选）</span>
          <input value={reason} onChange={e => setReason(e.target.value)} maxLength={500} />
        </label>
      </>
    );
  } else if (kind === 'block') {
    fields = (
      <label className="adm-field">
        <span>屏蔽原因（可选）</span>
        <input value={reason} onChange={e => setReason(e.target.value)} placeholder="例如 违规词、刷榜" />
      </label>
    );
  } else {
    const isAlias = kind === 'add_alias';
    fields = (
      <>
        {isAlias ? (
          <PerformerPicker quick={quick('performer')} value={performer} onPick={setPerformer} />
        ) : (
          <ReleasePicker quick={quick('release')} value={release} onPick={setRelease} />
        )}
        <label className="adm-field">
          <span>{isAlias ? '别名' : '英文标题'}</span>
          <input required value={value} onChange={e => setValue(e.target.value)} maxLength={300} />
          <span className="adm-field-hint">默认是用户输入的原词</span>
        </label>
        <label className="adm-field">
          <span>理由（可选）</span>
          <input value={reason} onChange={e => setReason(e.target.value)} />
        </label>
      </>
    );
  }

  return (
    <form className="adm-panel adm-fix" onSubmit={e => void submit(e)}>
      <strong>
        {ACTION_LABELS[kind]} · 「{term.term}」
        {kind === 'add_english_title' && firstRelease ? <span className="muted"> 相近：{firstRelease.name}</span> : null}
      </strong>
      <AdminNotice error={act.error} />
      <div className="adm-form-grid">{fields}</div>
      <div className="adm-form-actions">
        <button type="submit" className="btn btn-primary btn-sm" disabled={act.pending === 'fix'}>
          确认{ACTION_LABELS[kind]}
        </button>
        <button type="button" className="btn btn-secondary btn-sm" onClick={onCancel}>
          取消
        </button>
      </div>
    </form>
  );
}

function ZeroResultsTab({days}: {days: number}) {
  const [min, setMin] = useState(20);
  const list = useAdminLoad(() => getZeroResultTerms({days, min, limit: 100}), [days, min]);
  const [open, setOpen] = useState<{term: string; kind: FixKind} | null>(null);
  const [notice, setNotice] = useState('');
  const items = list.data ?? [];

  const fixButtons = (z: ZeroResultTerm) => {
    const acts = [...(z.actions ?? [])];
    return (
      <div className="actions">
        {acts.map(a =>
          a === 'add_release' || a === 'create_upcoming' ? (
            <Link key={a} className="adm-link" to={a === 'add_release' ? '/admin/pipeline' : '/admin/videos'} title={a === 'add_release' ? '去入库流水线补片' : '去视频库建待上线作品'}>
              {ACTION_LABELS[a]}
            </Link>
          ) : (
            <button key={a} type="button" className="adm-link" onClick={() => setOpen({term: z.term, kind: a as FixKind})}>
              {ACTION_LABELS[a] ?? a}
            </button>
          ),
        )}
        <button type="button" className="adm-link" onClick={() => setOpen({term: z.term, kind: 'block'})}>
          屏蔽
        </button>
      </div>
    );
  };

  return (
    <section className="adm-card">
      <div className="adm-card-head">
        <h2>
          无结果搜索词 <span className="muted">近 {days} 天 · 无结果 ≥ {min} 次</span>
        </h2>
        <div className="adm-toolbar">
          <label className="adm-field">
            <span>至少无结果</span>
            <select value={min} onChange={e => setMin(Number(e.target.value))} aria-label="最少无结果次数">
              {[1, 5, 20, 50, 100].map(n => (
                <option key={n} value={n}>
                  {n} 次
                </option>
              ))}
            </select>
          </label>
          <button type="button" className="btn btn-secondary btn-sm" disabled={list.busy} onClick={list.reload}>
            刷新
          </button>
        </div>
      </div>
      <AdminNotice error={list.error} notice={notice} onClose={() => setNotice('')} />
      <p className="adm-field-hint">
        判断：库里没有 → 补片或建预约页；库里有没搜到 → 同义词、别名、英文标题；不是找视频 → 跳转到帮助页。数据来自每天的 search_stats 任务。
      </p>
      <div className="adm-table-wrap">
        <table className="adm-table">
          <thead>
            <tr>
              <th>搜索词</th>
              <th className="num">无结果 / 搜索</th>
              <th>判断</th>
              <th>相近内容</th>
              <th>最近</th>
              <th>处理</th>
            </tr>
          </thead>
          <tbody>
            {!list.busy && list.data && items.length === 0 && <EmptyRow cols={6}>没有达到阈值的无结果搜索词</EmptyRow>}
            {items.map(z => (
              <Fragment key={z.term}>
                <tr className={open?.term === z.term ? 'sel' : ''}>
                  <td className="wrap">
                    <strong>{z.term}</strong>
                    {z.stripped && (
                      <div className="adm-field-hint">
                        去掉修饰词：「{z.stripped}」{z.would_rescue ? ' · 二次搜索可找回' : ' · 仍无结果'}
                      </div>
                    )}
                  </td>
                  <td className="num">
                    {fmtNum(z.zero_results)} / {fmtNum(z.searches)}
                    {z.rescued > 0 && <div className="adm-field-hint">挽回 {fmtNum(z.rescued)}</div>}
                  </td>
                  <td>
                    <Pill tone={CLASS_TONE[z.class] ?? 'plain'}>{z.class_label || z.class}</Pill>
                    {z.resolved && (
                      <div style={{marginTop: 4}}>
                        <Pill tone="green">现在已能搜到</Pill>
                      </div>
                    )}
                  </td>
                  <td className="wrap">
                    <NearMatches items={z.near_matches ?? []} />
                  </td>
                  <td style={{whiteSpace: 'nowrap'}}>{z.last_seen ? z.last_seen.slice(0, 10) : '—'}</td>
                  <td>{fixButtons(z)}</td>
                </tr>
                {open?.term === z.term && (
                  <tr>
                    <td colSpan={6}>
                      <FixForm
                        key={open.kind}
                        term={z}
                        kind={open.kind}
                        onCancel={() => setOpen(null)}
                        onDone={msg => {
                          setOpen(null);
                          setNotice(msg);
                          list.reload();
                        }}
                      />
                    </td>
                  </tr>
                )}
              </Fragment>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function SynonymsTab() {
  const [q, setQ] = useState('');
  const list = useAdminLoad(() => getSynonyms(q.trim()), [q]);
  const act = useAdminAction();
  const [term, setTerm] = useState('');
  const [canonical, setCanonical] = useState('');
  const [edit, setEdit] = useState<{id: number; term: string; canonical: string} | null>(null);

  const add = async (e: FormEvent) => {
    e.preventDefault();
    const ok = await act.attempt('add', () => createSynonym(term.trim(), canonical.trim()), v => `已添加：${v.term} → ${v.canonical}`);
    if (ok) {
      setTerm('');
      setCanonical('');
      list.reload();
    }
  };
  const save = async () => {
    if (!edit) return;
    const ok = await act.attempt(`edit-${edit.id}`, () => updateSynonym(edit.id, edit.term.trim(), edit.canonical.trim()), '已保存');
    if (ok) {
      setEdit(null);
      list.reload();
    }
  };
  const remove = async (id: number, label: string) => {
    if (!window.confirm(`删除同义词「${label}」？`)) return;
    const ok = await act.attempt(`del-${id}`, async () => (await deleteSynonym(id), true), '已删除');
    if (ok) list.reload();
  };

  return (
    <section className="adm-card">
      <div className="adm-card-head">
        <h2>同义词</h2>
        <div className="adm-toolbar">
          <input value={q} onChange={e => setQ(e.target.value)} placeholder="查找词" aria-label="查找同义词" />
        </div>
      </div>
      <p className="adm-field-hint">搜索词（或其中的词）命中时按规范词搜索。用于繁简、错别字、俗称和英文片名。</p>
      <AdminNotice error={act.error || list.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />
      <form className="adm-toolbar" onSubmit={e => void add(e)}>
        <input required value={term} onChange={e => setTerm(e.target.value)} placeholder="搜索词，如 林遙" aria-label="搜索词" />
        <span>→</span>
        <input required value={canonical} onChange={e => setCanonical(e.target.value)} placeholder="规范词，如 林遥" aria-label="规范词" />
        <button type="submit" className="btn btn-primary btn-sm" disabled={act.pending === 'add'}>
          添加
        </button>
      </form>
      <div className="adm-table-wrap">
        <table className="adm-table">
          <thead>
            <tr>
              <th>搜索词</th>
              <th>规范词</th>
              <th>添加人</th>
              <th>更新</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {list.data && list.data.length === 0 && <EmptyRow cols={5}>还没有同义词</EmptyRow>}
            {(list.data ?? []).map(s =>
              edit?.id === s.id ? (
                <tr key={s.id} className="sel">
                  <td>
                    <input className="adm-input" value={edit.term} onChange={e => setEdit({...edit, term: e.target.value})} aria-label="搜索词" />
                  </td>
                  <td>
                    <input className="adm-input" value={edit.canonical} onChange={e => setEdit({...edit, canonical: e.target.value})} aria-label="规范词" />
                  </td>
                  <td>{s.created_by}</td>
                  <td>{fmtTime(s.updated_at)}</td>
                  <td className="actions">
                    <button type="button" className="adm-link" onClick={() => void save()}>
                      保存
                    </button>
                    <button type="button" className="adm-link" onClick={() => setEdit(null)}>
                      取消
                    </button>
                  </td>
                </tr>
              ) : (
                <tr key={s.id}>
                  <td>{s.term}</td>
                  <td>{s.canonical}</td>
                  <td>{s.created_by}</td>
                  <td>{fmtTime(s.updated_at)}</td>
                  <td className="actions">
                    <button type="button" className="adm-link" onClick={() => setEdit({id: s.id, term: s.term, canonical: s.canonical})}>
                      编辑
                    </button>
                    <button type="button" className="adm-link" onClick={() => void remove(s.id, s.term)}>
                      删除
                    </button>
                  </td>
                </tr>
              ),
            )}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function RedirectsTab() {
  const [q, setQ] = useState('');
  const list = useAdminLoad(() => getRedirects(q.trim()), [q]);
  const act = useAdminAction();
  const [form, setForm] = useState({term: '', target: '', reason: ''});
  const [edit, setEdit] = useState<{id: number; term: string; target: string; reason: string} | null>(null);

  const add = async (e: FormEvent) => {
    e.preventDefault();
    const ok = await act.attempt('add', () => createRedirect(form.term.trim(), form.target.trim(), form.reason.trim()), v => `已添加跳转：${v.term} → ${v.target_url}`);
    if (ok) {
      setForm({term: '', target: '', reason: ''});
      list.reload();
    }
  };
  const save = async () => {
    if (!edit) return;
    const ok = await act.attempt(`edit-${edit.id}`, () => updateRedirect(edit.id, edit.term.trim(), edit.target.trim(), edit.reason.trim()), '已保存');
    if (ok) {
      setEdit(null);
      list.reload();
    }
  };
  const remove = async (id: number, label: string) => {
    if (!window.confirm(`删除跳转「${label}」？`)) return;
    const ok = await act.attempt(`del-${id}`, async () => (await deleteRedirect(id), true), '已删除');
    if (ok) list.reload();
  };

  return (
    <section className="adm-card">
      <div className="adm-card-head">
        <h2>跳转</h2>
        <div className="adm-toolbar">
          <input value={q} onChange={e => setQ(e.target.value)} placeholder="查找词" aria-label="查找跳转" />
        </div>
      </div>
      <p className="adm-field-hint">整句命中时直接跳到目标页，不显示搜索结果。适合「怎么下载」「会员充值」这类求助词。</p>
      <AdminNotice error={act.error || list.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />
      <form className="adm-toolbar" onSubmit={e => void add(e)}>
        <input required value={form.term} onChange={e => setForm({...form, term: e.target.value})} placeholder="搜索词" aria-label="搜索词" />
        <input required value={form.target} onChange={e => setForm({...form, target: e.target.value})} placeholder="/legal/privacy 或 https://…" aria-label="跳转到" />
        <input value={form.reason} onChange={e => setForm({...form, reason: e.target.value})} placeholder="原因（可选）" aria-label="原因" maxLength={500} />
        <button type="submit" className="btn btn-primary btn-sm" disabled={act.pending === 'add'}>
          添加
        </button>
      </form>
      <div className="adm-table-wrap">
        <table className="adm-table">
          <thead>
            <tr>
              <th>搜索词</th>
              <th>跳转到</th>
              <th>原因</th>
              <th>添加人</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {list.data && list.data.length === 0 && <EmptyRow cols={5}>还没有跳转规则</EmptyRow>}
            {(list.data ?? []).map(r =>
              edit?.id === r.id ? (
                <tr key={r.id} className="sel">
                  <td>
                    <input className="adm-input" value={edit.term} onChange={e => setEdit({...edit, term: e.target.value})} aria-label="搜索词" />
                  </td>
                  <td>
                    <input className="adm-input" value={edit.target} onChange={e => setEdit({...edit, target: e.target.value})} aria-label="跳转到" />
                  </td>
                  <td>
                    <input className="adm-input" value={edit.reason} onChange={e => setEdit({...edit, reason: e.target.value})} aria-label="原因" />
                  </td>
                  <td>{r.created_by}</td>
                  <td className="actions">
                    <button type="button" className="adm-link" onClick={() => void save()}>
                      保存
                    </button>
                    <button type="button" className="adm-link" onClick={() => setEdit(null)}>
                      取消
                    </button>
                  </td>
                </tr>
              ) : (
                <tr key={r.id}>
                  <td>{r.term}</td>
                  <td className="mono wrap">{r.target_url}</td>
                  <td className="wrap">{r.reason || '—'}</td>
                  <td>{r.created_by}</td>
                  <td className="actions">
                    <button type="button" className="adm-link" onClick={() => setEdit({id: r.id, term: r.term, target: r.target_url, reason: r.reason})}>
                      编辑
                    </button>
                    <button type="button" className="adm-link" onClick={() => void remove(r.id, r.term)}>
                      删除
                    </button>
                  </td>
                </tr>
              ),
            )}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function BlocklistTab() {
  const list = useAdminLoad(() => getBlocklist());
  const act = useAdminAction();
  const [term, setTerm] = useState('');
  const [reason, setReason] = useState('');

  const add = async (e: FormEvent) => {
    e.preventDefault();
    const ok = await act.attempt('add', () => blockTerm(term.trim(), reason.trim()), v => `已屏蔽「${v.term}」`);
    if (ok) {
      setTerm('');
      setReason('');
      list.reload();
    }
  };
  const remove = async (id: number, label: string) => {
    if (!window.confirm(`解除屏蔽「${label}」？`)) return;
    const ok = await act.attempt(`del-${id}`, async () => (await unblockTerm(id), true), '已解除屏蔽');
    if (ok) list.reload();
  };

  return (
    <section className="adm-card">
      <h2>屏蔽词</h2>
      <p className="adm-field-hint">屏蔽的词不会出现在「大家都在搜」和热搜词里（审核词库里的词也会自动排除）。不影响用户搜索本身。</p>
      <AdminNotice error={act.error || list.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />
      <form className="adm-toolbar" onSubmit={e => void add(e)}>
        <input required value={term} onChange={e => setTerm(e.target.value)} placeholder="搜索词" aria-label="屏蔽词" />
        <input value={reason} onChange={e => setReason(e.target.value)} placeholder="原因（可选）" aria-label="原因" />
        <button type="submit" className="btn btn-primary btn-sm" disabled={act.pending === 'add'}>
          屏蔽
        </button>
      </form>
      <div className="adm-table-wrap">
        <table className="adm-table">
          <thead>
            <tr>
              <th>词</th>
              <th>原因</th>
              <th>添加人</th>
              <th>时间</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {list.data && list.data.length === 0 && <EmptyRow cols={5}>没有屏蔽词</EmptyRow>}
            {(list.data ?? []).map(b => (
              <tr key={b.id}>
                <td>{b.term}</td>
                <td className="wrap">{b.reason || '—'}</td>
                <td>{b.created_by}</td>
                <td>{fmtTime(b.created_at)}</td>
                <td className="actions">
                  <button type="button" className="adm-link" onClick={() => void remove(b.id, b.term)}>
                    解除
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

export default function AdminSearch() {
  const [params, setParams] = useSearchParams();
  const tab = (TABS.find(t => t.key === params.get('tab'))?.key ?? 'zero') as TabKey;
  const days = Number(params.get('days')) === 30 ? 30 : 7;
  const stats = useAdminLoad(() => getSearchStats(days), [days]);
  const missing = !!stats.failure && isMissingEndpoint(stats.failure);

  const set = (k: string, v: string | null) => {
    const p = new URLSearchParams(params);
    if (v) p.set(k, v);
    else p.delete(k);
    setParams(p, {replace: true});
  };

  return (
    <>
      <AdminHeader
        eyebrow="ADMIN · SEARCH"
        title="搜索词情报"
        sub="用户搜什么、哪些没搜到、为什么没搜到；没搜到的词直接变成补片或改数据的任务。"
        actions={
          <div className="adm-chips" role="group" aria-label="统计周期">
            {[7, 30].map(d => (
              <button key={d} type="button" className="adm-chip" aria-pressed={days === d} onClick={() => set('days', d === 7 ? null : String(d))}>
                近 {d} 天
              </button>
            ))}
          </div>
        }
      />
      {missing ? (
        <NotDeployed what="搜索词情报接口（admin/search/*）" />
      ) : (
        <>
          <AdminNotice error={stats.error} />
          {stats.data && <StatsHeader stats={stats.data} />}
          <div className="adm-tabs" role="tablist" aria-label="搜索词情报">
            {TABS.map(t => (
              <button key={t.key} type="button" role="tab" className="adm-tab" aria-selected={tab === t.key} onClick={() => set('tab', t.key === 'zero' ? null : t.key)}>
                {t.label}
              </button>
            ))}
          </div>
          {tab === 'zero' && <ZeroResultsTab days={days} />}
          {tab === 'synonyms' && <SynonymsTab />}
          {tab === 'redirects' && <RedirectsTab />}
          {tab === 'blocklist' && <BlocklistTab />}
        </>
      )}
    </>
  );
}
