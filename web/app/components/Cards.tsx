import type {ReactNode} from 'react';
import type {ApiImage} from '../lib/api';
import {Link} from '../lib/nav';
import {swatch} from '../lib/format';
import {Icon} from './Icon';
import {ReleaseCardMenu} from './CardMenu';
import {LaterButton} from './SocialButtons';

// Covers and portraits come from object storage (internal/images) as
// `cover` / `portrait` on catalog responses, only for image-permitted
// sources. Without one, artwork slots render a neutral placeholder tinted
// from the slug. Either way the slot keeps its real size (CLS, PRD 13): the
// thumb has a fixed aspect ratio and images carry width/height.

/** Absolute URL of a catalog image (og:image / JSON-LD need one). */
export const absoluteImageUrl = (origin: string, url: string) =>
  /^https?:[/][/]/.test(url) ? url : `${origin}${url.startsWith('/') ? '' : '/'}${url}`;

/** A stored catalog image: srcset/sizes, intrinsic size, lazy by default. */
export function CatalogImage({image, alt = '', sizes, className, eager = false, width, height}: {
  image: ApiImage;
  alt?: string;
  sizes?: string;
  className?: string;
  eager?: boolean;
  /** Rendered box (defaults to the image's intrinsic size). */
  width?: number;
  height?: number;
}) {
  const srcSet = image.srcset?.length ? image.srcset.map(v => `${v.url} ${v.width}w`).join(', ') : undefined;
  return (
    <img
      className={className}
      src={image.url}
      srcSet={srcSet}
      sizes={srcSet ? sizes : undefined}
      width={width ?? image.width}
      height={height ?? image.height}
      alt={alt}
      loading={eager ? 'eager' : 'lazy'}
      decoding="async"
    />
  );
}

export function Thumb({seed, badge, progress, className = '', image, sizes = '(max-width: 640px) 50vw, 320px'}: {
  seed: string;
  badge?: string;
  progress?: number;
  className?: string;
  /** The release cover; the tinted placeholder when absent. */
  image?: ApiImage | null;
  sizes?: string;
}) {
  return (
    <span className={`thumb ph-${swatch(seed)} ${className}`}>
      {image ? (
        <CatalogImage image={image} className="thumb-img" sizes={sizes} />
      ) : (
        <span className="thumb-icon">
          <Icon name="film" size={22} />
        </span>
      )}
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

export function ReleaseCard({slug, title, meta, sub, badge, progress, later = true, slot, reason, reasonCode, onOpen, cover, menu}: {
  slug: string;
  /** Stored cover (Release.cover). */
  cover?: ApiImage | null;
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
  /**
   * The 「⋯」 CardMenu over the thumbnail: by default the shared items
   * (添加到播放队列); recommendation cards pass theirs (RecFeedbackCard); null
   * for none.
   */
  menu?: ReactNode;
}) {
  return (
    <div className="release-card-wrap">
      <Link to={`/releases/${encodeURIComponent(slug)}`} className="release-card" state={linkState(slot, reasonCode)} onClick={onOpen}>
        <Thumb seed={slug} badge={badge} progress={progress} image={cover} />
        <span className="card-title">{title}</span>
        {meta && <span className="card-meta">{meta}</span>}
        {sub && <span className="card-meta">{sub}</span>}
        <CardReason text={reason} />
      </Link>
      {later && <LaterButton slug={slug} title={title} variant="icon" />}
      {menu === undefined ? <ReleaseCardMenu slug={slug} title={title} /> : menu}
    </div>
  );
}

/** Horizontal row card (Video aside, MobileSearch results, history). */
export function ReleaseRow({slug, title, meta, sub, badge, progress, children, query = '', slot, reason, reasonCode, cover}: {
  slug: string;
  cover?: ApiImage | null;
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
        <Thumb seed={slug} badge={badge} progress={progress} className="thumb-row" image={cover} sizes="168px" />
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

export function Avatar({seed, name, size = 88, image}: {seed: string; name: string; size?: number; image?: ApiImage | null}) {
  if (image) {
    return <CatalogImage image={image} className="avatar-img" width={size} height={size} sizes={`${size}px`} />;
  }
  return (
    <span className={`avatar-ph face-${swatch(seed)}`} style={{width: size, height: size, fontSize: Math.round(size * 0.36)}} aria-hidden="true">
      {Array.from(name.trim())[0] ?? '?'}
    </span>
  );
}

export function PerformerChip({slug, name, sub, size = 88, slot, portrait}: {
  slug: string;
  name: string;
  sub?: ReactNode;
  size?: number;
  slot?: string;
  /** Stored portrait (Performer.portrait). */
  portrait?: ApiImage | null;
}) {
  return (
    <Link to={`/performers/${encodeURIComponent(slug)}`} className="performer-chip" style={{width: size + 16}} state={linkState(slot)}>
      <Avatar seed={slug} name={name} size={size} image={portrait} />
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
