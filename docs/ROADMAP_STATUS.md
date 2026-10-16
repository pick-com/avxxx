# AVXXX Roadmap 状态

> 状态更新时间：2026-10-16
>
> 当前基础能力完成度约为 **96%**。基础架构、目录采集、调度、WebApp 浏览、Javtiful 播放映射、审核后台、生产 smoke/备份和 Ansible 部署已经可运行；来源授权/robots/terms 门禁、baseline/增量/verify 任务隔离、字段优先级策略和 fixture 验收工具已完成。剩余工作主要集中在来源覆盖（更多 adapter 和录制 fixture）、图片存储、生产值班通道配置和正式生产验收（监控、Alertmanager 通知、主机与备份告警已就绪）。
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
- [ ] 厂商目录 adapter
- [ ] JavDB、IAFD、ThePornDB、javinfo 交叉验证 adapter
  - [ ] ThePornDB — **已实现，待凭据实测**：`theporndb` adapter 按官方 OpenAPI（`https://api.theporndb.net/specs?openapi.json`，文档 `https://api.theporndb.net/docs`）映射 `GET /jav`、`/jav/{id}`、`/performers`、`/performers/{id}`，`THEPORNDB_API_TOKEN` 只以 Bearer 头发往 API 主机并全程脱敏。主模式是 `crosscheck` 任务（cmd/scheduler 每 `CROSSCHECK_EVERY` 入队、cmd/audit 执行）：对缺发售日/厂商/演员/封面证据或有待审字段冲突的作品按品番查询，唯一精确品番匹配才保存为证据，尝试记录在 `crosscheck_attempts`（未找到 14 天、3 次后 60 天重试；错误指数退避）；次要模式是 `GET /jav`（按发售日倒序）的 baseline/增量/verify 抓取。`third_party_api` 层级（优先级 50）只附着到已有作品/演员、只补缺字段、不单独开字段冲突、不新建演员，作品已有他源署名时不加署名；ThePornDB 演员 UUID 写入 `performer_external_ids(namespace='theporndb_performer')`。来源（migration 032）以 `official_api` 默认停用、`terms_reviewed_at` 为空，API 文档未给出条款/署名要求，审核时需确认。配置 token 与 `CATALOG_CONTACT` 后先跑 `go run ./cmd/scraper -dry-run -adapter theporndb` 核对 JAV 品番所在字段（`sku`/`external_id`）、`duration` 单位和 `meta.last` 含义，再记录条款审核并启用
  - [ ] JavDB、IAFD：没有官方 API，条款未确认前保持在许可门禁后，只能人工 verify，不实现自动抓取
  - [ ] javinfo：待评估

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
- [x] 作品、演员和字段完整性报告（`GET /api/admin/reports/completeness?entity=release|performer&source=&group_by=field|source|maker&format=csv`，`internal/completeness`；夜间 `completeness_snapshot` 任务写 `completeness_snapshots` 作趋势）

### Web 和 API

- [x] 演员列表、详情和作品页（列表在 discover 页面）
- [x] 作品列表、详情和演员页
- [x] 搜索、筛选、排序和游标分页
- [x] 系列查询 API（`series` 实体与 `releases.series_id`，迁移 027 回填和触发器；`GET /api/series`、`/api/series/{slug}`、`/api/series/{slug}/releases?sort=episode|newest|oldest`；作品详情带 `series`）
- [x] 来源证据和更新时间展示（`GET /api/releases/{slug}/evidence`、`GET /api/performers/{slug}/evidence`，不含 raw_data、覆盖原因和隐藏生日；作品页和演员页的「信息来源」折叠区，按字段显示来源、层级、更新时间和「人工校正」）
- [x] 系列页面（`/series`、`/series/{slug}`：服务端渲染、按集数/最新/最早排序、JSON-LD、`/sitemaps/series-{lang}-{n}.xml`）
- [x] 来源署名（`sources.attribution_html` 经 `GET /api/attributions?release=|performer=|series=` 下发，原样渲染在作品、演员、系列页末尾；DMM 数据出现时显示「Powered by FANZA Webサービス」）
- [x] 管理后台冲突审核页面
- [x] 管理后台任务和来源页面（`admin-pipeline`）
- [ ] 管理后台 checkpoint 页面（`GET /api/admin/sources/{id}/checkpoints` 已有，前端未接入）

### 生产化

- [x] 结构化日志、指标和告警（`internal/obs`、`deploy/monitoring`）
- [x] 值班通知：Alertmanager（page / warning / info 路由与抑制规则；webhook、邮件、Slack、Telegram 接收器由环境变量配置，未配置时空接收器并告警日志；`amtool check-config` 与路由测试进 CI）。生产主机的实际通道待配置
- [x] 主机与备份监控：node_exporter（磁盘 < 15% warning / < 5% page、内存、负载），`backup-db.sh` 写 textfile 指标，备份超过 26 小时或 < 1 MiB 告警；Ansible 可选每日备份 cron（`backup_cron_enabled`）
- [x] robots、terms、授权和配额门禁
- [x] 数据保留和图片使用策略（[DATA_RETENTION_AND_IMAGES.md](DATA_RETENTION_AND_IMAGES.md)：只有 `official_api`/`licensed`/`internal` 且管理员复核 `images_allowed` 的来源可存图；`internal/objstore` S3 兼容存储 + compose `storage` profile MinIO；`media_fetch`/`media_purge` 任务；公开接口 `cover`/`portrait` 字段和 `/api/images/`；屏蔽/下架 24 h 内清理；DMM 图片转存待法务确认）
- [x] 部署、备份和恢复文档（`docs/DEPLOYMENT.md`：Ansible provision/deploy、完整 Caddyfile 与 TLS、密钥/vault 清单、按 tag/SHA 发布与镜像标签回滚、只向前且幂等的迁移及其回滚含义、发布后检查、每日备份/保留/异地拷贝、生产恢复、来源许可首跑清单；`make restore-drill`（`scripts/restore-drill.sh`）把最新备份或种子库恢复到一次性 postgres:17、逐表比对 dump 行数、在其上启动 API 跑 smoke，本地种子模式约 9 秒、2 MB/35 万行 dump 约 4 秒，建议每月演练）
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

已完成目录首页、演员/作品详情、标签/厂商面板、搜索/筛选/游标分页、媒体搜索播放、Javtiful 播放映射、后台冲突/身份审核和任务/来源页面、Docker Compose 和 Ansible 部署；仍需完成 checkpoint 和来源证据展示、图片存储、来源记录自动关联、监控和最终验收。

## 下一步执行顺序

1. 管理员为现有目录来源完成条款审核（`PUT /api/admin/sources/{id}/license`）并配置 `CATALOG_CONTACT`，否则目录来源只能运行 verify 任务。
2. 为 JAV Database、XCITY、Jav.Guru 各保存 20 个演员、100 个作品页面作为 recorded fixture；`make fixturecheck` 达标后在 CI 中加 `-strict`。
3. 用真实凭据实测 DMM adapter（dry-run → 条款审核 → 启用），再实现厂商目录 adapter（官方来源字段优先级最高）。
4. 前端接入 checkpoint、来源证据和更新时间展示；补系列查询 API。
5. 目录 source record 与 Javtiful media record 自动关联。
6. 在生产主机配置值班通道（`ALERT_*`、`monitoring_enabled`、`backup_cron_enabled`）并完成生产验收。

## 总体状态

**基础工程：已完成**  
**单来源目录采集：已完成**  
**关系与冲突审核：已完成**  
**多来源规模化采集：进行中**  
**增量调度：分页 checkpoint、预算限速、阻断隔离、授权/robots 门禁和 baseline/增量隔离已完成**  
**数据质量：字段优先级策略和 fixture 验收工具已完成，录制 fixture 未达标（各来源 0/20、0/100）**  
**WebApp 完整交付：主要浏览和审核流程已完成，checkpoint 与来源证据展示未完成**  
**生产化验收：部署/备份/恢复手册、恢复演练和全量验收套件已完成；正式环境（生产主机值班通道、首次许可审核）验收未完成**
