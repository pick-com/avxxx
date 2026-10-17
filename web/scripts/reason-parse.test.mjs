// Values kept from the backend's zh-CN 推荐理由 (app/lib/reasonParse.ts), so
// other UI languages never show the Chinese text itself.
// Run: npm test (Node >= 22.18 strips the TypeScript types itself).
import assert from 'node:assert/strict';
import {test} from 'node:test';
import {sharedTag, trendingRank} from '../app/lib/reasonParse.ts';

test('trendingRank reads the chart rank', () => {
  assert.equal(trendingRank('本周热门 · 第 1 名'), 1);
  assert.equal(trendingRank('今日热门 · 第 12 名'), 12);
  assert.equal(trendingRank('本周热门'), null);
  assert.equal(trendingRank(''), null);
  assert.equal(trendingRank(undefined), null);
});

test('sharedTag reads the tag of a similar_tags reason', () => {
  assert.equal(sharedTag('相似标签 · Exclusive Distribution'), 'Exclusive Distribution');
  assert.equal(sharedTag('相似标签 · 巨乳'), '巨乳');
  assert.equal(sharedTag('内容相似'), '');
  assert.equal(sharedTag(undefined), '');
});
