import {useEffect, useId, useLayoutEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type ReactNode} from 'react';
import {createPortal} from 'react-dom';
import {useOverlayHost} from '../lib/fullscreenHost';
import {useT} from '../lib/i18n';
import {offscreen, placeMenu, visibleArea, type Placement, type Rect} from '../lib/menuPlacement';
import {useAdhocQueue} from '../lib/useQueue';
import {Icon, type IconName} from './Icon';

/**
 * One action in a card's 「⋯」 menu. Features add their own items (推荐反馈
 * in RecFeedback.tsx) and pass the list to <CardMenu>; an empty list renders
 * nothing. Every release card's menu starts with useReleaseMenuItems
 * (添加到播放队列); feature items follow.
 */
export type CardMenuItem = {
  key: string;
  label: string;
  icon?: IconName;
  /** May return a short confirmation, shown by the button for a moment. */
  onSelect: () => void | string;
  disabled?: boolean;
};

/** The items every release card's menu starts with: 添加到播放队列 (lib/queue ?list=queue). */
export function useReleaseMenuItems(slug: string, title: string): CardMenuItem[] {
  const t = useT();
  const {queue, add} = useAdhocQueue();
  const queued = queue.slugs.includes(slug);
  return [
    {
      key: 'queue',
      label: queued ? t('queue.already') : t('queue.add'),
      icon: 'queue',
      disabled: queued,
      onSelect: () => (add(slug, title) ? t('queue.added') : t('queue.already')),
    },
  ];
}

/** A release card's menu with only the shared items (cards without feature items). */
export function ReleaseCardMenu({slug, title}: {slug: string; title: string}) {
  const t = useT();
  return <CardMenu label={t('cardMenu.label', {title})} items={useReleaseMenuItems(slug, title)} />;
}

/** Rects of the ancestors that clip `el` (overflow other than visible): its scrolling shelf, mostly. */
function clipRects(el: HTMLElement): Rect[] {
  const out: Rect[] = [];
  for (let p = el.parentElement; p && p !== document.body; p = p.parentElement) {
    const style = getComputedStyle(p);
    if (style.overflowX !== 'visible' || style.overflowY !== 'visible') out.push(p.getBoundingClientRect());
  }
  return out;
}

/**
 * The 「⋯」 button on a release card or row and its popup menu. Arrow keys
 * move, Enter / Space pick, Esc or a click outside closes (focus returns to
 * the button). On a card it sits over the thumbnail like the 稍后观看 button
 * (shown on hover / focus, always on touch); on a row it is a trailing button.
 *
 * The menu and its short confirmation are portalled out of the card (to the
 * body, or the fullscreen element) and placed with position: fixed against
 * the button (lib/menuPlacement), so a horizontally scrolling shelf does not
 * clip them; they flip above / shift inside the viewport, follow the button
 * while the page or shelf scrolls, and close once it scrolls out of view.
 * Tab from the menu goes back to the button first, so focus continues from
 * the card rather than from the end of the page.
 */
export function CardMenu({label, items}: {label: string; items: CardMenuItem[]}) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const noteRef = useRef<HTMLSpanElement>(null);
  const menuId = useId();
  const [note, setNote] = useState('');
  const host = useOverlayHost();
  const [menuPos, setMenuPos] = useState<Placement | null>(null);
  const [notePos, setNotePos] = useState<Placement | null>(null);

  useEffect(() => {
    if (!note) return;
    const timer = window.setTimeout(() => setNote(''), 2500);
    return () => window.clearTimeout(timer);
  }, [note]);

  const align = (): 'start' | 'end' => (rootRef.current?.closest('.release-row') ? 'end' : 'start');
  const viewport = () => ({width: document.documentElement.clientWidth, height: window.innerHeight});

  // Placed before paint, again on every scroll (any scroller: the shelf too) and resize.
  useLayoutEffect(() => {
    if (!open) {
      setMenuPos(null);
      return;
    }
    let frame = 0;
    const place = () => {
      frame = 0;
      const button = buttonRef.current;
      const menu = menuRef.current;
      if (!button || !menu) return;
      const rect = button.getBoundingClientRect();
      if (offscreen(rect, visibleArea(viewport(), clipRects(button)))) {
        setOpen(false);
        return;
      }
      setMenuPos(placeMenu(rect, {width: menu.offsetWidth, height: menu.scrollHeight}, viewport(), align()));
    };
    const schedule = () => {
      if (!frame) frame = window.requestAnimationFrame(place);
    };
    place();
    window.addEventListener('scroll', schedule, true);
    window.addEventListener('resize', schedule);
    return () => {
      window.removeEventListener('scroll', schedule, true);
      window.removeEventListener('resize', schedule);
      if (frame) window.cancelAnimationFrame(frame);
    };
  }, [open]);

  // The first item takes focus once the menu is placed (a hidden element cannot be focused).
  const placed = menuPos !== null;
  useEffect(() => {
    if (open && placed) menuRef.current?.querySelector<HTMLElement>('[role="menuitem"]:not(:disabled)')?.focus({preventScroll: true});
  }, [open, placed]);

  useEffect(() => {
    if (!open) return;
    const onDown = (event: PointerEvent) => {
      const target = event.target as Node;
      if (!rootRef.current?.contains(target) && !menuRef.current?.contains(target)) setOpen(false);
    };
    document.addEventListener('pointerdown', onDown);
    return () => document.removeEventListener('pointerdown', onDown);
  }, [open]);

  // The confirmation shows where the menu was (below the button, kept on screen).
  useLayoutEffect(() => {
    const button = buttonRef.current;
    const el = noteRef.current;
    if (!note || !button || !el) {
      setNotePos(null);
      return;
    }
    setNotePos(placeMenu(button.getBoundingClientRect(), {width: el.offsetWidth, height: el.offsetHeight}, viewport(), align()));
  }, [note]);

  if (items.length === 0) return null;

  const close = (focusButton: boolean) => {
    setOpen(false);
    if (focusButton) buttonRef.current?.focus();
  };

  const onMenuKey = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    const els = Array.from(menuRef.current?.querySelectorAll<HTMLElement>('[role="menuitem"]:not(:disabled)') ?? []);
    const index = els.indexOf(document.activeElement as HTMLElement);
    if (event.key === 'Escape') {
      event.preventDefault();
      event.stopPropagation();
      close(true);
    } else if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault();
      const step = event.key === 'ArrowDown' ? 1 : -1;
      els[(index + step + els.length) % els.length]?.focus();
    } else if (event.key === 'Home') {
      event.preventDefault();
      els[0]?.focus();
    } else if (event.key === 'End') {
      event.preventDefault();
      els[els.length - 1]?.focus();
    } else if (event.key === 'Tab') {
      // The menu lives at the end of the page: hand focus back to the
      // button so this Tab moves on from the card.
      close(true);
    }
  };

  const portal = (node: ReactNode) => (typeof document === 'undefined' ? null : createPortal(node, host ?? document.body));

  return (
    <div className={open ? 'card-menu is-open' : 'card-menu'} ref={rootRef}>
      <button
        ref={buttonRef}
        type="button"
        className="card-action card-menu-btn"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        aria-label={label}
        title={label}
        onClick={event => {
          event.preventDefault();
          event.stopPropagation();
          setOpen(o => !o);
        }}
      >
        <span aria-hidden="true">⋯</span>
      </button>
      {open &&
        portal(
          <div
            ref={menuRef}
            id={menuId}
            role="menu"
            aria-label={label}
            className="card-menu-list card-menu-floating"
            data-side={menuPos?.side}
            style={
              menuPos
                ? {top: menuPos.top, left: menuPos.left, maxHeight: menuPos.maxHeight}
                : {top: 0, left: 0, visibility: 'hidden'}
            }
            onKeyDown={onMenuKey}
            // Portalled: React still bubbles to the card, which must not see these clicks.
            onClick={event => event.stopPropagation()}
          >
            {items.map(item => (
              <button
                key={item.key}
                type="button"
                role="menuitem"
                className="card-menu-item"
                disabled={item.disabled}
                onClick={() => {
                  close(true);
                  const message = item.onSelect();
                  if (message) setNote(message);
                }}
              >
                {item.icon && <Icon name={item.icon} size={16} />}
                {item.label}
              </button>
            ))}
          </div>,
        )}
      {/* Announced from here; the visible copy floats outside the card. */}
      <span className="sr-only" role="status">
        {note}
      </span>
      {note &&
        portal(
          <span
            ref={noteRef}
            className="card-menu-note card-menu-floating"
            aria-hidden="true"
            style={notePos ? {top: notePos.top, left: notePos.left} : {top: 0, left: 0, visibility: 'hidden'}}
          >
            {note}
          </span>,
        )}
    </div>
  );
}
