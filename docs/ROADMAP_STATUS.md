# AVXXX Roadmap 状态

> 状态更新时间：2026-10-15
>
> 当前基础能力完成度约为 **92%**。基础架构、目录采集、调度、WebApp 浏览、Javtiful 播放映射、审核后台、生产 smoke/备份和 Ansible 部署已经可运行，剩余工作主要集中在来源覆盖、分页增量执行、图片存储和正式生产验收。

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

## 进行中

- [ ] 完善来源级字段优先级和规范字段更新策略
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
- [ ] DMM/FANZA 官方目录或 Affiliate API（`dmm` adapter 已实现：ItemList digital/videoa + ActressSearch，来源默认停用；待配置 `DMM_API_ID`/`DMM_AFFILIATE_ID` 后先跑 `go run ./cmd/scraper -dry-run -adapter dmm` 用真实响应核对字段，再启用）
- [ ] 厂商目录 adapter
- [ ] JavDB、IAFD、ThePornDB、javinfo 交叉验证 adapter

### 增量调度

- [x] cron 只创建任务，不直接执行抓取
- [ ] baseline 与日常增量任务隔离
- [x] 分页 cursor、水位和 checkpoint 推进
- [x] 来源级请求次数、字节预算和限速
- [x] 失败批次不推进 checkpoint
- [x] blocked 来源隔离，不影响其它来源队列

### 数据质量和审核

- [ ] 每个来源 20 个演员、100 个作品 fixture 验收
- [ ] PostgreSQL 集成测试
- [ ] identity candidate 审核队列
- [ ] 合并、拆分、人工覆盖和撤销审计
- [ ] 来源可信度和字段优先级策略
- [ ] 目录 source record 与 Javtiful media record 自动关联
- [ ] 作品、演员和字段完整性报告

### Web 和 API

- [ ] 演员列表、详情和作品页
- [ ] 作品列表、详情和演员页
- [ ] 搜索、筛选、排序和游标分页
- [ ] 系列查询 API
- [ ] 来源证据和更新时间展示
- [ ] 管理后台冲突审核页面
- [ ] 管理后台任务、来源和 checkpoint 页面

### 生产化

- [ ] 结构化日志、指标和告警
- [ ] robots、terms、授权和配额门禁
- [ ] 数据保留和图片使用策略
- [ ] 部署、备份和恢复文档
- [ ] 全量验收：重复抓取不重复实体、阻断不清空数据、公开 API 不泄露 raw_data

## 阶段完成标记

### 阶段 1：基础数据层 — 已完成

数据库、目录实体、来源记录、证据字段、任务租约和基础 API 已实现。

### 阶段 2：单来源目录采集 — 已完成

JAV Database adapter 已接入 scraper，可完成发现、详情抓取、解析和事务入库。

### 阶段 3：关系和冲突管理 — 已完成

演员关系、标签关系、字段冲突检测和冲突审核 API 已实现。

### 阶段 4：多来源和增量采集 — 进行中

已完成 JAV Database、XCITY、Jav.Guru adapter、scheduler、来源级 discovery interval 和 crawl policy API，并已完成 checkpoint 分页接入、来源级页面/字节预算与限速、Retry-After 和 blocked 来源隔离；仍需完成更多 adapter 和 baseline/增量任务隔离。

### 阶段 5：用户 WebApp 和生产化 — 进行中

已完成目录首页、演员/作品详情、标签/厂商面板、媒体搜索播放、Javtiful 播放映射、Docker Compose 和 Ansible 部署；仍需完成后台审核页面、图片存储、来源记录自动关联、监控和最终验收。

## 下一步执行顺序

1. 实现 XCITY adapter。
2. 实现 Jav.Guru adapter。
3. 增加 identity candidate 生成和审核。
4. 实现 checkpoint、分页增量和 cron 调度。
5. 增加 PostgreSQL 集成测试和来源 fixture 验收。
6. 完成 WebApp 页面和后台审核页面。
7. 完成监控、部署和生产验收。

## 总体状态

**基础工程：已完成**  
**单来源目录采集：已完成**  
**关系与冲突审核：已完成**  
**多来源规模化采集：进行中**  
**增量调度：分页 checkpoint、预算限速和阻断隔离已完成，baseline/增量任务隔离未完成**  
**WebApp 完整交付：主要浏览和审核流程已完成**  
**生产化验收：基础检查、备份和恢复已完成，正式环境验收未完成**
