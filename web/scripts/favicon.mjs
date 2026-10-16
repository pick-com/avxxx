#!/usr/bin/env node
// Site icons, generated here so the repo needs no external assets:
// `npm run favicon` writes public/favicon.svg, favicon.png (32 px),
// favicon.ico (16/32/48 px PNGs) and apple-touch-icon.png (180 px).
//
// The mark is a "J" monogram in the logo text colour on the dark background,
// with the accent dot of the REEL. wordmark (styles/app.css: --bg, --text,
// --accent). The SVG and the bitmaps are drawn from the same geometry in a
// 32-unit box; bitmaps are rasterized with 4x4 supersampling.
//
// scripts/favicon.test.mjs checks that the committed files match this output.

import {writeFileSync} from 'node:fs';
import {dirname, join} from 'node:path';
import {fileURLToPath} from 'node:url';
import {deflateSync} from 'node:zlib';

const BG = [0x0f, 0x10, 0x12];
const FG = [0xec, 0xec, 0xee];
const ACCENT = [0xf2, 0xa9, 0x3b];
const hex = c => '#' + c.map(v => v.toString(16).padStart(2, '0')).join('');

// Geometry (units of a 32 x 32 box).
const RADIUS = 7; // background corner radius (0 for the apple-touch icon)
const STROKE = 4.5; // J stroke width
const STEM = {x: 19, top: 7, bottom: 18}; // J stem
const HOOK = {cx: 13, cy: 18, r: 6}; // lower half circle from the stem's foot
const DOT = {cx: 25, cy: 24.5, r: 2.75};

export function svg() {
  const {x, top, bottom} = STEM;
  const {cx, r} = HOOK;
  return [
    '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">',
    `<rect width="32" height="32" rx="${RADIUS}" fill="${hex(BG)}"/>`,
    `<path d="M${x} ${top}V${bottom}A${r} ${r} 0 0 1 ${cx - r} ${bottom}" fill="none" stroke="${hex(FG)}" stroke-width="${STROKE}" stroke-linecap="round"/>`,
    `<circle cx="${DOT.cx}" cy="${DOT.cy}" r="${DOT.r}" fill="${hex(ACCENT)}"/>`,
    '</svg>',
    '',
  ].join('\n');
}

const dist = (ax, ay, bx, by) => Math.hypot(ax - bx, ay - by);

/** Distance from (px, py) to the J's centre line. */
function jDistance(px, py) {
  const {x, top, bottom} = STEM;
  const stem = dist(px, py, x, Math.min(bottom, Math.max(top, py)));
  const {cx, cy, r} = HOOK;
  const hook = py >= cy ? Math.abs(dist(px, py, cx, cy) - r) : Math.min(dist(px, py, cx + r, cy), dist(px, py, cx - r, cy));
  return Math.min(stem, hook);
}

function insideRoundRect(px, py, radius) {
  if (px < 0 || py < 0 || px > 32 || py > 32) return false;
  const qx = Math.min(px, 32 - px);
  const qy = Math.min(py, 32 - py);
  return qx >= radius || qy >= radius || dist(qx, qy, radius, radius) <= radius;
}

/** RGBA pixels of a size x size icon. */
export function raster(size, radius = RADIUS) {
  const out = Buffer.alloc(size * size * 4);
  const N = 4;
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      let r = 0, g = 0, b = 0, a = 0;
      for (let sy = 0; sy < N; sy++) {
        for (let sx = 0; sx < N; sx++) {
          const px = ((x + (sx + 0.5) / N) * 32) / size;
          const py = ((y + (sy + 0.5) / N) * 32) / size;
          if (!insideRoundRect(px, py, radius)) continue;
          const c = dist(px, py, DOT.cx, DOT.cy) <= DOT.r ? ACCENT : jDistance(px, py) <= STROKE / 2 ? FG : BG;
          r += c[0];
          g += c[1];
          b += c[2];
          a += 1;
        }
      }
      const i = (y * size + x) * 4;
      // Colour averaged over covered samples (straight alpha), coverage as alpha.
      out[i] = a ? Math.round(r / a) : 0;
      out[i + 1] = a ? Math.round(g / a) : 0;
      out[i + 2] = a ? Math.round(b / a) : 0;
      out[i + 3] = Math.round((a / (N * N)) * 255);
    }
  }
  return out;
}

const CRC_TABLE = Array.from({length: 256}, (_, n) => {
  let c = n;
  for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
  return c >>> 0;
});

function crc32(buf) {
  let c = 0xffffffff;
  for (const byte of buf) c = CRC_TABLE[(c ^ byte) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}

function chunk(type, data) {
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length);
  const body = Buffer.concat([Buffer.from(type, 'ascii'), data]);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(body));
  return Buffer.concat([len, body, crc]);
}

/** An 8-bit RGBA PNG. */
export function png(size, radius = RADIUS) {
  const rgba = raster(size, radius);
  const rows = Buffer.alloc(size * (size * 4 + 1));
  for (let y = 0; y < size; y++) rgba.copy(rows, y * (size * 4 + 1) + 1, y * size * 4, (y + 1) * size * 4);
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(size, 0);
  ihdr.writeUInt32BE(size, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 6; // RGBA
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', ihdr),
    chunk('IDAT', deflateSync(rows, {level: 9})),
    chunk('IEND', Buffer.alloc(0)),
  ]);
}

/** A .ico holding PNG images (supported by every current browser). */
export function ico(sizes) {
  const images = sizes.map(s => png(s));
  const header = Buffer.alloc(6);
  header.writeUInt16LE(1, 2); // type: icon
  header.writeUInt16LE(images.length, 4);
  let offset = 6 + 16 * images.length;
  const entries = images.map((img, i) => {
    const e = Buffer.alloc(16);
    e[0] = sizes[i] >= 256 ? 0 : sizes[i];
    e[1] = sizes[i] >= 256 ? 0 : sizes[i];
    e.writeUInt16LE(1, 4); // planes
    e.writeUInt16LE(32, 6); // bits per pixel
    e.writeUInt32LE(img.length, 8);
    e.writeUInt32LE(offset, 12);
    offset += img.length;
    return e;
  });
  return Buffer.concat([header, ...entries, ...images]);
}

/** File name under public/ → contents. */
export function icons() {
  return {
    'favicon.svg': Buffer.from(svg()),
    'favicon.png': png(32),
    'favicon.ico': ico([16, 32, 48]),
    // iOS rounds the corners itself and shows transparency as black.
    'apple-touch-icon.png': png(180, 0),
  };
}

export const PUBLIC = join(dirname(fileURLToPath(import.meta.url)), '..', 'public');

if (import.meta.url === `file://${process.argv[1]}`) {
  for (const [name, data] of Object.entries(icons())) {
    writeFileSync(join(PUBLIC, name), data);
    console.log(`public/${name} ${data.length} bytes`);
  }
}
