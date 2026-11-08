import { apiFetch } from './client'

// OIDC 单点登录（Casdoor 等 IdP）前端 API（2.1.0）。

export interface SsoConfig {
  enabled: boolean
  issuer: string
  client_id: string
  scopes: string[]
  auto_link_username: boolean
  auto_create_users: boolean
  /** client_secret 只写不读，仅返回是否已设置 */
  has_secret: boolean
  /** 需在 IdP 侧登记的回调地址，由实例对外地址派生 */
  redirect_uri: string
}

export interface SsoConfigInput {
  enabled: boolean
  issuer: string
  client_id: string
  /** 留空表示保留已保存的密钥 */
  client_secret?: string
  scopes?: string[]
  auto_link_username: boolean
  auto_create_users: boolean
}

export interface SsoProbeResult {
  ok: boolean
  error?: string
  authorization_endpoint?: string
  token_endpoint?: string
}

export async function adminGetSso(): Promise<SsoConfig> {
  return apiFetch('/api/v1/admin/oidc')
}

export async function adminSetSso(input: SsoConfigInput): Promise<SsoConfig> {
  return apiFetch('/api/v1/admin/oidc', { method: 'PUT', body: JSON.stringify(input) })
}

export async function adminTestSso(issuer: string): Promise<SsoProbeResult> {
  return apiFetch('/api/v1/admin/oidc/test', { method: 'POST', body: JSON.stringify({ issuer }) })
}

export async function unbindMySso(password: string): Promise<void> {
  await apiFetch('/api/v1/me/oidc/unbind', { method: 'POST', body: JSON.stringify({ password }) })
}

export async function adminUnbindSso(userId: number): Promise<void> {
  await apiFetch(`/api/v1/admin/users/${userId}/unbind-oidc`, { method: 'POST' })
}

/** 回调 302 到 /login?sso_error=<code> 后，登录页展示的中文提示。 */
export const SSO_ERROR_MESSAGES: Record<string, string> = {
  oidc_disabled: 'SSO 登录当前未启用，请使用用户名密码登录。',
  oidc_not_configured: 'SSO 配置无效或身份提供方暂时不可达，请联系管理员。',
  oidc_state_mismatch: '登录会话已过期或请求异常，请重新发起 SSO 登录。',
  oidc_provider_denied: '身份提供方拒绝了本次登录，请重试或联系管理员。',
  oidc_exchange_failed: 'SSO 登录凭据交换失败，请重试；若持续失败请联系管理员。',
  oidc_identity_unbound: '当前 IdP 账号尚未开通本站访问，请联系管理员绑定或开通。',
  oidc_identity_taken: '该 SSO 身份已绑定其他账号，请勿重复绑定。',
  oidc_username_invalid: 'IdP 返回的用户名不符合本站规则，无法自动创建账号。',
  oidc_username_missing: '身份提供方返回的令牌缺少标准 preferred_username 声明，请联系管理员检查 IdP 配置。',
  oidc_username_conflict: '同名用户已存在，无法自动创建账号，请联系管理员绑定。',
  oidc_account_disabled: '该账号已被禁用，请联系管理员。',
  oidc_bind_requires_login: '绑定 SSO 身份前请先登录本站。',
  oidc_rate_limited: '尝试过于频繁，请稍后再试。',
  oidc_internal: 'SSO 登录过程中出现内部错误，请稍后重试。',
}
