import {useEffect, useId, useRef, useState, type KeyboardEvent as ReactKeyboardEvent} from 'react';
import {Icon} from './Icon';

export type MenuOption<T> = {value: T; label: string; hint?: string};

/**
 * Small popup menu for the desktop player bar (清晰度 / 倍速). Opens upward over
 * the video; arrow keys move, Enter/Space pick, Esc closes and returns focus.
 */
export function PlayerMenu<T extends string | number>({label, buttonText, options, value, onChange, disabled}: {
  label: string;
  buttonText: string;
  options: MenuOption<T>[];
  value: T;
  onChange: (value: T) => void;
  disabled?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const menuId = useId();

  useEffect(() => {
    if (!open) return;
    const items = menuRef.current?.querySelectorAll<HTMLElement>('[role="menuitemradio"]');
    const checked = menuRef.current?.querySelector<HTMLElement>('[aria-checked="true"]');
    (checked ?? items?.[0])?.focus();
    const onDown = (event: PointerEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener('pointerdown', onDown);
    return () => document.removeEventListener('pointerdown', onDown);
  }, [open]);

  const close = (focusButton: boolean) => {
    setOpen(false);
    if (focusButton) buttonRef.current?.focus();
  };

  const onMenuKey = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    const items = Array.from(menuRef.current?.querySelectorAll<HTMLElement>('[role="menuitemradio"]') ?? []);
    const index = items.indexOf(document.activeElement as HTMLElement);
    if (event.key === 'Escape') {
      event.preventDefault();
      event.stopPropagation();
      close(true);
    } else if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault();
      const step = event.key === 'ArrowDown' ? 1 : -1;
      items[(index + step + items.length) % items.length]?.focus();
    } else if (event.key === 'Home') {
      event.preventDefault();
      items[0]?.focus();
    } else if (event.key === 'End') {
      event.preventDefault();
      items[items.length - 1]?.focus();
    } else if (event.key === 'Tab') {
      setOpen(false);
    }
  };

  return (
    <div className="pmenu" ref={rootRef}>
      <button
        ref={buttonRef}
        type="button"
        className="pbar-btn pbar-text"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        aria-label={`${label}：${buttonText}`}
        disabled={disabled}
        onClick={() => setOpen(o => !o)}
      >
        {buttonText}
        <Icon name="chevronDown" size={14} />
      </button>
      {open && (
        <div ref={menuRef} id={menuId} role="menu" aria-label={label} className="pmenu-list" onKeyDown={onMenuKey}>
          <span className="pmenu-title" aria-hidden="true">
            {label}
          </span>
          {options.map(option => (
            <button
              key={String(option.value)}
              type="button"
              role="menuitemradio"
              aria-checked={option.value === value}
              tabIndex={-1}
              className="pmenu-item"
              onClick={() => {
                onChange(option.value);
                close(true);
              }}
            >
              <span className="pmenu-check" aria-hidden="true">
                {option.value === value && <Icon name="check" size={16} />}
              </span>
              <span>{option.label}</span>
              {option.hint && <span className="pmenu-hint">{option.hint}</span>}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
