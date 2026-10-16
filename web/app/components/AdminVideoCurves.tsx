import {useMemo, useState, type KeyboardEvent, type PointerEvent} from 'react';
import {AdminNotice, NotDeployed, Pill, fmtNum, fmtTime} from './AdminUI';
import {topPeaks} from './PeakCurve';
import {isMissingEndpoint} from '../lib/api';
import {getReleaseCurves, type CurveVersion} from '../lib/adminDataApi';
import {useAdminLoad} from '../lib/adminContext';
import {formatClock} from '../lib/format';
import '../styles/admin-data.css';

// AdminVideo 数据标签 tab (PRD 14.3): GET admin/releases/{id}/curves drawn as
// three inline SVG charts sharing one time axis — 留存 (viewers / valid
// plays), 重看 (replays / viewers) and 跳过 (skips / valid plays). Markers
// show the three largest retention drops, rewatch peaks and skip peaks; the
// pointer (or ←/→ on a focused chart) moves one cursor across all three and
// the readout lists every value at that bucket. Curves come from the hourly
// curve job; a release without plays has no versions.

const BUCKETS = 200;
const W = 600;
const H = 120;
const PAD_TOP = 8;

const VERSION_LABELS: Record<string, string> = {main: '正片', high_quality: '高清版', dub: '配音版', alt: '其他版本'};

type Series = {key: 'retention' | 'replay' | 'skip'; title: string; hint: string; values: number[]; peaks: number[]; peakLabel: string; tone: string; kind: 'line' | 'bars'};

const pctText = (v: number | undefined) => (v === undefined ? '—' : `${(Math.round(v * 1000) / 10).toFixed(1)}%`);

/** The three largest one-bucket drops of a retention curve (where viewers leave). */
export function retentionDrops(retention: number[], n = 3): number[] {
  const drops = retention.map((v, i) => (i === 0 ? 0 : Math.max(0, retention[i - 1] - v)));
  return topPeaks(drops, n);
}

function bucketTime(b: number, durationMS: number | null): string {
  if (!durationMS) return `第 ${b + 1} / ${BUCKETS} 段`;
  const width = durationMS / BUCKETS / 1000;
  return `${formatClock(b * width)}–${formatClock((b + 1) * width)}`;
}

function linePath(values: number[], top: number): string {
  const n = values.length;
  return values
    .map((v, i) => {
      const x = ((i + 0.5) / n) * W;
      const y = H - (Math.min(v, top) / top) * (H - PAD_TOP);
      return `${i === 0 ? 'M' : 'L'}${x.toFixed(1)} ${y.toFixed(1)}`;
    })
    .join(' ');
}

function CurveChart({s, cursor, onCursor, durationMS}: {s: Series; cursor: number | null; onCursor: (b: number | null) => void; durationMS: number | null}) {
  const top = Math.max(s.key === 'retention' ? 1 : 0.0001, ...s.values) * (s.key === 'retention' ? 1 : 1.1);
  const bw = W / BUCKETS;
  const yOf = (v: number) => H - (Math.min(v, top) / top) * (H - PAD_TOP);
  const pick = (e: PointerEvent<SVGSVGElement>) => {
    const rect = e.currentTarget.getBoundingClientRect();
    if (rect.width <= 0) return;
    onCursor(Math.max(0, Math.min(BUCKETS - 1, Math.floor(((e.clientX - rect.left) / rect.width) * BUCKETS))));
  };
  const key = (e: KeyboardEvent<SVGSVGElement>) => {
    const step = e.shiftKey ? 10 : 1;
    if (e.key === 'ArrowRight') onCursor(Math.min(BUCKETS - 1, (cursor ?? -1) + step));
    else if (e.key === 'ArrowLeft') onCursor(Math.max(0, (cursor ?? BUCKETS) - step));
    else if (e.key === 'Escape') onCursor(null);
    else return;
    e.preventDefault();
  };
  return (
    <figure className="adm-curve">
      <figcaption>
        <strong>{s.title}</strong>
        <span className="muted">{s.hint}</span>
        <span className="adm-curve-top mono">峰值 {pctText(Math.max(0, ...s.values))}</span>
      </figcaption>
      <svg
        viewBox={`0 0 ${W} ${H}`}
        preserveAspectRatio="none"
        className={`adm-curve-svg ${s.tone}`}
        role="img"
        tabIndex={0}
        aria-label={`${s.title}曲线；${s.peakLabel}：${s.peaks.map(b => bucketTime(b, durationMS)).join('、') || '无'}。左右方向键移动读数`}
        onPointerMove={pick}
        onPointerDown={pick}
        onPointerLeave={() => onCursor(null)}
        onKeyDown={key}
      >
        {[0.25, 0.5, 0.75].map(g => (
          <line key={g} x1={0} x2={W} y1={yOf(top * g)} y2={yOf(top * g)} className="adm-curve-grid" />
        ))}
        {s.kind === 'bars' ? (
          s.values.map((v, i) => (v > 0 ? <rect key={i} x={i * bw + 0.3} width={bw - 0.6} y={yOf(v)} height={H - yOf(v)} className="adm-curve-bar" /> : null))
        ) : (
          <>
            <path d={`${linePath(s.values, top)} L${W} ${H} L0 ${H} Z`} className="adm-curve-area" />
            <path d={linePath(s.values, top)} className="adm-curve-line" vectorEffect="non-scaling-stroke" />
          </>
        )}
        {s.peaks.map((b, i) => (
          <g key={b} className="adm-curve-peak">
            <line x1={(b + 0.5) * bw} x2={(b + 0.5) * bw} y1={0} y2={H} vectorEffect="non-scaling-stroke" />
            <circle cx={(b + 0.5) * bw} cy={Math.max(4, yOf(s.values[b]))} r={3.5} vectorEffect="non-scaling-stroke">
              <title>
                {s.peakLabel} {i + 1}：{bucketTime(b, durationMS)} · {pctText(s.values[b])}
              </title>
            </circle>
          </g>
        ))}
        {cursor !== null && <line x1={(cursor + 0.5) * bw} x2={(cursor + 0.5) * bw} y1={0} y2={H} className="adm-curve-cursor" vectorEffect="non-scaling-stroke" />}
      </svg>
      <div className="adm-curve-peaks">
        <span className="muted">{s.peakLabel}：</span>
        {s.peaks.length === 0 && <span className="muted">无</span>}
        {s.peaks.map(b => (
          <button key={b} type="button" className="adm-chip" onClick={() => onCursor(b)}>
            {bucketTime(b, durationMS)} · {pctText(s.values[b])}
          </button>
        ))}
      </div>
    </figure>
  );
}

function Readout({v, cursor}: {v: CurveVersion; cursor: number | null}) {
  if (cursor === null) return <p className="adm-curve-readout muted">把鼠标移到曲线上（或聚焦后用 ← / →）查看每一段的数值。</p>;
  const b = cursor;
  return (
    <dl className="adm-curve-readout" aria-live="polite">
      <div>
        <dt>时间</dt>
        <dd className="mono">{bucketTime(b, v.duration_ms)}</dd>
      </div>
      <div>
        <dt>留存</dt>
        <dd className="mono">
          {pctText(v.retention[b])} <span className="muted">（{fmtNum(v.viewers[b])} 人）</span>
        </dd>
      </div>
      <div>
        <dt>重看率</dt>
        <dd className="mono">
          {pctText(v.replay_rate[b])} <span className="muted">（{fmtNum(v.replays[b])} 次）</span>
        </dd>
      </div>
      <div>
        <dt>跳过率</dt>
        <dd className="mono">
          {v.skip_rate ? pctText(v.skip_rate[b]) : '—'} {v.skips && <span className="muted">（{fmtNum(v.skips[b])} 次）</span>}
        </dd>
      </div>
      {v.replay_peak && (
        <div>
          <dt>播放波峰</dt>
          <dd className="mono">{v.replay_peak[b]} / 255</dd>
        </div>
      )}
    </dl>
  );
}

export function AdminVideoCurves({releaseId}: {releaseId: number}) {
  const load = useAdminLoad(signal => getReleaseCurves(releaseId, signal), [releaseId]);
  const [picked, setPicked] = useState<string | null>(null);
  const [cursor, setCursor] = useState<number | null>(null);
  const data = load.data;
  const versions = data?.versions ?? [];
  const v = versions.find(x => x.version_key === picked) ?? versions[0];

  const series = useMemo<Series[]>(() => {
    if (!v) return [];
    const out: Series[] = [
      {
        key: 'retention',
        title: '留存',
        hint: '看到这一段的有效播放占比',
        values: v.retention,
        peaks: retentionDrops(v.retention),
        peakLabel: '流失最多',
        tone: 'blue',
        kind: 'line',
      },
      {
        key: 'replay',
        title: '重看',
        hint: v.sufficient ? '重看次数 / 看到这一段的人数' : `不足 ${fmtNum(data?.min_valid_plays)} 次有效播放，前台不显示播放波峰`,
        values: v.replay_rate,
        peaks: topPeaks(v.replay_rate),
        peakLabel: '重看最多',
        tone: 'purple',
        kind: 'line',
      },
    ];
    if (v.skip_rate)
      out.push({key: 'skip', title: '跳过', hint: '拖动跳过这一段的有效播放占比', values: v.skip_rate, peaks: topPeaks(v.skip_rate), peakLabel: '跳过最多', tone: 'amber', kind: 'bars'});
    return out;
  }, [v, data?.min_valid_plays]);

  if (load.failure && isMissingEndpoint(load.failure)) return <NotDeployed what="播放曲线接口（admin/releases/{id}/curves）" />;

  return (
    <section className="adm-card">
      <div className="adm-card-head">
        <div>
          <h2>数据标签</h2>
          <p className="adm-field-hint">每小时由播放事件计算；时间轴分 200 段。去掉机器人和异常播放后才计入有效播放。</p>
        </div>
        <button type="button" className="btn btn-secondary btn-sm" disabled={load.busy} onClick={load.reload}>
          {load.busy ? '加载中…' : '刷新'}
        </button>
      </div>
      <AdminNotice error={load.error} />
      {data && versions.length === 0 && <p className="muted">还没有曲线：这条视频暂无可统计的播放，或曲线任务尚未运行。</p>}
      {versions.length > 1 && (
        <div className="adm-chips" role="radiogroup" aria-label="时间线">
          {versions.map(x => (
            <button
              key={x.version_key}
              type="button"
              role="radio"
              className="adm-chip"
              aria-checked={x.version_key === v?.version_key}
              aria-pressed={x.version_key === v?.version_key}
              onClick={() => {
                setPicked(x.version_key);
                setCursor(null);
              }}
            >
              {VERSION_LABELS[x.version_key] ?? x.version_key} · {fmtNum(x.valid_plays)}
            </button>
          ))}
        </div>
      )}
      {v && (
        <>
          <div className="adm-chips">
            <Pill tone={v.sufficient ? 'green' : 'amber'}>有效播放 {fmtNum(v.valid_plays)}</Pill>
            <Pill>时长 {v.duration_ms ? formatClock(v.duration_ms / 1000) : '未知'}</Pill>
            <Pill>计算于 {fmtTime(v.computed_at)}</Pill>
            {v.intro_skip_suggested && (
              <Pill tone="amber" title="跳过集中在前 10%（PRD 14.3）">
                建议标注片头
              </Pill>
            )}
            {!v.skip_rate && <Pill tone="blue">时长未知，没有跳过曲线</Pill>}
          </div>
          <Readout v={v} cursor={cursor} />
          <div className="adm-curves">
            {series.map(s => (
              <CurveChart key={s.key} s={s} cursor={cursor} onCursor={setCursor} durationMS={v.duration_ms} />
            ))}
          </div>
        </>
      )}
    </section>
  );
}
