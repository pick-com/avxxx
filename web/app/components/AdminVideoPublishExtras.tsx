import {useState, type FormEvent} from 'react';
import {Link} from 'react-router';
import {AdminNotice, Pill, fmtTime} from './AdminUI';
import {isMissingEndpoint} from '../lib/api';
import {useAdmin, useAdminAction, useAdminLoad} from '../lib/adminContext';
import type {AdminRelease} from '../lib/adminApi';
import {createHomeSlot, getAdminHomeSlots, type SlotType} from '../lib/adminOpsApi';
import {RIGHTS_KIND_LABELS, addRightsProof, getRightsProofs, removeRightsProof, type RightsProof, type RightsProofKind} from '../lib/adminRightsApi';

// AdminVideo 发布 tab extras (artboard AdminVideo, PRD 11 / 14.3):
//   推荐位   this release's current and future home slots (#98) and an
//            "add to home slot" shortcut (POST admin/home-slots, ops:write);
//            the full schedule stays on /admin/ops.
//   版权凭证 private proof (document reference / URL, notes, valid until);
//            back office only, reads are op-logged (admin/releases/{id}/rights-proofs).

const pad = (n: number) => String(n).padStart(2, '0');
const localInput = (d: Date) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
const SLOT_LABEL: Record<SlotType, string> = {hero: '首页大图（hero）', shelf: '推荐货架（shelf）'};

export function HomeSlotShortcut({r}: {r: AdminRelease}) {
  const {can} = useAdmin();
  const writable = can('ops:write');
  const readable = can('ops:read');
  const act = useAdminAction();
  const [from] = useState(() => new Date().toISOString());
  const slots = useAdminLoad(signal => (readable ? getAdminHomeSlots({from}, signal) : Promise.resolve({items: [], now: from})), [r.id, readable]);
  const now = new Date();
  const [draft, setDraft] = useState({slot_type: 'shelf' as SlotType, starts: localInput(now), ends: localInput(new Date(now.getTime() + 7 * 864e5)), priority: 0});
  const mine = (slots.data?.items ?? []).filter(s => s.target_type === 'release' && s.release_id === r.id);

  const add = async (e: FormEvent) => {
    e.preventDefault();
    const starts = new Date(draft.starts);
    const ends = draft.ends ? new Date(draft.ends) : null;
    if (Number.isNaN(starts.getTime()) || (ends && Number.isNaN(ends.getTime()))) {
      act.setError('请填写有效的开始和结束时间');
      return;
    }
    const v = await act.attempt(
      'slot',
      () =>
        createHomeSlot({
          slot_type: draft.slot_type,
          target_type: 'release',
          release_id: r.id,
          texts: {},
          langs: [],
          regions: [],
          starts_at: starts.toISOString(),
          ends_at: ends ? ends.toISOString() : null,
          priority: draft.priority,
          enabled: true,
        }),
      s => `已加入${SLOT_LABEL[s.slot_type]}（#${s.id}）`,
    );
    if (v) slots.reload();
  };

  if (!readable) return null;
  return (
    <section className="adm-card">
      <div className="adm-card-head">
        <h2>推荐位</h2>
        <Link to="/admin/ops" className="btn btn-secondary btn-sm">
          打开首页排期
        </Link>
      </div>
      <AdminNotice error={slots.error || act.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />
      {r.status !== 'published' && r.status !== 'upcoming' && <p className="adm-field-hint">作品未上线时，推荐位在前台不显示（排期里标为「目标不可见」）。</p>}
      {mine.length === 0 ? (
        <p className="muted">这部作品目前没有进行中或将来的推荐位。</p>
      ) : (
        <ul className="adm-events">
          {mine.map(s => (
            <li key={s.id}>
              <Pill tone={s.enabled ? 'green' : 'plain'}>{SLOT_LABEL[s.slot_type]}</Pill> {fmtTime(s.starts_at)} – {s.ends_at ? fmtTime(s.ends_at) : '不设结束'} · 优先级 {s.priority}
              {s.regions.length > 0 && <span className="muted"> · 地区 {s.regions.join(', ')}</span>}
            </li>
          ))}
        </ul>
      )}
      {writable && (
        <form onSubmit={add}>
          <div className="adm-form-grid">
            <label className="adm-field">
              位置
              <select value={draft.slot_type} onChange={e => setDraft({...draft, slot_type: e.target.value as SlotType})}>
                <option value="shelf">{SLOT_LABEL.shelf}</option>
                <option value="hero">{SLOT_LABEL.hero}</option>
              </select>
            </label>
            <label className="adm-field">
              开始
              <input type="datetime-local" required value={draft.starts} onChange={e => setDraft({...draft, starts: e.target.value})} />
            </label>
            <label className="adm-field">
              结束（空 = 不设结束）
              <input type="datetime-local" value={draft.ends} onChange={e => setDraft({...draft, ends: e.target.value})} />
            </label>
            <label className="adm-field">
              优先级
              <input type="number" value={draft.priority} onChange={e => setDraft({...draft, priority: Number(e.target.value) || 0})} />
            </label>
          </div>
          <p className="adm-field-hint">文案默认用作品标题，地区与语言不限；需要多语言文案或地区定向时到首页排期里编辑。推荐语见上方「分级与地区」。</p>
          <div className="adm-form-actions">
            <button type="submit" className="btn btn-secondary btn-sm" disabled={act.pending === 'slot'}>
              加入推荐位
            </button>
          </div>
        </form>
      )}
    </section>
  );
}

const emptyProof = {kind: 'license' as RightsProofKind, reference: '', url: '', note: '', valid_until: ''};

export function RightsProofs({r}: {r: AdminRelease}) {
  const {can} = useAdmin();
  const writable = can('catalog:write');
  const [showRemoved, setShowRemoved] = useState(false);
  const list = useAdminLoad(signal => getRightsProofs(r.id, showRemoved, signal), [r.id, showRemoved]);
  const act = useAdminAction();
  const [draft, setDraft] = useState(emptyProof);
  const [reasons, setReasons] = useState<Record<number, string>>({});

  const add = async (e: FormEvent) => {
    e.preventDefault();
    if (await act.attempt('proof', () => addRightsProof(r.id, draft), '已添加版权凭证')) {
      setDraft(emptyProof);
      list.reload();
    }
  };
  const remove = async (p: RightsProof) => {
    if (!window.confirm('移除这条版权凭证？记录会保留，可在「显示已移除」里查看。')) return;
    if (await act.attempt(`rm-${p.id}`, () => removeRightsProof(r.id, p.id, reasons[p.id] ?? ''), '已移除')) list.reload();
  };

  if (list.failure && isMissingEndpoint(list.failure)) return null;
  return (
    <section className="adm-card">
      <div className="adm-card-head">
        <h2>版权凭证</h2>
        <label className="muted">
          <input type="checkbox" checked={showRemoved} onChange={e => setShowRemoved(e.target.checked)} /> 显示已移除
        </label>
      </div>
      <p className="adm-field-hint">仅后台可见，不会出现在任何前台页面或接口；每次查看都会记入操作日志。文件本身请放在私有存储或合同库，这里只记录编号 / 链接与说明。</p>
      <AdminNotice error={list.error || act.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />
      {list.data && list.data.length === 0 && <p className="muted">还没有版权凭证。</p>}
      {(list.data ?? []).length > 0 && (
        <div className="adm-table-wrap">
          <table className="adm-table">
            <thead>
              <tr>
                <th>类型</th>
                <th>编号 / 链接</th>
                <th>说明</th>
                <th>有效期至</th>
                <th>添加</th>
                {writable && <th>操作</th>}
              </tr>
            </thead>
            <tbody>
              {(list.data ?? []).map(p => (
                <tr key={p.id}>
                  <td>
                    <Pill tone={p.removed_at ? 'plain' : 'blue'}>{RIGHTS_KIND_LABELS[p.kind] ?? p.kind}</Pill>
                    {p.removed_at && <div className="adm-source-meta">已由 {p.removed_by} 移除</div>}
                  </td>
                  <td className="wrap">
                    {p.reference && <div className="mono">{p.reference}</div>}
                    {p.url && (
                      <a href={p.url} target="_blank" rel="noopener noreferrer nofollow" referrerPolicy="no-referrer" className="adm-link adm-break">
                        {p.url}
                      </a>
                    )}
                  </td>
                  <td className="wrap">{p.note || '—'}</td>
                  <td>{p.valid_until ?? '—'}</td>
                  <td>
                    {p.created_by}
                    <div className="adm-source-meta">{fmtTime(p.created_at)}</div>
                  </td>
                  {writable && (
                    <td>
                      {!p.removed_at && (
                        <div className="adm-row-actions">
                          <input className="adm-input" placeholder="原因" value={reasons[p.id] ?? ''} onChange={e => setReasons(x => ({...x, [p.id]: e.target.value}))} />
                          <button type="button" className="btn btn-danger btn-sm" disabled={act.pending === `rm-${p.id}`} onClick={() => void remove(p)}>
                            移除
                          </button>
                        </div>
                      )}
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {writable && (
        <form onSubmit={add}>
          <div className="adm-form-grid">
            <label className="adm-field">
              类型
              <select value={draft.kind} onChange={e => setDraft({...draft, kind: e.target.value as RightsProofKind})}>
                {(Object.keys(RIGHTS_KIND_LABELS) as RightsProofKind[]).map(k => (
                  <option key={k} value={k}>
                    {RIGHTS_KIND_LABELS[k]}
                  </option>
                ))}
              </select>
            </label>
            <label className="adm-field">
              文件编号 / 私有存储位置
              <input value={draft.reference} maxLength={500} onChange={e => setDraft({...draft, reference: e.target.value})} placeholder="例如 合同 C-2026-018" />
            </label>
            <label className="adm-field">
              链接（http/https）
              <input type="url" value={draft.url} maxLength={2000} onChange={e => setDraft({...draft, url: e.target.value})} />
            </label>
            <label className="adm-field">
              有效期至
              <input type="date" value={draft.valid_until} onChange={e => setDraft({...draft, valid_until: e.target.value})} />
            </label>
            <label className="adm-field span-all">
              说明
              <textarea rows={2} maxLength={2000} value={draft.note} onChange={e => setDraft({...draft, note: e.target.value})} />
            </label>
          </div>
          <div className="adm-form-actions">
            <button type="submit" className="btn btn-secondary btn-sm" disabled={act.pending === 'proof' || (!draft.reference.trim() && !draft.url.trim())}>
              添加凭证
            </button>
          </div>
        </form>
      )}
    </section>
  );
}
