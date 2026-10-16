import {useEffect, useMemo, useState, type DragEvent, type KeyboardEvent} from 'react';
import {AdminHeader, AdminNotice, EmptyRow, NotDeployed, Pill, fmtTime} from '../components/AdminUI';
import {
  POLICY_FIELDS,
  TIER_LABELS,
  UNKNOWN_TIER_PRIORITY,
  deleteFieldPolicy,
  fieldLabel,
  getFieldPolicies,
  putFieldPolicy,
  routeMissing,
  type EffectiveSource,
  type FieldPolicy,
} from '../lib/adminGovApi';
import {useAdminAction, useAdminLoad} from '../lib/adminContext';

export const meta = () => [{title: 'REEL · 字段优先级'}];

// 字段优先级 (FINAL_DATA_DESIGN 2.4): which source wins a canonical field.
// Every source starts at its source_kind tier default; a policy row
// (source_field_policies) overrides it for one entity / field. Lower wins.
// GET admin/field-policies[?entity_type&field_name] (policies, tier defaults,
// effective order), PUT admin/field-policies, DELETE
// admin/field-policies/{entity_type}/{field}/{source_id}?reason=.

type Entity = 'release' | 'performer';
const ENTITY_LABELS: Record<Entity, string> = {release: '作品', performer: '演员'};

type Row = EffectiveSource & {draft: number};

/** Spaces a new order 10 apart, keeping every priority inside 0–1000. */
const spaced = (rows: Row[]): Row[] => rows.map((r, i) => ({...r, draft: Math.min(1000, (i + 1) * 10)}));

function OrderEditor({entity, field, policies, onChanged}: {entity: Entity; field: string; policies: FieldPolicy[]; onChanged: () => void}) {
  const eff = useAdminLoad(() => getFieldPolicies(entity, field), [entity, field]);
  const act = useAdminAction();
  const [rows, setRows] = useState<Row[]>([]);
  const [dragging, setDragging] = useState<number | null>(null);
  const [over, setOver] = useState<number | null>(null);
  const [reason, setReason] = useState('');

  useEffect(() => {
    setRows((eff.data?.effective ?? []).map(e => ({...e, draft: e.priority})));
  }, [eff.data]);

  const sorted = useMemo(() => [...rows].sort((a, b) => a.draft - b.draft || a.source_id - b.source_id), [rows]);
  const changed = rows.filter(r => r.draft !== r.priority);
  const policyFor = (sourceId: number) => policies.find(p => p.source_id === sourceId);

  const move = (from: number, to: number) => {
    if (to < 0 || to >= sorted.length || from === to) return;
    const next = [...sorted];
    const [item] = next.splice(from, 1);
    next.splice(to, 0, item);
    setRows(spaced(next));
  };
  const onKey = (e: KeyboardEvent<HTMLLIElement>, i: number) => {
    if (e.key === 'ArrowUp' && (e.altKey || e.metaKey)) {
      e.preventDefault();
      move(i, i - 1);
    } else if (e.key === 'ArrowDown' && (e.altKey || e.metaKey)) {
      e.preventDefault();
      move(i, i + 1);
    }
  };
  const onDrop = (e: DragEvent, i: number) => {
    e.preventDefault();
    if (dragging !== null) move(dragging, i);
    setDragging(null);
    setOver(null);
  };
  const setDraft = (sourceId: number, v: number) => setRows(prev => prev.map(r => (r.source_id === sourceId ? {...r, draft: v} : r)));

  const save = () =>
    void act
      .attempt(
        'save',
        async () => {
          for (const r of changed) {
            await putFieldPolicy({entity_type: entity, field_name: field, source_id: r.source_id, priority: r.draft, reason: reason.trim() || undefined});
          }
          return changed.length;
        },
        n => `已保存 ${n} 条优先级`,
      )
      .then(v => {
        if (v !== undefined) {
          setReason('');
          eff.reload();
          onChanged();
        }
      });
  const remove = (r: Row) =>
    void act
      .attempt(`del-${r.source_id}`, () => deleteFieldPolicy(entity, field, r.source_id, reason.trim()), `已删除「${r.source_name}」的策略，恢复为类型默认值`)
      .then(v => {
        if (v) {
          eff.reload();
          onChanged();
        }
      });

  if (eff.failure && routeMissing(eff.failure)) return <NotDeployed what="字段优先级接口（admin/field-policies）" />;
  return (
    <section className="adm-card">
      <div className="adm-card-head">
        <h2>
          生效顺序 · {ENTITY_LABELS[entity]} · {fieldLabel(field)}
        </h2>
        <span className="muted">数字越小越优先；拖动、用 ↑↓ 按钮或 Alt+↑/↓ 调整顺序，也可以直接改数字</span>
      </div>
      <AdminNotice error={eff.error || act.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />
      {eff.data && sorted.length === 0 && <p className="muted">没有可参与的目录来源（RSS 订阅源不参与字段决策）。</p>}
      <ol className="adm-order" aria-label="来源优先顺序">
        {sorted.map((r, i) => {
          const p = policyFor(r.source_id);
          return (
            <li
              key={r.source_id}
              className={`adm-order-item ${dragging === i ? 'dragging' : ''} ${over === i ? 'over' : ''} ${r.draft !== r.priority ? 'changed' : ''}`}
              draggable
              tabIndex={0}
              aria-label={`${i + 1}. ${r.source_name}，优先级 ${r.draft}`}
              onDragStart={e => {
                setDragging(i);
                e.dataTransfer.effectAllowed = 'move';
              }}
              onDragOver={e => {
                e.preventDefault();
                setOver(i);
              }}
              onDragLeave={() => setOver(o => (o === i ? null : o))}
              onDragEnd={() => {
                setDragging(null);
                setOver(null);
              }}
              onDrop={e => onDrop(e, i)}
              onKeyDown={e => onKey(e, i)}
            >
              <span className="adm-order-rank">{i + 1}</span>
              <span className="adm-grip" aria-hidden="true">
                ⋮⋮
              </span>
              <span>
                <strong>{r.source_name}</strong> <span className="muted mono">#{r.source_id}</span>
                <span className="adm-source-meta">
                  {' '}
                  · {TIER_LABELS[r.source_kind] ?? r.source_kind}（默认 {eff.data?.tier_defaults[r.source_kind] ?? UNKNOWN_TIER_PRIORITY}）
                </span>
                {r.from_policy ? (
                  <Pill tone="blue" title={p ? `${p.updated_by ?? '—'} · ${fmtTime(p.updated_at)}` : ''}>
                    策略
                  </Pill>
                ) : (
                  <Pill>类型默认</Pill>
                )}
              </span>
              <span className="actions">
                <button type="button" className="adm-link" aria-label="上移" disabled={i === 0} onClick={() => move(i, i - 1)}>
                  ↑
                </button>
                <button type="button" className="adm-link" aria-label="下移" disabled={i === sorted.length - 1} onClick={() => move(i, i + 1)}>
                  ↓
                </button>
                <input
                  type="number"
                  min={0}
                  max={1000}
                  aria-label={`${r.source_name} 优先级`}
                  value={r.draft}
                  onChange={e => setDraft(r.source_id, Math.max(0, Math.min(1000, Number(e.target.value) || 0)))}
                  onKeyDown={e => e.stopPropagation()}
                />
              </span>
              <span>
                {r.from_policy && (
                  <button type="button" className="adm-link" disabled={act.pending !== null} onClick={() => remove(r)}>
                    删除策略
                  </button>
                )}
              </span>
            </li>
          );
        })}
      </ol>
      <div className="adm-toolbar">
        <input className="adm-input" placeholder="修改理由（可选，写入修改记录）" value={reason} onChange={e => setReason(e.target.value)} style={{flex: 1, minWidth: 260}} />
        <button type="button" className="btn btn-primary btn-sm" disabled={changed.length === 0 || act.pending !== null} onClick={save}>
          {act.pending === 'save' ? '保存中…' : `保存（${changed.length} 处修改）`}
        </button>
        <button type="button" className="btn btn-secondary btn-sm" disabled={changed.length === 0} onClick={() => setRows(prev => prev.map(r => ({...r, draft: r.priority})))}>
          还原
        </button>
      </div>
      <p className="adm-field-hint">
        保存时只为改动的来源写入策略行；新顺序按 10、20、30… 编号。删除策略后该来源回到其类型默认值。已有的字段值会在下一次入库或重算时按新顺序生效。
      </p>
    </section>
  );
}

export default function AdminFieldPolicies() {
  const all = useAdminLoad(() => getFieldPolicies());
  const [entity, setEntity] = useState<Entity>('release');
  const [field, setField] = useState(POLICY_FIELDS.release[0]);
  const [custom, setCustom] = useState('');
  const policies = all.data?.policies ?? [];

  const fields = useMemo(() => {
    const set = new Set(POLICY_FIELDS[entity]);
    for (const p of policies) if (p.entity_type === entity) set.add(p.field_name);
    return [...set];
  }, [entity, policies]);
  const countByField = useMemo(() => {
    const m: Record<string, number> = {};
    for (const p of policies) if (p.entity_type === entity) m[p.field_name] = (m[p.field_name] ?? 0) + 1;
    return m;
  }, [entity, policies]);
  const tiers = Object.entries(all.data?.tier_defaults ?? {}).sort((a, b) => a[1] - b[1]);
  const missing = all.failure && routeMissing(all.failure);

  const pickEntity = (e: Entity) => {
    setEntity(e);
    setField(POLICY_FIELDS[e][0]);
  };

  return (
    <>
      <AdminHeader
        eyebrow="ADMIN · FIELD POLICY"
        title="字段优先级"
        sub="多个来源给出同一字段时，按这里的顺序决定采用哪个值；管理员手动覆盖始终优先于任何来源。"
        actions={
          <button type="button" className="btn btn-secondary btn-sm" disabled={all.busy} onClick={all.reload}>
            {all.busy ? '刷新中…' : '刷新'}
          </button>
        }
      />
      {missing ? <NotDeployed what="字段优先级接口（admin/field-policies）" /> : <AdminNotice error={all.error} />}

      <div className="adm-grid-2" style={{gridTemplateColumns: 'minmax(0, 1fr) minmax(0, 2fr)'}}>
        <div style={{display: 'flex', flexDirection: 'column', gap: 16}}>
          <section className="adm-card">
            <h2>默认层级</h2>
            <p className="adm-field-hint">没有策略行时，来源按其类型（source_kind）取默认优先级；同级时按来源自身排序和 ID。</p>
            <table className="adm-table">
              <tbody>
                {tiers.map(([k, v]) => (
                  <tr key={k}>
                    <td>{TIER_LABELS[k] ?? k}</td>
                    <td className="mono muted">{k}</td>
                    <td className="num">{v}</td>
                  </tr>
                ))}
                {tiers.length > 0 && (
                  <tr>
                    <td>其他类型</td>
                    <td className="mono muted">*</td>
                    <td className="num">{UNKNOWN_TIER_PRIORITY}</td>
                  </tr>
                )}
              </tbody>
            </table>
          </section>
          <section className="adm-card">
            <h2>字段</h2>
            <div className="adm-seg" role="tablist" aria-label="实体">
              {(['release', 'performer'] as Entity[]).map(e => (
                <button key={e} type="button" role="tab" aria-selected={entity === e} className={`adm-seg-item ${entity === e ? 'on' : ''}`} onClick={() => pickEntity(e)}>
                  {ENTITY_LABELS[e]}
                </button>
              ))}
            </div>
            <div className="adm-table-wrap">
              <table className="adm-table">
                <tbody>
                  {fields.map(f => (
                    <tr key={f} className={f === field ? 'sel' : ''}>
                      <td>
                        <button type="button" className="adm-link" aria-current={f === field} onClick={() => setField(f)}>
                          {fieldLabel(f)}
                        </button>
                      </td>
                      <td className="num">{countByField[f] ? <Pill tone="blue">{countByField[f]} 条策略</Pill> : <span className="muted">默认</span>}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <form
              className="adm-toolbar"
              onSubmit={e => {
                e.preventDefault();
                const v = custom.trim().toLowerCase();
                if (/^[a-z][a-z0-9_]{0,63}$/.test(v)) {
                  setField(v);
                  setCustom('');
                }
              }}
            >
              <input className="adm-input" placeholder="其他字段名（小写，如 cover）" value={custom} onChange={e => setCustom(e.target.value)} pattern="[a-z][a-z0-9_]{0,63}" />
              <button type="submit" className="btn btn-secondary btn-sm" disabled={!custom.trim()}>
                打开
              </button>
            </form>
          </section>
        </div>
        <div style={{display: 'flex', flexDirection: 'column', gap: 16}}>
          {!missing && (
            <OrderEditor key={`${entity}/${field}`} entity={entity} field={field} policies={policies.filter(p => p.entity_type === entity && p.field_name === field)} onChanged={all.reload} />
          )}
          <section className="adm-card">
            <h2>全部策略（{policies.length}）</h2>
            <div className="adm-table-wrap">
              <table className="adm-table">
                <thead>
                  <tr>
                    <th>实体</th>
                    <th>字段</th>
                    <th>来源</th>
                    <th className="num">优先级</th>
                    <th>修改</th>
                  </tr>
                </thead>
                <tbody>
                  {all.data && policies.length === 0 && <EmptyRow cols={5}>还没有策略行：全部字段按类型默认值决定</EmptyRow>}
                  {policies.map(p => (
                    <tr key={`${p.entity_type}/${p.field_name}/${p.source_id}`}>
                      <td>{ENTITY_LABELS[p.entity_type as Entity] ?? p.entity_type}</td>
                      <td>
                        <button
                          type="button"
                          className="adm-link"
                          onClick={() => {
                            setEntity(p.entity_type as Entity);
                            setField(p.field_name);
                          }}
                        >
                          {fieldLabel(p.field_name)}
                        </button>
                      </td>
                      <td>
                        {p.source_name ?? '—'} <span className="muted mono">#{p.source_id}</span>
                      </td>
                      <td className="num">{p.priority}</td>
                      <td className="muted">
                        {p.updated_by ?? '—'} · {fmtTime(p.updated_at)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>
        </div>
      </div>
    </>
  );
}
