import type {ReactNode} from 'react';
import {Link} from '../lib/nav';
import {swatch} from '../lib/format';
import {Icon} from './Icon';
import {LaterButton} from './SocialButtons';

// The API has no cover or portrait images yet (tech plan 2.4: covers via
// media_assets / object storage). Artwork slots render a neutral placeholder
// tinted from the slug so cards keep their real size (CLS, PRD 13).
// TODO(tech plan 2.4 / 3.1 object storage): render covers and portraits.

export function Thumb({seed, badge, progress, className = ''}: {seed: string; badge?: string; progress?: number; className?: string}) {
  return (
    <span className={`thumb ph-${swatch(seed)} ${className}`}>
      <span className="thumb-icon">
        <Icon name="film" size={22} />
      </span>
      {badge && <span className="thumb-badge">{badge}</span>}
      {progress !== undefined && (
        <span className="thumb-progress" aria-hidden="true">
          <span style={{width: `${Math.min(100, Math.max(0, progress * 100))}%`}} />
        </span>
      )}
    </span>
  );
}

/** Video card. Signed-in users get a 稍后观看 toggle over the thumbnail (NavLater). */
/** Link state read by the root page tracker (slot) and the player (play_request reason). */
const linkState = (slot?: string, reasonCode?: string) => (slot || reasonCode ? {slot, reason: reasonCode} : undefined);

/** One-line 推荐理由 under a card (PRD 10 可解释). */
export function CardReason({text}: {text?: string}) {
  return text ? <span className="card-reason">{text}</span> : null;
}

export function ReleaseCard({slug, title, meta, sub, badge, progress, later = true, slot, reason, reasonCode, onOpen}: {
  slug: string;
  title: string;
  meta?: ReactNode;
  sub?: ReactNode;
  badge?: string;
  progress?: number;
  later?: boolean;
  /** Analytics entry position (PRD 14.1 `slot`, e.g. "home:r2:c3"). */
  slot?: string;
  /** Recommendation reason shown under the card. */
  reason?: string;
  /** Recommendation reason code, sent as play_request `reason`. */
  reasonCode?: string;
  onOpen?: () => void;
}) {
  return (
    <div className="release-card-wrap">
      <Link to={`/releases/${encodeURIComponent(slug)}`} className="release-card" state={linkState(slot, reasonCode)} onClick={onOpen}>
        <Thumb seed={slug} badge={badge} progress={progress} />
        <span className="card-title">{title}</span>
        {meta && <span className="card-meta">{meta}</span>}
        {sub && <span className="card-meta">{sub}</span>}
        <CardReason text={reason} />
      </Link>
      {later && <LaterButton slug={slug} title={title} variant="icon" />}
    </div>
  );
}

/** Horizontal row card (Video aside, MobileSearch results, history). */
export function ReleaseRow({slug, title, meta, sub, badge, progress, children, query = '', slot, reason, reasonCode}: {
  slug: string;
  /** Appended to the link, e.g. "?list=later". */
  query?: string;
  title: string;
  meta?: ReactNode;
  sub?: ReactNode;
  badge?: string;
  progress?: number;
  children?: ReactNode;
  slot?: string;
  reason?: string;
  reasonCode?: string;
}) {
  return (
    <div className="release-row">
      <Link to={`/releases/${encodeURIComponent(slug)}${query}`} className="release-row-link" state={linkState(slot, reasonCode)}>
        <Thumb seed={slug} badge={badge} progress={progress} className="thumb-row" />
        <span className="release-row-text">
          <span className="card-title">{title}</span>
          {meta && <span className="card-meta">{meta}</span>}
          {sub && <span className="card-meta">{sub}</span>}
          <CardReason text={reason} />
        </span>
      </Link>
      {children}
    </div>
  );
}

export function Avatar({seed, name, size = 88}: {seed: string; name: string; size?: number}) {
  return (
    <span className={`avatar-ph face-${swatch(seed)}`} style={{width: size, height: size, fontSize: Math.round(size * 0.36)}} aria-hidden="true">
      {Array.from(name.trim())[0] ?? '?'}
    </span>
  );
}

export function PerformerChip({slug, name, sub, size = 88, slot}: {slug: string; name: string; sub?: ReactNode; size?: number; slot?: string}) {
  return (
    <Link to={`/performers/${encodeURIComponent(slug)}`} className="performer-chip" style={{width: size + 16}} state={linkState(slot)}>
      <Avatar seed={slug} name={name} size={size} />
      <span className="performer-chip-name">{name}</span>
      {sub && <span className="performer-chip-sub">{sub}</span>}
    </Link>
  );
}

export function SectionHeader({title, sub, action}: {title: string; sub?: string; action?: ReactNode}) {
  return (
    <div className="section-head">
      <h2>{title}</h2>
      {sub && <span className="section-sub">{sub}</span>}
      <span className="section-spacer" />
      {action}
    </div>
  );
}
