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

邮件通知（迁移 048）：投诉人与反通知人不是用户，`notification_outbox` 按 `user_id`（非空、随账号删除、按用户合并、读通知设置）投递，所以另建 `mail_outbox`，不改用户通知的路径。行里只记收件角色（`claimant` / `counter`）和要告知的状态，**不存邮箱**；`cmd/notifier` 认领时才从 `takedown_requests` 读取邮箱、称呼和语言（`internal/notify/claimant.go`）。邮件与编号变更在同一事务里入队：

| 时机 | 收件人 | 模板 |
| --- | --- | --- |
| 提交投诉 | 投诉人 | `takedown_received`：编号、24 h 时限、查询入口 |
| 状态变为审核中 / 已下架 / 驳回 / 已恢复 / 结案 | 投诉人；已有反通知时也发给反通知人 | `takedown_status` |
| 收到反通知 | 反通知人（回执）；投诉人（「已收到反通知」，**不转交**反通知人的联系方式和陈述，见下方 [法务]） | `takedown_counter`；`takedown_status` |

模板在 `internal/notify/templates/takedown_*.<lang>.txt`，纯文本，7 种语言（zh-CN、en、ja、ko、vi、es、th；zh-TW 回落 zh-CN），按提交时的语言发送（投诉用 `takedown_requests.lang`，反通知用新增的 `counter_lang`）。链接只带编号（`/copyright?tab=lookup&ticket=…`），邮箱从不进 URL，查询时仍在表单里填写并经请求头发送。日志只记行 id、类型和收件角色。保留：邮件行随投诉编号删除（`ON DELETE CASCADE`）；联系方式只在编号行里，保存期限跟随本节和数据保留文档的 **[法务]** 决定；若日后清空编号上的邮箱，未发出的邮件会以「cancelled」结束。未配置 SMTP 时这些行保持 pending，48 h 后作废。

**[法务]**
- 指定代理人（DMCA agent）信息与登记；版权政策文本。
- 反通知后恢复的等待期（DMCA 为 10–14 个工作日）目前由管理员手动掌握，系统不计时。
- 投诉人与反通知人个人信息的保存期限，以及是否、何时把反通知内容（含联系方式）转交投诉方（目前只通知投诉方「已收到反通知」，不附内容）。
- 24 h 下架时限是否满足适用法律；CDN 模式下图片缓存最长 24 h（见数据保留文档第 4 节）。
- 地区限制：play-config 在授权地区外返回 `region_blocked`。地区取 `?region=`、`CF-IPCountry` 或 `GEOIP_HEADER`（部署文档 2.5）；没有 CDN 国家头时地区功能不生效，且客户端可自报地区，所以这只是展示规则，不是访问控制。是否需要更强的地区判定待定。

## 5. 隐私要点

- 不存原始 IP：限流只在内存里以地址为键；同意记录只存设备 id 的 HMAC。
- 公开接口泄露检查（`internal/acceptance` 的 public-API walk）已加入投诉人 / 反通知人姓名、邮箱、电话、地址、签名、编号，反馈正文与邮箱，以及同意记录的设备哈希。

## 6. 进站 18+ 确认

本站是成人站（`DESIGN_ALIGNMENT.md` #8 已定）：进站确认 18+，没有青少年模式和家长密码。迁移：`migrations/046_adult_confirmation.sql`。代码：`internal/api/agegate.go`、`web/app/lib/ageGate*.ts`、`web/app/components/AgeGate.tsx`。

| 项目 | 实现 |
| --- | --- |
| 进站 | 首次访问任何页面都先显示 18+ 勾选确认页：「本站包含成人内容。请确认你已年满 18 周岁（或你所在地区的法定成年年龄）。」勾选后才能点「确认并进入」，旁边有「离开」（跳出本站）。`POST /api/age-gate {confirmed:true}` 写 ok Cookie，页面刷新后才渲染内容。确认页不问出生日期，也不会拒绝或锁定 |
| 年龄计算 | 当前采用用户自我声明，不收集或计算出生年月；旧版 year/month 参数仍兼容 |
| Cookie | `ag` = `v1.<ok\|denied>.<到期时间>.<HMAC-SHA256>`，密钥 `AGE_GATE_KEY`（API 与 web 共用；未设置时用开发密钥并打警告）；HttpOnly、SameSite=Lax、Path=/，https 下 Secure。ok 约 1 年；denied 24 小时，期间接口拒绝重新提交（403），不能改填 |
| 未满 18 | denied Cookie 只来自填写了出生年月的路径：注册或老账号确认时 Cookie 不是 ok 而填了未满 18 的出生年月，或旧版客户端仍提交 `{year, month}`。denied 期间只显示说明页和法律 / 帮助链接；`/legal/*`、`/help`、`/copyright` 和后台不受拦截 |
| 不存数据 | 不收集访客出生年月；只有计数 `avxxx_age_gate_total{result=ok\|denied\|locked\|invalid}`。接口按地址限流（每 10 分钟 20 次） |
| 服务端渲染 | 根路由读取 Cookie：未确认时页面主体换成确认页，播放器、片源请求、页面数据请求都不执行；页面 loader 只取 `<head>` 需要的字段（标题、描述、canonical、JSON-LD），列表、片源署名、公告等一律跳过 |
| 搜索引擎 | 不区分爬虫和用户（不做 cloaking）：没有 Cookie 的爬虫和访客拿到同一份 HTML，`<head>` 完整，正文是确认页。站点地图照常列出内容页 |
| 注册 | 必须勾选「已年满 18 周岁」；确认 Cookie 不是 ok 时还要填出生年月（未满 18 返回 403 并写入 denied Cookie）。`users.adult_confirmed_at` 记录确认时间，`adult_birth_ym`（YYYY-MM）只在当时填写了出生年月才保存，不存日 |
| Google / Apple | 注册页勾选后才带着确认去第三方；回调时没有确认就不创建账号（`age_required`），已有账号照常登录 |
| 老账号 | `adult_confirmed_at` 为空的账号登录后弹一次确认（`POST /api/me/adult-confirm`）；拒绝则退出登录。数据导出包含这两个字段 |
| 分级字段 | `releases.age_rating` 保留为元数据，不再用于前台隐藏 |

已知限制：
- 这是自我声明，不是身份验证；确认页只要求勾选，不核对年龄。denied 的 24 小时锁定只对同一浏览器有效，清除 Cookie（或换浏览器、设备）即可重新确认。
- 内容 API（`/api/releases` 等）不受拦截，爬虫和 API 客户端可以直接读取目录数据；拦截只作用于网页。
- 拦截页的 `<head>` 里有作品标题、简介，og:image 为封面地址，用于搜索和分享预览。
- 确认页需要 JavaScript 才能提交。

**[法务]**
- 英国（Online Safety Act，Ofcom 要求「高度有效的年龄验证」）、美国多个州（如路易斯安那、犹他、得克萨斯等的成人网站年龄验证法）、欧盟部分国家（如法国 SREN 法）要求第三方年龄验证（证件、信用卡、年龄估算等），自我声明不满足。需要决定：按访问地区接入验证服务（`PRODUCT_REVIEW_AND_TECH_PLAN.md` 的 `age_verifications` 表），还是对这些地区禁止访问。地区判定来源（GeoIP / CDN 国家头）同第 4 节，尚未决定。本期不做。
- 内容 API 对未确认的客户端开放是否可接受，或是否需要对 API 也做限制。
- `adult_birth_ym` 的保存期限（目前随账号删除）；是否应完全不存出生年月，只存确认时间。
- 确认页、注册勾选的文案需律师审阅；用户协议中应写明仅限成年人使用并禁止未成年人访问。
- 是否需要「RTA」（Restricted To Adults）标签等让家长控制软件识别的标记（`<meta name="rating" content="RTA-5042-1996-1400-1577-RTA">`）。
