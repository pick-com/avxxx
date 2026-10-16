# 数据保留和图片使用策略

适用范围：目录图片（作品封面、演员主照片）、来源原始数据、事件数据和账号数据。实现：`internal/objstore`（S3 兼容存储）、`internal/images`（策略、抓取、清理）、`migrations/031_media_storage.sql`。存储设计见 [IMAGE_STORAGE.md](IMAGE_STORAGE.md)。

标 **[法务]** 的条目需律师确认后才能在生产启用。

## 1. 哪些来源的图片可以存储

图片下载只在下面全部成立时发生（`images.SourceAllowed`，每次抓取前判断，任一不满足即跳过，不发请求）：

| 条件 | 字段 |
| --- | --- |
| 来源许可明确覆盖图片：`official_api`、`licensed`（或自有数据 `internal`） | `sources.license_status` |
| 条款已复核 | `sources.terms_reviewed_at` |
| 管理员单独确认「图片可存储」并留下复核人和说明 | `sources.images_allowed` + `images_reviewed_at/by` + `images_note` |
| 不是 RSS/feed 来源 | `sources.adapter` |

- `terms_confirmed` 只代表条款允许自动采集 **metadata**，不代表允许转存图片；`review_required` / `manual_only` / `denied` 一律不下载。
- `images_allowed` 默认 `false`，迁移不为任何来源开启。开启接口 `PUT /api/admin/sources/{id}/images` 拒绝许可等级不够的来源；之后若许可等级降级，抓取立即停止（判断在运行时做，不依赖旧标记），已存图片按第 4 节清理。
- 只存两类资产：作品封面（`release/cover`）和演员主照片（`performer/avatar`）。样图（`sample_image_urls`）、图集、预告视频**不存**，仍只作为来源证据和外链。**[法务]** 是否可以存样图。
- **[法务]** DMM/FANZA Affiliate API：封面和女优图片能否转存、改尺寸、经自有域名/CDN 分发，还是只能直链原地址并附带署名。确认前 DMM 来源保持 `images_allowed=false`。
- **[法务]** 演员肖像权：即便来源许可允许，是否需要额外同意或仅限官方宣传照。

## 2. 存什么

| 项目 | 规则 |
| --- | --- |
| 原图 | 只接受 `image/jpeg`、`image/png`、`image/webp`（响应头和内容嗅探一致）；≤ 8 MiB（`IMAGE_MAX_BYTES`）；最短边 ≥ 64 px，最长边 ≤ 8000 px 且 ≤ 40 MP（防解压炸弹）。最长边超过 1600 px（`IMAGE_MAX_DIMENSION`）时等比缩到 1600 px 并转 JPEG 存储，否则原样存储 |
| 派生图 | 320w、720w 两档 JPEG（质量 82，纯 Go 编码，无 CGO）；原图不比档位宽时不生成该档 |
| 对象键 | 原图 `{entity_type}/{entity_id}/{asset_type}/{sha256}.{ext}`；派生图 `{…}/{sha256}.w320.jpg`，sha256 是**下载字节**的哈希 |
| 去重 | 同一实体、类型、sha256 只存一份；已存在则不重复上传 |
| 元数据 | `media_assets`：来源、来源记录、原始 URL、MIME、宽高、字节数、`variant`、`derived_from` |
| 访问 | 私有 bucket，不设公开策略。默认经 API `GET /api/images/{key}` 读取（每次校验未屏蔽、实体公开）；可选 `S3_PUBLIC_BASE_URL`（CDN 回源私有 bucket）或 `IMAGE_URL_MODE=signed`（短期签名 URL） |

下载规则与目录爬虫相同：带联系方式的 User-Agent（`CATALOG_CONTACT`）、robots.txt（取不到按禁止处理）、每来源请求间隔 ≥ 2 s、每次运行请求数上限、遇 403/429/挑战页即停该来源。失败只记 `media_fetch_attempts`（指数退避 1 天起、最长 30 天），不写空资产。

## 3. 保留期

| 数据 | 保留 | 执行 |
| --- | --- | --- |
| 图片原图和派生图 | 实体公开期间（作品 `published`/`upcoming`，演员 `active`，未被合并） | `media_purge` 任务 |
| 屏蔽的图片（`suppressed_at`） | 屏蔽后 24 h 内删除对象和行（`MEDIA_PURGE_AFTER`，默认 24h） | `media_purge`，每小时 |
| 下架、R-AGE 暂扣、隐藏、合并后的实体的图片 | 对外立刻不再返回；24 h 后删除；24 h 内恢复公开则保留 | `media_purge` 置/清 `retained_until` |
| 孤儿对象（bucket 里有、`media_assets` 无） | 上传 24 h 后仍无记录即删除 | `media_purge` 分批扫描 bucket |
| 来源原始数据 `source_records.raw_data` | 来源记录存在期间保留（字段证据、拆分/合并重算都依赖它）。**[法务]** 来源转为 `denied` 或合作终止后是否需在 30 天内清空 raw_data —— 目前未自动执行 | 人工 |
| 原始事件 | 90 天（PRD 14.5，`events` 按月分区） | 既有 |
| 聚合结果（曲线、日报） | 长期 | 既有 |
| 账号关联数据 | 注销后 30 天内删除（PRD 14.5）。图片不含用户数据，不受影响 | 账号模块（不在本文范围） |
| Cookie 同意记录 `consent_records` | 只追加；随账号删除。**[法务]** 保存期限、注销后是否保留为同意证明（见 [COMPLIANCE.md](COMPLIANCE.md)） | 未自动清理 |
| 反馈 `feedback`（含可选邮箱） | **[法务]** 保存期限待定 | 未自动清理 |
| 版权投诉 `takedown_requests`（投诉人 / 反通知人联系方式） | **[法务]** 保存期限待定 | 未自动清理；邮件 `mail_outbox` 不存邮箱，随编号删除 |
| 数据库备份 | 7 日 + 4 周（PRODUCTION_OPERATIONS.md）；对象存储不做版本化，删除即生效。**[法务]** 备份里残留的已下架元数据是否可接受 | 运维 |

## 4. 下架如何传到图片

| 触发 | 效果 |
| --- | --- |
| 版权投诉（PRD 15：24 h 内暂时下架） | 在 `/admin/takedowns` 对投诉编号执行「下架作品」（作品改为 `unpublished`，前台返回 451）或「屏蔽图片」（见 [COMPLIANCE.md](COMPLIANCE.md)）。公开接口和 `/api/images/` 立即不再返回；24 h 后对象删除。恢复上架后由 `media_fetch` 重新下载 |
| 演员 `remove_photo` 请求批准 | 既有逻辑把该演员 `avatar`/`gallery` 全部置 `suppressed_at`；`media_fetch` 以后不再为此演员下载照片（以批准的请求为准，永久生效）；24 h 后对象删除 |
| 管理员屏蔽单张图 `POST /api/admin/media/{id}/suppress` | 原图和派生图一起屏蔽，写 `media_blocks`，之后不再为该实体该类型下载；写 `review_events`（`media_suppress`，含操作人） |
| 管理员恢复 `POST /api/admin/media/{id}/restore` | 未清理前可恢复；删除 `media_blocks`；写 `review_events`（`media_restore`） |
| 来源许可降级或关闭 `images_allowed` | 停止新下载。已存图片保留到 **[法务]** 确认是否需要随许可一起删除；需要时用屏蔽接口批量处理 |

CDN 模式下对象删除后 CDN 缓存最长仍保留 `Cache-Control: max-age=86400`（24 h）；更快下架需在 CDN 侧主动清缓存。**[法务]** 24 h 是否满足版权投诉时限。
