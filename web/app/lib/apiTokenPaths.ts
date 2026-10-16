// Which API paths (relative to /api) get the user token (tech plan 2.6;
// lib/api.ts tokenFor). Kept free of imports so scripts/api-token-paths.test.mjs
// can run it under plain Node.

// play-config is public, but a user token adds `resume` (tech plan 3.3).
const PLAY_CONFIG_PATH = /^releases\/[^/?]+\/play-config(\?|$)/;
// Recommendations (internal/api/recs.go): public, but a user token adds the
// viewer's history, follows and watched filter.
const RECS_PATH = /^recs(\?|$)/;
// Upcoming (internal/api/catalog.go): public, but ?followed=1 needs the user
// and a token adds each item's `reserved` state.
const UPCOMING_PATH = /^upcoming(\?|$)/;
// Community routes (internal/api/community.go) and 赞 / 踩 / comments
// (internal/api/interactions.go): a signed-in voter is identified by the
// token only (no X-Device-ID), and posting or liking a comment needs it.
const COMMUNITY_PATH =
  /^(releases\/[^/?]+\/(danmaku|rating|reviews|reaction|comments)|danmaku\/\d+\/report|reviews\/\d+(\/(like|report))?|comments\/\d+(\/(like|replies|report))?|reports)(\?|$)/;

/** True when a request to `path` must carry the user token. */
export function sendsUserToken(path: string): boolean {
  return (
    path.startsWith('me/') ||
    path === 'auth/me' ||
    PLAY_CONFIG_PATH.test(path) ||
    COMMUNITY_PATH.test(path) ||
    RECS_PATH.test(path) ||
    UPCOMING_PATH.test(path)
  );
}
