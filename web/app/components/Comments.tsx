import {useEffect, useState} from 'react';
import {Link, useLocation} from '../lib/nav';
import {ApiError, isAbort} from '../lib/api';
import {track} from '../lib/analytics';
import {useAuth} from '../lib/auth';
import {postErrorMessage, type ReportReason} from '../lib/community';
import {formatCount, formatDate} from '../lib/format';
import {useT} from '../lib/i18n';
import {
  COMMENT_MAX,
  commentsDisabled,
  deleteComment,
  editComment,
  getComments,
  getReplies,
  likeComment,
  postComment,
  reportComment,
  type Comment,
  type CommentSort,
} from '../lib/interactions';
import type {MessageKey} from '../lib/messages.zh-CN';
import {Icon} from './Icon';
import {ReportPicker} from './ReportPicker';
import {ErrorBanner} from './States';
import {TimestampText} from './Timestamps';

// 评论 (PRD 5.4 · Video / MobileVideo; tech plan 3.13; internal/api/interactions.go).
//
// Distinct from 短评 (Ratings.tsx, PRD 19): a 短评 is one ≤ 140-character
// note attached to the author's rating; comments are open discussion — any
// number per person, up to 500 characters, threaded one level deep (a reply
// to a reply joins the thread), sorted 最热 / 最新, liked once per person,
// 含剧透 folded like short reviews, reported into the same moderation queue.
// The author can delete their own, and edit it within 10 minutes of posting
// (「已编辑」 marker; the edit goes through the keyword check again and
// moderators keep the earlier versions); a deleted comment that has replies
// stays as 「该评论已删除」. Keyword hits are shown only to their author (待审核).
// The whole section hides itself when the backend has comments turned off.

const PAGE = 20;
const runeLength = (s: string) => Array.from(s).length;

const SORTS: {value: CommentSort; label: MessageKey}[] = [
  {value: 'top', label: 'comment.sortTop'},
  {value: 'newest', label: 'comment.sortNewest'},
];

function useLoginHref() {
  const location = useLocation();
  return `/login?next=${encodeURIComponent(location.pathname + location.search)}`;
}

function CommentComposer({slug, releaseId, parent, onPosted, onCancel}: {
  slug: string;
  releaseId: number;
  /** Replying to this comment (its thread); none for a top-level comment. */
  parent?: Comment;
  onPosted: (c: Comment) => void;
  onCancel?: () => void;
}) {
  const t = useT();
  const [body, setBody] = useState('');
  const [spoiler, setSpoiler] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const length = runeLength(body.trim());
  const over = length > COMMENT_MAX;
  const inputId = parent ? `comment-reply-${parent.id}` : 'comment-input';

  const submit = async () => {
    const text = body.trim();
    if (!text || over || busy) return;
    setBusy(true);
    setError('');
    try {
      const c = await postComment(slug, text, spoiler, parent?.id);
      onPosted({...c, mine: true});
      setBody('');
      setSpoiler(false);
      track('comment_post', {release_id: releaseId, reply: !!parent, spoiler});
    } catch (cause) {
      setError(postErrorMessage(cause));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form
      className={parent ? 'review-composer comment-reply-composer' : 'review-composer'}
      onSubmit={event => {
        event.preventDefault();
        void submit();
      }}
    >
      <label className="sr-only" htmlFor={inputId}>
        {parent ? t('comment.replyTo', {name: parent.author?.display_name || t('review.anonymous')}) : t('comment.inputLabel')}
      </label>
      <textarea
        id={inputId}
        className="review-input"
        rows={parent ? 2 : 3}
        value={body}
        autoFocus={!!parent}
        placeholder={parent ? t('comment.replyPlaceholder') : t('comment.placeholder', {n: COMMENT_MAX})}
        aria-invalid={over || !!error || undefined}
        aria-describedby={`${inputId}-count`}
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
        <span id={`${inputId}-count`} className={over ? 'dm-count dm-count-over' : 'dm-count'} aria-live="polite">
          {length}/{COMMENT_MAX}
        </span>
        {onCancel && (
          <button type="button" className="text-btn" onClick={onCancel}>
            {t('report.cancel')}
          </button>
        )}
        <button type="submit" className="btn btn-primary btn-sm" disabled={busy || length === 0 || over}>
          {busy ? t('review.posting') : parent ? t('comment.reply') : t('review.post')}
        </button>
      </div>
      {(error || over) && (
        <p className="field-error" role="alert">
          {over ? t('comment.tooLong', {n: COMMENT_MAX}) : error}
        </p>
      )}
    </form>
  );
}

function CommentItem({comment, onChange, onRemove, onReply}: {
  comment: Comment;
  onChange: (c: Comment) => void;
  onRemove: () => void;
  /** Top-level comments only: open the reply box. */
  onReply?: () => void;
}) {
  const t = useT();
  const {status} = useAuth();
  const loginHref = useLoginHref();
  const [open, setOpen] = useState(!comment.spoiler);
  const [reporting, setReporting] = useState(false);
  const [reported, setReported] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [editing, setEditing] = useState(false);

  if (comment.deleted) {
    return <p className="muted comment-deleted">{t('comment.deleted')}</p>;
  }

  const toggleLike = async () => {
    if (busy) return;
    const like = !comment.liked_by_me;
    setBusy(true);
    setError('');
    onChange({...comment, liked_by_me: like, likes: Math.max(0, comment.likes + (like ? 1 : -1))});
    try {
      const r = await likeComment(comment.id, like);
      onChange({...comment, liked_by_me: r.liked, likes: r.likes});
      if (like) track('comment_like', {comment_id: comment.id});
    } catch (cause) {
      onChange(comment);
      setError(postErrorMessage(cause));
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!window.confirm(t('comment.deleteConfirm'))) return;
    setBusy(true);
    setError('');
    try {
      await deleteComment(comment.id);
      onRemove();
    } catch (cause) {
      setError(postErrorMessage(cause));
      setBusy(false);
    }
  };

  const report = async (reason: ReportReason) => {
    const r = await reportComment(comment.id, reason);
    setReported(r.created ? t('report.thanks') : t('report.already'));
    setReporting(false);
  };

  const canEdit = comment.mine && !!comment.editable_until && Date.parse(comment.editable_until) > Date.now();

  return (
    <>
      <div className="review-head">
        <strong className="review-author">{comment.author?.display_name || t('review.anonymous')}</strong>
        {comment.pending && <span className="review-badge">{t('comment.pending')}</span>}
        <span className="muted review-date">{formatDate(comment.created_at)}</span>
        {comment.edited_at && (
          <span className="muted comment-edited" title={t('comment.editedAt', {date: formatDate(comment.edited_at)})}>
            {t('comment.edited')}
          </span>
        )}
      </div>
      {editing ? (
        <CommentEditor
          comment={comment}
          onSaved={c => {
            onChange({...comment, ...c, replies: comment.replies, mine: true});
            setEditing(false);
          }}
          onCancel={() => setEditing(false)}
        />
      ) : comment.spoiler && !open ? (
        <button type="button" className="review-spoiler" aria-expanded={false} onClick={() => setOpen(true)}>
          {t('review.spoilerFolded')}
        </button>
      ) : (
        <p className="review-body">
          {comment.spoiler && <span className="review-spoiler-tag">{t('review.spoilerTag')}</span>}
          <TimestampText text={comment.body} />
        </p>
      )}
      <div className="review-actions">
        {status === 'user' ? (
          <button
            type="button"
            className={comment.liked_by_me ? 'review-like is-on' : 'review-like'}
            aria-pressed={comment.liked_by_me}
            aria-label={t(comment.liked_by_me ? 'comment.unlike' : 'comment.like', {n: comment.likes})}
            disabled={busy || comment.pending}
            onClick={() => void toggleLike()}
          >
            <Icon name="thumb" size={18} filled={comment.liked_by_me} />
            <span aria-hidden="true">{comment.likes > 0 ? formatCount(comment.likes) : t('vote.likeShort')}</span>
          </button>
        ) : (
          <Link to={loginHref} className="review-like" aria-label={t('review.loginToLike', {n: comment.likes})}>
            <Icon name="thumb" size={18} />
            <span aria-hidden="true">{comment.likes > 0 ? formatCount(comment.likes) : t('vote.likeShort')}</span>
          </Link>
        )}
        {onReply &&
          (status === 'user' ? (
            <button type="button" className="text-btn" onClick={onReply}>
              {t('comment.reply')}
            </button>
          ) : (
            <Link to={loginHref} className="text-btn">
              {t('comment.reply')}
            </Link>
          ))}
        <span className="section-spacer" />
        {comment.mine ? (
          <>
            {canEdit && !editing && (
              <button type="button" className="text-btn" disabled={busy} onClick={() => setEditing(true)}>
                {t('comment.edit')}
              </button>
            )}
            <button type="button" className="text-btn" disabled={busy} onClick={() => void remove()}>
              {t('review.delete')}
            </button>
          </>
        ) : (
          !reported &&
          !comment.pending && (
            <button type="button" className="icon-btn" aria-label={t('comment.report')} aria-expanded={reporting} onClick={() => setReporting(r => !r)}>
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
            <Link to={loginHref}>{t('report.loginFirst')}</Link>
          </p>
        ))}
    </>
  );
}

/** The author's edit box (within the 10-minute window). */
function CommentEditor({comment, onSaved, onCancel}: {comment: Comment; onSaved: (c: Comment) => void; onCancel: () => void}) {
  const t = useT();
  const [body, setBody] = useState(comment.body);
  const [spoiler, setSpoiler] = useState(comment.spoiler);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const length = runeLength(body.trim());
  const over = length > COMMENT_MAX;
  const inputId = `comment-edit-${comment.id}`;

  const save = async () => {
    const text = body.trim();
    if (!text || over || busy) return;
    setBusy(true);
    setError('');
    try {
      onSaved(await editComment(comment.id, text, spoiler));
    } catch (cause) {
      setError(cause instanceof ApiError && cause.status === 409 ? t('comment.editClosed') : postErrorMessage(cause));
      setBusy(false);
    }
  };

  return (
    <form
      className="review-composer comment-edit-form"
      onSubmit={event => {
        event.preventDefault();
        void save();
      }}
    >
      <label className="sr-only" htmlFor={inputId}>
        {t('comment.editLabel')}
      </label>
      <textarea
        id={inputId}
        className="review-input"
        rows={3}
        value={body}
        autoFocus
        aria-invalid={over || !!error || undefined}
        aria-describedby={`${inputId}-count`}
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
        <span id={`${inputId}-count`} className={over ? 'dm-count dm-count-over' : 'dm-count'} aria-live="polite">
          {length}/{COMMENT_MAX}
        </span>
        <button type="button" className="text-btn" onClick={onCancel}>
          {t('report.cancel')}
        </button>
        <button type="submit" className="btn btn-primary btn-sm" disabled={busy || length === 0 || over}>
          {t('comment.save')}
        </button>
      </div>
      {(error || over) && (
        <p className="field-error" role="alert">
          {over ? t('comment.tooLong', {n: COMMENT_MAX}) : error}
        </p>
      )}
    </form>
  );
}

/** One top-level comment with its replies and reply box. */
function Thread({slug, releaseId, comment, onChange, onRemove}: {
  slug: string;
  releaseId: number;
  comment: Comment;
  onChange: (c: Comment) => void;
  onRemove: () => void;
}) {
  const t = useT();
  const [replying, setReplying] = useState(false);
  const [replies, setReplies] = useState<Comment[]>(comment.replies ?? []);
  const [cursor, setCursor] = useState<string | null>(null);
  const [expanded, setExpanded] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  useEffect(() => {
    if (!expanded) setReplies(comment.replies ?? []);
  }, [comment.replies, expanded]);
  const hidden = Math.max(0, comment.reply_count - replies.length);

  const loadMore = async () => {
    setLoading(true);
    setError('');
    try {
      // The preview holds the first replies: page from the start once, then on.
      const page = await getReplies(comment.id, PAGE, expanded ? cursor : null);
      setReplies(list => (expanded ? list.concat(page.items.filter(r => !list.some(x => x.id === r.id))) : page.items));
      setCursor(page.nextCursor);
      setExpanded(true);
    } catch (cause) {
      setError(postErrorMessage(cause));
    } finally {
      setLoading(false);
    }
  };

  const update = (next: Comment) => setReplies(list => list.map(x => (x.id === next.id ? next : x)));
  const removeReply = (id: number) => {
    setReplies(list => list.filter(x => x.id !== id));
    onChange({...comment, reply_count: Math.max(0, comment.reply_count - 1), replies: (comment.replies ?? []).filter(x => x.id !== id)});
  };

  return (
    <li className="review comment">
      <CommentItem comment={comment} onChange={onChange} onRemove={onRemove} onReply={comment.deleted ? undefined : () => setReplying(r => !r)} />
      {(replies.length > 0 || replying) && (
        <ul className="review-list comment-replies" aria-label={t('comment.replies', {n: comment.reply_count})}>
          {replies.map(r => (
            <li key={r.id} className="review comment comment-reply">
              <CommentItem comment={r} onChange={update} onRemove={() => removeReply(r.id)} />
            </li>
          ))}
        </ul>
      )}
      {(hidden > 0 || (expanded && cursor)) && (
        <button type="button" className="text-btn comment-more" disabled={loading} onClick={() => void loadMore()}>
          {loading ? t('review.loading') : t('comment.moreReplies', {n: hidden || comment.reply_count})}
        </button>
      )}
      {error && (
        <p className="field-error" role="alert">
          {error}
        </p>
      )}
      {replying && (
        <CommentComposer
          slug={slug}
          releaseId={releaseId}
          parent={comment}
          onCancel={() => setReplying(false)}
          onPosted={c => {
            setReplying(false);
            setReplies(list => [...list, c]);
            onChange({...comment, reply_count: comment.reply_count + 1, replies: [...(comment.replies ?? []), c]});
          }}
        />
      )}
    </li>
  );
}

/** 评论 under the cast on the release page. */
export function Comments({slug, releaseId}: {slug: string; releaseId: number}) {
  const t = useT();
  const {status} = useAuth();
  const loginHref = useLoginHref();
  const [sort, setSort] = useState<CommentSort>('top');
  const [items, setItems] = useState<Comment[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<Error | null>(null);
  const [off, setOff] = useState(false);
  const [nonce, setNonce] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError(null);
    getComments(slug, sort, PAGE, null, controller.signal).then(
      page => {
        setItems(page.items);
        setCursor(page.nextCursor);
        setLoading(false);
      },
      cause => {
        if (isAbort(cause)) return;
        if (commentsDisabled(cause)) setOff(true);
        else setError(cause instanceof Error ? cause : new Error(String(cause)));
        setLoading(false);
      },
    );
    return () => controller.abort();
  }, [slug, sort, status, nonce]);

  const more = async () => {
    if (!cursor) return;
    setLoading(true);
    try {
      const page = await getComments(slug, sort, PAGE, cursor);
      setItems(list => list.concat(page.items.filter(c => !list.some(x => x.id === c.id))));
      setCursor(page.nextCursor);
    } catch (cause) {
      setError(cause instanceof Error ? cause : new Error(String(cause)));
    } finally {
      setLoading(false);
    }
  };

  if (off) return null;
  return (
    <section className="comments info-card" aria-labelledby="comments-title">
      <div className="reviews-head">
        <h2 id="comments-title" className="section-title">
          {t('comment.title')}
        </h2>
        <div className="segmented segmented-sm" role="radiogroup" aria-label={t('comment.sort')}>
          {SORTS.map(s => (
            <button key={s.value} type="button" role="radio" aria-checked={sort === s.value} className="segmented-item" onClick={() => setSort(s.value)}>
              {t(s.label)}
            </button>
          ))}
        </div>
      </div>
      {status === 'user' ? (
        <CommentComposer slug={slug} releaseId={releaseId} onPosted={c => setItems(list => [c, ...list.filter(x => x.id !== c.id)])} />
      ) : status === 'anon' ? (
        <p className="muted">
          <Link to={loginHref} className="text-link">
            {t('comment.loginToWrite')}
          </Link>
        </p>
      ) : null}
      {error && <ErrorBanner error={error} onRetry={() => setNonce(n => n + 1)} />}
      {!loading && !error && items.length === 0 && <p className="muted">{t('comment.empty')}</p>}
      {items.length > 0 && (
        <ul className="review-list">
          {items.map(c => (
            <Thread
              key={c.id}
              slug={slug}
              releaseId={releaseId}
              comment={c}
              onChange={next => setItems(list => list.map(x => (x.id === next.id ? next : x)))}
              onRemove={() =>
                // A thread with replies keeps a placeholder, as on reload.
                setItems(list =>
                  c.reply_count > 0 ? list.map(x => (x.id === c.id ? {...x, deleted: true, body: '', mine: false} : x)) : list.filter(x => x.id !== c.id),
                )
              }
            />
          ))}
        </ul>
      )}
      {cursor && (
        <button type="button" className="btn btn-secondary" disabled={loading} onClick={() => void more()}>
          {loading ? t('review.loading') : t('comment.more')}
        </button>
      )}
    </section>
  );
}
