// Clickable timestamps in comments, reviews and the danmaku list (YouTube):
// `12:34` and `1:02:03` in a text become seek links. Pure (no imports) so
// node --test can load it; components/Timestamps.tsx renders the pieces as
// React nodes, never as HTML.

export type TextPiece = string | {text: string; seconds: number};

// m:ss / mm:ss / mmm:ss, or h:mm:ss / hh:mm:ss. Not part of a longer run of
// digits, colons or word characters ("a1:23", "12:34:56:78", "1:23.5").
const TIME = /(?<![\w:.])(?:(\d{1,2}):([0-5]\d)|(\d{1,3})):([0-5]\d)(?![\w:]|\.\d)/g;

/** Seconds for one matched timestamp, or undefined when it is not a valid time. */
export function timestampSeconds(text: string): number | undefined {
  const m = /^(?:(\d{1,2}):([0-5]\d)|(\d{1,3})):([0-5]\d)$/.exec(text.trim());
  if (!m) return undefined;
  return m[1] !== undefined ? Number(m[1]) * 3600 + Number(m[2]) * 60 + Number(m[4]) : Number(m[3]) * 60 + Number(m[4]);
}

/**
 * Splits `text` into plain strings and timestamps. Times past `maxSeconds`
 * (the release duration, when known) stay plain text.
 */
export function splitTimestamps(text: string, maxSeconds?: number): TextPiece[] {
  const out: TextPiece[] = [];
  let last = 0;
  for (const m of text.matchAll(TIME)) {
    const seconds = timestampSeconds(m[0]);
    if (seconds === undefined) continue;
    if (maxSeconds !== undefined && maxSeconds > 0 && seconds > maxSeconds) continue;
    const at = m.index ?? 0;
    if (at > last) out.push(text.slice(last, at));
    out.push({text: m[0], seconds});
    last = at + m[0].length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}
