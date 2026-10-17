import {useEffect, useId, useState} from 'react';
import {isAbort} from '../lib/api';
import {track} from '../lib/analytics';
import {useAuth} from '../lib/auth';
import {
  deleteReview,
  getRating,
  getReviews,
  likeReview,
  postErrorMessage,
  postReview,
  putRating,
  reportReview,
  type RatingSummary,
  type ReportReason,
  type Review,
  type ReviewSort,
} from '../lib/community';
import {formatCount, formatDate} from '../lib/format';
import {useT} from '../lib/i18n';
import type {MessageKey} from '../lib/messages.zh-CN';
import {useAsync} from '../lib/useAsync';
import {useMediaQuery} from '../lib/useMediaQuery';
import {useRequireAuth} from './AuthPrompt';
import {Icon} from './Icon';
import {ReportPicker} from './ReportPicker';
import {Sheet} from './Sheet';
import {ErrorBanner} from './States';
import {TimestampText} from './Timestamps';

// 评分与短评 (PRD 19 · Ratings / MobileRatings; internal/api/community.go).
//
// Ratings: 1–5 stars, shown on a 10-point scale; the score stays hidden
// (评分人数不足) under 50 votes. Visitors vote per device (X-Device-ID).
// Reviews: ≤ 140 characters, 含剧透 folds the text, sorted 最有用 / 最新 /
// 只看看完的, one like per person, the author's watch progress as a badge,
// 举报. Phones show a one-line summary that opens the rating half sheet.

export const REVIEW_MAX = 140;
const PAGE = 20;
const runeLength = (s: string) => Array.from(s).length;

function Stars({value, size = 16}: {value: number; size?: number}) {
  // value: 0-5 (may be fractional); drawn as five stars, filled to the value.
  return (
    <span className="stars" aria-hidden="true">
      {[0, 1, 2, 3, 4].map(i => (
        <span key={i} className="star" style={{width: size, height: size}}>
          <Icon name="star" size={size} />
          <span className="star-fill" style={{width: `${Math.max(0, Math.min(1, value - i)) * 100}%`}}>
            <Icon name="star" size={size} filled />
          </span>
        </span>
      ))}
    </span>
  );
}

function ScoreBlock({r}: {r: RatingSummary}) {
  const t = useT();
  if (r.score === null || r.insufficient) {
    return (
      <div className="rating-score rating-score-hidden">
        <strong>{t('rating.insufficient')}</strong>
        <span className="muted">{t('rating.countHidden', {n: formatCount(r.count)})}</span>
      </div>
    );
  }
  return (
    <div className="rating-score">
      <strong className="rating-number">{r.score.toFixed(1)}</strong>
      <span className="rating-scale">/ 10</span>
      <span className="rating-meta">
        <Stars value={r.score / 2} />
        <span className="muted">{t('rating.count', {n: formatCount(r.count)})}</span>
      </span>
    </div>
  );
}

function Distribution({r}: {r: RatingSummary}) {
  const t = useT();
  const dist = r.distribution;
  if (!dist || dist.length !== 5 || r.score === null) return null;
  const total = dist.reduce((a, b) => a + b, 0) || 1;
  return (
    <ul className="rating-dist" aria-label={t('rating.distribution')}>
      {[4, 3, 2, 1, 0].map(i => {
        const pct = Math.round((dist[i] / total) * 100);
        return (
          <li key={i}>
            <span className="rating-dist-label">{t('rating.stars', {n: i + 1})}</span>
            <span className="rating-dist-bar" aria-hidden="true">
              <span style={{width: `${pct}%`}} />
            </span>
            <span className="rating-dist-pct">{pct}%</span>
          </li>
        );
      })}
    </ul>
  );
}

/** The rating panel: score, distribution and the viewer's own stars. */
function RatingPanel({slug, releaseId, watched, rating, onRated}: {
  slug: string;
  releaseId: number;
  watched: boolean;
  rating: RatingSummary;
  onRated: (r: RatingSummary) => void;
}) {
  const t = useT();
  const {status} = useAuth();
  const labelId = useId();
  const [hover, setHover] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [saved, setSaved] = useState('');
  const mine = rating.my_score ?? 0;

  const rate = async (n: number) => {
    if (busy) return;
    setBusy(true);
    setError('');
    setSaved('');
    try {
      const next = await putRating(slug, n);
      onRated(next);
      setSaved(t('rating.saved', {n: n * 2}));
      track('rating_submit', {release_id: releaseId, score: n * 2, watched});
    } catch (cause) {
      setError(postErrorMessage(cause));
    } finally {
      setBusy(false);
    }
  };

  const shown = hover || mine;
  return (
    <div className="rating-panel">
      <div className="rating-top">
        <ScoreBlock r={rating} />
        <Distribution r={rating} />
      </div>
      <div className="rating-mine">
        <span id={labelId} className="rating-mine-label">
          {mine ? t('rating.yours', {n: mine * 2}) : t('rating.rateIt')}
        </span>
        <div className="rating-stars-input" role="radiogroup" aria-labelledby={labelId} onMouseLeave={() => setHover(0)}>
          {[1, 2, 3, 4, 5].map(n => (
            <button
              key={n}
              type="button"
              role="radio"
              aria-checked={mine === n}
              aria-label={t('rating.starLabel', {n, score: n * 2})}
              className={n <= shown ? 'rating-star is-on' : 'rating-star'}
              disabled={busy}
              onMouseEnter={() => setHover(n)}
              onFocus={() => setHover(n)}
              onBlur={() => setHover(0)}
              onClick={() => void rate(n)}
            >
              <Icon name="star" size={26} filled={n <= shown} />
            </button>
          ))}
        </div>
        <span className="rating-hint muted">{status === 'user' ? t('rating.hintUser') : t('rating.hintDevice')}</span>
        {error && (
          <span className="field-error" role="alert">
            {error}
          </span>
        )}
        <span className="sr-status" role="status">
          {saved}
        </span>
      </div>
    </div>
  );
}

// ---- Reviews ----

const SORTS: {value: ReviewSort; label: MessageKey}[] = [
  {value: 'helpful', label: 'review.sortHelpful'},
  {value: 'newest', label: 'review.sortNewest'},
  {value: 'finished', label: 'review.sortFinished'},
];

function WatchBadge({review}: {review: Review}) {
  const t = useT();
  if (review.watch_badge === 'finished') return <span className="review-badge is-finished">{t('review.watchedAll')}</span>;
  if (review.watch_badge === 'partial') return <span className="review-badge">{t('review.watchedPart', {p: review.watch_percent})}</span>;
  return <span className="review-badge is-none">{t('review.notWatched')}</span>;
}

function ReviewItem({review, onChange, onRemove}: {review: Review; onChange: (r: Review) => void; onRemove: () => void}) {
  const t = useT();
  const {status} = useAuth();
  const requireAuth = useRequireAuth();
  const [open, setOpen] = useState(!review.spoiler);
  const [reporting, setReporting] = useState(false);
  const [reported, setReported] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const toggleLike = async () => {
    if (busy) return;
    const like = !review.liked_by_me;
    setBusy(true);
    setError('');
    onChange({...review, liked_by_me: like, likes: Math.max(0, review.likes + (like ? 1 : -1))});
    try {
      const r = await likeReview(review.id, like);
      onChange({...review, liked_by_me: r.liked, likes: r.likes});
      if (like) track('review_like', {review_id: review.id});
    } catch (cause) {
      onChange(review);
      setError(postErrorMessage(cause));
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    setBusy(true);
    setError('');
    try {
      await deleteReview(review.id);
      onRemove();
    } catch (cause) {
      setError(postErrorMessage(cause));
      setBusy(false);
    }
  };

  const report = async (reason: ReportReason) => {
    const r = await reportReview(review.id, reason);
    setReported(r.created ? t('report.thanks') : t('report.already'));
    setReporting(false);
  };

  return (
    <li className="review">
      <div className="review-head">
        <strong className="review-author">{review.author?.display_name || t('review.anonymous')}</strong>
        <WatchBadge review={review} />
        <span className="muted review-date">{formatDate(review.created_at)}</span>
      </div>
      {review.spoiler && !open ? (
        <button type="button" className="review-spoiler" aria-expanded={false} onClick={() => setOpen(true)}>
          {t('review.spoilerFolded')}
        </button>
      ) : (
        <p className="review-body">
          {review.spoiler && <span className="review-spoiler-tag">{t('review.spoilerTag')}</span>}
          <TimestampText text={review.body} />
        </p>
      )}
      <div className="review-actions">
        {status === 'user' ? (
          <button
            type="button"
            className={review.liked_by_me ? 'review-like is-on' : 'review-like'}
            aria-pressed={review.liked_by_me}
            aria-label={t(review.liked_by_me ? 'review.unlike' : 'review.like', {n: review.likes})}
            disabled={busy || review.mine}
            onClick={() => void toggleLike()}
          >
            <Icon name="thumb" size={18} filled={review.liked_by_me} />
            <span aria-hidden="true">{review.likes > 0 ? formatCount(review.likes) : t('review.helpful')}</span>
          </button>
        ) : (
          <button type="button" className="review-like" aria-label={t('review.loginToLike', {n: review.likes})} onClick={() => requireAuth('like')}>
            <Icon name="thumb" size={18} />
            <span aria-hidden="true">{review.likes > 0 ? formatCount(review.likes) : t('review.helpful')}</span>
          </button>
        )}
        <span className="section-spacer" />
        {review.mine ? (
          <button type="button" className="text-btn" disabled={busy} onClick={() => void remove()}>
            {t('review.delete')}
          </button>
        ) : (
          !reported && (
            <button type="button" className="icon-btn" aria-label={t('review.report')} aria-expanded={reporting} onClick={() => setReporting(r => !r)}>
              <Icon name="flag" size={18} />
            </button>
          )
        )}
      </div>
      {error && (
        <p className="field-error" role="alert">
          {error}
        </p>
      )}
      {reported && (
        <p className="muted" role="status">
          {reported}
        </p>
      )}
      {reporting &&
        (status === 'user' ? (
          <ReportPicker onSubmit={report} onCancel={() => setReporting(false)} />
        ) : (
          <p className="muted">
            <button type="button" className="text-link" onClick={() => requireAuth('report')}>
              {t('report.loginFirst')}
            </button>
          </p>
        ))}
    </li>
  );
}

function Composer({slug, releaseId, watched, onPosted}: {slug: string; releaseId: number; watched: boolean; onPosted: (r: Review) => void}) {
  const t = useT();
  const [body, setBody] = useState('');
  const [spoiler, setSpoiler] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const length = runeLength(body.trim());
  const over = length > REVIEW_MAX;

  const submit = async () => {
    const text = body.trim();
    if (!text || over || busy) return;
    setBusy(true);
    setError('');
    try {
      const r = await postReview(slug, text, spoiler);
      onPosted({...r, mine: true});
      setBody('');
      setSpoiler(false);
      track('review_post', {release_id: releaseId, spoiler, watched});
    } catch (cause) {
      setError(postErrorMessage(cause));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form
      className="review-composer"
      onSubmit={event => {
        event.preventDefault();
        void submit();
      }}
    >
      <label className="sr-only" htmlFor="review-input">
        {t('review.inputLabel')}
      </label>
      <textarea
        id="review-input"
        className="review-input"
        rows={3}
        value={body}
        placeholder={t('review.placeholder', {n: REVIEW_MAX})}
        aria-invalid={over || !!error || undefined}
        aria-describedby="review-count"
        onChange={e => {
          setBody(e.target.value);
          if (error) setError('');
        }}
      />
      <div className="review-composer-foot">
        <label className="review-check">
          <input type="checkbox" checked={spoiler} onChange={e => setSpoiler(e.target.checked)} />
          <span>{t('review.markSpoiler')}</span>
        </label>
        <span className="section-spacer" />
        <span id="review-count" className={over ? 'dm-count dm-count-over' : 'dm-count'} aria-live="polite">
          {length}/{REVIEW_MAX}
        </span>
        <button type="submit" className="btn btn-primary btn-sm" disabled={busy || length === 0 || over}>
          {busy ? t('review.posting') : t('review.post')}
        </button>
      </div>
      {(error || over) && (
        <p className="field-error" role="alert">
          {over ? t('review.tooLong', {n: REVIEW_MAX}) : error}
        </p>
      )}
    </form>
  );
}

function Reviews({slug, releaseId, watched}: {slug: string; releaseId: number; watched: boolean}) {
  const t = useT();
  const {status} = useAuth();
  const requireAuth = useRequireAuth();
  const [sort, setSort] = useState<ReviewSort>('helpful');
  const [items, setItems] = useState<Review[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<Error | null>(null);
  const [nonce, setNonce] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError(null);
    getReviews(slug, sort, PAGE, null, controller.signal).then(
      page => {
        setItems(page.items);
        setCursor(page.nextCursor);
        setLoading(false);
      },
      cause => {
        if (isAbort(cause)) return;
        setError(cause instanceof Error ? cause : new Error(String(cause)));
        setLoading(false);
      },
    );
    return () => controller.abort();
  }, [slug, sort, status, nonce]);

  const more = async () => {
    if (!cursor) return;
    setLoading(true);
    try {
      const page = await getReviews(slug, sort, PAGE, cursor);
      setItems(list => list.concat(page.items.filter(r => !list.some(x => x.id === r.id))));
      setCursor(page.nextCursor);
    } catch (cause) {
      setError(cause instanceof Error ? cause : new Error(String(cause)));
    } finally {
      setLoading(false);
    }
  };

  const hasMine = items.some(r => r.mine);
  return (
    <section className="reviews" aria-labelledby="reviews-title">
      <div className="reviews-head">
        <h2 id="reviews-title" className="section-title">
          {t('review.title')}
        </h2>
        <div className="segmented segmented-sm" role="radiogroup" aria-label={t('review.sort')}>
          {SORTS.map(s => (
            <button key={s.value} type="button" role="radio" aria-checked={sort === s.value} className="segmented-item" onClick={() => setSort(s.value)}>
              {t(s.label)}
            </button>
          ))}
        </div>
      </div>
      {status === 'user' ? (
        hasMine ? (
          <p className="muted">{t('review.alreadyPosted')}</p>
        ) : (
          <Composer slug={slug} releaseId={releaseId} watched={watched} onPosted={r => setItems(list => [r, ...list.filter(x => x.id !== r.id)])} />
        )
      ) : status === 'anon' ? (
        <p className="muted">
          <button type="button" className="text-link" onClick={() => requireAuth('review')}>
            {t('review.loginToWrite')}
          </button>
        </p>
      ) : null}
      {error && <ErrorBanner error={error} onRetry={() => setNonce(n => n + 1)} />}
      {!loading && !error && items.length === 0 && <p className="muted">{sort === 'finished' ? t('review.emptyFinished') : t('review.empty')}</p>}
      {items.length > 0 && (
        <ul className="review-list">
          {items.map(r => (
            <ReviewItem
              key={r.id}
              review={r}
              onChange={next => setItems(list => list.map(x => (x.id === next.id ? next : x)))}
              onRemove={() => setItems(list => list.filter(x => x.id !== r.id))}
            />
          ))}
        </ul>
      )}
      {cursor && (
        <button type="button" className="btn btn-secondary" disabled={loading} onClick={() => void more()}>
          {loading ? t('review.loading') : t('review.more')}
        </button>
      )}
    </section>
  );
}

/** 评分与短评 under the release info (desktop inline; phones: summary + half sheet). */
export function Community({slug, releaseId, watched}: {slug: string; releaseId: number; watched: boolean}) {
  const t = useT();
  const {status} = useAuth();
  const mobile = useMediaQuery('(max-width: 767px)');
  const [sheet, setSheet] = useState(false);
  const loaded = useAsync(signal => getRating(slug, signal), [slug, status]);
  const [rating, setRating] = useState<RatingSummary | null>(null);
  useEffect(() => setRating(loaded.data ?? null), [loaded.data]);

  let ratingView = null;
  if (loaded.error && !rating) {
    ratingView = <ErrorBanner error={loaded.error} onRetry={loaded.reload} />;
  } else if (rating) {
    const panel = <RatingPanel slug={slug} releaseId={releaseId} watched={watched} rating={rating} onRated={setRating} />;
    ratingView = mobile ? (
      <>
        <button type="button" className="rating-summary" aria-haspopup="dialog" onClick={() => setSheet(true)}>
          {rating.score === null || rating.insufficient ? (
            <span className="rating-summary-score muted">{t('rating.insufficient')}</span>
          ) : (
            <>
              <strong className="rating-summary-score">{rating.score.toFixed(1)}</strong>
              <Stars value={rating.score / 2} size={14} />
            </>
          )}
          <span className="muted">{t('rating.count', {n: formatCount(rating.count)})}</span>
          <span className="section-spacer" />
          <span className="rating-summary-cta">{rating.my_score ? t('rating.yours', {n: rating.my_score * 2}) : t('rating.rateIt')}</span>
          <Icon name="chevronRight" size={18} />
        </button>
        <Sheet open={sheet} onClose={() => setSheet(false)} label={t('rating.title')} header={<h2 className="sheet-title">{t('rating.title')}</h2>}>
          {panel}
        </Sheet>
      </>
    ) : (
      panel
    );
  }

  return (
    <section className="community info-card" aria-labelledby="rating-title">
      <h2 id="rating-title" className={mobile ? 'sr-only' : 'section-title'}>
        {t('rating.title')}
      </h2>
      {ratingView}
      <Reviews slug={slug} releaseId={releaseId} watched={watched} />
    </section>
  );
}
