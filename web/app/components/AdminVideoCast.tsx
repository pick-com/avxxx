import {useEffect, useState, type FormEvent} from 'react';
import {Link} from 'react-router';
import {AdminNotice, Pill} from './AdminUI';
import {listPerformerPage, type PerformerListItem} from '../lib/api';
import {adminErrorText, removeReleasePerformer, setReleasePerformer, type AdminReleasePerformer} from '../lib/adminApi';
import {useAdminAction} from '../lib/adminContext';

// 演员 tab (PR #61 PUT/DELETE admin/releases/{id}/performers/{performer_id}):
// credits as lead (主演) or guest (客串) with billing order and credited name,
// age at filming (shoot year − birth year, 「约」 when only the release year is
// known, PRD 6.4) and the R-AGE flag. Adding a credit searches the public
// performer list.

function ageText(p: AdminReleasePerformer) {
  if (!p.age_at_filming) return p.birth_date ? '—（缺拍摄 / 发布年份）' : '—（生日未知）';
  const a = p.age_at_filming;
  return `${a.approx ? '约 ' : ''}${a.age} 岁${a.basis === 'release_year' ? '（按发布年）' : ''}`;
}

function CreditRow({p, busy, onSave, onRemove}: {
  p: AdminReleasePerformer;
  busy: boolean;
  onSave: (body: {role: 'lead' | 'guest'; billing_order: number | null; credited_name: string | null; reason: string}) => void;
  onRemove: (reason: string) => void;
}) {
  const [role, setRole] = useState<'lead' | 'guest'>(p.role);
  const [order, setOrder] = useState(p.billing_order?.toString() ?? '');
  const [credited, setCredited] = useState(p.credited_name ?? '');
  const [reason, setReason] = useState('');
  const dirty = role !== p.role || order !== (p.billing_order?.toString() ?? '') || credited !== (p.credited_name ?? '');
  const flagged = !!p.age_flag;
  return (
    <tr className={flagged ? 'flag-red' : ''}>
      <td>
        <Link to={`/performers/${p.slug}`} target="_blank">
          {p.name}
        </Link>
        {p.name_ja && <div className="adm-source-meta">{p.name_ja}</div>}
        <div className="adm-source-meta mono">#{p.id}</div>
      </td>
      <td>
        <select className="adm-input" value={role} onChange={e => setRole(e.target.value as 'lead' | 'guest')} aria-label="角色">
          <option value="lead">主演</option>
          <option value="guest">客串</option>
        </select>
      </td>
      <td>
        <input className="adm-input" style={{width: 70}} type="number" min={0} max={1000} value={order} onChange={e => setOrder(e.target.value)} aria-label="署名顺序" />
      </td>
      <td>
        <input className="adm-input" value={credited} maxLength={200} onChange={e => setCredited(e.target.value)} placeholder="同演员名" aria-label="署名" />
      </td>
      <td>
        {ageText(p)}
        <div className="adm-source-meta">生日 {p.birth_date ?? '未知'}</div>
        {flagged && (
          <div style={{marginTop: 4}}>
            <Pill tone="red">R-AGE · {p.age_flag}</Pill>
          </div>
        )}
      </td>
      <td>
        <input className="adm-input" value={reason} maxLength={2000} onChange={e => setReason(e.target.value)} placeholder="理由（可选）" aria-label="理由" />
        <div className="actions" style={{marginTop: 6}}>
          <button
            type="button"
            className="adm-link"
            disabled={!dirty || busy}
            onClick={() => onSave({role, billing_order: order.trim() === '' ? null : Number(order), credited_name: credited.trim() || null, reason})}
          >
            保存
          </button>
          <button type="button" className="adm-link danger" disabled={busy} onClick={() => onRemove(reason)}>
            移除
          </button>
        </div>
      </td>
    </tr>
  );
}

export function AdminVideoCast({releaseId, performers, shootYear, releaseDate, onChanged}: {
  releaseId: number;
  performers: AdminReleasePerformer[];
  shootYear: number | null;
  releaseDate: string | null;
  onChanged: () => void;
}) {
  const act = useAdminAction();
  const [q, setQ] = useState('');
  const [hits, setHits] = useState<PerformerListItem[]>([]);
  const [searchErr, setSearchErr] = useState('');
  const [role, setRole] = useState<'lead' | 'guest'>('lead');

  useEffect(() => {
    const term = q.trim();
    if (term.length < 1) {
      setHits([]);
      return;
    }
    const ctrl = new AbortController();
    const t = window.setTimeout(() => {
      listPerformerPage('name', 8, null, term, ctrl.signal)
        .then(p => setHits(p.items))
        .catch(cause => {
          if (!ctrl.signal.aborted) setSearchErr(adminErrorText(cause));
        });
    }, 250);
    return () => {
      ctrl.abort();
      window.clearTimeout(t);
    };
  }, [q]);

  const save = async (p: AdminReleasePerformer, body: Parameters<typeof setReleasePerformer>[2]) => {
    const ok = await act.attempt(`p-${p.id}`, () => setReleasePerformer(releaseId, p.id, body), `已保存 ${p.name}`);
    if (ok) onChanged();
  };
  const remove = async (p: AdminReleasePerformer, reason: string) => {
    const ok = await act.attempt(`p-${p.id}`, () => removeReleasePerformer(releaseId, p.id, reason), `已移除 ${p.name}`);
    if (ok) onChanged();
  };
  const add = async (hit: PerformerListItem) => {
    const ok = await act.attempt(`add-${hit.id}`, () => setReleasePerformer(releaseId, hit.id, {role}), `已添加 ${hit.canonical_name}`);
    if (ok) {
      setQ('');
      setHits([]);
      onChanged();
    }
  };
  const submitSearch = (e: FormEvent) => {
    e.preventDefault();
    if (hits[0]) void add(hits[0]);
  };

  const flagged = performers.filter(p => p.age_flag);
  const sorted = [...performers].sort((a, b) => Number(b.is_primary) - Number(a.is_primary) || (a.billing_order ?? 999) - (b.billing_order ?? 999));

  return (
    <div className="adm-card">
      <div className="adm-card-head">
        <div>
          <h2>演员（{performers.length}）</h2>
          <p className="adm-field-hint">
            出演时年龄 = 拍摄年份（{shootYear ?? '未填'}）− 演员生日年份；没有拍摄年份时按发布日期（{releaseDate ?? '未填'}）估算，前台显示「约」。生日未知时前台不显示。
          </p>
        </div>
      </div>
      {flagged.length > 0 && (
        <div className="adm-agegate" role="alert">
          <strong>R-AGE：{flagged.map(p => p.name).join('、')} 的出演时年龄可能未满 18 岁</strong>
          <span>请核对拍摄年份（基本信息）、发布日期或演员生日。规则通过前作品不能上线。</span>
        </div>
      )}
      <AdminNotice error={act.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />
      <div className="adm-table-wrap">
        <table className="adm-table">
          <thead>
            <tr>
              <th>演员</th>
              <th>类型</th>
              <th>顺序</th>
              <th>署名</th>
              <th>出演时年龄</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {sorted.length === 0 && (
              <tr>
                <td colSpan={6} className="adm-empty">
                  还没有演员
                </td>
              </tr>
            )}
            {sorted.map(p => (
              <CreditRow
                key={`${p.id}-${p.role}-${p.billing_order}-${p.credited_name}`}
                p={p}
                busy={act.pending === `p-${p.id}`}
                onSave={body => void save(p, body)}
                onRemove={reason => void remove(p, reason)}
              />
            ))}
          </tbody>
        </table>
      </div>
      <form className="adm-toolbar" onSubmit={submitSearch}>
        <label className="adm-field" style={{flex: 1, minWidth: 240}}>
          添加演员（按名字搜索）
          <input value={q} onChange={e => (setQ(e.target.value), setSearchErr(''))} placeholder="演员名" />
        </label>
        <label className="adm-field">
          类型
          <select value={role} onChange={e => setRole(e.target.value as 'lead' | 'guest')}>
            <option value="lead">主演</option>
            <option value="guest">客串</option>
          </select>
        </label>
      </form>
      {searchErr && <AdminNotice error={searchErr} />}
      {hits.length > 0 && (
        <div className="adm-chips">
          {hits.map(h => {
            const already = performers.some(p => p.id === h.id);
            return (
              <button key={h.id} type="button" className="adm-chip" disabled={already || act.pending === `add-${h.id}`} onClick={() => void add(h)}>
                {h.canonical_name}
                {h.birth_date ? <span className="muted">{h.birth_date.slice(0, 4)}</span> : null}
                {already ? <span className="muted">已在列表</span> : <span>+ 添加</span>}
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}
