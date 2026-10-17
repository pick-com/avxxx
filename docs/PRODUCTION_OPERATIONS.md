# 生产运维与验收

> 首次部署、密钥清单、发布 / 回滚、反向代理与 TLS、备份 / 恢复 / 恢复演练和来源许可首跑清单见 **`docs/DEPLOYMENT.md`**（唯一的部署手册）。本文负责运行期：日志、指标、告警、值班处置、SEO 检查和发布验收清单。

## 部署验收

部署完成后，在部署主机执行：

```bash
API_URL=http://127.0.0.1:8080 ./scripts/smoke.sh
```

脚本验证 API 健康检查、演员和作品公开接口，以及可发现的 PostgreSQL 容器。它不要求数据库已有目录数据，因此适合首次部署和升级后验收。

## 备份与恢复

使用 PostgreSQL custom format，便于选择性恢复：

```bash
DATABASE_URL='postgres://user:password@host:5432/avxxx?sslmode=require' \
  OUTPUT_DIR=/var/backups/avxxx ./scripts/backup-db.sh

DATABASE_URL='postgres://user:password@host:5432/avxxx?sslmode=require' \
  ./scripts/restore-db.sh /var/backups/avxxx/avxxx-20261015T010000Z.dump
```

生产恢复步骤、异地拷贝和恢复演练（`make restore-drill`）见 `docs/DEPLOYMENT.md` §6–§7。建议每天执行一次全量备份，至少保留 7 个日备份和 4 个周备份；备份文件应存放在独立磁盘或对象存储，不能只放在 PostgreSQL 主机。恢复操作会覆盖同名表，必须在维护窗口执行，并在恢复后重新运行 smoke test。

`scripts/backup-db.sh` 的其他选项：

- `BACKUP_VIA_COMPOSE=1`：在 compose `db` 容器里运行 `pg_dump`（主机不需要 PostgreSQL 客户端，版本总与服务端一致），需在项目目录执行；使用 `POSTGRES_USER` / `POSTGRES_DB`（默认 avxxx）。
- 先写 `.partial` 再改名，失败的运行不会留下看似完整的文件；dump 文件权限 0600。
- `BACKUP_KEEP_DAYS=N`：成功后删除 `OUTPUT_DIR` 中超过 N 天的 `avxxx-*.dump`（默认不删除）。
- `BACKUP_METRICS_DIR=<dir>`：成功后把 `avxxx_backup.prom`（`avxxx_backup_last_success_timestamp_seconds`、`avxxx_backup_last_size_bytes`、`avxxx_backup_last_duration_seconds`）原子写入该目录，供 node_exporter textfile collector 读取（compose 默认挂载 `./backups/metrics`，可用同名变量改路径）。BackupStale / BackupTooSmall / BackupMetricMissing 告警依赖它。

定时备份：Ansible 部署传 `-e backup_cron_enabled=true` 时为部署用户安装每日 cron（默认 03:17，`backup_cron_hour` / `backup_cron_minute`），以 `BACKUP_VIA_COMPOSE=1` 写入 `{{ app_dir }}/backups`（0700），保留 `backup_keep_days`（默认 14）天，指标写到 `backups/metrics`，日志追加到 `backups/backup.log`。不传或设为 false 时会移除该 cron 条目。cron 只负责本机副本，异地拷贝仍需另行配置。

## 日志、指标与告警

实现位于 `internal/obs`（技术方案 3.12）；五个二进制（api、scheduler、scraper、audit、notifier）共用同一套日志与指标约定。

### 结构化日志

- 输出到 stderr；`LOG_FORMAT=json`（默认，生产）或 `text`（开发）；`LOG_LEVEL=debug|info|warn|error`（默认 info）。每行带 `service`。遗留的 `log.Printf` 也经同一个 slog handler 输出（INFO）。
- API 每个请求一行 `msg=http_request`：`request_id`、`method`、`route`（ServeMux 路由模式，如 `/api/releases/{slug}`，绝不记录原始路径或查询串；未匹配的请求记为 `unmatched`）、`status`、`duration_ms`、`bytes`、`client_ip`（只保留 /24 或 IPv6 /48），处理器错误附在同一行的 `err`；5xx 为 ERROR 级。
- `X-Request-ID`：接受客户端/反向代理传入的值（8–64 位 `[A-Za-z0-9._:-]`），否则生成 32 位十六进制；总在响应头回显。
- worker 日志带 `job_id`、`kind`、`source_id`（爬虫另带 `adapter`），任务结束一行 `msg="job finished"`，含 `duration_ms` 和 `err`。
- 脱敏（`internal/obs/redact.go`，对所有输出生效）：键名含 token / password / secret / authorization / cookie / auth / email / endpoint / session / p256dh 等的值替换为 `[REDACTED]`；自由文本中的 Bearer/Basic 凭证、Cookie 头、查询串中的 token/key/password/sig 等参数值、邮箱地址、Web Push endpoint 路径、公网 IPv4 末段均被清除。用户 id 只以 `obs.HashID` 的 HMAC 截断哈希出现；生产环境设置 `LOG_HASH_KEY` 让哈希跨重启稳定。
- 日志集中保存并设置 30 天保留期。

### Prometheus 指标

每个进程在独立的内部监听地址上暴露 `GET /metrics`（`METRICS_ADDR`，设为 `off` 关闭；与公共 API 端口分离）：

| 进程 | 默认 `METRICS_ADDR` | compose 内 |
| --- | --- | --- |
| api | `127.0.0.1:9090` | `api:9090` |
| scheduler | `127.0.0.1:9091` | `scheduler:9091` |
| scraper | `127.0.0.1:9092` | `scraper:9092` |
| audit | `127.0.0.1:9093` | `audit:9093` |
| notifier | `127.0.0.1:9094` | `notifier:9094` |

compose 中各服务监听 `0.0.0.0:909x`，但不发布端口，只有 compose 网络内（Prometheus）可达。`audit -once` 等一次性命令不启动监听。选择 pull（每进程自己的端口）而非 Pushgateway：worker 都是常驻进程，`up` 指标本身就是存活探测。

数据库派生的仪表（队列、租约、审计、片源健康、配对率）只由 scheduler 暴露（单例，避免 API 多实例时重复计数），查询结果缓存 30 秒（配对率 5 分钟）。

| 指标 | 标签 | 来源 |
| --- | --- | --- |
| `avxxx_http_requests_total` / `avxxx_http_request_duration_seconds` / `avxxx_http_response_bytes_total` | method, route, code / status_class | api |
| `avxxx_health_up` | — | api（与 `/api/health` 同一数据库探测） |
| `avxxx_db_pool_conns{state}`、`avxxx_db_pool_max_conns`、`avxxx_db_pool_acquires_total`、`avxxx_db_pool_acquire_wait_seconds_total`、`avxxx_db_pool_empty_acquires_total`、`avxxx_db_pool_canceled_acquires_total` | state | 全部 |
| `avxxx_events_total` | outcome = accepted / duplicate / quarantined | api |
| `avxxx_job_duration_seconds` | kind, result | scraper、audit |
| `avxxx_job_lease_lost_total`、`avxxx_job_claim_errors_total` | kind（, op） | scraper、audit |
| `avxxx_crawl_pages_total`、`avxxx_crawl_bytes_total`、`avxxx_crawl_records_total`、`avxxx_crawl_blocked_total`、`avxxx_crawl_budget_exhausted_total` | source_id（抓取来源） | scraper |
| `avxxx_notifier_deliveries_total`、`avxxx_notifier_delivery_duration_seconds`、`avxxx_notifier_push_sends_total` | channel, status / outcome | notifier |
| `avxxx_jobs_queued`、`avxxx_jobs_expired_leases`、`avxxx_jobs_oldest_due_age_seconds`、`avxxx_jobs_failed_last_hour` | kind（, state = due / scheduled / running） | scheduler |
| `avxxx_crawl_checkpoint_last_success_timestamp_seconds`、`avxxx_crawl_sources_blocked` | source_id | scheduler |
| `avxxx_audit_open_issues`、`avxxx_audit_holds_total` | rule（, severity） | scheduler |
| `avxxx_playback_sources`、`avxxx_source_health_score`、`avxxx_source_health_playable_ratio`、`avxxx_source_health_attempts`、`avxxx_source_health_last_run_timestamp_seconds` | band, status | scheduler |
| `avxxx_play_pairing_ratio`、`avxxx_play_pairing_requests` | — | scheduler（24 小时窗口，截止 1 小时前） |
| `avxxx_dbstats_up`、`avxxx_dbstats_duration_seconds` | — | scheduler |

标签只用代码常量或管理员配置的抓取来源 id；不允许作品、slug、用户、设备、播放片源或路径作标签（`internal/obs` 的测试会检查）。另有 Go runtime 与 process 指标。

### 告警规则

`deploy/monitoring/alerts.yml`（`make check-alerts` 用 promtool 校验语法并运行 `alerts_test.yml` 单元测试）：

| 告警 | 条件 | 级别 |
| --- | --- | --- |
| ApiHealthProbeFailing | `avxxx_health_up == 0` 或 API 抓取失败，持续 3 分钟 | page |
| WorkerDown | 任一 worker 抓取失败 5 分钟 | page |
| ApiHigh5xxRate | 5xx 占比 > 1%，10 分钟 | page |
| ReleaseDetailP99High / RecsP99High | 作品详情 P99 > 150 ms / 推荐 P99 > 50 ms，15 分钟 | warning |
| PlayConfigP99High | play-config P99 > 300 ms（运维阈值） | warning |
| DbPoolSaturated | 连接池占用 > 90%，10 分钟 | warning |
| JobsBacklogNotDraining / JobsPendingGrowing | 最老的待执行任务等待 > 30 分钟 / due 积压 > 20 且 1 小时内增长 | warning |
| JobsExpiredLeases | 存在过期租约 5 分钟 | warning |
| JobsFailed | 最近 1 小时有任务重试耗尽后失败 | warning |
| CrawlCheckpointStale | 已启用来源 checkpoint 24 小时未推进 | warning |
| CrawlSourceBlocked / JobLeaseLost | 来源被 429/403 阻断 / worker 丢失租约 | info |
| SourceHealthBelowAlphaGate | 汇总片源健康分 < 95（Alpha 门槛，≥ 50 次尝试） | warning |
| PlayableRateLow | 可播放率 < 95% | page |
| SourceHealthStale | 片源健康任务 1 小时未运行 | warning |
| EventsQuarantineRateHigh | 事件隔离比例 > 5%，15 分钟 | warning |
| PlayPairingLow | `play_request`→`play_end` 配对率 < 95%（PRD 14.5，≥ 100 次播放） | warning |
| NotifierFailures | 某通道 1 小时失败 > 5 | warning |
| RAgeHold | 新增 R-AGE 扣留 | page |
| RAgeIssuesUnresolved | R-AGE 问题打开超过 24 小时 | warning |
| DbStatsFailing | scheduler 读不到数据库状态 10 分钟（上述任务和数据质量告警失效） | warning |
| HostDiskSpaceLow | 任一真实文件系统（排除 tmpfs/overlay/`/boot` 等）可用空间 < 15%，10 分钟 | warning |
| HostDiskSpaceCritical | 可用空间 < 5%，5 分钟 | page |
| HostMemoryLow | `MemAvailable` < 10%，15 分钟 | warning |
| HostLoadHigh | 15 分钟负载 > 2 × CPU 数，30 分钟 | warning |
| NodeExporterDown | node_exporter 抓取失败 5 分钟（主机与备份告警失效） | warning |
| BackupStale | 最近一次成功备份超过 26 小时 | warning |
| BackupTooSmall | 最近一次备份 < 1 MiB（空库或截断） | warning |
| BackupMetricMissing | node_exporter 在线但 26 小时内没有任何备份指标（未排程或目录不一致） | warning |
| AlertmanagerDown / AlertmanagerNotificationsFailing | Alertmanager 抓取失败 5 分钟 / 某通道 15 分钟内持续发送失败 | warning |

PostgreSQL 数据卷位于 `/var/lib/docker/volumes`，由包含 `/var/lib/docker` 的文件系统（单盘 VPS 上即 `/`）覆盖；磁盘告警检查所有真实文件系统，不只 `/`。

### 运行监控 profile

```bash
make monitoring        # = docker compose --profile monitoring up -d prometheus alertmanager node-exporter
ssh -L 9095:127.0.0.1:9095 -L 9096:127.0.0.1:9096 <host>   # Prometheus / Alertmanager UI 只绑定 127.0.0.1
```

- Prometheus：`deploy/monitoring/prometheus.yml`（15 秒抓取，scheduler 30 秒，默认保留 30 天，`PROMETHEUS_RETENTION` 可调），告警发往 `alertmanager:9093`。每个抓取目标带 `service` 标签（api / scheduler / scraper / audit / notifier / host / monitoring），Alertmanager 按它分组。
- Alertmanager（`prom/alertmanager:v0.28.1`，nobody 用户、只读根文件系统、关闭集群端口）：UI `127.0.0.1:9096`，静默数据存在 `amdata` 卷。配置见下节。
- node_exporter（`prom/node-exporter:v1.9.1`，nobody 用户、只读）：以 `/:/host:ro,rslave` 只读挂载主机根目录读取磁盘、内存和负载；只在 compose 网络内可达，不发布端口；textfile collector 读取 `BACKUP_METRICS_DIR`（默认 `./backups/metrics`，只读挂载）。
- 修改 `prometheus.yml` 或 `render-config.sh` 后需重建容器：`docker compose --profile monitoring up -d --force-recreate prometheus alertmanager`。
- Ansible 部署传 `-e monitoring_enabled=true` 时创建备份目录并启动（重建）这三个服务。

### 值班通知（Alertmanager）

Alertmanager 不展开配置文件中的环境变量，因此容器入口是 `deploy/monitoring/alertmanager/render-config.sh`：启动时根据下列变量在 tmpfs（`/run/alertmanager`，不落盘）生成配置；webhook URL、SMTP 密码和 bot token 写入 0600 文件并以 `*_file` 字段引用，渲染出的 YAML 和 Alertmanager `/api/v2/status` 都不含密钥。任何密钥都不进仓库：放在主机 `.env`（Ansible 从 vault 变量生成）。

| 变量 | Ansible 变量 | 用途 |
| --- | --- | --- |
| `ALERT_WEBHOOK_URL` | `alert_webhook_url` | page 级通用 webhook（Alertmanager webhook JSON；可接 PagerDuty/Opsgenie/自建转发） |
| `ALERT_WARNING_WEBHOOK_URL` | `alert_warning_webhook_url` | warning 级 webhook（可选；不要与 page 的寻呼地址相同） |
| `ALERT_EMAIL_TO` | `alert_email_to` | page 级收件人（逗号分隔） |
| `ALERT_WARNING_EMAIL_TO` | `alert_warning_email_to` | warning 级收件人，默认同 `ALERT_EMAIL_TO` |
| `ALERT_EMAIL_FROM` | `alert_email_from` | 发件人，默认 `MAIL_FROM` |
| `SMTP_HOST` / `SMTP_PORT` / `SMTP_USER` / `SMTP_PASSWORD` | `smtp_*` | 复用 notifier 的 SMTP；要求 STARTTLS，465 端口为隐式 TLS |
| `ALERT_SLACK_WEBHOOK_URL` | `alert_slack_webhook_url` | 可选：Slack（或兼容的 Mattermost 等）incoming webhook，page 与 warning 都发 |
| `ALERT_TELEGRAM_BOT_TOKEN` + `ALERT_TELEGRAM_CHAT_ID` | `alert_telegram_bot_token` / `alert_telegram_chat_id` | 可选：Telegram bot，page 与 warning 都发；chat id 必须是数字 |
| `ALERTMANAGER_EXTERNAL_URL` | — | 通知里的链接，默认 `http://127.0.0.1:9096` |

一个都没配时 Alertmanager 仍然启动（接收器为空），日志输出 `level=warn ... no alert receiver configured`；只配了 warning 通道而没有 page 通道时同样告警。启动后用 `docker compose logs alertmanager | grep render-config` 确认 `receivers configured: ...`。

路由树（`render-config.sh` 内，`make check-alerts` 用 `amtool check-config` 校验，并用 `amtool config routes test` 断言下列路由）：

```
route  receiver=ticket  group_by=[alertname, service]  group_wait=1m  group_interval=5m  repeat=12h
├── severity="info"     → null    （不通知；在 UI 查看）
├── severity="page"     → pager   group_wait=30s  group_interval=5m   repeat=2h
└── severity="warning"  → ticket  group_wait=2m   group_interval=10m  repeat=12h
（没有 severity 的告警落到根路由 ticket）
```

抑制规则（源告警触发时静音目标告警）：

- ApiHealthProbeFailing → ApiHigh5xxRate、ReleaseDetailP99High、RecsP99High、PlayConfigP99High。
- WorkerDown{job=avxxx-scraper} → `kind=crawl` 的 Jobs* / JobLeaseLost，以及 CrawlCheckpointStale。
- WorkerDown{job=avxxx-audit} → audit worker 处理的各 kind（audit、source_health、recs_*、release_curves、search_stats、media_link、completeness_snapshot）的 Jobs* / JobLeaseLost，以及 SourceHealthStale。
- WorkerDown{job=avxxx-notifier} → NotifierFailures；WorkerDown{job=avxxx-scheduler} → DbStatsFailing。
- HostDiskSpaceCritical → 同一 instance + mountpoint 的 HostDiskSpaceLow；NodeExporterDown → BackupMetricMissing。

发送测试告警（验证通道）：

```bash
curl -XPOST http://127.0.0.1:9096/api/v2/alerts -H 'Content-Type: application/json' \
  -d '[{"labels":{"alertname":"PagingTest","severity":"page","service":"monitoring"},"annotations":{"summary":"paging test, ignore"}}]'
```

30 秒内应收到 page 通知，约 5 分钟后（未再推送）收到 resolved。

静默（维护窗口或已知问题）：在 UI `http://127.0.0.1:9096/#/silences` 新建，或在主机上：

```bash
docker compose exec alertmanager amtool --alertmanager.url=http://127.0.0.1:9093 \
  silence add alertname=HostDiskSpaceLow mountpoint=/ --duration=4h --comment='resize scheduled' --author=<you>
docker compose exec alertmanager amtool --alertmanager.url=http://127.0.0.1:9093 silence query
docker compose exec alertmanager amtool --alertmanager.url=http://127.0.0.1:9093 silence expire <id>
```

静默保存在 `amdata` 卷，重启和重建容器后仍然有效。

### 告警处置手册（page 级）

每条 page 告警：确认 → 先止血 → 在值班记录里写下原因。需要更长时间时为它加静默并注明原因，不要静默 `severity=page` 整体。

**ApiHealthProbeFailing** — API 进程不可达，或 `/api/health` 的数据库 ping 失败 3 分钟；公开站点不可用。
- `docker compose ps api db`；`curl -s 127.0.0.1:8080/api/health`；`docker compose logs --since 15m api | grep -i error`。
- 数据库：`docker compose exec db pg_isready`，`docker compose logs --since 15m db`；磁盘满（HostDiskSpaceCritical 同时触发）先处理磁盘。
- 进程退出：`docker compose up -d api`；刚发布的话回滚到上一版本后再查。
- 静默：`amtool silence add alertname=ApiHealthProbeFailing --duration=1h`（仅限计划内维护）。

**WorkerDown** — 某 worker（`job` 标签）的指标端口 5 分钟无响应，通常是进程崩溃或反复重启；其下游任务告警已被抑制。
- `docker compose ps <service>`；`docker compose logs --since 30m <service>`，查 panic、`DATABASE_URL`、迁移版本不匹配。
- `docker compose up -d <service>`；重启后 Prometheus `/targets` 应恢复 up。scheduler 停止时 DbStatsFailing 被抑制，但所有数据库派生告警此期间都失明。
- 静默：`amtool silence add alertname=WorkerDown job=avxxx-scraper --duration=2h`（例如有意停用爬虫时）。

**ApiHigh5xxRate** — 超过 1% 的 API 请求返回 5xx 达 10 分钟（API 整体不可达时被 ApiHealthProbeFailing 抑制）。
- 按路由定位：Prometheus 查询 `sum by (route, code) (rate(avxxx_http_requests_total{code=~"5.."}[5m]))`。
- `docker compose logs --since 15m api | grep '"level":"ERROR"'`，用 `request_id` 关联；检查 DbPoolSaturated、数据库慢查询、最近一次发布或迁移。
- 静默：通常不静默；确认为单一路由的已知问题时 `amtool silence add alertname=ApiHigh5xxRate --duration=2h --comment=<issue>`。

**PlayableRateLow** — 片源健康检查的可播放率 < 95%（≥ 50 次尝试），用户点播放大面积失败。
- Prometheus：`avxxx_playback_sources` 按 status 看失败集中在哪个提供方；`docker compose logs --since 1h audit | grep source_health`。
- 播放提供方被封（403/429）或改版：考虑关闭 `MEDIA_LINK_ON_DEMAND`、暂停对应来源，联系提供方。
- 静默：提供方已知故障时按提供方恢复时间静默 `alertname=PlayableRateLow`。

**RAgeHold** — 合规：有作品因演员拍摄时可能未满 18 岁被 R-AGE 扣留（已自动下架）。
- 在 AdminAudit 过滤 rule R-AGE 审核；确认无误才可重新发布，有疑问保持扣留。
- 不要静默；处理完后 15 分钟窗口过去告警自动恢复。

**HostDiskSpaceCritical** — 某文件系统可用空间 < 5%；PostgreSQL 写满后会拒绝写入甚至无法启动。
- `df -h`；`docker system df`；`du -sh /opt/avxxx/backups /var/lib/docker/volumes/*`。
- 快速释放：删除旧备份（先确认异地已有副本）、`docker image prune`、清理容器日志；不要删除 `pgdata` 卷内文件。
- 根本解决：扩容磁盘或把备份移到独立磁盘/对象存储。
- 静默：扩容进行中时 `amtool silence add alertname=HostDiskSpaceCritical mountpoint=/ --duration=2h`。

## 播放源离线映射（media_link）

作品 → Javtiful 的映射（`release_media_sources`）由离线任务 `media_link` 提前生成，而不是等到首次打开播放页：

- `cmd/scheduler` 每 `MEDIA_LINK_EVERY`（默认 1h）入队一次，`cmd/audit` worker 执行；也可 `go run ./cmd/audit -media-link` 立即跑一次，或调用 `POST /api/admin/media-links/run`。
- 选取已发布、未合并、有 DVD 番号、尚无该来源映射且尝试记录缺失或已到期的作品，按发行日期从新到旧；按每个番号搜索，只接受路径末段与番号完全相同的结果，唯一命中才写入映射。新入库作品无需额外操作，下一次整点任务会处理。
- 每次尝试写入 `media_link_attempts`：`not_found` 7 天后重试（连续 3 次后改为 30 天）；`ambiguous` 不自动重试，由管理员在 `GET /api/admin/media-links?status=ambiguous` 中选择候选（`POST /api/admin/media-links/{release_id}/choose`）；`error` 按 10 分钟起指数退避，最长 6 小时。`POST /api/admin/media-links/{release_id}/retry` 让某作品立即到期。
- 礼貌限制：两次请求至少间隔 `MEDIA_LINK_MIN_INTERVAL`（默认且最小 2s），每次最多 `MEDIA_LINK_MAX_PER_RUN`（默认 200）部，24 小时内每个来源最多 `MEDIA_LINK_DAILY_CAP`（默认 2000）部。遇到 HTTP 403/429 立即停止并整体退避 1 小时，连续 3 次错误退避 30 分钟；退避时间记录在 `nightly_runs`（job = `media_link.javtiful`）的 `detail.backoff_until`。
- `MEDIA_LINK_ON_DEMAND`（API，默认 `true`）：播放页缺映射时按同一张尝试表即时搜索一次（同样遵守退避和进程内限速）；设为 `false` 则只依赖离线任务，未映射作品的播放接口返回 404。
- `MEDIA_RESOLVE_TTL`（API，默认 5m，0 关闭）：`POST /api/resolve/{source}/{id}` 成功结果的缓存时间，滑动续期（每次命中延长到 now + TTL）；流地址带过期令牌，缓存不会超过最早的 `expires_at` 前 30 秒（适配器未给出时从签名 URL 的 `expires=` / `exp=` / `e=` / `validto=`、SigV4、Akamai token 中解析，含义不明确则不解析），错误从不缓存。
- `MEDIA_RESOLVE_MAX_AGE`（API，默认 30m）：流过期时间未知时，结果自解析起最多复用这么久。结果在进程内缓存之外还写入 Postgres `media_resolve_cache`（单行 ≤ 64 KiB；带 Cookie / Authorization 头的结果不落库），重启和多个 API 实例共用；过期行由 API 每 10 分钟顺带清理。播放器在缓存地址播放失败时以 `?refresh=1` 重新解析，同一 (source, id) 每个实例每 60 秒最多强制刷新一次。
- `PLAYBACK_RESOLVE_ON`（Web，默认 `load`）：`load` 打开播放页即解析播放地址（#115 的行为）；`play` 只加载 play-config / 播放映射（仅查库），显示封面和播放按钮，点击播放时才调用 `/api/resolve`，自动连播和队列（`?list=`）仍立即开始。

## Web 前端（SSR）与反向代理

PRD 13 SEO 要求视频页、演员页、分类页服务端渲染。此前 `web/dist` 没有任何服务在提供（compose 和 Ansible 只启动 Go 服务，仓库里没有 nginx / Caddy 配置），前端只能用 `npm run dev` 访问。现在：

- `web` 服务（`web/Dockerfile`，node:24 bookworm-slim，以内置的非 root 用户 `node` 运行，`/healthz` 健康检查）运行 `web/server.mjs`：`/assets/*` 带 `immutable` 一年缓存，其余静态文件 1 小时，其余请求由 React Router 服务端渲染（`ssr: true`）。监听 compose 内 `web:3000`，主机只绑定 `127.0.0.1:${WEB_PORT:-3000}`。
- SSR 加载器经 `API_INTERNAL_URL`（compose：`http://api:8080`）直接调用 Go API；公开 URL（canonical、hreflang、站点地图）用 `PUBLIC_BASE_URL`（默认取 `BASE_URL`，再退回请求自身的 origin），生产必须设置成对外域名。
- 环境变量：`API_INTERNAL_URL`、`PUBLIC_BASE_URL`、`API_PROXY`（compose 用 `WEB_API_PROXY`）、`API_TIMEOUT_MS`（默认 4000）、`SITEMAP_TTL_SECONDS`（默认 3600）、`SITEMAP_CHUNK`（每个站点地图文件的 URL 数，默认 5000）、`LOG_REQUESTS`（每请求一行 JSON 日志，`false` 关闭）、`PORT` / `HOST`。

反向代理（主机上的 Caddy / nginx，负责 TLS 和压缩）按路径分流：

| 路径 | 上游 |
| --- | --- |
| `/api/*` | `127.0.0.1:8080`（Go API） |
| 其余全部（页面、`/assets/*`、`/sitemap.xml`、`/sitemaps/*`、`/robots.txt`、`/sw.js`） | `127.0.0.1:3000`（web） |

`/api/` 应直接交给 API：API 只用连接地址记录 `client_ip` 和做投稿限流，经 web 转发时看到的都是 web 容器。`WEB_API_PROXY=true`（默认）时 web 也会转发 `/api/*`，只用于单上游的简易部署和本地运行。示例（Caddy）：

```
javpick.com {
  encode zstd gzip
  handle /api/* {
    reverse_proxy 127.0.0.1:8080
  }
  handle {
    reverse_proxy 127.0.0.1:3000
  }
}
```

HTML 响应为 `Cache-Control: no-cache` + `Vary: Cookie`（无前缀入口会按 `lang` cookie 302 到所选语言），不要让共享缓存缓存 HTML；站点地图和 robots.txt 为 `public, max-age=3600`。

### 多语种 URL

- 每个前台页面每种语言一个 URL：无前缀为简体中文（同时是 `x-default`），其余七种为 `/{lang}/...`（`/en/releases/abc-123`、`/zh-TW/performers/x`）。后台 `/admin` 无前缀。
- `/zh-CN/...` 和不规范写法（`/EN/...`、`/zh-tw/...`）301 到规范 URL；不是语言的首段（`/foo/...`）返回 404。
- 页面的界面语言和内容语言（`?lang=`）都取自 URL；切换器跳到同一页面的另一语言并写入 `lang` cookie（登录用户同时 PATCH `ui_lang`）；带前缀的入口也会写 cookie。无前缀入口在有非默认 cookie 时 302 到该语言；爬虫不带 cookie，总是看到 URL 自身的语言。登录用户在设置里单独选了不同于界面语言的内容语言时，内容按设置。
- 每个可索引页面输出 canonical、8 种语言 + `x-default` 的 hreflang、Open Graph / Twitter 卡片和 JSON-LD（视频页 VideoObject、演员页 Person（隐藏的生日从不输出）、BreadcrumbList、首页 WebSite）。搜索、设置、个人页、登录、后台、404 和筛选组合页带 `noindex`；不存在 / 未发布 / 扣留的视频和演员返回 404，API 不可用时返回 503。

### 站点地图与 robots.txt

- `/sitemap.xml` 为索引，列出 `/sitemaps/{type}-{lang}-{n}.xml`：`pages`（首页、发现、分类、演员库、法律页）、`releases`（已发布、未合并且可播放：有 active/degraded 片源或 Javtiful 映射）、`performers`（有已发布作品的 active 演员）、`tags`（`/discover?tag=`），每种语言一份，每个 URL 带全部 hreflang 备选，视频 / 演员 / 标签带 `lastmod`。
- 数据来自 `GET /api/sitemap/{releases,performers,tags}?cursor=&limit=`（游标分页，单页最多 10000）和 `GET /api/sitemap/counts`；web 进程内缓存 `SITEMAP_TTL_SECONDS`。
- `robots.txt` 允许前台页面，禁止 `/admin`、`/api/`、个人页、设置、登录和带查询串的搜索页（按语言前缀逐条列出并以 `$` 结尾，避免误伤 slug），并声明站点地图地址。

### 收录健康分（`seo.check`）

PRD 11 / 13：收录健康分 = 服务端渲染通过率 × 0.6 + 站点地图覆盖率 × 0.4（目标 ≥ 90）。`npm run seo:check`（`web/scripts/seo-check.mjs`，镜像内为 `node scripts/seo-check.mjs`）：

1. 读取 `/sitemap.xml`，统计 zh-CN 文件中各类型 URL 数，与 `GET /api/sitemap/counts` 比较得到覆盖率（缺少某语言文件按比例扣分）；
2. 从站点地图每类型随机抽 `SEO_SAMPLES`（默认 5）个 URL（随机语言），不执行 JS 直接请求，检查 200、非空 `<h1>`、自指 canonical、8 语言 + x-default hreflang、`<html lang>`、无 noindex；
3. 输出 JSON 报告；设置 `ADMIN_TOKEN` 时 POST 到 `/api/admin/seo/report`，写入 `nightly_runs`（job = `seo.check`，detail 含 `score`、`band`、各类型结果和最多 20 条失败），后台总览「收录健康分」显示最近一次结果，`GET /api/admin/seo/report` 返回最近一次。`SEO_MIN_SCORE` 可让分数不达标时退出码为 1。

Ansible 部署会安装每日 05:15 的 cron（`seo_check_enabled=false` 关闭），在 compose 网络内运行：

```bash
docker compose run --rm --no-deps -e WEB_URL=http://web:3000 -e API_URL=http://api:8080 -e ADMIN_TOKEN web node scripts/seo-check.mjs
```

## 发布验收清单

发布的提交必须在 GitHub Actions CI（`.github/workflows/ci.yml`）中全部通过：`go`（build、vet、单元测试，gofmt 仅告警）、`integration`（Postgres 17 服务容器中的集成测试）、`web`（`npm ci` + `npm run build`）。

本地发布前再执行：

```bash
go test ./...
go vet ./...
make test-integration   # 需要 docker；启动一次性 postgres:17 容器（随机端口，--rm），结束后自动清理；含 internal/acceptance 全量验收
make restore-drill      # 需要 docker；最新备份（或种子库）恢复到一次性容器 + API + smoke + 行数比较
go build ./cmd/...
(cd web && npm run build)
docker compose config
docker compose --profile monitoring config -q
make check-alerts       # 需要 docker 或本地 promtool
API_URL=http://127.0.0.1:8080 ./scripts/smoke.sh
docker compose build web && curl -fsS http://127.0.0.1:3000/healthz
WEB_URL=http://127.0.0.1:3000 API_URL=http://127.0.0.1:8080 npm --prefix web run seo:check
```

集成测试会删除 `AVXXX_TEST_DATABASE_URL` 指向数据库的 public schema，因此该变量只能指向一次性测试库，绝不能指向 `DATABASE_URL`、compose 的 `db` 服务或生产库；`make test-integration` 会自动创建并销毁专用容器。

只有 CI 与上述检查全部通过，且最近一次备份可以读取，才将部署标记为可用。
