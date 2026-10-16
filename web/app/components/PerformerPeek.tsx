import {useCallback, useEffect, useLayoutEffect, useRef, useState, type FocusEvent, type ReactNode} from 'react';
import {createPortal} from 'react-dom';
import {Link} from '../lib/nav';
import {track} from '../lib/analytics';
import {getPerformer, getPerformerReleases, type Performer, type PerformerDetail, type Release} from '../lib/api';
import {ageAt, formatDate, formatYear} from '../lib/format';
import {t, useT} from '../lib/i18n';
import {useAsync} from '../lib/useAsync';
import {Avatar, Thumb} from './Cards';
import {Sheet} from './Sheet';
import {FollowButton} from './SocialButtons';

// PRD 6.3: desktop hover card and mobile half sheet (MobileActorSheet), both
// from GET /api/performers/{slug} and GET /api/performers/{slug}/releases.
//
// Not shown for lack of backend (never filled with sample data):
// 关注 + bell (NewWorkAlert) shares state with the performer page (lib/social).
// TODO(tech plan 3.13 performer_stats): 粉丝 / 总播放 / 热度 stats.
// TODO(tech plan 2.4 media_assets): portrait photos (placeholder avatar for now).
// TODO(tech plan 3.5 performer_similar): 相似演员.
// TODO(release_performers.role_type / character names): 「本片饰 …」.
// The works list is newest first (the endpoint's order), so it is labelled
// 最新作品 rather than 代表作品 until there is a popularity signal.

const PEEK_WORKS = 7;

type PeekData = {detail: PerformerDetail; works: Release[]};

// Hover and sheet share one request per performer for the page's lifetime.
const cache = new Map<string, Promise<PeekData>>();

function loadPeek(slug: string): Promise<PeekData> {
  let hit = cache.get(slug);
  if (!hit) {
    hit = Promise.all([getPerformer(slug), getPerformerReleases(slug, PEEK_WORKS)]).then(([detail, works]) => ({detail, works}));
    hit.catch(() => cache.delete(slug));
    cache.set(slug, hit);
  }
  return hit;
}

function usePeek(slug: string, enabled: boolean) {
  return useAsync(() => loadPeek(slug), [slug], enabled);
}

const present = (value: unknown) => value !== null && value !== undefined && String(value).trim() !== '';

// Source records can carry partial dates such as "1978-??-??" plus padding;
// show the full date when parseable, else just the year, else nothing.
function looseDate(value: string | null | undefined): string {
  const full = formatDate(value);
  if (full) return full;
  const year = /^\s*(\d{4})\b/.exec(value ?? '')?.[1];
  return year ? t('common.year', {y: year}) : '';
}

const cleanText = (value: unknown) => {
  const text = String(value ?? '').trim();
  return /^[-?\s]*$/.test(text) ? '' : text;
};

function facts(detail: PerformerDetail): [string, string][] {
  const heightValue = Number.parseFloat(String(detail.height_cm ?? ''));
  const height = Number.isFinite(heightValue) && heightValue > 0 ? `${heightValue} cm` : '';
  const rows: [string, string][] = [
    [t('peek.birthday'), looseDate(detail.birth_date)],
    [t('performer.height'), height],
    [t('peek.debut'), formatYear(detail.debut_date) ? t('common.year', {y: formatYear(detail.debut_date)}) : ''],
    [t('performer.birthPlace'), cleanText(detail.birth_place)],
    [t('performer.bloodType'), cleanText(detail.blood_type)],
  ];
  return rows.filter(([, v]) => present(v));
}

/** 「约 N 岁」 for this release (PRD 6.4: only a release date is known). */
export function castAge(performer: Pick<Performer, 'birth_date'>, releaseDate?: string): number | null {
  return ageAt(performer.birth_date, releaseDate);
}

function WorkTile({release, birth, compact}: {release: Release; birth?: string | null; compact?: boolean}) {
  const t = useT();
  const year = formatYear(release.release_date);
  const age = ageAt(birth, release.release_date);
  return (
    <Link to={`/releases/${encodeURIComponent(release.slug)}`} className={compact ? 'peek-work peek-work-compact' : 'peek-work'}>
      <Thumb seed={release.slug} image={release.cover} sizes="160px" />
      <span className="peek-work-title">{release.canonical_title}</span>
      {year && <span className="peek-work-sub">{age !== null ? t('peek.workAge', {year, age}) : t('common.year', {y: year})}</span>}
    </Link>
  );
}

function PeekBody({performer, releaseDate, currentSlug, layout}: {
  performer: Performer;
  releaseDate?: string;
  currentSlug?: string;
  layout: 'card' | 'sheet';
}) {
  const t = useT();
  const peek = usePeek(performer.slug, true);
  const age = castAge(performer, releaseDate);
  const detail = peek.data?.detail;
  const works = (peek.data?.works ?? []).filter(w => w.slug !== currentSlug).slice(0, layout === 'card' ? 3 : 6);
  const rows = detail ? facts(detail) : [];
  const href = `/performers/${encodeURIComponent(performer.slug)}`;

  let worksBlock: ReactNode = null;
  if (peek.error) {
    worksBlock = <p className="peek-note">{t('peek.worksFailed')}</p>;
  } else if (peek.loading) {
    worksBlock = peek.slow ? (
      <div className="peek-works" aria-busy="true" aria-label={t('app.loading')}>
        {Array.from({length: 3}, (_, i) => (
          <span key={i} className="sk sk-media peek-sk" />
        ))}
      </div>
    ) : null;
  } else if (works.length > 0) {
    worksBlock = (
      <div className={layout === 'card' ? 'peek-works peek-works-grid' : 'peek-works'}>
        {works.map(w => (
          <WorkTile key={w.id} release={w} birth={performer.birth_date ?? detail?.birth_date} compact={layout === 'card'} />
        ))}
      </div>
    );
  }

  return (
    <>
      <div className="peek-head">
        <Avatar seed={performer.slug} name={performer.canonical_name} size={layout === 'card' ? 84 : 72} image={performer.portrait} />
        <div className="peek-names">
          <span className="peek-name">{performer.canonical_name}</span>
          {performer.canonical_name_ja && performer.canonical_name_ja !== performer.canonical_name && (
            <span className="peek-alt">{performer.canonical_name_ja}</span>
          )}
          {layout === 'card' && rows.length > 0 && <span className="peek-meta">{rows.slice(0, 2).map(([k, v]) => `${k} ${v}`).join(' · ')}</span>}
          {age !== null && <span className="peek-age">{t('peek.castAge', {age})}</span>}
        </div>
      </div>

      {layout === 'sheet' && (
        <div className="peek-actions">
          <FollowButton slug={performer.slug} name={performer.canonical_name} performerId={performer.id} source="half_sheet" block />
          <Link to={href} className="btn btn-secondary peek-action">
            {t('peek.fullProfile')}
          </Link>
        </div>
      )}

      {(worksBlock || !peek.loading) && (
        <div className="peek-section">
          <span className="peek-label">{t('peek.latestWorks')}</span>
          {worksBlock ?? <p className="peek-note">{t('peek.noWorks')}</p>}
        </div>
      )}

      {layout === 'sheet' && detail && (
        <div className="peek-more">
          {rows.length > 0 && (
            <dl className="peek-facts">
              {rows.map(([k, v]) => (
                <div key={k} className="peek-fact">
                  <dt>{k}</dt>
                  <dd>{v}</dd>
                </div>
              ))}
            </dl>
          )}
          {detail.biography && <p className="bio">{detail.biography}</p>}
        </div>
      )}

      {layout === 'card' && (
        <div className="peek-actions peek-actions-card">
          <FollowButton slug={performer.slug} name={performer.canonical_name} performerId={performer.id} source="hover" size="sm" block />
          <Link to={href} className="btn btn-secondary btn-sm peek-action">
            {t('peek.viewProfile')}
          </Link>
        </div>
      )}
    </>
  );
}

/** MobileActorSheet: the video keeps playing above; 完整主页 is the only navigation. */
export function PerformerSheet({performer, releaseDate, currentSlug, onClose}: {
  performer: Performer | null;
  releaseDate?: string;
  currentSlug?: string;
  onClose: () => void;
}) {
  const t = useT();
  const openId = performer?.id;
  useEffect(() => {
    if (openId) track('actor_card_open', {performer_id: openId, source: 'half_sheet'});
  }, [openId]);
  return (
    <Sheet open={performer !== null} onClose={onClose} label={performer ? t('peek.sheetLabel', {name: performer.canonical_name}) : t('peek.sheetFallback')} className="sheet-performer">
      {performer && <PeekBody performer={performer} releaseDate={releaseDate} currentSlug={currentSlug} layout="sheet" />}
    </Sheet>
  );
}

const OPEN_DELAY_MS = 300;
const CLOSE_DELAY_MS = 150;
const CARD_WIDTH = 340;
const EDGE = 16;

/**
 * Desktop hover card (Video 主演): opens ~300 ms after pointer hover or on
 * keyboard focus, closes on leave / blur / Esc / scroll. Rendered in a portal
 * with fixed positioning because the cast row scrolls horizontally (which
 * would clip an absolutely positioned card); it flips to the anchor's right
 * edge near the viewport's right side and above the anchor near the bottom.
 */
export function PerformerHoverCard({performer, releaseDate, currentSlug, children}: {
  performer: Performer;
  releaseDate?: string;
  currentSlug?: string;
  children: ReactNode;
}) {
  const anchorRef = useRef<HTMLDivElement>(null);
  const cardRef = useRef<HTMLDivElement>(null);
  const timer = useRef<number | undefined>(undefined);
  const [open, setOpen] = useState(false);
  const [pos, setPos] = useState<{left: number; top: number; side: 'left' | 'right'; above: boolean} | null>(null);

  const clear = () => window.clearTimeout(timer.current);
  const show = useCallback((delay: number) => {
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setOpen(true), delay);
  }, []);
  const hide = useCallback((delay: number) => {
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setOpen(false), delay);
  }, []);

  useEffect(() => () => window.clearTimeout(timer.current), []);
  useEffect(() => {
    if (open && performer.id) track('actor_card_open', {performer_id: performer.id, source: 'hover'});
  }, [open, performer.id]);

  // Prefetch so the card is usually filled by the time it opens.
  const prefetch = () => {
    loadPeek(performer.slug).catch(() => undefined);
  };

  useLayoutEffect(() => {
    if (!open) {
      setPos(null);
      return;
    }
    const place = () => {
      const anchor = anchorRef.current?.getBoundingClientRect();
      if (!anchor) return;
      const vw = document.documentElement.clientWidth;
      const vh = window.innerHeight;
      const side: 'left' | 'right' = anchor.left + CARD_WIDTH > vw - EDGE ? 'right' : 'left';
      const left = side === 'left' ? Math.max(EDGE, anchor.left) : Math.max(EDGE, anchor.right - CARD_WIDTH);
      const cardHeight = cardRef.current?.offsetHeight ?? 320;
      const above = anchor.bottom + 8 + cardHeight > vh - EDGE && anchor.top - 8 - cardHeight > EDGE;
      setPos({left, top: above ? anchor.top - 8 - cardHeight : anchor.bottom + 8, side, above});
    };
    place();
    const onScroll = (event: Event) => {
      if (cardRef.current && event.target instanceof Node && cardRef.current.contains(event.target)) return;
      setOpen(false);
    };
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setOpen(false);
    };
    window.addEventListener('scroll', onScroll, true);
    window.addEventListener('resize', place);
    document.addEventListener('keydown', onKey);
    const observer = new ResizeObserver(place);
    if (cardRef.current) observer.observe(cardRef.current);
    return () => {
      window.removeEventListener('scroll', onScroll, true);
      window.removeEventListener('resize', place);
      document.removeEventListener('keydown', onKey);
      observer.disconnect();
    };
  }, [open]);

  const within = (node: EventTarget | null) =>
    node instanceof Node && (anchorRef.current?.contains(node) || cardRef.current?.contains(node));

  const handlers = {
    onMouseEnter: () => {
      prefetch();
      show(OPEN_DELAY_MS);
    },
    onMouseLeave: () => hide(CLOSE_DELAY_MS),
    onFocus: () => {
      prefetch();
      clear();
      // Keyboard focus opens after the same delay so tabbing through the row is calm.
      show(OPEN_DELAY_MS);
    },
    onBlur: (event: FocusEvent) => {
      if (!within(event.relatedTarget)) hide(0);
    },
  };

  return (
    <div ref={anchorRef} className="hovercard-anchor" {...handlers}>
      {children}
      {open &&
        createPortal(
          <div
            ref={cardRef}
            role="group"
            aria-label={t('peek.cardLabel', {name: performer.canonical_name})}
            className="hovercard"
            data-side={pos?.side ?? 'left'}
            data-above={pos?.above ? 'true' : 'false'}
            style={{left: pos?.left ?? -9999, top: pos?.top ?? -9999, width: CARD_WIDTH, visibility: pos ? 'visible' : 'hidden'}}
            {...handlers}
          >
            <PeekBody performer={performer} releaseDate={releaseDate} currentSlug={currentSlug} layout="card" />
          </div>,
          document.body,
        )}
    </div>
  );
}
