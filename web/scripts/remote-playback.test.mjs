// Casting helpers (app/lib/remotePlayback.ts): which sources can be cast and
// what the 投屏 button shows.
// Run: npm test (Node >= 22.18 strips the TypeScript types itself).
import assert from 'node:assert/strict';
import {test} from 'node:test';
import {castButtonState, castableSource} from '../app/lib/remotePlayback.ts';

test('MSE-backed HLS (hls.js) is never castable; progressive and native HLS are', () => {
  assert.equal(castableSource(null), false);
  assert.equal(castableSource({hls: false, nativeHls: false}), true, 'MP4 in Chrome');
  assert.equal(castableSource({hls: true, nativeHls: true}), true, 'HLS in Safari / Android');
  assert.equal(castableSource({hls: true, nativeHls: false}), false, 'HLS through hls.js');
});

test('button: hidden without a device, kept while connecting / connected', () => {
  assert.equal(castButtonState(false, true, 'disconnected'), 'hidden');
  assert.equal(castButtonState(false, true, 'connected'), 'hidden');
  assert.equal(castButtonState(true, false, 'disconnected'), 'hidden');
  assert.equal(castButtonState(true, true, 'disconnected'), 'idle');
  assert.equal(castButtonState(true, false, 'connecting'), 'connecting');
  assert.equal(castButtonState(true, false, 'connected'), 'connected');
  assert.equal(castButtonState(true, true, 'connected'), 'connected');
});
