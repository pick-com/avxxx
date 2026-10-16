// Google Cast helpers (app/lib/googleCast.ts: LOAD request, state and error
// mapping) and the <video> shadow (app/lib/castShadow.ts).
// Run: npm test (Node >= 22.18 strips the TypeScript types itself).
import assert from 'node:assert/strict';
import {test} from 'node:test';
import {
  buildCastLoad,
  castButtonFromState,
  castContentType,
  castErrorKind,
  castVariantUrl,
  castableUrl,
  hlsSegmentFormatOf,
  remoteSnapshot,
  shouldLoadCastSdk,
} from '../app/lib/googleCast.ts';
import {emitShadow, isCastShadowed, realMedia, shadowMedia} from '../app/lib/castShadow.ts';

const BASE = 'https://example.test/v/abc-123';

test('the SDK loads only in Chrome-family browsers in a secure context', () => {
  assert.equal(shouldLoadCastSdk({chrome: {}, isSecureContext: true}), true);
  assert.equal(shouldLoadCastSdk({chrome: {}, isSecureContext: false}), false, 'plain http on a LAN IP');
  assert.equal(shouldLoadCastSdk({isSecureContext: true}), false, 'Safari / Firefox');
  assert.equal(shouldLoadCastSdk({chrome: null, isSecureContext: true}), false);
  assert.equal(shouldLoadCastSdk(null), false, 'SSR');
});

test('only absolute http(s) URLs are castable; relative ones resolve against the page', () => {
  assert.equal(castableUrl('/media/a.mp4', BASE), 'https://example.test/media/a.mp4');
  assert.equal(castableUrl('https://cdn.test/a.m3u8', BASE), 'https://cdn.test/a.m3u8');
  assert.equal(castableUrl('blob:https://example.test/1234', BASE), null);
  assert.equal(castableUrl('data:video/mp4;base64,AA', BASE), null);
  assert.equal(castableUrl('', BASE), null);
});

test('content type: HLS when flagged or .m3u8, else by extension, MP4 by default', () => {
  assert.equal(castContentType('https://cdn.test/master', true), 'application/x-mpegURL');
  assert.equal(castContentType('https://cdn.test/a.m3u8?token=1', false), 'application/x-mpegURL');
  assert.equal(castContentType('https://cdn.test/a.mp4?x=.m3u8', false), 'video/mp4', 'the query does not count');
  assert.equal(castContentType('https://cdn.test/a.webm', false), 'video/webm');
  assert.equal(castContentType('https://cdn.test/stream', false), 'video/mp4');
});

test('HLS segment format from a segment URL', () => {
  assert.equal(hlsSegmentFormatOf('https://cdn.test/seg-1.ts?sig=x'), 'ts');
  assert.equal(hlsSegmentFormatOf('seg-1.m4s'), 'fmp4');
  assert.equal(hlsSegmentFormatOf('https://cdn.test/seg-1.mp4'), 'fmp4');
  assert.equal(hlsSegmentFormatOf('https://cdn.test/seg-1'), undefined);
  assert.equal(hlsSegmentFormatOf(undefined), undefined);
});

test('buildCastLoad: URL, type, metadata, start time, autoplay', () => {
  const spec = buildCastLoad(
    {
      url: '/hls/master.m3u8',
      hls: true,
      title: 'ABC-123 Title',
      artist: 'A, B',
      album: 'Maker',
      artwork: {url: '/img/cover.jpg', width: 800, height: 538, srcset: [{url: '/img/cover-400.jpg', width: 400, height: 269}]},
      segmentFormat: 'fmp4',
    },
    754.2,
    BASE,
  );
  assert.equal(spec.contentId, 'https://example.test/hls/master.m3u8');
  assert.equal(spec.contentType, 'application/x-mpegURL');
  assert.equal(spec.streamType, 'BUFFERED');
  assert.equal(spec.hlsSegmentFormat, 'fmp4');
  assert.equal(spec.hlsVideoSegmentFormat, 'fmp4');
  assert.equal(spec.currentTime, 754.2);
  assert.equal(spec.autoplay, true);
  assert.deepEqual(spec.metadata, {
    title: 'ABC-123 Title',
    subtitle: 'A, B',
    studio: 'Maker',
    images: [
      {url: 'https://example.test/img/cover.jpg', width: 800, height: 538},
      {url: 'https://example.test/img/cover-400.jpg', width: 400, height: 269},
    ],
  });
});

test('buildCastLoad: progressive MP4, no artwork, bad start times, uncastable URL', () => {
  const spec = buildCastLoad({url: 'https://cdn.test/a.mp4', hls: false, title: 'T'}, NaN, BASE);
  assert.equal(spec.contentType, 'video/mp4');
  assert.equal(spec.currentTime, 0);
  assert.equal(spec.hlsSegmentFormat, undefined);
  assert.deepEqual(spec.metadata, {title: 'T', images: []});
  assert.equal(buildCastLoad({url: 'https://cdn.test/a.mp4', hls: false, title: 'T'}, -3, BASE).currentTime, 0);
  const ts = buildCastLoad({url: 'https://cdn.test/a.m3u8', hls: true, title: 'T', segmentFormat: 'ts'}, 0, BASE);
  assert.equal(ts.hlsSegmentFormat, 'ts');
  assert.equal(ts.hlsVideoSegmentFormat, undefined);
  assert.equal(buildCastLoad({url: 'blob:https://example.test/1', hls: true, title: 'T'}, 0, BASE), null);
});

test('a quality pick casts its variant unless Auto or it needs a separate audio group', () => {
  assert.equal(castVariantUrl('m.m3u8', null), 'm.m3u8');
  assert.equal(castVariantUrl('m.m3u8', {url: '720.m3u8'}), '720.m3u8');
  assert.equal(castVariantUrl('m.m3u8', {url: '720.m3u8', hasAudioGroup: true}), 'm.m3u8');
  assert.equal(castVariantUrl('m.m3u8', {url: ''}), 'm.m3u8');
});

test('cast state → button', () => {
  assert.equal(castButtonFromState('NO_DEVICES_AVAILABLE', true), 'hidden');
  assert.equal(castButtonFromState('NOT_CONNECTED', true), 'idle');
  assert.equal(castButtonFromState('CONNECTING', true), 'connecting');
  assert.equal(castButtonFromState('CONNECTED', true), 'connected');
  assert.equal(castButtonFromState('CONNECTED', false), 'hidden', 'nothing castable shown');
  assert.equal(castButtonFromState(null, true), 'hidden');
});

test('receiver state → page state', () => {
  assert.deepEqual(remoteSnapshot({playerState: 'PLAYING', isPaused: false, currentTime: 12.5, duration: 3600}), {
    time: 12.5,
    duration: 3600,
    paused: false,
    ended: false,
    failed: false,
    buffering: false,
  });
  assert.equal(remoteSnapshot({playerState: 'PAUSED', isPaused: true}).paused, true);
  assert.equal(remoteSnapshot({playerState: 'BUFFERING', isPaused: true}).paused, false, 'buffering while playing');
  assert.equal(remoteSnapshot({playerState: 'BUFFERING'}).buffering, true);
  const finished = remoteSnapshot({playerState: 'IDLE', idleReason: 'FINISHED'});
  assert.equal(finished.ended, true);
  assert.equal(finished.paused, true);
  assert.equal(remoteSnapshot({playerState: 'IDLE', idleReason: 'ERROR'}).failed, true);
  assert.equal(remoteSnapshot({playerState: 'IDLE', idleReason: 'INTERRUPTED'}).failed, false);
  assert.deepEqual(remoteSnapshot({}), {time: 0, duration: 0, paused: true, ended: false, failed: false, buffering: false});
  assert.equal(remoteSnapshot({playerState: 'PLAYING', currentTime: NaN, duration: Infinity}).duration, 0);
});

test('error mapping', () => {
  assert.equal(castErrorKind('cancel'), 'ignore');
  assert.equal(castErrorKind('LOAD_CANCELLED'), 'ignore');
  assert.equal(castErrorKind('load_media_failed'), 'source');
  assert.equal(castErrorKind('LOAD_FAILED'), 'source');
  assert.equal(castErrorKind({code: 'load_media_failed', description: 'x'}), 'source');
  assert.equal(castErrorKind('session_error', 'load'), 'source', 'a receiver LOAD_FAILED surfaces as session_error');
  assert.equal(castErrorKind('session_error'), 'session');
  assert.equal(castErrorKind('timeout'), 'session');
  assert.equal(castErrorKind('receiver_unavailable'), 'session');
  assert.equal(castErrorKind(undefined), 'session');
});

// A stand-in for HTMLMediaElement: prototype accessors like the real one.
class FakeVideo extends EventTarget {
  #time = 30;
  #paused = false;
  #muted = false;
  #volume = 0.8;
  autoplayValue = true;
  get currentTime() {
    return this.#time;
  }
  set currentTime(v) {
    this.#time = v;
  }
  get duration() {
    return 100;
  }
  get paused() {
    return this.#paused;
  }
  get ended() {
    return false;
  }
  get muted() {
    return this.#muted;
  }
  set muted(v) {
    this.#muted = v;
  }
  get volume() {
    return this.#volume;
  }
  set volume(v) {
    this.#volume = v;
  }
  get autoplay() {
    return this.autoplayValue;
  }
  set autoplay(v) {
    this.autoplayValue = v;
  }
  get buffered() {
    return {length: 1, start: () => 0, end: () => 50};
  }
  play() {
    this.#paused = false;
    return Promise.resolve();
  }
  pause() {
    this.#paused = true;
  }
}

function fakeBackend() {
  const calls = [];
  const state = {time: 300, duration: 3600, paused: false, ended: false, volume: 0.4, muted: false};
  return {
    calls,
    state,
    backend: {
      time: () => state.time,
      duration: () => state.duration,
      paused: () => state.paused,
      ended: () => state.ended,
      volume: () => state.volume,
      muted: () => state.muted,
      play: () => calls.push('play'),
      pause: () => calls.push('pause'),
      seek: to => calls.push(['seek', to]),
      setVolume: v => calls.push(['volume', v]),
      setMuted: m => calls.push(['muted', m]),
    },
  };
}

test('shadowMedia: the element reads and drives the receiver; the real one is paused, muted, no autoplay', async () => {
  const video = new FakeVideo();
  const {backend, calls, state} = fakeBackend();
  const restore = shadowMedia(video, backend);
  assert.equal(isCastShadowed(video), true);
  const real = realMedia(video);
  assert.equal(real.get('paused'), true);
  assert.equal(real.get('muted'), true);
  assert.equal(video.autoplayValue, false);

  assert.equal(video.currentTime, 300);
  assert.equal(video.duration, 3600);
  assert.equal(video.paused, false);
  assert.equal(video.volume, 0.4);
  assert.equal(video.muted, false, 'the TV is not muted even though the local element is');
  assert.equal(video.buffered.length, 0);

  video.currentTime = 310;
  video.volume = 2;
  video.muted = true;
  await video.play();
  video.pause();
  assert.deepEqual(calls, [['seek', 310], ['volume', 1], ['muted', true], 'play', 'pause']);
  assert.equal(real.get('paused'), true, 'the local element never starts');

  state.paused = true;
  const seen = [];
  video.addEventListener('pause', () => seen.push(video.paused));
  emitShadow(video, 'pause');
  assert.deepEqual(seen, [true]);

  assert.equal(shadowMedia(video, backend), restore, 'idempotent');
  assert.deepEqual(restore(), {wasMuted: false});
  assert.equal(isCastShadowed(video), false);
  assert.equal(video.currentTime, 30, 'real properties again');
  assert.equal(video.muted, false, 'unmuted again');
  emitShadow(video, 'pause');
  assert.deepEqual(seen, [true], 'no events once restored');
});

test('shadowMedia keeps a muted element muted after restore', () => {
  const video = new FakeVideo();
  video.muted = true;
  const restore = shadowMedia(video, fakeBackend().backend);
  assert.deepEqual(restore(), {wasMuted: true});
  assert.equal(video.muted, true);
});
