import {useMemo, useState} from 'react';
import {Link, useSearchParams} from 'react-router';
import {AdminHeader, AdminNotice, EmptyRow, NotDeployed, Pill, fmtNum, fmtTime} from '../components/AdminUI';
import {adminErrorText} from '../lib/adminApi';
import {
  downloadCompletenessCSV,
  fieldLabel,
  getCompleteness,
  routeMissing,
  type CompletenessGroup,
  type CompletenessQuery,
  type FieldStat,
  type TrendPoint,
} from '../lib/adminGovApi';
import {useAdminLoad} from '../lib/adminContext';

export const meta = () => [{title: 'REEL · 完整性报告'}];

// 完整性报告 (ROADMAP 作品、演员和字段完整性报告; internal/completeness):
// GET admin/reports/completeness?entity&group_by&source&runs&top[&format=csv].
// Per-field coverage ranked by the popularity of what is missing, per-source
// coverage, optional groups (source / maker) and the nightly snapshot trend
// (inline SVG, no chart library).

type Entity = CompletenessQuery['entity'];
type GroupBy = CompletenessQuery['group_by'];
const ENTITY_LABELS: Record<Entity, string> = {release: '作品', performer: '演员'};
const GROUP_LABELS: Record<GroupBy, string> = {field: '按字段', source: '按来源', maker: '按片商'};
const RUNS = [14, 30, 90];

const tone = (pct: number) => (pct >= 90 ? 'green' : pct >= 60 ? '' : pct >= 30 ? 'amber' : 'red');
const pctText = (v: number | undefined | null) => (v === null || v === undefined ? '—' : `${Math.round(v * 10) / 10}%`);

function PctBar({v}: {v: number}) {
  return (
    <span className="adm-pct">
      <span className="adm-bar-track">
        <span className={`adm-bar-fill ${tone(v)}`} style={{width: `${Math.max(0, Math.min(100, v))}%`}} />
      </span>
      <span className="adm-bar-n">{pctText(v)}</span>
    </span>
  );
}

const entityHref = (entity: Entity, id: number) => (entity === 'release' ? `/admin/videos/${id}` : `/admin/actors?id=${id}`);

/** Required-complete % (and one field's %) over the nightly snapshots: 0–100 on one axis. */
function TrendChart({points, field}: {points: TrendPoint[]; field: string}) {
  const [hover, setHover] = useState<number | null>(null);
  const W = 960;
  const H = 220;
  const pad = {l: 44, r: 28, t: 12, b: 26};
  const iw = W - pad.l - pad.r;
  const ih = H - pad.t - pad.b;
  const x = (i: number) => pad.l + (points.length <= 1 ? iw / 2 : (i / (points.length - 1)) * iw);
  const y = (v: number) => pad.t + ih - (Math.max(0, Math.min(100, v)) / 100) * ih;
  const main = points.map(p => p.required_complete_percent);
  const alt = field ? points.map(p => p.fields?.[field]) : [];
  const path = (vals: (number | undefined)[]) =>
    vals
      .map((v, i) => (v === undefined ? null : `${x(i).toFixed(1)},${y(v).toFixed(1)}`))
      .filter(Boolean)
      .map((pt, i) => `${i ? 'L' : 'M'}${pt}`)
      .join(' ');
  const ticks = [0, 25, 50, 75, 100];
  const labelEvery = Math.max(1, Math.ceil(points.length / 7));
  const h = hover !== null ? points[hover] : null;

  return (
    <div style={{position: 'relative'}}>
      <svg
        className="adm-trend"
        viewBox={`0 0 ${W} ${H}`}
        role="img"
        aria-label={`必填完整率趋势，${points.length} 个快照`}
        onMouseLeave={() => setHover(null)}
        onMouseMove={e => {
          const rect = e.currentTarget.getBoundingClientRect();
          const px = ((e.clientX - rect.left) / rect.width) * W;
          if (points.length === 0) return;
          const i = points.length <= 1 ? 0 : Math.round(((px - pad.l) / iw) * (points.length - 1));
          setHover(Math.max(0, Math.min(points.length - 1, i)));
        }}
      >
        {ticks.map(t => (
          <g key={t}>
            <line className="grid" x1={pad.l} x2={W - pad.r} y1={y(t)} y2={y(t)} />
            <text className="axis" x={pad.l - 6} y={y(t) + 4} textAnchor="end">
              {t}%
            </text>
          </g>
        ))}
        {points.map((p, i) =>
          i % labelEvery === 0 || i === points.length - 1 ? (
            <text key={p.date} className="axis" x={x(i)} y={H - 6} textAnchor="middle">
              {p.date.slice(5)}
            </text>
          ) : null,
        )}
        {hover !== null && <line className="grid" x1={x(hover)} x2={x(hover)} y1={pad.t} y2={pad.t + ih} />}
        {field && <path className="line alt" d={path(alt)} />}
        <path className="line" d={path(main)} />
        {points.length <= 31 && main.map((v, i) => <circle key={i} className="dot" cx={x(i)} cy={y(v)} r={hover === i ? 4 : 2.5} />)}
      </svg>
      {h && (
        <div className="adm-tooltip" style={{left: `${(x(hover!) / W) * 100}%`}}>
          <strong>{h.date}</strong>
          <span>必填完整 {pctText(h.required_complete_percent)}</span>
          {field && <span>{fieldLabel(field)} {pctText(h.fields?.[field])}</span>}
          <span className="muted">共 {fmtNum(h.total)}</span>
        </div>
      )}
    </div>
  );
}

function FieldsTable({entity, fields, showSource}: {entity: Entity; fields: FieldStat[]; showSource?: boolean}) {
  return (
    <div className="adm-table-wrap">
      <table className="adm-table">
        <thead>
          <tr>
            <th>字段</th>
            <th>覆盖率</th>
            <th className="num">有 / 总数</th>
            <th className="num">缺失</th>
            <th className="num" title="缺失该字段的条目的热度之和">缺失热度</th>
            {showSource && <th className="num">来源提供</th>}
            <th>最该补的（按热度）</th>
          </tr>
        </thead>
        <tbody>
          {fields.length === 0 && <EmptyRow cols={showSource ? 7 : 6}>没有字段</EmptyRow>}
          {fields.map(f => (
            <tr key={f.field}>
              <td>
                {fieldLabel(f.field)} {f.required && <Pill tone="blue">必填</Pill>}
              </td>
              <td style={{minWidth: 180}}>
                <PctBar v={f.percent} />
              </td>
              <td className="num">
                {fmtNum(f.present)} / {fmtNum(f.total)}
              </td>
              <td className="num">{fmtNum(f.missing)}</td>
              <td className="num">{fmtNum(f.missing_popularity)}</td>
              {showSource && <td className="num">{f.source_provided === undefined ? '—' : fmtNum(f.source_provided)}</td>}
              <td className="wrap" style={{maxWidth: 360}}>
                {(f.top_missing ?? []).length === 0 ? (
                  <span className="muted">—</span>
                ) : (
                  <ol className="adm-missing-list">
                    {(f.top_missing ?? []).map(e => (
                      <li key={e.id}>
                        <Link to={entityHref(entity, e.id)}>{e.name || e.slug}</Link> <span className="muted mono">热度 {fmtNum(e.popularity)}</span>
                      </li>
                    ))}
                  </ol>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function GroupsTable({entity, groups, groupBy}: {entity: Entity; groups: CompletenessGroup[]; groupBy: GroupBy}) {
  const [open, setOpen] = useState<string | null>(null);
  return (
    <div className="adm-table-wrap">
      <table className="adm-table">
        <thead>
          <tr>
            <th>{groupBy === 'source' ? '来源' : '片商'}</th>
            <th className="num">条目</th>
            <th>必填完整</th>
            <th>最低的字段</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {groups.length === 0 && <EmptyRow cols={5}>没有分组</EmptyRow>}
          {groups.map(g => {
            const worst = [...(g.fields ?? [])].sort((a, b) => a.percent - b.percent).slice(0, 3);
            return [
              <tr key={g.key} className={open === g.key ? 'sel' : ''}>
                <td>
                  {g.key || '（未知）'} {g.source_kind && <span className="muted mono">{g.source_kind}</span>}
                </td>
                <td className="num">{fmtNum(g.total)}</td>
                <td style={{minWidth: 180}}>
                  <PctBar v={g.required_complete_percent} />
                </td>
                <td className="adm-source-meta">{worst.map(f => `${fieldLabel(f.field)} ${pctText(f.percent)}`).join(' · ') || '—'}</td>
                <td>
                  <button type="button" className="adm-link" aria-expanded={open === g.key} onClick={() => setOpen(open === g.key ? null : g.key)}>
                    {open === g.key ? '收起' : '展开字段'}
                  </button>
                </td>
              </tr>,
              open === g.key ? (
                <tr key={`${g.key}-fields`}>
                  <td colSpan={5}>
                    <FieldsTable entity={entity} fields={g.fields ?? []} showSource={groupBy === 'source'} />
                  </td>
                </tr>
              ) : null,
            ];
          })}
        </tbody>
      </table>
    </div>
  );
}

export default function AdminCompleteness() {
  const [params, setParams] = useSearchParams();
  const entity: Entity = params.get('entity') === 'performer' ? 'performer' : 'release';
  const groupBy: GroupBy = (['source', 'maker'] as const).find(g => g === params.get('group_by')) ?? 'field';
  const source = params.get('source') ?? '';
  const runs = Number(params.get('runs')) || 14;
  const [trendField, setTrendField] = useState('');
  const [csvBusy, setCsvBusy] = useState(false);
  const [csvErr, setCsvErr] = useState('');

  const q: CompletenessQuery = {entity, group_by: groupBy, source: source || undefined, runs, top: 5};
  const rep = useAdminLoad(signal => getCompleteness(q, signal), [entity, groupBy, source, runs]);
  const r = rep.data && rep.data.entity === entity ? rep.data : null;

  const set = (patch: Record<string, string | null>) => {
    const p = new URLSearchParams(params);
    for (const [k, v] of Object.entries(patch)) {
      if (v) p.set(k, v);
      else p.delete(k);
    }
    setParams(p, {replace: true});
  };
  const fields = r?.fields ?? [];
  const trend = useMemo(() => [...(r?.trend ?? [])].sort((a, b) => a.date.localeCompare(b.date)), [r]);
  const trendFields = useMemo(() => [...new Set(trend.flatMap(p => Object.keys(p.fields ?? {})))].sort(), [trend]);
  const first = trend[0];
  const last = trend[trend.length - 1];
  const delta = first && last && trend.length > 1 ? last.required_complete_percent - first.required_complete_percent : null;
  const missing = rep.failure && routeMissing(rep.failure);

  const csv = async () => {
    setCsvBusy(true);
    setCsvErr('');
    try {
      await downloadCompletenessCSV(q);
    } catch (cause) {
      setCsvErr(adminErrorText(cause));
    } finally {
      setCsvBusy(false);
    }
  };

  return (
    <>
      <AdminHeader
        eyebrow="ADMIN · COMPLETENESS"
        title="完整性报告"
        sub="每个字段有多少条目已填写，缺失的按热度排出最该补的；趋势来自每晚的快照。"
        actions={
          <>
            <button type="button" className="btn btn-secondary btn-sm" disabled={csvBusy || !!missing} onClick={() => void csv()}>
              {csvBusy ? '导出中…' : '下载 CSV'}
            </button>
            <button type="button" className="btn btn-secondary btn-sm" disabled={rep.busy} onClick={rep.reload}>
              {rep.busy ? '刷新中…' : '刷新'}
            </button>
          </>
        }
      />
      {missing ? <NotDeployed what="完整性报告接口（admin/reports/completeness）" /> : <AdminNotice error={rep.error || csvErr} />}

      <div className="adm-toolbar">
        <div className="adm-seg" role="tablist" aria-label="实体">
          {(['release', 'performer'] as Entity[]).map(e => (
            <button key={e} type="button" role="tab" aria-selected={entity === e} className={`adm-seg-item ${entity === e ? 'on' : ''}`} onClick={() => set({entity: e === 'release' ? null : e, source: null, group_by: e === 'performer' && groupBy === 'maker' ? null : groupBy === 'field' ? null : groupBy})}>
              {ENTITY_LABELS[e]}
            </button>
          ))}
        </div>
        <div className="adm-seg" role="tablist" aria-label="分组">
          {(['field', 'source', 'maker'] as GroupBy[])
            .filter(g => g !== 'maker' || entity === 'release')
            .map(g => (
              <button key={g} type="button" role="tab" aria-selected={groupBy === g} className={`adm-seg-item ${groupBy === g ? 'on' : ''}`} onClick={() => set({group_by: g === 'field' ? null : g})}>
                {GROUP_LABELS[g]}
              </button>
            ))}
        </div>
        <label className="adm-field">
          只看来源
          <select value={source} onChange={e => set({source: e.target.value || null})}>
            <option value="">全部来源</option>
            {(r?.sources ?? []).map(s => (
              <option key={s.source_id} value={String(s.source_id)}>
                {s.name}
              </option>
            ))}
            {source && !(r?.sources ?? []).some(s => String(s.source_id) === source) && <option value={source}>{source}</option>}
          </select>
        </label>
        <label className="adm-field">
          趋势快照
          <select value={runs} onChange={e => set({runs: e.target.value === '14' ? null : e.target.value})}>
            {RUNS.map(n => (
              <option key={n} value={n}>
                最近 {n} 次
              </option>
            ))}
          </select>
        </label>
      </div>

      <section className="adm-kpis">
        <div className="adm-kpi">
          <span className="adm-kpi-label">{ENTITY_LABELS[entity]}总数</span>
          <span className="adm-kpi-value">{fmtNum(r?.total)}</span>
          <span className="adm-kpi-sub">{r ? `生成于 ${fmtTime(r.generated_at)}` : ''}</span>
        </div>
        <div className="adm-kpi">
          <span className="adm-kpi-label">必填字段全部齐全</span>
          <span className={`adm-kpi-value ${r ? tone(r.required_complete_percent) : ''}`}>{r ? pctText(r.required_complete_percent) : '—'}</span>
          <span className="adm-kpi-sub">{r ? `${fmtNum(r.required_complete)} 条` : ''}</span>
        </div>
        <div className="adm-kpi">
          <span className="adm-kpi-label">趋势变化</span>
          <span className={`adm-kpi-value ${delta === null ? '' : delta >= 0 ? 'green' : 'red'}`}>{delta === null ? '—' : `${delta >= 0 ? '+' : ''}${Math.round(delta * 10) / 10}`}</span>
          <span className="adm-kpi-sub">{delta === null ? '至少两次快照后显示' : `${first.date} → ${last.date}（百分点）`}</span>
        </div>
      </section>

      <section className="adm-card">
        <div className="adm-card-head">
          <h2>趋势</h2>
          {trendFields.length > 0 && (
            <label className="adm-field" style={{flexDirection: 'row', alignItems: 'center'}}>
              对比字段
              <select value={trendField} onChange={e => setTrendField(e.target.value)}>
                <option value="">不对比</option>
                {trendFields.map(f => (
                  <option key={f} value={f}>
                    {fieldLabel(f)}
                  </option>
                ))}
              </select>
            </label>
          )}
        </div>
        {trend.length === 0 ? (
          <p className="muted">还没有快照：夜间任务（completeness）运行后在这里显示趋势。</p>
        ) : (
          <>
            <div className="adm-trend-legend">
              <span>
                <i />
                必填完整率
              </span>
              {trendField && (
                <span>
                  <i className="alt" />
                  {fieldLabel(trendField)}
                </span>
              )}
            </div>
            <TrendChart points={trend} field={trendField} />
            <details>
              <summary>表格视图</summary>
              <div className="adm-table-wrap">
                <table className="adm-table">
                  <thead>
                    <tr>
                      <th>日期</th>
                      <th className="num">总数</th>
                      <th className="num">必填完整</th>
                      {trendField && <th className="num">{fieldLabel(trendField)}</th>}
                    </tr>
                  </thead>
                  <tbody>
                    {trend.map(p => (
                      <tr key={p.date}>
                        <td>{p.date}</td>
                        <td className="num">{fmtNum(p.total)}</td>
                        <td className="num">{pctText(p.required_complete_percent)}</td>
                        {trendField && <td className="num">{pctText(p.fields?.[trendField])}</td>}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </details>
          </>
        )}
      </section>

      {groupBy !== 'field' && r && (
        <section className="adm-card">
          <h2>
            {GROUP_LABELS[groupBy]}（{(r.groups ?? []).length}）
          </h2>
          <GroupsTable entity={entity} groups={r.groups ?? []} groupBy={groupBy} />
        </section>
      )}

      <section className="adm-card">
        <h2>字段覆盖（按缺失热度排序）</h2>
        <FieldsTable entity={entity} fields={fields} showSource={!!source} />
      </section>

      <section className="adm-card">
        <h2>来源覆盖</h2>
        <div className="adm-table-wrap">
          <table className="adm-table">
            <thead>
              <tr>
                <th>来源</th>
                <th>类型</th>
                <th className="num">提供的条目</th>
                <th>占比</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {r && (r.sources ?? []).length === 0 && <EmptyRow cols={5}>没有来源数据</EmptyRow>}
              {(r?.sources ?? []).map(s => (
                <tr key={s.source_id} className={String(s.source_id) === source ? 'sel' : ''}>
                  <td>
                    {s.name} <span className="muted mono">#{s.source_id}</span>
                  </td>
                  <td className="mono">{s.source_kind}</td>
                  <td className="num">{fmtNum(s.entities)}</td>
                  <td style={{minWidth: 180}}>
                    <PctBar v={s.percent} />
                  </td>
                  <td>
                    <button type="button" className="adm-link" onClick={() => set({source: String(s.source_id) === source ? null : String(s.source_id)})}>
                      {String(s.source_id) === source ? '看全部' : '只看此来源'}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
    </>
  );
}
