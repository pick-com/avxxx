import {useEffect, useRef} from 'react';
import {Link} from '../lib/nav';
import {formatRuntime} from '../lib/format';
import {useT} from '../lib/i18n';
import {SectionHeader} from './Cards';
import {Sheet} from './Sheet';

// 选集 (MobilePlayerFull; PRD 5.1 章节、选集) and the desktop 同系列 panel.
//
// The release page loads the series' releases in episode order
// (lib/series.ts listSeriesEpisodes) and hands them to the Player as
// `episodes`. Picking one navigates to that release page like 自动连播
// does, so PlayerHost keeps its single player instance (the mini bar and
// fullscreen survive) and a play queue (?list=) is kept when the episode
// is in it.

export type Episode = {slug: string; title: string; episode?: number | null; runtimeMinutes?: number};

export type PlayerEpisodes = {
  seriesName: string;
  seriesSlug: string;
  /** In episode order. */
  items: Episode[];
  /** The playing release. */
  current: string;
  onPick: (slug: string) => void;
};

function EpisodeLabel({e, n}: {e: Episode; n: number}) {
  const t = useT();
  return <span className="ep-num">{typeof e.episode === 'number' ? t('series.episode', {n: e.episode}) : `${n}`}</span>;
}

/** The 选集 drawer over the player (a Sheet; inside the fullscreen shell when container is set). */
export function EpisodesSheet({open, onClose, episodes, container}: {
  open: boolean;
  onClose: () => void;
  episodes: PlayerEpisodes;
  container?: HTMLElement | null;
}) {
  const t = useT();
  const listRef = useRef<HTMLOListElement>(null);
  const index = episodes.items.findIndex(e => e.slug === episodes.current);

  // Bring the playing episode into view when the drawer opens.
  useEffect(() => {
    if (!open) return;
    const frame = window.requestAnimationFrame(() => listRef.current?.querySelector('[aria-current="true"]')?.scrollIntoView({block: 'center'}));
    return () => window.cancelAnimationFrame(frame);
  }, [open]);

  return (
    <Sheet
      open={open}
      onClose={onClose}
      label={t('series.episodes')}
      container={container}
      className="sheet-episodes"
      header={
        <div className="ep-sheet-head">
          <h2 className="sheet-title">{t('series.episodesTitle', {name: episodes.seriesName})}</h2>
          <span className="muted">
            {index >= 0 ? t('series.episodePosition', {i: index + 1, n: episodes.items.length}) : t('series.works', {n: episodes.items.length})}
          </span>
        </div>
      }
    >
      <ol className="ep-list" ref={listRef}>
        {episodes.items.map((e, i) => {
          const current = e.slug === episodes.current;
          return (
            <li key={e.slug}>
              <button
                type="button"
                className="ep-item"
                aria-current={current ? 'true' : undefined}
                onClick={() => {
                  onClose();
                  if (!current) episodes.onPick(e.slug);
                }}
              >
                <EpisodeLabel e={e} n={i + 1} />
                <span className="ep-title">{e.title}</span>
                {current ? <span className="ep-now">{t('series.nowPlaying')}</span> : e.runtimeMinutes ? <span className="ep-meta">{formatRuntime(e.runtimeMinutes)}</span> : null}
              </button>
            </li>
          );
        })}
      </ol>
    </Sheet>
  );
}

/** How many episodes the compact panel shows around the playing one. */
const PANEL_BEFORE = 1;
const PANEL_SIZE = 5;

/**
 * Desktop 同系列 panel in the up-next column: the series in episode order,
 * a window around the playing episode, and 查看系列全部作品. The 同系列 tab of
 * 接下来播放 lists series works the recommender picked (unordered, without
 * the current position, absent when recs fall back), so this does not repeat it.
 */
export function SeriesEpisodesPanel({episodes, query = () => ''}: {episodes: PlayerEpisodes; query?: (slug: string) => string}) {
  const t = useT();
  const {items} = episodes;
  const index = items.findIndex(e => e.slug === episodes.current);
  const start = Math.max(0, Math.min(index - PANEL_BEFORE, items.length - PANEL_SIZE));
  const shown = items.slice(start, start + PANEL_SIZE);
  return (
    <section className="aside-section ep-panel" aria-label={t('series.sameSeries')}>
      <SectionHeader
        title={t('series.sameSeries')}
        sub={index >= 0 ? t('series.episodePosition', {i: index + 1, n: items.length}) : t('series.works', {n: items.length})}
      />
      <p className="ep-panel-name">{episodes.seriesName}</p>
      <ol className="ep-list ep-list-compact">
        {shown.map((e, i) => {
          const current = e.slug === episodes.current;
          return (
            <li key={e.slug}>
              {current ? (
                <span className="ep-item" aria-current="page">
                  <EpisodeLabel e={e} n={start + i + 1} />
                  <span className="ep-title">{e.title}</span>
                  <span className="ep-now">{t('series.nowPlaying')}</span>
                </span>
              ) : (
                <Link className="ep-item" to={`/releases/${encodeURIComponent(e.slug)}${query(e.slug)}`} state={{reason: 'same_series'}}>
                  <EpisodeLabel e={e} n={start + i + 1} />
                  <span className="ep-title">{e.title}</span>
                  {e.runtimeMinutes ? <span className="ep-meta">{formatRuntime(e.runtimeMinutes)}</span> : null}
                </Link>
              )}
            </li>
          );
        })}
      </ol>
      <Link to={`/series/${encodeURIComponent(episodes.seriesSlug)}`} className="text-btn">
        {t('series.viewAll')}
      </Link>
    </section>
  );
}
