# OIDC 单点登录（Casdoor）

> 修订：2026-11-07。2.1.0 功能基线。

OmniStore 支持以 OIDC 授权码流程接入外部身份提供方（首测 Casdoor），实现单点登录。协议交互由后端驱动（302 重定向链），前端不承接回调；登录态复用既有服务端 Session + CSRF 体系，`requireAuth`/`requireAdmin` 对 SSO 用户零差异生效。

## 流程

```text
登录页「使用 SSO 登录」
-> GET /api/v1/auth/oidc/login（写一次性 flow cookie：state+nonce+mode，10 分钟有效）
-> 302 IdP 授权页
-> 302 GET /api/v1/auth/oidc/callback?code&state
-> 验 state（与 flow cookie 恒定时间比对）→ 授权码交换 → ID token 验签（RS256/JWKS/aud/exp/iss）→ nonce 比对
-> 用户映射（见下）
-> sessions.Create + Session Cookie → 302 /app
失败统一 302 /login?sso_error=<code>，登录页展示中文横幅
```

- 回调地址由 `server.public_url`（`OMNISTORE_PUBLIC_URL`）派生：`{public_url}/api/v1/auth/oidc/callback`，与分享/图床公链同一事实来源。反代部署必须正确设置该值。
- 管理页展示该回调地址供复制到 Casdoor 的 Redirect URLs，并提供「测试连接」（对 issuer 做 discovery 连通性检查，可在保存前验证表单值）。

## 配置

存 `system_settings`（key=`oidc_config`，JSON），管理后台「系统设置 → 站点配置 → 认证」热配置，无需重启：

- `enabled`：总开关；登录页入口与 `/api/v1/auth/status` 的 `oidc_enabled` 随之变化。
- `issuer` / `client_id` / `client_secret`：`client_secret` 只写不读（响应仅 `has_secret`；更新时留空表示保留原值）。
- `scopes`：默认 `openid profile email`，始终强制包含 `openid`。
- `auto_link_username`：按用户名自动绑定。仅对未绑定的 `role=user` 本地账号生效，**绝不允许自动绑定 super_admin**，防止 IdP 侧同名账号提权。
- `auto_link_username` 与 `auto_create_users` 默认关闭（安全默认）。
- `auto_create_users`（JIT）：无法绑定的 IdP 用户首次登录自动创建本地账号：角色固定 `user`、`password_hash=''`（天然无法密码登录）、email/显示名取自 ID token claims。

issuer 允许 http/https（自托管 Casdoor 常为内网 http）；JWKS 与 discovery 由 go-oidc 缓存，配置变更（指纹：issuer+client+secret+scopes）后重建客户端。

## 登录名解析

登录名一律取标准 OIDC 的 `preferred_username` 声明，不做任何按 IdP 的猜测回退：
自动绑定与 JIT 都以它为登录名，显示名取 `name`。IdP 不返回该声明时显式失败
`oidc_username_missing`（提示管理员检查 IdP 配置），已有绑定不受影响（按
`(oidc_iss, oidc_sub)` 匹配登录，不依赖该声明）。

> Casdoor 系注意：默认 tokenFormat=JWT（legacy）的 ID token 不含 `preferred_username`，
> 应用需切换到标准令牌格式（如 masdoor 的 `JWT-Standard`）。

## 用户映射优先级

1. `(oidc_iss, oidc_sub)` 精确匹配已绑定用户 → 登录；
2. `auto_link_username` 开启且 `preferred_username` 匹配未绑定的普通账号 → 绑定并登录；
3. `auto_create_users` 开启 → 清洗用户名（非法字符替换为 `_`、截断 32 位、去首尾 `_`/`-`）后建号；用户名冲突返回 `oidc_username_conflict`，声明缺失返回 `oidc_username_missing`；
4. 均不满足 → `oidc_identity_unbound`。

被禁用账号即使已绑定也拒绝（`oidc_account_disabled`）。

## 绑定与解绑

- 已登录用户可在「账户设置 → 个人账户」发起绑定：`GET /api/v1/auth/oidc/login?mode=bind`（要求有效 Session），回调绑定到当前用户（幂等），成功回 `/app/settings?section=profile&sso_bound=1`。
- 自助解绑 `POST /api/v1/me/oidc/unbind` 需校验当前密码（防会话被劫持后恶意解绑）；**未设置本地密码的 SSO 建号账号拒绝解绑**（否则会把自己锁在门外），需先由管理员经 CLI `admin reset-password` 设置密码。
- 管理员强制解绑 `POST /api/v1/admin/users/{id}/unbind-oidc`。

## 密码边界

- SSO 建号账号无密码：密码登录必败（bcrypt dummy-hash 等成本比较，不泄露账号存在性）；`POST /api/v1/me/password` 显式提示「请在身份提供方修改密码」。
- 本地密码账号绑定 SSO 后仍可密码登录（并存模式）。
- SSO 用户照常生成/使用 WebDAV Token、图床 Token、S3 凭据。

## 安全与运维

- 登录限流：回调与密码登录共享 IP 滑动窗口（`LoginLimiter`）；IdP 拒绝（用户取消）不消耗额度，交换/验签失败消耗。限流触发 302 `oidc_rate_limited`。
- 审计：`oidc_login_success` / `oidc_login_failed` / `oidc_bind_success` / `oidc_bind_failed` / `oidc_unbind` / `update_oidc_config` / `unbind_oidc`。
- flow cookie：HttpOnly + SameSite=Lax + 窄 Path（`/api/v1/auth/oidc`）+ 10 分钟，一次性消费。
- `oidc_iss`/`oidc_sub` 不出 API 响应（`json:"-"`），仅暴露 `email` 与 `sso_bound`。
- 非目标：IdP 侧单点登出（登出仅清本地 Session）、多 IdP、SCIM、组/角色同步（SSO 用户提权走管理员后台）。

## 迁移

`v2.1.0.sql`：`users` 增加 `email`、`oidc_iss`、`oidc_sub`（空串=未绑定），partial unique index `idx_users_oidc(oidc_iss, oidc_sub) WHERE oidc_sub != ''` 保证同一 IdP 身份至多绑定一个账号。E2E：`web/e2e/oidc-sso.spec.ts`（spec 内嵌 fake IdP，不依赖真实 Casdoor 与固定端口）。
