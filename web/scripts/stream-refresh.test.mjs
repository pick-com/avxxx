// Refresh-once logic for resolved streams (app/lib/streamRefresh.ts).
// Run: npm test (Node >= 22.18 strips the TypeScript types itself).
import assert from 'node:assert/strict';
import {test} from 'node:test';
import {pickRefreshedStream, shouldRefreshStream} from '../app/lib/streamRefresh.ts';

const url = 'https://cdn.example/v.mp4?e=1';

test('token-like failures of a resolved stream refresh', () => {
  const tried = new Set();
  for (const failure of [{kind: 'media'}, {kind: 'hls-media'}, {kind: 'hls-network', status: 403}, {kind: 'hls-network', status: 410}]) {
    assert.equal(shouldRefreshStream({legacy: true, url, tried, failure}), true, JSON.stringify(failure));
  }
});

test('other failures and play-config sources do not', () => {
  const tried = new Set();
  assert.equal(shouldRefreshStream({legacy: false, url, tried, failure: {kind: 'media'}}), false);
  assert.equal(shouldRefreshStream({legacy: true, url, tried, failure: {kind: 'hls-network', status: 404}}), false);
  assert.equal(shouldRefreshStream({legacy: true, url, tried, failure: {kind: 'hls-network'}}), false);
  assert.equal(shouldRefreshStream({legacy: true, url, tried, failure: {kind: 'other'}}), false);
  assert.equal(shouldRefreshStream({legacy: true, url: '', tried, failure: {kind: 'media'}}), false);
});

test('at most once per URL, and never for a refreshed URL', () => {
  const tried = new Set();
  assert.equal(shouldRefreshStream({legacy: true, url, tried, failure: {kind: 'media'}}), true);
  // The player records the failed URL and the one the refresh returned.
  const next = 'https://cdn.example/v.mp4?e=2';
  tried.add(url);
  tried.add(next);
  assert.equal(shouldRefreshStream({legacy: true, url, tried, failure: {kind: 'media'}}), false);
  assert.equal(shouldRefreshStream({legacy: true, url: next, tried, failure: {kind: 'media'}}), false);
});

test('picks the same quality, else the first new URL, else nothing', () => {
  const tried = new Set([url]);
  const streams = [
    {url, quality: '1080'},
    {url: 'https://cdn.example/a.mp4', quality: '720'},
    {url: 'https://cdn.example/b.mp4', quality: '1080'},
  ];
  assert.equal(pickRefreshedStream(streams, {quality: '1080'}, tried), 2);
  assert.equal(pickRefreshedStream(streams, {quality: '480'}, tried), 1);
  assert.equal(pickRefreshedStream(streams, {}, tried), 1);
  // The server answered with the same (rate-limited) URL: nothing to retry.
  assert.equal(pickRefreshedStream([{url, quality: '1080'}], {quality: '1080'}, tried), null);
  assert.equal(pickRefreshedStream([], {}, tried), null);
});
