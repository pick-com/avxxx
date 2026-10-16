import type {LangCode} from './api';

// Per-request render scope for server rendering.
//
// t(), uiLang() and the Intl formatters read module state that the browser
// sets once per page. On the SSR server many requests render concurrently
// (React schedules work with setImmediate), so module state would leak from
// one request into another. entry.server.tsx runs each render inside an
// AsyncLocalStorage scope and registers its getter here; this module stays
// free of node imports so the browser bundle can include it.

export type RenderScope = {lang: LangCode; dict: Partial<Record<string, string>>};

let getter: (() => RenderScope | undefined) | null = null;

export function setScopeGetter(fn: () => RenderScope | undefined) {
  getter = fn;
}

/** The current server render's scope; always undefined in the browser. */
export const renderScope = (): RenderScope | undefined => (getter ? getter() : undefined);

export const isServer = typeof document === 'undefined';
