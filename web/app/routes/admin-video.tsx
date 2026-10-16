import {useEffect, useMemo, useState, type FormEvent, type ReactNode} from 'react';
import {Link, useParams, useSearchParams} from 'react-router';
import {AdminVideoCast} from '../components/AdminVideoCast';
import {AdminVideoSources} from '../components/AdminVideoSources';
import {AdminNotice, NotDeployed, Pill, fmtTime, jsonText, statusTone} from '../components/AdminUI';
import {ApiError, isMissingEndpoint} from '../lib/api';
import {
  AGE_RATINGS,
  ALLOWED_TRANSITIONS,
  I18N_LANGS,
  LANG_NAMES,
  RULE_LABELS,
  STATUS_LABELS,
  TRANSLATION_LABELS,
  clearOverride,
  getAdminRelease,
  parseAgeGate,
  patchAdminRelease,
  transitionRelease,
  type AdminRelease,
  type AgeGateFinding,
  type Override,
} from '../lib/adminApi';
import {useAdminAction, useAdminLoad} from '../lib/adminContext';
import {SplitPanel, UndoSplit} from '../components/AdminSplit';
import {HomeSlotShortcut, RightsProofs} from '../components/AdminVideoPublishExtras';

export const meta = () => [{title: 'REEL · 视频编辑'}];

// AdminVideo editor (PRD 11 A3, PR #61): one release with tabs 基本信息 /
// 片源 / 演员 / 发布 / 多语言 / 修改记录. Field edits are PATCHed with the
// optimistic-lock version and become manual overrides (the crawler stops
// writing those fields until 清除覆盖). Publishing is gated by R-AGE on the
// server; its 409 names the performers. 发布 also carries 推荐位 (home slot
// shortcut) and 版权凭证 (AdminVideoPublishExtras). 截屏与章节 (artboard tab) has no
// backend yet (needs the transcoding pipeline) and is left out.

type TabKey = 'basic' | 'sources' | 'cast' | 'publish' | 'i18n' | 'history';
const TABS: {key: TabKey; label: string}[] = [
  {key: 'basic', label: '基本信息'},
  {key: 'sources', label: '片源'},
  {key: 'cast', label: '演员'},
  {key: 'publish', label: '发布'},
  {key: 'i18n', label: '多语言'},
  {key: 'history', label: '修改记录'},
];

// PATCH field → override column (store/overrides.go releaseFieldAliases).
type FieldDef = {key: string; column: string; label: string; kind?: 'text' | 'textarea' | 'date' | 'int'; hint?: string; span?: boolean};
const BASIC_FIELDS: FieldDef[] = [
  {key: 'title', column: 'canonical_title', label: '标题', hint: '前台显示的标题（必填）', span: true},
  {key: 'title_ja', column: 'title_ja', label: '日文标题', span: true},
  {key: 'release_date', column: 'release_date', label: '发布日期', kind: 'date'},
  {key: 'shoot_year', column: 'shoot_year', label: '拍摄年份', kind: 'int', hint: '用于计算演员出演时年龄；不确定时留空，前台显示「约」'},
  {key: 'runtime_minutes', column: 'runtime_minutes', label: '时长（分钟）', kind: 'int'},
  {key: 'series', column: 'series_name', label: '系列'},
  {key: 'director', column: 'director_name', label: '导演'},
  {key: 'maker', column: 'maker_name', label: '片商'},
  {key: 'label', column: 'label_name', label: '厂牌'},
  {key: 'synopsis', column: 'synopsis', label: '简介', kind: 'textarea', span: true},
];
const FIELD_LABEL: Record<string, string> = {
  ...Object.fromEntries(BASIC_FIELDS.map(f => [f.column, f.label])),
  tags: '标签',
  performers: '演员',
  age_rating: '年龄分级',
  region_allow: '仅限地区',
  region_deny: '屏蔽地区',
  featured_note: '推荐语',
};

const fieldValue = (r: AdminRelease, key: string): string => {
  const v = (r as unknown as Record<string, unknown>)[key];
  return v === null || v === undefined ? '' : String(v);
};

function versionConflict(cause: unknown) {
  return cause instanceof ApiError && cause.status === 409 && /version|stale|modified/i.test(cause.detail ?? '');
}

function OverrideList({overrides, busy, onClear}: {overrides: Override[]; busy: string | null; onClear: (o: Override, reason: string) => void}) {
  const [reasons, setReasons] = useState<Record<string, string>>({});
  if (overrides.length === 0) return <p className="muted">没有手动覆盖：所有字段都跟随抓取数据。</p>;
  return (
    <div className="adm-overrides">
      {overrides.map(o => (
        <div key={o.field} className="adm-override">
          <Pill tone="amber">覆盖</Pill>
          <strong>{FIELD_LABEL[o.field] ?? o.field}</strong>
          <span className="mono adm-clip" style={{maxWidth: 320}}>
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
            value={reasons[o.field] ?? ''}
            onChange={e => setReasons(prev => ({...prev, [o.field]: e.target.value}))}
            style={{width: 160}}
          />
          <button type="button" className="adm-link" disabled={busy === `clear-${o.field}`} onClick={() => onClear(o, reasons[o.field] ?? '')}>
            清除覆盖
          </button>
        </div>
      ))}
    </div>
  );
}

function BasicTab({r, onSaved}: {r: AdminRelease; onSaved: () => void}) {
  const act = useAdminAction();
  const initial = useMemo(() => {
    const m: Record<string, string> = {};
    for (const f of BASIC_FIELDS) m[f.key] = fieldValue(r, f.key);
    m.tags = (r.tags ?? []).map(t => t.tag).join(', ');
    return m;
  }, [r]);
  const [form, setForm] = useState(initial);
  const [reason, setReason] = useState('');
  const [stale, setStale] = useState(false);
  useEffect(() => {
    setForm(initial);
    setStale(false);
  }, [initial]);

  const overridden = new Set((r.overrides ?? []).map(o => o.field));
  const changed = Object.keys(form).filter(k => form[k] !== initial[k]);

  const save = async (e: FormEvent) => {
    e.preventDefault();
    const body: Record<string, unknown> = {version: r.version};
    for (const k of changed) {
      const def = BASIC_FIELDS.find(f => f.key === k);
      const v = form[k].trim();
      if (k === 'tags') body.tags = v ? v.split(/[,，]/).map(t => t.trim()).filter(Boolean) : [];
      else if (def?.kind === 'int') body[k] = v === '' ? null : Number(v);
      else body[k] = v === '' ? null : v;
    }
    if (reason.trim()) body.reason = reason.trim();
    try {
      await act.run('save', () => patchAdminRelease(r.id, body), v => `已保存：${v.changed.join('、') || '无变化'}${v.status !== r.status ? `；状态变为 ${STATUS_LABELS[v.status] ?? v.status}` : ''}`);
      setReason('');
      onSaved();
    } catch (cause) {
      if (versionConflict(cause)) setStale(true);
    }
  };

  const clear = async (o: Override, why: string) => {
    const ok = await act.attempt(`clear-${o.field}`, () => clearOverride(r.id, o.field, why.trim()), `已清除「${FIELD_LABEL[o.field] ?? o.field}」的覆盖，下次抓取会重新写入`);
    if (ok) onSaved();
  };

  const input = (f: FieldDef) => {
    const isOver = overridden.has(f.column);
    const common = {value: form[f.key], onChange: (e: {target: {value: string}}) => setForm(prev => ({...prev, [f.key]: e.target.value}))};
    return (
      <label key={f.key} className={`adm-field ${isOver ? 'overridden' : ''} ${f.span ? 'span-all' : ''}`}>
        <span className="label-row">
          {f.label} {isOver && <Pill tone="amber">已覆盖</Pill>} {form[f.key] !== initial[f.key] && <Pill tone="blue">未保存</Pill>}
        </span>
        {f.kind === 'textarea' ? (
          <textarea rows={5} {...common} />
        ) : f.kind === 'date' ? (
          <input type="date" {...common} />
        ) : f.kind === 'int' ? (
          <input type="number" {...common} />
        ) : (
          <input required={f.key === 'title'} {...common} />
        )}
        {f.hint && <span className={`adm-field-hint ${f.key === 'shoot_year' ? 'warn' : ''}`}>{f.hint}</span>}
      </label>
    );
  };

  return (
    <>
      <form className="adm-card" onSubmit={save}>
        <div className="adm-card-head">
          <h2>基本信息</h2>
          <span className="muted mono">version {r.version}</span>
        </div>
        {stale && (
          <div className="adm-notice adm-notice-warn" role="alert">
            这条视频已被别人修改（版本不一致）。
            <button type="button" className="adm-link" onClick={onSaved}>
              重新加载最新版本
            </button>
          </div>
        )}
        <AdminNotice error={act.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />
        <div className="adm-form-grid">
          {BASIC_FIELDS.map(input)}
          <label className={`adm-field span-all ${overridden.has('tags') ? 'overridden' : ''}`}>
            <span className="label-row">
              标签 {overridden.has('tags') && <Pill tone="amber">已覆盖</Pill>}
            </span>
            <input value={form.tags} onChange={e => setForm(prev => ({...prev, tags: e.target.value}))} />
            <span className="adm-field-hint">用逗号分隔</span>
          </label>
          <div className="adm-field span-all">
            只读：番号 {(r.identifiers ?? []).map(i => `${i.value}（${i.type}）`).join('、') || '—'} · 版本类型 {r.edition_type} · 打码 {r.censorship_type ?? '—'} · slug{' '}
            <span className="mono">{r.slug}</span>
          </div>
        </div>
        <div className="adm-form-actions">
          <input className="adm-input" placeholder="修改理由（可选，写入修改记录）" value={reason} onChange={e => setReason(e.target.value)} style={{flex: 1, minWidth: 240}} />
          <button type="button" className="btn btn-secondary btn-sm" disabled={!changed.length} onClick={() => setForm(initial)}>
            撤销
          </button>
          <button type="submit" className="btn btn-primary btn-sm" disabled={!changed.length || act.pending === 'save'}>
            保存{changed.length ? `（${changed.length}）` : ''}
          </button>
        </div>
        <p className="adm-field-hint">保存后的字段成为「手动覆盖」，之后的抓取不会再改它，直到清除覆盖。</p>
      </form>
      <section className="adm-card">
        <h2>手动覆盖（{r.overrides?.length ?? 0}）</h2>
        <OverrideList overrides={r.overrides ?? []} busy={act.pending} onClear={(o, why) => void clear(o, why)} />
      </section>
    </>
  );
}

const toLocalInput = (iso: string | null) => {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
};

function AgeGateBlock({findings, rejected}: {findings: AgeGateFinding[]; rejected?: boolean}) {
  return (
    <div className="adm-agegate" role="alert">
      {rejected && <span>服务端拒绝了这次发布（409）。</span>}
      <strong>无法上线：R-AGE 仍未通过，以下演员的出演时年龄可能未满 18 岁</strong>
      <ul>
        {findings.map((f, i) => (
          <li key={i}>
            <strong>{f.name}</strong> · {f.age} 岁 · {f.reason}
          </li>
        ))}
      </ul>
      <span>先修正拍摄年份、发布日期或演员生日（基本信息 / 演员），再发布。</span>
    </div>
  );
}

function PublishTab({r, onSaved}: {r: AdminRelease; onSaved: () => void}) {
  const act = useAdminAction();
  const [to, setTo] = useState('');
  const [scheduled, setScheduled] = useState(toLocalInput(r.scheduled_at));
  const [overrideReason, setOverrideReason] = useState('');
  const [reason, setReason] = useState('');
  const [gate, setGate] = useState<AgeGateFinding[] | null>(null);
  const [stale, setStale] = useState(false);
  const [ageRating, setAgeRating] = useState(r.age_rating ?? '');
  const [allow, setAllow] = useState((r.region_allow ?? []).join(', '));
  const [deny, setDeny] = useState((r.region_deny ?? []).join(', '));
  const [note, setNote] = useState(r.featured_note ?? '');

  useEffect(() => {
    setAgeRating(r.age_rating ?? '');
    setAllow((r.region_allow ?? []).join(', '));
    setDeny((r.region_deny ?? []).join(', '));
    setNote(r.featured_note ?? '');
    setScheduled(toLocalInput(r.scheduled_at));
  }, [r]);

  const allowed = ALLOWED_TRANSITIONS[r.status] ?? [];
  const held = r.status === 'held_review';
  const ageIssues = (r.audit_issues ?? []).filter(i => i.rule === 'R-AGE' && i.status === 'open');
  const flagged = (r.performers ?? []).filter(p => p.age_flag);

  const transition = async (e: FormEvent) => {
    e.preventDefault();
    if (!to) return;
    setGate(null);
    setStale(false);
    const body: Parameters<typeof transitionRelease>[1] = {status: to, version: r.version};
    if (to === 'upcoming') {
      const d = new Date(scheduled);
      if (!scheduled || Number.isNaN(d.getTime())) {
        act.setError('待上线需要设置上线时间');
        return;
      }
      body.scheduled_at = d.toISOString();
    }
    if (held && to === 'published') body.override_reason = overrideReason.trim();
    if (reason.trim()) body.reason = reason.trim();
    try {
      await act.run('status', () => transitionRelease(r.id, body), v => `状态：${STATUS_LABELS[v.from] ?? v.from} → ${STATUS_LABELS[v.to] ?? v.to}`);
      setTo('');
      setOverrideReason('');
      setReason('');
      onSaved();
    } catch (cause) {
      const findings = parseAgeGate(cause);
      if (findings && findings.length > 0) {
        setGate(findings);
        act.setError('');
      } else if (versionConflict(cause)) setStale(true);
    }
  };

  const splitRegions = (s: string) =>
    s
      .split(/[\s,，]+/)
      .map(x => x.trim().toUpperCase())
      .filter(Boolean);

  const saveRules = async (e: FormEvent) => {
    e.preventDefault();
    const body: Record<string, unknown> = {version: r.version};
    if (ageRating !== (r.age_rating ?? '')) body.age_rating = ageRating || null;
    const a = splitRegions(allow);
    const d = splitRegions(deny);
    if (a.join(',') !== (r.region_allow ?? []).join(',')) body.region_allow = a.length ? a : null;
    if (d.join(',') !== (r.region_deny ?? []).join(',')) body.region_deny = d.length ? d : null;
    if (note.trim() !== (r.featured_note ?? '')) body.featured_note = note.trim() || null;
    if (Object.keys(body).length === 1) {
      act.setNotice('没有改动');
      return;
    }
    try {
      await act.run('rules', () => patchAdminRelease(r.id, body), '已保存发布设置');
      onSaved();
    } catch (cause) {
      if (versionConflict(cause)) setStale(true);
    }
  };

  return (
    <>
      <form className="adm-card" onSubmit={transition}>
        <div className="adm-card-head">
          <h2>发布状态</h2>
          <span>
            当前 <Pill tone={statusTone(r.status)}>{STATUS_LABELS[r.status] ?? r.status}</Pill>
            {r.status === 'upcoming' && r.scheduled_at && <span className="muted"> · {fmtTime(r.scheduled_at)} 上线</span>}
          </span>
        </div>
        {stale && (
          <div className="adm-notice adm-notice-warn" role="alert">
            这条视频已被别人修改（版本不一致）。
            <button type="button" className="adm-link" onClick={onSaved}>
              重新加载最新版本
            </button>
          </div>
        )}
        {held && (
          <div className="adm-notice adm-notice-err" role="status">
            合规暂停（held_review）：R-AGE 发现演员出演时年龄可能未满 18 岁，作品已从前台隐藏。只能修正数据后填写解除理由发布，或直接下线。
          </div>
        )}
        {(ageIssues.length > 0 || flagged.length > 0) && !gate && (
          <AgeGateBlock
            findings={
              flagged.length
                ? flagged.map(p => ({name: p.name, age: p.age_at_filming?.age ?? NaN, reason: p.age_flag ?? ''}))
                : ageIssues.flatMap(i =>
                    ((i.detail?.performers as {name?: string; age?: number; reason?: string}[] | undefined) ?? []).map(p => ({
                      name: p.name ?? '',
                      age: p.age ?? NaN,
                      reason: p.reason ?? '',
                    })),
                  )
            }
          />
        )}
        {gate && <AgeGateBlock findings={gate} rejected />}
        <AdminNotice error={act.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />
        <div className="adm-chips" role="radiogroup" aria-label="改为">
          {allowed.length === 0 && <span className="muted">当前状态不能再变更。</span>}
          {allowed.map(s => (
            <button key={s} type="button" role="radio" className="adm-chip" aria-checked={to === s} aria-pressed={to === s} onClick={() => setTo(to === s ? '' : s)}>
              → {STATUS_LABELS[s]}
              {s === r.status ? '（改时间）' : ''}
            </button>
          ))}
        </div>
        {to === 'upcoming' && (
          <label className="adm-field" style={{maxWidth: 280}}>
            上线时间（必须晚于现在）
            <input type="datetime-local" required value={scheduled} onChange={e => setScheduled(e.target.value)} />
          </label>
        )}
        {held && to === 'published' && (
          <label className="adm-field">
            <span className="label-row">
              解除暂停的理由 <Pill tone="red">必填</Pill>
            </span>
            <textarea
              required
              rows={3}
              maxLength={2000}
              value={overrideReason}
              onChange={e => setOverrideReason(e.target.value)}
              placeholder="例如：已核对拍摄年份为 2024，演员出演时 22 岁"
            />
            <span className="adm-field-hint">会写入修改记录，并关闭这条 R-AGE 问题。服务端仍会重新检查 R-AGE。</span>
          </label>
        )}
        {to && (
          <div className="adm-form-actions">
            <input className="adm-input" placeholder="备注（可选）" value={reason} onChange={e => setReason(e.target.value)} style={{flex: 1, minWidth: 240}} />
            <button type="submit" className="btn btn-primary btn-sm" disabled={act.pending === 'status'}>
              确认改为「{STATUS_LABELS[to]}」
            </button>
          </div>
        )}
      </form>

      <form className="adm-card" onSubmit={saveRules}>
        <h2>分级与地区</h2>
        <div className="adm-form-grid">
          <label className="adm-field">
            年龄分级（青少年模式按此隐藏）
            <select value={ageRating} onChange={e => setAgeRating(e.target.value)}>
              <option value="">未分级</option>
              {AGE_RATINGS.map(a => (
                <option key={a} value={a}>
                  {a === 'all' ? '全年龄' : `${a}+`}
                </option>
              ))}
            </select>
          </label>
          <label className="adm-field">
            仅限地区（ISO 两位代码，空 = 不限）
            <input value={allow} onChange={e => setAllow(e.target.value)} placeholder="JP, US" />
          </label>
          <label className="adm-field">
            屏蔽地区
            <input value={deny} onChange={e => setDeny(e.target.value)} placeholder="CN" />
          </label>
          <label className="adm-field span-all">
            推荐语（推荐位用）
            <input value={note} maxLength={2000} onChange={e => setNote(e.target.value)} />
          </label>
        </div>
        <p className="adm-field-hint">地区限制叠加在每个片源自己的地区设置之上。</p>
        <div className="adm-form-actions">
          <button type="submit" className="btn btn-secondary btn-sm" disabled={act.pending === 'rules'}>
            保存发布设置
          </button>
        </div>
      </form>

      <HomeSlotShortcut r={r} />
      <RightsProofs r={r} />
    </>
  );
}

function I18nTab({r}: {r: AdminRelease}) {
  const counts = r.translations?.counts ?? {};
  const items = r.translations?.items ?? [];
  const fields = Array.from(new Set(items.map(i => i.field)));
  const cell = (field: string, lang: string) => items.find(i => i.field === field && i.lang === lang)?.status ?? 'missing';
  return (
    <section className="adm-card">
      <div className="adm-card-head">
        <h2>多语言</h2>
        <Link to="/admin/i18n?entity_type=release" className="btn btn-secondary btn-sm">
          打开翻译工作台
        </Link>
      </div>
      <div className="adm-chips">
        {Object.entries(counts).map(([s, n]) => (
          <Link key={s} to={`/admin/i18n?entity_type=release${s === 'missing' ? '&missing=1' : `&status=${s}`}`}>
            <Pill tone={statusTone(s)}>
              {TRANSLATION_LABELS[s] ?? s} {n}
            </Pill>
          </Link>
        ))}
      </div>
      <div className="adm-table-wrap">
        <table className="adm-table">
          <thead>
            <tr>
              <th>字段</th>
              {I18N_LANGS.map(l => (
                <th key={l}>{LANG_NAMES[l]}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {fields.length === 0 && (
              <tr>
                <td colSpan={I18N_LANGS.length + 1} className="adm-empty">
                  没有翻译记录
                </td>
              </tr>
            )}
            {fields.map(f => (
              <tr key={f}>
                <td>{f === 'title' ? '标题' : f === 'synopsis' ? '简介' : f}</td>
                {I18N_LANGS.map(l => {
                  const s = cell(f, l);
                  return (
                    <td key={l}>
                      <Link to={`/admin/i18n?entity_type=release&lang=${l}${s === 'missing' ? '&missing=1' : `&status=${s}`}`}>
                        <Pill tone={statusTone(s)}>{TRANSLATION_LABELS[s] ?? s}</Pill>
                      </Link>
                    </td>
                  );
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

const ACTION_LABELS: Record<string, string> = {
  edit: '修改字段',
  status_change: '状态变更',
  clear_override: '清除覆盖',
  create: '添加片源',
  promote: '转为片源',
  reorder_sources: '片源排序',
  set_performer: '演员署名',
  remove_performer: '移除演员',
  merge: '合并',
  split: '拆分',
  undo_split: '撤销拆分',
  resolve: '解决冲突',
  add_rights_proof: '添加版权凭证',
  remove_rights_proof: '移除版权凭证',
};

function HistoryTab({r, onChanged}: {r: AdminRelease; onChanged: () => void}) {
  const events = r.review_events ?? [];
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
              <strong>{ACTION_LABELS[e.action] ?? e.action}</strong> · {e.actor ?? '—'}
              {e.entity_type !== 'release' && (
                <span className="muted">
                  {' '}
                  · {e.entity_type} #{e.entity_id}
                </span>
              )}
              {e.reason && <div className="muted">理由：{e.reason}</div>}
              {e.action === 'split' && e.entity_type === 'release' && <UndoSplit eventId={e.id} onDone={onChanged} />}
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

export default function AdminVideo() {
  const {id: idParam} = useParams();
  const id = Number(idParam);
  const [params, setParams] = useSearchParams();
  const tab = (TABS.find(t => t.key === params.get('tab'))?.key ?? 'basic') as TabKey;
  const rel = useAdminLoad(() => getAdminRelease(id), [id]);
  const r = rel.data;
  const missing = rel.failure && isMissingEndpoint(rel.failure) && !(rel.failure instanceof ApiError && rel.failure.detail === 'not found');

  const setTab = (k: TabKey) => setParams(k === 'basic' ? {} : {tab: k}, {replace: true});
  const [splitting, setSplitting] = useState(false);

  if (!Number.isInteger(id) || id <= 0) return <AdminNotice error="无效的视频 ID" />;

  const warn: Partial<Record<TabKey, 'red' | 'amber'>> = {};
  if (r) {
    if ((r.performers ?? []).some(p => p.age_flag)) warn.cast = 'red';
    if (r.status === 'held_review') warn.publish = 'red';
    if ((r.sources ?? []).length === 0 || (r.sources ?? []).some(s => s.health_band === 'severe' || s.status !== 'active')) warn.sources = 'amber';
    if ((r.translations?.counts?.missing ?? 0) > 0 || (r.translations?.counts?.stale ?? 0) > 0) warn.i18n = 'amber';
  }

  let body: ReactNode = null;
  if (r) {
    if (tab === 'basic') body = <BasicTab r={r} onSaved={rel.reload} />;
    else if (tab === 'sources') body = <AdminVideoSources releaseId={r.id} sources={r.sources ?? []} media={r.media_sources ?? []} onChanged={rel.reload} />;
    else if (tab === 'cast')
      body = <AdminVideoCast releaseId={r.id} performers={r.performers ?? []} shootYear={r.shoot_year} releaseDate={r.release_date} onChanged={rel.reload} />;
    else if (tab === 'publish') body = <PublishTab r={r} onSaved={rel.reload} />;
    else if (tab === 'i18n') body = <I18nTab r={r} />;
    else body = <HistoryTab r={r} onChanged={rel.reload} />;
  }

  const openIssues = (r?.audit_issues ?? []).filter(i => i.status === 'open');

  return (
    <>
      <nav className="adm-crumb" aria-label="位置">
        <Link to="/admin/videos">视频</Link> › <span>{r?.title ?? `#${id}`}</span>
      </nav>
      <header className="adm-head">
        <div className="adm-head-text">
          <h1>{r?.title ?? (rel.busy ? '加载中…' : `视频 #${id}`)}</h1>
          {r && (
            <p className="adm-sub">
              <Pill tone={statusTone(r.status)}>{STATUS_LABELS[r.status] ?? r.status}</Pill> · ID {r.id} · {(r.identifiers ?? []).map(i => i.value).join(' / ') || r.slug} · 更新于{' '}
              {fmtTime(r.updated_at)}
            </p>
          )}
        </div>
        <div className="adm-head-actions">
          {r && (
            <Link to={`/releases/${r.slug}`} target="_blank" className="btn btn-secondary btn-sm">
              前台预览
            </Link>
          )}
          {r && (
            <button type="button" className="btn btn-secondary btn-sm" aria-expanded={splitting} onClick={() => setSplitting(v => !v)}>
              拆分
            </button>
          )}
          <button type="button" className="btn btn-secondary btn-sm" disabled={rel.busy} onClick={rel.reload}>
            刷新
          </button>
        </div>
      </header>
      {r && splitting && (
        <SplitPanel
          entity="release"
          id={r.id}
          version={r.version}
          mediaLinks={(r.media_sources ?? []).map(m => m.source)}
          onDone={rel.reload}
          onClose={() => setSplitting(false)}
        />
      )}

      {missing && <NotDeployed what="视频编辑接口（admin/releases/{id}）" />}
      {!missing && <AdminNotice error={rel.error === '内容不存在或已下架：not found' ? '没有这条视频' : rel.error} />}

      {r && (openIssues.length > 0 || (r.conflicts ?? []).length > 0) && (
        <div className="adm-chips">
          {openIssues.map(i => (
            <Link key={i.id} to={`/admin/audit?rule=${i.rule}`}>
              <Pill tone={i.rule === 'R-AGE' ? 'red' : 'amber'}>
                {i.rule} · {RULE_LABELS[i.rule] ?? ''}
              </Pill>
            </Link>
          ))}
          {(r.conflicts ?? []).length > 0 && (
            <Link to="/admin/review">
              <Pill tone="blue">字段冲突 {(r.conflicts ?? []).length}</Pill>
            </Link>
          )}
        </div>
      )}

      {r && (
        <div className="adm-tabs" role="tablist" aria-label="编辑">
          {TABS.map(t => (
            <button key={t.key} type="button" role="tab" className="adm-tab" aria-selected={tab === t.key} onClick={() => setTab(t.key)}>
              {t.label}
              {warn[t.key] && <span className={`adm-tab-warn ${warn[t.key] === 'red' ? 'red' : ''}`} aria-label="需要处理" />}
            </button>
          ))}
        </div>
      )}
      {body}
    </>
  );
}
