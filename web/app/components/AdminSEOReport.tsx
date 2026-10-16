import {ApiError} from '../lib/api';
import {AdminNotice, BandPill, EmptyRow, Pill, fmtDuration, fmtNum, fmtTime} from './AdminUI';
import {getSEOReport, routeMissing} from '../lib/adminGovApi';
import {useAdminLoad} from '../lib/adminContext';

// 收录健康分 details (PRD 11 / 13): the latest `npm run seo:check` run from
// GET admin/seo/report — SSR pass rate, sitemap coverage per type and the
// failing sample URLs with their reasons.

const pct = (v?: number) => (typeof v === 'number' ? `${Math.round(v * 1000) / 10}%` : '—');
const TYPE_LABELS: Record<string, string> = {releases: '作品', performers: '演员', tags: '标签', pages: '页面', series: '系列'};
const REASON_LABELS: Record<string, string> = {
  h1: '缺少或空 h1',
  canonical: '缺少 canonical',
  canonical_mismatch: 'canonical 不一致',
  hreflang: 'hreflang 不全',
  html_lang: 'html lang 不对',
  noindex: '页面 noindex',
};
const reasonLabel = (r: string) => REASON_LABELS[r] ?? (r.startsWith('status_') ? `HTTP ${r.slice(7)}` : r);

export function SEOReportPanel({onClose}: {onClose: () => void}) {
  const rep = useAdminLoad(() => getSEOReport());
  const d = rep.data?.detail;
  const none = rep.failure instanceof ApiError && rep.failure.status === 404 && !routeMissing(rep.failure);
  const missing = rep.failure && routeMissing(rep.failure);
  const types = Object.entries(d?.by_type ?? {});
  const sitemap = Object.entries(d?.sitemap ?? {});
  const failures = d?.failures ?? [];

  return (
    <section className="adm-card" aria-label="收录健康详情">
      <div className="adm-card-head">
        <h2>收录健康详情</h2>
        <span style={{display: 'flex', gap: 12}}>
          <button type="button" className="adm-link" disabled={rep.busy} onClick={rep.reload}>
            {rep.busy ? '刷新中…' : '刷新'}
          </button>
          <button type="button" className="adm-link" onClick={onClose}>
            关闭
          </button>
        </span>
      </div>
      {missing ? (
        <div className="adm-notice adm-notice-warn" role="status">
          后端未部署：SEO 检查报告接口（admin/seo/report）。
        </div>
      ) : none ? (
        <p className="muted">还没有 SEO 检查记录：运行 npm run seo:check（设置 ADMIN_TOKEN 时会自动上报）。</p>
      ) : (
        <AdminNotice error={rep.error} />
      )}
      {rep.data && (
        <>
          <section className="adm-kpis">
            <div className="adm-kpi">
              <span className="adm-kpi-label">收录健康分</span>
              <span className="adm-kpi-value">{d?.score ?? '—'}</span>
              <span className="adm-kpi-sub">
                <BandPill band={d?.band} />
              </span>
            </div>
            <div className="adm-kpi">
              <span className="adm-kpi-label">SSR 通过率（× 0.6）</span>
              <span className="adm-kpi-value">{pct(d?.ssr_pass_rate)}</span>
              <span className="adm-kpi-sub">
                抽样 {fmtNum(d?.checked ?? rep.data.checked)} 页 · 失败 {fmtNum(d?.failed ?? rep.data.issues)}
              </span>
            </div>
            <div className="adm-kpi">
              <span className="adm-kpi-label">站点地图覆盖（× 0.4）</span>
              <span className="adm-kpi-value">{pct(d?.sitemap_coverage)}</span>
              <span className="adm-kpi-sub">{(d?.sitemap_missing_langs ?? []).length ? `缺语言 ${(d?.sitemap_missing_langs ?? []).join('、')}` : '各语言站点地图齐全'}</span>
            </div>
            <div className="adm-kpi">
              <span className="adm-kpi-label">检查时间</span>
              <span className="adm-kpi-value" style={{fontSize: 18}}>
                {fmtTime(d?.checked_at ?? rep.data.started_at)}
              </span>
              <span className="adm-kpi-sub">
                <Pill tone={rep.data.result === 'ok' ? 'green' : 'amber'}>{rep.data.result === 'ok' ? '正常' : '有待处理'}</Pill> · 用时 {fmtDuration(rep.data.duration_ms)}
                {d?.base_url ? ` · ${d.base_url}` : ''}
              </span>
            </div>
          </section>
          <div className="adm-grid-2">
            <div className="adm-table-wrap">
              <table className="adm-table">
                <thead>
                  <tr>
                    <th>类型</th>
                    <th className="num">SSR 抽样</th>
                    <th className="num">通过</th>
                  </tr>
                </thead>
                <tbody>
                  {types.length === 0 && <EmptyRow cols={3}>没有分类型数据</EmptyRow>}
                  {types.map(([t, v]) => (
                    <tr key={t} className={v.passed < v.checked ? 'flag-red' : ''}>
                      <td>{TYPE_LABELS[t] ?? t}</td>
                      <td className="num">{fmtNum(v.checked)}</td>
                      <td className="num">{fmtNum(v.passed)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <div className="adm-table-wrap">
              <table className="adm-table">
                <thead>
                  <tr>
                    <th>站点地图</th>
                    <th className="num">已列出</th>
                    <th className="num">应列出</th>
                    <th className="num">文件 / 语言</th>
                  </tr>
                </thead>
                <tbody>
                  {sitemap.length === 0 && <EmptyRow cols={4}>没有站点地图数据</EmptyRow>}
                  {sitemap.map(([t, v]) => (
                    <tr key={t} className={v.expected !== null && v.listed < v.expected ? 'flag-red' : ''}>
                      <td>{TYPE_LABELS[t] ?? t}</td>
                      <td className="num">{fmtNum(v.listed)}</td>
                      <td className="num">{v.expected === null ? '—' : fmtNum(v.expected)}</td>
                      <td className="num">{fmtNum(v.files_per_lang)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
          <h3>未通过的页面（{failures.length}）</h3>
          {failures.length === 0 ? (
            <p className="muted">抽样页面全部通过。</p>
          ) : (
            <div className="adm-table-wrap">
              <table className="adm-table">
                <thead>
                  <tr>
                    <th>URL</th>
                    <th>原因</th>
                  </tr>
                </thead>
                <tbody>
                  {failures.map(f => (
                    <tr key={f.url} className="flag-red">
                      <td className="wrap mono">
                        <a href={f.url} target="_blank" rel="noreferrer noopener">
                          {f.url}
                        </a>
                      </td>
                      <td className="wrap">
                        {(f.reasons ?? []).map(r => (
                          <Pill key={r} tone="red" title={r}>
                            {reasonLabel(r)}
                          </Pill>
                        ))}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </section>
  );
}
