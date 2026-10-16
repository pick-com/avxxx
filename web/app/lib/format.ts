// Formatting helpers, localized with Intl for the current UI language (PRD 8:
// dates, numbers, view counts (万 / K), durations). lib/i18n calls
// setFormatLocale(); pages remount when the language changes, so formatters
// are looked up per call (and cached per locale).
//
// Dates from the API are calendar dates (Go time.Time at midnight UTC, or
// Postgres date::text), so they are read and printed in UTC to avoid shifting
// a day in negative-offset time zones.

import {t} from './i18n';
import {renderScope} from './scope';

let browserLocale = 'zh-CN';
// The server render's language comes from its request scope (lib/scope.ts).
const current = () => renderScope()?.lang ?? browserLocale;
const isZh = () => current() === 'zh-CN' || current() === 'zh-TW';

export function setFormatLocale(next: string) {
  browserLocale = next || 'zh-CN';
}

const cache = new Map<string, unknown>();
function cached<T>(key: string, make: (locale: string) => T): T {
  const locale = current();
  const k = `${locale}|${key}`;
  let v = cache.get(k) as T | undefined;
  if (!v) {
    v = make(locale);
    cache.set(k, v);
  }
  return v;
}

const dateFormat = () =>
  cached('date', locale => new Intl.DateTimeFormat(locale, {year: 'numeric', month: '2-digit', day: '2-digit', timeZone: 'UTC'}));
const localDateFormat = () => cached('localDate', locale => new Intl.DateTimeFormat(locale, {year: 'numeric', month: '2-digit', day: '2-digit'}));
const numberFormat = () => cached('number', locale => new Intl.NumberFormat(locale));
const compactFormat = () => cached('compact', locale => new Intl.NumberFormat(locale, {notation: 'compact', maximumFractionDigits: 1}));
const relativeFormat = () => cached('relative', locale => new Intl.RelativeTimeFormat(locale, {numeric: 'auto'}));
const scheduleFormat = () => cached('schedule', locale => new Intl.DateTimeFormat(locale, {month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit'}));
const minutesFormat = () => cached('minutes', locale => new Intl.NumberFormat(locale, {style: 'unit', unit: 'minute', unitDisplay: 'long'}));

// zh-CN keeps the design's YYYY-MM-DD; other locales use their own order.
const dashed = (text: string) => (isZh() ? text.replace(/\//g, '-') : text);

function parseDate(value: string | null | undefined): Date | null {
  if (!value) return null;
  const match = /^(\d{4})-(\d{2})-(\d{2})/.exec(value);
  if (!match) return null;
  const date = new Date(Date.UTC(Number(match[1]), Number(match[2]) - 1, Number(match[3])));
  return Number.isNaN(date.getTime()) ? null : date;
}

export function formatDate(value: string | null | undefined): string {
  const date = parseDate(value);
  return date ? dashed(dateFormat().format(date)) : '';
}

export function formatYear(value: string | null | undefined): string {
  const date = parseDate(value);
  return date ? String(date.getUTCFullYear()) : '';
}

/** Whole years between birth date and the given date; null when either is missing. */
export function ageAt(birth: string | null | undefined, at: string | null | undefined): number | null {
  const b = parseDate(birth);
  const d = parseDate(at);
  if (!b || !d) return null;
  let age = d.getUTCFullYear() - b.getUTCFullYear();
  if (d.getUTCMonth() < b.getUTCMonth() || (d.getUTCMonth() === b.getUTCMonth() && d.getUTCDate() < b.getUTCDate())) age -= 1;
  return age >= 0 && age < 120 ? age : null;
}

/** 120 分钟 / 120 minutes. */
export function formatRuntime(minutes: number | null | undefined): string {
  if (!minutes || minutes <= 0) return '';
  return current() === 'zh-CN' ? `${minutes} 分钟` : minutesFormat().format(minutes);
}

export function formatClock(totalSeconds: number): string {
  const s = Math.max(0, Math.floor(totalSeconds));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = String(s % 60).padStart(2, '0');
  return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${sec}` : `${m}:${sec}`;
}

export const formatCount = (n: number) => numberFormat().format(n);

/** View counts and similar: 1.2万 / 12K (PRD 8). */
export const formatCompact = (n: number) => compactFormat().format(n);

/** Local calendar day label for history grouping: 今天 / 昨天 / date. */
export function dayLabel(iso: string, now = new Date()): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return t('time.earlier');
  const startOf = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const diff = Math.round((startOf(now) - startOf(d)) / 86400000);
  if (diff === 0 || diff === 1) return relativeFormat().format(-diff, 'day');
  return dashed(localDateFormat().format(d));
}

/** 刚刚 / N 分钟前 / N 小时前 / 昨天 / date for timestamps (local time). */
export function timeAgo(iso: string, now = new Date()): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  const minutes = Math.floor((now.getTime() - d.getTime()) / 60000);
  if (minutes < 1) return t('time.justNow');
  if (minutes < 60) return relativeFormat().format(-minutes, 'minute');
  const today = Math.round((new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime() - new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime()) / 86400000) === 0;
  if (minutes < 24 * 60 && today) return relativeFormat().format(-Math.floor(minutes / 60), 'hour');
  return dayLabel(iso, now);
}

/** When an upcoming release goes live: scheduled_at in local time, else the release date, else 日期未定. */
export function formatSchedule(scheduledAt: string | null | undefined, releaseDate?: string | null): string {
  const live = (when: string) => t('schedule.live', {when});
  if (scheduledAt) {
    const d = new Date(scheduledAt);
    if (!Number.isNaN(d.getTime())) return live(scheduleFormat().format(d));
  }
  const date = formatDate(releaseDate);
  return date ? live(date) : t('schedule.tba');
}

const languageNames = () =>
  cached('languageNames', locale => {
    try {
      return new Intl.DisplayNames([locale], {type: 'language'});
    } catch {
      return null;
    }
  });

/** 日本語 / Japanese for a language code (playback audio / subtitle languages); the code itself when unknown. */
export function languageName(code: string): string {
  try {
    return languageNames()?.of(code) || code;
  } catch {
    return code;
  }
}

/** Day heading for the 即将上线 list: 今天 / 明天 / 后天 (relative) or the date; YYYY-MM-DD in. */
export function upcomingDayLabel(date: string, now = new Date()): string {
  const d = parseDate(date);
  if (!d) return '';
  const today = Date.UTC(now.getFullYear(), now.getMonth(), now.getDate());
  const diff = Math.round((d.getTime() - today) / 86400000);
  if (diff >= 0 && diff <= 2) return relativeFormat().format(diff, 'day');
  return formatDate(date);
}

/** Stable placeholder colour index (0–9) for artwork we do not have yet. */
export function swatch(key: string): number {
  let hash = 0;
  for (let i = 0; i < key.length; i++) hash = (hash * 31 + key.charCodeAt(i)) >>> 0;
  return hash % 10;
}
