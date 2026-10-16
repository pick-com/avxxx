# AVXXX Roadmap 状态

> 状态更新时间：2026-10-16（#109 之后；#97–#109 的用户功能、后台和合规已并入）
>
> 当前完成度约为 **86%**。计算方法：本页所有勾选项（`- [x]` / `- [ ]`，含「进行中」小节）计数；「代码已完成、只差外部凭据或真实页面」的 4 项（DMM、ThePornDB、厂商目录 adapter、Google/Apple 登录）计入完成。共 106 项：87 项已勾选 + 4 项待凭据 = 91/106 ≈ 85.8%（只算已勾选为 87/106 ≈ 82%）。比 #92 时的 93%（78/84）低，是因为本次把 PRD/设计稿的用户功能与后台（22 项，见「用户功能与后台」）纳入了统计口径，不是有功能退回。
>
> 基础架构、目录采集、调度、审核后台、图片存储、部署/备份/恢复与全量验收（#92 之前）已完成；#97–#109 又交付了统计重建规则 G、首页运营位与公告、收藏夹、账号与 OAuth、发现/搜索/演员统计、赞踩评论、合规、观看统计、后台个人账号与 2FA、服务令牌与夜间任务页、弹幕过滤与选集。剩余项分四类：外部输入、产品决策（广告与会员）、依赖媒体资产的功能，以及少量不依赖外部输入的代码工作（见文末「剩余事项」）。进站 18+ 自我声明正在实现（定位已定为成人站，不做青少年模式）。
>
> 注意：门禁上线后，`license_status` 未审核（`review_required` / `manual_only`）或缺少 `terms_reviewed_at` / `terms_url` 的目录来源不再自动抓取，只能手动创建 verify 任务；需管理员通过 `PUT /api/admin/sources/{id}/license` 记录条款审核后才恢复增量。爬虫必须配置 `CATALOG_CONTACT`（User-Agent 联系方式），否则目录任务以 `config_error` 失败。

## 已完成

- [x] PostgreSQL 基础迁移和目录数据 schema
- [x] 来源、任务队列、租约、心跳、重试和过期任务回收
- [x] RSS scraper
- [x] 目录 adapter 接口和 HTTP 文档抓取器
- [x] JAV Database adapter
- [x] 演员、作品、标识符、标签和演员作品关系入库
- [x] 来源原始记录和字段历史证据
- [x] performer/release slug 和 dedupe 基础逻辑
- [x] DVD code 作用域和 Content ID 全局作用域
- [x] 日文名 slug 与关联：假名→罗马字 / `dmm-actress-<id>` / `performer-<id>` 兜底、永不为空；DMM `performer_refs` 按 actress id / 名称解析并关联作品；厂商作用域 `dmm-maker:<id>` / `maker-name:<hash>`，migration 028 修复历史裸 `maker:`（FINAL_DATA_DESIGN 3.1.1–3.1.2）
- [x] 字段冲突自动检测
- [x] 冲突查询和审核 API
- [x] 演员、作品、演员作品、作品演员公开 API
- [x] 标签和厂商公开 API
- [x] 统一演员/作品搜索 API
- [x] WebApp 演员、作品详情页
- [x] WebApp 标签、厂商面板
- [x] Docker Compose API/scheduler/scraper 服务
- [x] Ansible provision/deploy playbook
- [x] 作品详情的来源播放映射 API
- [x] Javtiful detail/resolve 播放路径
- [x] source record 与媒体来源映射
- [x] 后台冲突和 identity candidate 审核页面
- [x] 生产 smoke、数据库备份和恢复脚本
- [x] 管理员 Bearer Token 鉴权
- [x] 独立 scheduler 命令和到期来源任务创建
- [x] 来源级 discovery interval 调度
- [x] 来源抓取策略管理 API
- [x] Go 单元测试、`go vet` 和 `git diff --check`
- [x] 来源授权、条款、robots.txt 和配额门禁（`catalog.Gate`、`catalog.Robots`，migration 024）
- [x] baseline / incremental / verify 任务分相、独立 checkpoint 和预算
- [x] 来源级字段优先级策略（`source_field_policies` + source_kind 分层默认值）和管理 API
- [x] fixture 验收工具（`cmd/fixturecheck`、`make fixturecheck`）

## 进行中

- [x] 完善来源级字段优先级和规范字段更新策略
- [x] 增加 unresolved identity candidate 生成和候选匹配
- [x] 增加合并、拆分和撤销合并操作
- [x] 增加 checkpoint 和分页断点数据访问层
- [x] 将 checkpoint 接入每个目录 adapter 的分页执行流程
- [x] 目录批次成功后推进 discovery checkpoint

## 待完成

### 来源适配器

- [x] XCITY
- [x] Jav.Guru
- [ ] XSList（VPN/授权条件确认后启用）
- [ ] JAVLibrary（VPN/授权条件确认后启用）
- [ ] DMM/FANZA 官方目录或 Affiliate API — **已实现，待凭据实测**：`dmm` adapter（ItemList digital/videoa + ActressSearch `sort=-id`）已接入 baseline/增量/verify 阶段（增量用增量 checkpoint 水位推导 `gte_date` 并按头部记录停止，baseline 独立游标），走 robots/许可门禁；官方目录（优先级 10）按字段策略胜出作品身份字段。来源以 `official_api` 默认停用、`terms_reviewed_at` 为空：配置 `DMM_API_ID`/`DMM_AFFILIATE_ID`/`CATALOG_CONTACT` 后先跑 `go run ./cmd/scraper -dry-run -adapter dmm` 用真实响应核对字段（含 `sort=date`/`sort=-id` 排序方向），再记录条款审核并启用
- [ ] 厂商目录 adapter — **已实现，待真实页面与条款审核**：一个配置驱动的 `maker:<slug>` adapter（`internal/catalog/maker.go`），每个厂商一份 profile（`maker_profiles.json`，可用 `MAKER_PROFILES_FILE` 覆盖/新增）：列表/详情/演员 URL 正则、CSS 选择器（`@attr` 取属性）、日期格式、品番前缀 `code_pattern`（只收本厂商作品，外厂商作品在发现阶段丢弃/解析时跳过）。走通用 baseline/增量（新到旧列表、头部水位）/verify、robots、联系 UA、按来源限速与许可门禁；`official_maker` 层级（优先级 20）在本厂商作品上胜过 JAV Database 和聚合站；作用域恒为 `maker:<slug>`。已附 S1、IDEA POCKET 两个 profile，选择器只按合成 fixture 编写（`testdata/maker-*/README.md`）；migration 034 以 `review_required`、`terms_reviewed_at`/`terms_url` 为空、停用状态播种。启用步骤见 `docs/MAKER_CATALOG.md`（保存真实页面 → 调整 profile → `-dry-run -adapter maker:<slug>` → verify → 记录条款审核 → 启用）
- [ ] JavDB、IAFD、ThePornDB、javinfo 交叉验证 adapter
  - [ ] ThePornDB — **已实现，待凭据实测**：`theporndb` adapter 按官方 OpenAPI（`https://api.theporndb.net/specs?openapi.json`，文档 `https://api.theporndb.net/docs`）映射 `GET /jav`、`/jav/{id}`、`/performers`、`/performers/{id}`，`THEPORNDB_API_TOKEN` 只以 Bearer 头发往 API 主机并全程脱敏。主模式是 `crosscheck` 任务（cmd/scheduler 每 `CROSSCHECK_EVERY` 入队、cmd/audit 执行）：对缺发售日/厂商/演员/封面证据或有待审字段冲突的作品按品番查询，唯一精确品番匹配才保存为证据，尝试记录在 `crosscheck_attempts`（未找到 14 天、3 次后 60 天重试；错误指数退避）；次要模式是 `GET /jav`（按发售日倒序）的 baseline/增量/verify 抓取。`third_party_api` 层级（优先级 50）只附着到已有作品/演员、只补缺字段、不单独开字段冲突、不新建演员，作品已有他源署名时不加署名；ThePornDB 演员 UUID 写入 `performer_external_ids(namespace='theporndb_performer')`。来源（migration 032）以 `official_api` 默认停用、`terms_reviewed_at` 为空，API 文档未给出条款/署名要求，审核时需确认。配置 token 与 `CATALOG_CONTACT` 后先跑 `go run ./cmd/scraper -dry-run -adapter theporndb` 核对 JAV 品番所在字段（`sku`/`external_id`）、`duration` 单位和 `meta.last` 含义，再记录条款审核并启用
  - [ ] JavDB、IAFD：没有官方 API，条款未确认前保持在许可门禁后，只能人工 verify，不实现自动抓取
  - [x] javinfo：已评估，**拒绝**（付费的实时抓取转售 API，无服务条款与数据许可，上游含 DMM/JAV Database（已直接接入）、JavDB/JAVLibrary（自身待审核）与 MissAV 等盗版源，会丢失字段来源；详见 `docs/SOURCE_EVALUATIONS.md`）

### 增量调度

- [x] cron 只创建任务，不直接执行抓取
- [x] baseline 与日常增量任务隔离（baseline 仅由管理员请求；增量按 discovery interval 从列表头读到水位即停；独立预算；同一来源同时只运行一个任务，Claim 顺序 verify > incremental > baseline）
- [x] 分页 cursor、水位和 checkpoint 推进
- [x] 来源级请求次数、字节预算和限速
- [x] 失败批次不推进 checkpoint
- [x] blocked 来源隔离，不影响其它来源队列

### 数据质量和审核

- [x] fixture 验收工具：按来源解析 `internal/catalog/testdata/<source>/manifest.json`，报告必填字段和标识符/日期规范化比例
- [ ] 每个来源 20 个演员、100 个作品 fixture 验收（当前录制 fixture：javdatabase、xcity、javguru 均为演员 0/20、作品 0/100；仅有合成样例，需运维从线上保存页面后补齐）
- [x] PostgreSQL 集成测试（`make test-integration`，一次性 postgres:17 容器）
- [x] identity candidate 审核队列（API + `admin-dedupe` / `admin-review` 页面）
- [x] 合并、人工覆盖和撤销合并审计（`review_events`）
- [x] 拆分非合并产生的实体（`identity.BuildSplitPlan` 纯函数计划 + `store.SplitEntity`：选中的来源记录/署名证据及只由它们支持的别名、外部 ID、番号、署名、标签移到新实体，两边按字段策略重算规范字段，原实体保留 override、slug 和用户数据；不可变 `review_events(action='split')` + `not_duplicate_pairs` 防止候选生成和入库回并；`GET/POST /api/admin/{performers|releases}/{id}/split-preview|split`，`POST /api/admin/splits/{event_id}/undo` 经合并路径撤销；见 FINAL_DATA_DESIGN 5.5）
- [x] 来源可信度和字段优先级策略
- [x] 目录 source record 与 Javtiful media record 自动关联（离线 `media_link` 任务按番号精确匹配，`media_link_attempts` 记录尝试与退避；后台 `/api/admin/media-links` 可重试和人工选择）
- [x] 收录检查规则 R-SEO（`internal/audit/seo.go`，PRD 11 规则 H）：站点地图列出的作品/演员的空标题、`release-<id>` 兜底 slug、无事实可写的描述、缺存储封面、按语言重复的标题，以及最近一次 `seo.check` 报告中失败的页面开 issue；站点地图缺口、缺语言文件和过期报告写入运行详情；不自动改数据，`audit -once -rules R-SEO` 可单独运行
- [x] 作品、演员和字段完整性报告（`GET /api/admin/reports/completeness?entity=release|performer&source=&group_by=field|source|maker&format=csv`，`internal/completeness`；夜间 `completeness_snapshot` 任务写 `completeness_snapshots` 作趋势）

### Web 和 API

- [x] 演员列表、详情和作品页（列表在 discover 页面）
- [x] 作品列表、详情和演员页
- [x] 搜索、筛选、排序和游标分页
- [x] 界面多语言完整性：`web/app/locales/{en,ja,ko,vi,es,th}.json` 内置译文（已审核的 `ui_strings` 覆盖其上，缺键回落 zh-CN），`npm run i18n:check`（CI web 任务）检查缺键、多余键和 `{占位符}` 一致；zh-TW 仍只来自 `ui_strings`
- [x] 系列查询 API（`series` 实体与 `releases.series_id`，迁移 027 回填和触发器；`GET /api/series`、`/api/series/{slug}`、`/api/series/{slug}/releases?sort=episode|newest|oldest`；作品详情带 `series`）
- [x] 来源证据和更新时间展示（`GET /api/releases/{slug}/evidence`、`GET /api/performers/{slug}/evidence`，不含 raw_data、覆盖原因和隐藏生日；作品页和演员页的「信息来源」折叠区，按字段显示来源、层级、更新时间和「人工校正」）
- [x] 系列页面（`/series`、`/series/{slug}`：服务端渲染、按集数/最新/最早排序、JSON-LD、`/sitemaps/series-{lang}-{n}.xml`）
- [x] 来源署名（`sources.attribution_html` 经 `GET /api/attributions?release=|performer=|series=` 下发，原样渲染在作品、演员、系列页末尾；DMM 数据出现时显示「Powered by FANZA Webサービス」）
- [x] 管理后台冲突审核页面
- [x] 管理后台任务和来源页面（`admin-pipeline`）
- [x] 管理后台 checkpoint 页面（#88：`web/app/components/AdminSourceGovernance.tsx` 接入 `GET /api/admin/sources/{id}/checkpoints`，与许可审核、手动建任务同页）

### 用户功能与后台（PRD / 设计稿画板）

逐画板对照见 [DESIGN_ALIGNMENT.md](DESIGN_ALIGNMENT.md)，未实现画板功能见 [IMPLEMENTATION_GAPS.md](IMPLEMENTATION_GAPS.md)。

- [x] 审计规则 G 统计重建 R-STATS（#97，`internal/audit/stats.go`）：`reviews.likes`、`danmaku_curves.valid_count`、保留期内的 `search_query_daily` 按原始数据重算并在有界事务内修复；热榜 plays、年榜、`release_curves` 只报告；第二次运行修复 0
- [x] 首页运营（#98，迁移 038）：`home_slots` 主推横幅 / 编辑精选（语言、地区、时段、优先级）、`announcements` 公告条与关闭记录、`GET /api/home/top-rated-short` 高分短片；`/admin/ops` 两周排期、拖动排序、按时间/语言/地区预览，写入 `review_events`
- [x] 收藏夹与观看记录（#99，迁移 036）：多收藏夹（私密 / 链接 / 公开，可轮换 `share_token`），旧收藏回填为默认收藏夹；`/playlists/:token` 公开页（链接可见 noindex）；历史筛选、单条删除、清空、暂停；稍后看「移到收藏夹」、看完 90% 自动移出；关注动态按演员筛选
- [x] 账号（#100，迁移 037）：记住我（30 天 / 24 小时）、服务端登出、设备会话列表与撤销、改密撤销其他会话、忘记密码（只存 token 哈希、1 小时单次、统一 202、限流）、条款版本重新确认、数据导出（每天 3 次）、注销 30 天宽限后硬删除、预约提醒时机和每日上限
- [ ] Google / Apple 登录 — **已实现，待凭据实测**（#100，`internal/oidc`）：OIDC code flow + state + PKCE S256 + nonce + 绑定 cookie，ID token 全量校验，不自动关联已有密码账号；未配置时隐藏，目前只对假 provider 测试过
- [x] 发现与演员（#101，迁移 039）：`GET /api/upcoming`（按日分组、只看关注、可预约）、`GET /api/catalog/facets` 分面与类型方块、搜索类型 tab 计数、`user_search_history` 账号同步最近搜索、热门榜类型筛选；`performer_stats`（作品 / 主演 / 即将上线 / 播放 / 粉丝 / 30 天热度）每小时刷新，演员热度 / 粉丝 / 新晋排序与年代 / 类型 / 语言筛选，演员页别名、主演 / 客串筛选、随机播放全部（国籍筛选无数据未做）
- [x] 互动（#102，迁移 040）：作品赞 / 踩（踩不公开，推荐过滤踩过的作品）、评论（一层回复、剧透折叠、限流、敏感词影子屏蔽、回复通知、`COMMENTS_ENABLED`）、后台评论审核 tab、分享面板（复制链接、Web Share、`?t=` 起播时间）
- [x] 合规（#103，迁移 041；#104 修复其不稳定测试）：`consent_records` 服务端同意记录与跨设备同步（GPC 禁止个性化广告）、`legal_documents` 多语言版本化法律文本、反馈、版权投诉 / 编号查询 / 反通知、下架作品返回 451、`region_blocked` 播放状态，`/admin/legal`、`/admin/feedback`、`/admin/takedowns`；不存原始 IP（[COMPLIANCE.md](COMPLIANCE.md)）
- [ ] 合规法务结论：[COMPLIANCE.md](COMPLIANCE.md) 与 [DATA_RETENTION_AND_IMAGES.md](DATA_RETENTION_AND_IMAGES.md) 中标 **[法务]** 的条目（法律文本定稿、反馈与投诉数据保存期限、反通知等待期、样图 / DMM 图片转存、演员肖像权、来源终止后 raw_data 清理）需律师答复（需外部输入）
- [x] 观看统计（#105，迁移 043）：`user_watch_daily` 按用户本地日聚合（`watch_stats` 每小时、`watch_stats_nightly` 每晚，幂等），`GET /api/me/watch-stats?range=7d|30d|365d`，`/me/stats` 统计卡、日曲线、7×24 热力、常看演员 / 类型、连续天数；数据导出包含该表
- [x] 后台个人账号（#107，迁移 042）：`admin_users`（argon2id、AES-GCM 加密 TOTP、恢复码、锁定退避、30 分钟空闲 / 12 小时绝对会话）、7 角色权限矩阵覆盖每条后台路由（未映射路由测试失败）、双人审批、`admin_audit_log`、前台用户封禁、`cmd/admin` 引导首个管理员；`ADMIN_TOKEN` 降为可关闭的 break-glass
- [x] 服务令牌与后台补全（#108，迁移 044）：`svc_` 服务令牌（只存哈希、按路由范围、必须过期、可撤销、不能触达账号 / 审批类路由），SEO 检查改用 `SEO_REPORT_TOKEN`；`/admin/jobs` 夜间任务页（队列、重试、运行历史、13 类无来源任务立即运行）；AdminVideo 发布页「推荐位」与私有「版权凭证」；AdminSearch 无结果词「补片」「建预约页」
- [x] 弹幕与选集（#109，迁移 045）：服务端按文字判定弹幕语言并回填，语言过滤、个人屏蔽词（账号同步 / 访客本地）、密度 35 / 70 / 100%；手机全屏「选集」抽屉、桌面「同系列」面板；修复全屏面板崩溃和搜索近似匹配误判
- [x] 集成测试稳定性：#104（takedown `due_at` 微秒舍入）、#106（map 遍历顺序）、#109（`TestSearchIntegration` 隔离库）、本次（`TestDMMSourceSeedIntegration` 隔离库；ThePornDB 交叉验证测试的 `Ambiguous:2` 已由 #93/#95 修复，见该提交说明）；CI gofmt 改为失败
- [ ] 进站 18+ 自我声明 — **进行中**：定位已定为成人站，进站自我声明 18+，不做青少年模式（PRD 第 15 节、DESIGN_ALIGNMENT 第 2 节 #8、COMPLIANCE.md 由另一分支更新）
- [ ] 广告与会员 — **待产品决策**：没有任何 `ad_*` 表、广告位或会员逻辑；PRD 第 1、16、21 节对上线阶段自相矛盾（DESIGN_ALIGNMENT 第 2 节 #5、#7），成人站定位下还要选广告网络
- [ ] 依赖媒体资产的功能：演员照片集、雪碧图预览、章节截屏、预告片段、字幕轨、跳过片头，以及审计规则 B 水印 / D 字幕 / F 截屏（仍为桩）——需要自营媒体或授权素材，见 [IMPLEMENTATION_GAPS.md](IMPLEMENTATION_GAPS.md)
- [ ] AdminVideo「数据标签」tab：`GET /api/admin/releases/{id}/curves`（`internal/api/admin_curves.go`）已提供留存 / 重看 / 跳过曲线，`web/app/routes/admin-video.tsx` 没有该 tab
- [ ] AdminDashboard 播放 / 日活 / 时长 / 完播 KPI：`POST /api/events` 已入库，缺聚合与接口（`web/app/routes/admin-dashboard.tsx` 注明未做）
- [ ] 冷启动推荐：Onboarding 选的类型（`user_interests`，只在 `internal/store/account.go` 读写）和 Tonight 的「想要 / 和谁看」（`web/app/routes/tonight.tsx`，无后端字段）都没有进入 `internal/recs`
- [ ] AdminDedupe 对比面板：候选只返回 id 与打分证据，缺双方实体摘要（标题、封面、作品）；视频「合并为新片源 / 合并为其他版本」后端只有一种合并（`web/app/routes/admin-dedupe.tsx`）
- [ ] 机器翻译：`internal/i18n.Translator` 有意无实现，需选定翻译服务并提供凭据（需外部输入）

### 生产化

- [x] 结构化日志、指标和告警（`internal/obs`、`deploy/monitoring`）
- [x] 值班通知：Alertmanager（page / warning / info 路由与抑制规则；webhook、邮件、Slack、Telegram 接收器由环境变量配置，未配置时空接收器并告警日志；`amtool check-config` 与路由测试进 CI）。生产主机的实际通道待配置
- [x] 主机与备份监控：node_exporter（磁盘 < 15% warning / < 5% page、内存、负载），`backup-db.sh` 写 textfile 指标，备份超过 26 小时或 < 1 MiB 告警；Ansible 可选每日备份 cron（`backup_cron_enabled`）
- [x] robots、terms、授权和配额门禁
- [x] 数据保留和图片使用策略（[DATA_RETENTION_AND_IMAGES.md](DATA_RETENTION_AND_IMAGES.md)：只有 `official_api`/`licensed`/`internal` 且管理员复核 `images_allowed` 的来源可存图；`internal/objstore` S3 兼容存储 + compose `storage` profile MinIO；`media_fetch`/`media_purge` 任务；公开接口 `cover`/`portrait` 字段和 `/api/images/`；屏蔽/下架 24 h 内清理；DMM 图片转存待法务确认）
- [x] 部署、备份和恢复文档（`docs/DEPLOYMENT.md`：Ansible provision/deploy、完整 Caddyfile 与 TLS、密钥/vault 清单、按 tag/SHA 发布与镜像标签回滚、只向前且幂等的迁移及其回滚含义、发布后检查、每日备份/保留/异地拷贝、生产恢复、来源许可首跑清单；`make restore-drill`（`scripts/restore-drill.sh`）把最新备份或种子库恢复到一次性 postgres:17、逐表比对 dump 行数、在其上启动 API 跑 smoke，本地种子模式约 9 秒、2 MB/35 万行 dump 约 4 秒，建议每月演练）
- [ ] 正式环境验收：在生产主机配置值班通道（`ALERT_*`、`monitoring_enabled`、`backup_cron_enabled`）、为目录来源完成首次条款审核（需外部输入）
- [x] 全量验收：重复抓取不重复实体、阻断不清空数据、公开 API 不泄露 raw_data（`internal/acceptance`，随 `make test-integration` / CI integration 运行，`make acceptance` 单独运行：经生产路径 Enqueue→Claim→`crawlrun`→Finish 抓取 javdatabase/xcity/javguru/DMM/RSS 与 ThePornDB 式回退来源的 httptest 站点。重复/乱序/大小写/空白变化的再入库不新增任何行；403/429/503/验证页/404/500/robots/许可门禁/预算/解析失败/跳过/退化/空页/DMM 错误不清空数据、不丢 last_seen_at、不推进 checkpoint；覆盖、扣留、合并、拆分经多轮重抓保持；遍历全部公开 GET 路由无 raw_data、凭据、覆盖原因、隐藏生日、未发布作品。验收中发现并修复 9 个缺陷：字段历史每次重抓翻倍、退化页/改标题生成重复作品、拆分后署名重抓生成新演员、空列表页推进 checkpoint、javdatabase 空标签生成伪 content_id、javguru/xcity 标签解析、未发布作品的 /performers 返回 200、回退来源新增品番作用域、Ansible 默认 DATABASE_URL 密码不一致）

## 阶段完成标记

### 阶段 1：基础数据层 — 已完成

数据库、目录实体、来源记录、证据字段、任务租约和基础 API 已实现。

### 阶段 2：单来源目录采集 — 已完成

JAV Database adapter 已接入 scraper，可完成发现、详情抓取、解析和事务入库。

### 阶段 3：关系和冲突管理 — 已完成

演员关系、标签关系、字段冲突检测和冲突审核 API 已实现。

### 阶段 4：多来源和增量采集 — 进行中

已完成 JAV Database、XCITY、Jav.Guru adapter、scheduler、来源级 discovery interval 和 crawl policy API，并已完成 checkpoint 分页接入、来源级页面/字节预算与限速、Retry-After、blocked 来源隔离、授权/robots/terms 门禁、baseline/增量/verify 任务隔离和字段优先级策略；仍需完成更多 adapter 和每来源录制 fixture 达标。

### 阶段 5：用户 WebApp 和生产化 — 进行中

已完成目录首页、演员/作品详情、标签/厂商面板、搜索/筛选/游标分页、媒体搜索播放、Javtiful 播放映射、后台冲突/身份审核和任务/来源页面、checkpoint 页面（#88）、来源证据与系列页（#87）、来源记录自动关联（media_link）、图片存储（#91）、监控告警、部署/备份/恢复手册与全量验收（#92），以及首页运营（#98）、收藏夹（#99）、账号与 OAuth（#100）、发现与演员统计（#101）、赞踩评论（#102）、合规（#103）、观看统计（#105）、后台账号与权限（#107）、服务令牌与夜间任务页（#108）、弹幕过滤与选集（#109）；进行中：进站 18+ 自我声明；仍需外部输入完成正式环境验收，广告 / 会员待产品决策。

## 剩余事项

只列尚未完成的部分。

### 进行中

- 进站 18+ 自我声明（成人站定位已定；不做青少年模式）。

### 需要外部输入

1. **来源凭据**：DMM（`DMM_API_ID`/`DMM_AFFILIATE_ID`）、ThePornDB（`THEPORNDB_API_TOKEN`）拿到后 dry-run → 条款审核 → 启用；Google / Apple 登录（OIDC 客户端凭据）拿到后对真实 provider 实测。
2. **VPN / 授权**：XSList、JAVLibrary 确认访问条件后启用；JavDB、IAFD 条款确认前保持人工 verify。
3. **真实保存的页面**：JAV Database、XCITY、Jav.Guru 各 20 个演员、100 个作品页面作为 recorded fixture（`make fixturecheck` 达标后 CI 加 `-strict`）；厂商目录（S1、IDEA POCKET）按 `docs/MAKER_CATALOG.md` 保存真实页面、校准 profile、记录条款审核后启用。
4. **法务答复**：[COMPLIANCE.md](COMPLIANCE.md) 与 [DATA_RETENTION_AND_IMAGES.md](DATA_RETENTION_AND_IMAGES.md) 中所有 **[法务]** 条目（含 DMM 图片转存、样图、肖像权、法律文本定稿、数据保存期限）。
5. **生产环境**：配置值班通道（`ALERT_*`、`monitoring_enabled`、`backup_cron_enabled`），完成首次部署和目录来源条款审核（`PUT /api/admin/sources/{id}/license`、`CATALOG_CONTACT`），用 `SEO_REPORT_TOKEN` 跑 `npm run seo:check`，完成正式环境验收。
6. **机器翻译服务**：选定 provider 并提供凭据后实现 `internal/i18n.Translator`。

### 需要产品决策

- 广告与会员：是否上广告、在哪个阶段（PRD 第 1、16 节与第 21 节矛盾）、成人站可用的广告网络，以及 4K「会员」标签是否保留（DESIGN_ALIGNMENT 第 2 节 #5、#7）。

### 依赖媒体资产

- 演员照片集、雪碧图预览、章节截屏、预告片段、字幕轨、跳过片头标注；审计规则 B / D / F。需要自营转码或授权素材，见 [IMPLEMENTATION_GAPS.md](IMPLEMENTATION_GAPS.md)。

### 不依赖外部输入的代码工作

- AdminVideo「数据标签」tab（接口已在，缺页面）。
- AdminDashboard 播放 / 日活 / 时长 / 完播 KPI（事件已入库，缺聚合与接口）。
- 冷启动推荐使用 Onboarding 兴趣与 Tonight 的「想要 / 和谁看」。
- AdminDedupe 对比面板实体摘要，视频两种合并方式。

## 总体状态

**基础工程：已完成**  
**单来源目录采集：已完成**  
**关系与冲突审核：已完成**  
**多来源规模化采集：代码已完成，待凭据 / 授权 / 真实页面**  
**增量调度：分页 checkpoint、预算限速、阻断隔离、授权/robots 门禁和 baseline/增量隔离已完成**  
**数据质量：字段优先级策略、fixture 验收工具和审计规则 R-AGE/META/TITLE/SOURCE/PERFORMER-NAME/SEO/STATS（#97）已完成（水印、字幕、截屏缺媒体数据，保持桩），录制 fixture 未达标（各来源 0/20、0/100）**  
**WebApp 与后台：浏览、审核、运营、账号、收藏夹、互动、合规、观看统计、后台账号与权限已完成（#87–#109）；进站 18+ 进行中，广告 / 会员待决策，媒体相关功能待素材，另有 4 项后台 / 推荐代码工作**  
**生产化验收：图片存储（#91）、部署/备份/恢复手册、恢复演练和全量验收套件（#92）已完成；正式环境（首次部署、值班通道、首次许可审核）验收未完成**
