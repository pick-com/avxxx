// Playable entries for the Player, built from play-config sources (tech plan
// 3.3) or, on the legacy path, from resolved javtiful streams.

import type {PlayConfig, PlaySource, Stream} from './api';
import {formatClock} from './format';

export type EntryRender = 'video' | 'embed' | 'external';

export type PlayEntry = {
  key: string;
  render: EntryRender;
  url: string;
  /** For render=video: 'hls' or 'file'. */
  protocol: string;
  /** 片源 A / 线路 1 — what the States 04 message names. */
  name: string;
  /** Version label (正片 / 高画质 / 配音 / 其他版本); empty on the legacy path. */
  version: string;
  /** Alternate version: own progress, starts from 0 (PRD 5.2). */
  alt: boolean;
  /** Short spec line: provider label, height, duration, audio, subtitles. */
  hint: string;
  provider?: string;
  sourceId?: number;
};

export const VERSION_LABELS: Record<string, string> = {
  main: '正片',
  high_quality: '高画质',
  dub: '配音',
  alt: '其他版本',
};

const MEDIA_FILE = /\.(m3u8|mp4|m4v|webm|mov)(\?|#|$)/i;
const HLS_FILE = /\.m3u8(\?|#|$)/i;

/** Only absolute http(s) URLs are ever put in a src or href. */
function safeUrl(raw: string | null | undefined): string | null {
  if (!raw) return null;
  try {
    const url = new URL(raw, window.location.href);
    return url.protocol === 'https:' || url.protocol === 'http:' ? url.href : null;
  } catch {
    return null;
  }
}

function renderFor(source: PlaySource, url: string): EntryRender | null {
  switch (source.kind) {
    case 'external_link':
      return 'external';
    case 'official_embed':
      return 'embed';
    case 'official_preview':
      return MEDIA_FILE.test(url) ? 'video' : 'embed';
    case 'hls_owned':
      // Served from our storage; play-config exposes a URL only once signed delivery exists.
      return 'video';
    default:
      return null;
  }
}

const letter = (i: number) => (i < 26 ? String.fromCharCode(65 + i) : String(i + 1));

function sourceHint(s: PlaySource, alt: boolean): string {
  const audio = (s.audio_langs ?? []).filter(Boolean);
  const subs = (s.subtitle_langs ?? []).filter(Boolean);
  return [
    s.label,
    s.max_height ? `${s.max_height}p${s.has_hdr ? ' HDR' : ''}` : s.has_hdr ? 'HDR' : '',
    s.duration_seconds ? formatClock(s.duration_seconds) : '',
    audio.length ? audio.join(' / ') : '',
    subs.length ? `${subs.length} 种字幕` : '',
    alt ? '时长不同 · 从头播放' : '',
  ]
    .filter(Boolean)
    .join(' · ');
}

/** play-config sources in the server's play order, minus anything we cannot open. */
export function entriesFromConfig(config: PlayConfig | null | undefined): PlayEntry[] {
  if (!config) return [];
  const out: PlayEntry[] = [];
  for (const s of config.sources ?? []) {
    const url = safeUrl(s?.url);
    if (!url) continue;
    const render = renderFor(s, url);
    if (!render) continue;
    const alt = s.is_alt_version === true || s.version_kind === 'alt';
    out.push({
      key: `src-${s.id}`,
      render,
      url,
      protocol: render === 'video' ? (HLS_FILE.test(url) || s.kind === 'hls_owned' ? 'hls' : 'file') : render,
      name: `片源 ${letter(out.length)}`,
      version: alt ? VERSION_LABELS.alt : VERSION_LABELS[s.version_kind] ?? VERSION_LABELS.main,
      alt,
      hint: sourceHint(s, alt),
      provider: s.provider,
      sourceId: s.id,
    });
  }
  return out;
}

export function entriesFromStreams(streams: Stream[]): PlayEntry[] {
  return streams.map((s, i) => ({
    key: `stream-${i}-${s.url}`,
    render: 'video' as const,
    url: s.url,
    protocol: s.protocol,
    name: `线路 ${i + 1}`,
    version: '',
    alt: false,
    hint: s.quality || s.protocol.toUpperCase() || '',
  }));
}

export const isHlsEntry = (entry: PlayEntry) => entry.protocol === 'hls' || HLS_FILE.test(entry.url);
