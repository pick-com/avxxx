import {Link} from '../lib/nav';
import {useT} from '../lib/i18n';
import {ADHOC_QUEUE, queueStartPath, type Queue} from '../lib/queue';
import {useAdhocQueue} from '../lib/useQueue';
import {ReleaseRow, SectionHeader} from './Cards';
import {Icon} from './Icon';

// 播放队列 panel in the release page's up-next column (YouTube's queue):
// count or position, 播放队列 when this video is not in it, and per row
// 上移 / 下移 / 移除, plus 清空. Rows link with ?list=queue.

export function PlayQueuePanel({queue, slug, playing, upNext}: {
  queue: Queue;
  slug: string;
  /** This video is playing through the queue (?list=queue). */
  playing: boolean;
  /** The queue starts when this video ends (autoplay-next / next track). */
  upNext: boolean;
}) {
  const t = useT();
  const {move, remove, clear} = useAdhocQueue();
  const {slugs} = queue;
  const index = playing ? slugs.indexOf(slug) : -1;
  const query = `?list=${ADHOC_QUEUE}`;
  return (
    <section className="aside-section queue-panel play-queue" aria-label={t('queue.title')}>
      <SectionHeader
        title={t('queue.title')}
        sub={index >= 0 ? `${index + 1} / ${slugs.length}` : t('queue.count', {n: slugs.length})}
        action={
          <button type="button" className="text-btn" onClick={clear}>
            {t('queue.clear')}
          </button>
        }
      />
      {index < 0 && (
        <p className="play-queue-start">
          {upNext && <span className="muted">{t('queue.playsNext')}</span>}
          <Link to={queueStartPath(ADHOC_QUEUE, slugs[0])} state={{autoplay: true}} className="text-btn">
            <Icon name="play" size={16} />
            {t('queue.playNow')}
          </Link>
        </p>
      )}
      <div className="row-list play-queue-list">
        {slugs.map((s, i) => {
          const title = queue.titles?.[s] ?? s;
          return (
            <ReleaseRow
              key={s}
              slug={s}
              query={query}
              title={title}
              meta={i === index ? t('queue.nowPlaying') : index >= 0 && i === index + 1 ? t('release.queueNext') : t('release.queueNth', {n: i + 1})}
            >
              <span className="play-queue-actions">
                <button type="button" className="icon-btn" aria-label={t('queue.moveUp', {title})} disabled={i === 0} onClick={() => move(s, -1)}>
                  <Icon name="chevronDown" size={16} />
                </button>
                <button type="button" className="icon-btn" aria-label={t('queue.moveDown', {title})} disabled={i === slugs.length - 1} onClick={() => move(s, 1)}>
                  <Icon name="chevronDown" size={16} />
                </button>
                <button type="button" className="icon-btn" aria-label={t('queue.remove', {title})} onClick={() => remove(s)}>
                  <Icon name="close" size={16} />
                </button>
              </span>
            </ReleaseRow>
          );
        })}
      </div>
    </section>
  );
}
