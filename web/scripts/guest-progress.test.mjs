import {test} from 'node:test';
import assert from 'node:assert/strict';
import {prune, resumePoint} from '../app/lib/guestProgress.ts';

test('resume point skips the first seconds and finished videos', () => {
  assert.equal(resumePoint(undefined), 0);
  assert.equal(resumePoint({p: 4, d: 600, t: 1}), 0);
  assert.equal(resumePoint({p: 125.7, d: 600, t: 1}), 125);
  assert.equal(resumePoint({p: 545, d: 600, t: 1}), 0); // ≥ 90 %
  assert.equal(resumePoint({p: 595, d: 0, t: 1}), 595); // unknown duration
});

test('prune keeps the most recent entries', () => {
  const now = 1_000_000;
  const store = {a: {p: 10, d: 0, t: now - 3}, b: {p: 10, d: 0, t: now - 1}, c: {p: 10, d: 0, t: now - 2}, bad: {p: NaN, d: 0, t: now}};
  assert.deepEqual(Object.keys(prune(store, 2, now)).sort(), ['b', 'c']);
  assert.deepEqual(Object.keys(prune(store, 5, now)).sort(), ['a', 'b', 'c']);
});

test('prune drops entries older than 90 days', () => {
  const now = 200 * 86_400_000;
  const store = {fresh: {p: 10, d: 0, t: now - 89 * 86_400_000}, stale: {p: 10, d: 0, t: now - 91 * 86_400_000}};
  assert.deepEqual(Object.keys(prune(store, 100, now)), ['fresh']);
});
