// 片源 details card labels (app/lib/streamFormat.ts).
// Run: npm test (Node >= 22.18 strips the TypeScript types itself).
import assert from 'node:assert/strict';
import {test} from 'node:test';
import {formatName, streamQualityLabel} from '../app/lib/streamFormat.ts';

test('a bare height reads as NNNp', () => {
  assert.equal(streamQualityLabel('720'), '720p');
  assert.equal(streamQualityLabel('1080'), '1080p');
  assert.equal(streamQualityLabel(' 480 '), '480p');
});

test('other quality labels are kept', () => {
  assert.equal(streamQualityLabel('720p'), '720p');
  assert.equal(streamQualityLabel('HD'), 'HD');
  assert.equal(streamQualityLabel(''), '');
  assert.equal(streamQualityLabel(undefined), '');
  assert.equal(streamQualityLabel(null), '');
});

test('format from the protocol when the signed URL has no extension', () => {
  assert.equal(formatName('mp4', 'https://cdn.example.test/p/2c628f49'), 'MP4');
  assert.equal(formatName('hls', 'https://cdn.example.test/p/2c628f49'), 'HLS');
});

test('format from the URL extension', () => {
  assert.equal(formatName('file', 'https://cdn.example.test/a.m3u8?x=1'), 'HLS');
  assert.equal(formatName('file', 'https://cdn.example.test/a.mp4'), 'MP4');
  assert.equal(formatName('file', 'https://cdn.example.test/a.webm#t=1'), 'WebM');
  assert.equal(formatName('file', 'https://cdn.example.test/a'), '');
});
