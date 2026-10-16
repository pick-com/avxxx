import {useEffect, useRef, useState, type ReactNode, type PointerEvent as ReactPointerEvent} from 'react';
import {createPortal} from 'react-dom';

type Snap = 'half' | 'full';

const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';
const EXPAND_PX = 60;
const CLOSE_PX = 90;

/**
 * Mobile bottom sheet (PRD 9 「底部面板优先」: MobileSources, MobileActorSheet).
 * Drag the handle area up to expand, down to collapse or close; Esc and the
 * backdrop close it. Focus is trapped while open and restored on close.
 *
 * `container` lets the player render the sheet inside its fullscreen element,
 * which is the only part of the page visible in fullscreen.
 */
export function Sheet({open, onClose, label, header, children, initialSnap = 'half', container, className = ''}: {
  open: boolean;
  onClose: () => void;
  label: string;
  header?: ReactNode;
  children: ReactNode;
  initialSnap?: Snap;
  container?: HTMLElement | null;
  className?: string;
}) {
  const [snap, setSnap] = useState<Snap>(initialSnap);
  const [drag, setDrag] = useState(0);
  const dragStart = useRef<{y: number; id: number; moved: boolean} | null>(null);
  const suppressClick = useRef(false);
  const dialogRef = useRef<HTMLDivElement>(null);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  useEffect(() => {
    if (open) setSnap(initialSnap);
    setDrag(0);
  }, [open, initialSnap]);

  // Focus trap, Esc, focus restore, background scroll lock.
  useEffect(() => {
    if (!open) return;
    const previous = document.activeElement as HTMLElement | null;
    const dialog = dialogRef.current;
    // The dialog itself takes focus so its label is announced; Tab moves into it.
    dialog?.focus({preventScroll: true});
    const overflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';

    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        event.stopPropagation();
        onCloseRef.current();
        return;
      }
      if (event.key !== 'Tab' || !dialog) return;
      const items = Array.from(dialog.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(el => el.offsetParent !== null);
      if (items.length === 0) {
        event.preventDefault();
        dialog.focus();
        return;
      }
      const head = items[0];
      const tail = items[items.length - 1];
      const active = document.activeElement;
      if (event.shiftKey && (active === head || !dialog.contains(active))) {
        event.preventDefault();
        tail.focus();
      } else if (!event.shiftKey && (active === tail || !dialog.contains(active))) {
        event.preventDefault();
        head.focus();
      }
    };
    document.addEventListener('keydown', onKey, true);
    return () => {
      document.removeEventListener('keydown', onKey, true);
      document.body.style.overflow = overflow;
      if (previous && document.contains(previous)) previous.focus({preventScroll: true});
    };
  }, [open]);

  if (!open || typeof document === 'undefined') return null;

  const onPointerDown = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (event.pointerType === 'mouse' && event.button !== 0) return;
    // Let buttons and links in the header take their own clicks.
    if ((event.target as HTMLElement).closest('a, button:not(.sheet-handle)')) return;
    suppressClick.current = false;
    dragStart.current = {y: event.clientY, id: event.pointerId, moved: false};
    try {
      event.currentTarget.setPointerCapture(event.pointerId);
    } catch {
      /* pointer already gone */
    }
  };
  const onPointerMove = (event: ReactPointerEvent<HTMLDivElement>) => {
    const start = dragStart.current;
    if (!start || start.id !== event.pointerId) return;
    const dy = event.clientY - start.y;
    if (Math.abs(dy) > 4) start.moved = true;
    // Upward drag is damped once fully expanded.
    setDrag(snap === 'full' && dy < 0 ? dy / 4 : dy);
  };
  const onPointerUp = (event: ReactPointerEvent<HTMLDivElement>) => {
    const start = dragStart.current;
    if (!start || start.id !== event.pointerId) return;
    dragStart.current = null;
    const dy = event.clientY - start.y;
    setDrag(0);
    if (!start.moved) return;
    suppressClick.current = true;
    if (dy < -EXPAND_PX) setSnap('full');
    else if (dy > CLOSE_PX) {
      if (snap === 'full' && dy < 260) setSnap('half');
      else onClose();
    }
  };

  const sheet = (
    <div className={`sheet-layer ${container ? 'sheet-layer-contained' : ''}`}>
      <button type="button" className="sheet-backdrop" aria-label="关闭" tabIndex={-1} onClick={onClose} />
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-label={label}
        tabIndex={-1}
        className={`sheet ${className}`}
        data-snap={snap}
        data-dragging={drag !== 0 ? 'true' : 'false'}
        style={drag ? {transform: `translateY(${Math.max(drag, -80)}px)`} : undefined}
      >
        <div
          className="sheet-grip"
          onPointerDown={onPointerDown}
          onPointerMove={onPointerMove}
          onPointerUp={onPointerUp}
          onPointerCancel={onPointerUp}
        >
          <button
            type="button"
            className="sheet-handle"
            aria-label={snap === 'full' ? '收起面板' : '展开面板'}
            aria-expanded={snap === 'full'}
            onClick={() => {
              if (suppressClick.current) {
                suppressClick.current = false;
                return;
              }
              setSnap(s => (s === 'full' ? 'half' : 'full'));
            }}
          >
            <span aria-hidden="true" />
          </button>
          {header && (
            <div className="sheet-header">
              {header}
            </div>
          )}
        </div>
        <div className="sheet-body">{children}</div>
      </div>
    </div>
  );

  return createPortal(sheet, container ?? document.body);
}

/** Tabs used inside sheets (MobileSources). */
export function SheetTabs<T extends string>({tabs, value, onChange, label}: {
  tabs: {id: T; label: string}[];
  value: T;
  onChange: (id: T) => void;
  label: string;
}) {
  return (
    <div className="sheet-tabs" role="tablist" aria-label={label}>
      {tabs.map(tab => (
        <button
          key={tab.id}
          type="button"
          role="tab"
          aria-selected={tab.id === value}
          className="sheet-tab"
          onClick={() => onChange(tab.id)}
          onKeyDown={event => {
            if (event.key !== 'ArrowRight' && event.key !== 'ArrowLeft') return;
            event.preventDefault();
            const index = tabs.findIndex(t => t.id === value);
            const next = tabs[(index + (event.key === 'ArrowRight' ? 1 : tabs.length - 1)) % tabs.length];
            onChange(next.id);
            const list = event.currentTarget.parentElement;
            window.requestAnimationFrame(() => list?.querySelector<HTMLElement>('[aria-selected="true"]')?.focus());
          }}
          tabIndex={tab.id === value ? 0 : -1}
        >
          {tab.label}
        </button>
      ))}
    </div>
  );
}
