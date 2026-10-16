import {useState, type FormEvent} from 'react';
import {ApiError} from '../lib/api';
import {AdminNotice, EmptyRow, Pill, fmtNum, fmtTime} from './AdminUI';
import {
  AUTOMATIC_LICENSES,
  LICENSE_LABELS,
  LICENSE_STATUSES,
  PHASE_LABELS,
  VERIFY_MAX_PAGES,
  getCheckpoints,
  requestJob,
  routeMissing,
  setSourceLicense,
  type GovJob,
  type GovSource,
  type JobPhase,
} from '../lib/adminGovApi';
import {useAdminAction, useAdminLoad} from '../lib/adminContext';

// Crawl governance of one source (FINAL_DATA_DESIGN 2.4, 7, 10.3) for
// AdminPipeline: the license / terms review, phase-aware job requests and the
// discovery checkpoints. Routes: PUT admin/sources/{id}/license,
// POST admin/sources/{id}/jobs {phase, max_pages}, GET admin/sources/{id}/checkpoints.

export const licenseTone = (s: string) =>
  s === 'denied' ? 'red' : s === 'review_required' ? 'amber' : s === 'manual_only' ? 'blue' : AUTOMATIC_LICENSES.has(s) ? 'green' : 'plain';

/** Small status line: license, gate and blocked_until (when a backend returns it). */
export function GateLine({s}: {s: GovSource}) {
  const blocked = s.blocked_until && new Date(s.blocked_until).getTime() > Date.now();
  return (
    <div className="adm-chips">
      <Pill tone={licenseTone(s.license_status)} title={s.license_status}>
        {LICENSE_LABELS[s.license_status] ?? (s.license_status || '—')}
      </Pill>
      {s.automatic_allowed === undefined ? null : s.automatic_allowed ? (
        <Pill tone="green">自动抓取已放行</Pill>
      ) : (
        <Pill tone="amber" title={s.gate_reason}>
          仅人工验证
        </Pill>
      )}
      {s.terms_reviewed_at ? <Pill title={`审核人 ${s.terms_reviewed_by || '—'}`}>条款已审 {fmtTime(s.terms_reviewed_at)}</Pill> : null}
      {s.baseline_requested_at && <Pill tone="blue">全量基线已申请 {fmtTime(s.baseline_requested_at)}</Pill>}
      {blocked && (
        <Pill tone="red" title={s.blocked_reason ?? ''}>
          封锁至 {fmtTime(s.blocked_until)}
        </Pill>
      )}
    </div>
  );
}

function LicenseForm({s, onSaved}: {s: GovSource; onSaved: () => void}) {
  const act = useAdminAction();
  const [status, setStatus] = useState(s.license_status || 'review_required');
  const [terms, setTerms] = useState(s.terms_url ?? '');
  const [reviewed, setReviewed] = useState(!!s.terms_reviewed_at);
  const [note, setNote] = useState(s.license_note ?? '');
  const automatic = AUTOMATIC_LICENSES.has(status);
  const save = (e: FormEvent) => {
    e.preventDefault();
    void act
      .attempt('license', () => setSourceLicense(s.id, {license_status: status, terms_url: terms.trim() || null, reviewed, note: note.trim()}), '已保存授权与条款审核')
      .then(v => v && onSaved());
  };
  // Mirrors catalog.Gate so the admin sees the outcome before saving.
  const outcome =
    status === 'denied'
      ? '禁止：任何抓取（包括验证）都会被拒绝。'
      : !automatic
        ? '只能运行人工「验证」任务（最多 3 页，不写入片库）。'
        : !reviewed
          ? '状态允许自动抓取，但还没有勾选「已审核条款」：自动抓取仍被拦截。'
          : !terms.trim() && status !== 'internal'
            ? '缺少条款链接：自动抓取仍被拦截。'
            : '保存后放行自动抓取（增量按发现间隔排程；全量基线需单独申请）。';
  return (
    <form className="adm-gov-block" onSubmit={save}>
      <h3>授权与条款</h3>
      <p className="adm-field-hint">
        自动抓取（增量、全量基线）只在授权状态为「条款允许 / 已授权 / 官方 API / 自有数据」、已记录条款审核且填写条款链接后放行；在此之前只能人工验证。每次修改都写入修改记录。
      </p>
      <div className="adm-form-grid">
        <label className="adm-field">
          授权状态
          <select value={status} onChange={e => setStatus(e.target.value)}>
            {LICENSE_STATUSES.map(v => (
              <option key={v} value={v}>
                {LICENSE_LABELS[v]}（{v}）
              </option>
            ))}
          </select>
        </label>
        <label className="adm-field" style={{gridColumn: 'span 2'}}>
          条款链接（terms_url）
          <input type="url" value={terms} onChange={e => setTerms(e.target.value)} placeholder="https://…/terms" />
        </label>
      </div>
      <label className="adm-check">
        <input type="checkbox" checked={reviewed} onChange={e => setReviewed(e.target.checked)} /> 已审核条款（记录为本人、当前时间；取消勾选会撤回审核并停止自动抓取）
      </label>
      <label className="adm-field">
        备注
        <textarea rows={2} value={note} onChange={e => setNote(e.target.value)} placeholder="审核依据、联系人、协议编号…" maxLength={2000} />
      </label>
      <p className={`adm-gov-outcome ${automatic && reviewed && (terms.trim() || status === 'internal') ? 'ok' : status === 'denied' ? 'bad' : ''}`}>{outcome}</p>
      <div className="adm-form-actions">
        <button type="submit" className="btn btn-primary btn-sm" disabled={act.pending === 'license'}>
          {act.pending === 'license' ? '保存中…' : '保存审核'}
        </button>
        <span className="muted">
          上次审核：{s.terms_reviewed_at ? `${s.terms_reviewed_by || '—'} · ${fmtTime(s.terms_reviewed_at)}` : '未审核'}
        </span>
      </div>
      <AdminNotice error={act.error} notice={act.notice} />
    </form>
  );
}

type EntityCounts = {parsed?: number; failed?: number; complete?: number; required_pct?: number; normalized_pct?: number; samples?: string[] | null};

/** The dry-run report a verify job stores in jobs.result_counts (catalog.ParseReport.Summary). */
export function VerifyReport({counts}: {counts: Record<string, unknown>}) {
  const entities = Object.entries(counts).filter(([, v]) => v && typeof v === 'object' && !Array.isArray(v)) as [string, EntityCounts][];
  const failures = Array.isArray(counts.failures) ? (counts.failures as string[]) : [];
  return (
    <div className="adm-gov-report">
      <div className="adm-table-wrap">
        <table className="adm-table">
          <thead>
            <tr>
              <th>实体</th>
              <th className="num">解析</th>
              <th className="num">失败</th>
              <th className="num">完整</th>
              <th className="num">必填覆盖</th>
              <th className="num">规范化</th>
              <th>样本</th>
            </tr>
          </thead>
          <tbody>
            {entities.length === 0 && <EmptyRow cols={7}>报告里没有实体统计</EmptyRow>}
            {entities.map(([k, e]) => (
              <tr key={k}>
                <td>{k}</td>
                <td className="num">{fmtNum(e.parsed)}</td>
                <td className="num">{fmtNum(e.failed)}</td>
                <td className="num">{fmtNum(e.complete)}</td>
                <td className="num">{e.required_pct ?? '—'}%</td>
                <td className="num">{e.normalized_pct ?? '—'}%</td>
                <td className="wrap mono">{(e.samples ?? []).slice(0, 5).join(', ') || '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {failures.length > 0 && (
        <details>
          <summary>解析失败 {failures.length} 条</summary>
          <ul className="adm-gov-failures">
            {failures.slice(0, 30).map((f, i) => (
              <li key={i} className="mono">
                {f}
              </li>
            ))}
          </ul>
        </details>
      )}
    </div>
  );
}

function JobRequest({s, jobs, onQueued}: {s: GovSource; jobs: GovJob[]; onQueued: () => void}) {
  const act = useAdminAction();
  const [phase, setPhase] = useState<JobPhase>(s.automatic_allowed === false ? 'verify' : 'incremental');
  const [pages, setPages] = useState(1);
  const automaticBlocked = s.automatic_allowed === false;
  const denied = s.license_status === 'denied';
  const blocked = denied || (phase !== 'verify' && automaticBlocked);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    void act
      .run(`job-${phase}`, () => requestJob(s.id, phase, phase === 'verify' ? pages : undefined), v => `已排入${PHASE_LABELS[phase]}任务 #${v.job_id}`)
      .then(() => onQueued())
      .catch(cause => {
        if (cause instanceof ApiError && cause.status === 409) act.setError(`无法排入：${cause.detail || '同一阶段已有排队或运行中的任务，或授权闸门拒绝'}`);
      });
  };
  const verifyJobs = jobs.filter(j => j.phase === 'verify');
  const lastVerify = verifyJobs[0];
  return (
    <form className="adm-gov-block" onSubmit={submit}>
      <h3>按阶段抓取</h3>
      <div className="adm-seg" role="radiogroup" aria-label="抓取阶段">
        {(['incremental', 'baseline', 'verify'] as JobPhase[]).map(p => (
          <label key={p} className={`adm-seg-item ${phase === p ? 'on' : ''}`}>
            <input type="radio" name={`phase-${s.id}`} value={p} checked={phase === p} onChange={() => setPhase(p)} />
            {PHASE_LABELS[p]}
          </label>
        ))}
      </div>
      <p className="adm-field-hint">
        {phase === 'incremental' && '从列表头读到上次的水位线为止，日常排程也是这一种。'}
        {phase === 'baseline' && '一次性全量回溯，按页断点续跑；只在这里申请时排入，调度器会持续推进直到完成。'}
        {phase === 'verify' && `人工试跑：最多 ${VERIFY_MAX_PAGES} 页发现页，只解析不写入片库，结果是一份试跑报告。未审核条款的来源也可以运行。`}
      </p>
      {phase === 'verify' && (
        <label className="adm-field" style={{maxWidth: 200}}>
          页数上限（1–{VERIFY_MAX_PAGES}）
          <input type="number" min={1} max={VERIFY_MAX_PAGES} value={pages} onChange={e => setPages(Math.min(VERIFY_MAX_PAGES, Math.max(1, Number(e.target.value) || 1)))} />
        </label>
      )}
      {blocked && (
        <div className="adm-notice adm-notice-warn" role="status">
          {denied ? '授权状态为「禁止抓取」：任何任务都会被拒绝。' : `授权闸门：${s.gate_reason || '自动抓取未放行'}。先完成上面的条款审核，或改用「验证」。`}
        </div>
      )}
      <div className="adm-form-actions">
        <button type="submit" className="btn btn-primary btn-sm" disabled={blocked || !s.enabled || act.pending !== null}>
          {act.pending ? '排入中…' : `排入${PHASE_LABELS[phase]}任务`}
        </button>
        {!s.enabled && <span className="muted">来源已停用</span>}
      </div>
      <AdminNotice error={act.error} notice={act.notice} />
      <h4>最近的验证任务</h4>
      {lastVerify ? (
        <div>
          <p className="adm-source-meta">
            #{lastVerify.id} · <Pill tone={lastVerify.status === 'failed' ? 'red' : lastVerify.status === 'succeeded' ? 'green' : 'plain'}>{lastVerify.status}</Pill> ·{' '}
            {fmtTime(lastVerify.created_at)}
            {lastVerify.page_cap ? ` · ${lastVerify.page_cap} 页` : ''}
            {lastVerify.error && <span className="mono"> · {lastVerify.error}</span>}
          </p>
          {lastVerify.result_counts ? (
            <VerifyReport counts={lastVerify.result_counts} />
          ) : (
            <p className="muted">
              {lastVerify.status === 'succeeded'
                ? '试跑报告保存在 jobs.result_counts，但当前后端的任务列表（GET admin/jobs）没有返回它。'
                : '任务完成后在这里显示试跑报告。'}
            </p>
          )}
        </div>
      ) : (
        <p className="muted">还没有验证任务。</p>
      )}
    </form>
  );
}

const TASK_LABELS: Record<string, string> = {discovery: '发现', detail: '详情'};

function Checkpoints({s, jobs}: {s: GovSource; jobs: GovJob[]}) {
  const cps = useAdminLoad(() => getCheckpoints(s.id), [s.id]);
  const failuresByPhase = (phase: string) => jobs.filter(j => j.phase === phase && j.status === 'failed').length;
  const lastByPhase = (phase: string) => jobs.find(j => j.phase === phase);
  return (
    <div className="adm-gov-block">
      <div className="adm-card-head">
        <h3>断点与水位</h3>
        <button type="button" className="adm-link" disabled={cps.busy} onClick={cps.reload}>
          {cps.busy ? '刷新中…' : '刷新'}
        </button>
      </div>
      <div className="adm-table-wrap">
        <table className="adm-table">
          <thead>
            <tr>
              <th>阶段</th>
              <th className="num">最近任务</th>
              <th className="num">失败任务</th>
            </tr>
          </thead>
          <tbody>
            {(['incremental', 'baseline', 'verify'] as const).map(p => {
              const last = lastByPhase(p);
              const f = failuresByPhase(p);
              return (
                <tr key={p} className={f ? 'flag-red' : ''}>
                  <td>{PHASE_LABELS[p]}</td>
                  <td className="num">{last ? `#${last.id} · ${last.status} · ${fmtTime(last.created_at)}` : '—'}</td>
                  <td className="num">{f || '—'}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      {cps.failure && routeMissing(cps.failure) ? (
        <p className="muted">后端未部署断点接口（admin/sources/{'{id}'}/checkpoints）。</p>
      ) : (
        <>
          <AdminNotice error={cps.error} />
          <div className="adm-table-wrap">
            <table className="adm-table">
              <thead>
                <tr>
                  <th>任务 / 分区</th>
                  <th>游标</th>
                  <th>水位线</th>
                  <th className="num">已完成页</th>
                  <th>上次成功</th>
                  <th>最近错误</th>
                  <th>解析器</th>
                </tr>
              </thead>
              <tbody>
                {cps.data && cps.data.length === 0 && <EmptyRow cols={7}>还没有断点（来源还没跑过发现任务）</EmptyRow>}
                {(cps.data ?? []).map(c => (
                  <tr key={`${c.task_type}/${c.partition_key}`} className={c.last_error_code ? 'flag-red' : ''}>
                    <td>
                      {TASK_LABELS[c.task_type] ?? c.task_type} <span className="muted mono">{c.partition_key || '—'}</span>
                    </td>
                    <td className="wrap mono" title="游标按接口返回显示（敏感参数已由后端脱敏）">
                      {c.cursor || '—'}
                    </td>
                    <td>{fmtTime(c.watermark)}</td>
                    <td className="num">{fmtNum(c.completed_pages)}</td>
                    <td>{fmtTime(c.last_success_at)}</td>
                    <td className="mono">
                      {c.last_error_code || '—'}
                      {c.failures ? ` ×${c.failures}` : ''}
                    </td>
                    <td className="mono">{c.parser_version || '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
    </div>
  );
}

/** The governance drawer under a source row. */
export function SourceGovernance({s, jobs, onChanged}: {s: GovSource; jobs: GovJob[]; onChanged: () => void}) {
  return (
    <section className="adm-panel adm-gov" aria-label={`授权与抓取 · ${s.name}`}>
      <div className="adm-card-head">
        <h3>
          {s.name} <span className="muted mono">#{s.id}</span>
        </h3>
        <GateLine s={s} />
      </div>
      {s.gate_reason && <p className="adm-source-meta mono">闸门：{s.gate_reason}</p>}
      {s.adapter === 'rss' || !s.adapter ? (
        <p className="muted">RSS 订阅源面向自动读取发布，不受目录授权闸门约束。</p>
      ) : null}
      <div className="adm-gov-grid">
        <LicenseForm key={s.id} s={s} onSaved={onChanged} />
        <JobRequest s={s} jobs={jobs} onQueued={onChanged} />
      </div>
      <Checkpoints s={s} jobs={jobs} />
    </section>
  );
}

