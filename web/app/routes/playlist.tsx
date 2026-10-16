import {data, useLoaderData, useParams, type LoaderFunctionArgs, type MetaFunction} from 'react-router';
import {ReleaseRow} from '../components/Cards';
import {Icon} from '../components/Icon';
import {BlockSkeleton, EmptyState, ErrorBanner} from '../components/States';
import {ApiError, api, type PlaylistDetail} from '../lib/api';
import {isGated} from '../lib/ageGate.server';
import {isGone, serverApi} from '../lib/api.server';
import {formatDate, formatRuntime} from '../lib/format';
import {t, useT} from '../lib/i18n';
import {routeLang} from '../lib/langs';
import {useNavigate} from '../lib/nav';
import {playlistName, visibilityIcon, visibilityLabel} from '../lib/playlists';
import {playlistQueue, queueStartPath, startQueue} from '../lib/queue';
import {seoMeta, type SeoHandle} from '../lib/seo';
import {useAsync} from '../lib/useAsync';
import {shuffled} from '../lib/useReorder';
import '../styles/library.css';

export const handle: SeoHandle = {seo: true};

// Public read-only 收藏夹 page (/playlists/{share_token}; NavFavorites 分享).
// Server rendered from GET /api/playlists/{token}, which answers only for
// public and link-only playlists (private and unknown tokens are 404, so a
// private playlist is never readable by anyone but its owner). Link-only
// playlists are noindex (meta robots and X-Robots-Tag); public ones may be
// indexed. 全部播放 / 随机播放 start a playlist:<id> session queue the video
// page follows (lib/queue), in the owner's order.

type PlaylistLoad = {token: string; detail?: PlaylistDetail; missing?: boolean; unavailable?: boolean};

export async function loader({params, request}: LoaderFunctionArgs) {
  const token = params.token ?? '';
  if (!routeLang(params)) return null;
  try {
    const detail = await serverApi<PlaylistDetail>(`playlists/${encodeURIComponent(token)}`, {signal: request.signal});
    const headers: HeadersInit = detail.playlist.visibility === 'public' ? {} : {'X-Robots-Tag': 'noindex'};
    // 18+ gate: the head needs the playlist itself, not its items.
    if (isGated(request)) return data({token, detail: {playlist: detail.playlist, items: []}} satisfies PlaylistLoad, {headers});
    return data({token, detail} satisfies PlaylistLoad, {headers});
  } catch (e) {
    if (isGone(e)) return data({token, missing: true} satisfies PlaylistLoad, {status: 404, headers: {'X-Robots-Tag': 'noindex'}});
    console.error('playlist loader', e instanceof Error ? e.message : e);
    return data({token, unavailable: true} satisfies PlaylistLoad, {status: 503});
  }
}

export const clientLoader = () => null;

export function headers({loaderHeaders}: {loaderHeaders: Headers}) {
  const robots = loaderHeaders.get('X-Robots-Tag');
  return robots ? {'X-Robots-Tag': robots} : {};
}

export const meta: MetaFunction<typeof loader> = ({data: d, params, matches}) => {
  const path = `/playlists/${encodeURIComponent(params.token ?? '')}`;
  const p = (d as PlaylistLoad | null | undefined)?.detail?.playlist;
  if (!p) return seoMeta({matches, path, title: t('title.named', {name: t('playlists.notFound')}), noindex: true});
  const name = playlistName(p);
  const description = p.description || t('playlists.publicDescription', {name, n: p.item_count});
  return seoMeta({matches, path, title: t('title.named', {name}), description, noindex: p.visibility !== 'public'});
};

export default function PublicPlaylist() {
  const t = useT();
  const {token = ''} = useParams();
  const loaded = useLoaderData<typeof loader>() as PlaylistLoad | null;
  const own = loaded && loaded.token === token ? loaded : null;
  const missingOnServer = !!own?.missing;
  const navigate = useNavigate();
  const detail = useAsync(signal => api<PlaylistDetail>(`playlists/${encodeURIComponent(token)}`, {signal}), [token], !missingOnServer, own?.detail);

  if (missingOnServer || detail.error) {
    const missing = missingOnServer || (detail.error instanceof ApiError && detail.error.status === 404);
    return (
      <div className="page">
        {missing ? (
          <EmptyState icon="favorites" title={t('playlists.notFound')} body={t('playlists.notFoundBody')} actions={[{to: '/', label: t('common.goHome')}]} />
        ) : (
          <ErrorBanner error={detail.error!} onRetry={detail.reload} />
        )}
      </div>
    );
  }
  const d = detail.data;
  if (!d) return <div className="page narrow">{detail.slow && <BlockSkeleton lines={4} />}</div>;
  const p = d.playlist;
  const play = (order: PlaylistDetail['items']) => {
    if (order.length === 0) return;
    const name = playlistQueue(p.id);
    startQueue(
      name,
      order.map(i => i.slug),
      {title: playlistName(p), titles: Object.fromEntries(order.map(i => [i.slug, i.title])), manage: `/playlists/${encodeURIComponent(token)}`},
    );
    navigate(queueStartPath(name, order[0].slug));
  };
  return (
    <div className="page narrow lib-page">
      <header className="pl-public-head">
        <h1 className="page-title">{playlistName(p)}</h1>
        <p className="muted pl-card-meta">
          <Icon name={visibilityIcon[p.visibility]} size={14} />
          {[t(visibilityLabel[p.visibility]), p.owner_name && t('playlists.by', {name: p.owner_name}), t('playlists.count', {n: p.item_count})].filter(Boolean).join(' · ')}
        </p>
        {p.description && <p className="pl-desc">{p.description}</p>}
        {d.items.length > 0 && (
          <div className="action-row">
            <button type="button" className="btn btn-primary" onClick={() => play(d.items)}>
              <Icon name="play" size={16} filled />
              {t('later.playAll')}
            </button>
            <button type="button" className="btn btn-secondary" onClick={() => play(shuffled(d.items))}>
              <Icon name="shuffle" size={16} />
              {t('later.shuffle')}
            </button>
          </div>
        )}
      </header>
      {d.items.length === 0 ? (
        <EmptyState icon="favorites" title={t('playlists.publicEmpty')} />
      ) : (
        <div className="row-list">
          {d.items.map(item => (
            <ReleaseRow
              key={item.slug}
              slug={item.slug}
              title={item.title}
              meta={formatRuntime(item.runtime_minutes) || undefined}
              sub={t('playlists.addedOn', {date: formatDate(item.added_at)})}
            />
          ))}
        </div>
      )}
    </div>
  );
}
