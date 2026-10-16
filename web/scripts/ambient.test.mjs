// 氛围模式 helpers (app/lib/ambient.ts).
// Run: npm test (Node >= 22.18 strips the TypeScript types itself).
import assert from 'node:assert/strict';
import {test} from 'node:test';
import {AMBIENT_KEY, ambientActive, ambientSampling, isTaintError, readAmbientPref, writeAmbientPref} from '../app/lib/ambient.ts';

const memory = () => {
  const m = new Map();
  return {getItem: k => (m.has(k) ? m.get(k) : null), setItem: (k, v) => m.set(k, String(v)), m};
};

test('the pref defaults to off and round-trips', () => {
  const s = memory();
  assert.equal(readAmbientPref(s), false);
  writeAmbientPref(s, true);
  assert.equal(s.m.get(AMBIENT_KEY), '1');
  assert.equal(readAmbientPref(s), true);
  writeAmbientPref(s, false);
  assert.equal(readAmbientPref(s), false);
});

test('blocked storage never throws', () => {
  const broken = {
    getItem() {
      throw new Error('denied');
    },
    setItem() {
      throw new Error('denied');
    },
  };
  assert.equal(readAmbientPref(broken), false);
  assert.doesNotThrow(() => writeAmbientPref(broken, true));
  assert.equal(readAmbientPref(null), false);
});

test('the glow is only for an inline, readable, local video', () => {
  const base = {pref: true, blocked: false, video: true, fullscreen: false, mini: false, casting: false};
  assert.equal(ambientActive(base), true);
  for (const k of ['blocked', 'fullscreen', 'mini', 'casting']) assert.equal(ambientActive({...base, [k]: true}), false, k);
  for (const k of ['pref', 'video']) assert.equal(ambientActive({...base, [k]: false}), false, k);
});

test('sampling pauses for a hidden page, a paused video and reduced motion', () => {
  assert.equal(ambientSampling({hidden: false, paused: false, reducedMotion: false}), true);
  assert.equal(ambientSampling({hidden: true, paused: false, reducedMotion: false}), false);
  assert.equal(ambientSampling({hidden: false, paused: true, reducedMotion: false}), false);
  assert.equal(ambientSampling({hidden: false, paused: false, reducedMotion: true}), false);
});

test('SecurityError means a tainted canvas', () => {
  assert.equal(isTaintError({name: 'SecurityError'}), true);
  assert.equal(isTaintError(new TypeError('x')), false);
  assert.equal(isTaintError(null), false);
  assert.equal(isTaintError('SecurityError'), false);
});
