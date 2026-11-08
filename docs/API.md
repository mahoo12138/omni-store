# API 约定与 2.0 目标接口

> 修订：2026-09-29 / R2。2.0 目标规范；未标为基线的内容不代表已实现。

现有 `/api/v1` 可继续使用。2.0 是产品 Major Version，不要求为了版本号机械创建 `/api/v2`。

如果现有 endpoint 语义发生冲突，可以破坏性修改，但前后端必须同一提交更新。

## 1. 通用

成功：

```json
{
  "data": {},
  "request_id": "req_xxx"
}
```

错误：

- stable error code；
- message；
- request_id。

## 2. Storage Source

Admin Source API 不再返回/接收：

```text
public_read_enabled
public_mount_path
image_bed_enabled
```

增加：

```text
webdav_enabled
s3_enabled
```

普通 User Source View 可返回：

```text
key
name
description
permission
webdav_enabled
s3_enabled
quota
```

## 3. Site Capability

当前接口：

```text
GET /api/v1/admin/capabilities
PUT /api/v1/admin/capabilities/{capability}
```

capability：

```text
public_drive
image_bed
```

Body：

```json
{
  "enabled": true,
  "storage_source_key": "src-..."
}
```

更新前：

- Source exists/enabled；
- root accessible；
- service namespace collision check（需要时）；
- audit。

静态 HTTP 不属于 Site Capability；使用各源的 `/api/v1/admin/sources/{key}/static-access/*` 接口。

## 4. Public Drive

固定：

```text
GET /public/*
GET /api/v1/public/files?path=...
```

不再接受 mount name。

后端自动解析当前 `public_drive` binding。

## 5. Image Bed

快速图床上传 API 不接受 Source 或目标目录选择；按路径管理的博客图片/视频使用普通文件上传，不走此接口。

后端解析当前 `image_bed` binding。

旧 image URL：

```text
/i/{image_id}.{ext}
```

保持。

## 6. Protocol

S3：

```text
bucket = storage_key
```

WebDAV：

```text
/dav/{storage_key}/...
```

若现有路由不同，可保留现有前缀，但 Source segment 必须是 key。

## 7. 仪表盘与运行维护

当前接口（管理员权限）：

```text
GET  /api/v1/admin/overview
GET  /api/v1/admin/operations/status
POST /api/v1/admin/sources/{key}/reconcile
POST /api/v1/admin/operations/cleanup
POST /api/v1/admin/operations/integrity-check
```

仪表盘只请求 `/admin/overview`，返回 `system`（连接配置）、`operations`（与 `/operations/status` 相同的完整健康快照）及 `anonymous_image_bed_on`。用户数读取 `operations.database.table_rows.users`，全部存储源在 `operations.sources`，含 `root_path`、协议开关、禁用状态、目录可访问性与用量。旧概览的用户/权限/审计明细和独立源摘要已移除。

维护动作沿用现有接口；完成后重新获取仪表盘快照。

## 8. Backup

建议：

```text
POST /api/v1/admin/backups
POST /api/v1/admin/restore/preflight
POST /api/v1/admin/restore
```

Restore 是高风险操作，必须有明确交互确认和服务状态约束。

## 9. 按源静态 HTTP 配置

以下管理接口要求超级管理员，写操作需要 CSRF。路径指定存储源，静态访问不进入 Site Capability API。

```http
GET    /api/v1/admin/sources/{key}/static-access/config
POST   /api/v1/admin/sources/{key}/static-access/config
PUT    /api/v1/admin/sources/{key}/static-access/config
DELETE /api/v1/admin/sources/{key}/static-access/config
POST   /api/v1/admin/sources/{key}/static-access/preflight
POST   /api/v1/admin/sources/{key}/static-access/rebind
```

GET 对未配置源返回默认关闭的视图，public_asset_id/base_url 为空。POST 创建并启用发布配置：

```json
{
  "publish_root": "blog-assets",
  "public_origin": "https://files.example.com",
  "cache_mode": "short",
  "cors_mode": "public",
  "allowed_origins": [],
  "confirm_publish_root": false
}
```

发布源根必须 confirm_publish_root=true，目录必须存在且符合安全规则。服务端生成 public_asset_id/base_url。PUT 修改 enabled、cache_mode、cors_mode、public_origin、allowed_origins，可携带 expected_revision；修改 source_key/publish_root 返回 409 STATIC_REBIND_REQUIRED，需使用 rebind。

rebind 指定目标 source_key、publish_root、mode=reset|relocate 和 expected_revision。relocate 需要 confirm_relocate，保留公开 ID；reset 生成新 ID。目标源已有配置返回 409，不覆盖。重绑保留启停状态，不隐式开启已关闭的协议。

DELETE 要求先关闭协议，并提供当前 expected_revision；只撤销公开身份，保留普通文件。源有静态配置时删除 Source 返回 409。

响应包括 source_key/source_name/source_disabled、publish_root、public_asset_id、base_url、cache/CORS 和 revision。公开响应不暴露这些配置。旧全站 /admin/static-assets/* 管理端点已移除，/assets/{id}/{path} 保持稳定。

## 10. 静态资源公开端点（2.0 目标）

```http
GET /assets/{public_asset_id}/{relative_path...}
HEAD /assets/{public_asset_id}/{relative_path...}
OPTIONS /assets/{public_asset_id}/{relative_path...}
```

无 Cookie/Token 要求；OPTIONS 仅用于配置允许的无凭据读取预检。不存在公开写入或目录列表端点。空相对路径/目录/`.omnistore`/源外/不允许类型返回真实 404，不跳登录或 SPA。媒体响应、条件请求、Range、缓存与 CORS 由 [公开 HTTP](design/public-content-http.md) 定义。

## 11. 博客上传复用当前接口

当前固定基线已定义 `POST /api/v1/sources/{key}/upload?path=...`，一次 multipart 请求一个文件，支持显式 relative_path。发布目录 blog-assets，文章路径 images/posts/a.webp 时，应上传到普通目标 `blog-assets/images/posts/a.webp`，而不是 `.omnistore/`。

不得将本段混同为新增匿名写入。Web Session 写入保留 CSRF；WebDAV/S3 保留各自凭据和源开关。只有逐目标路径 Policy 授权成功才写入。已登录但没有目标目录权限返回 403。

## 12. 通用内部路径错误

通用读取/浏览 `.omnistore` 或后代使用 404（REST FILE_NOT_FOUND；S3 GET NoSuchKey；HEAD 无体）。普通创建/移动/复制/Multipart 目标也不得碰内部范围，不创建临时文件或上传状态。详情见 [命名空间契约](design/managed-namespace.md)。

## 13. 快速图床匿名保留设置

图床设置增加 `anonymous_retention_days`：0 表示不自动过期。只有管理员可改，新的匿名上传固化自己的 expires_at；变更不会追溯旧图。上传结果在现有 PicGo 兼容返回之外增加可选 expires_at（不改变既有 URL 字段语义），网页明确显示。快速图床 Token 不可用作静态目录写入凭据。

### 公开站点服务状态

`GET /api/v1/system/status` 无需登录，除原有 `s3`、`webdav`、`file_preview`、`anonymous` 外，返回 `public_drive`、`image_bed`。各项均为 `{ enabled, status, hint }`；两项站点服务的 `enabled` 表示当前开关已启用、已绑定且存储源与根目录可用，而不是仅返回数据库开关。`status` 区分未绑定、已关闭、存储源已禁用、目录不可用与已启用；公开响应不包含源身份、真实路径或底层错误。

`anonymous.enabled` 同时依赖匿名上传开关与图床实际可用状态。

## 14. 账号偏好

```http
GET   /api/v1/me/preferences
PATCH /api/v1/me/preferences
```

需要登录 Session；PATCH 与其他写操作一样要求 `X-CSRF-Token`。请求体只接受 `theme`，取值为 `light`、`dark`、`system` 三档，其它值返回 400 `VALIDATION_ERROR`，未知字段同样拒绝。

返回 `data = { theme, updated_at }`。账号从未设置过偏好时 GET 返回 `{ "theme": "system" }` 且 `updated_at` 为空，读操作不建库记录。

偏好是**账号级**外观选择，只覆盖登录用户；匿名访客与冷启动阶段的取值由前端本地存储提供，见 [设计系统](DESIGN_SYSTEM.md) 的主题条款。

## 15. OIDC 单点登录（2.1.0）

协议细节、配置项与用户映射规则见 [SSO 特性文档](features/sso.md)。

```http
GET  /api/v1/auth/oidc/login[?mode=bind]     # 免鉴权；302 到 IdP 授权页（bind 需已登录）
GET  /api/v1/auth/oidc/callback              # 免鉴权；IdP 回调，成功 302 /app（bind 回 /app/settings?section=profile&sso_bound=1）
GET  /api/v1/admin/oidc                      # 管理员读取 SSO 配置（client_secret 不回传，仅 has_secret）
PUT  /api/v1/admin/oidc                      # 管理员保存配置；client_secret 留空表示保留原值
POST /api/v1/admin/oidc/test                 # 管理员对 issuer 做 discovery 连通性检查（可测未保存的表单值）
POST /api/v1/me/oidc/unbind                  # 登录用户自助解绑，body {"password"}；未设本地密码账号拒绝
POST /api/v1/admin/users/{id}/unbind-oidc    # 管理员强制解绑
```

`GET /api/v1/auth/status` 增加 `oidc_enabled`；`GET /api/v1/system/status` 增加 `sso: {enabled, status, hint}`。`GET /api/v1/auth/me` 的 `user` 增加 `email`、`sso_bound`（`oidc_iss`/`oidc_sub` 不出任何 API 响应）。

回调失败统一 302 `/login?sso_error=<code>`，code 取值：`oidc_disabled`、`oidc_not_configured`、`oidc_state_mismatch`、`oidc_provider_denied`、`oidc_exchange_failed`、`oidc_identity_unbound`、`oidc_identity_taken`、`oidc_username_invalid`、`oidc_username_missing`、`oidc_username_conflict`、`oidc_account_disabled`、`oidc_bind_requires_login`、`oidc_rate_limited`、`oidc_internal`。这些码不是 HTTP API 错误码，仅供前端登录页展示中文提示。
