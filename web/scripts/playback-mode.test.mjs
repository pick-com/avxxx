// PLAYBACK_RESOLVE_ON switch (app/lib/playbackMode.ts).
// Run: npm test (Node >= 22.18 strips the TypeScript types itself).
import assert from 'node:assert/strict';
import {test} from 'node:test';
import {deferResolve, parseResolveOn} from '../app/lib/playbackMode.ts';

test('parseResolveOn defaults to load', () => {
  for (const v of [undefined, null, '', 'load', 'LOAD', 'bogus', 'click']) assert.equal(parseResolveOn(v), 'load', String(v));
  for (const v of ['play', ' Play ', 'PLAY']) assert.equal(parseResolveOn(v), 'play', v);
});

test('load mode never defers', () => {
  for (const autoplay of [false, true]) {
    for (const queued of [false, true]) assert.equal(deferResolve('load', {autoplay, queued}), false);
  }
});

test('play mode defers only a plain page view', () => {
  assert.equal(deferResolve('play', {autoplay: false, queued: false}), true);
  assert.equal(deferResolve('play', {autoplay: true, queued: false}), false);
  assert.equal(deferResolve('play', {autoplay: false, queued: true}), false);
});
