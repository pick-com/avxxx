// One playback's analytics (PRD 14.2 / 14.3): play_request → first_frame →
// heartbeat every 10 s (with pause / resume / seek / rebuffer / quality /
// rate / source switches in between) → play_end, plus progress milestones.
//
// The Player creates a PlayTracker when a <video> source with a play-config
// source id starts, attaches it to each <video> element it renders, and ends
// it when the release changes, the page goes away or every source failed.
// Embeds and external links cannot be observed and are not tracked; the
// legacy javtiful path has no source ids, so it is not tracked either (a
// play_request without source_id would be quarantined).
//
// Seek method: whoever moves currentTime programmatically sets
// video.dataset.seekMethod first ('keyboard', 'double_tap', 'skip' for the
// resume / source-switch restore); anything else is a native-controls drag.
//
// Replay table (PRD 14.3): the work's timeline is cut into 200 buckets; a
// bucket counts +1 each time ≥ 50% of it is actually played in one pass at
// rate ≤ 2× with the page in the foreground or in picture-in-picture.
// Alternate versions (其他版本, different runtime) are not counted. The
// table goes out as a delta: long videos (> 20 min) attach the counts since
// the last report to a heartbeat every 5 minutes, and play_end carries the
// rest, so the sum of `buckets` over one play_id is the full table.
//
// `buckets` encoding ("r1"): run-length tokens joined by ",", each
// "<count>" or "<count>*<run>", counts capped at 255, e.g. "r1:0*40,1*150,2*10"
// (200 buckets). Omitted when the delta is all zeros.

import {flush, networkType, newPlayId, onUnload, track, type ChangeMode, type EventProps, type PlayEndReason, type PlayEntryKind} from './analytics';

const HEARTBEAT_MS = 10_000;
const BUCKETS = 200;
const BUCKET_REPORT_EVERY = 30; // heartbeats (= 5 min)
const LONG_VIDEO_S = 20 * 60;
const SEEK_SETTLE_MS = 400;
const MANUAL_QUALITY_WINDOW_MS = 10_000;

export function encodeBuckets(counts: ArrayLike<number>): string | undefined {
  let any = false;
  const tokens: string[] = [];
  let i = 0;
  while (i < counts.length) {
    const v = Math.min(255, counts[i]);
    if (v > 0) any = true;
    let run = 1;
    while (i + run < counts.length && Math.min(255, counts[i + run]) === v) run++;
    tokens.push(run > 1 ? `${v}*${run}` : String(v));
    i += run;
  }
  return any ? `r1:${tokens.join(',')}` : undefined;
}

type TrackerInit = {
  releaseId: number;
  entry: PlayEntryKind;
  reason?: string;
  /** Runtime from play-config, used until the <video> knows its duration. */
  durationHint?: number | null;
  getFullscreen: () => boolean;
  getPip: () => boolean;
  /** true while the quality menu is on 自动 (ABR). */
  isAutoQuality: () => boolean;
};

type Source = {id: number; alt: boolean};

const ms = (seconds: number) => Math.max(0, Math.round(seconds * 1000));

export class PlayTracker {
  readonly playId = newPlayId();
  private init: TrackerInit;
  private source: Source;
  private video: HTMLVideoElement | null = null;
  private detach: (() => void) | null = null;
  private ended = false;
  private closed = false;
  private requestAt = performance.now();
  private firstFrameSent = false;
  private lastTime = 0;
  private lastWall = 0;
  private watchedMs = 0;
  private pausedAt: number | null = null;
  private waitingAt: number | null = null;
  private pendingSeek: {from: number; to: number; method: EventProps['seek']['method'] | 'skip'; timer?: number} | null = null;
  private milestones = new Set<number>();
  private counts = new Uint16Array(BUCKETS);
  private reported = new Uint16Array(BUCKETS);
  private pass = {bucket: -1, played: 0, counted: false};
  private heartbeatTimer: number | undefined;
  private heartbeats = 0;
  private rate = 1;
  private height = 0;
  private manualQualityAt = 0;
  private reachedEnd = false;
  private removeUnload: () => void;

  constructor(init: TrackerInit, source: Source, rate: number) {
    this.init = init;
    this.source = source;
    this.rate = rate;
    this.emit('play_request', {release_id: init.releaseId, source_id: source.id, entry: init.entry, reason: init.reason});
    this.removeUnload = onUnload(() => this.end(this.reachedEnd ? 'completed' : 'closed'));
  }

  get sourceId() {
    return this.source.id;
  }

  get isClosed() {
    return this.closed;
  }

  private emit<E extends keyof EventProps>(event: E, props: EventProps[E]) {
    if (this.closed) return;
    track(event, props, {playId: this.playId});
  }

  private duration(): number {
    const d = this.video?.duration;
    if (d && Number.isFinite(d) && d > 0) return d;
    return this.init.durationHint && this.init.durationHint > 0 ? this.init.durationHint : 0;
  }

  private quality(): string {
    const h = this.video?.videoHeight ?? 0;
    return h > 0 ? `${h}p` : 'unknown';
  }

  /** Listen to one <video> element (a new element per source). */
  attach(video: HTMLVideoElement) {
    if (this.closed) return;
    this.detach?.();
    this.video = video;
    this.firstFrameSent = false;
    this.lastTime = video.currentTime;
    this.lastWall = 0;
    this.height = 0;
    this.pass = {bucket: -1, played: 0, counted: false};
    const on = <K extends keyof HTMLVideoElementEventMap>(type: K, fn: (e: HTMLVideoElementEventMap[K]) => void) => {
      video.addEventListener(type, fn);
      return () => video.removeEventListener(type, fn);
    };
    const offs = [
      on('playing', () => this.onPlaying()),
      on('waiting', () => this.onWaiting()),
      on('pause', () => this.onPause()),
      on('play', () => this.onPlay()),
      on('seeking', () => this.onSeeking()),
      on('seeked', () => this.onSeeked()),
      on('timeupdate', () => this.onTime()),
      on('ratechange', () => this.onRate()),
      on('resize', () => this.onResize()),
      on('ended', () => this.onEnded()),
    ];
    this.detach = () => {
      offs.forEach(off => off());
      this.detach = null;
    };
    this.startHeartbeat();
  }

  private startHeartbeat() {
    window.clearInterval(this.heartbeatTimer);
    this.heartbeatTimer = window.setInterval(() => this.heartbeat(), HEARTBEAT_MS);
  }

  private heartbeat() {
    const v = this.video;
    if (!v || v.paused || v.ended || this.closed) return;
    this.heartbeats++;
    let buckets: string | undefined;
    if (this.heartbeats % BUCKET_REPORT_EVERY === 0 && this.duration() > LONG_VIDEO_S) buckets = this.takeDelta();
    this.emit('heartbeat', {
      position_ms: ms(v.currentTime),
      rate: v.playbackRate,
      quality: this.quality(),
      fullscreen: this.init.getFullscreen(),
      pip: this.init.getPip(),
      foreground: document.visibilityState === 'visible',
      buckets,
    });
  }

  private takeDelta(): string | undefined {
    const delta = new Uint16Array(BUCKETS);
    for (let i = 0; i < BUCKETS; i++) {
      delta[i] = this.counts[i] - this.reported[i];
      this.reported[i] = this.counts[i];
    }
    return encodeBuckets(delta);
  }

  private onPlaying() {
    const v = this.video!;
    if (!this.firstFrameSent) {
      this.firstFrameSent = true;
      this.emit('first_frame', {
        source_id: this.source.id,
        quality: this.quality(),
        elapsed_ms: Math.round(performance.now() - this.requestAt),
        network: networkType(),
      });
    }
    if (this.waitingAt !== null) {
      this.emit('rebuffer', {source_id: this.source.id, duration_ms: Math.round(performance.now() - this.waitingAt), position_ms: ms(v.currentTime)});
      this.waitingAt = null;
    }
    this.lastWall = performance.now();
  }

  private onWaiting() {
    const v = this.video!;
    // Only stalls during playback; the wait after a seek is the seek's cost.
    if (!this.firstFrameSent || v.seeking || this.pendingSeek) return;
    this.waitingAt = performance.now();
  }

  private onPause() {
    const v = this.video!;
    if (v.ended || this.closed) return;
    this.pausedAt = performance.now();
    this.waitingAt = null;
    this.emit('pause', {position_ms: ms(v.currentTime)});
  }

  private onPlay() {
    const v = this.video!;
    if (this.pausedAt === null) return;
    this.emit('resume', {position_ms: ms(v.currentTime), paused_ms: Math.round(performance.now() - this.pausedAt)});
    this.pausedAt = null;
  }

  private onSeeking() {
    const v = this.video!;
    const method = (v.dataset.seekMethod as EventProps['seek']['method'] | 'skip' | undefined) ?? 'drag';
    delete v.dataset.seekMethod;
    this.waitingAt = null;
    this.pass = {bucket: -1, played: 0, counted: false};
    if (this.pendingSeek) {
      window.clearTimeout(this.pendingSeek.timer);
      this.pendingSeek.to = v.currentTime;
      if (method !== 'drag') this.pendingSeek.method = method;
      return;
    }
    this.pendingSeek = {from: this.lastTime, to: v.currentTime, method};
  }

  private onSeeked() {
    const p = this.pendingSeek;
    if (!p) return;
    window.clearTimeout(p.timer);
    p.to = this.video!.currentTime;
    // Native scrubbing fires many seeks; report one once it settles.
    p.timer = window.setTimeout(() => this.settleSeek(), SEEK_SETTLE_MS);
  }

  private settleSeek() {
    const p = this.pendingSeek;
    this.pendingSeek = null;
    if (!p) return;
    this.lastTime = this.video?.currentTime ?? p.to;
    if (p.method === 'skip' || Math.abs(p.to - p.from) < 0.5) return;
    this.emit('seek', {from_ms: ms(p.from), to_ms: ms(p.to), method: p.method});
  }

  private onTime() {
    const v = this.video!;
    const t = v.currentTime;
    const now = performance.now();
    if (v.seeking || this.pendingSeek) {
      this.lastWall = now;
      return;
    }
    const mediaDelta = t - this.lastTime;
    const wallDelta = this.lastWall ? now - this.lastWall : 0;
    this.lastTime = t;
    this.lastWall = now;
    if (v.paused || mediaDelta <= 0 || mediaDelta > 3) return;
    if (wallDelta > 0 && wallDelta < 2000) this.watchedMs += wallDelta;

    const duration = this.duration();
    if (duration <= 0) return;
    // Replay table: normal playback only (PRD 14.3).
    const normal = v.playbackRate <= 2 && (document.visibilityState === 'visible' || this.init.getPip());
    if (normal && !this.source.alt) {
      const size = duration / BUCKETS;
      const b = Math.min(BUCKETS - 1, Math.floor(t / size));
      if (b !== this.pass.bucket) this.pass = {bucket: b, played: 0, counted: false};
      this.pass.played += mediaDelta;
      if (!this.pass.counted && this.pass.played >= size / 2) {
        this.pass.counted = true;
        this.counts[b] = Math.min(65535, this.counts[b] + 1);
      }
    }
    const pct = (t / duration) * 100;
    for (const m of [25, 50, 75] as const) {
      if (pct >= m && !this.milestones.has(m)) {
        this.milestones.add(m);
        this.emit('progress', {release_id: this.init.releaseId, milestone: m});
      }
    }
  }

  private onRate() {
    const v = this.video!;
    const to = v.playbackRate;
    if (to === this.rate) return;
    const mode: ChangeMode = v.dataset.rateMode === 'auto' ? 'auto' : 'manual';
    delete v.dataset.rateMode;
    this.emit('rate_change', {from: this.rate, to, mode});
    this.rate = to;
  }

  /** Adaptive bitrate moved to another rendition (videoHeight changed). */
  private onResize() {
    const h = this.video?.videoHeight ?? 0;
    const prev = this.height;
    this.height = h;
    if (!prev || !h || prev === h) return;
    if (!this.init.isAutoQuality() || performance.now() - this.manualQualityAt < MANUAL_QUALITY_WINDOW_MS) return;
    this.emit('quality_change', {from: `${prev}p`, to: `${h}p`, mode: 'auto'});
  }

  private onEnded() {
    this.reachedEnd = true;
    if (!this.milestones.has(100)) {
      for (const m of [25, 50, 75] as const) {
        if (!this.milestones.has(m)) {
          this.milestones.add(m);
          this.emit('progress', {release_id: this.init.releaseId, milestone: m});
        }
      }
      this.milestones.add(100);
      this.emit('progress', {release_id: this.init.releaseId, milestone: 100});
    }
  }

  /** The viewer picked a quality in the menu ('auto' or e.g. '720p'). */
  manualQuality(from: string, to: string) {
    if (from === to) return;
    this.manualQualityAt = performance.now();
    this.emit('quality_change', {from, to, mode: 'manual'});
  }

  /** A switch between two play-config sources (PRD 5.2). */
  switchSource(to: Source, mode: ChangeMode, reason: string) {
    if (to.id === this.source.id) return;
    this.emit('source_switch', {from_source_id: this.source.id, to_source_id: to.id, mode, reason});
    this.source = to;
    this.requestAt = performance.now();
  }

  get hasEnded() {
    return this.reachedEnd;
  }

  /** play_end; idempotent. */
  end(reason: PlayEndReason, errorCode?: string) {
    if (this.closed) return;
    if (this.pendingSeek) this.settleSeek();
    // A play cut off while stalled still reports the stall.
    if (this.waitingAt !== null && this.video) {
      this.emit('rebuffer', {source_id: this.source.id, duration_ms: Math.round(performance.now() - this.waitingAt), position_ms: ms(this.video.currentTime)});
      this.waitingAt = null;
    }
    const buckets = this.takeDelta();
    this.emit('play_end', {reason, watched_seconds: Math.floor(this.watchedMs / 1000), buckets, error_code: errorCode});
    this.closed = true;
    window.clearInterval(this.heartbeatTimer);
    this.detach?.();
    this.removeUnload();
    void flush();
  }
}

// The release page asks for the next play_end reason before it navigates for
// autoplay (PRD 5.4); the Player's cleanup reads it once.
let nextEndReason: PlayEndReason | null = null;
let nextEntry: PlayEntryKind | null = null;

export function markAutoplayNavigation() {
  nextEndReason = 'autoplay_next';
  nextEntry = 'autoplay';
}

export function takeEndReason(): PlayEndReason | null {
  const r = nextEndReason;
  nextEndReason = null;
  return r;
}

export function takeEntryOverride(): PlayEntryKind | null {
  const e = nextEntry;
  nextEntry = null;
  return e;
}
