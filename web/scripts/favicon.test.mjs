import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {join} from 'node:path';
import test from 'node:test';
import {inflateSync} from 'node:zlib';
import {icons, PUBLIC, raster} from './favicon.mjs';

const PNG_SIG = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);

// Every PNG in buf (a .png, or the PNGs inside an .ico) as its IHDR plus the
// inflated image data. deflate output differs between zlib builds (macOS vs
// the Linux CI), so bitmaps are compared by content, not by bytes.
function pngContents(buf) {
  const out = [];
  for (let at = buf.indexOf(PNG_SIG); at >= 0; at = buf.indexOf(PNG_SIG, at + 1)) {
    let pos = at + 8;
    let ihdr = '';
    const idat = [];
    while (pos + 8 <= buf.length) {
      const len = buf.readUInt32BE(pos);
      const type = buf.toString('latin1', pos + 4, pos + 8);
      const data = buf.subarray(pos + 8, pos + 8 + len);
      if (type === 'IHDR') ihdr = data.toString('hex');
      if (type === 'IDAT') idat.push(data);
      pos += 12 + len;
      if (type === 'IEND') break;
    }
    out.push(ihdr + ':' + inflateSync(Buffer.concat(idat)).toString('hex'));
  }
  return out;
}

test('committed icons match scripts/favicon.mjs (run `npm run favicon`)', () => {
  for (const [name, data] of Object.entries(icons())) {
    const committed = readFileSync(join(PUBLIC, name));
    if (name.endsWith('.svg')) {
      assert.ok(committed.equals(data), `public/${name} is stale`);
      continue;
    }
    const want = pngContents(data);
    assert.ok(want.length > 0, `${name}: generator produced no PNG`);
    assert.deepEqual(pngContents(committed), want, `public/${name} is stale`);
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
