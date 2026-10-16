# 部署、备份与恢复手册

本文是生产部署的唯一操作手册：从一台空主机到可用站点、日常发布与回滚、备份与恢复、恢复演练和发布后检查。日志 / 指标 / 告警细节、告警处置和 SEO 检查见 `docs/PRODUCTION_OPERATIONS.md`；Ansible 变量的逐项说明见 `deploy/ansible/README.md`。

> 生产主机地址只登记在 `deploy/ansible/inventory.ini`（`[avxxx]` 组中的 `avxxx-pod` 条目）。本文和任何文档、脚本都不重复主机 IP 或密钥；操作前先核对 inventory 里的当前地址。

## 1. 架构一览

```
Internet ──443──▶ Caddy（主机，TLS、压缩）
                    ├── /api/*  ──▶ 127.0.0.1:8080  api（Go，容器）
                    └── 其余    ──▶ 127.0.0.1:3000  web（SSR，容器）
docker compose（项目目录 /opt/avxxx，部署用户 avxxx）
  db (postgres:17, 卷 pgdata, 127.0.0.1:55434)   api   web   scheduler   scraper   audit   notifier
  monitoring profile：prometheus 127.0.0.1:9095 · alertmanager 127.0.0.1:9096 · node-exporter
  storage profile（可选）：minio（目录图片，S3 兼容，仅 compose 网络）
主机 cron：03:17 数据库备份（可选）· 05:15 SEO 检查 · 异地拷贝（需自行配置，见 §6.3）
```

所有 Go 服务共用 `avxxx-app:latest` 镜像（`Dockerfile.api`），web 用 `avxxx-web:latest`；两者都在主机上从 git 检出的源码构建，没有镜像仓库。所有端口只绑定 `127.0.0.1`，对外只有 Caddy 的 80/443。

## 2. 首次部署

### 2.1 前置条件

- 一台 Debian/Ubuntu VPS（≥ 2 vCPU、4 GB 内存、40 GB 磁盘；目录数据增长后按 `HostDiskSpaceLow` 告警扩容），root SSH 可达。
- 域名的 A/AAAA 记录指向该主机（Caddy 申请证书需要）。
- 部署机（运维笔记本）装有 Ansible ≥ 2.15，能 SSH 到主机；主机能访问 `git_repo`（默认 `git@github.com:pick-com/avxxx.git`，给部署用户配置只读 deploy key）。

### 2.2 准备 inventory 与 vault

1. 在 `deploy/ansible/inventory.ini` 的 `[avxxx]` 组里启用 `avxxx-pod ansible_host=… ansible_user=root` 条目（地址以该文件为准，不要写进其他文件），删除或注释 `localhost` 行。
2. 建立 vault 文件（不提交仓库）：

```bash
cd deploy/ansible
ansible-vault create secrets.vault.yml     # 内容见 §3，至少 postgres_password / admin_token / catalog_contact / public_base_url
```

### 2.3 Provision（主机初始化，一次性）

```bash
ansible-playbook -i inventory.ini provision.yml --check   # 预演
ansible-playbook -i inventory.ini provision.yml
```

安装 `git`、`docker.io`、`docker-compose-plugin`，创建加入 `docker` 组的部署用户 `avxxx` 和 `/opt/avxxx`（0750）。之后在主机上为 `avxxx` 用户放置 GitHub deploy key（`~avxxx/.ssh/id_ed25519`，并 `ssh -T git@github.com` 接受主机指纹）。防火墙只放行 22/80/443（`ufw allow OpenSSH; ufw allow 80,443/tcp; ufw enable`）。

### 2.4 Deploy

```bash
ansible-playbook -i inventory.ini deploy.yml --check -e @secrets.vault.yml --ask-vault-pass
ansible-playbook -i inventory.ini deploy.yml \
  -e @secrets.vault.yml --ask-vault-pass \
  -e monitoring_enabled=true -e backup_cron_enabled=true
```

deploy.yml 依次：检出 `git_branch`（默认 main）→ 由 vault 变量渲染 `/opt/avxxx/.env`（0600）→ 启动 `db` → 构建 `api`、`web` 镜像 → 启动 api、scheduler、scraper、audit、notifier、web → （可选）监控 profile 与每日备份 cron → 检查 `127.0.0.1:8080/api/health` 和 `127.0.0.1:3000/healthz` → 安装每日 SEO 检查 cron。数据库迁移由第一个启动的 Go 进程自动执行（§4.3）。

### 2.5 反向代理与 TLS（Caddy）

在主机上安装 Caddy（`apt install caddy`，官方仓库），写入 `/etc/caddy/Caddyfile`，`systemctl reload caddy`。Caddy 自动申请和续期 Let's Encrypt 证书（需 80/443 可达），HTTP 自动 308 到 HTTPS。

```caddyfile
{
	email ops@example.com            # ACME 账户联系人（证书到期通知）
}

www.reel.example {
	redir https://reel.example{uri} permanent
}

reel.example {
	encode zstd gzip

	header {
		Strict-Transport-Security "max-age=31536000; includeSubDomains"
		X-Content-Type-Options    "nosniff"
		Referrer-Policy           "strict-origin-when-cross-origin"
		X-Frame-Options           "SAMEORIGIN"
		-Server
	}

	# 后台、指标和监控 UI 从不经代理暴露：/metrics 只在 compose 网络内，
	# Prometheus / Alertmanager 只绑定 127.0.0.1（ssh -L 访问）。
	@internal path /metrics /metrics/*
	respond @internal 404

	handle /api/* {
		reverse_proxy 127.0.0.1:8080 {
			header_up X-Request-ID {http.request.header.X-Request-ID}
			transport http {
				read_timeout 35s       # API WriteTimeout 30s
			}
		}
	}

	handle {
		reverse_proxy 127.0.0.1:3000 {
			health_uri /healthz
			health_interval 15s
		}
	}

	log {
		output file /var/log/caddy/reel.access.log {
			roll_size 50MiB
			roll_keep 10
		}
		format json
	}
}
```

要点：

- `/api/` 直接给 API（不经 web），`WEB_API_PROXY=true` 只是单上游的退路。
- 客户端地址：在 `.env` 设置 `TRUSTED_PROXIES=127.0.0.1/32,::1/128`（Ansible 变量 `trusted_proxies`，`-e trusted_proxies=127.0.0.1/32,::1/128` 或写进 vault）。API 默认（变量为空）只信任 TCP 对端地址、不读 `X-Forwarded-For`，经 Caddy 后所有请求都来自代理，演员投稿的「每地址」限流退化为一个共享桶，只剩全局限流起作用。设置后，对端在这些网段内时 API 从 `X-Forwarded-For` 右往左取第一个不在可信网段内的地址作为客户端（`internal/clientip`），客户端自己伪造的左侧条目不会被采用；访问日志的 `client_ip` 也改用该地址（仍截断到 /24、/48）。Caddy 的 `reverse_proxy` 默认就会设置 `X-Forwarded-For`（不信任客户端传入的值），无需额外配置。
  - 只列 Caddy 实际连到 API 时使用的地址，不要放宽到整个 Docker 网段（web 容器等也在其中）。启用后看一条经 Caddy 的请求日志：`client_ip` 应是真实客户端网段；如果仍是 `172.x.x.0/24` 之类的 Docker 网桥地址（发布端口经 docker-proxy 转发时，API 看到的对端是网桥网关），把该网关地址（`docker network inspect avxxx_default` 的 `Gateway`）以 `/32` 加进 `TRUSTED_PROXIES`。
  - 值无法解析时 API 拒绝启动（`client ip config`）。
- `PUBLIC_BASE_URL` 必须等于对外域名（`https://reel.example`），否则 canonical、hreflang、站点地图指向错误的 origin。
- 验证：`curl -sI https://reel.example/ | head -1`，`curl -s https://reel.example/api/health`，`curl -s https://reel.example/sitemap.xml | head`。

### 2.6 首次运行：来源许可门禁清单（恢复抓取前必做）

新库（或从备份恢复的库）里，`license_status` 为 `review_required` / `manual_only`、或缺少 `terms_reviewed_at` / `terms_url` 的目录来源**不会被自动抓取**，只能手动跑 verify 任务（`catalog.Gate`，migration 024）；DMM（025）与 ThePornDB（032）来源默认停用且未审核。按来源逐一完成：

1. **配置联系方式**：`.env` 有 `CATALOG_CONTACT`（URL 或邮箱，写进 User-Agent）。没有它，所有目录任务以 `config_error` 失败（RSS 照常）。
2. **列出来源与门禁状态**：`curl -s -H "Authorization: Bearer $ADMIN_TOKEN" -H "X-Admin-User: $USER" 127.0.0.1:8080/api/admin/sources | jq '(.items // .)[] | {id,name,adapter,source_kind,license_status,terms_url,terms_reviewed_at,automatic_allowed,gate_reason}'`（或后台「来源治理」页）。
3. **阅读条款**：打开 `terms_url`（没有就先找到站点条款 / API 文档），确认允许自动化抓取元数据、频率限制和署名要求；检查 `robots.txt`（爬虫令牌 `avxxx-catalog`）。被禁止的来源设为 `denied`。
4. **核对解析**：对允许的来源先跑一次 verify（不写目录）：`POST /api/admin/sources/{id}/jobs`（`{"phase":"verify"}`，最多 3 页），在后台查看 `result_counts.verify` 的字段覆盖率；凭据类来源先 dry-run：`docker compose run --rm scraper /usr/local/bin/scraper -dry-run -adapter dmm`（`theporndb` 同理）。
5. **记录审核**：`PUT /api/admin/sources/{id}/license`，`{"license_status":"terms_confirmed","terms_url":"https://…","reviewed":true,"note":"条款要点、限速、署名"}`（官方 API 用 `official_api`）。每次审核写入 `review_events`。
6. **预算与限速**：`PUT /api/admin/sources/{id}/crawl-policy`（或后台）确认 `min_request_interval`、每日页数/字节预算、`discovery_interval`；新来源从小预算开始。
7. **启用并观察**：启用来源；需要全量时由管理员请求 baseline（`{"phase":"baseline"}`），日常增量由 scheduler 按 `discovery_interval` 创建。第一天盯住 `CrawlSourceBlocked`、`JobsFailed`、`CrawlCheckpointStale` 告警和 `GET /api/admin/sources/{id}/checkpoints`。
8. **署名**：需要署名的来源填写 `sources.attribution_html`（DMM 数据要求「Powered by FANZA Webサービス」）。

从备份恢复后，来源的审核状态随数据一起恢复；但如果恢复的是很旧的备份，重新确认第 3 步的条款没有变化再恢复自动抓取。

### 2.7 后台账号：首个管理员、紧急令牌与停用

后台用个人管理员账号登录（`/admin/login`：密码 + 身份验证器 6 位码），账号表 `admin_users` 与前台用户完全分开（migration 042）。7 种角色与权限矩阵见后台「用户与权限 → 角色矩阵」或 `internal/adminauth/roles.go`；每个 `/api/admin` 路由的权限在 `internal/api/admin_perms.go`，没有登记的新路由只有超级管理员能用（`go test ./internal/api -run TestAdminRoutePermissionsCoverEveryRoute` 会列出它们）。

**会话与安全策略**：会话放在 HttpOnly、`SameSite=Strict`、`Path=/api/admin` 的 cookie 里（`BASE_URL` 为 https 时带 `Secure`），30 分钟无操作或登录 12 小时后失效；每个请求还要带 `X-Admin-CSRF: 1` 头。密码 argon2id、至少 12 个字符；同一账号连续失败 5 次（密码或验证码）锁定 1 分钟，此后每次失败翻倍，最长 1 小时；登录接口另按来源 IP 限 15 分钟 20 次。TOTP 密钥用 `ADMIN_TOTP_KEY`（AES-256-GCM）加密保存，同一步长的验证码不能重复使用；10 个恢复码只存哈希，每个用一次。

1. **设置密钥**：vault 加 `admin_totp_key`（`openssl rand -hex 32`），重新执行 deploy.yml，`.env` 里出现 `ADMIN_TOTP_KEY`。api 和 `admin` 命令必须用同一个值；更换它会让已有的 TOTP 密钥无法解密，需要给每个管理员重置两步验证。未设置时密钥以明文存储，设置后会在下次登录时自动加密。
2. **创建第一个超级管理员**（在主机上，交互输入密码，不回显、不进日志）：
   ```bash
   cd /opt/avxxx
   docker compose run --rm -it api /usr/local/bin/admin create -username nc -email nc@example.com -name 倪
   ```
   命令打印 `otpauth://` 链接、手动输入用的密钥和 10 个恢复码（只显示这一次）。在验证器 App 里添加后，到 `https://<域名>/admin/login` 用用户名或邮箱、密码和 6 位码登录，第一次成功登录即完成绑定。恢复码离线保存。脚本化时可用 `-password-stdin` 从标准输入读一行密码。
3. **其余管理员**：超级管理员在「用户与权限 → 后台账号」邀请（用户名、邮箱、角色），得到一个 72 小时有效的启用链接 `/admin/enroll#…`，通过可信渠道单独发给本人；对方在该页设置密码、绑定验证器并保存恢复码。
4. **双人审批**：修改角色、重新启用已停用的账号、重置两步验证和「举报与审核」里批量删除刷分评分，发起后进入「待审批」（`/admin/approvals`），必须由另一位有相同权限的管理员批准后才执行（发起人不能批准自己的请求，72 小时未处理自动过期）。至少保留两位超级管理员，否则角色变更只能用紧急令牌批准。停用账号立即生效（同时注销其会话），不需要审批；系统不允许停用或降级最后一位在用的超级管理员。
5. **手机丢失**：用恢复码登录后在「用户与权限 → 我的账号」重新生成恢复码；没有恢复码时，请另一位超级管理员在「后台账号」里重置两步验证（双人审批），批准后得到新的启用链接。
6. **账号被锁**：等锁定时间过去，或在主机上 `docker compose run --rm api /usr/local/bin/admin unlock -username nc`；`admin list` 查看所有账号、角色和锁定状态。

**服务令牌（自动化脚本用）**：脚本不再用紧急令牌，而是用超级管理员在「用户与权限 → 服务令牌」（`/admin/users?tab=service`）创建的服务令牌（`svc_` 开头，`Authorization: Bearer svc_…`）。

- 创建时选择范围（scope）：具体路由（如 `POST /api/admin/seo/report`）或「路由组:read / write」（如 `dashboard:read`），并设有效期（1–730 天，默认 365）。令牌值只在创建时显示一次，库里只存 SHA-256 哈希和前几个字符。
- 无论范围怎么写，服务令牌都**不能**访问后台账号与角色、操作日志、自己的会话 / 待审批队列，也不能调用任何双人审批路由；这类范围在创建时就会被拒绝。
- 每个请求（含只读和被拒绝的）都写入操作日志，身份「服务令牌」、操作人 `service:<名称>`，带来源 IP 的密钥哈希；列表里显示最近使用时间。撤销立即生效，到期自动失效。
- **每日 SEO 检查**：创建名为 `seo-check`、范围只有 `POST /api/admin/seo/report` 的令牌，把值放进 vault 的 `seo_report_token` 并重新部署（`.env` 中 `SEO_REPORT_TOKEN`）。cron 只在 `seo_report_token` 为空时才回退使用 `ADMIN_TOKEN`；手动运行 `npm run seo:check` 时同样优先用 `SEO_REPORT_TOKEN`。

**紧急令牌（break-glass）**：`ADMIN_TOKEN` 仍可作为 `Authorization: Bearer` 使用，权限等同超级管理员、不走双人审批，用于账号体系不可用时（自动化脚本改用上面的服务令牌；`npm run seo:check` 只在未设置 `SEO_REPORT_TOKEN` 时回退用它上报）。每次使用（含只读请求）都写入操作日志（`admin_audit_log`，身份「紧急令牌」，记录路由、对象和来源 IP 的密钥哈希），API 日志也有一条 `admin break-glass token used`（含 `ip_hash`，不含令牌）。写操作的操作人记为 `token:<标签>`，标签取 `X-Admin-User` 头（只用可打印 ASCII，最多 64 字符，缺省 `unlabelled`）；该头不再作为个人账号的操作人。后台登录页下方「紧急令牌」链接可在浏览器里临时使用它（只存在当前标签页）。

- **停用**：建立至少两位超级管理员后，在 vault 设 `admin_token_disabled: true` 并重新部署（`.env` 中 `ADMIN_TOKEN_DISABLED=true`），令牌立即全部 401（尝试会记一条警告日志）。停用前先按上文给每日 SEO 检查配置 `seo_report_token`；否则它回退用紧急令牌，上报会被拒绝（页面检查仍会运行）。
- **轮换**：改 vault 的 `admin_token` 后重新部署；用 `GET /api/admin/audit-log?auth=token` 或后台「操作日志」按身份「紧急令牌」检查最近的使用记录。

**操作日志**：后台「操作日志」（`/admin/oplog`，仅超级管理员）可按关键词、操作人、身份、对象和时间搜索；内容包括所有写操作、所有紧急令牌请求、读取前台用户和版权投诉等个人数据的请求、登录成功与失败。字段级的修改前后值仍在 `review_events`。

## 3. 密钥与配置清单

全部放在 Ansible vault（`secrets.vault.yml`），deploy.yml 渲染到主机 `/opt/avxxx/.env`（0600，属部署用户）。仓库、inventory、文档里都不出现真实值。生成随机值：`openssl rand -hex 32`（URL 安全，可直接放进连接串）。

| vault 变量 | `.env` 变量 | 必需 | 用途 / 说明 |
| --- | --- | --- | --- |
| `postgres_password` | `POSTGRES_PASSWORD`、`DATABASE_URL` | 是 | 数据库密码。只在 `pgdata` 卷**首次初始化**时生效；`database_url` 默认由它派生（vars.yml），已初始化的库改密码需 `ALTER USER` 后再改 vault |
| `admin_token` | `ADMIN_TOKEN` | 建议 | 紧急令牌（break-glass，2.7）：`/api/admin/*` 的 Bearer，等同超级管理员，每次使用记入操作日志。SEO 检查 cron 仅在 `seo_report_token` 为空时用它上报；为空时只能用个人账号登录 |
| `seo_report_token` | `SEO_REPORT_TOKEN` | 建议 | 每日 SEO 检查上报收录健康分用的服务令牌（`svc_…`，范围 `POST /api/admin/seo/report`，2.7）；为空时 cron 回退用 `ADMIN_TOKEN` |
| `admin_token_disabled` | `ADMIN_TOKEN_DISABLED` | 否 | `true` 时紧急令牌全部 401（默认 false）；有两位以上超级管理员后建议开启 |
| `admin_totp_key` | `ADMIN_TOTP_KEY` | 是 | 加密管理员 TOTP 密钥的键（AES-256-GCM）；api 与 `admin` 命令用同一值。更换后需重置所有人的两步验证 |
| `public_base_url` / `base_url` | `PUBLIC_BASE_URL` / `BASE_URL` | 是 | 对外 origin（`https://reel.example`）；SSR canonical/站点地图、通知里的链接 |
| `catalog_contact` | `CATALOG_CONTACT` | 抓取需要 | 爬虫 User-Agent 联系方式；空则目录任务 `config_error` |
| `log_hash_key` | `LOG_HASH_KEY` | 建议 | 日志中用户 id 的 HMAC 键；空则每次重启随机（哈希不可跨重启关联） |
| `dmm_api_id` / `dmm_affiliate_id` | `DMM_API_ID` / `DMM_AFFILIATE_ID` | 可选 | DMM/FANZA Affiliate API；affiliate id 须 990–999 结尾。只发往 `api.dmm.com`，日志/任务/入库前脱敏 |
| `theporndb_api_token` | `THEPORNDB_API_TOKEN` | 可选 | ThePornDB 个人 token，只发往 `api.theporndb.net` |
| `vapid_public_key` / `vapid_private_key` / `vapid_subject` | `VAPID_*` | 可选 | Web Push（`go run ./cmd/notifier -gen-vapid` 生成）；私钥只给 notifier |
| `smtp_host` / `smtp_port` / `smtp_user` / `smtp_password` / `mail_from` | `SMTP_*`、`MAIL_FROM` | 可选 | 邮件通知与告警邮件（STARTTLS；465 为隐式 TLS） |
| `alert_webhook_url`、`alert_warning_webhook_url`、`alert_email_to`、`alert_warning_email_to`、`alert_email_from`、`alert_slack_webhook_url`、`alert_telegram_bot_token`、`alert_telegram_chat_id` | `ALERT_*` | 生产必需至少一个 page 通道 | Alertmanager 接收器（`docs/PRODUCTION_OPERATIONS.md`「值班通知」）。全空时告警无人接收 |
| `web_api_proxy` | `WEB_API_PROXY` | 否 | 默认 true；Caddy 已分流 `/api/` 时可设 false |
| `s3_endpoint` / `s3_bucket` / `s3_access_key` / `s3_secret_key` / `s3_region` / `s3_public_base_url` | `S3_*` | 可选 | 目录图片存储（`docs/IMAGE_STORAGE.md`、`docs/DATA_RETENTION_AND_IMAGES.md`）；`S3_ENDPOINT` 为空时不存图。用自带 MinIO 时加 `-e storage_enabled=true`（compose profile `storage`，`S3_ENDPOINT=http://minio:9000`，不发布端口） |

非密钥的部署开关（`-e` 传入或写在 vault 里）：`monitoring_enabled`、`backup_cron_enabled`、`backup_keep_days`（默认 14）、`backup_cron_hour` / `backup_cron_minute`、`seo_check_enabled`（默认 true）、`git_branch`、`trusted_proxies`（`TRUSTED_PROXIES`，默认空；见 2.5）。

轮换：改 vault → 重新执行 deploy.yml（重新渲染 `.env`，`docker compose up -d` 只重建配置变化的服务）。`ADMIN_TOKEN` 轮换后同步更新仍使用它的脚本（2.7）；服务令牌轮换：后台新建同范围、新名称的令牌（如 `seo-check-2`）→ 改 vault → 重新部署 → 撤销旧令牌；`LOG_HASH_KEY` 轮换会断开新旧日志的用户哈希关联。

## 4. 发布与回滚

### 4.1 发布前

1. 目标提交的 CI（go / integration（含 `internal/acceptance` 全量验收）/ web）全绿。
2. 本地或 CI 上 `make restore-drill` 最近一次通过（§7）。
3. 读一遍这次包含的迁移（`git diff <当前生产提交>..<目标> -- migrations/`），按 §4.3 判断能否回滚。含破坏性变更时先手动备份（§6.2）。

### 4.2 发布

```bash
# 记录当前生产提交，回滚时用
ssh avxxx@<host> 'git -C /opt/avxxx rev-parse HEAD'
# 回滚保险：给当前镜像打上提交标签（构建会覆盖 :latest）
ssh avxxx@<host> 'cd /opt/avxxx && c=$(git rev-parse --short HEAD) && docker tag avxxx-app:latest avxxx-app:$c && docker tag avxxx-web:latest avxxx-web:$c'

ansible-playbook -i inventory.ini deploy.yml -e @secrets.vault.yml --ask-vault-pass \
  -e git_branch=<tag 或 commit，默认 main> -e monitoring_enabled=true -e backup_cron_enabled=true
```

`git_branch` 接受分支、tag 或完整 commit SHA；建议发布用 tag（`git tag v2026.10.16 && git push --tags`），这样 `.env` 之外的一切都由一个不可变引用决定。deploy.yml 末尾的健康检查失败时 playbook 失败，但新容器已经在运行——立刻做 §5 的检查，必要时按 §4.4 回滚。

### 4.3 迁移：只向前、幂等

- 迁移是 `migrations/NNN_name.sql`，嵌入二进制（`migrations.Schema`）。**每个 Go 进程启动时**（api、scheduler、scraper、audit、notifier 都会）在 `pg_advisory_xact_lock(842010)` 下按文件名顺序在一个事务里执行全部文件；第一个拿到锁的进程迁移，其余等待后执行一遍无变化的语句。
- 每条语句都必须可重复执行（`CREATE … IF NOT EXISTS`、`ADD COLUMN IF NOT EXISTS`、`ON CONFLICT DO NOTHING`、带条件的 `UPDATE`），集成测试会连续 Open 两次验证。
- **没有 down 迁移。** 含义：
  - 回滚代码不会回滚 schema。旧代码必须能在新 schema 上运行，所以迁移只做加法（新表、新列带默认值、新索引）；删列、改类型、改语义要分两次发布（先让代码不再依赖，下个版本再删）。
  - 回填数据的迁移（如 027 系列、028 作用域修正）在回滚后保留结果，再次发布时因幂等不会重复执行出错。
  - 真正需要撤销一个迁移时只能恢复备份（§6.2），会丢失备份之后的写入——所以破坏性迁移发布前必须先手动备份。
  - 迁移失败时事务回滚、进程退出（`WorkerDown` / `ApiHealthProbeFailing`），库保持迁移前状态；修复后重新发布即可。
  - 编号由各功能分支预留（如 031 留给图片存储），文件名排序决定顺序；不要改已发布迁移的内容，修正写成新文件。
- 用备份演练迁移：`make restore-drill` 让当前代码的 API 在恢复出的生产数据上启动，打印启动迁移改动了哪些表（§7）。

### 4.4 回滚

```bash
# 1) 代码回滚（schema 保持最新，见 4.3）
ansible-playbook -i inventory.ini deploy.yml -e @secrets.vault.yml --ask-vault-pass -e git_branch=<上一个生产提交或 tag>
# 1') 更快：不重新构建，直接用 4.2 保存的镜像
ssh avxxx@<host> 'cd /opt/avxxx && git checkout <上一个提交> && docker tag avxxx-app:<短提交> avxxx-app:latest && docker tag avxxx-web:<短提交> avxxx-web:latest && docker compose up -d api scheduler scraper audit notifier web'
# 2) 只有迁移造成数据损坏时：恢复发布前的备份（§6.2），再部署旧提交
```

回滚后同样执行 §5。清理旧镜像标签：`docker image ls avxxx-app` 保留最近 3 个，`docker image prune` 清理悬空层。

## 5. 发布后检查

在主机上依次执行（任何一步失败都先止血：回滚或停用相关服务）：

```bash
cd /opt/avxxx
docker compose ps                                        # 所有服务 Up，web healthy
API_URL=http://127.0.0.1:8080 DB_CONTAINER=avxxx-db-1 ./scripts/smoke.sh
curl -fsS http://127.0.0.1:3000/healthz && curl -fsSI https://<域名>/ | head -1
docker compose logs --since 10m api scheduler scraper audit notifier | grep -E '"level":"(ERROR|WARN)"' | head
set -a; . ./.env; set +a
docker compose run --rm --no-deps -e WEB_URL=http://web:3000 -e API_URL=http://api:8080 -e SEO_REPORT_TOKEN -e ADMIN_TOKEN web node scripts/seo-check.mjs | tail -5   # 收录健康分 ≥ 90（优先用服务令牌上报）
```

- **监控**：`ssh -L 9095:127.0.0.1:9095 -L 9096:127.0.0.1:9096 <host>` → Prometheus `/targets` 全部 up、`/alerts` 无新的 firing；`docker compose logs alertmanager | grep render-config` 显示 `receivers configured`。首次部署发一条测试告警（`docs/PRODUCTION_OPERATIONS.md`「发送测试告警」）。
- **任务队列**：后台「任务与来源」页或 `GET /api/admin/jobs`：没有新的 `failed` 洪峰；`GET /api/admin/sources` 中没有意外 `blocked_until`。
- **备份**：`ls -l /opt/avxxx/backups | tail -3`，最近一次 < 26 小时（`BackupStale` 同样会告警）。

## 6. 备份

### 6.1 日常备份（主机 cron）

`-e backup_cron_enabled=true` 为部署用户安装每日 cron（默认 03:17），执行：

```bash
cd /opt/avxxx && BACKUP_VIA_COMPOSE=1 OUTPUT_DIR=/opt/avxxx/backups BACKUP_KEEP_DAYS=14 \
  BACKUP_METRICS_DIR=/opt/avxxx/backups/metrics ./scripts/backup-db.sh >> /opt/avxxx/backups/backup.log 2>&1
```

- `pg_dump --format=custom --no-owner --no-acl` 在 db 容器内执行（版本永远一致，主机无需客户端），先写 `.partial` 再改名，文件 0600，目录 0700。
- 保留：本机保留 `backup_keep_days` 天（默认 14）。异地按「7 个日备份 + 4 个周备份 + 6 个月备份」保留（§6.3）。
- 监控：脚本写 `avxxx_backup_last_success_timestamp_seconds` / `_size_bytes` / `_duration_seconds`；`BackupStale`（> 26 h）、`BackupTooSmall`（< 1 MiB）、`BackupMetricMissing` 告警。
- 范围：只包含 PostgreSQL。目录图片在对象存储（自带 MinIO 的卷或外部 S3）里，不在 dump 中：外部 S3 开启版本控制，自带 MinIO 的数据按同样节奏单独拷贝到异地（如 `mc mirror`）。数据库与对象存储分别恢复时，以数据库为准，库里引用而存储里缺失的对象需按 `docs/IMAGE_STORAGE.md` 重新抓取。
- 手动备份（发布前、破坏性操作前）：`ssh avxxx@<host> 'cd /opt/avxxx && BACKUP_VIA_COMPOSE=1 OUTPUT_DIR=/opt/avxxx/backups ./scripts/backup-db.sh'`。

### 6.2 恢复（生产）

恢复会覆盖同名对象，必须在维护窗口执行：

```bash
cd /opt/avxxx
docker compose stop api scheduler scraper audit notifier web       # 停写入（db 保持运行）
BACKUP_VIA_COMPOSE=1 OUTPUT_DIR=/opt/avxxx/backups ./scripts/backup-db.sh   # 先给当前状态留底
docker compose exec -T db pg_restore --clean --if-exists --no-owner --no-acl \
  -U avxxx -d avxxx < /opt/avxxx/backups/avxxx-<时间戳>.dump
docker compose up -d api scheduler scraper audit notifier web     # 启动时自动补齐新迁移
API_URL=http://127.0.0.1:8080 ./scripts/smoke.sh                  # 然后做完整的 §5
```

主机装有匹配版本（17）的 PostgreSQL 客户端时也可用 `DATABASE_URL=… ./scripts/restore-db.sh <dump>`。

恢复后：

- 备份时间点之后的写入（用户行为、抓取结果）已丢失。目录数据会被后续增量抓取补回（重复抓取不产生重复实体，见全量验收）；检查 `crawl_checkpoints` 是否需要管理员请求一次 baseline。
- 如果恢复的是较旧的备份，按 §2.6 第 3 步复核来源条款后再恢复自动抓取；必要时先停用 scraper。
- 主机整体丢失时：§2.3 provision 新主机 → 更新 inventory → §2.4 deploy（会创建空库）→ 立即按上面的步骤恢复最新的异地备份。

### 6.3 异地拷贝

cron 只在本机留副本，必须另配异地拷贝（备份和数据库在同一块盘上时，磁盘故障会同时丢失两者）。推荐在部署用户的 crontab 里于备份后执行（示例，rclone 指向对象存储、启用服务端加密和对象版本 / 锁定）：

```cron
45 3 * * * rclone copy /opt/avxxx/backups remote:avxxx-backups/daily --include 'avxxx-*.dump' --max-age 26h >> /opt/avxxx/backups/offsite.log 2>&1
```

周备份 / 月备份由对象存储生命周期规则保留（或另设周日、每月 1 日的 `rclone copy` 到 `weekly/`、`monthly/` 前缀）。异地凭据只放在主机的 rclone 配置（0600），不进仓库和 `.env`。每季度从异地存储取回一个文件做一次恢复演练（§7），确认异地副本可读。

## 7. 恢复演练

`make restore-drill`（`scripts/restore-drill.sh`）证明备份能恢复成一个可用的系统，然后把所有临时资源删除：

1. 取 `BACKUP_DIR`（默认 `./backups`）中最新的 `avxxx-*.dump`，或 `BACKUP_FILE`；没有备份（或 `DRILL_SEED=1`）时，启动一次性 postgres:17，用 API 执行全部迁移并写入种子数据，再从中备份；
2. 在新的一次性 postgres:17 容器里 `pg_restore --exit-on-error`（客户端在容器内，无版本差异）；
3. 每张表的行数与 dump 内的行数逐一比较（种子模式下还与源库实时行数比较），不一致即失败；
4. 用当前代码启动 API（有 Go 时 `go build ./cmd/api`，否则 `avxxx-app:latest` 镜像，`DRILL_API=binary|image` 可强制），它会执行比备份更新的迁移；运行 `scripts/smoke.sh` 和目录读取检查，并列出启动迁移改动的表；
5. 删除它创建的容器、网络、进程和临时文件（`DRILL_KEEP=1` 保留以便排查）。

它从不读取或写入 `DATABASE_URL`，不碰 compose 的 `db` 或任何已有容器（全部资源命名为 `avxxx-drill-<pid>-<时间>-*`）。

```bash
make restore-drill                                    # 本地：无备份时自动种子
ssh avxxx@<host> 'cd /opt/avxxx && ./scripts/restore-drill.sh'   # 生产主机：演练最新的每日备份（image 模式）
BACKUP_FILE=/path/to/avxxx-20261015T031700Z.dump ./scripts/restore-drill.sh   # 指定文件（如从异地取回的）
```

参考耗时（Apple Silicon 笔记本）：种子模式约 9–12 秒（构建 3 s、种子 2 s、恢复 2 s、计数 < 1 s、API 启动 1 s、smoke 1 s）；一个 2 MB / 35 万行的 dump 在 image 模式约 4 秒。生产数据量增大后以实际输出为准，耗时显著增长时检查磁盘和 `pg_restore` 并行度。

**演练节奏**：

- 每月第一个工作日在生产主机上演练最新的每日备份，并把输出（`RESTORE DRILL PASSED in …`）贴进值班记录；
- 每季度用从异地存储取回的备份演练一次（§6.3）；
- 每次包含迁移的发布前，在一份最新的生产备份上演练一次，确认新迁移在真实数据上能执行（输出中的「tables changed by start-up migrations」就是这次迁移要改动的表）。

可以把月度演练做成部署用户的 cron（演练在一次性容器里运行，不影响线上服务，但会占用约 2× 数据库大小的磁盘和数分钟 CPU）：

```cron
30 4 1 * * cd /opt/avxxx && ./scripts/restore-drill.sh >> /opt/avxxx/backups/restore-drill.log 2>&1
```

## 8. 全量验收（每次发布由 CI 执行）

`make test-integration`（CI 的 integration 任务）包含 `internal/acceptance`，也可单独 `make acceptance`。它用 httptest 伪造 javdatabase、xcity、javguru、DMM（签名请求、回显凭据）、ThePornDB 式回退来源和 RSS，经生产路径（Enqueue → Claim → `crawlrun.Runner.Execute` → Finish）抓取到独立数据库后断言：

- 重复抓取（baseline、增量、第二次 baseline、乱序 / 大小写 / 空白不同的重新入库、改标题的无番号作品）不新增任何实体、标识符、别名、署名、标签、来源记录或 RSS 条目；未变化的页面不新增字段历史；
- 403、429、503/200 验证页、404/500、robots.txt 禁止 / 不可达、许可门禁、预算耗尽、无法解析 / 跳过 / 退化 / 空 / 截断页面、DMM API 错误都不删除或清空已有值、不丢失 `last_seen_at`、不把 checkpoint 推过失败批次，凭据不出现在任务错误和游标里；
- 人工覆盖、隐藏生日、扣留 / 下架、合并与拆分在多轮重新抓取后保持不变；第三方回退来源只附着、不创建实体、不覆盖更高层级、不单独开冲突；
- 遍历全部公开 GET 路由（`api.Patterns()`），响应中没有 raw_data、来源载荷标记、DMM 凭据、覆盖原因 / 操作人、隐藏生日或未发布作品。

## 9. 常用位置

| 内容 | 位置 |
| --- | --- |
| 应用目录 / `.env` | `/opt/avxxx`、`/opt/avxxx/.env`（0600） |
| 数据库卷 | docker 卷 `avxxx_pgdata`（`/var/lib/docker/volumes`） |
| 备份 / 日志 | `/opt/avxxx/backups/`（`backup.log`、`metrics/`、`restore-drill.log`） |
| SEO 检查日志 | `/opt/avxxx/seo-check.log` |
| Caddy | `/etc/caddy/Caddyfile`、`/var/log/caddy/` |
| 主机地址 | `deploy/ansible/inventory.ini` |
