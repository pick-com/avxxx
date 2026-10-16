// 循环播放 / A-B loop logic (app/lib/playerLoop.ts).
// Run: npm test (Node >= 22.18 strips the TypeScript types itself).
import assert from 'node:assert/strict';
import {test} from 'node:test';
import {NO_AB, abActive, abMarkers, abSeekTarget, endAction, setAbPoint} from '../app/lib/playerLoop.ts';

test('setting A then B makes an active loop', () => {
  let ab = setAbPoint(NO_AB, 'a', 10, 100);
  assert.deepEqual(ab, {a: 10, b: null});
  assert.equal(abActive(ab), false);
  ab = setAbPoint(ab, 'b', 20, 100);
  assert.deepEqual(ab, {a: 10, b: 20});
  assert.equal(abActive(ab), true);
});

test('B without A loops from the start', () => {
  assert.deepEqual(setAbPoint(NO_AB, 'b', 30, 100), {a: 0, b: 30});
});

test('out-of-order or too tight points drop the other point', () => {
  const ab = {a: 10, b: 20};
  assert.deepEqual(setAbPoint(ab, 'a', 25, 100), {a: 25, b: null});
  assert.deepEqual(setAbPoint(ab, 'a', 19.5, 100), {a: 19.5, b: null});
  assert.deepEqual(setAbPoint(ab, 'b', 5, 100), {a: 10, b: null});
  assert.deepEqual(setAbPoint(ab, 'b', 10.5, 100), {a: 10, b: null});
  assert.deepEqual(setAbPoint(ab, 'a', 15, 100), {a: 15, b: 20});
});

test('points clamp into the video', () => {
  assert.deepEqual(setAbPoint(NO_AB, 'a', -3, 100), {a: 0, b: null});
  assert.deepEqual(setAbPoint({a: 10, b: null}, 'b', 120, 100), {a: 10, b: 100});
  // Unknown duration: no upper clamp.
  assert.deepEqual(setAbPoint({a: 10, b: null}, 'b', 120, NaN), {a: 10, b: 120});
  assert.deepEqual(setAbPoint(NO_AB, 'a', NaN, 100), {a: 0, b: null});
});

test('reaching B seeks back to A; before A is left alone', () => {
  const ab = {a: 10, b: 20};
  assert.equal(abSeekTarget(ab, 15), null);
  assert.equal(abSeekTarget(ab, 20), 10);
  assert.equal(abSeekTarget(ab, 42), 10);
  assert.equal(abSeekTarget(ab, 3), null);
  assert.equal(abSeekTarget({a: 10, b: null}, 42), null);
  assert.equal(abSeekTarget(NO_AB, 42), null);
});

test('end of video: A-B, then loop, then the end screen', () => {
  assert.equal(endAction(false, NO_AB), 'end');
  assert.equal(endAction(true, NO_AB), 'restart');
  assert.equal(endAction(false, {a: 5, b: 100}), 'a');
  assert.equal(endAction(true, {a: 5, b: 100}), 'a');
  assert.equal(endAction(true, {a: 5, b: null}), 'restart');
});

test('markers are fractions of a known duration', () => {
  assert.deepEqual(abMarkers({a: 25, b: 50}, 100), {a: 0.25, b: 0.5});
  assert.deepEqual(abMarkers({a: 25, b: null}, 100), {a: 0.25, b: null});
  assert.deepEqual(abMarkers({a: 25, b: 150}, 100), {a: 0.25, b: 1});
  assert.deepEqual(abMarkers({a: 25, b: 50}, 0), {a: null, b: null});
  assert.deepEqual(abMarkers({a: 25, b: 50}, NaN), {a: null, b: null});
});
