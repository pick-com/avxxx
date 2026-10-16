import type {LangCode} from './api';

// Launch languages (PRD 8; store.SettingsLangs), each in its own script.
export const LANG_LABELS: Record<LangCode, string> = {
  'zh-CN': '简体中文',
  'zh-TW': '繁體中文',
  en: 'English',
  ja: '日本語',
  ko: '한국어',
  vi: 'Tiếng Việt',
  es: 'Español',
  th: 'ไทย',
};
export const LANGS = Object.keys(LANG_LABELS) as LangCode[];
