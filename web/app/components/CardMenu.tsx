import {useEffect, useId, useRef, useState, type KeyboardEvent as ReactKeyboardEvent} from 'react';
import {Icon, type IconName} from './Icon';

/**
 * One action in a card's 「⋯」 menu. Features add their own items (推荐反馈
 * in RecFeedback.tsx; others such as 加入队列 can append theirs) and pass
 * the list to <CardMenu>; an empty list renders nothing.
 */
export type CardMenuItem = {
  key: string;
  label: string;
  icon?: IconName;
  onSelect: () => void;
  disabled?: boolean;
};

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
                item.onSelect();
              }}
            >
              {item.icon && <Icon name={item.icon} size={16} />}
              {item.label}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
