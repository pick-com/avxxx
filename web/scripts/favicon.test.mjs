import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {join} from 'node:path';
import test from 'node:test';
import {icons, PUBLIC, raster} from './favicon.mjs';

test('committed icons match scripts/favicon.mjs (run `npm run favicon`)', () => {
  for (const [name, data] of Object.entries(icons())) {
    assert.ok(readFileSync(join(PUBLIC, name)).equals(data), `public/${name} is stale`);
  }
});

test('icon pixels: transparent rounded corner, background, stroke and accent dot', () => {
  const px = raster(32);
  const at = (x, y) => [...px.subarray((y * 32 + x) * 4, (y * 32 + x) * 4 + 4)];
  assert.equal(at(0, 0)[3], 0); // outside the rounded corner
  assert.deepEqual(at(4, 4), [0x0f, 0x10, 0x12, 255]); // background
  assert.deepEqual(at(19, 10), [0xec, 0xec, 0xee, 255]); // J stem
  assert.deepEqual(at(25, 24), [0xf2, 0xa9, 0x3b, 255]); // accent dot
});
