// Which API paths carry the user token (app/lib/apiTokenPaths.ts).
// Run: npm test (Node >= 22.18 strips the TypeScript types itself).
//
// Regression: 赞 / 踩 and comments were missing, so a signed-in viewer sent
// neither a token nor X-Device-ID (400 on every vote, 401 on posting a
// comment), and 即将上线 > 只看关注 answered 401.
import assert from 'node:assert/strict';
import {test} from 'node:test';
import {sendsUserToken} from '../app/lib/apiTokenPaths.ts';

test('signed-in routes get the token', () => {
  for (const path of [
    'me/settings',
    'auth/me',
    'releases/abc-123/play-config?lang=en',
    'releases/abc-123/rating',
    'releases/abc-123/reviews?sort=top',
    'releases/abc-123/danmaku?from=0',
    'danmaku/7/report',
    'reviews/3/like',
    'reports',
    'recs?slot=home',
    'recs/feedback',
    'recs/feedback?kind=not_interested_release&target=abc',
    'me/rec-feedback',
    'upcoming?followed=1',
    'releases/abc-123/reaction',
    'releases/abc-123/reaction?lang=zh-CN',
    'releases/abc-123/comments?sort=top',
    'comments/42',
    'comments/42/like',
    'comments/42/replies?cursor=x',
    'comments/42/report',
    'clips/abcDEF1234',
    'clips/abcDEF1234/report',
  ]) {
    assert.equal(sendsUserToken(path), true, path);
  }
});

test('public catalog routes do not', () => {
  for (const path of ['releases/abc-123', 'releases?limit=10', 'performers/x', 'search?q=a', 'announcements?lang=en', 'playlists/tok', 'commentsx', 'comments/abc', 'recs/feedbackx', 'clips', 'clips/short']) {
    assert.equal(sendsUserToken(path), false, path);
  }
});
