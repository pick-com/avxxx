// Where a popup menu goes next to its trigger (components/CardMenu.tsx).
//
// The card 「⋯」 menu is portalled out of its card, so horizontally
// scrolling shelves (overflow: auto) no longer clip it, and placed with
// position: fixed against the trigger's viewport rect. It opens below the
// trigger, aligned to its start edge (cards) or end edge (rows), and flips
// above when there is no room below but there is above; it is then shifted
// to stay `margin` px inside the viewport, and when it fits neither below
// nor above it takes the taller side and scrolls (maxHeight).

export type Rect = {top: number; left: number; bottom: number; right: number};
export type Size = {width: number; height: number};

export type Placement = {
  top: number;
  left: number;
  side: 'below' | 'above';
  /** Set when the menu is taller than the room on its side (it scrolls). */
  maxHeight?: number;
};

export function placeMenu(trigger: Rect, menu: Size, viewport: Size, align: 'start' | 'end' = 'start', gap = 4, margin = 8): Placement {
  const below = viewport.height - margin - (trigger.bottom + gap);
  const above = trigger.top - gap - margin;
  let side: Placement['side'] = 'below';
  if (menu.height > below && (menu.height <= above || above > below)) side = 'above';
  const room = Math.max(0, side === 'below' ? below : above);
  const height = Math.min(menu.height, room);
  const top = side === 'below' ? trigger.bottom + gap : trigger.top - gap - height;

  let left = align === 'end' ? trigger.right - menu.width : trigger.left;
  const maxLeft = viewport.width - margin - menu.width;
  if (left > maxLeft) left = maxLeft;
  if (left < margin) left = margin;

  const out: Placement = {top: Math.round(top), left: Math.round(left), side};
  if (menu.height > room) out.maxHeight = Math.floor(room);
  return out;
}

/** The area left visible by the viewport and every clipping (overflow) ancestor: their intersection. */
export function visibleArea(viewport: Size, clips: Rect[]): Rect {
  const area: Rect = {top: 0, left: 0, bottom: viewport.height, right: viewport.width};
  for (const c of clips) {
    area.top = Math.max(area.top, c.top);
    area.left = Math.max(area.left, c.left);
    area.bottom = Math.min(area.bottom, c.bottom);
    area.right = Math.min(area.right, c.right);
  }
  return area;
}

/**
 * True when the trigger has scrolled completely out of `area` (visibleArea:
 * out of the viewport, or out of its scrolling shelf); the menu then closes.
 */
export function offscreen(trigger: Rect, area: Rect): boolean {
  return trigger.bottom <= area.top || trigger.top >= area.bottom || trigger.right <= area.left || trigger.left >= area.right;
}
