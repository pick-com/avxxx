// Which API paths (relative to /api) get the user token (tech plan 2.6;
// lib/api.ts tokenFor). Kept free of imports so scripts/api-token-paths.test.mjs
// can run it under plain Node.

// play-config is public, but a user token adds `resume` (tech plan 3.3).
const PLAY_CONFIG_PATH = /^releases\/[^/?]+\/play-config(\?|$)/;
// Recommendations (internal/api/recs.go): public, but a user token adds the
// viewer's history, follows and watched filter. 推荐反馈 (recs/feedback,
// internal/api/rec_feedback.go) is the token's, else the X-Device-ID's.
const RECS_PATH = /^recs(\/feedback)?(\?|$)/;
// Upcoming (internal/api/catalog.go): public, but ?followed=1 needs the user
// and a token adds each item's `reserved` state.
const UPCOMING_PATH = /^upcoming(\?|$)/;
// Community routes (internal/api/community.go) and 赞 / 踩 / comments
// (internal/api/interactions.go): a signed-in voter is identified by the
// token only (no X-Device-ID), and posting or liking a comment needs it.
const COMMUNITY_PATH =
  /^(releases\/[^/?]+\/(danmaku|rating|reviews|reaction|comments)|danmaku\/\d+\/report|reviews\/\d+(\/(like|report))?|comments\/\d+(\/(like|replies|report))?|reports)(\?|$)/;
// 片段 (internal/api/clips.go): public, but the token marks the viewer's own
// clip (`mine`, may delete) and is required to report one.
const CLIP_PATH = /^clips\/[A-Za-z0-9]{10}(\/report)?(\?|$)/;

/** True when a request to `path` must carry the user token. */
export function sendsUserToken(path: string): boolean {
  return (
    path.startsWith('me/') ||
    path === 'auth/me' ||
    PLAY_CONFIG_PATH.test(path) ||
    COMMUNITY_PATH.test(path) ||
    RECS_PATH.test(path) ||
    UPCOMING_PATH.test(path) ||
    CLIP_PATH.test(path)
  );
}
