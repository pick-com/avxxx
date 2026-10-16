# 生产运维与验收

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

建议每天执行一次全量备份，至少保留 7 个日备份和 4 个周备份；备份文件应存放在独立磁盘或对象存储，不能只放在 PostgreSQL 主机。恢复操作会覆盖同名表，必须在维护窗口执行，并在恢复后重新运行 smoke test。

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

### 运行监控 profile

```bash
docker compose --profile monitoring up -d prometheus   # 或 make monitoring
ssh -L 9095:127.0.0.1:9095 <host>                      # UI 只绑定在 127.0.0.1:9095
```

Prometheus 配置在 `deploy/monitoring/prometheus.yml`（15 秒抓取，scheduler 30 秒，默认保留 30 天，`PROMETHEUS_RETENTION` 可调）。Ansible 部署传 `-e monitoring_enabled=true` 时一并启动。目前没有 Alertmanager：触发中的告警在 Prometheus UI 的 `/alerts` 查看；接入值班通知时在 `prometheus.yml` 加 `alerting` 段。PostgreSQL 磁盘使用率和备份文件大小仍需主机级监控（node_exporter 或云厂商监控）。

## 发布验收清单

发布的提交必须在 GitHub Actions CI（`.github/workflows/ci.yml`）中全部通过：`go`（build、vet、单元测试，gofmt 仅告警）、`integration`（Postgres 17 服务容器中的集成测试）、`web`（`npm ci` + `npm run build`）。

本地发布前再执行：

```bash
go test ./...
go vet ./...
make test-integration   # 需要 docker；启动一次性 postgres:17 容器（随机端口，--rm），结束后自动清理
go build ./cmd/...
(cd web && npm run build)
docker compose config
docker compose --profile monitoring config -q
make check-alerts       # 需要 docker 或本地 promtool
API_URL=http://127.0.0.1:8080 ./scripts/smoke.sh
```

集成测试会删除 `AVXXX_TEST_DATABASE_URL` 指向数据库的 public schema，因此该变量只能指向一次性测试库，绝不能指向 `DATABASE_URL`、compose 的 `db` 服务或生产库；`make test-integration` 会自动创建并销毁专用容器。

只有 CI 与上述检查全部通过，且最近一次备份可以读取，才将部署标记为可用。
