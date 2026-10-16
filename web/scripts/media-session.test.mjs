// Media Session helpers (app/lib/mediaSession.ts): artwork, seek mapping,
// position-state throttling and previous / next track targets.
// Run: npm test (Node >= 22.18 strips the TypeScript types itself).
import assert from 'node:assert/strict';
import {test} from 'node:test';
import {PositionGate, adjacentTracks, artistLine, artworkFor, positionStateOf, seekByTarget, seekToTarget} from '../app/lib/mediaSession.ts';

const BASE = 'https://example.test/releases/abc-123?list=later';

test('artwork: absolute URLs, sizes when known, srcset smallest first, no duplicates', () => {
  assert.deepEqual(artworkFor(null, BASE), []);
  assert.deepEqual(artworkFor({url: '/img/c.jpg'}, BASE), [{src: 'https://example.test/img/c.jpg', type: 'image/jpeg'}]);
  assert.deepEqual(
    artworkFor(
      {
        url: '/img/c.webp',
        width: 800,
        height: 538,
        srcset: [
          {url: '/img/c-400.webp', width: 400, height: 269},
          {url: '/img/c.webp', width: 800, height: 538},
          {url: 'https://cdn.example.test/c-200.png?v=2', width: 200, height: 134},
        ],
      },
      BASE,
    ),
    [
      {src: 'https://cdn.example.test/c-200.png?v=2', sizes: '200x134', type: 'image/png'},
      {src: 'https://example.test/img/c-400.webp', sizes: '400x269', type: 'image/webp'},
      {src: 'https://example.test/img/c.webp', sizes: '800x538', type: 'image/webp'},
    ],
  );
  // Unknown size and extension: just the URL.
  assert.deepEqual(artworkFor({url: 'cover', width: 0, height: 0}, BASE), [{src: 'https://example.test/releases/cover'}]);
});

test('artist line: first three distinct names', () => {
  assert.equal(artistLine(['A', ' B ', '', null, 'A', 'C', 'D']), 'A, B, C');
  assert.equal(artistLine([]), '');
});

test('seekbackward / seekforward honour seekOffset, default 10 s, clamp', () => {
  assert.equal(seekByTarget('seekforward', undefined, 30, 100), 40);
  assert.equal(seekByTarget('seekbackward', undefined, 30, 100), 20);
  assert.equal(seekByTarget('seekforward', 15, 30, 100), 45);
  assert.equal(seekByTarget('seekbackward', 15, 5, 100), 0);
  assert.equal(seekByTarget('seekforward', 30, 90, 100), 100);
  assert.equal(seekByTarget('seekforward', 0, 90, 100), 100, 'zero offset falls back to the step');
  assert.equal(seekByTarget('seekforward', 30, 90, NaN), 120, 'unknown duration: no upper bound');
});

test('seekto clamps and ignores a missing seekTime', () => {
  assert.equal(seekToTarget(undefined, 100), null);
  assert.equal(seekToTarget(NaN, 100), null);
  assert.equal(seekToTarget(50, 100), 50);
  assert.equal(seekToTarget(-3, 100), 0);
  assert.equal(seekToTarget(150, 100), 100);
});

test('position state is valid or null', () => {
  assert.equal(positionStateOf(NaN, 3, 1), null);
  assert.equal(positionStateOf(Infinity, 3, 1), null);
  assert.equal(positionStateOf(0, 0, 1), null);
  assert.deepEqual(positionStateOf(100, 120, 1.5), {duration: 100, position: 100, playbackRate: 1.5});
  assert.deepEqual(positionStateOf(100, -1, 0), {duration: 100, position: 0, playbackRate: 1});
});

test('PositionGate throttles timeupdate, passes changes, drift and forced events', () => {
  const gate = new PositionGate(5000, 1.5);
  const st = (position, rate = 1, duration = 100) => ({duration, position, playbackRate: rate});
  assert.equal(gate.check(st(0), true, 0), true, 'first state goes out');
  assert.equal(gate.check(st(0.25), true, 250), false);
  assert.equal(gate.check(st(2), true, 2000), false, 'on track with the extrapolation');
  assert.equal(gate.check(st(2), true, 4000), true, 'stalled 2 s behind: drift');
  assert.equal(gate.check(st(3), true, 5000), false);
  assert.equal(gate.check(st(3.5), true, 5500), false);
  assert.equal(gate.check(st(9), true, 9000), true, 'resync after 5 s');
  assert.equal(gate.check(st(9.2, 2), true, 9200), true, 'rate change');
  assert.equal(gate.check(st(9.4, 2, 120), true, 9300), true, 'duration change');
  assert.equal(gate.check(st(9.4, 2, 120), false, 9400), true, 'paused');
  assert.equal(gate.check(st(9.4, 2, 120), false, 12000), false, 'paused, not moving');
  assert.equal(gate.check(st(9.4, 2, 120), false, 12100, true), true, 'forced (seeked / play / pause)');
  assert.equal(gate.check(null, false, 13000), true, 'clears once');
  assert.equal(gate.check(null, false, 13100), false, 'then stays quiet');
});

test('previous / next: queue, then series, then related', () => {
  const queue = {name: 'later', slugs: ['a', 'b', 'c']};
  assert.deepEqual(adjacentTracks({slug: 'b', queue}), {previous: {slug: 'a', query: '?list=later'}, next: {slug: 'c', query: '?list=later'}});
  // Last queue item: next falls through to the series, then related.
  assert.deepEqual(adjacentTracks({slug: 'c', queue, episodes: ['x', 'c', 'y'], related: {slug: 'r', reason: 'similar'}}), {
    previous: {slug: 'b', query: '?list=later'},
    next: {slug: 'y', query: '', reason: 'same_series'},
  });
  assert.deepEqual(adjacentTracks({slug: 'c', queue, related: {slug: 'r', reason: 'similar'}}).next, {slug: 'r', query: '', reason: 'similar'});
  // Series episode in the queue keeps ?list=.
  assert.deepEqual(adjacentTracks({slug: 'e2', queue: {name: 'playlist:7', slugs: ['e1', 'e2']}, episodes: ['e1', 'e2', 'e3']}), {
    previous: {slug: 'e1', query: '?list=playlist:7'},
    next: {slug: 'e3', query: '', reason: 'same_series'},
  });
  // Nothing: both unset (the OS hides the buttons).
  assert.deepEqual(adjacentTracks({slug: 'solo'}), {});
  assert.deepEqual(adjacentTracks({slug: 'solo', related: {slug: 'solo'}}), {});
  // Not in the queue: the queue is ignored.
  assert.deepEqual(adjacentTracks({slug: 'z', queue}), {});
});
