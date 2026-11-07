-- 2.1.0: OIDC 单点登录(Casdoor)。
-- users 增加 email 与 OIDC 身份绑定列;空串表示未绑定,
-- partial unique index 保证同一 IdP 身份至多绑定一个本地账号。
ALTER TABLE users ADD COLUMN email TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN oidc_iss TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN oidc_sub TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX idx_users_oidc ON users(oidc_iss, oidc_sub) WHERE oidc_sub != '';
