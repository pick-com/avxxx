import {test} from 'node:test';
import assert from 'node:assert/strict';
import {
  PENDING_DRAFT_MAX,
  PENDING_TTL_MS,
  boundPayload,
  clearPending,
  parsePending,
  peekPending,
  savePending,
  setPendingMethod,
  takePending,
} from '../app/lib/pendingAction.ts';

function memoryStore() {
  const map = new Map();
  return {
    map,
    getItem: k => (map.has(k) ? map.get(k) : null),
    setItem: (k, v) => map.set(k, String(v)),
    removeItem: k => map.delete(k),
  };
}

test('a saved action is taken once, for its own kind and target only', () => {
  const store = memoryStore();
  const now = 1_000_000;
  savePending({kind: 'later', target: 'abc-1'}, now, store);
  assert.equal(takePending('favorite', 'abc-1', now + 1, store), null);
  assert.equal(takePending('later', 'abc-2', now + 1, store), null);
  const p = takePending('later', 'abc-1', now + 1, store);
  assert.deepEqual(p, {kind: 'later', target: 'abc-1', created_at: now});
  assert.equal(takePending('later', 'abc-1', now + 2, store), null);
});

test('actions expire after 10 minutes and are removed', () => {
  const store = memoryStore();
  const now = 5_000_000;
  savePending({kind: 'follow', target: 'jane'}, now, store);
  assert.ok(peekPending(now + PENDING_TTL_MS - 1, store));
  assert.equal(takePending('follow', 'jane', now + PENDING_TTL_MS, store), null);
  assert.equal(store.map.size, 0);
});

test('a clock in the past (future created_at) is not trusted', () => {
  assert.equal(parsePending(JSON.stringify({kind: 'like', target: 'x', created_at: 2000}), 1000), null);
});

test('malformed values are ignored', () => {
  for (const raw of [null, '', 'nope', '[]', '{}', JSON.stringify({kind: 1, target: 'x', created_at: 1}), JSON.stringify({kind: 'like', created_at: 1})]) {
    assert.equal(parsePending(raw, 10), null, String(raw));
  }
});

test('drafts and the sign-in method are kept, drafts bounded', () => {
  const store = memoryStore();
  const now = 7_000;
  const long = '弹'.repeat(PENDING_DRAFT_MAX + 50);
  savePending({kind: 'danmaku', target: 'abc-1', payload: {draft: long, spoiler: true}}, now, store);
  setPendingMethod('google', now + 5, store);
  const p = takePending('danmaku', 'abc-1', now + 10, store);
  assert.equal(p.method, 'google');
  assert.equal(p.created_at, now); // the method does not extend the expiry
  assert.equal(Array.from(p.payload.draft).length, PENDING_DRAFT_MAX);
  assert.equal(p.payload.spoiler, true);
});

test('boundPayload leaves numbers and booleans alone', () => {
  assert.deepEqual(boundPayload({id: 3, ok: false, s: 'hi'}), {id: 3, ok: false, s: 'hi'});
  assert.equal(boundPayload(undefined), undefined);
});

test('clearPending and a missing store are safe', () => {
  const store = memoryStore();
  savePending({kind: 'later', target: 'a'}, 1, store);
  clearPending(store);
  assert.equal(peekPending(2, store), null);
  assert.equal(peekPending(2, null), null);
  savePending({kind: 'later', target: 'a'}, 1, null);
});
