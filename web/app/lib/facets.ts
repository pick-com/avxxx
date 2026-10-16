// Playback-source facets (清晰度 / 字幕 / 语言) for the browse filters:
// GET /api/catalog/facets says which values have data, so a filter is only
// offered when it can match something. One request per page load; an older
// backend without the route (or no playback data) yields no facets.

import {getCatalogFacets, isAbort, type CatalogFacets} from './api';
import {useAsync} from './useAsync';

let pending: Promise<CatalogFacets | null> | null = null;

function loadFacets(): Promise<CatalogFacets | null> {
  if (!pending) {
    pending = getCatalogFacets().catch(error => {
      pending = null;
      if (isAbort(error)) throw error;
      return null;
    });
  }
  return pending;
}

/** The facets with data, or null while loading / when there are none. */
export function useCatalogFacets(enabled = true): CatalogFacets | null {
  const res = useAsync(() => loadFacets(), [], enabled);
  const f = res.data;
  if (!f) return null;
  const has = f.heights.length > 0 || f.subtitled > 0 || f.audio_langs.length > 0;
  return has ? f : null;
}
