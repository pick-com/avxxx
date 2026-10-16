import {useEffect, useId, useRef, useState, type KeyboardEvent as ReactKeyboardEvent} from 'react';
import {useT} from '../lib/i18n';
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

/**
 * The 「⋯」 button on a release card or row and its popup menu. Arrow keys
 * move, Enter / Space pick, Esc or a click outside closes (focus returns to
 * the button). On a card it sits over the thumbnail like the 稍后观看 button
 * (shown on hover / focus, always on touch); on a row it is a trailing button.
 */
export function CardMenu({label, items}: {label: string; items: CardMenuItem[]}) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const menuId = useId();
  const [note, setNote] = useState('');

  useEffect(() => {
    if (!note) return;
    const timer = window.setTimeout(() => setNote(''), 2500);
    return () => window.clearTimeout(timer);
  }, [note]);

  useEffect(() => {
    if (!open) return;
    menuRef.current?.querySelector<HTMLElement>('[role="menuitem"]:not(:disabled)')?.focus();
    const onDown = (event: PointerEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener('pointerdown', onDown);
    return () => document.removeEventListener('pointerdown', onDown);
  }, [open]);

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
      setOpen(false);
    }
  };

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
      {open && (
        <div ref={menuRef} id={menuId} role="menu" aria-label={label} className="card-menu-list" onKeyDown={onMenuKey}>
          {items.map(item => (
            <button
              key={item.key}
              type="button"
              role="menuitem"
              className="card-menu-item"
              disabled={item.disabled}
              onClick={() => {
                close(false);
                const message = item.onSelect();
                if (message) setNote(message);
              }}
            >
              {item.icon && <Icon name={item.icon} size={16} />}
              {item.label}
            </button>
          ))}
        </div>
      )}
      <span className="card-menu-note" role="status">
        {note}
      </span>
    </div>
  );
}
