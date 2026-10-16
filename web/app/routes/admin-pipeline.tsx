import {useMemo, useState, type FormEvent} from 'react';
import {ApiError} from '../lib/api';
import {AdminHeader, AdminNotice, EmptyRow, Pill, fmtNum, fmtTime, statusTone} from '../components/AdminUI';
import {
  addIngestSource,
  enqueueIngestJob,
  getCrawlPolicies,
  getIngestJobs,
  getIngestSources,
  putCrawlPolicy,
  type CrawlPolicy,
  type CrawlPolicyInput,
  type IngestJob,
  type IngestSource,
} from '../lib/adminApi';
import {useAdminAction, useAdminLoad} from '../lib/adminContext';

export const meta = () => [{title: 'REEL · 入库流水线'}];

// AdminPipeline (PRD 11 A2), scoped to what the backend exposes today:
// GET admin/sources, admin/crawl-policies, admin/jobs (latest 100);
// POST admin/sources, admin/sources/{id}/jobs; PUT admin/sources/{id}/crawl-policy.
// Not shown: blocked_until (no endpoint returns it) and the per-stage flow
// (发现 → 上线) counts, which have no backend yet.

const EMPTY_POLICY: CrawlPolicyInput = {
  discovery_interval: '1 day',
  detail_refresh_interval: '7 days',
  min_request_interval: '5 seconds',
  daily_page_budget: 1000,
  daily_byte_budget: 0,
};

function PolicyForm({source, policy, busy, onSave, onCancel}: {
  source: IngestSource;
  policy: CrawlPolicy | undefined;
  busy: boolean;
  onSave: (p: CrawlPolicyInput) => void;
  onCancel: () => void;
}) {
  const [p, setP] = useState<CrawlPolicyInput>(
    policy
      ? {
          discovery_interval: policy.discovery_interval,
          detail_refresh_interval: policy.detail_refresh_interval,
          min_request_interval: policy.min_request_interval,
          daily_page_budget: policy.daily_page_budget,
          daily_byte_budget: policy.daily_byte_budget,
        }
      : EMPTY_POLICY,
  );
  const set = <K extends keyof CrawlPolicyInput>(k: K, v: CrawlPolicyInput[K]) => setP(prev => ({...prev, [k]: v}));
  const submit = (e: FormEvent) => {
    e.preventDefault();
    onSave(p);
  };
  return (
    <form className="adm-panel" onSubmit={submit}>
      <h3>抓取策略 · {source.name}</h3>
      <p className="adm-field-hint">间隔用 PostgreSQL interval 写法，例如「6 hours」「1 day」「00:00:05」。预算 0 表示不限。</p>
      <div className="adm-form-grid">
        <label className="adm-field">
          发现间隔
          <input value={p.discovery_interval} onChange={e => set('discovery_interval', e.target.value)} placeholder="1 day" />
        </label>
        <label className="adm-field">
          详情刷新间隔
          <input value={p.detail_refresh_interval} onChange={e => set('detail_refresh_interval', e.target.value)} placeholder="7 days" />
        </label>
        <label className="adm-field">
          最小请求间隔（必填）
          <input required value={p.min_request_interval} onChange={e => set('min_request_interval', e.target.value)} placeholder="5 seconds" />
        </label>
        <label className="adm-field">
          每日页数预算
          <input type="number" min={0} value={p.daily_page_budget} onChange={e => set('daily_page_budget', Number(e.target.value) || 0)} />
        </label>
        <label className="adm-field">
          每日字节预算
          <input type="number" min={0} value={p.daily_byte_budget} onChange={e => set('daily_byte_budget', Number(e.target.value) || 0)} />
        </label>
      </div>
      <div className="adm-form-actions">
        <button type="submit" className="btn btn-primary btn-sm" disabled={busy}>
          保存策略
        </button>
        <button type="button" className="btn btn-secondary btn-sm" onClick={onCancel}>
          取消
        </button>
      </div>
    </form>
  );
}

export default function AdminPipeline() {
  const data = useAdminLoad(async () => {
    const [sources, policies, jobs] = await Promise.all([getIngestSources(), getCrawlPolicies(), getIngestJobs()]);
    return {sources, policies, jobs};
  });
  const act = useAdminAction();
  const [editing, setEditing] = useState<number | null>(null);
  const [selected, setSelected] = useState<number | null>(null);
  const [name, setName] = useState('');
  const [url, setUrl] = useState('');

  const jobsBySource = useMemo(() => {
    const m = new Map<number, IngestJob[]>();
    for (const j of data.data?.jobs ?? []) {
      const list = m.get(j.source_id) ?? [];
      list.push(j);
      m.set(j.source_id, list);
    }
    return m;
  }, [data.data]);
  const policyBySource = useMemo(() => new Map((data.data?.policies ?? []).map(p => [p.source_id, p])), [data.data]);

  const enqueue = (s: IngestSource) =>
    act
      .run(`enqueue-${s.id}`, () => enqueueIngestJob(s.id), `已为「${s.name}」排入抓取任务`)
      .then(() => data.reload())
      .catch(cause => {
        // store.Enqueue: 409 when the source is disabled or already has a pending / running job.
        if (cause instanceof ApiError && cause.status === 409) act.setError(`「${s.name}」已有排队或运行中的任务，或来源已停用`);
      });
  const savePolicy = (s: IngestSource, p: CrawlPolicyInput) =>
    act.attempt(`policy-${s.id}`, () => putCrawlPolicy(s.id, p), `已保存「${s.name}」的抓取策略`).then(v => {
      if (v) {
        setEditing(null);
        data.reload();
      }
    });
  const addSource = (e: FormEvent) => {
    e.preventDefault();
    void act.attempt('add', () => addIngestSource(name.trim(), url.trim()), '已添加来源').then(v => {
      if (v) {
        setName('');
        setUrl('');
        data.reload();
      }
    });
  };

  const jobs = data.data?.jobs ?? [];
  const failed = jobs.filter(j => j.status === 'failed');
  const shownJobs = selected ? jobsBySource.get(selected) ?? [] : jobs;
  const sourceName = (id: number) => (id ? data.data?.sources.find(s => s.id === id)?.name ?? `#${id}` : '非抓取任务（如审计）');

  return (
    <>
      <AdminHeader
        eyebrow="ADMIN · PIPELINE"
        title="入库流水线"
        sub="来源、抓取策略与最近 100 个抓取任务。失败任务在这里重试；抓取被封锁的时间（blocked_until）后端尚未提供接口。"
        actions={
          <button type="button" className="btn btn-secondary btn-sm" disabled={data.busy} onClick={data.reload}>
            {data.busy ? '刷新中…' : '刷新'}
          </button>
        }
      />
      <AdminNotice error={data.error || act.error} notice={act.notice} onClose={() => (act.setError(''), act.setNotice(''))} />

      <section className="adm-kpis">
        <div className="adm-kpi">
          <span className="adm-kpi-label">来源</span>
          <span className="adm-kpi-value">{fmtNum(data.data?.sources.length)}</span>
          <span className="adm-kpi-sub">启用 {fmtNum(data.data?.sources.filter(s => s.enabled).length)}</span>
        </div>
        <div className="adm-kpi">
          <span className="adm-kpi-label">排队 / 运行中</span>
          <span className="adm-kpi-value">
            {fmtNum(jobs.filter(j => j.status === 'pending').length)} / {fmtNum(jobs.filter(j => j.status === 'running').length)}
          </span>
          <span className="adm-kpi-sub">最近 100 个任务</span>
        </div>
        <div className="adm-kpi">
          <span className="adm-kpi-label">失败</span>
          <span className={`adm-kpi-value ${failed.length ? 'red' : ''}`}>{fmtNum(failed.length)}</span>
          <span className="adm-kpi-sub">最近 100 个任务</span>
        </div>
      </section>

      <section className="adm-card">
        <div className="adm-card-head">
          <h2>来源与抓取策略</h2>
        </div>
        <div className="adm-table-wrap">
          <table className="adm-table">
            <thead>
              <tr>
                <th>来源</th>
                <th>类型 / 授权</th>
                <th>抓取策略</th>
                <th>最近任务</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {data.data && data.data.sources.length === 0 && <EmptyRow cols={5}>还没有来源</EmptyRow>}
              {data.data?.sources.map(s => {
                const p = policyBySource.get(s.id);
                const sj = jobsBySource.get(s.id) ?? [];
                const last = sj[0];
                const fails = sj.filter(j => j.status === 'failed').length;
                return (
                  <tr key={s.id} className={`${selected === s.id ? 'sel' : ''} ${fails ? 'flag-red' : ''}`}>
                    <td className="wrap">
                      <strong>{s.name}</strong> <span className="muted mono">#{s.id}</span>
                      <div className="adm-source-meta">{s.url}</div>
                      <div className="adm-chips" style={{marginTop: 4}}>
                        <Pill tone={s.enabled ? 'green' : 'red'}>{s.enabled ? '启用' : '停用'}</Pill>
                        <Pill>{s.adapter || '—'}</Pill>
                        <Pill>v{s.version}</Pill>
                      </div>
                    </td>
                    <td>
                      {s.source_kind || '—'}
                      <div className="adm-source-meta">
                        授权 {s.license_status || '—'} · 出口 {s.egress_policy || '—'}
                      </div>
                    </td>
                    <td>
                      {p ? (
                        <div className="adm-source-meta">
                          发现 {p.discovery_interval || '—'} · 刷新 {p.detail_refresh_interval || '—'}
                          <br />
                          最小间隔 {p.min_request_interval} · 每日 {p.daily_page_budget || '∞'} 页 / {p.daily_byte_budget || '∞'} 字节
                          <br />
                          基线 {p.baseline_completed_at ? fmtTime(p.baseline_completed_at) : '未完成'}
                        </div>
                      ) : (
                        <span className="muted">未设置</span>
                      )}
                    </td>
                    <td>
                      {last ? (
                        <>
                          <Pill tone={statusTone(last.status === 'done' ? 'ok' : last.status)}>{last.status}</Pill>{' '}
                          <span className="adm-source-meta">{fmtTime(last.created_at)}</span>
                          {fails > 0 && <div className="adm-source-meta">失败 {fails} 次</div>}
                        </>
                      ) : (
                        <span className="muted">无</span>
                      )}
                    </td>
                    <td>
                      <div className="actions">
                        <button
                          type="button"
                          className="adm-link"
                          disabled={!s.enabled || act.pending === `enqueue-${s.id}`}
                          title={s.enabled ? '' : '来源已停用'}
                          onClick={() => void enqueue(s)}
                        >
                          立即抓取
                        </button>
                        <button type="button" className="adm-link" onClick={() => setEditing(editing === s.id ? null : s.id)}>
                          编辑策略
                        </button>
                        <button type="button" className="adm-link" onClick={() => setSelected(selected === s.id ? null : s.id)}>
                          {selected === s.id ? '看全部任务' : '只看任务'}
                        </button>
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
        {editing !== null && data.data?.sources.find(s => s.id === editing) && (
          <PolicyForm
            key={editing}
            source={data.data.sources.find(s => s.id === editing)!}
            policy={policyBySource.get(editing)}
            busy={act.pending === `policy-${editing}`}
            onSave={p => void savePolicy(data.data!.sources.find(s => s.id === editing)!, p)}
            onCancel={() => setEditing(null)}
          />
        )}
        <form className="adm-toolbar" onSubmit={addSource}>
          <label className="adm-field">
            新来源名称
            <input required value={name} onChange={e => setName(e.target.value)} placeholder="例如 Demo Feed" />
          </label>
          <label className="adm-field" style={{flex: 1, minWidth: 280}}>
            Feed URL（http/https）
            <input required type="url" value={url} onChange={e => setUrl(e.target.value)} placeholder="https://…" />
          </label>
          <button type="submit" className="btn btn-secondary btn-sm" disabled={act.pending === 'add'}>
            添加来源
          </button>
        </form>
      </section>

      <section className="adm-card">
        <div className="adm-card-head">
          <h2>{selected ? `任务 · ${sourceName(selected)}` : '最近任务'}</h2>
          {selected && (
            <button type="button" className="adm-link" onClick={() => setSelected(null)}>
              看全部
            </button>
          )}
        </div>
        <div className="adm-table-wrap">
          <table className="adm-table">
            <thead>
              <tr>
                <th className="num">ID</th>
                <th>来源</th>
                <th>状态</th>
                <th className="num">尝试</th>
                <th>创建</th>
                <th>租约至</th>
                <th>错误</th>
              </tr>
            </thead>
            <tbody>
              {data.data && shownJobs.length === 0 && <EmptyRow cols={7}>没有任务</EmptyRow>}
              {shownJobs.map(j => (
                <tr key={j.id} className={j.status === 'failed' ? 'flag-red' : ''}>
                  <td className="num">{j.id}</td>
                  <td>{sourceName(j.source_id)}</td>
                  <td>
                    <Pill tone={statusTone(j.status === 'done' ? 'ok' : j.status)}>{j.status}</Pill>
                  </td>
                  <td className="num">{j.attempt}</td>
                  <td>{fmtTime(j.created_at)}</td>
                  <td>{fmtTime(j.lease_until)}</td>
                  <td className="wrap mono">{j.error || '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
    </>
  );
}
