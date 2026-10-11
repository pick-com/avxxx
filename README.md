# avxxx

参考 books 的模块结构搭建的信息采集与展示项目：Go API + Go 爬虫 Worker + React WebApp，共享 PostgreSQL。当前提供 RSS/Atom 采集适配器，具体业务及网站待确认。

## 需求文档

产品范围、模块职责、开发阶段与验收标准见 [需求文档](docs/REQUIREMENTS.md)。

架构、数据库、任务租约、接口和部署设计见 [技术文档](docs/TECHNICAL_DESIGN.md)。

## 模块

| 模块 | 目录 | 职责 |
|---|---|---|
| 用户 WebApp | web/ | 浏览信息、标题搜索、跳转原始来源 |
| 数据管理界面 | web/ | 管理来源、创建任务、查看成功和失败状态 |
| API | cmd/api/ | 对外查询、管理员鉴权和管理接口 |
| 爬虫 | cmd/scraper/、internal/scraper/ | 拉取任务、限时采集、RSS/Atom 解析 |
| 数据模块 | internal/store/、internal/model/ | 统一数据库访问、URL 去重、原子任务领取和结果入库 |
| 数据库迁移 | migrations/ | 内嵌初始化 SQL，启动时加锁应用 |
| 开发基础设施 | docker-compose.yml、Makefile | 本地数据库和构建命令 |

数据流：管理员创建来源 → 数据模块写入任务 → Worker 领取任务 → 采集解析 → 数据模块保存信息和任务状态 → API → 用户 WebApp。爬虫与 API 无需相互调用。多 Worker 通过 SKIP LOCKED 避免领取同一个任务。

## 本地运行

```sh
cp .env.example .env
# 将 ADMIN_TOKEN 改成自己的随机密钥
make db-up
cd web && npm install && cd ..
make api
# 分别在另外两个终端运行
make scraper
make web
```

打开 http://127.0.0.1:5173，在数据管理输入 .env 中的 ADMIN_TOKEN，添加 RSS/Atom URL，创建采集任务。刷新任务状态后返回浏览信息。管理密钥仅保存在页面内存中；公开查询接口无需密钥。前端通过 Vite 代理调用本地 API。

```sh
make test
make build
```

## 第一版边界与扩展

- 本版按需手动采集，没有自动调度或自动重试。失败后可重新创建任务。
- Worker 异常退出可能遗留 running 任务；确认对应 Worker 已停止后，将该任务置为 failed 再重新创建。后续应加入租约、心跳和超时回收。
- 来源链接去重；列表最多返回最近 100 条，搜索按标题匹配。后续增加分页、标签、详情及全文索引。
- 采集摘要在 React 中作为文本展示，原始 HTML 不执行。
- 数据模块现在负责来源、任务及内容；来源禁用/删除、数据编辑、用户账号与审计尚未实现。
- 后续网站适配器应将网络抓取与解析分离，并增加限速、退避重试及来源规则。当前 RSS 客户端限制响应 5 MiB、超时 30 秒。
- 开发服务默认仅监听本机。部署前需要 HTTPS、独立数据库凭据、管理用户鉴权、备份及来源 URL 网络访问限制；管理员应仅添加可信来源。
- SQL 初始化采用幂等模式；新增版本迁移时应切换到 books 的 goose 版本管理方式。

books 的邮件、通知、搜索索引、代理出口等属于可选业务模块，未复制其站点规则、部署配置或账户数据。
