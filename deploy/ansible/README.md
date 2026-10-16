# Ansible 部署

部署方式参考 `books/deploy/ansible`：provision 负责主机依赖和用户，deploy 负责拉取代码、生成环境变量、启动 PostgreSQL、API、scheduler 和 scraper。

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

可观测性（`docs/PRODUCTION_OPERATIONS.md`「日志、指标与告警」）：传入 `-e log_hash_key=<随机长字符串>` 让日志中的用户哈希在重启后保持稳定；传入 `-e monitoring_enabled=true` 额外启动 compose `monitoring` profile 中的 Prometheus（只监听 `127.0.0.1:9095`，通过 `ssh -L 9095:127.0.0.1:9095` 访问）。各服务的 `/metrics` 只在 compose 网络内可达，不对外发布端口。
