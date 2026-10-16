# 高清图片存储方案

图片二进制不放 PostgreSQL，数据库只保存 `media_assets` 元数据；原图和派生图放 S3/MinIO 兼容对象存储。

## 对象键

```text
{entity_type}/{entity_id}/{asset_type}/{sha256}.{ext}
```

同一实体、类型和 SHA-256 只保留一份，避免跨来源重复下载。`source_url` 保存合法来源地址，`retained_until` 支持生命周期清理。

## 建议策略

- 原图：长期保存，限制为授权来源和允许的高清尺寸。
- 缩略图：由原图重新生成，丢失时允许重建。
- 失败下载：只保留任务错误，不写入空资产。
- 下载前检查 Content-Type、最大字节数和图片尺寸。
- 图片 hash 在写入对象存储前计算，避免重复上传。
- 生产环境使用私有 bucket，通过签名 URL 或 CDN 分发。

## 容量估算

第一阶段按 50 万作品、每部 5 张、平均 2 MB 原图估算约 5 TB 原图；缩略图和备份后建议预留 15–25 TB。对象存储和数据库应分离扩容。

## 实现

策略和保留期见 [DATA_RETENTION_AND_IMAGES.md](DATA_RETENTION_AND_IMAGES.md)。

| 部分 | 位置 |
| --- | --- |
| 对象存储客户端（SigV4，put/get/delete/list/presign，私有 bucket） | `internal/objstore` |
| 来源/资产许可判断、校验与派生图、下载任务、清理任务、URL 生成 | `internal/images` |
| 表结构：`sources.images_allowed`、`media_assets.variant/derived_from/source_id`、`media_fetch_attempts`、`media_blocks` | `migrations/031_media_storage.sql` |
| 任务：`media_fetch`、`media_purge`（`cmd/scheduler` 每小时入队，`cmd/audit` 执行；`audit -media-fetch` / `-media-purge` 手动运行） | `cmd/audit`、`cmd/scheduler` |
| 公开字段 `cover` / `portrait`（`{url,width,height,srcset}`）和 `GET /api/images/{key}` | `internal/api/images.go` |
| 管理：`GET /api/admin/media`、`POST /api/admin/media/{id}/suppress|restore`、`GET /api/admin/media/sources`、`PUT /api/admin/sources/{id}/images`、`POST /api/admin/media/jobs` | 同上 |
| 本地 MinIO：`docker compose --profile storage up -d minio`，`S3_ENDPOINT=http://minio:9000` | `docker-compose.yml` |

派生图键为 `{entity_type}/{entity_id}/{asset_type}/{sha256}.{w320|w720}.jpg`，sha256 与原图相同（下载字节的哈希）。原图超过 1600 px 时按比例缩小后以 JPEG 存储，键仍用下载字节的 sha256。
