import {test} from 'node:test';
import assert from 'node:assert/strict';
import {ADHOC_MAX, ADHOC_QUEUE, emptyQueue, isQueueList, normalizeQueue, queueAdd, queueMove, queueRemove} from '../app/lib/queue.ts';

const q = (...slugs) => ({name: ADHOC_QUEUE, slugs, titles: {}});

test('?list=queue is a followed queue', () => {
  assert.ok(isQueueList('queue'));
  assert.ok(isQueueList('later'));
  assert.ok(isQueueList('playlist:12'));
  assert.ok(!isQueueList('queues'));
  assert.ok(!isQueueList(null));
});

test('queueAdd appends once and keeps the title', () => {
  const a = queueAdd(emptyQueue(), 'abc-001', 'Title A');
  assert.equal(a.added, true);
  assert.deepEqual(a.queue.slugs, ['abc-001']);
  assert.equal(a.queue.titles['abc-001'], 'Title A');
  const b = queueAdd(a.queue, 'abc-002');
  assert.deepEqual(b.queue.slugs, ['abc-001', 'abc-002']);
  const again = queueAdd(b.queue, 'abc-001', 'Other');
  assert.equal(again.added, false);
  assert.equal(again.queue, b.queue);
  assert.equal(queueAdd(b.queue, '').added, false);
});

test('queueAdd stops at the cap', () => {
  const full = q(...Array.from({length: ADHOC_MAX}, (_, i) => `s${i}`));
  assert.equal(queueAdd(full, 'one-more').added, false);
});

test('queueRemove drops the slug and its title', () => {
  const a = queueAdd(queueAdd(emptyQueue(), 'a', 'A').queue, 'b', 'B').queue;
  const r = queueRemove(a, 'a');
  assert.deepEqual(r.slugs, ['b']);
  assert.deepEqual(r.titles, {b: 'B'});
  assert.equal(queueRemove(r, 'missing'), r);
});

test('queueMove reorders within bounds', () => {
  assert.deepEqual(queueMove(q('a', 'b', 'c'), 'a', 1).slugs, ['b', 'a', 'c']);
  assert.deepEqual(queueMove(q('a', 'b', 'c'), 'c', -1).slugs, ['a', 'c', 'b']);
  assert.deepEqual(queueMove(q('a', 'b', 'c'), 'b', 10).slugs, ['a', 'c', 'b']);
  const same = q('a', 'b');
  assert.equal(queueMove(same, 'a', -1), same);
  assert.equal(queueMove(same, 'zz', 1), same);
});

test('normalizeQueue cleans stored values', () => {
  assert.deepEqual(normalizeQueue(null), emptyQueue());
  assert.deepEqual(normalizeQueue({slugs: 'x'}), emptyQueue());
  assert.deepEqual(normalizeQueue({slugs: ['a', 'a', 3, '', 'b'], titles: {a: 'A', z: 'Z', b: 7}}), {name: ADHOC_QUEUE, slugs: ['a', 'b'], titles: {a: 'A'}});
});
