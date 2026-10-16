// Refresh-on-failure for resolved (legacy-path) streams: a cached direct URL
// whose signed token expired fails with a media error, an hls.js fatal error
// or a 403 / 410. The player then asks POST /api/resolve/...?refresh=1 once
// and retries the new URL from the same position before giving up.
// Kept free of imports so scripts/stream-refresh.test.mjs runs it under
// plain Node.

export type StreamFailure =
  /** <video> error event (a 403 / 410 on a progressive file surfaces here too). */
  | {kind: 'media'}
  /** hls.js fatal media error that recoverMediaError did not fix. */
  | {kind: 'hls-media'}
  /** hls.js fatal network error; status is the HTTP code when known. */
  | {kind: 'hls-network'; status?: number}
  /** Anything else (hls.js unsupported, other fatal types). */
  | {kind: 'other'};

/** HTTP codes that mean "this signed URL is no longer valid". */
const EXPIRED_STATUS = new Set([403, 410]);

/**
 * Whether a failure of `url` should re-resolve with refresh. Only resolved
 * (legacy) streams, only token-like failures, and at most once per URL:
 * `tried` holds every URL already refreshed away from and every URL a
 * refresh produced, so a refreshed URL that fails too never loops.
 */
export function shouldRefreshStream(opts: {legacy: boolean; url: string; tried: ReadonlySet<string>; failure: StreamFailure}): boolean {
  if (!opts.legacy || !opts.url || opts.tried.has(opts.url)) return false;
  switch (opts.failure.kind) {
    case 'media':
    case 'hls-media':
      return true;
    case 'hls-network':
      return opts.failure.status !== undefined && EXPIRED_STATUS.has(opts.failure.status);
    default:
      return false;
  }
}

/**
 * The stream to retry after a refresh: the same quality as the failed one
 * when the new result has it, else the first. null when the refresh gave
 * nothing new to try (no streams, or only URLs already tried).
 */
export function pickRefreshedStream<S extends {url: string; quality?: string}>(streams: readonly S[], failed: {quality?: string}, tried: ReadonlySet<string>): number | null {
  const fresh = streams.map((s, i) => ({s, i})).filter(({s}) => s.url && !tried.has(s.url));
  if (fresh.length === 0) return null;
  const same = failed.quality ? fresh.find(({s}) => (s.quality ?? '') === failed.quality) : undefined;
  return (same ?? fresh[0]).i;
}
