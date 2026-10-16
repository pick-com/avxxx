# 设计稿画板中尚未实现的功能

更新：2026-10-16（#109 之后）。逐画板状态见 [DESIGN_ALIGNMENT.md](DESIGN_ALIGNMENT.md)，整体进度见 [ROADMAP_STATUS.md](ROADMAP_STATUS.md)。本页只列还没做的部分，每项写明原因和代码证据。证据由检查代码得出，不是照搬 PR 描述。

原因分类：**进行中**；**产品决策**（等产品定）；**外部输入**（等凭据、授权或法务）；**媒体**（需要自营转码或授权素材，现在的播放来源是第三方嵌入 / 外链）；**代码**（不依赖外部输入，可以直接做）。

## 进行中

| 画板 | 缺什么 | 原因 | 证据 |
|---|---|---|---|

## 产品决策

| 画板 | 缺什么 | 原因 | 证据 |
|---|---|---|---|
| `AdPlacements` `AdDepth` `MobileAds` `AdminAds` | 广告位、频控、激励广告、SSAI、品类屏蔽、广告后台、实验框架 | 是否上广告、在哪个阶段没定（PRD 第 1、16 节与第 21 节矛盾，DESIGN_ALIGNMENT 第 2 节 #5）；成人站还要选广告网络 | `migrations/` 没有 `ad_*` 表，Go 和 web 里没有广告或实验分组代码 |
| `Settings` | 广告偏好、屏蔽广告类别 | 同上，取决于广告方案 | `web/app/routes/settings.tsx:32` |
| `MobileSources` | 4K 片源的「会员」标签 | 会员不在本期（DESIGN_ALIGNMENT 第 2 节 #7）；没有会员逻辑 | 代码里没有会员 / 订阅实现（`internal/search/classify.go` 里的「会员」只是求助词分类） |

## 外部输入

| 画板 | 缺什么 | 原因 | 证据 |
|---|---|---|---|
| `Account` | Google / Apple 登录真实可用 | 代码已完成，需要 OIDC 客户端凭据；目前只对假 provider 测试过，未配置时按钮隐藏 | `internal/oidc/oidc.go`、`GET /api/auth/providers` |
| `AdminI18n` | 机器翻译 | 翻译服务未选定，也没有凭据；接口有意不实现 | `internal/i18n/i18n.go:295` |
| `LegalPage` | 法律文本定稿 | 现有文本是模板，需律师审阅后以新版本发布 | `docs/COMPLIANCE.md` 第 2 节 **[法务]** |
| `AdminPipeline` | DMM、ThePornDB、厂商目录、XSList、JAVLibrary 真实运行 | 需要凭据、VPN / 授权、真实保存的页面和条款审核 | `docs/ROADMAP_STATUS.md`「需要外部输入」 |

## 媒体

| 画板 | 缺什么 | 原因 | 证据 |
|---|---|---|---|
| `Actor` `MobileActor` | 照片集；主照片只有已存储的 portrait | 只能存许可来源的封面和演员主照片，图集不存（法务待定） | `web/app/routes/performer.tsx:128`；`docs/DATA_RETENTION_AND_IMAGES.md` 第 1 节 |
| `Video` `TechPlayback` | 进度条雪碧图预览、字幕轨 | 没有转码 / 雪碧图产出；play-config 的字幕为空。精细指针下用隐藏视频取帧代替预览 | `web/app/components/Player.tsx:75`、`web/app/lib/useFramePreview.ts` |
| `Video` `AdminVideo` | 章节截屏条，AdminVideo「截屏与章节」tab | 需要转码管线产出截屏 | `web/app/routes/release.tsx:166`、`web/app/routes/admin-video.tsx:35` |
| `Tonight` `MobileTonight` | 静音预告 | 没有预告字段；只有 play-config 给出浏览器可直接播放的文件时才自动播放 | `web/app/routes/tonight.tsx:33` |
| `Settings` | 跳过片头、字幕大小、配音 | 没有片头标注（只有基于曲线的建议）、字幕轨和配音轨 | `web/app/routes/settings.tsx:30-31` |
| `AdminAudit` | 规则 B 水印、D 字幕、F 截屏 | 没有自营媒体的抽帧、字幕表或截屏资产；规则仍为桩，运行记为 `not_applicable` | `internal/audit/runner.go:19-23` |

## 代码

原列的六项代码工作已实现（迁移 047）：AdminVideo「数据标签」tab（`web/app/components/AdminVideoCurves.tsx`）；AdminDashboard 播放 / 日活 / 时长 / 完播（`site_metrics` 任务、`site_daily_metrics`、`GET /api/admin/site-metrics`）；冷启动推荐用 Onboarding 兴趣、Tonight「想要 / 和谁看」作为软偏好（`internal/recs/prefs.go`）；AdminDedupe 对比摘要与视频三种处理（新片源 / 其他版本 / 不是重复，`release_versions`）；设置页默认片源（`user_settings.preferred_provider`）与清除搜索记录。仍未做的细节：

| 画板 | 缺什么 | 证据 |
|---|---|---|
| `Tonight` | 「和谁看 · 家人 / 自己」不影响推荐：目录没有能对应的字段，只映射了「朋友」和四种「想要」 | `internal/recs/prefs.go` `TonightPreferences` |
| `AdminDashboard` | 日活按 UTC 日；未同意分析 Cookie 的访客按会话计，会偏高 | `internal/sitemetrics/sitemetrics.go` |

## 有意不做或已从设计中去掉

- 青少年模式（`AgeGate` `Settings`）：成人站定位，不做。
- 演员国籍筛选（`NavActors`）：演员没有国籍数据，只有自由文本出生地（`web/app/routes/discover.tsx:552`）。
- 跨语言向量搜索、推荐向量（`Browse` `TechRec`）：按 DESIGN_ALIGNMENT 第 4 节分期，V1 用多语言标题、别名和规则 + 协同过滤，有埋点数据后再做向量。
- 演员库拼音首字母索引：只在 PRD 第 6.2 节，`NavActors` 画板没有（DESIGN_ALIGNMENT 第 2 节 #3）。
