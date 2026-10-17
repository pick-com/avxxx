# Security review: #100–#187

Scope: the high-risk code merged in PRs #100–#187: auth (#100, #150–#154,
#160–#164), the admin back office (#107, #108), the age gate (#111, #116),
public data exposure (clips #158/#167–#169, comments, playlists, takedowns,
consent, legal), outbound fetchers (images, resolve refresh #133–#136, the
catalog proxy #149, OpenRouter #147, Google Cast), web XSS and redirect
sinks, and rate limits on public POST routes. Reviewed at `a5b5f9b`
(2026-10-17).

This list only includes issues with a concrete exploit path. Line numbers
point at the code before the fix.

## Findings

| # | Severity | Confidence | Where | Fix | Test |
|---|----------|------------|-------|-----|------|
| 1 | Medium | High | `internal/api/admin_users.go:69` (`inviteAdmin`), `:90` (`reissueInvite`); `internal/api/admin_perms.go:65-66` | `665bf10` | `TestSuperAdminEnrollmentLinksNeedSecondAdmin` (`internal/api/admin_invite_test.go`) |
| 2 | Medium | High | `internal/api/media.go:36-38` (`/api/search`, `/api/detail/...`, `/api/resolve/...`) | `c9d23c0` | `TestMediaProviderRoutesAreRateLimitedPerAddress` (`internal/api/media_limit_test.go`) |
| 3 | Low–Medium | High | `internal/api/community.go:394` (`putRating`), `internal/api/interactions.go:123` (`setReaction`) | `c6e3f64` | `TestAnonymousVotesAreLimitedPerAddress` (`internal/api/anon_vote_limit_test.go`) |
| — | Test gap | — | `internal/acceptance/publicapi_integration_test.go` (leak walk) | `5fa69c9` | `TestPublicAPINeverLeaksIntegration` |

### 1. A single super admin could defeat dual approval (Medium, high confidence)

Dual approval covers role changes, 2FA resets and re-enabling accounts. But
`POST /api/admin/admin-users` (invite) and `POST /api/admin/admin-users/{id}/invite`
(reissue an enrollment link) were plain `wr(gAdminUsers)` routes. So one
super admin could do this alone:

1. Invite `puppet@attacker` with `roles: ["super_admin"]`. The response
   carries the enrollment link.
2. Enroll the account with a password and TOTP.
3. Use the puppet account to approve the original admin's own pending
   requests. `ClaimAdminApproval` only checks that the approver is a
   different account.

The reissue route allowed a variant of the same attack. Admin A requests a
2FA reset for super admin C, and B approves it and receives C's link. A
then reissues the link, which revokes B's, and enrolls as C.

**Fix:** an invite whose roles include `super_admin` is now stored as an
approval request (202) instead of running, and so is reissuing the link of
an invited `super_admin` account. Both go through `needsSecondAdmin` and
`storeApproval` in `admin_auth.go`. After approval, the enrollment link goes
to the approver, as it already does for reset-2fa. Invites for other roles
and break-glass (the documented bypass, logged) are unchanged. The admin web
page handles the 202 response, and `docs/DEPLOYMENT.md` 2.7 is updated.

### 2. The public media provider routes had no rate limit (Medium, high confidence)

`POST /api/resolve/{source}/{id...}`, `GET /api/detail/{source}/{id...}` and
`GET /api/search` are public, and 19 provider adapters are registered in
`cmd/api`. The resolve cache (#133/#134) and the `?refresh=1` throttle
(#135) only dedupe one `(source, id)` key, and the id comes from the client.
Any uncached id, or a refresh of a new id, is a live request to the third
party. One client looping over ids could make the server send an unbounded
number of provider requests. That would get the server's address blocked at
the provider, which breaks playback for everyone. It also turns the API into
a free scraping proxy. There is no rate limit at the edge either: the deploy
config has no `limit_req`.

On the narrower question of whether `refresh=1` can hammer the provider:
for a single id, no. It is throttled to once per
`DefaultResolveRefreshInterval`. The real gap is that both refreshes and
plain resolves are unbounded across ids.

**Fix:** per-address limits in `mediaLimited`. Resolve is limited to 120 per
10 minutes, `?refresh=1` additionally to 20, and search + detail together to
60. Requests past a limit get a 429 with `Retry-After`, and the provider is
not contacted.

### 3. Anonymous 赞/踩 and ratings were unlimited per address (Low–Medium, high confidence)

Anonymous votes are keyed on `X-Device-ID`, which the client chooses.
Reactions were limited only per voter key (`ReactionPerMinute`), and
ratings had no limit at all. A script that sends a fresh device id on each
request can cast any number of votes:

- 赞 inflates the public like count.
- 踩 feeds recommendations.
- Ratings count at weight 0.3. The rating-flood detector only catches
  identical scores in one hour.

**Fix:** `allowAnonVote` adds a per-address limit of 60 anonymous votes per
10 minutes to both routes. Signed-in voters are unchanged: they get one vote
per account plus their existing limits.

### Leak-walk coverage (test only)

The acceptance leak walk substituted `"1"` for `{token}` and `{kind}`, so
two route groups were walked without real values:

- `GET /api/playlists/{token}`
- `GET /api/legal/{kind}` and `/versions`

The walk now covers:

- a private playlist: its name and token must never show, even to someone
  who has the token;
- a link-only playlist that also holds a non-public release: neither that
  release nor the owner-only `share_token` may show;
- every legal kind, with a draft document present that must not be served.

The code already handled all of these correctly; this commit is regression
coverage only.

## Areas checked and found sound

**Auth**

- **Sessions and remember-me.** Tokens are 32 random bytes, and only the
  SHA-256 is stored. Lifetimes are enforced server-side: 30 days remembered,
  24 hours otherwise. Bans are checked in `createSession`, so every sign-in
  path goes through the check, and a ban also deletes existing sessions.
  Password reset revokes all sessions, and a password change revokes all but
  the current one.
- **Password reset.** The token is 64 hex characters, single use, and
  hash-only in storage. It is minted by the notifier and travels in the URL
  fragment (`/reset-password#token=`). Forgot-password gives the same answer
  whether or not the account exists. Limits: per address in memory, per
  account in the database (`FOR UPDATE`).
- **OAuth/OIDC.**
  - The state is hashed, single use, consumed before anything else, and
    bound to its provider.
  - PKCE verifier and nonce are stored server-side.
  - A browser-binding cookie (HttpOnly, path-scoped, compared in constant
    time) defeats login CSRF and callback replay.
  - The login code is single use, lasts 2 minutes and travels in the
    fragment.
  - `next` is sanitised server-side, and `/\` is refused.
  - Account linking requires the bearer token at start and binds the user id
    into the state. A provider email that is already registered is never
    auto-linked (`ErrIdentityEmailTaken`).
  - The last sign-in method cannot be unlinked.
- **Sign-in sheet pending actions** (#151). They live in sessionStorage only
  (same tab), expire after 10 minutes, and are bounded. Text is put back
  into its input and never auto-posted. Only same-origin script can write
  them.
- **Guest history merge** (#152). `watched_at` is capped at `now()` (in both
  the API and the SQL `LEAST`), so a client cannot push an entry ahead of
  later saves. Backdating only affects the caller's own rows and can only
  move them *out of* the trending window. Progress is client-reported for
  every save anyway, so the merge adds no new trust.
- **Rec feedback claim** (#164). It needs a valid user token *and* the
  device's `X-Device-ID`. That is a random per-browser id (8–128 chars)
  that is never returned by any API or stored raw: only a hash is kept, and
  it is distinct from the analytics and consent ids. Claiming someone
  else's feedback would require knowing their device secret. The route is
  rate limited per address and per account.

**Admin**

- **Role matrix.** Every `/api/admin/` route is wrapped by `srv.admin` (a
  grep confirms no unwrapped pattern). Permissions come from `r.Pattern`.
  Unmapped routes are super_admin only, and a test fails on unmapped
  routes.
- **Dual approval.**
  - The approver must hold the route's permission and be a different
    account.
  - The claim is atomic (`status='pending'` → `executing`), and approvals
    expire.
  - The replay re-checks that the requester is still active and holds the
    permission, and refuses a route pattern that has changed.
  - Finding 1 was the only bypass found.
- **Break-glass.** Compared in constant time on SHA-256 digests. Every use
  is logged with the hashed IP, and it can be turned off.
- **Service tokens.** Scopes are checked per pattern. Admin-users, op-log,
  self and dual routes are denied both at creation and per request. Only a
  super admin can create them, and every request is logged.
- **Admin cookie.** HttpOnly, `SameSite=Strict`, `Path=/api/admin` (which
  does not match `/api/admin-auth`), Secure behind https. The
  `X-Admin-CSRF: 1` header is required, and the API sends no CORS headers at
  all, so the header cannot be sent cross-origin.
- **Sign-in.** Rate limited per IP, with per-account lockout and TOTP replay
  protection (`totp_last_step`).
- **Op log.** Writes, break-glass use, service tokens, replays and PII reads
  are logged with the keyed IP hash.

**Age gate**

- **Cookie signing.** HMAC-SHA256 over the version, verdict and expiry,
  compared in constant time, in both Go and Node.
- **Fail-closed key.** On a public https deployment without `AGE_GATE_KEY`,
  both processes use a random per-process key, so no cookie verifies.
- **SSR gating.** The root renders only the gate until the verdict is ok.
  Every page loader with data calls `isGated`, which keeps body data out of
  the hydration payload. The exceptions are legal (exempt), not-found and a
  redirect-only route.
- **Exempt paths.** The regex is anchored after the language prefix is
  stripped. Variants such as a trailing slash fail closed (gated).

**Public data exposure**

- **Clips.** Public ids are 10 base62 characters (about 59 bits). Hidden
  (shadowed, deleted or unpublished) clips and shadowed titles reach only
  the author, and `title_status` is shown to the author only. The leak walk
  covers both anonymous visitors and another signed-in user.
- **Comments.** The body of a shadowed or deleted comment is never shown,
  the 踩 count is private, and author emails never appear.
- **Playlists.** Tokens are 128-bit UUIDs. Private playlists return 404, the
  owner's token is blanked, only published items are listed, and responses
  are `no-store` with `noindex` for link-only playlists.
- **Takedowns.**
  - Tickets are 80-bit random values.
  - A status lookup needs ticket + email (sent in a header, kept out of
    URLs), and any mismatch is a 404.
  - A taken-down release returns 451 without naming itself.
  - All three public takedown routes have per-IP and global limits.
- **Consent.** Only a keyed hash of the device id is stored, never the IP.
- **Legal.** Public routes serve only published versions. The admin routes
  are in the role matrix (`legal` group).

**Outbound fetchers**

- **Image fetch.**
  - Only http(s) URLs without userinfo are accepted, and literal internal
    hosts are refused.
  - The dialer `Control` hook re-checks every resolved address, which
    defeats DNS rebinding.
  - Redirects are re-validated (at most 3), and proxy env vars are ignored.
  - Bodies are capped, and images are decoded and re-encoded to JPEG.
    Serving uses `nosniff` and a CSP sandbox.
  - Minor hardening, not exploitable as deployed: `publicIP` does not block
    100.64.0.0/10 (CGNAT, e.g. Tailscale) or 198.18.0.0/15. Nothing in the
    deployment listens there today. Worth adding if the VPS ever joins a
    tailnet.
- **Catalog proxy** (#149). An admin can set only `direct`,
  `env:CATALOG_PROXY_*` (restricted by a regex, so an env reference cannot
  point at another secret) or a proxy URL without credentials (also enforced
  by a database CHECK). Descriptions and transport errors are redacted.
- **OpenRouter** (#147). The API process never calls the provider. The key
  only decides `configured`. `Format`, `GoString`, `LogValue` and errors
  scrub it.
- **Google Cast and media URLs.** Cast accepts only absolute http(s) URLs
  (`castableUrl`). Resolver stream URLs must be http(s) without userinfo.
  React 19 neutralises `javascript:` in `href` and `src`. Embeds are
  sandboxed iframes from adapter-validated URLs.

**Web**

- **`dangerouslySetInnerHTML`.** Used only for the theme script (a static
  constant) and source credits. `sources.attribution_html` is written only
  by migrations; no admin route edits it.
- **Legal markdown** (#126). Renders to React nodes, and `safeHref` allows
  only http(s), mailto or relative links.
- **Timestamps** (#142). Rendered as React nodes, never as HTML.
- **Data-driven hrefs.** Takedown URLs are validated as http(s) at intake.
  Admin-set links (announcements, home slots) are admin-only input, and
  React blocks `javascript:`.
- **`?next=`.**
  - `/login` and `/welcome` accept a path starting with `/`, and so allow
    `/\host` and `/\t/host`.
  - I tested this in the browser against react-router 7.18.4. Its
    `validateNavigationTarget` throws "External navigation is not allowed"
    for both, so `navigate()` cannot leave the site. Through the language
    wrapper they also become `/en/\…` on non-default languages.
  - `admin-login` requires an `/admin` prefix. The server-side
    `safeNextPath` refuses `/\`.
  - No exploitable redirect. Tightening `safeNext` to a URL-origin check
    would remove the reliance on the router; that is optional hardening.
- **postMessage.** No listeners.
- **Storage.** Session tokens sit in localStorage for remember-me, and
  sessionStorage otherwise; this is the documented design. Admin sessions
  live in the HttpOnly cookie. The break-glass token is tab-scoped in
  sessionStorage.

**Rate limits on public POST routes**

- Already limited before this review: register and login, forgot and reset
  password, OAuth start and exchange, age gate, consent, feedback,
  takedowns and counter-notices, performer requests, rec feedback and claim,
  and comments, danmaku and clips (posting limits).
- Added by this review: media resolve, detail and search (finding 2), and
  anonymous ratings and reactions (finding 3).

## Needs a product or legal decision

1. **Inviting a super admin now needs a second super admin** (finding 1).
   With only one super admin, they must use `admin create` on the host or
   the break-glass token. DEPLOYMENT.md already recommends at least two.
   Decide whether *every* invite (not just super_admin ones) should need
   approval. A sock-puppet moderator could approve moderator dual routes,
   such as rating-flood removal.
2. **Limit values** (findings 2 and 3): 120 resolves, 20 refreshes and 60
   lookups per 10 minutes, and 60 anonymous votes per 10 minutes per
   address. Households and mobile carriers behind CGNAT share an address.
   Raise the limits if real users hit 429, or require sign-in for anonymous
   votes.
3. **Counter-notices are authorised by the ticket number alone.** The
   claimant also knows it, so a claimant could file a counter-notice
   against their own ticket and move it to `counter_received`. Legal should
   confirm the counter-notice flow; one option is to send counter-notice
   tickets only to the affected uploader.
4. **Reviews show the author's watch percentage** (`watch_percent` /
   `watch_badge`) publicly. That is viewing history. Confirm it is covered
   by the privacy policy and consider hiding it when the author has paused
   history.
5. **`POST /api/events` has no per-address limit.** It predates #100 and is
   out of scope, so it was not changed. If events feed public aggregates
   (trending, play curves), add one.

## Verification

- `go test ./...` passes.
- `go test -race` passes on `internal/api`, `internal/media` and
  `internal/acceptance`.
- `gofmt -l` is clean.
- In `web`: `npm ci`, `npm run build`, `npm run typecheck` and `npm test`
  all pass.
- `make test-integration` is still **pending**, including the extended leak
  walk. The Docker VM disk was full during this review, so no Postgres
  container could start.
