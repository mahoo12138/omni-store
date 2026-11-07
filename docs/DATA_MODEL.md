# 2.0 数据模型

> 修订：2026-09-29 / R2。2.0 目标规范；未标为基线的内容不代表已实现。

本文 SQL 是 2.0 目标结构片段，不是可以对生产库直接执行的完整迁移；Agent 必须结合固定基线实现 `v2.0.0.sql`。代码/迁移记录描述实现事实，本文描述待实现目标。

已发布：

```text
v1.0.0.sql
v1.1.0.sql
v1.1.1.sql
```

2.0 新增：

```text
v2.0.0.sql
```

2.1 新增：

```text
v2.1.0.sql
```

## 1. storage_sources

2.0 目标字段：

```text
id
key
name
description
root_path
is_disabled
webdav_enabled
s3_enabled
quota_bytes
created_at
updated_at
```

删除：

```text
public_read_enabled
public_mount_path
image_bed_enabled
```

建议新 Source：

```text
webdav_enabled = false
s3_enabled = false
```

协议默认显式开启。

## 2. site_capability_bindings

```sql
CREATE TABLE site_capability_bindings (
  capability TEXT PRIMARY KEY,
  storage_source_id INTEGER,
  is_enabled BOOLEAN NOT NULL DEFAULT 0,
  updated_at DATETIME NOT NULL,
  FOREIGN KEY(storage_source_id) REFERENCES storage_sources(id) ON DELETE RESTRICT,
  CHECK(capability IN ('public_drive', 'image_bed')),
  CHECK(is_enabled = 0 OR storage_source_id IS NOT NULL)
);
```

一个 capability 一行。

## 3. 删除历史模型

2.0 删除：

```text
public_mount_redirects
user_preferences.default_image_bed_storage_source_id
```

`user_preferences` 保留为账号级偏好表，2.0 只有一个有效字段：

```sql
CREATE TABLE user_preferences (
  user_id INTEGER PRIMARY KEY,
  theme TEXT NOT NULL DEFAULT 'system' CHECK(theme IN ('light', 'dark', 'system')),
  updated_at DATETIME NOT NULL,
  FOREIGN KEY(user_id) REFERENCES users(id)
);
```

`theme` 由 `GET/PATCH /api/v1/me/preferences` 读写；1.1.x 升级上来的账号没有该列，迁移后落到默认值 `system`，账号删除时偏好行一并删除。

## 4. images

保留现有：

```text
storage_source_id
relative_path
```

旧图片不迁移物理文件。

2.0 新上传统一使用当前 Image Bed binding，并写入：

```text
.omnistore/image-bed/...
```

images 仍可对应 legacy path。新图床台账标记 `resource_scope=image_bed`；已存在普通路径旧图不自动移动或改归属，不能把所有登录用户图片推断成博客静态资源。

## 5. file_records

普通网盘、静态资源原件与需要计费的图床文件继续使用，但必须分开 `resource_scope`。静态资源是普通文件，不另建一行副本；图床 images 与 file_records 不重复累加用量。

## 6. system_settings

继续保存：

- instance name；
- anonymous image-bed setting；
- 其他小型实例设置。

Capability -> Source 关系不得塞进 JSON/system_settings，使用外键表。

## 6.1 users（2.1.0 追加列）

```text
email       TEXT NOT NULL DEFAULT ''
oidc_iss    TEXT NOT NULL DEFAULT ''
oidc_sub    TEXT NOT NULL DEFAULT ''
```

`oidc_sub` 空串表示未绑定（沿用本项目空串哨兵风格）；partial unique index
`idx_users_oidc(oidc_iss, oidc_sub) WHERE oidc_sub != ''` 保证同一 IdP 身份至多绑定
一个本地账号。SSO 建号（JIT）账号 `password_hash` 为空串，天然不能密码登录。
API 序列化上 `oidc_iss`/`oidc_sub` 不出响应，仅暴露 `email` 与派生的 `sso_bound`。

## 6.2 system_settings 新增键（2.1.0）

- `oidc_config`：OIDC 单点登录配置 JSON（enabled/issuer/client_id/client_secret/scopes/auto_link_username/auto_create_users），见 [SSO 特性文档](features/sso.md)。

## 7. 按源静态 HTTP 配置

`source_static_access` 每个源至多一行，替代全站静态 binding 和单例 settings。各源独立维护公开身份、版本、缓存和 CORS 策略。

```sql
CREATE TABLE source_static_access (
  storage_source_id INTEGER PRIMARY KEY REFERENCES storage_sources(id) ON DELETE RESTRICT,
  enabled BOOLEAN NOT NULL DEFAULT 0,
  publish_root TEXT NOT NULL,
  public_asset_id TEXT NOT NULL UNIQUE,
  public_origin TEXT NOT NULL DEFAULT '',
  cache_mode TEXT NOT NULL DEFAULT 'short' CHECK(cache_mode IN ('short', 'no-cache', 'long')),
  cors_mode TEXT NOT NULL DEFAULT 'public' CHECK(cors_mode IN ('none', 'public', 'allowlist')),
  revision INTEGER NOT NULL DEFAULT 1,
  updated_at DATETIME NOT NULL
);

CREATE TABLE source_static_origins (
  storage_source_id INTEGER NOT NULL REFERENCES source_static_access(storage_source_id) ON DELETE CASCADE ON UPDATE CASCADE,
  origin TEXT NOT NULL,
  created_at DATETIME NOT NULL,
  PRIMARY KEY (storage_source_id, origin)
);

```

v2.0.0 不自动发布任何源，也不移动文件：旧的 public_read_enabled/public_mount_path 只用于收敛公开网盘绑定，不能推断发布范围。白名单外键随跨源 relocate 迁移；已有配置的目标源不能被覆盖。移除配置必须先关闭协议，只撤销公开身份，不删除文件；源有静态配置时禁止删除。

无需逐文件发布记录，公开读取使用普通源路径，不依赖 images 台账或 Reconcile。

## 8. 普通台账 / 图床台账隔离

目标 migration 增加：

```sql
ALTER TABLE file_records ADD COLUMN resource_scope TEXT NOT NULL DEFAULT 'file'
  CHECK(resource_scope IN ('file', 'image_bed'));

CREATE INDEX idx_file_records_scope_source
  ON file_records(resource_scope, storage_source_id, record_status);
```

同步修改所有 FTS 触发器和初始重建查询，只纳入 `resource_scope='file' AND record_status='active'` 的允许路径；对旧保留路径也过滤。普通 Reconcile 的 upsert 和 missing 删除同样限定 file scope。用户配额可以聚合 file 和 image_bed，但不能再重复加 images.size。

新图床原件写入 image_bed scope。旧 images 若仍位于普通目录，其 legacy file 行保持原访问语义，重构时单独标识来源，不执行路径猜测搬迁。

## 9. 匿名图片保留期

```sql
ALTER TABLE images ADD COLUMN expires_at DATETIME;
CREATE INDEX idx_images_expiry ON images(expires_at)
  WHERE expires_at IS NOT NULL;
```

旧图 expires_at 保持 NULL。匿名保留天数是站点设置，创建新图时固化绝对 expires_at；改变默认值不追溯修改历史记录。只有图床服务按明确图记录撤下/清理，不把静态目录加入 TTL 清理任务。

## 10. 托管根登记

```sql
CREATE TABLE source_managed_roots (
  storage_source_id INTEGER PRIMARY KEY,
  ownership_nonce TEXT NOT NULL UNIQUE,
  namespace_version INTEGER NOT NULL DEFAULT 1 CHECK(namespace_version = 1),
  created_at DATETIME NOT NULL,
  FOREIGN KEY(storage_source_id) REFERENCES storage_sources(id) ON DELETE RESTRICT
);
```

使用实例持久标识、数据库登记与磁盘 marker 配对。该登记不是图片 payload 的删除清单；GC 仍需具体对象及日志证明归属。备份/恢复保留登记，找不到匹配时报告 blocked，不自动接管目录。
