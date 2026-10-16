import {useEffect, useMemo, useState} from 'react';
import {Link, useSearchParams} from 'react-router';
import {AdminHeader, AdminNotice, EmptyRow, NotDeployed, Pill, fmtTime} from '../components/AdminUI';
import {isMissingEndpoint} from '../lib/api';
import {createLegalDraft, getAdminLegal, getLegalDiff, publishLegal, updateLegalDraft, type AdminLegalDoc, type LegalDiff, type LegalDraft} from '../lib/adminComplianceApi';
import {LEGAL_KINDS} from '../lib/compliance';
import {useAdminAction, useAdminLoad} from '../lib/adminContext';
import {LANGS} from '../lib/langs';
import '../styles/admin-compliance.css';

export const meta = () => [{title: 'REEL · 法律文本'}];

// 法律文本 (LegalPage back office; PRD 15, tech plan 3.13): every version of
// every document (legal_documents, migration 041). Drafts are edited here,
// published once (then frozen), and diffed against the previous published
// version. A version number belongs to the document kind: a translation is
// a draft with the same version in another language and inherits that
// version's effective date. Every write is a review_events row with the
// signed-in admin as 操作人. Body text is Markdown: `## 标题 {#anchor}`, blank
// lines between paragraphs, `- ` lists, **bold**, [text](https://…).

const KIND_LABELS: Record<string, string> = {
  privacy: '隐私政策',
  terms: '用户协议',
  cookies: 'Cookie 政策',
  copyright: '版权政策',
  community: '社区规则',
  ads: '广告说明',
  'performer-info': '演员信息更正',
};

type Editing = {id: number | null; kind: string; lang: string; version: string; title: string; summary: string; body: string; effective: string; note: string};

const toLocalInput = (iso: string | null) => {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  const p = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
};
const fromLocalInput = (v: string) => (v ? new Date(v).toISOString() : null);

function editingOf(d: AdminLegalDoc, asNew: boolean): Editing {
  return {
    id: asNew ? null : d.id,
    kind: d.kind,
    lang: d.lang,
    version: asNew ? '' : String(d.version),
    title: d.title,
    summary: d.summary.join('\n'),
    body: d.body,
    effective: asNew ? '' : toLocalInput(d.effective_at),
    note: asNew ? '' : d.change_note,
  };
}

function draftOf(e: Editing): LegalDraft {
  return {
    ...(e.id === null ? {kind: e.kind, lang: e.lang, version: e.version ? Number(e.version) : 0} : {}),
    title: e.title,
    summary: e.summary.split('\n').map(s => s.trim()).filter(Boolean),
    body: e.body,
    effective_at: fromLocalInput(e.effective),
    change_note: e.note,
  };
}

function DiffView({diff}: {diff: LegalDiff}) {
  const changed = diff.lines.filter(l => l.op !== '=').length;
  return (
    <div className="adm-editor">
      <p className="muted">
        {diff.base ? `对比：${diff.base.lang} 第 ${diff.base.version} 版（${diff.base.status === 'published' ? '已发布' : '草稿'}）` : '没有可对比的已发布版本：全部为新增'} · {changed} 行变化
      </p>
      <pre className="adm-diff">
        {diff.lines.map((l, i) => (
          <span key={i} className={`adm-diff-line ${l.op === '+' ? 'add' : l.op === '-' ? 'del' : ''}`}>
            {l.text || ' '}
          </span>
        ))}
      </pre>
    </div>
  );
}

export default function AdminLegal() {
  const [params, setParams] = useSearchParams();
  const kind = params.get('kind') ?? '';
  const lang = params.get('lang') ?? '';
  const list = useAdminLoad(() => getAdminLegal({kind, lang}), [kind, lang]);
  const act = useAdminAction();
  const [selected, setSelected] = useState<number | null>(null);
  const [editing, setEditing] = useState<Editing | null>(null);
  const [diff, setDiff] = useState<LegalDiff | null>(null);
  const [publishAt, setPublishAt] = useState('');

  const items = list.data?.items ?? [];
  const current = useMemo(() => items.find(d => d.id === selected) ?? null, [items, selected]);

  useEffect(() => {
    setDiff(null);
    setPublishAt('');
    if (!current) return;
    setEditing(current.status === 'draft' ? editingOf(current, false) : null);
    const ctrl = new AbortController();
    getLegalDiff(current.id, undefined, ctrl.signal).then(setDiff, () => setDiff(null));
    return () => ctrl.abort();
  }, [current?.id, current?.updated_at]);

  const setFilter = (k: string, v: string) => {
    const p = new URLSearchParams(params);
    if (v) p.set(k, v);
    else p.delete(k);
    setParams(p, {replace: true});
  };

  const save = () =>
    editing &&
    void act
      .attempt('save', () => (editing.id === null ? createLegalDraft(draftOf(editing)) : updateLegalDraft(editing.id, draftOf(editing))), d =>
        editing.id === null ? `已创建 ${d.kind} ${d.lang} 第 ${d.version} 版草稿` : '草稿已保存',
      )
      .then(d => {
        if (!d) return;
        list.reload();
        setSelected(d.id);
      });
  const publish = () => {
    if (!current || !window.confirm(`发布 ${KIND_LABELS[current.kind] ?? current.kind} ${current.lang} 第 ${current.version} 版？发布后不可修改。`)) return;
    void act
      .attempt('publish', () => publishLegal(current.id, {effective_at: fromLocalInput(publishAt)}), d => `已发布第 ${d.version} 版，${fmtTime(d.effective_at)} 起生效`)
      .then(d => d && list.reload());
  };
  const missing = list.failure && isMissingEndpoint(list.failure);

  return (
    <>
      <AdminHeader
        eyebrow="ADMIN · LEGAL"
        title="法律文本"
        sub="各文件按版本管理：草稿可改，发布后冻结；新版本到生效时间才替换前台显示的当前版本。翻译用同一版本号新建草稿。"
        actions={
          <>
            <button
              type="button"
              className="btn btn-primary btn-sm"
              onClick={() => {
                setSelected(null);
                setEditing(current ? editingOf(current, true) : {id: null, kind: kind || 'privacy', lang: lang || 'zh-CN', version: '', title: '', summary: '', body: '', effective: '', note: ''});
              }}
            >
              {current ? '基于所选新建草稿' : '新建草稿'}
            </button>
            <button type="button" className="btn btn-secondary btn-sm" disabled={list.busy} onClick={list.reload}>
              {list.busy ? '刷新中…' : '刷新'}
            </button>
          </>
        }
      />
      {missing ? <NotDeployed what="法律文本接口（admin/legal）" /> : <AdminNotice error={list.error || act.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />}

      <div className="adm-split">
        <section className="adm-card">
          <div className="adm-toolbar">
            <label className="adm-field">
              文件
              <select value={kind} onChange={e => setFilter('kind', e.target.value)}>
                <option value="">全部</option>
                {LEGAL_KINDS.map(k => (
                  <option key={k} value={k}>
                    {KIND_LABELS[k] ?? k}
                  </option>
                ))}
              </select>
            </label>
            <label className="adm-field">
              语言
              <select value={lang} onChange={e => setFilter('lang', e.target.value)}>
                <option value="">全部</option>
                {LANGS.map(l => (
                  <option key={l} value={l}>
                    {l}
                  </option>
                ))}
              </select>
            </label>
          </div>
          <div className="adm-table-wrap">
            <table className="adm-table">
              <thead>
                <tr>
                  <th>文件</th>
                  <th>语言</th>
                  <th className="num">版本</th>
                  <th>状态</th>
                  <th>生效</th>
                </tr>
              </thead>
              <tbody>
                {list.data && items.length === 0 && <EmptyRow cols={5}>没有符合条件的文本</EmptyRow>}
                {items.map(d => (
                  <tr key={d.id} className={`clickable ${d.id === selected ? 'sel' : ''}`} onClick={() => setSelected(d.id)}>
                    <td>{KIND_LABELS[d.kind] ?? d.kind}</td>
                    <td className="mono">{d.lang}</td>
                    <td className="num">{d.version}</td>
                    <td>{d.status === 'published' ? <Pill tone="green">已发布</Pill> : <Pill tone="blue">草稿</Pill>}</td>
                    <td>{fmtTime(d.effective_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>

        <section className="adm-card">
          {editing ? (
            <div className="adm-editor">
              <h2>{editing.id === null ? '新建草稿' : `编辑草稿 · ${KIND_LABELS[editing.kind] ?? editing.kind} ${editing.lang} 第 ${editing.version} 版`}</h2>
              {editing.id === null && (
                <div className="adm-toolbar">
                  <label className="adm-field">
                    文件
                    <select value={editing.kind} onChange={e => setEditing({...editing, kind: e.target.value})}>
                      {LEGAL_KINDS.map(k => (
                        <option key={k} value={k}>
                          {KIND_LABELS[k] ?? k}
                        </option>
                      ))}
                    </select>
                  </label>
                  <label className="adm-field">
                    语言
                    <select value={editing.lang} onChange={e => setEditing({...editing, lang: e.target.value})}>
                      {LANGS.map(l => (
                        <option key={l} value={l}>
                          {l}
                        </option>
                      ))}
                    </select>
                  </label>
                  <label className="adm-field">
                    版本（留空 = 下一版；填已有版本号 = 翻译）
                    <input type="number" min={1} value={editing.version} onChange={e => setEditing({...editing, version: e.target.value})} />
                  </label>
                </div>
              )}
              <label className="adm-field">
                标题
                <input value={editing.title} maxLength={200} onChange={e => setEditing({...editing, title: e.target.value})} />
              </label>
              <label className="adm-field">
                简要版（每行一条，最多 10 条）
                <textarea value={editing.summary} rows={3} onChange={e => setEditing({...editing, summary: e.target.value})} />
              </label>
              <label className="adm-field">
                正文（Markdown）
                <textarea className="body" value={editing.body} onChange={e => setEditing({...editing, body: e.target.value})} />
              </label>
              <div className="adm-toolbar">
                <label className="adm-field">
                  计划生效时间（可空）
                  <input type="datetime-local" value={editing.effective} onChange={e => setEditing({...editing, effective: e.target.value})} />
                </label>
                <label className="adm-field" style={{flex: 1}}>
                  修改说明
                  <input value={editing.note} onChange={e => setEditing({...editing, note: e.target.value})} />
                </label>
              </div>
              <div className="adm-row-actions">
                <button type="button" className="btn btn-primary btn-sm" disabled={act.pending === 'save' || !editing.title.trim() || !editing.body.trim()} onClick={save}>
                  {act.pending === 'save' ? '保存中…' : editing.id === null ? '创建草稿' : '保存草稿'}
                </button>
                {editing.id === null && (
                  <button type="button" className="btn btn-secondary btn-sm" onClick={() => setEditing(null)}>
                    取消
                  </button>
                )}
              </div>
              {current && current.status === 'draft' && (
                <div className="adm-row-actions">
                  <label className="adm-field">
                    发布后生效时间（留空：草稿计划时间或立即；翻译沿用该版本时间）
                    <input type="datetime-local" value={publishAt} onChange={e => setPublishAt(e.target.value)} />
                  </label>
                  <button type="button" className="btn btn-primary btn-sm" disabled={act.pending === 'publish'} onClick={publish}>
                    {act.pending === 'publish' ? '发布中…' : '发布'}
                  </button>
                </div>
              )}
            </div>
          ) : current ? (
            <div className="adm-editor">
              <h2>
                {KIND_LABELS[current.kind] ?? current.kind} · {current.lang} · 第 {current.version} 版 <Pill tone="green">已发布</Pill>
              </h2>
              <dl className="adm-kv">
                <dt>生效</dt>
                <dd>{fmtTime(current.effective_at)}</dd>
                <dt>发布</dt>
                <dd>
                  {fmtTime(current.published_at)} · {current.published_by ?? '—'}
                </dd>
                <dt>说明</dt>
                <dd>{current.change_note || '—'}</dd>
              </dl>
              <Link to={`/legal/${current.kind}?v=${current.version}`} target="_blank" className="adm-link">
                在前台查看
              </Link>
            </div>
          ) : (
            <p className="muted">从左侧选择一份文本，或新建草稿。</p>
          )}
          {diff && current && (
            <>
              <h3>与上一版的差异</h3>
              <DiffView diff={diff} />
            </>
          )}
        </section>
      </div>
    </>
  );
}
