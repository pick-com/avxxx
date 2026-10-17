// Playable entries for the Player, built from play-config sources (tech plan
// 3.3) or, on the legacy path, from resolved javtiful streams.

import type {PlayConfig, PlaySource, Stream} from './api';
import {formatClock, languageName} from './format';
import {t} from './i18n';
import {formatName, streamQualityLabel} from './streamFormat';

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
  /** Source metadata for the 片源 details card (labelled rows, never sample data). */
  details: SourceDetail[];
  provider?: string;
  sourceId?: number;
};

export type SourceDetailKey = 'provider' | 'version' | 'resolution' | 'duration' | 'audio' | 'subtitles' | 'format';
export type SourceDetail = {key: SourceDetailKey; value: string};

const langList = (codes: string[] | null | undefined) =>
  (codes ?? [])
    .filter(Boolean)
    .map(c => languageName(c))
    .join('、');


function resolutionLabel(height: number | null | undefined, hdr: boolean): string {
  if (!height) return hdr ? 'HDR' : '';
  const name = height >= 2160 ? `4K (${height}p)` : `${height}p`;
  return hdr ? `${name} HDR` : name;
}

function sourceDetails(s: PlaySource, version: string, protocol: string, url: string): SourceDetail[] {
  const rows: SourceDetail[] = [
    {key: 'provider', value: s.label || s.provider || ''},
    {key: 'version', value: version},
    {key: 'resolution', value: resolutionLabel(s.max_height, s.has_hdr)},
    {key: 'duration', value: s.duration_seconds ? formatClock(s.duration_seconds) : ''},
    {key: 'audio', value: langList(s.audio_langs)},
    {key: 'subtitles', value: langList(s.subtitle_langs)},
    {key: 'format', value: formatName(protocol, url)},
  ];
  return rows.filter(r => r.value);
}

// Getters, so labels follow the current UI language.
export const VERSION_LABELS: Record<string, string> = {
  get main() {
    return t('play.versionMain');
  },
  get high_quality() {
    return t('play.versionHigh');
  },
  get dub() {
    return t('play.versionDub');
  },
  get alt() {
    return t('play.versionAlt');
  },
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
    subs.length ? t('play.subsCount', {n: subs.length}) : '',
    alt ? t('play.altHint') : '',
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
    const protocol = render === 'video' ? (HLS_FILE.test(url) || s.kind === 'hls_owned' ? 'hls' : 'file') : render;
    const version = alt ? VERSION_LABELS.alt : VERSION_LABELS[s.version_kind] ?? VERSION_LABELS.main;
    out.push({
      key: `src-${s.id}`,
      render,
      url,
      protocol,
      name: t('play.sourceName', {id: letter(out.length)}),
      version,
      alt,
      hint: sourceHint(s, alt),
      details: sourceDetails(s, version, protocol, url),
      provider: s.provider,
      sourceId: s.id,
    });
  }
  return out;
}

export function entriesFromStreams(streams: Stream[], provider = ''): PlayEntry[] {
  return streams.map((s, i) => ({
    key: `stream-${i}-${s.url}`,
    render: 'video' as const,
    url: s.url,
    protocol: s.protocol,
    name: t('play.lineName', {n: i + 1}),
    version: '',
    alt: false,
    hint: streamQualityLabel(s.quality) || s.protocol.toUpperCase() || '',
    details: [
      {key: 'provider' as const, value: provider},
      {key: 'resolution' as const, value: streamQualityLabel(s.quality)},
      {key: 'format' as const, value: formatName(s.protocol, s.url)},
    ].filter(r => r.value),
    provider: provider || undefined,
  }));
}

export const isHlsEntry = (entry: PlayEntry) => entry.protocol === 'hls' || HLS_FILE.test(entry.url);
