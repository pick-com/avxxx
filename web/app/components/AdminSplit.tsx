import {useEffect, useMemo, useState} from 'react';
import {Link} from 'react-router';
import {isAbort} from '../lib/api';
import {adminErrorText} from '../lib/adminApi';
import {AdminNotice, Pill, fmtTime} from './AdminUI';
import {
  SPLIT_USER_TABLES,
  getSplitPreview,
  routeMissing,
  splitEntity,
  undoSplit,
  type SplitEntity,
  type SplitField,
  type SplitPreview,
  type SplitResult,
} from '../lib/adminGovApi';
import {useAdminAction, useAdminLoad} from '../lib/adminContext';

// Entity split (FINAL_DATA_DESIGN 5.5) for the performer and video editors:
// pick source records (and, for releases, media links; user-data rows by id)
// that belong to a different real entity, preview what moves and both
// sides' resulting fields, then split with a reason and the version seen.
// GET admin/{performers|releases}/{id}/split-preview, POST .../split,
// POST admin/splits/{event_id}/undo (from 修改记录).

const ORIGIN_LABELS: Record<string, string> = {
  evidence: '按来源决定',
  override: '保留手动覆盖',
  cleared: '清空（只来自移走的证据）',
  kept: '不变',
  fallback: '必填兜底',
  empty: '空',
};
const TABLE_LABELS: Record<string, string> = {
  source_records: '来源记录',
  release_performer_sources: '演员署名证据',
  release_performers: '演员署名',
  performer_aliases: '别名',
  performer_external_ids: '外部 ID',
  performer_source_metrics: '来源指标',
  release_identifiers: '番号 / 标识',
  release_tag_sources: '标签证据',
  release_tags: '标签',
  release_source_metrics: '来源指标',
  release_media_sources: '媒体映射',
  follows: '关注',
  favorites: '收藏',
  watch_history: '观看历史',
  reservations: '预约',
  watch_later: '稍后看',
  ratings: '评分',
  reviews: '短评',
  playback_sources: '片源',
};

const show = (v: unknown) => (v === null || v === undefined ? '—' : typeof v === 'string' ? v : JSON.stringify(v));
const same = (a: unknown, b: unknown) => JSON.stringify(a ?? null) === JSON.stringify(b ?? null);
const parseIds = (s: string) =>
  s
    .split(/[\s,，]+/)
    .map(x => Number(x))
    .filter(n => Number.isInteger(n) && n > 0);

function FieldSide({title, fields, original}: {title: string; fields: SplitField[]; original?: boolean}) {
  return (
    <div className="adm-split-side">
      <h4>{title}</h4>
      {fields.length === 0 ? (
        <p className="muted">没有字段变化</p>
      ) : (
        <table className="adm-split-fields">
          <tbody>
            {fields.map(f => {
              const changed = original ? f.before !== undefined && !same(f.before, f.value) : f.origin !== 'empty';
              return (
                <tr key={f.column} className={changed && original ? 'changed' : ''}>
                  <td>{f.column}</td>
                  <td>
                    {original && f.before !== undefined && !same(f.before, f.value) && <span className="was">{show(f.before)}</span>}
                    {show(f.value)}
                  </td>
                  <td>
                    <Pill tone={f.origin === 'cleared' ? 'amber' : f.origin === 'override' ? 'blue' : f.origin === 'fallback' ? 'amber' : 'plain'}>
                      {ORIGIN_LABELS[f.origin] ?? f.origin}
                    </Pill>
                    {f.conflict && <Pill tone="red">冲突</Pill>}
                    {f.source_record_id ? <span className="muted mono"> #{f.source_record_id}</span> : null}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
    </div>
  );
}

export function SplitPanel({
  entity,
  id,
  version,
  mediaLinks = [],
  onDone,
  onClose,
}: {
  entity: SplitEntity;
  id: number;
  version: number;
  mediaLinks?: string[];
  onDone: (r: SplitResult) => void;
  onClose: () => void;
}) {
  const base = useAdminLoad(signal => getSplitPreview(entity, id, [], [], signal), [entity, id, version]);
  const act = useAdminAction();
  const [picked, setPicked] = useState<number[]>([]);
  const [links, setLinks] = useState<string[]>([]);
  const [userData, setUserData] = useState<Record<string, string>>({});
  const [preview, setPreview] = useState<SplitPreview | null>(null);
  const [previewErr, setPreviewErr] = useState('');
  const [previewBusy, setPreviewBusy] = useState(false);
  const [reason, setReason] = useState('');
  const [ver, setVer] = useState(version);
  const [result, setResult] = useState<SplitResult | null>(null);

  useEffect(() => setVer(version), [version]);

  const records = base.data?.source_records ?? [];
  const moveUserData = useMemo(() => {
    const out: Record<string, number[]> = {};
    for (const [t, v] of Object.entries(userData)) {
      const ids = parseIds(v);
      if (ids.length) out[t] = ids;
    }
    return out;
  }, [userData]);

  // Live preview: debounce selection changes; user-data rows are applied by
  // the split itself (the preview route takes records and media links only).
  useEffect(() => {
    if (picked.length === 0) {
      setPreview(null);
      setPreviewErr('');
      return;
    }
    const ctrl = new AbortController();
    const t = setTimeout(() => {
      setPreviewBusy(true);
      setPreviewErr('');
      getSplitPreview(entity, id, picked, links, ctrl.signal)
        .then(p => !ctrl.signal.aborted && setPreview(p))
        .catch(cause => {
          if (isAbort(cause) || ctrl.signal.aborted) return;
          setPreview(null);
          setPreviewErr(adminErrorText(cause));
        })
        .finally(() => !ctrl.signal.aborted && setPreviewBusy(false));
    }, 250);
    return () => {
      clearTimeout(t);
      ctrl.abort();
    };
  }, [entity, id, picked, links]);

  const toggle = (rid: number) => setPicked(p => (p.includes(rid) ? p.filter(x => x !== rid) : [...p, rid].sort((a, b) => a - b)));
  const toggleLink = (l: string) => setLinks(p => (p.includes(l) ? p.filter(x => x !== l) : [...p, l]));
  const allPicked = records.length > 0 && picked.length === records.length;

  const confirm = () =>
    void act
      .attempt(
        'split',
        () =>
          splitEntity(entity, id, {
            source_records: picked,
            version: ver,
            reason: reason.trim(),
            media_links: links.length ? links : undefined,
            move_user_data: Object.keys(moveUserData).length ? moveUserData : undefined,
          }),
        r => `已拆分：新${entity === 'performer' ? '演员' : '作品'} #${r.new_id}（${r.new_slug}），修改记录 #${r.review_event_id}`,
      )
      .then(r => {
        if (r) {
          setResult(r);
          onDone(r);
        }
      });

  if (base.failure && routeMissing(base.failure)) {
    return (
      <section className="adm-card">
        <div className="adm-notice adm-notice-warn" role="status">
          后端未部署：拆分接口（admin/{entity === 'performer' ? 'performers' : 'releases'}/{'{id}'}/split-preview）。
        </div>
      </section>
    );
  }

  const plan = preview?.plan;
  const tables = (plan?.tables ?? []).filter(t => (t.move?.length ?? 0) + (t.copy?.length ?? 0) > 0);
  const newHref = (rid: number) => (entity === 'performer' ? `/admin/actors?id=${rid}` : `/admin/videos/${rid}`);
  const stale = base.data && base.data.version !== ver;

  return (
    <section className="adm-card" aria-label="拆分">
      <div className="adm-card-head">
        <h2>拆分{entity === 'performer' ? '演员' : '作品'}</h2>
        <button type="button" className="adm-link" onClick={onClose}>
          关闭
        </button>
      </div>
      <p className="adm-field-hint">
        当一个{entity === 'performer' ? '演员' : '作品'}其实混合了两个真实实体时，选出属于另一个实体的来源记录：它们和只由它们支持的别名、番号、署名等会移到新建的实体上。原实体保留 slug、手动覆盖和用户数据（除非在下面明确移动）。拆分写入修改记录，可以撤销。
      </p>
      <AdminNotice error={base.error} />
      {result ? (
        <div className="adm-notice adm-notice-ok" role="status">
          已拆分出新{entity === 'performer' ? '演员' : '作品'} <Link to={newHref(result.new_id)}>#{result.new_id} · {result.new_slug}</Link>
          。修改记录 #{result.review_event_id}，在「修改记录」里可以撤销。
        </div>
      ) : (
        <>
          <h3>1. 选择移到新实体的来源记录（{picked.length}/{records.length}）</h3>
          {base.data && records.length === 0 && <p className="muted">这个实体没有可选择的来源记录。</p>}
          {records.length > 0 && (
            <div className="adm-split-records" role="group" aria-label="来源记录">
              {records.map(r => (
                <label key={`${r.kind}-${r.id}`} className={`adm-split-record ${picked.includes(r.id) ? 'on' : ''}`}>
                  <input type="checkbox" checked={picked.includes(r.id)} onChange={() => toggle(r.id)} />
                  <span>
                    <strong>{r.name || r.credited_name || r.source_record_key}</strong>{' '}
                    <Pill tone={r.kind === 'credit' ? 'blue' : 'plain'}>{r.kind === 'credit' ? '署名证据' : '来源记录'}</Pill>
                    <span className="adm-source-meta">
                      {' '}
                      #{r.id} · {r.source_name} · <span className="mono">{r.source_record_key}</span>
                      {r.release_id ? (
                        <>
                          {' '}
                          · 作品 <Link to={`/admin/videos/${r.release_id}`}>#{r.release_id}</Link>
                        </>
                      ) : null}{' '}
                      · 最近 {fmtTime(r.last_seen_at)}
                    </span>
                    {r.source_url && (
                      <a href={r.source_url} target="_blank" rel="noreferrer noopener" className="adm-source-meta mono" style={{display: 'block', overflowWrap: 'anywhere'}}>
                        {r.source_url}
                      </a>
                    )}
                  </span>
                </label>
              ))}
            </div>
          )}
          {allPicked && <div className="adm-notice adm-notice-warn">不能把全部来源记录都移走：原实体至少要保留一条。</div>}

          {entity === 'release' && mediaLinks.length > 0 && (
            <>
              <h3>媒体映射（可选）</h3>
              <div className="adm-chips">
                {mediaLinks.map(l => (
                  <label key={l} className="adm-check">
                    <input type="checkbox" checked={links.includes(l)} onChange={() => toggleLink(l)} /> {l}
                  </label>
                ))}
              </div>
            </>
          )}

          <details>
            <summary>移动用户数据（可选，默认全部留在原实体）</summary>
            <div className="adm-form-grid" style={{marginTop: 8}}>
              {SPLIT_USER_TABLES[entity].map(t => (
                <label key={t.table} className="adm-field">
                  {t.label}（{t.key === 'user_id' ? '用户 ID' : '记录 ID'}，逗号分隔）
                  <input value={userData[t.table] ?? ''} onChange={e => setUserData(u => ({...u, [t.table]: e.target.value}))} placeholder="例如 7, 12" />
                </label>
              ))}
            </div>
            <p className="adm-field-hint">预览只计算来源记录和媒体映射；用户数据在确认拆分时按 ID 移动；不在原实体上的 ID 会让拆分被拒绝（400）。</p>
          </details>

          <h3>2. 预览 {previewBusy && <span className="muted">计算中…</span>}</h3>
          {picked.length === 0 && <p className="muted">选择至少一条来源记录后显示预览。</p>}
          <AdminNotice error={previewErr} />
          {plan && (
            <>
              <div className="adm-chips">
                {tables.map(t => (
                  <Pill key={t.table} tone={t.user_data ? 'purple' : 'plain'}>
                    {TABLE_LABELS[t.table] ?? t.table}：移 {t.move?.length ?? 0}
                    {t.copy?.length ? ` · 复制 ${t.copy.length}` : ''}
                  </Pill>
                ))}
                {tables.length === 0 && <span className="muted">没有派生行会移动</span>}
              </div>
              <div className="adm-split-sides">
                <FieldSide title={`原${entity === 'performer' ? '演员' : '作品'} #${id}（拆分后）`} fields={plan.original_fields ?? []} original />
                <FieldSide title={`新${entity === 'performer' ? '演员' : '作品'}（新建）`} fields={plan.new_fields ?? []} />
              </div>
            </>
          )}

          <h3>3. 确认</h3>
          <div className="adm-toolbar">
            <input className="adm-input" placeholder="拆分理由（写入修改记录）" value={reason} onChange={e => setReason(e.target.value)} style={{flex: 1, minWidth: 280}} maxLength={2000} />
            <label className="adm-field" style={{width: 110}}>
              版本
              <input type="number" min={1} value={ver} onChange={e => setVer(Number(e.target.value) || 0)} />
            </label>
            <button
              type="button"
              className="btn btn-primary btn-sm"
              disabled={picked.length === 0 || allPicked || !reason.trim() || ver <= 0 || act.pending !== null || !!previewErr}
              onClick={confirm}
            >
              {act.pending === 'split' ? '拆分中…' : '确认拆分'}
            </button>
          </div>
          {stale && <p className="adm-field-hint warn">你看到的版本是 v{ver}，服务器当前是 v{base.data?.version}：先刷新页面，否则拆分会因版本过期被拒绝（409）。</p>}
          <AdminNotice error={act.error} notice={act.notice} />
        </>
      )}
    </section>
  );
}

/** 撤销拆分 on a 修改记录 entry whose action is "split". */
export function UndoSplit({eventId, onDone}: {eventId: number; onDone: () => void}) {
  const act = useAdminAction();
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState('');
  const [done, setDone] = useState('');
  if (done)
    return (
      <div className="adm-notice adm-notice-ok" role="status" style={{marginTop: 6}}>
        {done}
      </div>
    );
  if (!open)
    return (
      <button type="button" className="adm-link" title="把拆出的实体合并回原实体；已撤销过的拆分会被服务器拒绝（409）" onClick={() => setOpen(true)}>
        撤销拆分
      </button>
    );
  return (
    <div className="adm-toolbar" style={{marginTop: 6}}>
      <input className="adm-input" placeholder="撤销理由" value={reason} onChange={e => setReason(e.target.value)} style={{minWidth: 220}} maxLength={2000} />
      <button
        type="button"
        className="btn btn-secondary btn-sm"
        disabled={act.pending !== null}
        onClick={() =>
          void act
            .attempt('undo', () => undoSplit(eventId, reason.trim()))
            .then(v => {
              if (!v) return;
              setDone(`已撤销拆分：#${v.merged_id} 合并回 #${v.original_id}（修改记录 #${v.review_event_id}）`);
              onDone();
            })
        }
      >
        {act.pending ? '撤销中…' : '确认撤销'}
      </button>
      <button type="button" className="adm-link" onClick={() => setOpen(false)}>
        取消
      </button>
      <AdminNotice error={act.error} notice={act.notice} />
    </div>
  );
}
