// Legacy playback answer (app/lib/legacyPlayback.ts).
// Run: npm test (Node >= 22.18 strips the TypeScript types itself).
import assert from 'node:assert/strict';
import {test} from 'node:test';
import {playbackOrNone} from '../app/lib/legacyPlayback.ts';

test('no mapping (available: false) reads as no source', () => {
  assert.equal(playbackOrNone({source: 'javtiful', available: false}), null);
  assert.equal(playbackOrNone(null), null);
  assert.equal(playbackOrNone(undefined), null);
});

test('a mapping is returned as is', () => {
  const m = {source: 'javtiful', source_key: 'k', source_url: '', detail_path: '/api/detail/javtiful/k', resolve_path: '/api/resolve/javtiful/k', available: true};
  assert.equal(playbackOrNone(m), m);
  // Servers without the flag still send a usable mapping.
  const {available: _, ...old} = m;
  assert.equal(playbackOrNone(old), old);
});

test('a body without resolve_path is not playable', () => {
  assert.equal(playbackOrNone({source: 'javtiful'}), null);
});
