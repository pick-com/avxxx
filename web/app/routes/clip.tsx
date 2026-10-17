import {useState} from 'react';
import {data, useLoaderData, useParams, type LoaderFunctionArgs, type MetaFunction} from 'react-router';
import {absoluteImageUrl} from '../components/Cards';
import {Icon} from '../components/Icon';
import {Player} from '../components/Player';
import {HostedPlayer} from '../components/PlayerHost';
import {ReportPicker} from '../components/ReportPicker';
import {useRequireAuth} from '../components/AuthPrompt';
import {BlockSkeleton, EmptyState, ErrorBanner} from '../components/States';
import {ApiError, type ReleaseDetail} from '../lib/api';
import {isGated} from '../lib/ageGate.server';
import {isGone, serverApi} from '../lib/api.server';
import {useAuth} from '../lib/auth';
import {getClip, reportClip, type Clip} from '../lib/clips';
import {formatClock} from '../lib/format';
import {t, useT} from '../lib/i18n';
import {routeLang} from '../lib/langs';
import {Link} from '../lib/nav';
import {rootMeta, seoMeta, type SeoHandle} from '../lib/seo';
import {useAsync} from '../lib/useAsync';
import type {ShellHandle} from './shell';

export const handle: ShellHandle & SeoHandle = {immersive: true, seo: true};

// 片段 page (/clips/{id}; internal/api/clips.go): plays the clip's release
// from the clip start and loops the segment (or stops once at its end), with
// a link to the full video. Server rendered from GET /api/clips/{id} plus the
// release (cover, for og:image); behind the 18+ gate the loader keeps only
// what the head needs, as the release loader does (#111). Clips are
// noindex: they are shared links, the release page is the canonical one.

type ClipLoad = {id: string; clip?: Clip; cover?: string; missing?: boolean; unavailable?: boolean};

export async function loader({params, request}: LoaderFunctionArgs) {
  const lang = routeLang(params);
  const id = params.id ?? '';
  if (!lang) return null;
  const headers = {'X-Robots-Tag': 'noindex'};
  try {
    const clip = await serverApi<Clip>(`clips/${encodeURIComponent(id)}`, {signal: request.signal});
    const release = await serverApi<ReleaseDetail>(`releases/${encodeURIComponent(clip.release_slug)}`, {lang, signal: request.signal}).catch(() => null);
    const cover = release?.cover?.url;
    // 18+ gate: the head needs the clip's titles and the cover, nothing else.
    if (isGated(request)) return data({id, clip: {...clip, author_name: undefined}, cover} satisfies ClipLoad, {headers});
    return data({id, clip, cover} satisfies ClipLoad, {headers});
  } catch (e) {
    if (isGone(e)) return data({id, missing: true} satisfies ClipLoad, {status: 404, headers});
    console.error('clip loader', e instanceof Error ? e.message : e);
    return data({id, unavailable: true} satisfies ClipLoad, {status: 503, headers});
  }
}

export const clientLoader = () => null;

export function headers({loaderHeaders}: {loaderHeaders: Headers}) {
  const robots = loaderHeaders.get('X-Robots-Tag');
  return robots ? {'X-Robots-Tag': robots} : {};
}

const span = (c: Clip) => `${formatClock(Math.floor(c.start_ms / 1000))} – ${formatClock(Math.ceil(c.end_ms / 1000))}`;
const clipName = (c: Clip) => c.title || t('clip.untitled', {title: c.release_title});

export const meta: MetaFunction<typeof loader> = ({data: d, params, matches}) => {
  const path = `/clips/${encodeURIComponent(params.id ?? '')}`;
  const load = d as ClipLoad | null | undefined;
  const c = load?.clip;
  if (!c) return seoMeta({matches, path, title: t('title.named', {name: t('clip.notFound')}), noindex: true});
  const {origin} = rootMeta(matches);
  const description = t('clip.description', {title: c.release_title, span: span(c)});
  const out = seoMeta({matches, path, title: t('title.named', {name: clipName(c)}), description, noindex: true});
  // noindex pages still unfurl in chats: Open Graph for the shared link.
  const image = load?.cover ? absoluteImageUrl(origin, load.cover) : null;
  out.push(
    {property: 'og:type', content: 'video.other'},
    {property: 'og:title', content: clipName(c)},
    {property: 'og:description', content: description},
    {name: 'twitter:card', content: image ? 'summary_large_image' : 'summary'},
    {name: 'twitter:title', content: clipName(c)},
  );
  if (image) out.push({property: 'og:image', content: image}, {name: 'twitter:image', content: image});
  return out;
};

export default function ClipPage() {
  const t = useT();
  const {id = ''} = useParams();
  const {status} = useAuth();
  const requireAuth = useRequireAuth();
  const loaded = useLoaderData<typeof loader>() as ClipLoad | null;
  const own = loaded && loaded.id === id ? loaded : null;
  const missingOnServer = !!own?.missing;
  // Refetched in the browser: `mine` needs the viewer's token.
  const clip = useAsync(signal => getClip(id, signal), [id, status], !missingOnServer && status !== 'unknown', own?.clip);
  const [loop, setLoop] = useState(true);
  const [reporting, setReporting] = useState(false);
  const [reported, setReported] = useState(false);

  if (missingOnServer || clip.error) {
    const missing = missingOnServer || (clip.error instanceof ApiError && clip.error.status === 404);
    return (
      <div className="page">
        {missing ? (
          <EmptyState icon="film" title={t('clip.notFound')} body={t('clip.notFoundBody')} actions={[{to: '/', label: t('common.goHome')}]} />
        ) : (
          <ErrorBanner error={clip.error!} onRetry={clip.reload} />
        )}
      </div>
    );
  }
  const c = clip.data;
  if (!c) return <div className="page">{clip.slow && <BlockSkeleton lines={4} />}</div>;
  const start = c.start_ms / 1000;
  const end = c.end_ms / 1000;
  const full = `/releases/${encodeURIComponent(c.release_slug)}?t=${Math.floor(start)}`;
  return (
    <div className="video-page clip-page">
      <div className="video-main">
        <HostedPlayer
          component={Player}
          slug={c.release_slug}
          title={c.release_title}
          startAt={start}
          segment={{start, end, loop}}
          autoStart={true}
          poster={own?.cover}
        />
        <div className="video-head">
          <h1 className="video-title">{clipName(c)}</h1>
          {c.mine && c.title_status === 'shadow' && <p className="field-hint">{t('clip.titleShadow')}</p>}
          {c.mine && c.title_status === 'removed' && <p className="field-hint">{t('clip.titleRemovedByMod')}</p>}
          <p className="muted clip-meta">
            {[t('clip.span', {span: span(c), n: Math.round(end - start)}), c.author_name && t('clip.by', {name: c.author_name})].filter(Boolean).join(' · ')}
          </p>
          <p className="clip-from">
            {t('clip.from')} <Link to={`/releases/${encodeURIComponent(c.release_slug)}`}>{c.release_title}</Link>
          </p>
          <div className="action-row">
            <Link className="btn btn-primary" to={full}>
              <Icon name="play" size={16} filled />
              {t('clip.watchFull')}
            </Link>
            <label className="review-check">
              <input type="checkbox" checked={loop} onChange={e => setLoop(e.target.checked)} />
              <span>{t('clip.loop')}</span>
            </label>
            {!c.mine && !reported && (
              <button type="button" className="btn btn-outline btn-sm" onClick={() => requireAuth('report') && setReporting(r => !r)}>
                {t('clip.report')}
              </button>
            )}
          </div>
          {reporting && !reported && (
            <ReportPicker
              onSubmit={async reason => {
                await reportClip(c.id, reason);
                setReported(true);
                setReporting(false);
              }}
              onCancel={() => setReporting(false)}
            />
          )}
          {reported && (
            <p className="muted" role="status">
              {t('report.thanks')}
            </p>
          )}
        </div>
      </div>
    </div>
  );
}
