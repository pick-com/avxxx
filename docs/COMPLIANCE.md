# 合规功能（PRD 15）

对应画板：`CookieBanner` / `MobileConsent`、`LegalPage`、`CopyrightForm`、`AdminOps` 版权投诉。迁移：`migrations/041_compliance.sql`。数据保留见 [DATA_RETENTION_AND_IMAGES.md](DATA_RETENTION_AND_IMAGES.md)。

标 **[法务]** 的条目需律师确认。

## 1. Cookie 同意记录

| 项目 | 实现 |
| --- | --- |
| 表 | `consent_records`：`user_id` 或 `device_hash`、`categories`（necessary 必含）、`policy_version`（写入时生效的 cookies 文档版本）、`gpc`、`source`（banner / preferences / sync / gpc）、`created_at` |
| 只追加 | 触发器拒绝 UPDATE；行只随账号删除（`ON DELETE CASCADE`）消失 |
| GPC | 浏览器 `navigator.globalPrivacyControl` 或请求头 `Sec-GPC: 1` → `personalized_ads` 强制关闭；数据库 CHECK 也拒绝 GPC 下的个性化广告 |
| 设备标识 | 浏览器随机生成 `consent_device_id`（localStorage，必要类）；服务端只存 `obs.HashKeyed` 的 HMAC（`LOG_HASH_KEY`），不存原值、不存 IP |
| 接口 | `POST /api/consent`（每地址 60 次/小时，全局 20000 次/小时）；`GET /api/me/consent` 返回登录用户最近一次选择 |
| 跨设备 | 登录后取服务端最新记录：比本地新则采用（GPC 仍优先），本地更新则以 `sync` 上传 |

**[法务]**
- 同意记录的保存期限，以及注销账号时是否应保留（作为取得同意的证明）而不是随账号删除。
- 未设置 `LOG_HASH_KEY` 时哈希键每次重启随机，旧记录无法再关联到同一浏览器；生产必须设置。是否满足"能证明谁在何时同意了什么"的要求。
- `consent_device_id` 作为"必要"存储是否成立（不经同意即写入）。
- cookies 文档 v1 简要版写着"你的选择保存在当前浏览器里"，现在也会保存在服务器：需要律师更新文本（发布第 2 版）。

## 2. 法律文本版本

| 项目 | 实现 |
| --- | --- |
| 表 | `legal_documents(kind, lang, version, title, summary[], body (Markdown), status draft/published, effective_at, published_at/by, change_note)`，唯一 `(kind, lang, version)` |
| 版本号 | 属于文件种类：en 第 3 版是 zh-CN 第 3 版的翻译，发布时沿用该版本的生效时间 |
| 冻结 | 已发布行由触发器禁止修改和删除；改文本只能发新版本 |
| 当前版本 | 已发布且 `effective_at <= now()` 的最高版本；未来生效的版本在历史列表里显示为"将于…生效" |
| 语言回退 | 请求语言 → PRD 8 回退链（zh-TW、zh-CN、en）→ zh-CN、en；响应带 `lang` 和 `fallback` |
| 种子 | 迁移 041 把原 `web/app/lib/legal.ts` 的模板文本作为 7 个文件的 zh-CN 第 1 版（仍标 [待律师审阅]） |
| 公开接口 | `GET /api/legal/{kind}?lang=&version=`、`GET /api/legal/{kind}/versions?lang=`、`GET /api/legal/terms/current-version` → `{"version": "1", "version_number": 1, "effective_at"}`（账号模块 `terms_acceptances` 用；Go 内用 `store.CurrentLegalVersion(ctx, "terms")`） |
| 后台 | `/admin/legal`：草稿新建 / 编辑、发布、与上一版的行级 diff；每次写入 `review_events`（entity_type `legal_document`，含操作人） |
| 前台 | `/legal/{kind}` 服务端渲染，`?v=N` 查看历史版本（noindex），版本选择器 |

**[法务]** 现有文本全部是模板；定稿后以新版本发布。用户协议变更后是否需要强制重新同意（目前 web 在版本号变化时提示）。

## 3. 反馈

`feedback(category, message, email?, page_url?, lang, status, admin_note, handled_by/at)`。`POST /api/feedback`（每地址 5 次/小时，全局 300 次/小时），页面地址只保留 scheme/host/path（去掉查询串和片段）。后台 `/admin/feedback`，状态变更写 `review_events`（entity_type `feedback`，不含邮箱和正文）。页脚新增 帮助中心（`/help`）、反馈问题（`/feedback`）、内容合作（`VITE_PARTNERSHIP_EMAIL` 的 mailto，未配置则隐藏）。

**[法务]** 反馈邮箱和正文的保存期限（目前无自动清理）。

## 4. 版权投诉与反通知

| 项目 | 实现 |
| --- | --- |
| 表 | `takedown_requests`（投诉人、权利人、作品、侵权 URL、善意与准确性声明、签名、时限 `due_at` = 提交 + 24 h、反通知字段）；`takedown_targets`（每个执行动作：release 下架 / 图片屏蔽，`previous_status`，`state` active/restored） |
| 编号 | `TD-XXXX-XXXX-XXXX-XXXX`，80 位随机（Crockford base32），不可猜 |
| 公开 | `POST /api/takedowns`；`GET /api/takedowns/{ticket}` 需请求头 `X-Takedown-Email`（投诉人或反通知人邮箱），只返回状态，不匹配一律 404；`POST /api/takedowns/{ticket}/counter` 只能用于已下架的编号 |
| 后台 | `/admin/takedowns`：队列 + SLA 徽章（未处理超过 24 h 标红，导航也显示）、详情、下架作品、屏蔽图片、驳回 / 结案、反通知后恢复 |
| 复用 | 下架 = 作品状态机 `unpublished`（`transitionReleaseTx`，写 `status_change`）；屏蔽 = `SuppressMediaAsset` 同一路径（`suppressMediaTx`，写 `media_suppress`，24 h 后清理对象）；恢复：作品回到 `published`（须通过 R-AGE；原状态不是 published/upcoming 或仍被其他编号下架则保持不动），图片未清理则恢复，已清理则解除下载屏蔽让 `media_fetch` 重新抓取 |
| 记录 | 每一步写 `review_events`（entity_type `takedown`），事件里不放联系方式 |
| 前台 | 被投诉下架的作品 `GET /api/releases/{slug}` 返回 451（正文不含标题），视频页服务端渲染 451 页面并链接反通知表单 |

邮件通知：未实现。现有通知 outbox（`notification_outbox`）按 `user_id` 投递并读取用户设置，投诉人不是用户；需要一个按邮箱投递的 outbox 和模板后再接入。

**[法务]**
- 指定代理人（DMCA agent）信息与登记；版权政策文本。
- 反通知后恢复的等待期（DMCA 为 10–14 个工作日）目前由管理员手动掌握，系统不计时。
- 投诉人与反通知人个人信息的保存期限，以及是否、何时把反通知内容（含联系方式）转交投诉方。
- 24 h 下架时限是否满足适用法律；CDN 模式下图片缓存最长 24 h（见数据保留文档第 4 节）。
- 地区限制：play-config 支持 `region` 并在授权地区外返回 `region_blocked`，但前端尚未发送地区（无 GeoIP / CDN 国家头）；地区判定来源需要决定。

## 5. 隐私要点

- 不存原始 IP：限流只在内存里以地址为键；同意记录只存设备 id 的 HMAC。
- 公开接口泄露检查（`internal/acceptance` 的 public-API walk）已加入投诉人 / 反通知人姓名、邮箱、电话、地址、签名、编号，反馈正文与邮箱，以及同意记录的设备哈希。
