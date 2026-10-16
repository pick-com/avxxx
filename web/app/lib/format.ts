// Formatting helpers. Dates from the API are calendar dates (Go time.Time at
// midnight UTC, or Postgres date::text), so everything is read and printed in UTC
// to avoid shifting a day in negative-offset time zones.

function parseDate(value: string | null | undefined): Date | null {
  if (!value) return null;
  const match = /^(\d{4})-(\d{2})-(\d{2})/.exec(value);
  if (!match) return null;
  const date = new Date(Date.UTC(Number(match[1]), Number(match[2]) - 1, Number(match[3])));
  return Number.isNaN(date.getTime()) ? null : date;
}

const dateFormat = new Intl.DateTimeFormat('zh-CN', {year: 'numeric', month: '2-digit', day: '2-digit', timeZone: 'UTC'});

export function formatDate(value: string | null | undefined): string {
  const date = parseDate(value);
  return date ? dateFormat.format(date).replace(/\//g, '-') : '';
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

export function formatRuntime(minutes: number | null | undefined): string {
  return minutes && minutes > 0 ? `${minutes} 分钟` : '';
}

export function formatClock(totalSeconds: number): string {
  const s = Math.max(0, Math.floor(totalSeconds));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = String(s % 60).padStart(2, '0');
  return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${sec}` : `${m}:${sec}`;
}

const numberFormat = new Intl.NumberFormat('zh-CN');
export const formatCount = (n: number) => numberFormat.format(n);

/** Local calendar day label for history grouping: 今天 / 昨天 / YYYY-MM-DD. */
export function dayLabel(iso: string, now = new Date()): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '更早';
  const startOf = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const diff = Math.round((startOf(now) - startOf(d)) / 86400000);
  if (diff === 0) return '今天';
  if (diff === 1) return '昨天';
  return new Intl.DateTimeFormat('zh-CN', {year: 'numeric', month: '2-digit', day: '2-digit'}).format(d).replace(/\//g, '-');
}

/** Stable placeholder colour index (0–9) for artwork we do not have yet. */
export function swatch(key: string): number {
  let hash = 0;
  for (let i = 0; i < key.length; i++) hash = (hash * 31 + key.charCodeAt(i)) >>> 0;
  return hash % 10;
}
