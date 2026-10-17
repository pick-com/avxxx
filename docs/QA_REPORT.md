# QA report: hands-on pass over #97–#114

Date: 2026-10-16. Scope: every page added or changed in #97–#114, clicked
through in a real browser at 1440px and 375px, light and dark, in zh-CN, en
and ja, signed out, as two site users, and as a super admin, a second super
admin, a moderator and a translator.

## Setup

- Isolated stack: throwaway `postgres:17` container `avxxx-qa-db` (own port),
  `cmd/api` built from the branch (migrations through 048 applied on start),
  `cmd/audit` run once to drain queued jobs, `react-router dev` on its own
  port with `API_PROXY_TARGET` / `API_INTERNAL_URL` pointing at the API, and a
  60-second H.264 HLS test stream generated with ffmpeg for playback.
- Env: `ADMIN_TOKEN`, `AGE_GATE_KEY`, `LOG_HASH_KEY`, a random
  `ADMIN_TOTP_KEY`, `BASE_URL`, `CATALOG_CONTACT`, `MEDIA_LINK_ON_DEMAND=false`
  (no calls to real providers).
- Data: `scripts/seed-demo.sql` was too small, so a QA seed added 12
  performers, 30 releases (4 in a series, one edition, one takedown target),
  4 upcoming releases, tags, playback sources (preview + an alt "other
  version" source), device ratings, a hero slot, a series shelf, an
  announcement, site metrics and an identity candidate. Everything else was
  created through the UI: two accounts (one with 记住我 off), likes, ratings,
  short reviews, comments and replies, a comment edit, a report, playlists, a
  link-shared playlist, watch later, follows, a reservation, danmaku and a
  block word, search history, feedback, a copyright notice, an approval, a
  service token, an announcement, a legal translation.
- Admins: `cmd/admin create` for two super admins, a moderator and a
  translator; TOTP codes computed from the printed secrets (RFC 6238).
- Browser: the desktop app's browser pane was at its tab cap (tabs owned by
  other sessions), so the pass used Chrome for Testing (headless, Playwright
  driver) with the same checks: console errors and warnings (hydration
  included), failed and 4xx/5xx requests, horizontal overflow, text that looks
  like an untranslated key, and screenshots of every page in each mode.

## Bugs found and fixed

| # | Severity | Bug | Fix |
|---|----------|-----|-----|
| 1 | High | Signed-in users could not 赞/踩 a release (every vote 400: no token and no `X-Device-ID`), could not post, like, delete or report comments (401), and 即将上线 > 只看关注的演员 answered 401 (reserved state never shown). `tokenFor()` in `lib/api.ts` did not list the reaction, comment and upcoming routes. | `fix(web): send the user token to 赞/踩, comments and upcoming`; regression test `test(web): lock which API paths carry the user token` (`npm test`, added to CI) |
| 2 | Medium | 下载我的数据 export left out comments and comment likes, release reactions, recent searches and consent records, although the settings row promises 评论. | `fix(account): data export includes comments, 赞/踩, searches and consent` (integration test extended) |
| 3 | Medium | 你关注的演员有新作 shelf named the release's lead (`QA Actress 9 等`) instead of the followed co-star; in en/ja the card reason said "New from QA Actress 9, who you follow". | `fix(recs): followed_new names the followed performer, not the lead` (reason carries `name`; unit test) |
| 4 | Medium | A translator (any role without `dashboard:read`) landed on 总览 after sign-in: all cards 403, two 当前角色没有此操作的权限 banners, links to hidden pages. | `fix(admin): roles without 总览 land on their first permitted page` |
| 5 | Low | 字段冲突 and 重复与归并 showed `请求失败（403）` plus "请先输入管理员令牌" to signed-in admins without the permission (and called a 401 an invalid admin token). | `fix(admin): 字段冲突 and 重复与归并 explain 401/403 like other pages` |
| 6 | Low | 稍后观看 rows at 375px squeezed the title to ~15px (one character per line). | `fix(web): watch-later rows keep a readable title at 375px` |
| 7 | Low | Performer page at 375px: 作品 / 粉丝 / 30 天热度 broke one character per line; in en the 全部 / 主演 / 客串 filter ran off screen. | `fix(web): performer stats and role filter fit at 375px` |
| 8 | Low | /settings fired `GET /api/me/sessions` and `/api/me/identities` (two 401s) for signed-out visitors before the sign-in prompt. | `fix(web): settings account section waits for the session` |

## Pages and results

Legend: OK = rendered at both widths and both themes with no console errors,
no failed requests and no overflow, and the listed interactions worked.

### Public

| Page | Result |
|------|--------|
| Age gate | OK. Tested first with the birth-month gate: ok (1990-04) enters and keeps the page; denied (2015) shows the restricted page with the unlock time; locked: reload stays denied and `POST /api/age-gate` answers 403; legal/help/copyright stay reachable. Retested after rebasing onto 3ea4bc6 (checkbox-only): the button stays disabled until the box is ticked, then the page renders and playback auto-loads (4d62601). The gate copy is stale, see decision 7. |
| Home | OK: hero, tag chips, 编辑精选 series shelf, 继续观看, 为你推荐, 你关注的演员有新作 (bug 3), 新上线, 热门演员; two announcements, dismiss persists per account; region-targeted announcement shown only with `CF-IPCountry: JP` (SSR and after hydration). |
| Discover | OK: 热门 (empty, then filled after the trending job), 新上线 filters, 即将上线 incl. 只看关注 (bug 1), 分类, 演员 with era / genre / language / sort. |
| Search | OK: 全部 / 演员 / 视频 tabs, duration / year / quality / subtitle filters, recent searches on the empty page, clearing from settings. |
| Release | OK after bug 1: 赞/踩 toggle and persist, rating, short review, comments, replies, edit (已编辑), report; share with `?t=` (starts at 0:30, share link carries the current time); playlist picker incl. new playlist; 其他版本 after linking in dedupe; 同系列 panel; mobile fullscreen 选集 drawer; danmaku post, settings sheet, block word synced to settings; 451 page for the taken-down release (zh and en). |
| Performer | OK after bug 7: stats, 主演/客串 filter, 即将上线 with 预约, follow, 随机播放全部. |
| Series list / series | OK. |
| History, 稍后观看, 收藏夹 | OK after bug 6: reorder by keyboard persists, 全部播放 opens the queue (`?list=playlist:2`), visibility switch, share link. |
| Public `/playlists/:token` | OK signed out and in en. |
| Login / register | OK: 18+ checkbox required (browser validation), terms checkbox, 记住我 off gives a browser session, wrong password message, forgotten password form. |
| Reset password | OK: valid token sets the password and old one stops working; invalid token message. (Mail itself not delivered, see below.) |
| Welcome (onboarding) | OK: language/appearance, interests (needs 3), follow step; interests saved. |
| Settings | OK after bug 8: account (nickname, password change signs out other devices, sessions list, revoke), playback (默认片源 persists), language, notifications (提醒时机 persists, daily cap, quiet hours), privacy (export download, clear search history), account deletion (scheduled, cancelled by signing in again). |
| /me/stats | OK, empty state and after the watch-stats job. |
| Legal, help, feedback, copyright | OK in zh/en/ja; untranslated documents show the fallback notice; feedback and copyright notice submit and appear in the back office. |
| Cookie banner | OK: 只用必要的 / 全部接受 / 自己选择; choice synced to the account. |

### Back office

| Page | Result |
|------|--------|
| Sign-in | OK: password, then TOTP; four admins. |
| 总览 | OK: KPIs, 播放与日活 from site metrics. Translator redirected (bug 4). |
| 用户与权限 / 待审批 | OK: role change by one super admin becomes an approval; the requester and the target cannot approve; the second super admin approves and it executes. Service token created with the SEO preset works only on its scope (POST seo/report 400 on empty body, others 403). |
| 操作日志 | OK. |
| 夜间任务 | OK: 立即运行 queued six jobs, all succeeded when `cmd/audit` ran. |
| 举报与审核 (评论 tab) | OK: keyword shadowing, queue, delete, edit history, report dismissal. |
| 视频 detail | OK: 基本信息, 片源, 演员, 发布 (推荐位, 版权凭证 section), 数据标签 curves, 多语言, 修改记录. |
| 重复与归并 | OK: compare summary and 关联为其他版本 (shows on both release pages). |
| 版权投诉 | OK: notice from the public form, 下架作品 → 451 publicly, history recorded, claimant mails queued. |
| 法律文本 | OK: en translation draft with diff, publish, shown on /en/legal/terms. |
| 用户反馈 | OK: status change. |
| 首页排期与公告 | OK: new announcement live immediately. |
| 字段优先级, 片源映射, 完整性报告, 入库流水线, 数据质量, 搜索词情报, 翻译管理, 演员 | Rendered without errors (read-only check). |
| Permissions | Moderator nav matches the API (hidden pages answer 403 with a clear message). Translator sees only 翻译管理, 用户与权限, 待审批; other pages show 当前角色没有此操作的权限 (bug 5 fixed the two that did not). |

## Not tested, and why

- Real mail: password reset and takedown mails are queued (`notification_outbox`,
  `mail_outbox`) but not delivered without SMTP; the reset link was exercised
  with a token inserted the same way the notifier mints one.
- Web Push (no VAPID keys), Google / Apple sign-in, S3 images, the media_link
  provider and third-party crawlers: need external services or credentials.
- The desktop browser pane itself (tab cap reached by other sessions); a
  headless Chrome for Testing was used instead. Real phones and Safari/iOS
  fullscreen were not available.
- Play curves were checked with seeded arrays, so the numbers on 数据标签 are
  not meaningful; the job ran but had too few events to recompute them.

## Needs a product or design decision

1. No favicon: every first page load logs a 404 for `/favicon.ico`. Needs a
   brand asset.
2. English (and other non-CJK) strings have no plural forms: "1 videos",
   "1 plays", "Longest: 1 days", "1 titles started". The i18n layer would need
   plural support.
3. `GET /api/push/vapid-public-key` (404 when VAPID is off) and
   `GET /api/me/consent` (404 before the first saved choice) log console
   errors on welcome, notification settings and login. Behaviour is as
   designed; a 204 or an explicit "disabled" body would keep consoles clean.
4. The legal editor accepts `# Heading` lines, which the Markdown subset
   renders as literal text (only `##` / `###` are headings). A warning or
   preview in the editor would avoid it.
5. The cookie banner also appears in the back office.
6. Recent searches are stored normalized ("Summer" is listed as "summer").
7. Since the checkbox-only gate (3ea4bc6) the gate text still describes the
   old form: `age.gateBody` asks to 填写出生年月, `age.gatePrivacy` says the
   birth month is only used for this check, `age.gateNotice` says under-18
   answers lock the site for 24 hours, and the unticked-box error reuses
   `login.pickBirth` (请选择出生年月), in all eight languages. This is
   compliance copy, so it was left for whoever owns the wording rather than
   rewritten here.

## Round 2: hands-on pass over #118–#169

Date: 2026-10-17. Scope: the features merged in #118–#158 that had only been
covered by builds and tests, plus a spot check of #160–#169 after the rebase.
Clicked through in the built-in browser pane at 1440px and 375px, in zh-CN
and en, signed out and signed in (one throwaway `@example.test` account,
created through the sign-in sheet), and with the break-glass admin token.

### Setup

- The shared local test API (compose project `avxxx-localtest`, port 18080)
  was down for most of the pass (container exited 137, stopped from
  elsewhere), so `cmd/api` was built from this branch and run on its own port
  against the same localtest database, with a generated `ADMIN_TOKEN`
  (no OAuth, S3, OpenRouter or real `AGE_GATE_KEY`). After the rebase the web
  app was pointed back at 18080 (#169) to recheck the fixes and #160–#169.
- `react-router dev` from the worktree with `API_PROXY_TARGET` and
  `API_INTERNAL_URL` (SSR needs the second; without it every page renders
  client-side after a 503).
- Playback used the database's one resolvable javtiful mapping (SNOS-397,
  JUQ-743); DOA-078 has no source.
- No rows were written to the database directly. Test data was removed
  through the API afterwards (comment, clip, 不感兴趣, favorite, watch
  history) and the account was put into deletion (30-day purge).

### Verified

| Area | PR | Result |
| --- | --- | --- |
| 片源 details card | #118 | Provider, resolution, format; fixed: 720 → 720p, missing MP4 (bug 2) |
| Fullscreen without the 弹幕 row | #119 | Code path checked (`!fullscreen` hides the row); the pane refuses `requestFullscreen`, so not seen in a real fullscreen |
| No-source cover on DOA-078 | #120 | 暂无视频资源 / 这部作品还没有可用的片源 |
| Resume from history | #121 / #129 | Guest: resume prompt from 10:19 after leaving and coming back; signed in: from server history (JUQ-743 0:57) |
| Pause when hidden | #132 | Simulated `visibilitychange` (the pane never hides a tab): pauses, stays paused on return |
| Shortcuts | #138 / #144 / #148 | ↑/↓ 5% steps inside the player (the page scrolls on the body, by design), T, Shift+N, ?, / ; fixed: / missing from the ? dialog (bug 3), menu button name (bug 7) |
| Play queue | #143 | 添加到播放队列 note, sessionStorage queue, 播放队列 panel with 上移 / 下移 / 移除 / 清空 |
| Clickable timestamps | #142 | Links with 跳到 m:ss 播放; fixed: the player never scrolled into view (bug 4) |
| Loop, A-B, sleep, ambient | #155–#157 | Loop switch and toast, A-B jumps back to A, 15-minute timer shows 剩余 15:00, ambient reports 当前片源不支持氛围模式 for the cross-origin stream; fixed: that notice flashed on every load (bug 6) |
| Clips | #158 / #167 | Clip 1:32.5–1:47.5 created, `/clips/<id>` plays the range, listed on 我的片段 |
| 不感兴趣 / 不推荐此演员 | #141 | Card turns into 已隐藏 · 撤销; actor's works leave every home shelf after reload; settings 推荐反馈 lists both and 撤销 works; fixed: Chinese reasons in en (bug 5) |
| History search | #145 | Signed in (server) and guest (local) by title, performer, code; fixed: guest search ignored text past 100 characters (bug 1) |
| Sign-in sheet | #150–#154 | Opens from 收藏 / 稍后观看 / 评论; sign-up in the sheet finishes the pending 收藏 (收藏夹 picker); comment draft restored into the box and focused; guest history merged into the account; reminder bar on a guest resume, once per day; fixed: 视频会继续播放 shown with no video (bug 8) |
| Admin 机器翻译 | #147 | Disabled with the tooltip 未配置 OPENROUTER_API_KEY, as expected without a key |
| Pipeline proxy field | #149 | XSList 编辑策略: a URL with credentials is refused (400), `env:CATALOG_PROXY_VPN` saves and shows 代理 env:…, clearing restores inheritance (left cleared) |
| Cast | #131 / #139 | Remote Playback present, Cast sender loads in Chrome; no button without a device, as designed |
| 375px | — | No horizontal page overflow on home, history, settings, release (with and without source), clip, trending, discover; settings sheet tabs scroll |
| #160–#169 | — | /me/clips lists the clip; release page, reasons, card menu and settings sheet recheck clean on 18080 |

### Bugs found and fixed

One commit each, with a `node --test` / Go test where the logic is pure.

1. **History search (guest) missed most of long titles** — medium.
   `matchesHistory` normalized titles with the query normalizer, which caps at
   100 characters; guest history has no cast, so the performer name at the end
   of a long JAV title never matched (`kuraki` found nothing, `tipsy` did).
   Only the query is capped now. Test: `history-filter.test.mjs`.
2. **片源 card read 清晰度 720 with no 格式** — low. Resolved streams carry
   quality `"720"` and protocol `mp4` on an extension-less signed URL. Bare
   heights read `720p`; the protocol names the format. `lib/streamFormat.ts`,
   `stream-format.test.mjs`.
3. **? dialog did not list /** — low. Added 转到搜索框 in all seven languages.
4. **Clicking a timestamp did not bring the player into view** — medium. It
   scrolled `.player-slot`, which is `display: contents` (no box), so the
   seek happened off screen. It scrolls `.video-main` with a scroll margin
   for the sticky top bar. Checked in the browser (player top at 80px after
   the click).
5. **Chinese 推荐理由 in English** — medium. `reasonText` kept the server's
   zh-CN text for trending / similar_tags / because_watched, so the en video
   page showed 本周热门 · 第 1 名. They use the localized template and keep
   the rank / tag (`Trending · #1`). `lib/reasonParse.ts`,
   `reason-parse.test.mjs`.
6. **氛围模式 notice on every page load** — low. With the saved preference
   and a cross-origin source, 当前片源不支持氛围模式 flashed on each load,
   over the resume prompt. It now follows a switch-on in the same page.
7. **Player menu button names** — low (a11y / i18n). `PlayerMenu` built
   `label + "：" + text`, so screen readers heard 倍速：倍速, and English got
   `Speed：Speed` with a full-width colon. New `pc.menuButton` template; the
   label alone when the text repeats it.
8. **Sign-in sheet said 视频会继续播放 with no video** — low. Also on a
   release without a source and for a paused video; a new
   `authPrompt.subNoVideo` is used then.
9. **Aborted requests logged as 500** — medium (observability). A fetch
   aborted by navigation or an effect cleanup made the store call fail with
   `context.Canceled`, answered 500, logged at ERROR and counted in the 5xx
   metrics: 5–6 per video-page navigation. `errorStatus` maps it to 499
   (client closed request). Test: `TestErrorStatus`.

### Not fixed (noted)

- `GET /api/releases/{slug}/playback?source=javtiful` answers 404 for a
  release with no media link, so every no-source page logs a console 404 from
  the legacy fallback after play-config returns no sources (same family as
  #125).
- SNOS-397's 导演 shows `Genre(s): 4K Exclusive Distribution …` — a bad
  `director_name` value in the local data, not traced to a source record.
- The 机器翻译 button explains itself only through `title` on a disabled
  button, which keyboard and touch users do not get.
- `PUT …/crawl-policy` errors are English inside the Chinese back office.
- Page heading 观看记录 vs. the search placeholder 搜索播放记录.
- Untestable here: real fullscreen and the sheet inside it (#119, #160 —
  the pane denies `requestFullscreen`), a real hidden tab (#132, simulated),
  Cast / AirPlay with a device, Google / Apple sign-in.
- Items the parallel agents own were not touched: onboarding after a sheet
  sign-up (no nudge was seen before the rebase; #161 landed since), review
  draft restore (#163), guest rec_feedback claim (#164), CardMenu clipping
  (#165), 我的片段 and clip moderation (#167 / #168).
