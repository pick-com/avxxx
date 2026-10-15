# AVXXX Roadmap 状态

> 状态更新时间：2026-10-14
>
> 当前基础能力完成度约为 **60%**。基础架构已经可运行，剩余工作主要集中在多来源规模化采集、增量调度、管理界面和生产化验收。

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
- [x] 管理员 Bearer Token 鉴权
- [x] Go 单元测试、`go vet` 和 `git diff --check`

## 进行中

- [ ] 完善来源级字段优先级和规范字段更新策略
- [ ] 增加 unresolved identity candidate 生成和候选匹配
- [ ] 增加合并、拆分和撤销合并操作
- [ ] 增加 checkpoint 和分页断点模型的实际执行逻辑

## 待完成

### 来源适配器

- [ ] XCITY
- [ ] Jav.Guru
- [ ] XSList（VPN/授权条件确认后启用）
- [ ] JAVLibrary（VPN/授权条件确认后启用）
- [ ] DMM/FANZA 官方目录或 Affiliate API
- [ ] 厂商目录 adapter
- [ ] JavDB、IAFD、ThePornDB、javinfo 交叉验证 adapter

### 增量调度

- [ ] cron 只创建任务，不直接执行抓取
- [ ] baseline 与日常增量任务隔离
- [ ] 分页 cursor、水位和 checkpoint 推进
- [ ] 来源级请求次数、字节预算和限速
- [ ] 失败批次不推进 checkpoint
- [ ] blocked 来源隔离，不影响其它来源队列

### 数据质量和审核

- [ ] 每个来源 20 个演员、100 个作品 fixture 验收
- [ ] PostgreSQL 集成测试
- [ ] identity candidate 审核队列
- [ ] 合并、拆分、人工覆盖和撤销审计
- [ ] 来源可信度和字段优先级策略
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

### 阶段 4：多来源和增量采集 — 未完成

需要完成更多 adapter、checkpoint、cron、限速和来源级失败隔离。

### 阶段 5：用户 WebApp 和生产化 — 未完成

需要完成前端浏览页面、后台审核页面、监控、部署和最终验收。

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
**多来源规模化采集：未完成**  
**增量调度：未完成**  
**WebApp 完整交付：未完成**  
**生产化验收：未完成**

