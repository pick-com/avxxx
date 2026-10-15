# AVXXX 女演员与作品数据最终设计

版本：1.1 审查修订稿  
日期：2026年10月13日  
状态：实施基线

## 1. 目标

系统从 XCITY、Jav.Guru、XSList、JAV Database、JAVLibrary 采集女演员和作品数据，保存各来源原始事实，经过规范化、去重、冲突 Review 后生成本站统一数据。

最终结果不是选择一个来源覆盖其它来源，而是：

```text
本站规范化数据
        + 多来源原始数据和统计
        + 人工审核结果
        -> 对外展示的合并实体
```

系统只采集公开 metadata 和合法预览/购买链接，不下载或分发受版权保护的视频，不绕过登录、验证码或明确的访问控制。

## 2. 数据分层

### 2.1 来源事实层

保存来源页面看到的原始数据，不能被本站编辑覆盖：

- `sources`：来源配置、适配器、启用状态和网络出口策略。
- `source_records`：来源中的艺人/作品记录、原始 URL、原始 JSON、解析器版本和抓取时间。
- `source_field_values`：字段级原始值、规范化值和可信度。
- `performer_source_metrics`、`release_source_metrics`：来源收藏、浏览、评分、评论等时间序列统计。

### 2.2 本站规范化层

本站拥有的稳定实体和关系：

- `performers`：女演员规范化实体。
- `releases`：作品规范化实体。
- `performer_aliases`：艺名、日文名、英文名和来源别名。
- `release_identifiers`：品番、DVD ID、Content ID、来源 ID。
- `release_performers`：作品和女演员关系。
- `release_tags`、`makers`、`series`：规范化分类。
- `performer_overrides`、`release_overrides`：人工修正。

### 2.3 审核层

- `identity_candidates`：疑似同一女演员或作品的候选合并。
- `field_conflicts`：单字段冲突。
- `review_events`：不可变的审核、合并、拆分和人工修改历史。

来源记录永不因合并而删除；规范化实体可以建立合并关系和重定向，但不能物理删除事实证据。

### 2.4 来源分层

来源按可信度和用途分为：

| source_kind | 示例 | 主要用途 |
|---|---|---|
| `official_catalog` | DMM/FANZA 正式商品目录或 Affiliate API | 作品身份、品番、发售日、厂商、封面和销售状态 |
| `official_maker` | SOD、IDEA POCKET、MOODYZ、S1、Madonna、Prestige | 厂商专属作品、艺人和系列补充 |
| `structured_database` | JAV Database、XCITY、JavDB、IAFD | 跨厂商作品、演员关系、历史资料和交叉验证 |
| `aggregator` | Jav.Guru、XSList | 别名、演员目录、标签和发现入口 |
| `third_party_api` | javinfo、ThePornDB API | 批量查询、缺失字段 fallback，不作为唯一事实源 |
| `manual_review` | 管理员确认或人工录入 | 冲突裁决和本站覆盖值 |

`sources` 增加 `source_kind`、`license_status`、`terms_url`、`egress_policy` 和 `priority`。没有确认条款、授权或 API 使用范围的来源只能用于人工验证，不能直接进入自动全量 cron。来源总优先级只决定默认排序；字段采用 `source_field_policies` 按实体类型和字段单独选择优先来源，避免让“作品品番可靠”的来源意外覆盖其它来源的演员简介。

## 3. 统一 slug 规则

### 3.1 女演员 slug

女演员 slug 只作为本站 URL 标识，内部关联永远使用不可变 `id`。

```text
Unicode NFKC
-> 转小写
-> 罗马字/英文规范化
-> 空格和分隔符转 '-'
-> 删除无意义标点
-> 合并连续 '-'
```

示例：

```text
Yui Hatano       -> yui-hatano
波多野結衣       -> yui-hatano（作为日文规范名和 alias 保存）
```

同名或无法确认同人时不强行合并，使用稳定消歧：

```text
yui-hatano--jp-name
yui-hatano--source-12345
```

### 3.2 作品 slug

有可信品番时统一采用品番 slug：

```text
JUQ-743 -> juq-743
juq743  -> juq-743
JUQ 743 -> juq-743
```

无品番时使用：

```text
title-<source>-<source-record-id>
```

作品 slug 不能单独承担跨来源去重。一个作品可有多个 `release_identifiers`；发现同品番但厂商、标题或日期冲突时进入 Review，必要时使用稳定冲突后缀 `juq-743--maker-2`。

## 4. 核心 schema

以下为逻辑 schema，最终 SQL 通过版本化迁移实现。

```sql
CREATE TABLE performers (
  id bigserial PRIMARY KEY,
  slug text NOT NULL UNIQUE,
  canonical_name text NOT NULL,
  canonical_name_ja text,
  birth_date date,
  birth_place text,
  debut_date date,
  debut_age smallint,
  blood_type text,
  height_cm numeric(5,2),
  bust_cm numeric(5,2),
  waist_cm numeric(5,2),
  hip_cm numeric(5,2),
  cup_size text,
  shoe_size numeric(4,1),
  hair_color text,
  hair_length text,
  activity_start date,
  activity_end date,
  biography text,
  status text NOT NULL DEFAULT 'active',
  version bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE performer_aliases (
  id bigserial PRIMARY KEY,
  performer_id bigint NOT NULL REFERENCES performers(id),
  name text NOT NULL,
  normalized_name text NOT NULL,
  language text,
  alias_type text NOT NULL DEFAULT 'stage',
  source_id bigint REFERENCES sources(id),
  UNIQUE (performer_id, normalized_name)
);

CREATE TABLE releases (
  id bigserial PRIMARY KEY,
  slug text NOT NULL UNIQUE,
  canonical_title text NOT NULL,
  title_ja text,
  release_date date,
  runtime_minutes smallint,
  maker_name text,
  label_name text,
  series_name text,
  director_name text,
  synopsis text,
  censorship_type text,
  edition_type text NOT NULL DEFAULT 'original',
  edition_of_release_id bigint REFERENCES releases(id),
  status text NOT NULL DEFAULT 'draft',
  version bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE release_identifiers (
  id bigserial PRIMARY KEY,
  release_id bigint NOT NULL REFERENCES releases(id),
  identifier_type text NOT NULL,
  identifier_value text NOT NULL,
  normalized_value text NOT NULL,
  identifier_scope text NOT NULL DEFAULT 'global',
  source_id bigint REFERENCES sources(id),
  UNIQUE (identifier_type, normalized_value, identifier_scope)
);

CREATE TABLE release_performers (
  release_id bigint NOT NULL REFERENCES releases(id),
  performer_id bigint NOT NULL REFERENCES performers(id),
  billing_order smallint,
  role text,
  credited_name text,
  is_primary boolean NOT NULL DEFAULT false,
  PRIMARY KEY (release_id, performer_id)
);

CREATE TABLE release_tags (
  release_id bigint NOT NULL REFERENCES releases(id),
  tag text NOT NULL,
  normalized_tag text NOT NULL,
  PRIMARY KEY (release_id, normalized_tag)
);

CREATE TABLE source_records (
  id bigserial PRIMARY KEY,
  source_id bigint NOT NULL REFERENCES sources(id),
  entity_type text NOT NULL CHECK(entity_type IN ('performer','release')),
  source_record_key text NOT NULL,
  source_url text NOT NULL,
  performer_id bigint REFERENCES performers(id),
  release_id bigint REFERENCES releases(id),
  raw_data jsonb NOT NULL DEFAULT '{}',
  parser_version text NOT NULL,
  first_seen_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  last_http_status smallint,
  resolution_status text NOT NULL DEFAULT 'unresolved',
  UNIQUE (source_id, entity_type, source_record_key),
  CHECK (NOT (performer_id IS NOT NULL AND release_id IS NOT NULL))
);

CREATE TABLE release_performer_sources (
  release_id bigint NOT NULL REFERENCES releases(id),
  performer_id bigint NOT NULL REFERENCES performers(id),
  source_record_id bigint NOT NULL REFERENCES source_records(id),
  billing_order smallint,
  role text,
  credited_name text,
  PRIMARY KEY (release_id, performer_id, source_record_id)
);

CREATE TABLE release_tag_sources (
  release_id bigint NOT NULL REFERENCES releases(id),
  normalized_tag text NOT NULL,
  source_record_id bigint NOT NULL REFERENCES source_records(id),
  source_tag text NOT NULL,
  PRIMARY KEY (release_id, normalized_tag, source_record_id)
);

ALTER TABLE sources ADD COLUMN source_kind text NOT NULL DEFAULT 'aggregator';
ALTER TABLE sources ADD COLUMN license_status text NOT NULL DEFAULT 'review_required';
ALTER TABLE sources ADD COLUMN terms_url text;
ALTER TABLE sources ADD COLUMN priority smallint NOT NULL DEFAULT 100;

CREATE TABLE source_field_values (
  id bigserial PRIMARY KEY,
  source_record_id bigint NOT NULL REFERENCES source_records(id),
  field_name text NOT NULL,
  raw_value jsonb,
  normalized_value jsonb,
  confidence text NOT NULL DEFAULT 'verified',
  captured_at timestamptz NOT NULL,
  is_current boolean NOT NULL DEFAULT true,
  UNIQUE(source_record_id, field_name, captured_at)
);
CREATE UNIQUE INDEX source_field_values_current
  ON source_field_values(source_record_id, field_name) WHERE is_current;

CREATE TABLE source_field_policies (
  entity_type text NOT NULL CHECK(entity_type IN ('performer','release')),
  field_name text NOT NULL,
  source_id bigint NOT NULL REFERENCES sources(id),
  priority smallint NOT NULL,
  PRIMARY KEY(entity_type, field_name, source_id)
);

CREATE TABLE performer_source_metrics (
  performer_id bigint NOT NULL REFERENCES performers(id),
  source_id bigint NOT NULL REFERENCES sources(id),
  favorites_count bigint,
  works_count int,
  rating numeric(3,2),
  rating_count int,
  views_count bigint,
  captured_at timestamptz NOT NULL,
  PRIMARY KEY (performer_id, source_id, captured_at)
);

CREATE TABLE release_source_metrics (
  release_id bigint NOT NULL REFERENCES releases(id),
  source_id bigint NOT NULL REFERENCES sources(id),
  favorites_count bigint,
  views_count bigint,
  rating numeric(3,2),
  rating_count int,
  comments_count int,
  captured_at timestamptz NOT NULL,
  PRIMARY KEY (release_id, source_id, captured_at)
);

CREATE TABLE source_crawl_policies (
  source_id bigint PRIMARY KEY REFERENCES sources(id),
  discovery_interval interval,
  detail_refresh_interval interval,
  min_request_interval interval NOT NULL DEFAULT interval '5 seconds',
  daily_page_budget integer NOT NULL DEFAULT 0,
  daily_byte_budget bigint NOT NULL DEFAULT 0,
  enabled_at timestamptz,
  baseline_completed_at timestamptz
);

CREATE TABLE crawl_checkpoints (
  source_id bigint NOT NULL REFERENCES sources(id),
  task_type text NOT NULL,
  partition_key text NOT NULL DEFAULT '',
  cursor text,
  watermark timestamptz,
  completed_pages bigint NOT NULL DEFAULT 0,
  last_success_at timestamptz,
  last_error_code text,
  parser_version text NOT NULL,
  PRIMARY KEY (source_id, task_type, partition_key)
);
```

`source_records` 允许先以 `unresolved` 状态入库，随后链接到 `performers` 或 `releases`；这使目录发现、详情解析和实体合并可独立重试。`release_performer_sources` 与 `release_tag_sources` 保留来源级关系证据，规范化关系表只保存合并后的当前关系。

媒体链接另建 `release_media`，人工修正另建 override 表，避免将来源统计和本站字段混在核心实体中。

## 5. 去重和合并

### 5.1 来源内去重

来源事实使用：

```text
UNIQUE(source_id, entity_type, source_record_key)
```

作品 `source_record_key` 的优先级：

1. 来源稳定作品 ID。
2. 规范化品番、DVD ID 或 Content ID。
3. 规范化作品 URL。
4. `sha256(maker + title + release_date + runtime)` 指纹。

标题不能单独作为唯一键。

### 5.2 跨来源作品合并

按以下顺序匹配：

1. 相同规范化 Content ID 或品番。
2. 厂商 + 品番 + 发售日期一致。
3. 厂商、标题、日期、时长和演员组合相似。
4. 只有标题相同只能进入候选，不能自动合并。

合集、重发、精选、无码版和 VR 版默认是不同 `releases`，通过 `edition_of_release_id` 关联。

### 5.3 跨来源艺人合并

自动合并必须至少包含一个强证据和一个辅助证据：来源稳定 ID 映射、日文名/别名、生日、身高、三围、作品交集。仅凭英文名、头像或年龄不能自动合并。

### 5.4 本站合并字段

| 字段 | 合并策略 |
|---|---|
| 姓名、生日、身高、三围 | 选择可信来源值，冲突进入 Review |
| 别名 | 多来源并集 |
| 作品列表 | 去重后并集 |
| 演员关系 | 多来源并集，冲突 Review |
| 标签 | 来源标签保留，规范化标签并集 |
| 收藏、浏览、评分 | 按来源分别保存 |
| 作品数量 | 从本站去重后的关系重新计算 |
| 本站热度 | 归一化后按来源权重计算，不直接相加 |

来源统计例如 XCITY 收藏数 761、JAV Database 收藏数 14，不合并为 775；它们进入独立来源指标，本站只计算 `site_popularity_score`。

## 6. Review 机制

数据不一致不覆盖旧值，进入 `field_conflicts`；实体疑似同人进入 `identity_candidates`。Review 页面必须展示当前值、所有候选值、来源 URL、抓取时间、parser version 和证据。

自动处理仅允许格式差异、单位差异、别名转换和空字段保留旧值。出生日期、品番、Content ID、厂商、标题和实体身份冲突必须人工确认。

管理员操作：

- 采用来源值。
- 手工修正并写入 override。
- 保留当前值。
- 标记为不冲突。
- 拒绝某个来源候选。
- 合并或拆分实体。

所有操作写入不可变 `review_events`，使用 `version` 乐观锁；旧页面提交时返回 409。人工 override 不被爬虫覆盖，清除 override 必须显式操作。

来源 403、503、验证码、登录墙或解析失败不得生成“空字段更新”，只更新来源任务状态和阻断原因。

## 7. Cron 增量

首次运行：

```text
艺人目录 -> 艺人详情 -> 作品发现 -> 作品详情 -> 关系回填 -> 冲突生成
```

后续拆为：`performer_discovery`、`performer_detail`、`release_discovery`、`release_detail`、`relation_reconcile`、`conflict_review` 和 `source_probe`。每个来源和任务类型保存 cursor/watermark checkpoint，只有本批成功写入后才推进。

推荐 cron：

```cron
17 */6 * * * avxxx scheduler discover --mode=incremental
*/15 * * * * avxxx scheduler dispatch --queues=detail,retry
35 3 * * * avxxx scheduler reconcile --since=24h
50 4 * * * avxxx scheduler probe --sources=enabled
*/30 * * * * avxxx scheduler probe --sources=blocked
20 5 * * 0 avxxx scheduler audit --sample=1%
```

cron 只创建任务，Worker 执行实际抓取；使用 scheduler lease 防止重叠。无游标来源使用最近 3 页或 7 天重叠窗口。目录中暂时消失的记录连续 3 次完整成功采集未出现后才标记 stale，绝不直接删除规范化实体。

## 8. 来源出口

来源配置 `egress_policy`：`direct`、`vpn` 或 `manual`。XSList 和 JAVLibrary 可先使用独立 VPN 出口探测；VPN 凭据通过 Secret 注入，不写入数据库。VPN 只改变出口，不绕过验证码、登录或明确禁止自动化。

出口启用前执行首页、固定目录页和限速请求探测。403、503 或挑战页面进入 blocked；恢复时先做 fixture、小批量 diff 和人工 Review，再恢复增量。

## 9. API 结果

公开 API 返回本站合并实体，同时提供来源证据和来源统计：

```json
{
  "slug": "juq-743",
  "title": "本站规范化标题",
  "identifiers": [
    {"type": "dvd_code", "value": "JUQ-743"},
    {"type": "content_id", "value": "juq00743"}
  ],
  "performers": [],
  "site_metrics": {"popularity_score": 0.82},
  "sources": [
    {"name": "XCITY", "favorites_count": 761},
    {"name": "JAV Database", "favorites_count": 14}
  ]
}
```

优先公开接口：

```text
GET /api/performers
GET /api/performers/{slug}
GET /api/performers/{slug}/releases
GET /api/releases
GET /api/releases/{slug}
GET /api/releases/{slug}/performers
GET /api/search
GET /api/tags
GET /api/makers
GET /api/series
```

列表接口使用游标分页，默认 20、最大 100；公开接口只返回已发布且未软删除的数据，不返回 `raw_data`、代理配置、管理员备注和内部错误。

## 9.1 推荐新增来源

优先新增官方和结构化来源：

- DMM/FANZA 正式商品目录或 Affiliate API：作品主数据、品番、发售日、厂商、系列、封面和销售状态。
- SOD、IDEA POCKET、MOODYZ、S1、Madonna、Prestige 等厂商目录：厂商专属演员、系列和新品信息。IDEA POCKET 的官方演员目录按假名列出日文名、罗马字名和新品状态，可作为厂商 adapter 的参考结构。
- JavDB：作品交叉验证和标签补充。
- ThePornDB：演员、作品/场景、厂商、导演和标签的 API 补充，但需要 token 和独立授权评估。
- javinfo：统一查询 DMM/FANZA、JAV Database、JAVLibrary 等来源，适合批量查询和 fallback，不作为本站唯一事实源。
- IAFD：演员职业履历和历史作品交叉验证，需单独确认商业授权。

官方来源决定作品身份的优先级；聚合站用于发现、补充和校验；第三方 API 用于减少 HTML 适配器维护。所有来源仍需记录条款、授权、配额和抓取方式。

## 10. 实施顺序

1. 迁移基线：执行 001/002，建立 `source_kind`、授权状态、来源事实、规范化实体和 Review 表；验证重复启动幂等。
2. 数据访问层：为 unresolved source record、identifier scope、字段证据历史和来源级关系提供 store 方法与事务测试。
3. 合规门禁：每个来源记录 terms、授权、robots、请求预算、出口策略和数据保留规则；门禁未通过只能手动 fixture。
4. DMM/FANZA 正式目录接入；先确认 Affiliate/API 许可、配额和图片使用规则。
5. SOD、IDEA POCKET、MOODYZ、S1、Madonna、Prestige 等官方厂商 adapter，按厂商独立限速。
6. XCITY、JAV Database adapter，作为跨厂商结构化补充；完成 20 个艺人、100 个作品 fixture 验收。
7. Jav.Guru、XSList adapter；XSList 先做 VPN 探测和小批量 diff，失败时保持 blocked。
8. JavDB、IAFD、ThePornDB 和 javinfo 逐一评估授权；默认作为交叉验证或 fallback，不作为唯一事实源。
9. 去重、候选合并、字段冲突、人工 override 和 Review 队列；先人工审核再开放自动合并。
10. 增量 checkpoint、scheduler lease、来源级 cron、页面/字节预算和租约恢复。
11. JAVLibrary VPN 探测；只有访问条款和自动化许可明确后才启用 adapter。
12. 公开 API、前端列表/详情和本站热度计算；公开查询只读合并后的 published 实体。

每个阶段必须满足退出条件后才能进入下一阶段：迁移可重复执行；fixture 解析结果可回归；失败不会推进 checkpoint；重复运行不重复实体；冲突可 Review 和撤销；来源阻断不影响其它队列；公开 API 不泄露 raw_data 或内部错误。

## 11. 验收标准

- 同一来源重复采集不会产生重复 source record。
- 相同品番跨来源能关联到同一作品候选；冲突品番不会静默覆盖。
- 同名艺人不会因为英文显示名相同而自动错误合并。
- 本站人工字段不会被后续采集覆盖。
- 来源收藏和评分分别保存，本站热度不简单相加。
- 403、503、验证码和空响应不会清空已有字段。
- checkpoint 只在完整批次成功后推进，任务可从中断位置继续。
- 合并、拆分、人工修正和冲突处理都有审计记录。
- 公开 API 返回本站合并结果，并能追溯来源记录。
- 官方目录、厂商目录、结构化数据库和第三方 API 在 UI 与数据处理中显示不同 source_kind。
- 未确认授权或条款的来源不会被调度为自动全量任务。
- 第三方 API 不可用时，核心官方/结构化来源流程仍可独立运行。
