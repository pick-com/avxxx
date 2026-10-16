import {useEffect, useState} from 'react';
import {Link, useSearchParams} from 'react-router';
import {AdminHeader, AdminNotice, EmptyRow, MoreButton, NotDeployed, Pill, fmtTime, jsonText, type Tone} from '../components/AdminUI';
import {isMissingEndpoint} from '../lib/api';
import {
  TAKEDOWN_STATUSES,
  getAdminTakedown,
  getAdminTakedowns,
  getReleaseMedia,
  releaseSlugsFromURLs,
  restoreTakedown,
  setTakedownStatus,
  takedownAction,
  type AdminMediaAsset,
  type AdminTakedown,
  type TakedownStatus,
} from '../lib/adminComplianceApi';
import {useAdminAction, useAdminLoad} from '../lib/adminContext';
import '../styles/admin-compliance.css';

export const meta = () => [{title: 'REEL · 版权投诉'}];

// 版权投诉 (AdminOps; PRD 15: temporary takedown within 24 h). The queue
// shows each ticket's SLA (due 24 h after filing; overdue until the first
// action or a rejection). Actions reuse the existing paths: 下架作品 moves
// the release to unpublished through the release status machine (the public
// page answers 451), 屏蔽图片 suppresses a stored image (objects purged after
// 24 h). 恢复 undoes every active action after a counter-notice. Every step
// is a review_events row with the 操作人.

const STATUS_LABELS: Record<TakedownStatus, string> = {
  received: '待处理',
  reviewing: '核对中',
  actioned: '已下架',
  rejected: '已驳回',
  counter_received: '收到反通知',
  restored: '已恢复',
  closed: '已结案',
};
const STATUS_TONES: Record<TakedownStatus, Tone> = {
  received: 'amber',
  reviewing: 'blue',
  actioned: 'purple',
  rejected: 'plain',
  counter_received: 'red',
  restored: 'green',
  closed: 'plain',
};
const OPEN = new Set<TakedownStatus>(['received', 'reviewing', 'actioned', 'counter_received']);

function SlaBadge({t}: {t: AdminTakedown}) {
  if (t.first_action_at) return <span className="muted adm-sla">已处理 {fmtTime(t.first_action_at)}</span>;
  if (!OPEN.has(t.status)) return <span className="muted">—</span>;
  const left = new Date(t.due_at).getTime() - Date.now();
  if (t.overdue || left <= 0) return <Pill tone="red">超时 {Math.max(1, Math.round(-left / 3_600_000))} 小时</Pill>;
  const hours = left / 3_600_000;
  return <Pill tone={hours < 6 ? 'amber' : 'plain'}>剩 {hours < 1 ? `${Math.max(1, Math.round(hours * 60))} 分钟` : `${Math.floor(hours)} 小时`}</Pill>;
}

function Detail({id, onChanged}: {id: number; onChanged: () => void}) {
  const act = useAdminAction();
  const detail = useAdminLoad(() => getAdminTakedown(id), [id]);
  const [slug, setSlug] = useState('');
  const [reason, setReason] = useState('');
  const [note, setNote] = useState('');
  const [media, setMedia] = useState<{releaseId: number; assets: AdminMediaAsset[] | null; error: string} | null>(null);
  const t = detail.data;

  useEffect(() => {
    setSlug(t ? (releaseSlugsFromURLs(t.infringing_urls ?? [])[0] ?? '') : '');
    setMedia(null);
  }, [t?.id]);

  const done = (v: AdminTakedown | undefined) => {
    if (!v) return;
    detail.setData(v);
    onChanged();
  };
  const unpublish = () =>
    slug.trim() &&
    void act.attempt('unpublish', () => takedownAction(id, {action: 'unpublish_release', release_slug: slug.trim(), reason}), `已下架 ${slug.trim()}（前台返回 451）`).then(done);
  const suppress = (assetId: number) =>
    void act.attempt(`suppress-${assetId}`, () => takedownAction(id, {action: 'suppress_image', media_asset_id: assetId, reason}), `已屏蔽图片 #${assetId}（24 小时后删除）`).then(done);
  const restore = () => {
    if (!reason.trim()) return act.setError('恢复需要填写原因');
    if (!window.confirm('恢复此编号下所有已下架的内容？')) return;
    void act.attempt('restore', () => restoreTakedown(id, reason.trim()), '已恢复').then(done);
  };
  const status = (to: TakedownStatus) => {
    if (to === 'rejected' && !note.trim()) return act.setError('驳回需要填写说明');
    void act.attempt(`status-${to}`, () => setTakedownStatus(id, to, note.trim()), `状态 → ${STATUS_LABELS[to]}`).then(done);
  };
  const loadMedia = async () => {
    const target = t?.targets?.find(x => x.target_type === 'release');
    const releaseId = target?.target_id;
    if (!releaseId) return act.setError('先下架作品，或在「视频」里查到作品编号');
    try {
      setMedia({releaseId, assets: await getReleaseMedia(releaseId), error: ''});
    } catch (cause) {
      setMedia({releaseId, assets: null, error: isMissingEndpoint(cause) ? '图片存储未配置（admin/media 不可用）' : String(cause)});
    }
  };

  if (!t) return <section className="adm-card">{detail.error ? <AdminNotice error={detail.error} /> : <p className="muted">加载中…</p>}</section>;
  const canAct = t.status === 'received' || t.status === 'reviewing' || t.status === 'actioned';
  const canRestore = (t.status === 'actioned' || t.status === 'counter_received') && t.active_targets > 0;
  const manual: TakedownStatus[] = ({received: ['reviewing', 'rejected'], reviewing: ['rejected', 'closed'], actioned: ['closed'], counter_received: ['closed'], restored: ['closed'], rejected: ['reviewing'], closed: []} as Record<TakedownStatus, TakedownStatus[]>)[t.status];

  return (
    <section className="adm-card adm-editor">
      <h2>
        <span className="mono">{t.ticket}</span> <Pill tone={STATUS_TONES[t.status]}>{STATUS_LABELS[t.status]}</Pill> <SlaBadge t={t} />
      </h2>
      <AdminNotice error={act.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />
      <dl className="adm-kv">
        <dt>投诉人</dt>
        <dd>
          {t.claimant_name}（{t.relationship === 'agent' ? '代理人' : '权利人本人'}） · <a href={`mailto:${t.claimant_email}`}>{t.claimant_email}</a>
          {t.claimant_phone && ` · ${t.claimant_phone}`}
        </dd>
        {t.claimant_address && (
          <>
            <dt>地址</dt>
            <dd>{t.claimant_address}</dd>
          </>
        )}
        <dt>权利人</dt>
        <dd>{t.rights_owner}</dd>
        <dt>签名</dt>
        <dd>{t.signature}</dd>
        <dt>提交</dt>
        <dd>
          {fmtTime(t.created_at)} · 时限 {fmtTime(t.due_at)} {t.lang && `· ${t.lang}`}
        </dd>
      </dl>
      <h3>作品</h3>
      <p className="adm-quote">{t.work_description}</p>
      <h3>本站侵权地址</h3>
      <ul className="adm-urls">
        {(t.infringing_urls ?? []).map(u => (
          <li key={u}>
            <a href={u} target="_blank" rel="noreferrer noopener">
              {u}
            </a>
          </li>
        ))}
      </ul>
      {(t.original_urls ?? []).length > 0 && (
        <>
          <h3>正版地址</h3>
          <ul className="adm-urls">
            {(t.original_urls ?? []).map(u => (
              <li key={u}>{u}</li>
            ))}
          </ul>
        </>
      )}
      {t.counter_received_at && (
        <>
          <h3>反通知（{fmtTime(t.counter_received_at)}）</h3>
          <dl className="adm-kv">
            <dt>提交人</dt>
            <dd>
              {t.counter_name} · {t.counter_email && <a href={`mailto:${t.counter_email}`}>{t.counter_email}</a>}
            </dd>
            <dt>地址</dt>
            <dd>{t.counter_address}</dd>
            <dt>签名</dt>
            <dd>{t.counter_signature}</dd>
          </dl>
          <p className="adm-quote">{t.counter_statement}</p>
        </>
      )}

      <h3>处理</h3>
      <label className="adm-field">
        原因 / 说明（写入 review_events）
        <input value={reason} onChange={e => setReason(e.target.value)} placeholder="例如：核对权利证明后暂时下架" />
      </label>
      {canAct && (
        <div className="adm-row-actions">
          <label className="adm-field">
            作品 slug
            <input value={slug} onChange={e => setSlug(e.target.value)} placeholder="从侵权地址自动识别" />
          </label>
          <button type="button" className="btn btn-primary btn-sm" disabled={!slug.trim() || act.pending === 'unpublish'} onClick={unpublish}>
            下架作品
          </button>
          <button type="button" className="btn btn-secondary btn-sm" onClick={() => void loadMedia()}>
            查看作品图片
          </button>
        </div>
      )}
      {media && (
        <div>
          {media.error ? (
            <p className="muted">{media.error}</p>
          ) : (media.assets ?? []).filter(a => !a.derived_from).length === 0 ? (
            <p className="muted">作品 #{media.releaseId} 没有存储的图片</p>
          ) : (
            <ul className="adm-events">
              {(media.assets ?? [])
                .filter(a => !a.derived_from)
                .map(a => (
                  <li key={a.id}>
                    <span className="mono">
                      #{a.id} {a.asset_type}
                    </span>
                    <span className="muted mono">{a.object_key}</span>
                    {a.suppressed_at ? (
                      <Pill>已屏蔽</Pill>
                    ) : (
                      <button type="button" className="btn btn-secondary btn-sm" disabled={!canAct || act.pending === `suppress-${a.id}`} onClick={() => suppress(a.id)}>
                        屏蔽图片
                      </button>
                    )}
                  </li>
                ))}
            </ul>
          )}
        </div>
      )}
      <div className="adm-row-actions">
        {canRestore && (
          <button type="button" className="btn btn-primary btn-sm" disabled={act.pending === 'restore'} onClick={restore}>
            恢复全部（反通知 / 投诉不成立）
          </button>
        )}
        <input className="adm-input" value={note} onChange={e => setNote(e.target.value)} placeholder="状态说明（驳回必填）" />
        {manual.map(s => (
          <button key={s} type="button" className="btn btn-secondary btn-sm" disabled={act.pending === `status-${s}`} onClick={() => status(s)}>
            {STATUS_LABELS[s]}
          </button>
        ))}
      </div>
      {t.resolution_note && <p className="muted">说明：{t.resolution_note}</p>}

      {(t.targets ?? []).length > 0 && (
        <>
          <h3>已执行</h3>
          <ul className="adm-events">
            {(t.targets ?? []).map(x => (
              <li key={x.id}>
                <Pill tone={x.state === 'active' ? 'purple' : 'green'}>{x.state === 'active' ? '生效中' : '已恢复'}</Pill>
                {x.target_type === 'release' ? (
                  <Link to={`/admin/videos/${x.target_id}`}>
                    下架作品 {x.detail?.slug ?? `#${x.target_id}`}（原状态 {x.previous_status ?? '—'}）
                  </Link>
                ) : (
                  <span>
                    屏蔽图片 #{x.target_id} {x.detail?.asset_type}
                  </span>
                )}
                <span className="muted">
                  {x.actor} · {fmtTime(x.created_at)}
                  {x.restored_at && ` · 恢复 ${x.restored_by} ${fmtTime(x.restored_at)}`}
                </span>
              </li>
            ))}
          </ul>
        </>
      )}
      <h3>记录</h3>
      <ul className="adm-events">
        {(t.events ?? []).map(e => (
          <li key={e.id}>
            <span className="mono">{e.action}</span>
            <span>{e.actor}</span>
            <span className="muted">{fmtTime(e.created_at)}</span>
            {e.reason && <span>{e.reason}</span>}
            <span className="muted mono">{jsonText(e.after)}</span>
          </li>
        ))}
        {(t.events ?? []).length === 0 && <li className="muted">暂无</li>}
      </ul>
    </section>
  );
}

export default function AdminTakedowns() {
  const [params, setParams] = useSearchParams();
  const status = params.get('status') ?? 'open';
  const selected = Number(params.get('id')) || null;
  const query = status === 'open' ? {open: true} : status === 'all' ? {} : {status};
  const list = useAdminLoad(() => getAdminTakedowns({...query, limit: 50}), [status]);
  const act = useAdminAction();
  const [extra, setExtra] = useState<{items: AdminTakedown[]; next: string | null} | null>(null);
  const [moreBusy, setMoreBusy] = useState(false);

  const items = [...(list.data?.items ?? []), ...(extra?.items ?? [])];
  const next = extra ? extra.next : (list.data?.next_cursor ?? null);
  const reload = () => {
    setExtra(null);
    list.reload();
  };
  const more = async () => {
    if (!next) return;
    setMoreBusy(true);
    try {
      const p = await getAdminTakedowns({...query, cursor: next, limit: 50});
      setExtra(prev => ({items: [...(prev?.items ?? []), ...p.items], next: p.next_cursor}));
    } catch (cause) {
      act.setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setMoreBusy(false);
    }
  };
  const setParam = (k: string, v: string) => {
    const p = new URLSearchParams(params);
    if (v) p.set(k, v);
    else p.delete(k);
    if (k === 'status') setExtra(null);
    setParams(p, {replace: true});
  };
  const counts = list.data?.counts;
  const missing = list.failure && isMissingEndpoint(list.failure);

  return (
    <>
      <AdminHeader
        eyebrow="ADMIN · COPYRIGHT"
        title="版权投诉"
        sub={`收到通知后 ${list.data?.sla_hours ?? 24} 小时内处理：暂时下架或驳回。未结 ${counts?.open ?? '—'} 件，超时 ${counts?.overdue ?? '—'} 件。`}
        actions={
          <button type="button" className="btn btn-secondary btn-sm" disabled={list.busy} onClick={reload}>
            {list.busy ? '刷新中…' : '刷新'}
          </button>
        }
      />
      {missing ? <NotDeployed what="版权投诉接口（admin/takedowns）" /> : <AdminNotice error={list.error || act.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />}
      <div className="adm-split">
        <section className="adm-card">
          <div className="adm-toolbar">
            <label className="adm-field">
              状态
              <select value={status} onChange={e => setParam('status', e.target.value === 'open' ? '' : e.target.value)}>
                <option value="open">未结</option>
                <option value="all">全部</option>
                {TAKEDOWN_STATUSES.map(s => (
                  <option key={s} value={s}>
                    {STATUS_LABELS[s]}
                  </option>
                ))}
              </select>
            </label>
          </div>
          <div className="adm-table-wrap">
            <table className="adm-table">
              <thead>
                <tr>
                  <th>编号</th>
                  <th>权利人 / 作品</th>
                  <th>状态</th>
                  <th>时限</th>
                </tr>
              </thead>
              <tbody>
                {list.data && items.length === 0 && <EmptyRow cols={4}>没有符合条件的投诉</EmptyRow>}
                {items.map(t => (
                  <tr key={t.id} className={`clickable ${t.id === selected ? 'sel' : ''} ${t.overdue ? 'flag-red' : ''}`} onClick={() => setParam('id', String(t.id))}>
                    <td className="mono">
                      {t.ticket}
                      <div className="adm-source-meta">{fmtTime(t.created_at)}</div>
                    </td>
                    <td className="wrap" style={{maxWidth: 240}}>
                      {t.rights_owner}
                      <div className="adm-source-meta">{t.work_description.slice(0, 60)}</div>
                    </td>
                    <td>
                      <Pill tone={STATUS_TONES[t.status]}>{STATUS_LABELS[t.status]}</Pill>
                    </td>
                    <td>
                      <SlaBadge t={t} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <MoreButton next={next} busy={moreBusy} onMore={() => void more()} />
        </section>
        {selected ? <Detail id={selected} onChanged={reload} /> : <section className="adm-card muted">从左侧选择一件投诉。</section>}
      </div>
    </>
  );
}
