// Labels for the 片源 details card (lib/playSources): the container format and
// a resolved stream's quality. Pure (node --test).

const HLS_FILE = /\.m3u8(\?|#|$)/i;

/**
 * HLS / MP4 / WebM from the protocol, then the URL's extension. Resolved
 * streams declare protocol "mp4" even when the signed URL has no extension.
 */
export function formatName(protocol: string, url: string): string {
  const p = protocol.toLowerCase();
  if (p === 'hls' || HLS_FILE.test(url)) return 'HLS';
  if (p === 'mp4' || /\.(mp4|m4v)(\?|#|$)/i.test(url)) return 'MP4';
  if (p === 'webm' || /\.webm(\?|#|$)/i.test(url)) return 'WebM';
  return '';
}

/** A resolved stream's quality: a bare height ("720") reads as "720p"; other labels as given. */
export function streamQualityLabel(quality: string | null | undefined): string {
  const q = (quality ?? '').trim();
  return /^\d{3,4}$/.test(q) ? `${q}p` : q;
}
