import {useMemo, useState} from 'react';
import {Link} from 'react-router';
import {AdminHeader, AdminNotice, EmptyRow, NotDeployed, Pill, fmtDuration, fmtNum, fmtTime, type Tone} from '../components/AdminUI';
import {isMissingEndpoint} from '../lib/api';
import {useAdmin, useAdminAction, useAdminLoad} from '../lib/adminContext';
import {getJobQueue, getJobRuns, retryJob, runJobNow, type QueueJob, type RunPoint, type RunResult, type RunSeries} from '../lib/adminJobsApi';

export const meta = () => [{title: 'REEL · 夜间任务'}];

// 夜间任务 (AdminNav 数据治理): the shared jobs queue (crawl and sourceless
// jobs) with leases and attempts, the nightly_runs history per job with a
// duration sparkline, retrying a failed job and running a sourceless kind
// now. Reads need ingest:read, the buttons ingest:write (the API checks).

const STATUS: Record<QueueJob['status'], {label: string; tone: Tone}> = {
  pending: {label: '待运行', tone: 'blue'},
  running: {label: '运行中', tone: 'green'},
  failed: {label: '失败', tone: 'red'},
  succeeded: {label: '成功', tone: 'plain'},
};
const RESULT: Record<RunResult, {label: string; tone: Tone; color: string}> = {
  ok: {label: '正常', tone: 'green', color: 'var(--adm-green-fg)'},
  issues: {label: '有问题', tone: 'amber', color: 'var(--adm-amber-fg)'},
  failed: {label: '失败', tone: 'red', color: 'var(--adm-red-fg)'},
  not_applicable: {label: '不适用', tone: 'plain', color: 'var(--faint)'},
};
const FILTERS = [
  {id: '', label: '全部'},
  {id: 'pending', label: '待运行'},
  {id: 'running', label: '运行中'},
  {id: 'failed', label: '失败（7 天）'},
] as const;

const leaseExpired = (j: QueueJob) => j.status === 'running' && !!j.lease_until && new Date(j.lease_until) < new Date();

/** Duration over time (oldest left), one dot per run coloured by result. */
function Sparkline({runs}: {runs: RunPoint[]}) {
  const pts = [...runs].reverse();
  const w = 160;
  const h = 28;
  if (pts.length === 0) return <span className="muted">—</span>;
  const max = Math.max(...pts.map(p => p.duration_ms), 1);
  const x = (i: number) => (pts.length === 1 ? w / 2 : 3 + (i * (w - 6)) / (pts.length - 1));
  const y = (ms: number) => h - 3 - (ms / max) * (h - 6);
  return (
    <svg width={w} height={h} viewBox={`0 0 ${w} ${h}`} role="img" aria-label={`最近 ${pts.length} 次运行耗时，最长 ${fmtDuration(max)}`}>
      <polyline fill="none" stroke="var(--muted)" strokeWidth={1} points={pts.map((p, i) => `${x(i)},${y(p.duration_ms)}`).join(' ')} />
      {pts.map((p, i) => (
        <circle key={i} cx={x(i)} cy={y(p.duration_ms)} r={2.2} fill={RESULT[p.result]?.color ?? 'var(--faint)'}>
          <title>{`${fmtTime(p.started_at)} · ${fmtDuration(p.duration_ms)} · ${RESULT[p.result]?.label ?? p.result}`}</title>
        </circle>
      ))}
    </svg>
  );
}

function History({series}: {series: RunSeries[]}) {
  return (
    <div className="adm-table-wrap">
      <table className="adm-table">
        <thead>
          <tr>
            <th>任务</th>
            <th>最近一次</th>
            <th>耗时（最近 / 平均）</th>
            <th>成功率</th>
            <th>检查 / 修复 / 问题</th>
            <th>耗时趋势</th>
          </tr>
        </thead>
        <tbody>
          {series.length === 0 && <EmptyRow cols={6}>这段时间没有运行记录</EmptyRow>}
          {series.map(s => {
            const last = s.runs[0];
            const avg = Math.round(s.runs.reduce((a, r) => a + r.duration_ms, 0) / s.runs.length);
            const good = s.runs.filter(r => r.result === 'ok' || r.result === 'issues' || r.result === 'not_applicable').length;
            return (
              <tr key={s.job}>
                <td className="mono">{s.job}</td>
                <td>
                  <Pill tone={RESULT[last.result]?.tone ?? 'plain'}>{RESULT[last.result]?.label ?? last.result}</Pill>
                  <div className="adm-source-meta">{fmtTime(last.started_at)}</div>
                </td>
                <td>
                  {fmtDuration(last.duration_ms)} / {fmtDuration(avg)}
                </td>
                <td>
                  {Math.round((100 * good) / s.runs.length)}% <span className="muted">（{s.runs.length} 次）</span>
                </td>
                <td>
                  {fmtNum(last.checked)} / {fmtNum(last.fixed)} / {fmtNum(last.issues)}
                </td>
                <td>
                  <Sparkline runs={s.runs} />
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

export default function AdminJobsPage() {
  const {can} = useAdmin();
  const writable = can('ingest:write');
  const queue = useAdminLoad(signal => getJobQueue(signal));
  const [days, setDays] = useState(30);
  const runs = useAdminLoad(signal => getJobRuns({days}, signal), [days]);
  const act = useAdminAction();
  const [filter, setFilter] = useState<(typeof FILTERS)[number]['id']>('');

  const labels = useMemo(() => Object.fromEntries((queue.data?.runnable ?? []).map(k => [k.kind, k.label])), [queue.data]);
  const kindLabel = (k: string) => (k === 'crawl' ? '抓取' : (labels[k] ?? k));
  const counts = useMemo(() => Object.fromEntries((queue.data?.kinds ?? []).map(k => [k.kind, k])), [queue.data]);
  const items = (queue.data?.items ?? []).filter(j => !filter || j.status === filter);
  const crawl = counts.crawl;

  const run = async (kind: string) => {
    const r = await act.attempt(`run-${kind}`, () => runJobNow(kind), v => (v.enqueued ? `已加入队列：${kindLabel(kind)}（#${v.job_id}）` : `${kindLabel(kind)} 已有任务在排队或运行（#${v.job_id}）`));
    if (r) queue.reload();
  };
  const retry = async (j: QueueJob) => {
    if (await act.attempt(`retry-${j.id}`, () => retryJob(j.id), `#${j.id} 已重新排队`)) queue.reload();
  };

  if (queue.failure && isMissingEndpoint(queue.failure)) return <NotDeployed what="任务队列接口（admin/jobs/queue）" />;
  return (
    <>
      <AdminHeader
        eyebrow="ADMIN · JOBS"
        title="夜间任务"
        sub="共享任务队列（抓取与后台任务）的租约和重试次数、每个任务的运行历史；失败任务可重试，后台任务可立即运行。抓取任务在入库流水线按来源发起。"
        actions={
          <button type="button" className="btn btn-secondary btn-sm" onClick={() => (queue.reload(), runs.reload())}>
            刷新
          </button>
        }
      />
      <AdminNotice error={queue.error || runs.error || act.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />

      <section className="adm-card">
        <div className="adm-card-head">
          <h2>任务类型</h2>
          {crawl && (
            <span className="muted">
              抓取：待运行 {crawl.pending} · 运行中 {crawl.running} · 7 天失败 {crawl.failed_7d}（<Link to="/admin/pipeline">入库流水线</Link>）
            </span>
          )}
        </div>
        <div className="adm-table-wrap">
          <table className="adm-table">
            <thead>
              <tr>
                <th>类型</th>
                <th>待运行</th>
                <th>运行中</th>
                <th>7 天成功 / 失败</th>
                <th>最近成功</th>
                {writable && <th>操作</th>}
              </tr>
            </thead>
            <tbody>
              {(queue.data?.runnable ?? []).map(k => {
                const c = counts[k.kind];
                const active = !!c && c.pending + c.running > 0;
                return (
                  <tr key={k.kind}>
                    <td>
                      <strong>{k.label}</strong>
                      <div className="adm-source-meta mono">{k.kind}</div>
                    </td>
                    <td>{c?.pending ?? 0}</td>
                    <td>{c?.running ?? 0}</td>
                    <td>
                      {c?.succeeded_7d ?? 0} / {c?.failed_7d ? <Pill tone="red">{c.failed_7d}</Pill> : 0}
                    </td>
                    <td>{fmtTime(c?.last_succeeded_at) || '—'}</td>
                    {writable && (
                      <td>
                        <button type="button" className="btn btn-secondary btn-sm" disabled={active || act.pending === `run-${k.kind}`} title={active ? '已有任务在排队或运行' : ''} onClick={() => void run(k.kind)}>
                          立即运行
                        </button>
                      </td>
                    )}
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </section>

      <section className="adm-card">
        <div className="adm-card-head">
          <h2>队列</h2>
          <div className="adm-seg" role="tablist" aria-label="状态">
            {FILTERS.map(f => (
              <button key={f.id} type="button" role="tab" aria-selected={filter === f.id} className={`adm-seg-item ${filter === f.id ? 'on' : ''}`} onClick={() => setFilter(f.id)}>
                {f.label}
              </button>
            ))}
          </div>
        </div>
        <div className="adm-table-wrap">
          <table className="adm-table">
            <thead>
              <tr>
                <th>任务</th>
                <th>状态</th>
                <th>尝试</th>
                <th>租约 / 心跳</th>
                <th>时间</th>
                <th>错误</th>
                {writable && <th>操作</th>}
              </tr>
            </thead>
            <tbody>
              {queue.data && items.length === 0 && <EmptyRow cols={7}>没有任务</EmptyRow>}
              {items.map(j => (
                <tr key={j.id}>
                  <td>
                    <strong>{kindLabel(j.kind)}</strong> <span className="muted">#{j.id}</span>
                    {j.source_id && (
                      <div className="adm-source-meta">
                        {j.source_name || `来源 #${j.source_id}`} · {j.phase}
                      </div>
                    )}
                  </td>
                  <td>
                    <Pill tone={STATUS[j.status]?.tone ?? 'plain'}>{STATUS[j.status]?.label ?? j.status}</Pill>
                    {leaseExpired(j) && (
                      <div className="adm-source-meta">
                        <Pill tone="amber">租约已过期，等待回收</Pill>
                      </div>
                    )}
                  </td>
                  <td>
                    {j.attempt} / {j.max_attempts}
                  </td>
                  <td>
                    {j.lease_until ? fmtTime(j.lease_until) : '—'}
                    {j.heartbeat_at && <div className="adm-source-meta">心跳 {fmtTime(j.heartbeat_at)}</div>}
                    {j.worker_id && <div className="adm-source-meta mono">{j.worker_id}</div>}
                  </td>
                  <td>
                    <div className="adm-source-meta">创建 {fmtTime(j.created_at)}</div>
                    {j.status === 'pending' && <div className="adm-source-meta">可运行 {fmtTime(j.available_at)}</div>}
                    {j.started_at && <div className="adm-source-meta">开始 {fmtTime(j.started_at)}</div>}
                    {j.finished_at && <div className="adm-source-meta">结束 {fmtTime(j.finished_at)}</div>}
                  </td>
                  <td className="wrap">
                    {j.error_code && <Pill tone="red">{j.error_code}</Pill>} <span className="adm-source-meta">{j.error}</span>
                  </td>
                  {writable && (
                    <td>
                      {j.status === 'failed' && (
                        <button type="button" className="btn btn-secondary btn-sm" disabled={act.pending === `retry-${j.id}`} onClick={() => void retry(j)}>
                          重试
                        </button>
                      )}
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>

      <section className="adm-card">
        <div className="adm-card-head">
          <h2>运行历史（nightly_runs）</h2>
          <div className="adm-seg" role="tablist" aria-label="时间范围">
            {[7, 30, 90].map(d => (
              <button key={d} type="button" role="tab" aria-selected={days === d} className={`adm-seg-item ${days === d ? 'on' : ''}`} onClick={() => setDays(d)}>
                {d} 天
              </button>
            ))}
          </div>
        </div>
        {runs.data && <History series={runs.data} />}
      </section>
    </>
  );
}
