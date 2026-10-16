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

## 监控信号

目前可直接监控：

- `/api/health`：API 和数据库是否可用
- scheduler 日志：任务创建失败、来源策略和 checkpoint 推进
- scraper 日志：任务领取、重试、租约过期和抓取失败
- PostgreSQL 磁盘使用率、连接数和备份文件大小

生产环境建议每分钟探测 `/api/health`，对连续三次失败告警；对 pending 任务持续增长、expired lease、checkpoint 长时间不推进分别告警。日志应集中保存并设置 30 天保留期。

## 播放源离线映射（media_link）

作品 → Javtiful 的映射（`release_media_sources`）由离线任务 `media_link` 提前生成，而不是等到首次打开播放页：

- `cmd/scheduler` 每 `MEDIA_LINK_EVERY`（默认 1h）入队一次，`cmd/audit` worker 执行；也可 `go run ./cmd/audit -media-link` 立即跑一次，或调用 `POST /api/admin/media-links/run`。
- 选取已发布、未合并、有 DVD 番号、尚无该来源映射且尝试记录缺失或已到期的作品，按发行日期从新到旧；按每个番号搜索，只接受路径末段与番号完全相同的结果，唯一命中才写入映射。新入库作品无需额外操作，下一次整点任务会处理。
- 每次尝试写入 `media_link_attempts`：`not_found` 7 天后重试（连续 3 次后改为 30 天）；`ambiguous` 不自动重试，由管理员在 `GET /api/admin/media-links?status=ambiguous` 中选择候选（`POST /api/admin/media-links/{release_id}/choose`）；`error` 按 10 分钟起指数退避，最长 6 小时。`POST /api/admin/media-links/{release_id}/retry` 让某作品立即到期。
- 礼貌限制：两次请求至少间隔 `MEDIA_LINK_MIN_INTERVAL`（默认且最小 2s），每次最多 `MEDIA_LINK_MAX_PER_RUN`（默认 200）部，24 小时内每个来源最多 `MEDIA_LINK_DAILY_CAP`（默认 2000）部。遇到 HTTP 403/429 立即停止并整体退避 1 小时，连续 3 次错误退避 30 分钟；退避时间记录在 `nightly_runs`（job = `media_link.javtiful`）的 `detail.backoff_until`。
- `MEDIA_LINK_ON_DEMAND`（API，默认 `true`）：播放页缺映射时按同一张尝试表即时搜索一次（同样遵守退避和进程内限速）；设为 `false` 则只依赖离线任务，未映射作品的播放接口返回 404。
- `MEDIA_RESOLVE_TTL`（API，默认 5m，0 关闭）：`POST /api/resolve/{source}/{id}` 成功结果的进程内缓存时间；流地址带过期令牌，缓存不会超过最早的 `expires_at` 前 30 秒，错误从不缓存。

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
API_URL=http://127.0.0.1:8080 ./scripts/smoke.sh
```

集成测试会删除 `AVXXX_TEST_DATABASE_URL` 指向数据库的 public schema，因此该变量只能指向一次性测试库，绝不能指向 `DATABASE_URL`、compose 的 `db` 服务或生产库；`make test-integration` 会自动创建并销毁专用容器。

只有 CI 与上述检查全部通过，且最近一次备份可以读取，才将部署标记为可用。
