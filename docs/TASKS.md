# OmniStore 2.0 Tasks

> 修订：2026-09-29 / R2。2.0 目标规范；未标为基线的内容不代表已实现。

本文件是编码清单。状态以代码和测试为准。

2026-10-18 状态说明：早期未勾选项尚未逐项重做验收，不等同于代码完全不存在；请先读 [STATUS](STATUS.md) 中的实现记录。下方完整产品目标继续保留，本轮仅更新有直接证据的恢复与兼容子集。

## P0 — Breaking Architecture

- [ ] 创建 `migrations/v2.0.0.sql`
- [ ] `site_capability_bindings`
- [ ] Storage Source 移除 public 字段
- [ ] Storage Source 移除 image-bed 字段
- [ ] Storage Source 新增 `s3_enabled`
- [ ] 删除 `public_mount_redirects`
- [ ] 删除 Image Bed Source preference
- [ ] Service Capability service/repository
- [ ] Admin Site Capability API
- [ ] Admin Site Capability UI
- [ ] Source deletion binding guard
- [ ] `.omnistore` reserved namespace
- [ ] Generic REST/WebDAV/S3/Public/Search 隐藏/拒绝 service namespace
- [ ] Storage physical quota 仍计算 service payload

## P0 — Public Drive

- [ ] 全站单 Source
- [ ] `/public` 固定入口
- [ ] 删除 mount path 概念
- [ ] 删除 redirect alias
- [ ] Anonymous read security
- [ ] service namespace exclusion
- [ ] E2E

## P0 — Image Bed

- [ ] 全站单 Source
- [ ] 登录用户不再选 Source
- [ ] Anonymous 同一绑定
- [ ] 旧图片仍可读取
- [ ] 新图片使用 service namespace
- [ ] Image Bed quota/ownership 语义测试
- [ ] PicGo 回归

## P0 — Protocol Scope

- [ ] WebDAV per-source
- [ ] S3 per-source
- [ ] S3 bucket = storage_key
- [ ] Bucket list 只返回启用且有权限 Source
- [ ] WebDAV 路径只允许启用 Source
- [ ] Source rename protocol stable
- [ ] UI 提供复制 endpoint/bucket
- [ ] protocol E2E

## P1 — Operation Center

- [ ] System status
- [ ] Source health
- [ ] Site Capability health
- [ ] protocol status
- [ ] DB health
- [ ] quota summary
- [ ] trash/cache/multipart
- [ ] recent failed audit
- [ ] reconcile
- [ ] cleanup
- [ ] integrity check

## P1 — Backup & Restore

- [ ] backup manifest v1
- [ ] DB consistent snapshot
- [ ] config
- [ ] keys
- [ ] checksum
- [ ] restore
- [ ] source rebind
- [ ] capability rebind validation
- [ ] reconcile
- [ ] restore report
- [ ] old backup compatibility

## P1 — Compatibility

- [ ] Windows WebDAV
- [ ] Finder WebDAV
- [ ] rclone WebDAV
- [ ] Cyberduck
- [ ] AWS CLI
- [ ] rclone S3
- [ ] s3cmd
- [ ] MinIO Client
- [ ] PicGo

## Release

- [ ] clean migration from 1.1.x
- [ ] fresh install
- [x] E2E：现有 Chromium 套件 67 项通过（2026-10-18）；完整产品/客户端目标仍按各条目验收
- [x] race：`go test -race -count=1 ./...`（2026-10-18）
- [x] crashcheck：现有场景各十轮（2026-10-18）
- [ ] backup restore
- [ ] real client matrix
- [ ] `v2.0.0-rc.1`

## P0 — 本次补充：命名空间隔离（NS）

- [ ] NS-01 所有新托管路径统一 `.omnistore/`，保留旧 upload/copy 临时前缀规则
- [ ] NS-02 按完整路径段检查，query/body/Destination/object key/relative_path 全入口接入
- [ ] NS-03 通用 GET/HEAD/PROPFIND 返回真实 404；SPA fallback 不吞状态
- [ ] NS-04 递归 ZIP/Share/Copy/Move/Delete 不泄漏或破坏托管子树
- [ ] NS-05 源物理用量包含内部目录，普通 Reconcile 不误删托管台账
- [ ] NS-06 未登记同名目录不接管、不删除；Source 历史引用删除保护
- [ ] NS-07 FTS、最近、收藏既有行的隐藏及 scope 分离

## P0 — 本次补充：快速图床（IMG）

- [ ] IMG-01 去除将“登录用户图床”当作路径型博客资源服务的混淆
- [ ] IMG-02 匿名默认关闭，登录/匿名共享唯一当前绑定
- [ ] IMG-03 新图 `.omnistore/image-bed/`；旧 `/i/` ID 不重新映射到新根
- [ ] IMG-04 匿名保留策略独立、默认不自动过期，上传前/结果明确 expires_at
- [ ] IMG-05 图床台账 resource_scope、用户计费不重复、普通扫描不误删
- [ ] IMG-06 PicGo 与个人历史、撤下/删除回归

## P0 — 本次补充：静态资源（AST）

- [x] AST-01 静态 HTTP 改为按源配置 + source_static_access/origins/revision；v2.0.0 迁移不自动发布旧源
- [x] AST-02 管理配置/预检/发布风险确认/发布根与类型策略
- [x] AST-03 普通文件映射；无 images 记录和无 Reconcile 先决条件
- [x] AST-04 GET/HEAD /assets/{id}/{path}，目录与内部路径 404
- [x] AST-05 复用普通上传/文件夹/覆盖以及 WebDAV/S3，逐路径 Policy 检查
- [x] AST-06 short/no-cache/long，验证器与原文件变化一致，404 no-store
- [x] AST-07 视频 Range/If-Range/416/HEAD，流式不全量缓冲
- [x] AST-08 无凭据 CORS/CORP 逐路由，后台边界不改变
- [x] AST-09 baseUrl 编码、可信 origin、无凭据博客示例
- [x] AST-10 reset/relocate、防并发修改、旧 ID 不静默指向无关内容
- [x] AST-11 关闭公开盘但静态资源可用，同源复用不公开内部内容
- [x] AST-12 备份含配置/发布ID但不含原件，恢复后显式核验才公开
- [ ] AST-13 双 origin 浏览器图片/视频/Canvas/fetch 与非法类型端到端验收
  - 已完成 Chromium 图片/Canvas/fetch/WebM 播放与 seek、非法类型和启停；MP4、其他浏览器与生产代理/CDN 未验收，见 [实测矩阵](design/compatibility-testing.md)。

NS/IMG/AST 任务全部属于 2.0，不挤掉 Operation Center、Backup/Restore 和协议兼容的既定范围。以下 checklist 是待执行清单，不是本次已通过的产品测试结果。

## 2026-10-18 — 可靠性加固

- [x] 浏览器门禁使用独立临时数据、动态端口、失败留证据与中断进程清理

## 2026-11-24 — 代码审查修复与架构加固

- [x] 主 HTTP Server 超时配置、流式滚动 Deadline、`statusRecorder` `Unwrap`/`ReadFrom` 零拷贝与 `WithRecover` 完善
- [x] 两阶段无锁上传（普通文件与图床）+ 路径锁每等待者 channel / `context` 取消 + 下载打开后立即释放读锁
- [x] 增量配额表 `source_usage`（`v2.1.0`）+ 写事务原子维护 + 后台每 15 分钟无锁校准
- [x] SQLite WAL 读写分池（`db.Pool`）、热路径写库节流（`last_seen`/`last_used`/`recent_files`）与分批 Reconcile（500 行/批）
- [x] Session ID SHA-256 哈希落库并移除冗余 `csrf_token_hash`（`v2.1.0`）；数据库迁移降级保护与外键检查修复
- [x] 迁移核心文件读写/删除/重命名至 `os.Root`（`security.SourceRoot`），消除软链接 TOCTOU 并补齐注入式竞态测试
- [x] 登录限流抗驱逐加固、统一 `security.IsInternalSegment` 与大小写不敏感源锁键归一、S3 SigV4 `EscapedPath`/原始字节编码修正及流式签名文档说明
- [x] `internal/app` 统一依赖装配/恢复编排/后台任务生命周期、`lifecycle.Manager` 实例注入、`internal/journal` 共享原语沉淀、清除 HTTP 层业务 SQL（含事务内原子迁移 `recent_files`/`favorites`）
- [x] 前端：`api/client.ts` 安全 JSON/错误解析、自研 React 19 `ImageLightbox` 替代 `react-viewer`、清理旧迁移脚本与 `.tsbuildinfo` 忽略规则、拆分 `FileManager` / `ProfileSection` / `AppHome` 大组件
