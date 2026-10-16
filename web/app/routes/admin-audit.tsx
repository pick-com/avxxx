import {useEffect, useState} from 'react';
import {Link, useSearchParams} from 'react-router';
import {AdminHeader, AdminNotice, EmptyRow, MoreButton, Pill, fmtDuration, fmtNum, fmtTime, statusTone} from '../components/AdminUI';
import {
  AUDIT_RULES,
  RULE_LABELS,
  SEVERITY_LABELS,
  dismissAuditIssue,
  getAuditIssues,
  getDashboard,
  runAudit,
  type AuditIssue,
} from '../lib/adminApi';
import {adminErrorText} from '../lib/adminApi';
import {useAdmin, useAdminAction, useAdminLoad} from '../lib/adminContext';

export const meta = () => [{title: 'REEL · 数据质量'}];

// AdminAudit (PRD 11 A5): GET admin/audit/issues by rule / status with cursor
// paging, POST .../dismiss with a note, POST admin/audit/run. R-AGE (age at
// filming < 18 → hold) is the compliance rule: it is listed first, coloured
// red, and its rows are pinned above the rest of a mixed page.

const STATUSES = [
  {v: 'open', t: '待处理'},
  {v: 'fixed', t: '已修复'},
  {v: 'dismissed', t: '已忽略'},
];

type Finding = {name?: string; age?: number; reason?: string; birth_date?: string; basis?: string};

function IssueDetail({issue}: {issue: AuditIssue}) {
  const d = issue.detail ?? {};
  if (issue.rule === 'R-AGE') {
    const performers = (Array.isArray(d.performers) ? d.performers : []) as Finding[];
    return (
      <div>
        <strong>{String(d.title ?? '')}</strong>
        <div className="adm-source-meta">
          {d.shoot_year ? `拍摄年份 ${String(d.shoot_year)}` : '无拍摄年份'} · {d.release_date ? `发布 ${String(d.release_date)}` : '无发布日期'}
        </div>
        <ul style={{margin: '6px 0 0', paddingLeft: 18}}>
          {performers.map((p, i) => (
            <li key={i}>
              {p.name} · 出演时 {p.age} 岁{p.basis === 'release_year' ? '（按发布年估算）' : ''} · 生日 {p.birth_date ?? '—'}
              {p.reason ? <span className="muted"> · {p.reason}</span> : null}
            </li>
          ))}
        </ul>
      </div>
    );
  }
  const title = d.title ?? d.name;
  const missing = Array.isArray(d.missing) ? (d.missing as unknown[]).map(String).join('、') : null;
  const rest = Object.entries(d).filter(([k]) => !['title', 'name', 'missing'].includes(k));
  return (
    <div>
      {title !== undefined && <strong>{String(title)}</strong>}
      {missing && <div>缺：{missing}</div>}
      {rest.length > 0 && (
        <div className="adm-source-meta mono">
          {rest.map(([k, v]) => `${k}: ${typeof v === 'object' ? JSON.stringify(v) : String(v)}`).join(' · ')}
        </div>
      )}
    </div>
  );
}

function entityLink(i: AuditIssue) {
  if (i.entity_type === 'release') return <Link to={`/admin/videos/${i.entity_id}`}>视频 #{i.entity_id}</Link>;
  if (i.entity_type === 'playback_source') return <span>片源 #{i.entity_id}</span>;
  return (
    <span>
      {i.entity_type === 'performer' ? '演员' : i.entity_type} #{i.entity_id}
    </span>
  );
}

export default function AdminAudit() {
  const [params, setParams] = useSearchParams();
  const rule = params.get('rule') ?? '';
  const status = params.get('status') ?? 'open';
  const {hasToken, epoch} = useAdmin();
  const dash = useAdminLoad(() => getDashboard());
  const act = useAdminAction();
  const [items, setItems] = useState<AuditIssue[]>([]);
  const [next, setNext] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [loaded, setLoaded] = useState(false);
  const [dismissing, setDismissing] = useState<number | null>(null);
  const [note, setNote] = useState('');

  const load = async (cursor: string | null) => {
    setBusy(true);
    setError('');
    try {
      const page = await getAuditIssues({rule, status, cursor, limit: 50});
      setItems(prev => (cursor ? [...prev, ...page.items] : page.items));
      setNext(page.next_cursor);
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
  }, [hasToken, epoch, rule, status]);

  const setFilter = (k: string, v: string) => {
    const p = new URLSearchParams(params);
    if (v) p.set(k, v);
    else p.delete(k);
    setParams(p, {replace: true});
  };

  const dismiss = async (id: number) => {
    const ok = await act.attempt(`dismiss-${id}`, () => dismissAuditIssue(id, note.trim()), `已忽略问题 #${id}`);
    if (ok) {
      setItems(prev => prev.filter(i => i.id !== id));
      setDismissing(null);
      setNote('');
      dash.reload();
    }
  };

  const run = () =>
    act.attempt('run', () => runAudit(), v => (v.enqueued ? `已排入审计任务 #${v.job_id}，由 audit worker 执行` : `已有审计任务在排队或运行（#${v.job_id}）`));

  // R-AGE first on a mixed page (the server orders by popularity, then recency).
  const ordered = rule ? items : [...items.filter(i => i.rule === 'R-AGE'), ...items.filter(i => i.rule !== 'R-AGE')];
  const byRule = dash.data?.issues.by_rule ?? {};
  const runs = new Map((dash.data?.nightly_runs ?? []).map(r => [r.job, r]));

  return (
    <>
      <AdminHeader
        eyebrow="ADMIN · AUDIT"
        title="数据质量"
        sub="新视频入库时检查一次；每晚再把全部已入库内容扫一遍，能自动修的直接修，修不了的留在这里。R-AGE 命中的作品会被自动暂停上线。"
        actions={
          <button type="button" className="btn btn-primary btn-sm" disabled={act.pending === 'run'} onClick={() => void run()}>
            立即运行审计
          </button>
        }
      />
      <AdminNotice error={act.error || dash.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />

      <section className="adm-card">
        <h2>规则</h2>
        <div className="adm-table-wrap">
          <table className="adm-table">
            <thead>
              <tr>
                <th>规则</th>
                <th className="num">待处理</th>
                <th>上次运行</th>
                <th className="num">检查</th>
                <th className="num">自动修</th>
                <th>结果</th>
              </tr>
            </thead>
            <tbody>
              {AUDIT_RULES.map(r => {
                const n = byRule[r] ?? 0;
                const last = runs.get(`audit.${r}`) ?? runs.get(r);
                return (
                  <tr
                    key={r}
                    className={`clickable ${rule === r ? 'sel' : ''} ${r === 'R-AGE' && n ? 'flag-red' : ''}`}
                    onClick={() => setFilter('rule', rule === r ? '' : r)}
                  >
                    <td>
                      <span className="mono">{r}</span> · {RULE_LABELS[r]}
                    </td>
                    <td className="num">{n ? <Pill tone={r === 'R-AGE' ? 'red' : 'amber'}>{fmtNum(n)}</Pill> : '0'}</td>
                    <td>{last ? `${fmtTime(last.started_at)} · ${fmtDuration(last.duration_ms)}` : '—'}</td>
                    <td className="num">{last ? fmtNum(last.checked) : '—'}</td>
                    <td className="num">{last ? fmtNum(last.fixed) : '—'}</td>
                    <td>{last ? <Pill tone={statusTone(last.result)}>{last.result}</Pill> : '—'}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </section>

      <section className="adm-card">
        <div className="adm-card-head">
          <h2>
            {rule ? `${rule} · ${RULE_LABELS[rule] ?? ''}` : '全部规则'} · {STATUSES.find(s => s.v === status)?.t}
          </h2>
          <div className="adm-toolbar">
            <select aria-label="规则" value={rule} onChange={e => setFilter('rule', e.target.value)}>
              <option value="">全部规则</option>
              {AUDIT_RULES.map(r => (
                <option key={r} value={r}>
                  {r}
                </option>
              ))}
            </select>
            <div className="adm-chips" role="group" aria-label="状态">
              {STATUSES.map(s => (
                <button key={s.v} type="button" className="adm-chip" aria-pressed={status === s.v} onClick={() => setFilter('status', s.v === 'open' ? '' : s.v)}>
                  {s.t}
                </button>
              ))}
            </div>
          </div>
        </div>
        <AdminNotice error={error} />
        <p className="adm-field-hint">按播放热度排序，先补最多人看的。</p>
        <div className="adm-table-wrap">
          <table className="adm-table">
            <thead>
              <tr>
                <th className="num">#</th>
                <th>规则</th>
                <th>对象</th>
                <th>详情</th>
                <th>严重度</th>
                <th className="num">热度</th>
                <th>最近发现</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {loaded && ordered.length === 0 && <EmptyRow cols={8}>没有符合条件的问题</EmptyRow>}
              {ordered.map(i => (
                <tr key={i.id} className={i.rule === 'R-AGE' ? 'flag-red' : ''}>
                  <td className="num">{i.id}</td>
                  <td>
                    <Pill tone={i.rule === 'R-AGE' ? 'red' : 'plain'}>{i.rule}</Pill>
                  </td>
                  <td>{entityLink(i)}</td>
                  <td className="wrap">
                    <IssueDetail issue={i} />
                    {dismissing === i.id && (
                      <form
                        className="adm-toolbar"
                        style={{marginTop: 8}}
                        onSubmit={e => {
                          e.preventDefault();
                          void dismiss(i.id);
                        }}
                      >
                        <input
                          autoFocus
                          value={note}
                          onChange={e => setNote(e.target.value)}
                          placeholder={i.rule === 'R-AGE' ? '说明为什么不是问题（必填）' : '备注（可选）'}
                          required={i.rule === 'R-AGE'}
                          maxLength={2000}
                          style={{flex: 1, minWidth: 200}}
                        />
                        <button type="submit" className="btn btn-secondary btn-sm" disabled={act.pending === `dismiss-${i.id}`}>
                          确认忽略
                        </button>
                        <button type="button" className="adm-link" onClick={() => setDismissing(null)}>
                          取消
                        </button>
                      </form>
                    )}
                    {i.rule === 'R-AGE' && dismissing === i.id && (
                      <p className="adm-field-hint warn">忽略不会解除暂停：作品要上线仍需在视频「发布」里填写解除理由。</p>
                    )}
                  </td>
                  <td>
                    <Pill tone={i.severity === 'blocking' ? 'red' : i.severity === 'high' ? 'amber' : 'plain'}>{SEVERITY_LABELS[i.severity] ?? i.severity}</Pill>
                  </td>
                  <td className="num">{fmtNum(i.priority)}</td>
                  <td>{fmtTime(i.last_seen_at)}</td>
                  <td>
                    <div className="actions">
                      {i.entity_type === 'release' && (
                        <Link className="adm-link" to={`/admin/videos/${i.entity_id}`}>
                          去修改
                        </Link>
                      )}
                      {i.status === 'open' && dismissing !== i.id && (
                        <button
                          type="button"
                          className="adm-link"
                          onClick={() => {
                            setDismissing(i.id);
                            setNote('');
                          }}
                        >
                          忽略
                        </button>
                      )}
                      {i.status !== 'open' && <span className="muted">{fmtTime(i.resolved_at)}</span>}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <MoreButton next={next} busy={busy} onMore={() => void load(next)} />
      </section>
    </>
  );
}
