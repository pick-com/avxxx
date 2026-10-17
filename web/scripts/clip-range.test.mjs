// 片段 range validation (app/lib/clipRange.ts), mirroring internal/store/clips.go.
// Run: npm test (Node >= 22.18 strips the TypeScript types itself).
import assert from 'node:assert/strict';
import {test} from 'node:test';
import {clipRangeError, clipRequest, clipTitleLength, clipWindow, defaultClipRange, moveClipHandle} from '../app/lib/clipRange.ts';

test('the default is the current position + 15 s, kept inside the video', () => {
  assert.deepEqual(defaultClipRange(30, 600), {start: 30, end: 45});
  assert.deepEqual(defaultClipRange(595, 600), {start: 585, end: 600});
  assert.deepEqual(defaultClipRange(12.34, NaN), {start: 12.3, end: 27.3});
  assert.deepEqual(defaultClipRange(-4, 600), {start: 0, end: 15});
  assert.deepEqual(defaultClipRange(3, 10), {start: 0, end: 10});
});

test('handles keep the length within 5–60 s', () => {
  const r = {start: 30, end: 45};
  assert.deepEqual(moveClipHandle(r, 'end', 50, 600), {start: 30, end: 50});
  assert.deepEqual(moveClipHandle(r, 'end', 31, 600), {start: 26, end: 31});
  assert.deepEqual(moveClipHandle(r, 'end', 200, 600), {start: 140, end: 200});
  assert.deepEqual(moveClipHandle(r, 'start', 42, 600), {start: 42, end: 47});
  assert.deepEqual(moveClipHandle(r, 'start', 0, 600), {start: 0, end: 45});
  assert.deepEqual(moveClipHandle({start: 100, end: 110}, 'start', 20, 600), {start: 20, end: 80});
});

test('handles stay inside the video', () => {
  assert.deepEqual(moveClipHandle({start: 590, end: 600}, 'start', 599, 600), {start: 595, end: 600});
  assert.deepEqual(moveClipHandle({start: 590, end: 600}, 'end', 700, 600), {start: 590, end: 600});
  assert.deepEqual(moveClipHandle({start: 0, end: 10}, 'end', 2, 600), {start: 0, end: 5});
  assert.deepEqual(moveClipHandle({start: 0, end: 10}, 'start', -9, 600), {start: 0, end: 10});
});

test('every handle move gives a range the API accepts', () => {
  const d = 300;
  let r = defaultClipRange(100, d);
  for (const [which, to] of [['end', 400], ['start', 0], ['end', 1], ['start', 299], ['end', 150], ['start', 3.33]]) {
    r = moveClipHandle(r, which, to, d);
    assert.equal(clipRangeError(r.start * 1000, r.end * 1000, d * 1000), null, JSON.stringify(r));
  }
});

test('range errors match the API', () => {
  assert.equal(clipRangeError(0, 5000, 0), null);
  assert.equal(clipRangeError(0, 60000, 0), null);
  assert.equal(clipRangeError(0, 4999, 0), 'length');
  assert.equal(clipRangeError(0, 60001, 0), 'length');
  assert.equal(clipRangeError(-1, 9000, 0), 'order');
  assert.equal(clipRangeError(9000, 9000, 0), 'order');
  assert.equal(clipRangeError(110000, 125000, 120000), 'end');
  assert.equal(clipRangeError(105000, 120000, 120000), null);
});

test('the timeline window follows the clip', () => {
  assert.deepEqual(clipWindow({start: 30, end: 45}, 100), {start: 0, end: 100});
  assert.deepEqual(clipWindow({start: 500, end: 515}, 1000), {start: 417.5, end: 597.5});
  assert.deepEqual(clipWindow({start: 0, end: 15}, 1000), {start: 0, end: 180});
  assert.deepEqual(clipWindow({start: 990, end: 1000}, 1000), {start: 820, end: 1000});
});

test('request body and title length', () => {
  assert.deepEqual(clipRequest('abc-1', {start: 12.3, end: 27.3}, '  hi '), {release: 'abc-1', start_ms: 12300, end_ms: 27300, title: 'hi'});
  assert.equal(clipTitleLength(' 名场面 '), 3);
  assert.equal(clipTitleLength('😀'.repeat(100)), 100);
});
