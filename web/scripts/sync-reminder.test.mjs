// 「登录后观看记录可跨设备同步」 frequency (app/lib/syncReminder.ts).
// Run: npm test (Node >= 22.18 strips the TypeScript types itself).
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {SYNC_REMINDER_AFTER, dayKey, shouldRemind} from '../app/lib/syncReminder.ts';

const noon = new Date(2026, 9, 16, 12, 0, 0).getTime();

test('dayKey is the local calendar day', () => {
  assert.equal(dayKey(noon), '2026-10-16');
  assert.equal(dayKey(new Date(2026, 0, 2, 23, 59).getTime()), '2026-01-02');
});

test('shows from the 3rd distinct watched video', () => {
  const base = {lastShown: null, now: noon, resumed: false};
  assert.equal(SYNC_REMINDER_AFTER, 3);
  assert.equal(shouldRemind({...base, watched: [], current: 'a'}), false);
  assert.equal(shouldRemind({...base, watched: ['a'], current: 'b'}), false);
  assert.equal(shouldRemind({...base, watched: ['a', 'b'], current: 'b'}), false); // rewatching does not count
  assert.equal(shouldRemind({...base, watched: ['a', 'b'], current: 'c'}), true);
  assert.equal(shouldRemind({...base, watched: ['a', 'b', 'c', 'd'], current: 'a'}), true);
});

test('shows on a guest resume even before the 3rd video', () => {
  assert.equal(shouldRemind({lastShown: null, now: noon, watched: ['a'], current: 'a', resumed: true}), true);
});

test('at most once per calendar day', () => {
  const ready = {watched: ['a', 'b', 'c'], current: 'c', resumed: true};
  assert.equal(shouldRemind({...ready, lastShown: '2026-10-16', now: noon}), false);
  assert.equal(shouldRemind({...ready, lastShown: '2026-10-16', now: new Date(2026, 9, 16, 23, 59).getTime()}), false);
  assert.equal(shouldRemind({...ready, lastShown: '2026-10-16', now: new Date(2026, 9, 17, 0, 1).getTime()}), true);
  assert.equal(shouldRemind({...ready, lastShown: '2026-10-15', now: noon}), true);
});
