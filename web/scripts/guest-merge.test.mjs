// Guest history merge on sign-in (app/lib/guestMerge.ts).
// Run: npm test (Node >= 22.18 strips the TypeScript types itself).
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {GUEST_MERGE_MAX, isPermanentFailure, selectGuestMerge, uploadGuestHistory} from '../app/lib/guestMerge.ts';

test('selection keeps the newest entries with progress, oldest first', () => {
  const now = 10_000_000;
  const store = {
    a: {p: 30, d: 600, t: now - 3000},
    b: {p: 0, d: 600, t: now - 10}, // never really watched
    c: {p: 120.9, d: 0, t: now - 2000},
    d: {p: NaN, d: 0, t: now},
    e: {p: 10, d: 100, t: now - 1000, title: 'E'},
  };
  assert.deepEqual(selectGuestMerge(store, now), [
    {slug: 'a', progress_seconds: 30, duration_seconds: 600, watched_at: new Date(now - 3000).toISOString()},
    {slug: 'c', progress_seconds: 120, duration_seconds: 0, watched_at: new Date(now - 2000).toISOString()},
    {slug: 'e', progress_seconds: 10, duration_seconds: 100, watched_at: new Date(now - 1000).toISOString()},
  ]);
  assert.deepEqual(
    selectGuestMerge(store, now, 2).map(e => e.slug),
    ['c', 'e'],
  );
});

test('selection is bounded to the last 100 and never dates an entry in the future', () => {
  const now = 50_000_000;
  const store = {};
  for (let i = 0; i < 130; i++) store[`s${i}`] = {p: 60, d: 0, t: now - 1000 * (130 - i)};
  store.future = {p: 5, d: 0, t: now + 60_000};
  const out = selectGuestMerge(store, now);
  assert.equal(out.length, GUEST_MERGE_MAX);
  assert.equal(out.at(-1).slug, 'future');
  assert.equal(out.at(-1).watched_at, new Date(now).toISOString());
  assert.equal(out[0].slug, 's31');
});

test('upload goes in order and drops permanently rejected entries', async () => {
  const sent = [];
  const entries = ['a', 'gone', 'b'].map(slug => ({slug, progress_seconds: 1, duration_seconds: 0, watched_at: ''}));
  const result = await uploadGuestHistory(entries, async e => {
    sent.push(e.slug);
    if (e.slug === 'gone') throw Object.assign(new Error('not found'), {status: 404});
  });
  assert.deepEqual(sent, ['a', 'gone', 'b']);
  assert.deepEqual(result, {done: ['a', 'gone', 'b'], complete: true});
});

test('upload stops on a network or server error and keeps the rest', async () => {
  const entries = ['a', 'b', 'c'].map(slug => ({slug, progress_seconds: 1, duration_seconds: 0, watched_at: ''}));
  for (const status of [0, 401, 429, 503, undefined]) {
    const result = await uploadGuestHistory(entries, async e => {
      if (e.slug === 'b') throw Object.assign(new Error('x'), {status});
    });
    assert.deepEqual(result, {done: ['a'], complete: false}, String(status));
  }
});

test('permanent failures are 4xx other than 401 / 408 / 429', () => {
  assert.equal(isPermanentFailure({status: 404}), true);
  assert.equal(isPermanentFailure({status: 400}), true);
  assert.equal(isPermanentFailure({status: 401}), false);
  assert.equal(isPermanentFailure({status: 500}), false);
  assert.equal(isPermanentFailure(new Error('offline')), false);
  assert.equal(isPermanentFailure(null), false);
});
