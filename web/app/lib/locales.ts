import type {LangCode} from './api';

// Bundled UI translations (app/locales/{lang}.json): flat key → string maps
// with the same keys as the zh-CN source (messages*.zh-CN.ts), kept complete
// by `npm run i18n:check`. They are the baseline for each language; reviewed
// ui_strings rows from GET /api/i18n/ui/{lang} override them key by key, and
// a key missing from both falls back to zh-CN (lib/i18n.tsx t()).
//
// Each file is its own chunk, loaded only for its language.

type Dict = Partial<Record<string, string>>;

const loaders = import.meta.glob<Dict>('../locales/*.json', {import: 'default'});
const loaded = new Map<LangCode, Dict>();

export async function bundledDict(lang: LangCode): Promise<Dict> {
  const hit = loaded.get(lang);
  if (hit) return hit;
  const load = loaders[`../locales/${lang}.json`];
  if (!load) return {};
  try {
    const dict = await load();
    loaded.set(lang, dict);
    return dict;
  } catch {
    return {};
  }
}

/** Bundled baseline overlaid with the reviewed strings from the API. */
export async function withBundled(lang: LangCode, reviewed: Dict): Promise<Dict> {
  const base = await bundledDict(lang);
  return Object.keys(base).length ? {...base, ...reviewed} : reviewed;
}
