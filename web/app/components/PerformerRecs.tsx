import {useEffect, useState} from 'react';
import {Link} from 'react-router';
import {useAuth} from '../lib/auth';
import {useT} from '../lib/i18n';
import {getPerformerRecs, performerName, reasonText, type PerformerRecItem} from '../lib/recs';
import {useAsync} from '../lib/useAsync';
import {Avatar, SectionHeader} from './Cards';
import {CardGridSkeleton} from './States';

// Performer recommendations (PRD 6.1 / 6.2, GET /api/recs):
//   SimilarPerformers      Actor 「相似演员」: slot=similar_performers, match % + one reason.
//   RecommendedPerformers  NavActors 「为你推荐的演员」 big cards with 换一批:
//                          slot=performers (signed in: from follows and history;
//                          visitors / no signal: the week's performer heat).
// Both render nothing when the backend has no recs routes or no rows.

const SIMILAR_LIMIT = 6;
const BATCH = 5;

export function SimilarPerformers({slug}: {slug: string}) {
  const t = useT();
  const list = useAsync(signal => getPerformerRecs('similar_performers', {performer: slug, limit: SIMILAR_LIMIT}, signal), [slug]);
  const items = list.data?.items ?? [];
  if (list.error || (!list.loading && items.length === 0)) return null;
  return (
    <section className="shelf similar-performers">
      <SectionHeader title={t('performer.similar')} />
      {list.loading ? (
        list.slow && <CardGridSkeleton count={SIMILAR_LIMIT} variant="avatar" />
      ) : (
        <div className="similar-grid">
          {items.map((p, i) => (
            <Link key={p.id} to={`/performers/${encodeURIComponent(p.slug)}`} className="similar-card" state={{slot: `actor:similar:c${i + 1}`}}>
              <Avatar seed={p.slug} name={performerName(p)} size={96} />
              <span className="similar-name">{performerName(p)}</span>
              {p.match_percent > 0 && <span className="match-pct">{t('performer.similarMatch', {n: p.match_percent})}</span>}
              <span className="card-reason">{reasonText(p.reason)}</span>
            </Link>
          ))}
        </div>
      )}
    </section>
  );
}

export function RecommendedPerformers() {
  const t = useT();
  const {status} = useAuth();
  // cursor of the batch being shown; null = first batch.
  const [cursor, setCursor] = useState<string | null>(null);
  useEffect(() => setCursor(null), [status]);
  const batch = useAsync(signal => getPerformerRecs('performers', {limit: BATCH, cursor}, signal), [cursor, status], status !== 'unknown');
  const [shown, setShown] = useState<{items: PerformerRecItem[]; next: string | null} | null>(null);
  useEffect(() => {
    if (batch.data) setShown({items: batch.data.items, next: batch.data.nextCursor});
  }, [batch.data]);
  useEffect(() => setShown(null), [status]);

  if (batch.error && !shown) return null;
  const items = shown?.items ?? [];
  if (!batch.loading && items.length === 0) return null;
  const hot = items.length > 0 && items.every(p => p.reason.code === 'hot_performer');
  // 换一批: the next page, or back to the first when the list is exhausted.
  const canShuffle = !!shown && (shown.next !== null || cursor !== null);
  return (
    <section className="shelf performer-recs">
      <SectionHeader
        title={t('actors.forYou')}
        sub={hot ? t('actors.hotSub') : t('actors.forYouSub')}
        action={
          canShuffle ? (
            <button type="button" className="btn btn-outline btn-sm" disabled={batch.loading} onClick={() => setCursor(shown?.next ?? null)}>
              {t('actors.shuffle')}
            </button>
          ) : undefined
        }
      />
      {!shown ? (
        batch.slow && <CardGridSkeleton count={BATCH} />
      ) : (
        <div className="performer-rec-grid" aria-busy={batch.loading}>
          {items.map((p, i) => (
            <Link key={p.id} to={`/performers/${encodeURIComponent(p.slug)}`} className="performer-rec-card" state={{slot: `actors:recs:c${i + 1}`}}>
              <span className="performer-rec-photo">
                <Avatar seed={p.slug} name={performerName(p)} size={88} />
                {p.match_percent > 0 && <span className="match-pill">{t('actors.match', {n: p.match_percent})}</span>}
                <span className="performer-rec-why">{reasonText(p.reason)}</span>
              </span>
              <span className="performer-rec-name">{performerName(p)}</span>
            </Link>
          ))}
        </div>
      )}
    </section>
  );
}
