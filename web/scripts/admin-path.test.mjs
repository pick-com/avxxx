import assert from 'node:assert/strict';
import test from 'node:test';
import {isAdminPath} from '../app/lib/langs.ts';

test('isAdminPath: back office only, with or without a language prefix', () => {
  for (const p of ['/admin', '/admin/', '/admin/legal', '/en/admin/users']) assert.ok(isAdminPath(p), p);
  for (const p of ['/', '/administrator', '/en', '/legal/admin', '/en/releases/admin']) assert.ok(!isAdminPath(p), p);
});
