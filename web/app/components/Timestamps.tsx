import {createContext, useContext, type ReactNode} from 'react';
import {Link, useNavigate} from '../lib/nav';
import {useT} from '../lib/i18n';
import {seekPlayer} from '../lib/playerSeek';
import {splitTimestamps} from '../lib/timestamps';

// Clickable timestamps (YouTube) in comment and review bodies and the
// danmaku list. Text stays text: the pieces are React nodes, never HTML.
//
// Inside a <TimestampScope> (the release page wraps 短评 and 评论 in one) a
// time links to /releases/<slug>?t=<s>: on that release's page a click seeks
// the hosted player and scrolls it into view; elsewhere (or before the player
// can seek) the link navigates there, and ?t= starts the video at that time.
// Without a scope, or with onSeek (the danmaku list, inside the player),
// times are plain text or seek buttons.

type Scope = {slug: string; maxSeconds?: number};
const ScopeContext = createContext<Scope | null>(null);

export function TimestampScope({slug, maxSeconds, children}: Scope & {children: ReactNode}) {
  return <ScopeContext.Provider value={{slug, maxSeconds}}>{children}</ScopeContext.Provider>;
}

/**
 * Scrolls the release page's player into view. The PlayerHost slot is
 * display: contents (no box, so scrollIntoView on it does nothing): scroll
 * its .video-main column, whose top is the player's.
 */
function revealPlayer() {
  const el = document.querySelector('.video-main .player-slot')?.closest('.video-main');
  if (el instanceof HTMLElement) el.scrollIntoView({block: 'start', behavior: 'smooth'});
}

export function TimestampText({text, onSeek, maxSeconds}: {text: string; onSeek?: (seconds: number) => void; maxSeconds?: number}) {
  const t = useT();
  const scope = useContext(ScopeContext);
  const navigate = useNavigate();
  const pieces = splitTimestamps(text, maxSeconds ?? scope?.maxSeconds);
  if (!onSeek && !scope) return <>{text}</>;
  return (
    <>
      {pieces.map((p, i) => {
        if (typeof p === 'string') return p;
        const label = t('review.seekTo', {time: p.text});
        if (onSeek) {
          return (
            <button key={i} type="button" className="timestamp-link" aria-label={label} onClick={() => onSeek(p.seconds)}>
              {p.text}
            </button>
          );
        }
        const href = `/releases/${encodeURIComponent(scope!.slug)}?t=${p.seconds}`;
        return (
          <Link
            key={i}
            to={href}
            className="timestamp-link"
            aria-label={label}
            onClick={e => {
              if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
              if (seekPlayer(scope!.slug, p.seconds)) {
                e.preventDefault();
                revealPlayer();
              } else if (document.querySelector('.video-main .player-slot')) {
                // This release's page, but the player cannot seek yet: ?t= starts it there.
                e.preventDefault();
                void navigate(href, {replace: true, preventScrollReset: true});
                revealPlayer();
              }
            }}
          >
            {p.text}
          </Link>
        );
      })}
    </>
  );
}
