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

可观测性（`docs/PRODUCTION_OPERATIONS.md`「日志、指标与告警」）：传入 `-e log_hash_key=<随机长字符串>` 让日志中的用户哈希在重启后保持稳定；传入 `-e monitoring_enabled=true` 额外启动 compose `monitoring` profile 中的 Prometheus（只监听 `127.0.0.1:9095`，通过 `ssh -L 9095:127.0.0.1:9095` 访问）。各服务的 `/metrics` 只在 compose 网络内可达，不对外发布端口。
