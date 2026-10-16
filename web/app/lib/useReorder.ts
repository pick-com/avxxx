import {useRef, useState, type KeyboardEvent, type PointerEvent as ReactPointerEvent, type RefObject} from 'react';

// Drag (mouse / touch) and keyboard (↑ / ↓ / Home / End on the focused
// handle) reordering of a list of rows, as on NavLater (routes/later.tsx).
// Rows are the `rowSelector` elements inside listRef, handles the
// `.drag-handle` buttons. onCommit receives the new order once a drag or a
// key press ends; announce() is called with the moved item and its new
// 0-based index for a polite live region.

type Item = {slug: string; title: string};

export function useReorder<T extends Item>(
  items: T[],
  setItems: (next: T[]) => void,
  opts: {listRef: RefObject<HTMLElement | null>; rowSelector: string; onCommit: (next: T[]) => void; announce: (item: T, index: number, viaKeyboard: boolean) => void},
) {
  const [dragging, setDragging] = useState<string | null>(null);
  const drag = useRef<{slug: string; pointer: number; startOrder: string[]} | null>(null);

  const move = (from: number, to: number): T[] => {
    const next = items.slice();
    const [it] = next.splice(from, 1);
    next.splice(to, 0, it);
    setItems(next);
    return next;
  };

  const onKeyDown = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    const delta = event.key === 'ArrowUp' ? -1 : event.key === 'ArrowDown' ? 1 : 0;
    const to = event.key === 'Home' ? 0 : event.key === 'End' ? items.length - 1 : index + delta;
    if ((delta === 0 && event.key !== 'Home' && event.key !== 'End') || to < 0 || to >= items.length || to === index) return;
    event.preventDefault();
    const target = event.currentTarget;
    const item = items[index];
    const next = move(index, to);
    opts.announce(item, to, true);
    opts.onCommit(next);
    // Keep focus on the moved row's handle after React re-renders.
    window.requestAnimationFrame(() => {
      const handles = opts.listRef.current?.querySelectorAll<HTMLButtonElement>('.drag-handle');
      (handles?.[to] ?? target).focus();
    });
  };

  const onPointerDown = (event: ReactPointerEvent<HTMLButtonElement>, slug: string) => {
    if (event.button !== 0) return;
    event.currentTarget.setPointerCapture(event.pointerId);
    drag.current = {slug, pointer: event.pointerId, startOrder: items.map(i => i.slug)};
    setDragging(slug);
  };

  const onPointerMove = (event: ReactPointerEvent<HTMLButtonElement>) => {
    const d = drag.current;
    if (!d || d.pointer !== event.pointerId || !opts.listRef.current) return;
    const rows = Array.from(opts.listRef.current.querySelectorAll<HTMLElement>(opts.rowSelector));
    const from = items.findIndex(i => i.slug === d.slug);
    let to = from;
    for (let i = 0; i < rows.length; i++) {
      const rect = rows[i].getBoundingClientRect();
      if (event.clientY < rect.top + rect.height / 2) {
        to = i > from ? i - 1 : i;
        break;
      }
      to = i;
    }
    if (to !== from && to >= 0) move(from, to);
  };

  const onPointerUp = (event: ReactPointerEvent<HTMLButtonElement>) => {
    const d = drag.current;
    if (!d || d.pointer !== event.pointerId) return;
    drag.current = null;
    setDragging(null);
    const index = items.findIndex(i => i.slug === d.slug);
    if (index >= 0 && items.map(i => i.slug).join('\n') !== d.startOrder.join('\n')) {
      opts.announce(items[index], index, false);
      opts.onCommit(items);
    }
  };

  const handleProps = (item: T, index: number) => ({
    onKeyDown: (event: KeyboardEvent<HTMLButtonElement>) => onKeyDown(event, index),
    onPointerDown: (event: ReactPointerEvent<HTMLButtonElement>) => onPointerDown(event, item.slug),
    onPointerMove,
    onPointerUp,
    onPointerCancel: onPointerUp,
  });

  return {dragging, handleProps};
}

/** A Fisher–Yates shuffled copy (随机播放). */
export function shuffled<T>(list: T[]): T[] {
  const out = list.slice();
  for (let i = out.length - 1; i > 0; i--) {
    const j = Math.floor(Math.random() * (i + 1));
    [out[i], out[j]] = [out[j], out[i]];
  }
  return out;
}
