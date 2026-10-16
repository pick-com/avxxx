import {useState} from 'react';
import {Link} from 'react-router';
import {AdminHeader, AdminNotice, BandPill, EmptyRow, MoreButton, Pill, fmtDuration, fmtNum, fmtTime, statusTone} from '../components/AdminUI';
import {
  AUDIT_RULES,
  BANDS,
  BAND_LABELS,
  RULE_LABELS,
  SEVERITY_LABELS,
  STATUS_LABELS,
  getDashboard,
  getModerationOverview,
  getSourceHealth,
  type SourceHealthItem,
} from '../lib/adminApi';
import {adminErrorText} from '../lib/adminApi';
import {useAdminLoad} from '../lib/adminContext';
import {SEOReportPanel} from '../components/AdminSEOReport';
import {AdminSiteKPIs} from '../components/AdminSiteKPIs';

export const meta = () => [{title: 'REEL · 后台总览'}];

// AdminDashboard (PRD 11 A1): GET admin/dashboard + the moderation counts.
// Every number links into the work queue that resolves it. 播放与日活
// (plays / DAU / watch time / completion) come from GET admin/site-metrics
// (AdminSiteKPIs).

const BAND_TONE: Record<string, string> = {severe: 'red', degraded: 'amber', healthy: '', excellent: 'green', insufficient_data: 'blue'};
const RESULT_LABELS: Record<string, string> = {ok: '正常', issues: '有待处理', failed: '失败', not_applicable: '未启用'};
const pct = (v: unknown) => (typeof v === 'number' ? `${Math.round(v * 1000) / 10}%` : '—');
const JOB_LABELS: Record<string, string> = {crawl: '抓取', audit: '数据审计', source_health: '片源健康', notify: '通知'};

function Bars({rows, max}: {rows: {label: string; n: number; tone?: string; to?: string}[]; max?: number}) {
  const top = max ?? Math.max(1, ...rows.map(r => r.n));
  return (
    <div className="adm-bars">
      {rows.map(r => (
        <div key={r.label} className="adm-bar-row">
          {r.to ? <Link to={r.to}>{r.label}</Link> : <span>{r.label}</span>}
          <span className="adm-bar-track">
            <span className={`adm-bar-fill ${r.tone ?? ''}`} style={{width: `${Math.round((r.n / top) * 100)}%`}} />
          </span>
          <span className="adm-bar-n">{fmtNum(r.n)}</span>
        </div>
      ))}
    </div>
  );
}

export default function AdminDashboard() {
  const dash = useAdminLoad(() => getDashboard());
  const mod = useAdminLoad(() => getModerationOverview(1));
  const [band, setBand] = useState('');
  const [health, setHealth] = useState<{items: SourceHealthItem[]; next: string | null} | null>(null);
  const [healthErr, setHealthErr] = useState('');
  const [healthBusy, setHealthBusy] = useState(false);
  const [seoOpen, setSeoOpen] = useState(false);

  const loadHealth = async (b: string, cursor: string | null) => {
    setHealthBusy(true);
    setHealthErr('');
    try {
      const page = await getSourceHealth({band: b, cursor, limit: 20, trend: 1});
      setHealth(prev => ({items: cursor && prev ? [...prev.items, ...page.items] : page.items, next: page.next_cursor}));
    } catch (cause) {
      setHealthErr(adminErrorText(cause));
    } finally {
      setHealthBusy(false);
    }
  };
  useAdminLoad(async () => {
    await loadHealth(band, null);
    return null;
  }, [band]);

  const d = dash.data;
  const counts = mod.data?.counts;
  const modTotal = counts ? counts.reports + counts.danmaku + counts.reviews + counts.rating_floods : null;
  const ageOpen = d?.issues.by_rule['R-AGE'] ?? 0;
  // 收录健康分 (PRD 11 / 13): the latest `npm run seo:check` run (nightly_runs job seo.check).
  const seoRun = d?.nightly_runs.find(r => r.job === 'seo.check');
  const seo = seoRun?.detail as {score?: number; band?: string; ssr_pass_rate?: number; sitemap_coverage?: number} | undefined;

  return (
    <>
      <AdminHeader
        eyebrow="ADMIN · DASHBOARD"
        title="总览"
        sub={d ? `生成于 ${fmtTime(d.generated_at)}` : undefined}
        actions={
          <button
            type="button"
            className="btn btn-secondary btn-sm"
            disabled={dash.busy}
            onClick={() => {
              dash.reload();
              mod.reload();
              void loadHealth(band, null);
            }}
          >
            {dash.busy ? '刷新中…' : '刷新'}
          </button>
        }
      />
      <AdminNotice error={dash.error} />

      {ageOpen > 0 && (
        <div className="adm-agegate" role="alert">
          <strong>合规：{ageOpen} 个 R-AGE（出演时年龄可能未满 18 岁）问题待处理</strong>
          <span>相关作品已自动暂停上线（held_review）。请先核对拍摄年份、发布日期或演员生日。</span>
          <span>
            <Link to="/admin/audit?rule=R-AGE" className="btn btn-sm btn-secondary">
              去处理 R-AGE
            </Link>
          </span>
        </div>
      )}

      <section className="adm-kpis" aria-label="关键指标">
        <div className="adm-kpi">
          <span className="adm-kpi-label">片源健康分</span>
          <span className={`adm-kpi-value ${BAND_TONE[d?.source_health.band ?? ''] ?? ''}`}>{d?.source_health.score ?? '—'}</span>
          <span className="adm-kpi-sub">
            {d
              ? d.source_health.attempts
                ? `可播放 ${fmtNum(d.source_health.playable)} / ${fmtNum(d.source_health.attempts)} 次`
                : '近 24 小时无播放样本'
              : ''}
          </span>
        </div>
        <button
          type="button"
          className="adm-kpi"
          aria-expanded={seoOpen}
          onClick={() => setSeoOpen(v => !v)}
          title="PRD 13：服务端渲染通过率 × 0.6 + 站点地图覆盖率 × 0.4（npm run seo:check，任务 seo.check）。点击查看详情"
        >
          <span className="adm-kpi-label">收录健康分</span>
          {seo ? (
            <>
              <span className={`adm-kpi-value ${BAND_TONE[String(seo.band ?? '')] ?? ''}`}>{seo.score ?? '—'}</span>
              <span className="adm-kpi-sub">
                {`SSR 通过 ${pct(seo.ssr_pass_rate)} · 地图覆盖 ${pct(seo.sitemap_coverage)} · ${fmtTime(seoRun!.started_at)}`}
              </span>
            </>
          ) : (
            <>
              <span className="adm-kpi-value">—</span>
              <span className="adm-kpi-sub">尚未运行 SEO 检查（npm run seo:check）</span>
            </>
          )}
          <span className="adm-kpi-sub">
            {d ? `数据完整 ${d.catalog_health.score ?? '—'} · 已上线 ${fmtNum(d.catalog_health.published)} · 有问题 ${fmtNum(d.catalog_health.with_open_issues)}` : ''}
          </span>
          <span className="adm-kpi-sub adm-link">{seoOpen ? '收起详情' : '查看详情 →'}</span>
        </button>
        <Link to="/admin/videos" className="adm-kpi">
          <span className="adm-kpi-label">视频</span>
          <span className="adm-kpi-value">{fmtNum(d?.releases.total)}</span>
          <span className="adm-kpi-sub">{d ? `合规暂停 ${fmtNum(d.releases.held)}` : ''}</span>
        </Link>
        <Link to="/admin/audit" className="adm-kpi">
          <span className="adm-kpi-label">数据质量待处理</span>
          <span className={`adm-kpi-value ${ageOpen ? 'red' : d?.issues.open ? 'amber' : ''}`}>{fmtNum(d?.issues.open)}</span>
          <span className="adm-kpi-sub">{d ? `阻断 ${fmtNum(d.issues.by_severity.blocking ?? 0)}` : ''}</span>
        </Link>
        <Link to="/admin/moderation" className="adm-kpi">
          <span className="adm-kpi-label">举报与审核</span>
          <span className={`adm-kpi-value ${counts?.reports_overdue ? 'red' : modTotal ? 'amber' : ''}`}>{fmtNum(modTotal)}</span>
          <span className="adm-kpi-sub">{counts ? `举报 ${counts.reports} · 超时 ${counts.reports_overdue}` : mod.error ? '不可用' : ''}</span>
        </Link>
        <div className="adm-kpi">
          <span className="adm-kpi-label">疑似重复 / 字段冲突</span>
          <span className={`adm-kpi-value ${d && d.identity_candidates_open + d.conflicts_open ? 'amber' : ''}`}>
            {d ? `${fmtNum(d.identity_candidates_open)} / ${fmtNum(d.conflicts_open)}` : '—'}
          </span>
          <span className="adm-kpi-sub">
            <Link to="/admin/dedupe">重复与归并 →</Link> · <Link to="/admin/review">字段冲突 →</Link>
          </span>
        </div>
      </section>
      {seoOpen && <SEOReportPanel onClose={() => setSeoOpen(false)} />}

      <AdminSiteKPIs />

      <div className="adm-grid-2">
        <section className="adm-card">
          <div className="adm-card-head">
            <h2>片源健康</h2>
            <span className="muted">{d?.source_health.last_run_at ? `上次评分 ${fmtTime(d.source_health.last_run_at)}` : '尚未评分'}</span>
          </div>
          <p className="adm-field-hint">分档：0–59 严重 · 60–79 降级 · 80–94 健康 · 95–100 优秀（PRD 11）</p>
          {d && (
            <Bars
              rows={BANDS.map(b => ({label: BAND_LABELS[b], n: d.source_health.by_band[b] ?? 0, tone: BAND_TONE[b]}))}
              max={Math.max(1, d.source_health.sources)}
            />
          )}
          {d && (
            <div className="adm-chips">
              {Object.entries(d.source_health.by_status).map(([s, n]) => (
                <Pill key={s} tone={statusTone(s)}>
                  {s === 'active' ? '启用' : s === 'degraded' ? '降级' : s === 'disabled' ? '停用' : s} {n}
                </Pill>
              ))}
              <Pill>共 {d.source_health.sources} 个片源</Pill>
            </div>
          )}
        </section>

        <section className="adm-card">
          <div className="adm-card-head">
            <h2>数据质量（未处理）</h2>
            <Link to="/admin/audit" className="adm-link">
              全部 →
            </Link>
          </div>
          {d && (
            <Bars
              rows={AUDIT_RULES.filter(r => d.issues.by_rule[r]).map(r => ({
                label: r,
                n: d.issues.by_rule[r],
                tone: r === 'R-AGE' ? 'red' : 'amber',
                to: `/admin/audit?rule=${r}`,
              }))}
            />
          )}
          {d && d.issues.open === 0 && <p className="muted">没有未处理的问题。</p>}
          {d && (
            <div className="adm-chips">
              {Object.entries(d.issues.by_severity).map(([s, n]) => (
                <Pill key={s} tone={s === 'blocking' ? 'red' : s === 'high' ? 'amber' : 'plain'}>
                  {SEVERITY_LABELS[s] ?? s} {n}
                </Pill>
              ))}
            </div>
          )}
          {d && Object.keys(d.issues.by_rule_severity).length > 0 && (
            <div className="adm-table-wrap">
              <table className="adm-table">
                <thead>
                  <tr>
                    <th>规则</th>
                    {['blocking', 'high', 'medium', 'low'].map(s => (
                      <th key={s} className="num">
                        {SEVERITY_LABELS[s]}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {Object.entries(d.issues.by_rule_severity).map(([rule, sev]) => (
                    <tr key={rule} className={rule === 'R-AGE' ? 'flag-red' : ''}>
                      <td>
                        <Link to={`/admin/audit?rule=${rule}`}>{RULE_LABELS[rule] ?? rule}</Link>
                      </td>
                      {['blocking', 'high', 'medium', 'low'].map(s => (
                        <td key={s} className="num">
                          {sev[s] ?? '·'}
                        </td>
                      ))}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </section>
      </div>

      <div className="adm-grid-2">
        <section className="adm-card">
          <h2>视频与演员状态</h2>
          {d && (
            <Bars
              rows={Object.entries(d.releases.by_status).map(([s, n]) => ({
                label: STATUS_LABELS[s] ?? s,
                n,
                tone: s === 'held_review' ? 'red' : s === 'published' ? 'green' : '',
                to: `/admin/videos?status=${s}`,
              }))}
            />
          )}
          {d && (
            <p className="adm-field-hint">
              演员 {fmtNum(d.performers.total)}：
              {Object.entries(d.performers.by_status)
                .map(([s, n]) => `${s} ${n}`)
                .join(' · ') || '—'}
            </p>
          )}
        </section>
        <section className="adm-card">
          <div className="adm-card-head">
            <h2>任务队列</h2>
            <Link to="/admin/pipeline" className="adm-link">
              入库流水线 →
            </Link>
          </div>
          <div className="adm-table-wrap">
            <table className="adm-table">
              <thead>
                <tr>
                  <th>任务</th>
                  <th className="num">排队</th>
                  <th className="num">运行中</th>
                  <th className="num">24h 失败</th>
                </tr>
              </thead>
              <tbody>
                {d && Object.keys(d.jobs).length === 0 && <EmptyRow cols={4}>没有任务记录</EmptyRow>}
                {d &&
                  Object.entries(d.jobs).map(([k, j]) => (
                    <tr key={k} className={j.failed_24h ? 'flag-red' : ''}>
                      <td>
                        {JOB_LABELS[k] ?? k} <span className="muted mono">{k}</span>
                      </td>
                      <td className="num">{j.pending}</td>
                      <td className="num">{j.running}</td>
                      <td className="num">{j.failed_24h}</td>
                    </tr>
                  ))}
              </tbody>
            </table>
          </div>
        </section>
      </div>

      <section className="adm-card">
        <div className="adm-card-head">
          <div>
            <h2>夜间任务</h2>
            <p className="adm-field-hint">每晚扫描全部已入库内容，能自动修的直接修，修不了的进待处理。</p>
          </div>
          <Link to="/admin/audit" className="adm-link">
            数据质量 →
          </Link>
        </div>
        <div className="adm-table-wrap">
          <table className="adm-table">
            <thead>
              <tr>
                <th>任务</th>
                <th>上次运行</th>
                <th className="num">耗时</th>
                <th className="num">检查</th>
                <th className="num">自动修复</th>
                <th className="num">问题</th>
                <th>结果</th>
              </tr>
            </thead>
            <tbody>
              {d && d.nightly_runs.length === 0 && <EmptyRow cols={7}>还没有运行记录</EmptyRow>}
              {d?.nightly_runs.map(r => (
                <tr key={`${r.job}-${r.started_at}`} className={r.result === 'failed' ? 'flag-red' : ''}>
                  <td className="mono">{r.job}</td>
                  <td>{fmtTime(r.started_at)}</td>
                  <td className="num">{fmtDuration(r.duration_ms)}</td>
                  <td className="num">{fmtNum(r.checked)}</td>
                  <td className="num">{fmtNum(r.fixed)}</td>
                  <td className="num">{fmtNum(r.issues)}</td>
                  <td>
                    <Pill tone={statusTone(r.result)}>{RESULT_LABELS[r.result] ?? r.result}</Pill>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>

      <section className="adm-card">
        <div className="adm-card-head">
          <div>
            <h2>片源健康明细</h2>
            <p className="adm-field-hint">最差的排前面；从未评分的排最后。</p>
          </div>
          <div className="adm-chips" role="group" aria-label="按分档筛选">
            {['', ...BANDS].map(b => (
              <button key={b || 'all'} type="button" className="adm-chip" aria-pressed={band === b} onClick={() => setBand(b)}>
                {b ? BAND_LABELS[b] : '全部'}
              </button>
            ))}
          </div>
        </div>
        <AdminNotice error={healthErr} />
        <div className="adm-table-wrap">
          <table className="adm-table">
            <thead>
              <tr>
                <th>视频</th>
                <th>片源</th>
                <th>状态</th>
                <th>健康</th>
                <th>评分时间</th>
              </tr>
            </thead>
            <tbody>
              {health && health.items.length === 0 && <EmptyRow cols={5}>没有片源</EmptyRow>}
              {health?.items.map(s => (
                <tr key={s.source_id} className={s.band === 'severe' ? 'flag-red' : ''}>
                  <td className="wrap">
                    <Link to={`/admin/videos/${s.release_id}?tab=sources`}>{s.release_title || s.release_slug}</Link>
                  </td>
                  <td>
                    {s.label} <span className="muted">· {s.provider}</span>
                  </td>
                  <td>
                    <Pill tone={statusTone(s.status)}>{s.status}</Pill>
                  </td>
                  <td>
                    <BandPill band={s.band} score={s.score} />
                  </td>
                  <td>{fmtTime(s.checked_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <MoreButton next={health?.next ?? null} busy={healthBusy} onMore={() => void loadHealth(band, health?.next ?? null)} />
      </section>
    </>
  );
}
