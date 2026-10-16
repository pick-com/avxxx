# Ansible 部署

部署方式参考 `books/deploy/ansible`：provision 负责主机依赖和用户，deploy 负责拉取代码、生成环境变量、启动 PostgreSQL、API、scheduler 和 scraper。

完整的部署 / 回滚 / 备份 / 恢复手册（含 Caddy、密钥清单、来源许可首跑清单）见 `docs/DEPLOYMENT.md`。`database_url` 默认由 `postgres_password` 派生，二者不必分别传入。

## 使用

1. 编辑 `inventory.ini`，替换目标主机。
2. 使用 Ansible Vault 或命令行传入数据库密码，不要提交密码。

```bash
ansible-playbook -i inventory.ini provision.yml
ansible-playbook -i inventory.ini deploy.yml \
  -e postgres_password='replace-me' \
  -e admin_token='replace-me'
```

`admin_token` 是后台接口 `/api/admin/*` 的 Bearer 令牌；不传时所有后台请求都返回 401。

部署前检查：

```bash
ansible-playbook -i inventory.ini provision.yml --check
ansible-playbook -i inventory.ini deploy.yml --check
```

生产环境建议把 `postgres_password`、`admin_token` 以及 SMTP / VAPID 私钥放进 vault，并将 `git_repo` 改为部署机可访问的 SSH 地址。

DMM/FANZA Affiliate API 凭据（`dmm` 目录适配器）同样放进 vault：`dmm_api_id`、`dmm_affiliate_id`（affiliate ID 须为 990–999 结尾的 API 用 ID）。默认空值时 DMM 来源保持停用，误启用的任务会以 “not configured” 失败。部署后先在主机上执行 `docker compose run --rm scraper /usr/local/bin/scraper -dry-run -adapter dmm` 验证一页映射结果（dry-run 按 verify 规则执行，同样需要 `catalog_contact`），再到后台记录条款审核（`PUT /api/admin/sources/{id}/license`）并启用来源；未审核前 DMM 来源只能跑 verify 任务。

ThePornDB API token（`theporndb` 适配器，third_party_api 层级）放进 vault：`theporndb_api_token`（theporndb.net 账户的个人 API token，只以 `Authorization: Bearer` 发往 `api.theporndb.net`，不写日志、不入库）。默认空值时 ThePornDB 来源什么都不做（抓取任务以 “not configured” 失败，`crosscheck` 任务在 nightly_runs 记录 `not_configured`）。部署后先执行 `docker compose run --rm scraper /usr/local/bin/scraper -dry-run -adapter theporndb` 核对一页 `GET /jav` 映射结果，再记录条款审核并启用来源；`audit` 服务需要同一个 token 和 `catalog_contact` 才能跑交叉验证查询（`docker compose run --rm audit /usr/local/bin/audit -crosscheck` 可手动跑一次）。

OpenRouter 机器翻译 key 放进 vault：`openrouter_api_key`（openrouter.ai 账户的 API key，只以 `Authorization: Bearer` 发往 `openrouter.ai`，不写日志、不入库）。默认使用免费模型（`openrouter_model` 可覆盖，逗号分隔的回退列表），`openrouter_daily_cap`（默认 200 次/UTC 日）限制请求数。空值时机器翻译关闭：`i18n_machine` 任务记录 `not_configured`，`/admin/i18n` 的「机器翻译」按钮不可用。机翻结果只写成「机翻」草稿，需要人工提交审校后才上线；可用 `docker compose run --rm audit /usr/local/bin/audit -mt` 手动跑一次。

目录抓取出口代理（XSList / JAVLibrary 等 `egress_policy=vpn` 来源）放进 vault：`catalog_proxy_url`（全局）和/或 `catalog_proxy_vpn`（供抓取策略 `proxy_url=env:CATALOG_PROXY_VPN` 的来源），值如 `socks5h://user:pass@host.docker.internal:1080`，渲染为 `.env` 的 `CATALOG_PROXY_URL` / `CATALOG_PROXY_VPN`。凭据只在 vault 与 `.env`，日志只显示 `***@host:port`；默认空值即直连，`vpn` 来源无代理时拒绝运行。VPN 本身由运维在主机上连接。启用步骤见 `docs/DEPLOYMENT.md` 2.8。

Web 前端（SSR，compose 服务 `web`，主机 `127.0.0.1:3000`）随部署一起构建和启动。传入 `-e public_base_url=https://<域名>`（不传时用 `base_url`）作为 canonical / hreflang / 站点地图的对外地址；主机反向代理把 `/api/` 转给 `127.0.0.1:8080`、其余转给 `127.0.0.1:3000`（见 `docs/PRODUCTION_OPERATIONS.md`「Web 前端（SSR）与反向代理」）。部署还会安装每日 SEO 检查 cron（写入后台「收录健康分」），`-e seo_check_enabled=false` 可关闭。它用 vault 里的 `seo_report_token`（后台「用户与权限 → 服务令牌」创建、范围只有 `POST /api/admin/seo/report` 的 `svc_…` 令牌，渲染为 `.env` 的 `SEO_REPORT_TOKEN`）上报；该变量为空时才回退用 `admin_token`。

可观测性（`docs/PRODUCTION_OPERATIONS.md`「日志、指标与告警」）：传入 `-e log_hash_key=<随机长字符串>` 让日志中的用户哈希在重启后保持稳定；传入 `-e monitoring_enabled=true` 额外启动 compose `monitoring` profile 中的 Prometheus（只监听 `127.0.0.1:9095`，通过 `ssh -L 9095:127.0.0.1:9095` 访问）。各服务的 `/metrics` 只在 compose 网络内可达，不对外发布端口。

`monitoring_enabled=true` 同时启动 Alertmanager（`127.0.0.1:9096`）和 node_exporter（不发布端口）。值班通道从 vault 变量写入主机 `.env`，都为空时 Alertmanager 照常启动但不通知任何人（日志 warn）：`alert_webhook_url`（page）、`alert_warning_webhook_url`、`alert_email_to` / `alert_warning_email_to` / `alert_email_from`（复用 `smtp_*`）、`alert_slack_webhook_url`、`alert_telegram_bot_token` + `alert_telegram_chat_id`。详见 `docs/PRODUCTION_OPERATIONS.md`「值班通知」。

数据库备份：传 `-e backup_cron_enabled=true` 为部署用户安装每日 cron（`vars.yml` 中 `backup_cron_hour` / `backup_cron_minute` / `backup_keep_days` / `backup_dir`），在 db 容器内 `pg_dump`，并把备份指标写到 `backup_metrics_dir` 供 node_exporter 读取（BackupStale 等告警）。不传时该 cron 条目被移除。异地拷贝需另行配置。

```bash
ansible-playbook -i inventory.ini deploy.yml \
  -e monitoring_enabled=true -e backup_cron_enabled=true \
  -e @secrets.vault.yml --ask-vault-pass   # alert_webhook_url、smtp_password 等
```
