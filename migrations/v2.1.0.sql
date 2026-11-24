-- 2.1.0: OIDC 单点登录(Casdoor)、增量配额缓存与会话哈希化。
-- 1. users 增加 email 与 OIDC 身份绑定列;空串表示未绑定,
--    partial unique index 保证同一 IdP 身份至多绑定一个本地账号。
ALTER TABLE users ADD COLUMN email TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN oidc_iss TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN oidc_sub TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX idx_users_oidc ON users(oidc_iss, oidc_sub) WHERE oidc_sub != '';

-- 2. 新增 source_usage 表维护存储源物理用量增量与校准时间戳，避免每次上传/配额查询全量遍历磁盘。
CREATE TABLE IF NOT EXISTS source_usage (
  storage_source_id INTEGER PRIMARY KEY REFERENCES storage_sources(id) ON DELETE CASCADE,
  usage_bytes INTEGER NOT NULL DEFAULT 0 CHECK(usage_bytes >= 0),
  calibrated_at DATETIME NOT NULL
);

-- 3. sessions 表仅保存 SHA-256(session_id) 哈希并移除冗余 csrf_token_hash 列；
--    升级时清空既有明文 session，强制重新登录。
CREATE TABLE sessions_v21 (
  session_id TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL,
  expires_at DATETIME NOT NULL,
  created_at DATETIME NOT NULL,
  last_seen_at DATETIME NOT NULL,
  user_agent TEXT,
  ip_address TEXT,
  FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
);

DROP TABLE sessions;
ALTER TABLE sessions_v21 RENAME TO sessions;
