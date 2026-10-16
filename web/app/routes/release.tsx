import {useEffect, useMemo, useState, type ReactNode} from 'react';
import {Link, useLocation, useNavigate, useParams, useSearchParams} from 'react-router';
import {Avatar, ReleaseRow, SectionHeader} from '../components/Cards';
import {Icon} from '../components/Icon';
import {PerformerHoverCard, PerformerSheet} from '../components/PerformerPeek';
import {Player} from '../components/Player';
import {LaterButton, ReserveButton} from '../components/SocialButtons';
import {BlockSkeleton, CardGridSkeleton, EmptyState, ErrorBanner} from '../components/States';
import {getFavorites, getPerformerReleases, getWatchLater, getRelease, getReleasePerformers, getWatchHistory, setFavorite, type Performer, type ReleaseDetail} from '../lib/api';
import {useAuth} from '../lib/auth';
import {ageAt, formatDate, formatRuntime} from '../lib/format';
import {discoverHref} from '../lib/releaseFilter';
import {useAsync} from '../lib/useAsync';
import {useCanonicalSlug} from '../lib/useCanonicalSlug';
import {useMediaQuery} from '../lib/useMediaQuery';
import {readQueue} from '../lib/queue';
import type {ShellHandle} from './shell';

export const handle: ShellHandle = {immersive: true};
export const meta = () => [{title: 'REEL · 视频'}];

// Video / VideoLight / MobileVideo.
//
// Omitted for lack of backend (never filled with sample data):
// TODO(tech plan 3.13): 赞 / 踩, 评论 (V1.1), 自动连播 switch.
// 稍后观看 toggles here; ?list=later follows the 全部播放 / 随机播放 queue.
// 预约 shows for status 'upcoming', but GET /api/releases/{slug} only serves
// published works today, so upcoming works are reserved from the feed.
// TODO(PRD 15 AgeGate): gate playback with <AgeConfirm> once releases carry an
// age rating; the API has no rating field yet, so the gate is not mounted.
// TODO(tech plan 2.4 / 3.2 release_assets): 章节 · 截屏 strip.
// TODO(tech plan 3.5 GET /api/recs?slot=related): 相关推荐 / 同系列 tabs.
// 片源 row and versions live in Player (GET play-config). Tags, director and
// product codes come from the catalog-filters release detail and are simply
// absent on older backends.
export default function ReleasePage() {
  const {slug = ''} = useParams();
  const navigate = useNavigate();
  const {status} = useAuth();
  const detail = useAsync(signal => Promise.all([getRelease(slug, signal), getReleasePerformers(slug, signal)]), [slug]);
  const history = useAsync(signal => getWatchHistory(signal), [status], status === 'user');
  const mobile = useMediaQuery('(max-width: 767px)');
  // Hover cards need a real hover; phones and tablets (any width) get the sheet.
  const canHover = useMediaQuery('(hover: hover) and (pointer: fine)');
  const useSheet = mobile || !canHover;
  const [theater, setTheater] = useState(false);
  const [sheetPerformer, setSheetPerformer] = useState<Performer | null>(null);
  const [params] = useSearchParams();
  const listName = params.get('list');
  const queue = useMemo(() => (listName === 'later' && typeof window !== 'undefined' ? readQueue('later') : null), [listName]);
  const queueIndex = queue ? queue.slugs.indexOf(slug) : -1;
  const nextSlug = queue && queueIndex >= 0 ? queue.slugs[queueIndex + 1] : undefined;

  useEffect(() => setSheetPerformer(null), [slug]);

  const release = detail.data?.[0];
  const performers = detail.data?.[1] ?? [];
  const resumeAt = history.data?.find(item => item.slug === slug)?.progress_seconds;

  useCanonicalSlug('releases', slug, release?.slug);

  useEffect(() => {
    if (release) document.title = `${release.canonical_title} · REEL`;
  }, [release]);

  const back = () => {
    if (window.history.length > 1) navigate(-1);
    else navigate('/');
  };

  if (detail.error) {
    const missing = detail.error.message === '内容不存在或已下架';
    return (
      <div className="page">
        {missing ? (
          <EmptyState
            icon="film"
            title="这个视频暂时无法观看"
            body="作品不存在、已下架，或链接有误。你的观看记录和收藏会保留。"
            actions={[{to: '/', label: '回到首页'}, {to: '/discover?tab=new', label: '看看新上线', primary: false}]}
          />
        ) : (
          <ErrorBanner error={detail.error} onRetry={detail.reload} />
        )}
      </div>
    );
  }

  return (
    <div className="video-page" data-theater={theater && !mobile ? 'true' : 'false'}>
      <div className="video-main">
        <Player
          slug={slug}
          title={release?.canonical_title ?? ''}
          resumeAt={resumeAt}
          onBack={back}
          theater={theater}
          onToggleTheater={() => setTheater(t => !t)}
          onEnded={nextSlug ? () => navigate(`/releases/${encodeURIComponent(nextSlug)}?list=later`) : undefined}
        />

        {!release ? (
          detail.slow && <BlockSkeleton lines={3} />
        ) : (
          <>
            <div className="video-head">
              <h1 className="video-title">{release.canonical_title}</h1>
              {release.title_ja && release.title_ja !== release.canonical_title && <p className="video-alt-title">{release.title_ja}</p>}
              <div className="video-subrow">
                <span className="muted">
                  {[release.release_date && `发行 ${formatDate(release.release_date)}`, formatRuntime(release.runtime_minutes)].filter(Boolean).join(' · ')}
                </span>
                <span className="section-spacer" />
                <div className="action-row">
                  {release.status === 'upcoming' ? (
                    <ReserveButton slug={slug} title={release.canonical_title} />
                  ) : (
                    <LaterButton slug={slug} title={release.canonical_title} />
                  )}
                  <FavoriteButton slug={slug} />
                  <ShareButton title={release.canonical_title} />
                </div>
              </div>
            </div>

            <ReleaseMeta release={release} />

            {performers.length > 0 && (
              <section className="cast">
                <SectionHeader title="主演" sub={useSheet ? '点击查看演员资料' : '鼠标悬停查看演员资料'} />
                <div className="avatar-row">
                  {performers.map(p =>
                    useSheet ? (
                      <CastMember key={p.id} performer={p} releaseDate={release.release_date} onOpen={() => setSheetPerformer(p)} />
                    ) : (
                      <PerformerHoverCard key={p.id} performer={p} releaseDate={release.release_date} currentSlug={slug}>
                        <CastMember performer={p} releaseDate={release.release_date} />
                      </PerformerHoverCard>
                    ),
                  )}
                </div>
              </section>
            )}
            <PerformerSheet
              performer={useSheet ? sheetPerformer : null}
              releaseDate={release.release_date}
              currentSlug={slug}
              onClose={() => setSheetPerformer(null)}
            />
          </>
        )}
      </div>

      <aside className="video-aside" aria-label="接下来播放">
        {queue && queueIndex >= 0 && <QueuePanel slugs={queue.slugs} index={queueIndex} />}
        {release && performers[0] && <SamePerformer performer={performers[0]} currentSlug={slug} />}
      </aside>
    </div>
  );
}

// release_identifiers.identifier_type values written by the ingest
// (store.SaveCatalogRecord); any other type shows under 编号.
const IDENTIFIER_LABELS: Record<string, string> = {
  dvd_code: '番号',
  content_id: '品番',
};

function ReleaseMeta({release}: {release: ReleaseDetail}) {
  const rows: [string, ReactNode][] = [
    ['发行日期', formatDate(release.release_date)],
    ['时长', formatRuntime(release.runtime_minutes)],
    [
      '片商',
      release.maker_name ? (
        <Link to={discoverHref({maker: release.maker_name})} className="meta-link">
          {release.maker_name}
        </Link>
      ) : undefined,
    ],
    ['发行商', release.label_name],
    ['系列', release.series_name],
    ['导演', release.director_name || undefined],
  ];
  // Product codes (catalog-filters backend); one row per identifier type.
  const byType = new Map<string, string[]>();
  for (const id of release.identifiers ?? []) {
    if (!id?.value) continue;
    const label = IDENTIFIER_LABELS[id.type] ?? '编号';
    byType.set(label, [...(byType.get(label) ?? []), id.value]);
  }
  for (const [label, values] of byType) rows.push([label, <span className="mono">{Array.from(new Set(values)).join(' · ')}</span>]);

  const present = rows.filter(([, v]) => v);
  const tags = (release.tags ?? []).filter(t => t?.slug && t.name);
  if (present.length === 0 && tags.length === 0) return null;
  return (
    <section className="info-card">
      {present.length > 0 && (
        <dl className="meta-grid">
          {present.map(([k, v]) => (
            <div key={k} className="meta-item">
              <dt>{k}</dt>
              <dd>{v}</dd>
            </div>
          ))}
        </dl>
      )}
      {tags.length > 0 && (
        <div className="chip-row chip-row-wrap release-tags" aria-label="标签">
          {tags.map(t => (
            <Link key={t.slug} to={discoverHref({tag: t.slug})} className="chip chip-tag">
              #{t.name}
            </Link>
          ))}
        </div>
      )}
    </section>
  );
}

function CastMember({performer, releaseDate, onOpen}: {performer: Performer; releaseDate?: string; onOpen?: () => void}) {
  // PRD 6.4: only a release date is known (no filming year), so always 「约」;
  // hidden when either date is missing.
  const age = ageAt(performer.birth_date, releaseDate);
  return (
    <Link
      to={`/performers/${encodeURIComponent(performer.slug)}`}
      className="performer-chip cast-member"
      aria-haspopup={onOpen ? 'dialog' : undefined}
      onClick={
        onOpen
          ? event => {
              // Mobile: open MobileActorSheet in place; the video keeps playing (PRD 6.3).
              if (event.metaKey || event.ctrlKey || event.shiftKey || event.button !== 0) return;
              event.preventDefault();
              onOpen();
            }
          : undefined
      }
    >
      <Avatar seed={performer.slug} name={performer.canonical_name} size={88} />
      <span className="performer-chip-name">{performer.canonical_name}</span>
      {performer.canonical_name_ja && <span className="performer-chip-sub">{performer.canonical_name_ja}</span>}
      {age !== null && <span className="age-pill">约 {age} 岁</span>}
    </Link>
  );
}

function SamePerformer({performer, currentSlug}: {performer: Performer; currentSlug: string}) {
  const works = useAsync(signal => getPerformerReleases(performer.slug, 12, signal), [performer.slug]);
  const list = (works.data ?? []).filter(r => r.slug !== currentSlug).slice(0, 10);
  if (works.error) return null;
  if (!works.loading && list.length === 0) return null;
  return (
    <section className="aside-section">
      <SectionHeader title="接下来播放" sub={`同演员 · ${performer.canonical_name}`} />
      {works.slow ? (
        <CardGridSkeleton count={4} variant="row" />
      ) : (
        <div className="row-list">
          {list.map(r => (
            <ReleaseRow
              key={r.id}
              slug={r.slug}
              title={r.canonical_title}
              badge={r.runtime_minutes ? `${r.runtime_minutes} 分` : undefined}
              meta={`同演员 · ${performer.canonical_name}`}
              sub={formatDate(r.release_date)}
            />
          ))}
        </div>
      )}
    </section>
  );
}

function FavoriteButton({slug}: {slug: string}) {
  const {status} = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const favorites = useAsync(signal => getFavorites(signal), [status], status === 'user');
  const [saved, setSaved] = useState<boolean | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => setSaved(null), [slug]);
  const isSaved = saved ?? (favorites.data?.some(f => f.slug === slug) ?? false);

  const toggle = async () => {
    if (status !== 'user') {
      navigate(`/login?next=${encodeURIComponent(location.pathname)}`);
      return;
    }
    const next = !isSaved;
    setSaved(next);
    setBusy(true);
    setError('');
    try {
      await setFavorite(slug, next);
    } catch (cause) {
      setSaved(!next);
      setError(cause instanceof Error ? cause.message : '收藏失败');
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <button type="button" className={isSaved ? 'pill-btn is-on' : 'pill-btn'} aria-pressed={isSaved} disabled={busy} onClick={toggle}>
        <Icon name="favorites" size={18} filled={isSaved} />
        {isSaved ? '已收藏' : '收藏'}
      </button>
      {error && (
        <span className="inline-error" role="alert">
          {error}
        </span>
      )}
    </>
  );
}

function ShareButton({title}: {title: string}) {
  const [message, setMessage] = useState('');
  const share = async () => {
    const url = window.location.href;
    try {
      if (navigator.share) {
        await navigator.share({title, url});
        return;
      }
      await navigator.clipboard.writeText(url);
      setMessage('链接已复制');
    } catch {
      setMessage('');
    }
    window.setTimeout(() => setMessage(''), 2000);
  };
  return (
    <>
      <button type="button" className="pill-btn" onClick={share}>
        <Icon name="share" size={18} />
        分享
      </button>
      <span className="sr-status" role="status">
        {message}
      </span>
    </>
  );
}

/** 稍后观看 queue (全部播放 / 随机播放): position, 下一个, and the rest of the list. */
function QueuePanel({slugs, index}: {slugs: string[]; index: number}) {
  const later = useAsync(signal => getWatchLater(signal), []);
  const titles = new Map((later.data ?? []).map(i => [i.slug, i.title]));
  const rest = slugs.slice(index + 1);
  return (
    <section className="aside-section queue-panel" aria-label="播放列表">
      <SectionHeader title="稍后观看" sub={`${index + 1} / ${slugs.length}`} />
      {rest.length === 0 ? (
        <p className="muted">这是列表里的最后一个视频。</p>
      ) : (
        <div className="row-list">
          {rest.slice(0, 8).map((s, i) => (
            <ReleaseRow key={s} slug={s} query="?list=later" title={titles.get(s) ?? s} meta={i === 0 ? '下一个 · 播完自动播放' : `第 ${index + i + 2} 个`} />
          ))}
        </div>
      )}
      <Link to="/later" className="text-btn">
        管理稍后观看
      </Link>
    </section>
  );
}
