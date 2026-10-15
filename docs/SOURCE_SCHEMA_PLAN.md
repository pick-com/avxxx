# JAV 来源采集、规范化与去重方案

版本：0.1 调研稿  
日期：2026年10月13日  
适用项目：avxxx

## 1. 目标与边界

从 XCITY、Jav.Guru、XSList、JAV Database、JAVLibrary 获取两类信息：

1. 女演员（下文统一称 `performer`）目录和详情。
2. 作品（`release`）目录、详情以及演员关联。

系统必须同时保存来源原文和规范化实体。来源页面随时可能改名、下线、重复或错误关联，因此来源记录不能直接覆盖规范化数据。首期只采集公开页面、元数据和指向合法来源的链接；不抓取或存储受版权保护的视频内容，也不绕过登录、验证码、Cloudflare 或站点访问限制。

当前调研确认的字段如下。

| 来源 | 艺人目录与详情 | 作品信息 | 可见字段 |
|---|---|---|---|
| [XCITY](https://xxx.xcity.jp/idol/) | 假名检索、艺人详情页 | 详情页按发售日或标题分页，含 Gravure、Behind the scenes、Sample Movie 分类 | 姓名、出生日期、出生地、身高、三围、血型、爱好、特长、简介、头像/媒体链接、收藏数、作品标题、厂商、系列、出演者、发售日 |
| [Jav.Guru](https://jav.guru/jav-actress-list/) | 分页艺人列表，按出现次数排序，艺人页有 Alias | 艺人页作品分页 | 艺人英文/日文名、别名、年龄、身高、罩杯、作品数、作品代码、标题、标签、发布日期、浏览/点赞/评论数、分类 |
| [XSList](https://xslist.org/) | `/en/model/` 档案和排名页（当前直连返回 503，历史抓取受 Cloudflare 影响；可使用配置的 VPN 出口重新探测） | 档案中的 filmography/作品链接，需逐页验证 | 姓名、别名、出生日期、出生地、三围、罩杯、身高、AV 活动起止/月份、头像、作品履历 |
| [JAV Database](https://www.javdatabase.com/idols/) | 分页艺人目录和 `/idols/{slug}/` 详情 | `/movies/{code}/` 作品详情 | 艺人年龄、生日、出道日/出道年龄、出生地、星座、血型、三围、罩杯、身高、鞋码、发长/发色、标签、日文名、官方/合法链接、作品状态；作品标题、DVD ID、Content ID、发售日、时长、厂商、导演、类型、演员、评分 |
| [JAVLibrary](https://www.javlibrary.com/en/star_list.php) | 星名列表（当前直连请求 403；可使用配置的 VPN 出口重新探测，仍需确认是否允许自动访问） | 作品页通常提供品番、标题、发布日期、时长、厂商/系列、演员、类型、封面 | 本来源字段必须在 VPN 出口访问成功且允许自动化后用固定样例核实，不能把常见字段当作已验证事实 |

XCITY 首页明确说明其艺人数据库自 1996 年收录标题、预览和写真；艺人页示例显示 DOB、出生地、身高、三围、简介、发售排序和厂商等字段。[XCITY 首页](https://xxx.xcity.jp/idol/) [XCITY 艺人详情](https://xxx.xcity.jp/idol/detail/8152/) 

Jav.Guru 的列表显示艺人名、日文名、年龄、作品数，并且分页超过 480 页；艺人页补充 alias、身高、罩杯、分类筛选、作品品番、标签、统计数和发布日期。[Jav.Guru 列表](https://jav.guru/jav-actress-list/) [Jav.Guru 艺人页](https://jav.guru/actress/hatano-yui/)

JAV Database 的艺人页显示生日、出道信息、三围、罩杯、身高、发型、标签、日文名及按 censored/uncensored 分组的作品；作品页显示 DVD ID、Content ID、发售日、时长、厂商、导演、类型和出演者。[JAV Database 艺人页](https://www.javdatabase.com/idols/sena-hashimoto/) [JAV Database 作品页](https://www.javdatabase.com/movies/juq-743/)

XSList 的可检索页面摘要显示出生日期、三围、罩杯、身高和 AV 活动时间；其直连抓取目前不稳定，VPN 只改变出口地址，不能自动解决验证码、浏览器挑战或站点条款限制。相关字段需要通过 VPN 出口、小批量 fixture 和允许的访问方式再确认。[XSList ranking](https://xslist.org/en/ranking) [XSList 抓取现状记录](https://github.com/stashapp/CommunityScrapers/issues/1294)

## 2. 关键设计：两个 slug 层级

### 2.1 规范化艺人 slug

`performers.slug` 是系统内部规范化键，不把任一来源 URL slug 当成全局身份。推荐算法：

```text
slug = lower(ascii_transliterate(canonical_display_name))
       -> Unicode NFKC
       -> trim
       -> whitespace/separator 转 '-'
       -> 删除标点和不可识别符号
       -> 合并连续 '-'
```

日文名不能可靠地只依赖罗马字转写，必须同时保存日文规范名和别名。最终唯一键为内部 UUID；`slug` 建立唯一约束用于 URL。发生同名冲突时不添加随机后缀来掩盖身份问题，而是使用稳定 disambiguator：

```text
<base-slug>--<jp-normalized-name>
<base-slug>--<source-id>-<source-record-id>  # 仅在无法判断同一人时
```

同名合并只在满足至少两个强证据时自动执行：相同来源稳定 ID、相同日文名/别名组合、相同出生日期、相同三围与身高、作品交集达到阈值。仅凭英文显示名、头像相似或年龄相同不得自动合并。疑似同人进入 `identity_candidates`，由管理员确认；确认后建立 `performer_aliases`，原始来源记录不改写。

建议艺人 URL 使用 `/performers/{slug}`；slug 变更时保留 `performer_slug_history` 并返回 301，规范化实体 ID 永不变化。

### 2.2 规范化作品 slug

作品优先使用厂商品番/产品代码作为稳定业务键，保留大小写规范化后的 `release_code`：

```text
release_code = upper(NFKC(trim(code)))
去除仅作为排版的空白；保留连字符，另存 compact_code=删除连字符后的值
```

`releases.slug` 统一采用品番风格：

```text
<release-code-lower>                 # 有可信品番时，例如 juq-743
title-<source>-<source-record-id>   # 无品番时的稳定回退键
```

因此 `JUQ-743`、`juq743`、`JUQ 743` 在规范化后都使用 `juq-743` 作为 URL slug。slug 只保留小写字母、数字和连字符，移除首尾连字符并合并连续连字符。

唯一约束不能只放在 `slug`：不同厂商可能复用代码，无码作品可能同名。因此规范化实体默认要求一个 slug 对应一个已确认的作品；发现相同品番但厂商、标题或日期冲突时，不强行共用 slug，而是建立冲突候选并使用稳定冲突后缀，例如 `juq-743--maker-2`，等待 review。使用以下优先级去重：

1. `(maker_normalized, release_code_compact)`。
2. 来源提供的稳定作品 ID。
3. 规范化 `(maker, title, release_date, runtime)` 的指纹。
4. 仅标题相同只能进入候选合并，不能自动合并。

一个规范化作品可有多个 `release_identifiers`（DVD ID、Content ID、品番、来源 ID），一个来源记录只指向一个规范化作品。合集、重发、廉价版和无码版默认是不同 release，通过 `edition_of_release_id` 关联；不能因标题相似合并。

## 3. 推荐 PostgreSQL schema

使用 `jsonb` 保存未标准化来源字段，标准字段用于检索和去重。所有来源快照保留抓取时间、HTTP 状态、原始 URL 和解析器版本。

```sql
CREATE TABLE performers (
  id bigserial PRIMARY KEY,
  slug text NOT NULL UNIQUE,
  canonical_name text NOT NULL,
  canonical_name_ja text,
  sex text NOT NULL DEFAULT 'female',
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
CREATE INDEX performer_alias_search ON performer_aliases(normalized_name);

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
  status text NOT NULL DEFAULT 'published',
  edition_of_release_id bigint REFERENCES releases(id),
  content_hash text,
  version bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE release_identifiers (
  id bigserial PRIMARY KEY,
  release_id bigint NOT NULL REFERENCES releases(id),
  identifier_type text NOT NULL, -- dvd_code, content_id, source_id, product_id
  identifier_value text NOT NULL,
  normalized_value text NOT NULL,
  source_id bigint REFERENCES sources(id),
  UNIQUE (identifier_type, normalized_value, source_id)
);
CREATE INDEX release_code_lookup ON release_identifiers(identifier_type, normalized_value);

CREATE TABLE release_performers (
  release_id bigint NOT NULL REFERENCES releases(id),
  performer_id bigint NOT NULL REFERENCES performers(id),
  billing_order smallint,
  role text,
  source_id bigint REFERENCES sources(id),
  PRIMARY KEY (release_id, performer_id)
);

CREATE TABLE makers (
  id bigserial PRIMARY KEY,
  slug text NOT NULL UNIQUE,
  name text NOT NULL,
  name_ja text,
  kind text NOT NULL DEFAULT 'maker'
);
CREATE TABLE release_tags (
  release_id bigint NOT NULL REFERENCES releases(id),
  tag text NOT NULL,
  normalized_tag text NOT NULL,
  source_id bigint REFERENCES sources(id),
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
  UNIQUE (source_id, entity_type, source_record_key),
  CHECK ((entity_type='performer' AND performer_id IS NOT NULL AND release_id IS NULL)
      OR (entity_type='release' AND release_id IS NOT NULL AND performer_id IS NULL))
);
CREATE INDEX source_records_entity ON source_records(entity_type, last_seen_at DESC);

CREATE TABLE identity_candidates (
  id bigserial PRIMARY KEY,
  entity_type text NOT NULL CHECK(entity_type IN ('performer','release')),
  left_id bigint NOT NULL,
  right_id bigint NOT NULL,
  score numeric(5,4) NOT NULL,
  evidence jsonb NOT NULL,
  status text NOT NULL DEFAULT 'pending',
  UNIQUE(entity_type, left_id, right_id)
);
```

`sources` 和 `jobs` 复用 avxxx 现有表；`source_records` 是抓取层的事实表，`performers`/`releases` 是规范化层。不要用 `raw_data` 直接作为公开 API，公开 DTO 只输出经过校验的字段。

## 4. 抓取器设计

每个来源实现一个 adapter，分为目录发现、详情抓取、作品详情抓取三类任务：

```text
discover_performers(source) -> source performer URLs/keys
fetch_performer(source key) -> performer fields + release references
discover_releases(performer/source page) -> release URLs/keys
fetch_release(source key) -> release fields + performer references
```

### XCITY

- 从 `/idol/` 的假名入口遍历艺人目录；分页/字母链接作为发现入口。
- 详情页解析 profile、头像、作品列表分页和作品关联；新面孔页可作为增量入口。
- 作品详情先使用品番、厂商、标题、发售日和出演者；免费预览、写真和视频链接按媒体类型单独保存，不混入作品核心字段。
- 详情页显示的收藏数和简介是来源展示字段，不作为身份合并强证据。

### Jav.Guru

- 遍历 `/jav-actress-list/` 分页（列表当前显示按作品出现次数排序，分页数量较大）。
- 艺人页保存英文名、日文名、alias、身高、罩杯和作品分类；作品列表按分页抓取。
- 作品以页面品番（如 `HTTM-0070`）为首选标识，标题、tag、发布日期和统计数进入来源扩展字段；重复品番只更新 source record。

### XSList

- 先通过配置的 VPN/HTTP(S) 出口做健康探测，再实现浏览器会话或人工授权探测；遇到 503/403、验证码、登录墙或明确禁止自动化时任务进入 `blocked`，不能无限重试。
- 使用 `/en/model/` 作为艺人详情入口；目录和搜索结果只负责发现 URL。
- 详情解析名字、别名、出生日期、三围、罩杯、身高、活动时间和 filmography。由于当前根页不可抓取，正式 selector 必须基于保存的脱敏 fixture 验证后启用。

### JAV Database

- 遍历 `/idols/` 分页和每个 `/idols/{slug}/` 详情；记录 stable source slug。
- 从艺人页的 censored/uncensored 分组作品入口发现作品，再抓 `/movies/{code}/` 详情。
- 使用 DVD ID、Content ID、发布日期、runtime、studio、director、genres 和 actresses；同一作品的合法购买/预览链接进入 `source_links`，不当作视频资源。

### JAVLibrary

- 当前直连 HTTP 返回 403。可以配置 VPN 出口重新探测，但 VPN 不等于绕过访问控制；如果出口仍要求验证码、登录或禁止自动化，则不能实现自动抓取。
- 在获得合规访问方式后，先保存列表和单个作品 fixture，确认 star URL、作品 URL、品番、标题、日期、厂商、系列、演员和类型选择器，再加入 adapter。
- 未确认前将来源状态置为 `blocked`，不把推测的 selector 部署到生产。

## 5. 合并算法与人工复核

所有候选先入库，再生成匹配候选，不在抓取事务里直接合并实体。匹配分数建议：

| 证据 | 艺人分数 | 作品分数 |
|---|---:|---:|
| 来源稳定 ID 已知映射 | 1.00 | 1.00 |
| 完全相同的规范化品番/Content ID | — | 0.95 |
| 日文名和英文名一致 | 0.70 | — |
| 生日一致 | 0.20 | — |
| 三围、身高、罩杯组合一致 | 0.15 | — |
| 厂商 + 品番 + 日期一致 | — | 0.30 |
| 标题相似 | — | 0.10 |
| 演员交集 | — | 0.20 |

分数只是排序依据，不是自动合并的唯一条件。自动合并阈值建议艺人 >= 0.90 且有一个强证据，作品 >= 0.95；0.70–0.90 进入人工队列，低于 0.70 不合并。合并操作写 `identity_candidates` 和审计记录，支持撤销；实体删除采用合并关系和软删除，不能物理删除来源事实。

字段冲突采用来源优先级而不是最后写入：

```text
JAV Database / XCITY（有结构化详情）
> XSList（结构化档案）
> Jav.Guru（聚合和别名丰富）
> JAVLibrary（访问恢复后再评估）
```

优先级只决定规范化展示值，所有来源原值都保留。出生日期、三围、身高和作品品番冲突进入审核，不进行静默覆盖。

## 6. 任务和增量策略

1. 每个来源先跑目录发现，写 `source_records`，按 `source_record_key` 幂等更新。
2. 新记录或 `last_seen_at` 超过详情刷新周期时创建 performer/release detail job。
3. 艺人详情成功后发现作品引用；作品详情成功后反向补齐演员关系。
4. 详情解析使用 `parser_version`；解析器升级时只重跑受影响来源记录。
5. 目录消失不能立即删除实体。连续三次完整目录运行未见才标记 source record stale；规范化实体保留。
6. 429、5xx、超时和 403/503 分开分类；403/503 进入 blocked 并要求人工解除，不按普通失败快速重试。
7. 任务必须遵守来源条款、robots、速率限制和网络安全校验；不使用绕过验证码或访问控制的方案。

建议第一轮只做每个来源 20 个艺人和 100 个作品 fixture，完成字段覆盖和去重验证，再开始全量目录。全量抓取必须有分页断点、每日预算和可暂停开关。

## 7. 验收标准

- 五个来源均有 adapter 状态：`active`、`partial` 或 `blocked`，每个状态有最近成功/失败原因。
- 同一来源重复运行不会生成重复 source record、performer 或 release。
- 同名艺人至少能保留多个候选，不会因为英文显示名相同而错误合并。
- 同一品番跨来源可合并；合集、重发、不同版本默认可分开表达。
- 规范化字段和来源原字段同时可追溯到 source URL、抓取时间和 parser version。
- 作品详情可以反向列出艺人，艺人详情可以分页列出作品。
- XSList/JAVLibrary 受阻时系统停止重试并提示原因，不影响其他来源任务。
- 任何合并、拆分、字段人工覆盖均有审计记录。

## 8. 推荐实施顺序

先扩展 schema 与 source adapter 接口，再实现 JAV Database 和 XCITY（结构化字段最丰富），随后实现 Jav.Guru。XSList 和 JAVLibrary 先完成阻断探测、fixture 和人工授权流程，确认访问后再启用全量抓取。最后加入 identity candidate 管理页、作品/艺人公开 API、增量调度和统计监控。

## 9. 数据不一致与 Review 流程

不同来源的数据不一致是正常情况，不能用“最后抓到的值覆盖旧值”。系统将原始来源记录、规范化值和人工决定分开保存；任何冲突都必须能回到来源 URL、抓取时间和解析器版本。

### 9.1 字段级证据

对每个需要合并的字段保存一条字段证据：

```text
entity_type: performer / release
entity_id
field_name: birth_date / height_cm / title / release_date ...
source_record_id
raw_value
normalized_value
confidence: verified / normalized / inferred / conflicting / unknown
captured_at
parser_version
```

规范化实体上的字段是当前生效值，但不能替代证据。`source_records.raw_data` 保存完整原始字段；字段证据用于显示“为什么当前值是这个值”。

### 9.2 冲突分类

| 类型 | 示例 | 默认处理 |
|---|---|---|
| 格式差异 | `JUQ743` 与 `JUQ-743` | 规范化后自动合并，保留原值 |
| 语言差异 | `波多野結衣` 与 `Hatano Yui` | 作为别名，不创建冲突 |
| 精度差异 | `163cm` 与 `163.0cm` | 统一单位和精度 |
| 时间精度差异 | `2024-05` 与 `2024-05-24` | 只在精度更高且来源可信时升级，不能伪造日 |
| 轻微数值差异 | 身高 `163` 与 `164` | 生成 field conflict，人工确认 |
| 语义冲突 | 出生日期或品番不同 | 阻止自动覆盖，进入 review |
| 来源缺失 | 本次抓取没有字段 | 保留旧值，不视为删除 |
| 来源阻断 | 403、503、验证码 | 标记 source blocked，不生成空更新 |

### 9.3 Review 数据模型

在 `identity_candidates` 外增加字段冲突和审核表：

```sql
CREATE TABLE field_conflicts (
  id bigserial PRIMARY KEY,
  entity_type text NOT NULL CHECK (entity_type IN ('performer','release')),
  entity_id bigint NOT NULL,
  field_name text NOT NULL,
  current_value jsonb,
  candidates jsonb NOT NULL,
  severity text NOT NULL DEFAULT 'medium',
  status text NOT NULL DEFAULT 'pending',
  selected_source_record_id bigint REFERENCES source_records(id),
  resolution_note text,
  resolved_by bigint,
  resolved_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX field_conflicts_queue
  ON field_conflicts(status, severity, updated_at);

CREATE TABLE review_events (
  id bigserial PRIMARY KEY,
  conflict_id bigint REFERENCES field_conflicts(id),
  entity_type text NOT NULL,
  entity_id bigint NOT NULL,
  action text NOT NULL,
  before_value jsonb,
  after_value jsonb,
  reason text,
  reviewer_id bigint,
  created_at timestamptz NOT NULL DEFAULT now()
);
```

`candidates` 必须包含每个候选值、来源名称、原始 URL、抓取时间、parser version 和证据摘要。不能只显示一组值让审核人猜来源。

### 9.4 自动决策规则

自动处理只允许低风险变化：

1. URL、品番、Content ID 的大小写、空白、连字符差异，经过规范化后视为相同。
2. 英文名、日文名和罗马字之间的转换写入 alias，不改写已有 canonical name。
3. 来源缺字段、页面暂时没有作品、403、503 或解析为空时，不清空现有字段。
4. 日期、身高、三围、罩杯、厂商、品番、Content ID、作品标题发生语义差异时，产生 `field_conflicts`。
5. 规范化合并需要强证据；不满足阈值的艺人或作品只建立 `identity_candidates`，不合并实体。
6. 自动规则只能选择候选来源，不能删除另一来源证据。

建议严重级别：

```text
high   品番、Content ID、出生日期、实体身份、厂商冲突
medium 身高、三围、罩杯、发售日、标题冲突
low    标签、简介、统计数、图片或链接变化
```

统计数、标签和图片可按来源分别展示，通常不需要阻塞核心实体更新；high 冲突必须 review 后才允许发布，medium 依据业务策略决定是否保留旧值，low 可自动采用最新成功来源并保留历史。

### 9.5 Review 页面与操作

管理界面提供 `/admin/review` 队列，支持按来源、实体类型、严重级别、字段和状态筛选。每条冲突展示：

- 当前生效值和更新时间。
- 所有来源候选值及来源优先级。
- 原始页面链接、抓取时间和解析器版本。
- 相关艺人/作品、品番和别名上下文。
- 选择某来源值、手工输入校正值、暂缓、标记为不冲突、拒绝来源值等操作。

操作结果：

| 操作 | 结果 |
|---|---|
| 采用来源值 | 更新规范化字段，并记录所选 `source_record_id` |
| 手工校正 | 写入人工 override，标记 `manual`，后续采集不得覆盖 |
| 保留当前值 | 记录理由，当前值继续生效，新候选保留为证据 |
| 标记不冲突 | 例如别名或单位差异，关闭冲突并记录规则 |
| 暂缓 | 保持 pending，不影响其它字段和任务 |
| 拒绝候选 | 只拒绝该来源字段，不删除来源原文 |
| 合并实体 | 创建 merge 事件、保留别名和重定向，禁止物理删除来源记录 |
| 拆分实体 | 恢复独立实体和关联，所有历史审核事件保留 |

所有操作要求乐观锁 `version`，页面打开后若数据已变化返回 409，审核人必须重新加载候选。批量审核只允许针对同一字段、同一规则的低风险冲突，不能批量处理出生日期、品番和实体合并。

### 9.6 来源优先级和阻断处理

来源优先级只用于排序，不代表绝对正确：

```text
结构化详情和稳定 ID：JAV Database、XCITY
艺人别名和聚合统计：Jav.Guru
档案补充：XSList（访问恢复后）
作品字段：JAVLibrary（访问恢复并验证后）
```

当来源被 403/503、验证码、登录墙或自动化禁止阻断时，任务状态为 `blocked`，并记录 `blocked_reason`、出口标识、最后成功时间和下一次人工探测时间。阻断不生成“字段为空”的候选，也不降低现有字段可信度。VPN 出口配置必须是来源级别的路由，不得通过全局环境变量让所有来源意外走同一出口。来源恢复后先运行 fixture 和小批量 diff，确认解析结果再恢复全量调度。

### 9.8 VPN 出口方案

来源表增加 `egress_policy`，取值为 `direct`、`vpn` 或 `manual`；敏感或不稳定来源可指定单独出口。实际出口地址和凭据不写入数据库，使用部署密钥或 Secret 注入。爬虫的 Fetcher 只接收已解析的代理配置，适配器不得自行切换出口。

```text
XSList      -> vpn-xslist
JAVLibrary  -> vpn-javlibrary
XCITY       -> direct（按实际探测结果调整）
Jav.Guru    -> direct（按实际探测结果调整）
JAV Database-> direct（按实际探测结果调整）
```

每个出口启用前执行探测：DNS/连接、目标首页、一个固定公开目录页和一次限速请求；检查 HTTP 状态、挑战页面特征和 robots/条款。探测失败只暂停该来源，不触发其它来源的 VPN 轮换。任务日志只记录出口名称、状态和耗时，不记录代理凭据。所有来源仍需遵守其访问条款、robots 和请求速率限制。

## 10. Cron 增量采集方案

`:codex-annotation{index="1"}`“所有女演员和作品列表”不应在每次 cron 中重新全量抓取。首次运行建立基线，后续将目录发现、详情更新、作品关联和冲突复核拆成不同任务。每个来源单独维护进度、频率和出口，不能用一个全局 cron 把所有来源同时打满。

### 10.1 任务类型

| 任务 | 作用 | 首次运行 | 增量频率 |
|---|---|---|---|
| performer_discovery | 发现新增/变更艺人 URL | 全目录 | 每日；活跃来源每 6 小时 |
| performer_detail | 抓艺人详情和别名 | 所有发现记录 | 新记录立即，旧记录按刷新周期 |
| release_discovery | 从目录、艺人页发现作品 URL/品番 | 全目录和艺人作品页 | 每日；新艺人立即 |
| release_detail | 抓作品完整 metadata | 所有发现记录 | 新记录立即，已发布作品 7–30 天 |
| relation_reconcile | 补齐艺人-作品关系和去重候选 | 全量关系 | 每日低优先级 |
| conflict_review | 生成字段冲突和身份候选 | 全量历史 | 每小时扫描新增冲突 |
| source_probe | 检查 403/503、VPN 和解析器健康 | 所有来源 | blocked 每 30 分钟，其它来源每日 |

### 10.2 进度水位

每个来源、任务类型和分区保存一条 checkpoint：

```sql
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

`cursor` 用于分页来源（下一页 URL、page token 或稳定的最后一条 ID）；`watermark` 用于按更新时间查询的来源。只有本批数据成功写入 `source_records` 并提交后，才能推进 checkpoint。抓取失败、解析失败或响应被阻断时不得推进水位。

来源没有更新时间和游标时，使用有限重叠窗口：每次从最近 N 页或最近 N 天重新发现，并依靠 `(source_id, entity_type, source_record_key)` 幂等去重。N 初始设为 3 页或 7 天，连续漏数监控后再调整。

### 10.3 首次基线与日常增量

首次基线按来源顺序执行，不与增量任务并发：

```text
目录发现 -> 艺人详情 -> 作品发现 -> 作品详情 -> 关系回填 -> 冲突生成
```

每一步使用可暂停的分页任务。基线完成后把 `baseline_completed_at` 写入来源配置；在基线完成前不允许日常增量覆盖同一分区。

日常增量采用以下策略：

1. 先抓来源的新增、最近更新或首页目录。
2. 新 URL/品番立即创建 detail job，优先级高于周期刷新。
3. 已知记录只在 `next_refresh_at <= now()` 时刷新。
4. 作品详情成功后更新演员关系；演员详情成功后重新检查其作品页是否有新增。
5. 目录中连续多次未出现不能立即删除；只有完整目录任务成功并连续 3 次未见，才把 `source_records` 标记 stale。
6. stale 不会删除规范化艺人或作品，也不会自动下架公开内容；下架由审核策略决定。

### 10.4 推荐 cron 与优先级

cron 只负责创建任务，不直接执行抓取。实际执行由数据库队列和 Worker 完成，避免 cron 重叠运行：

```cron
# 每 6 小时：活跃来源的新增目录
17 */6 * * * avxxx scheduler discover --mode=incremental

# 每小时：新记录详情和失败重试
*/15 * * * * avxxx scheduler dispatch --queues=detail,retry

# 每日低峰：作品关联、标签和轻量复核
35 3 * * * avxxx scheduler reconcile --since=24h

# 每日：正常来源健康探测
50 4 * * * avxxx scheduler probe --sources=enabled

# 每 30 分钟：blocked 来源只做探测，不启动全量抓取
*/30 * * * * avxxx scheduler probe --sources=blocked

# 每周：每个来源选择一个分区做重叠校验
20 5 * * 0 avxxx scheduler audit --sample=1%
```

同一命令必须使用 scheduler lease，例如 `scheduler_runs(source_id, task_type, lease_until)`，拿不到租约就退出。cron 进程崩溃后由租约回收，不依赖 pid 文件。来源级任务仍遵守 `enabled`、VPN 出口和主机速率限制。

### 10.5 优先级与预算

推荐队列优先级：

```text
1  新艺人/新作品详情
2  即将发布或最近更新作品
3  失败重试
4  普通周期刷新
5  关系、标签和统计复核
```

每个来源配置每日最大页面数、最大响应字节数、最大运行时间和最小请求间隔。达到预算后任务转为 `budget_exhausted`，checkpoint 保留下一页，不把预算耗尽当作成功。下个窗口从 checkpoint 继续。

### 10.6 一致性和幂等

- 发现记录使用 `(source_id, entity_type, source_record_key)` 唯一键。
- 作品使用品番/Content ID/source key 的 identifier 归并，不能用标题单独去重。
- 详情写入使用 parser version 和 content hash；内容未变化只更新 `last_seen_at`。
- checkpoint、source record 和本批规范化变更在同一个短事务提交；网络请求不在事务内。
- 每个任务有 `execution_version` 和 lease；旧 Worker 不能推进 checkpoint 或覆盖新解析结果。
- cron 重复触发只会得到一个 scheduler lease，重复任务由活跃任务唯一约束拦截。

### 10.7 失败和来源阻断

可重试错误（超时、429、502、503、504）进入退避队列，不推进 checkpoint；403、验证码、登录墙、解析器不匹配和 VPN 探测失败进入 `blocked` 或 `parser_failed`，停止该来源的增量任务。其它来源继续执行。

来源恢复后按顺序执行：探测 -> 单页 fixture -> 20 条小批量 diff -> 解析结果 review -> 恢复 checkpoint 增量 -> 恢复全量分区。恢复期间不直接跳过积压页，避免恢复后造成大量重复请求。

### 10.8 监控指标

至少监控：

- 每个来源最后成功时间和 checkpoint 停滞时间。
- 发现新增艺人/作品数、详情成功/失败数、stale 数。
- 任务队列长度、最老任务等待时间、重试次数和预算耗尽次数。
- 403/503/验证码比例、VPN 探测耗时和 blocked 时长。
- 同一品番产生的 identity candidate 数量和未处理冲突年龄。

“没有新作品”只有在目录任务成功、checkpoint 推进且未超过预算时才算成功；空结果、被阻断和解析失败必须分别显示。

### 9.7 审核验收

- 同一字段的多个来源值均可查看，不能只看到最终值。
- 空响应、403、503、解析失败不会清空有效数据。
- 采用来源值、人工校正、拒绝候选和实体合并都产生不可变 review event。
- 人工 override 在后续抓取中保持不变，清除 override 必须是显式操作。
- 版本冲突返回 409，不允许旧页面覆盖新审核结果。
- 合并错误可以撤销，来源事实和历史审核记录不能被删除。
