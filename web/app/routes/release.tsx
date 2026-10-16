import {useEffect, useRef, useState, type ReactNode} from 'react';
import {data, redirect, useLoaderData, useParams, useSearchParams, type LoaderFunctionArgs, type MetaFunction} from 'react-router';
import {Link, useLocation, useNavigate} from '../lib/nav';
import {Avatar, ReleaseRow, SectionHeader} from '../components/Cards';
import {Icon} from '../components/Icon';
import {PerformerHoverCard, PerformerSheet} from '../components/PerformerPeek';
import {Player} from '../components/Player';
import {HostedPlayer} from '../components/PlayerHost';
import type {EndItem} from '../components/PlayerStates';
import {Community} from '../components/Ratings';
import {LaterButton, ReserveButton} from '../components/SocialButtons';
import {BlockSkeleton, CardGridSkeleton, EmptyState, ErrorBanner} from '../components/States';
import {ApiError, getFavorites, getPerformerReleases, getWatchLater, getRelease, getReleasePerformers, getWatchHistory, setFavorite, type Performer, type ReleaseDetail} from '../lib/api';
import {track} from '../lib/analytics';
import {useAuth} from '../lib/auth';
import {t, useI18n, useT} from '../lib/i18n';
import {isGone, serverApi} from '../lib/api.server';
import {localizePath, routeLang} from '../lib/langs';
import type {MessageKey} from '../lib/messages.zh-CN';
import {breadcrumbs, clip, compact, isoDate, isoMinutes, pageUrl, rootMeta, seoMeta, type SeoHandle} from '../lib/seo';
import type {LangCode} from '../lib/api';
import {markAutoplayNavigation} from '../lib/playAnalytics';
import {usePlayerPrefs} from '../lib/playerPrefs';
import {ageAt, formatDate, formatRuntime} from '../lib/format';
import {discoverHref} from '../lib/releaseFilter';
import {useAsync} from '../lib/useAsync';
import {useCanonicalSlug} from '../lib/useCanonicalSlug';
import {useMediaQuery} from '../lib/useMediaQuery';
import {readQueue} from '../lib/queue';
import {getRecs, performerName, reasonText, recTitle, inRelatedTab, type RecItem, type RelatedTab} from '../lib/recs';
import type {ShellHandle} from './shell';

export const handle: ShellHandle & SeoHandle = {immersive: true, seo: true};

// Server rendering (PRD 13 SEO): the loader fetches the release and its cast
// from the API so the document carries the title, metadata, cast links and
// JSON-LD without JavaScript. Client-side navigations skip it (clientLoader)
// and the page fetches as before; the loader's data seeds useAsync so the
// hydrating render matches the server HTML.
type ReleaseLoad = {
  lang: LangCode;
  slug: string;
  release?: ReleaseDetail;
  performers?: Performer[];
  /** 404 / 410: not published, held, removed or unknown. */
  missing?: boolean;
  /** The API failed: render the loading state and let the browser retry. */
  unavailable?: boolean;
};

export async function loader({params, request}: LoaderFunctionArgs) {
  const lang = routeLang(params);
  const slug = params.slug ?? '';
  if (!lang) return null;
  const enc = encodeURIComponent(slug);
  try {
    const [release, cast] = await Promise.all([
      serverApi<ReleaseDetail>(`releases/${enc}`, {lang, signal: request.signal}),
      serverApi<{items: Performer[] | null}>(`releases/${enc}/performers`, {lang, signal: request.signal}).catch(() => ({items: []})),
    ]);
    // Merged release: the API followed the 301; send crawlers to the surviving URL.
    if (release.slug && release.slug !== slug) {
      throw redirect(localizePath(`/releases/${encodeURIComponent(release.slug)}`, lang) + new URL(request.url).search, 301);
    }
    return {lang, slug, release, performers: cast.items ?? []} satisfies ReleaseLoad;
  } catch (e) {
    if (e instanceof Response) throw e;
    if (isGone(e)) return data({lang, slug, missing: true} satisfies ReleaseLoad, {status: 404});
    console.error('release loader', slug, e);
    return data({lang, slug, unavailable: true} satisfies ReleaseLoad, {status: 503});
  }
}

export const clientLoader = () => null;

export const meta: MetaFunction<typeof loader> = ({data: d, params, matches}) => {
  const path = `/releases/${encodeURIComponent(params.slug ?? '')}`;
  const load = d as ReleaseLoad | null | undefined;
  const r = load?.release;
  if (!r) {
    const title = load?.missing ? t('title.named', {name: t('release.unavailableTitle')}) : t('title.release');
    return seoMeta({matches, path, title, noindex: true});
  }
  const performers = load?.performers ?? [];
  const names = performers.map(p => p.canonical_name).filter(Boolean);
  const facts = [r.maker_name, r.release_date && formatDate(r.release_date), formatRuntime(r.runtime_minutes), names.slice(0, 3).join('、')].filter(Boolean).join(' · ');
  const description = r.synopsis?.trim()
    ? clip(r.synopsis)
    : facts
      ? t('seo.releaseDescription', {title: r.canonical_title, facts})
      : t('seo.releaseDescriptionShort', {title: r.canonical_title});
  const {lang, origin} = rootMeta(matches);
  const url = pageUrl(origin, path, lang);
  const video = compact({
    '@context': 'https://schema.org',
    '@type': 'VideoObject',
    name: r.canonical_title,
    alternateName: r.title_ja && r.title_ja !== r.canonical_title ? r.title_ja : undefined,
    description,
    uploadDate: isoDate(r.release_date),
    duration: isoMinutes(r.runtime_minutes),
    url,
    inLanguage: lang,
    productionCompany: r.maker_name ? {'@type': 'Organization', name: r.maker_name} : undefined,
    director: r.director_name ? {'@type': 'Person', name: r.director_name} : undefined,
    keywords: (r.tags ?? []).map(tag => tag?.name).filter(Boolean).join(', ') || undefined,
    actor: performers.map(p => ({'@type': 'Person', name: p.canonical_name, url: pageUrl(origin, `/performers/${encodeURIComponent(p.slug)}`, lang)})),
  });
  return seoMeta({
    matches,
    path,
    title: t('title.named', {name: r.canonical_title}),
    description,
    ogType: 'video.movie',
    jsonLd: [
      video,
      breadcrumbs(matches, [
        [t('seo.breadcrumbHome'), '/'],
        [t('seo.breadcrumbDiscover'), '/discover'],
        [r.canonical_title, path],
      ]),
    ],
  });
};

// Video / VideoLight / MobileVideo.
//
// Omitted for lack of backend (never filled with sample data):
// TODO(tech plan 3.13): 赞 / 踩, 评论 (V1.1).
// 稍后观看 toggles here; ?list=later follows the 全部播放 / 随机播放 queue.
// 自动连播 (PRD 5.4, settings.autoplay, default on): when a video ends, the
// next one (the queue's next, else — also after the queue's last item — the
// first 接下来播放 work) starts after a 10 s countdown that can be cancelled;
// it reports autoplay_next.
// 接下来播放: GET /api/recs?slot=related&release= with reason labels and
// 相关推荐 / 同演员 / 同系列 tabs (tabs only when the reason codes allow).
// Without the recs routes, or with no related rows, it falls back to the
// first performer's other works.
// 预约 shows for status 'upcoming', but GET /api/releases/{slug} only serves
// published works today, so upcoming works are reserved from the feed.
// TODO(PRD 15 AgeGate): gate playback with <AgeConfirm> once releases carry an
// age rating; the API has no rating field yet, so the gate is not mounted.
// TODO(tech plan 2.4 / 3.2 release_assets): 章节 · 截屏 strip.
// 片源 row and versions live in Player (GET play-config). Tags, director and
// product codes come from the catalog-filters release detail and are simply
// absent on older backends.
export default function ReleasePage() {
  const t = useT();
  const {slug = ''} = useParams();
  const navigate = useNavigate();
  const {status} = useAuth();
  const {contentLang} = useI18n();
  const loaded = useLoaderData<typeof loader>() as ReleaseLoad | null;
  const own = loaded && loaded.slug === slug ? loaded : null;
  const missingOnServer = !!own?.missing;
  const seed = own?.release && own.lang === contentLang ? ([own.release, own.performers ?? []] as [ReleaseDetail, Performer[]]) : undefined;
  const detail = useAsync(signal => Promise.all([getRelease(slug, signal), getReleasePerformers(slug, signal)]), [slug], !missingOnServer, seed);
  const history = useAsync(signal => getWatchHistory(signal), [status], status === 'user');
  const mobile = useMediaQuery('(max-width: 767px)');
  // Hover cards need a real hover; phones and tablets (any width) get the sheet.
  const canHover = useMediaQuery('(hover: hover) and (pointer: fine)');
  const useSheet = mobile || !canHover;
  const [theater, setTheater] = useState(false);
  const [sheetPerformer, setSheetPerformer] = useState<Performer | null>(null);
  const [params] = useSearchParams();
  const listName = params.get('list');
  // The queue lives in this browser (sessionStorage): read after hydration.
  const [queue, setQueue] = useState<ReturnType<typeof readQueue>>(null);
  useEffect(() => setQueue(listName === 'later' ? readQueue('later') : null), [listName]);
  const queueIndex = queue ? queue.slugs.indexOf(slug) : -1;
  const nextSlug = queue && queueIndex >= 0 ? queue.slugs[queueIndex + 1] : undefined;
  const prefs = usePlayerPrefs();
  const location = useLocation();
  const linkState = location.state as {autoplay?: unknown; reason?: unknown} | null;
  const autoStart = linkState?.autoplay === true;
  const entryReason = typeof linkState?.reason === 'string' ? linkState.reason : undefined;
  const [ended, setEnded] = useState(false);
  const [related, setRelated] = useState<NextUp | null>(null);
  const [endItems, setEndItems] = useState<EndItem[]>([]);
  const queueNext = useAsync(signal => getRelease(nextSlug ?? '', signal), [nextSlug], !!nextSlug && ended);
  const next = nextSlug
    ? queueNext.data
      ? {id: queueNext.data.id, slug: queueNext.data.slug, title: queueNext.data.canonical_title, query: '?list=later'}
      : null
    : related
      ? {...related, query: ''}
      : null;

  useEffect(() => {
    setSheetPerformer(null);
    setEnded(false);
    setRelated(null);
    setEndItems([]);
  }, [slug]);

  const release = detail.data?.[0];
  const performers = detail.data?.[1] ?? [];
  const resumeAt = history.data?.find(item => item.slug === slug)?.progress_seconds;

  useCanonicalSlug('releases', slug, release?.slug);

  useEffect(() => {
    if (release) document.title = t('title.named', {name: release.canonical_title});
  }, [release]);

  const back = () => {
    if (window.history.length > 1) navigate(-1);
    else navigate('/');
  };

  if (detail.error || missingOnServer) {
    const missing = missingOnServer || (detail.error instanceof ApiError && detail.error.status === 404);
    return (
      <div className="page">
        {missing ? (
          <EmptyState
            icon="film"
            title={t('release.unavailableTitle')}
            body={t('release.unavailableBody')}
            actions={[{to: '/', label: t('error.backHome')}, {to: '/discover?tab=new', label: t('release.seeNew'), primary: false}]}
          />
        ) : (
          <ErrorBanner error={detail.error!} onRetry={detail.reload} />
        )}
      </div>
    );
  }

  return (
    <div className="video-page" data-theater={theater && !mobile ? 'true' : 'false'}>
      <div className="video-main">
        {/* PlayerHost keeps this instance playing in the mobile mini bar after leaving. */}
        <HostedPlayer
          component={Player}
          slug={slug}
          releaseId={release?.id}
          title={release?.canonical_title ?? ''}
          resumeAt={resumeAt}
          onBack={back}
          theater={theater}
          onToggleTheater={() => setTheater(on => !on)}
          autoStart={autoStart}
          entryReason={entryReason}
          onEnded={() => setEnded(true)}
          onReplay={() => setEnded(false)}
          related={endItems}
          overlay={
            ended && prefs.autoplay && next && release ? (
              <AutoplayNext
                key={next.slug}
                fromId={release.id}
                next={next}
                onGo={() => {
                  markAutoplayNavigation();
                  setEnded(false);
                  navigate(`/releases/${encodeURIComponent(next.slug)}${next.query}`, {state: {autoplay: true, reason: 'reason' in next ? next.reason : undefined}});
                }}
                onCancel={() => setEnded(false)}
              />
            ) : null
          }
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
                  {[release.release_date && t('release.releasedOn', {date: formatDate(release.release_date)}), formatRuntime(release.runtime_minutes)].filter(Boolean).join(' · ')}
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

            <Community slug={release.slug} releaseId={release.id} watched={!!resumeAt && resumeAt > 0} />

            {performers.length > 0 && (
              <section className="cast">
                <SectionHeader title={t('release.cast')} sub={useSheet ? t('release.castTapHint') : t('release.castHoverHint')} />
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

      <aside className="video-aside" aria-label={t('release.upNext')}>
        {queue && queueIndex >= 0 && <QueuePanel slugs={queue.slugs} index={queueIndex} />}
        {release && <UpNext slug={slug} performer={performers[0]} onFirst={setRelated} onList={setEndItems} />}
      </aside>
    </div>
  );
}

// release_identifiers.identifier_type values written by the ingest
// (store.SaveCatalogRecord); any other type shows under 编号.
const IDENTIFIER_LABELS: Record<string, MessageKey> = {
  dvd_code: 'release.idDvdCode',
  content_id: 'release.idContentId',
};

function ReleaseMeta({release}: {release: ReleaseDetail}) {
  const t = useT();
  const rows: [string, ReactNode][] = [
    [t('release.metaDate'), formatDate(release.release_date)],
    [t('release.metaRuntime'), formatRuntime(release.runtime_minutes)],
    [
      t('release.metaMaker'),
      release.maker_name ? (
        <Link to={discoverHref({maker: release.maker_name})} className="meta-link">
          {release.maker_name}
        </Link>
      ) : undefined,
    ],
    [t('release.metaLabel'), release.label_name],
    [t('release.metaSeries'), release.series_name],
    [t('release.metaDirector'), release.director_name || undefined],
  ];
  // Product codes (catalog-filters backend); one row per identifier type.
  const byType = new Map<string, string[]>();
  for (const id of release.identifiers ?? []) {
    if (!id?.value) continue;
    const label = t(IDENTIFIER_LABELS[id.type] ?? 'release.idOther');
    byType.set(label, [...(byType.get(label) ?? []), id.value]);
  }
  for (const [label, values] of byType) rows.push([label, <span className="mono">{Array.from(new Set(values)).join(' · ')}</span>]);

  const present = rows.filter(([, v]) => v);
  const tags = (release.tags ?? []).filter(tag => tag?.slug && tag.name);
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
        <div className="chip-row chip-row-wrap release-tags" aria-label={t('release.tags')}>
          {tags.map(tag => (
            <Link key={tag.slug} to={discoverHref({tag: tag.slug})} className="chip chip-tag">
              #{tag.name}
            </Link>
          ))}
        </div>
      )}
    </section>
  );
}

function CastMember({performer, releaseDate, onOpen}: {performer: Performer; releaseDate?: string; onOpen?: () => void}) {
  const t = useT();
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
      {age !== null && <span className="age-pill">{t('release.age', {age})}</span>}
    </Link>
  );
}

type NextUp = {id: number; slug: string; title: string; reason?: string};

/** 接下来播放: the related recs, else the same-performer list. */
function UpNext({slug, performer, onFirst, onList}: {
  slug: string;
  performer?: Performer;
  onFirst: (next: NextUp | null) => void;
  /** The first six, for the player's end screen. */
  onList: (items: EndItem[]) => void;
}) {
  const recs = useAsync(signal => getRecs('related', {release: slug, limit: 20}, signal), [slug]);
  const items = (recs.data?.items ?? []).filter(it => it.slug !== slug);
  if (recs.loading) return null;
  if (recs.error || items.length === 0) return performer ? <SamePerformer performer={performer} currentSlug={slug} onFirst={onFirst} onList={onList} /> : null;
  return <RelatedList items={items} onFirst={onFirst} onList={onList} />;
}

/** Reports the end-screen list whenever its slugs change. */
function useEndList(list: EndItem[], onList?: (items: EndItem[]) => void) {
  const key = list.map(i => i.slug).join(',');
  const latest = useRef(list);
  latest.current = list;
  useEffect(() => {
    onList?.(latest.current);
  }, [key, onList]);
}

const RELATED_TABS: {key: RelatedTab; label: MessageKey}[] = [
  {key: 'all', label: 'related.all'},
  {key: 'performer', label: 'related.performer'},
  {key: 'series', label: 'related.series'},
];

function RelatedList({items, onFirst, onList}: {items: RecItem[]; onFirst: (next: NextUp | null) => void; onList?: (items: EndItem[]) => void}) {
  const t = useT();
  useEndList(
    items.slice(0, 6).map(it => ({
      slug: it.slug,
      title: recTitle(it),
      reasonCode: it.reason.code,
      badge: it.runtime_minutes ? t('card.minutes', {n: it.runtime_minutes}) : undefined,
    })),
    onList,
  );
  const [tab, setTab] = useState<RelatedTab>('all');
  const tabs = RELATED_TABS.filter(x => items.some(it => inRelatedTab(it, x.key)));
  const active = tabs.some(x => x.key === tab) ? tab : 'all';
  const shown = items.filter(it => inRelatedTab(it, active));
  const first = items[0];
  useEffect(() => {
    onFirst(first ? {id: first.id, slug: first.slug, title: recTitle(first), reason: first.reason.code} : null);
  }, [first, onFirst]);
  return (
    <section className="aside-section related-list">
      <SectionHeader title={t('release.upNext')} />
      {tabs.length > 1 && (
        <div className="chip-row related-tabs" role="tablist" aria-label={t('related.tabs')}>
          {tabs.map(x => (
            <button
              key={x.key}
              type="button"
              role="tab"
              aria-selected={x.key === active}
              className={x.key === active ? 'chip chip-on' : 'chip'}
              onClick={() => setTab(x.key)}
            >
              {t(x.label)}
            </button>
          ))}
        </div>
      )}
      <div className="row-list" role={tabs.length > 1 ? 'tabpanel' : undefined}>
        {shown.slice(0, 12).map((it, i) => (
          <ReleaseRow
            key={it.id}
            slug={it.slug}
            title={recTitle(it)}
            badge={it.runtime_minutes ? t('card.minutes', {n: it.runtime_minutes}) : undefined}
            meta={performerName(it.performer) || it.maker_name}
            reason={reasonText(it.reason, it)}
            reasonCode={it.reason.code}
            slot={`watch:related:${active}:r${i + 1}`}
          />
        ))}
      </div>
    </section>
  );
}

function SamePerformer({performer, currentSlug, onFirst, onList}: {
  performer: Performer;
  currentSlug: string;
  /** The autoplay candidate (first work in the list). */
  onFirst?: (next: NextUp | null) => void;
  onList?: (items: EndItem[]) => void;
}) {
  const t = useT();
  const works = useAsync(signal => getPerformerReleases(performer.slug, 12, signal), [performer.slug]);
  const list = (works.data ?? []).filter(r => r.slug !== currentSlug).slice(0, 10);
  useEndList(
    list.slice(0, 6).map(r => ({slug: r.slug, title: r.canonical_title, badge: r.runtime_minutes ? t('card.minutes', {n: r.runtime_minutes}) : undefined})),
    onList,
  );
  const first = list[0];
  useEffect(() => {
    onFirst?.(first ? {id: first.id, slug: first.slug, title: first.canonical_title} : null);
  }, [first, onFirst]);
  if (works.error) return null;
  if (!works.loading && list.length === 0) return null;
  return (
    <section className="aside-section">
      <SectionHeader title={t('release.upNext')} sub={t('release.samePerformer', {name: performer.canonical_name})} />
      {works.slow ? (
        <CardGridSkeleton count={4} variant="row" />
      ) : (
        <div className="row-list">
          {list.map(r => (
            <ReleaseRow
              key={r.id}
              slug={r.slug}
              title={r.canonical_title}
              badge={r.runtime_minutes ? t('card.minutes', {n: r.runtime_minutes}) : undefined}
              meta={t('release.samePerformer', {name: performer.canonical_name})}
              sub={formatDate(r.release_date)}
            />
          ))}
        </div>
      )}
    </section>
  );
}

function FavoriteButton({slug}: {slug: string}) {
  const t = useT();
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
      setError(cause instanceof Error ? cause.message : t('release.favoriteFailed'));
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <button type="button" className={isSaved ? 'pill-btn is-on' : 'pill-btn'} aria-pressed={isSaved} disabled={busy} onClick={toggle}>
        <Icon name="favorites" size={18} filled={isSaved} />
        {isSaved ? t('release.favorited') : t('release.favorite')}
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
  const t = useT();
  const [message, setMessage] = useState('');
  const share = async () => {
    const url = window.location.href;
    try {
      if (navigator.share) {
        await navigator.share({title, url});
        return;
      }
      await navigator.clipboard.writeText(url);
      setMessage(t('release.linkCopied'));
    } catch {
      setMessage('');
    }
    window.setTimeout(() => setMessage(''), 2000);
  };
  return (
    <>
      <button type="button" className="pill-btn" onClick={share}>
        <Icon name="share" size={18} />
        {t('release.share')}
      </button>
      <span className="sr-status" role="status">
        {message}
      </span>
    </>
  );
}

const AUTOPLAY_SECONDS = 10;

/** PRD 5.4 / 14.2: 10 s countdown to the next video, with 立即播放 and 取消. */
function AutoplayNext({fromId, next, onGo, onCancel}: {
  fromId: number;
  next: {id: number; slug: string; title: string};
  onGo: () => void;
  onCancel: () => void;
}) {
  const t = useT();
  const [left, setLeft] = useState(AUTOPLAY_SECONDS);
  const done = useRef(false);
  const report = (cancelled: boolean, remaining: number) => {
    if (done.current) return;
    done.current = true;
    track('autoplay_next', {from_release_id: fromId, to_release_id: next.id, cancelled, countdown_left_s: Math.max(0, remaining)});
  };
  const reportRef = useRef(report);
  reportRef.current = report;
  const goRef = useRef(onGo);
  goRef.current = onGo;

  useEffect(() => {
    const timer = window.setInterval(() => setLeft(l => l - 1), 1000);
    return () => window.clearInterval(timer);
  }, []);
  useEffect(() => {
    if (left > 0) return;
    reportRef.current(false, 0);
    goRef.current();
  }, [left]);

  return (
    <div className="autoplay-next" role="dialog" aria-label={t('autoplay.label')} aria-live="polite">
      <span className="autoplay-next-label">{t('autoplay.countdown', {s: left})}</span>
      <strong className="autoplay-next-title">{next.title}</strong>
      <div className="autoplay-next-actions">
        <button
          type="button"
          className="btn btn-primary btn-sm"
          onClick={() => {
            report(false, left);
            onGo();
          }}
        >
          {t('autoplay.now')}
        </button>
        <button
          type="button"
          className="btn btn-on-dark btn-sm"
          onClick={() => {
            report(true, left);
            onCancel();
          }}
        >
          {t('autoplay.cancel')}
        </button>
      </div>
    </div>
  );
}

/** 稍后观看 queue (全部播放 / 随机播放): position, 下一个, and the rest of the list. */
function QueuePanel({slugs, index}: {slugs: string[]; index: number}) {
  const t = useT();
  const later = useAsync(signal => getWatchLater(signal), []);
  const titles = new Map((later.data ?? []).map(i => [i.slug, i.title]));
  const rest = slugs.slice(index + 1);
  return (
    <section className="aside-section queue-panel" aria-label={t('release.queue')}>
      <SectionHeader title={t('nav.later')} sub={`${index + 1} / ${slugs.length}`} />
      {rest.length === 0 ? (
        <p className="muted">{t('release.queueLast')}</p>
      ) : (
        <div className="row-list">
          {rest.slice(0, 8).map((s, i) => (
            <ReleaseRow key={s} slug={s} query="?list=later" title={titles.get(s) ?? s} meta={i === 0 ? t('release.queueNext') : t('release.queueNth', {n: index + i + 2})} />
          ))}
        </div>
      )}
      <Link to="/later" className="text-btn">
        {t('release.queueManage')}
      </Link>
    </section>
  );
}
