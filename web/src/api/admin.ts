import { apiFetch, ApiRequestError } from './client'
import type { User } from './auth'

export type AdminUser = User & { usage_bytes: number }

// --- 管理员 API ---

export interface AdminSource {
  id: number
  key: string
  name: string
  description: string
  root_path: string
  is_disabled: boolean
  webdav_enabled: boolean
  s3_enabled: boolean
  static_enabled: boolean
  quota_bytes: number
  created_at: string
  updated_at: string
}

// Site Capability 绑定（2.0）：产品功能归站点，全站单 Source。
export type SiteCapability = 'public_drive' | 'image_bed'

export interface CapabilityBinding {
  capability: SiteCapability
  enabled: boolean
  source_key?: string
  source_name?: string
  revision: number
  updated_at: string
}

export async function adminListCapabilities(): Promise<CapabilityBinding[]> {
  const data = await apiFetch<{ items: CapabilityBinding[]; total: number }>('/api/v1/admin/capabilities')
  return data.items ?? []
}

export async function adminUpdateCapability(
  capability: SiteCapability,
  input: { enabled?: boolean; storage_source_key?: string; expected_revision?: number },
): Promise<CapabilityBinding> {
  return apiFetch(`/api/v1/admin/capabilities/${capability}`, {
    method: 'PUT',
    body: JSON.stringify(input),
  })
}

export type AccessPermission = 'read_only' | 'read_write'

export interface AccessPolicyPathRule {
  path_prefix: string
  permission: AccessPermission
}

export interface AccessPolicy {
  key: string
  name: string
  description: string
  sources: Array<{
    source_key: string
    source_name: string
    permission: AccessPermission
    path_rules: AccessPolicyPathRule[]
  }>
  users: Array<{
    user_id: number
    username: string
    display_name: string
  }>
  created_at: string
  updated_at: string
}

export interface AccessPolicyInput {
  name: string
  description: string
  sources: Array<{
    source_key: string
    permission: AccessPermission
    path_rules: AccessPolicyPathRule[]
  }>
  user_ids: number[]
}

export interface StorageQuota {
  usage_bytes: number
  quota_bytes: number
  remaining_bytes: number
  unlimited: boolean
}

export interface ReconcileResult {
  scanned_files: number
  added: number
  updated: number
  removed: number
  unowned: number
  usage_bytes: number
}

export interface SourceCreationResult {
  source: AdminSource
  reconcile: ReconcileResult
}

export interface SourcePreflight {
  root_path: string
  is_empty: boolean
  summary: {
    total_entries: number
    visible_entries: number
    files: number
    directories: number
    symlinks: number
    unsupported_entries: number
    excluded_entries: number
  }
  entries: Array<{
    name: string
    kind: 'file' | 'directory' | 'symlink' | 'unsupported'
  }>
  sample_truncated: boolean
  exclude_patterns: string[]
  warnings: string[]
}

export interface AuditLog {
  id: number
  actor_type: string
  actor_user_id: number | null
  entry_type: string
  action: string
  storage_source_id: number | null
  storage_source_name: string | null
  relative_path: string | null
  target_relative_path: string | null
  ip_address: string | null
  status: string
  error_code: string | null
  created_at: string
}

export interface AuditLogQuery {
  page: number
  page_size: number
  actor_type?: 'user' | 'anonymous' | 'system'
  entry_type?: 'web' | 'webdav' | 's3' | 'image_bed' | 'anonymous_image_bed' | 'admin' | 'cli'
  status?: 'success' | 'failed'
  q?: string
}

export interface AuditLogPage {
  items: AuditLog[]
  total: number
  page: number
  page_size: number
}

// 用户管理
export async function adminListUsers(): Promise<AdminUser[]> {
  const data = await apiFetch<{ items: AdminUser[]; total: number }>('/api/v1/admin/users')
  return data.items ?? []
}

export async function adminCreateUser(input: {
  username: string
  display_name: string
  password: string
  role: string
  quota_bytes?: number
}): Promise<User> {
  return apiFetch('/api/v1/admin/users', { method: 'POST', body: JSON.stringify(input) })
}

export async function adminSetUserQuota(id: number, quotaBytes: number): Promise<StorageQuota> {
  return apiFetch(`/api/v1/admin/users/${id}/quota`, {
    method: 'PATCH',
    body: JSON.stringify({ quota_bytes: quotaBytes }),
  })
}

export async function adminSetUserDisabled(id: number, disabled: boolean): Promise<void> {
  await apiFetch(`/api/v1/admin/users/${id}/${disabled ? 'disable' : 'enable'}`, { method: 'POST' })
}

export interface RevokedCredentials {
  sessions: number
  webdav_tokens: number
  image_bed_tokens: number
  s3_credentials: number
}

export async function adminRevokeUserCredentials(id: number): Promise<RevokedCredentials> {
  return apiFetch(`/api/v1/admin/users/${id}/revoke-credentials`, { method: 'POST' })
}

export async function adminDeleteUser(id: number): Promise<void> {
  await apiFetch(`/api/v1/admin/users/${id}`, { method: 'DELETE' })
}

// 存储源管理
export async function adminListSources(): Promise<AdminSource[]> {
  const data = await apiFetch<{ items: AdminSource[]; total: number }>('/api/v1/admin/sources')
  return data.items ?? []
}

export async function adminCreateSource(input: {
  name: string
  description: string
  root_path: string
  exclude_patterns?: string[]
  import_existing: boolean
}): Promise<SourceCreationResult> {
  return apiFetch('/api/v1/admin/sources', { method: 'POST', body: JSON.stringify(input) })
}

export async function adminPreflightSource(input: {
  root_path: string
  exclude_patterns?: string[]
}): Promise<SourcePreflight> {
  return apiFetch('/api/v1/admin/sources/preflight', {
    method: 'POST',
    body: JSON.stringify(input),
  })
}

export async function adminGetSource(sourceKey: string): Promise<{
  source: AdminSource
  exclude_patterns: string[]
  quota: StorageQuota
  ledger_usage_bytes: number
}> {
  return apiFetch(`/api/v1/admin/sources/${encodeURIComponent(sourceKey)}`)
}

export async function adminReconcileSource(sourceKey: string): Promise<ReconcileResult> {
  return apiFetch(`/api/v1/admin/sources/${encodeURIComponent(sourceKey)}/reconcile`, { method: 'POST' })
}

export async function adminUpdateSource(
  sourceKey: string,
  input: Partial<{
    name: string
    description: string
    webdav_enabled: boolean
    s3_enabled: boolean
    static_enabled: boolean
    quota_bytes: number
    exclude_patterns: string[]
  }>,
): Promise<AdminSource> {
  return apiFetch(`/api/v1/admin/sources/${encodeURIComponent(sourceKey)}`, {
    method: 'PATCH',
    body: JSON.stringify(input),
  })
}

export async function adminSetSourceDisabled(sourceKey: string, disabled: boolean): Promise<void> {
  await apiFetch(
    `/api/v1/admin/sources/${encodeURIComponent(sourceKey)}/${disabled ? 'disable' : 'enable'}`,
    { method: 'POST' },
  )
}

export async function adminDeleteSource(sourceKey: string): Promise<void> {
  await apiFetch(`/api/v1/admin/sources/${encodeURIComponent(sourceKey)}`, { method: 'DELETE' })
}

export async function adminSetExcludePatterns(sourceKey: string, patterns: string[]): Promise<void> {
  await apiFetch(`/api/v1/admin/sources/${encodeURIComponent(sourceKey)}/exclude-patterns`, {
    method: 'PUT',
    body: JSON.stringify({ patterns }),
  })
}

// 访问策略
export async function adminListPolicies(): Promise<AccessPolicy[]> {
  const data = await apiFetch<{ items: AccessPolicy[]; total: number }>('/api/v1/admin/policies')
  return data.items ?? []
}

export async function adminCreatePolicy(input: AccessPolicyInput): Promise<AccessPolicy> {
  return apiFetch('/api/v1/admin/policies', { method: 'POST', body: JSON.stringify(input) })
}

export async function adminUpdatePolicy(policyKey: string, input: AccessPolicyInput): Promise<AccessPolicy> {
  return apiFetch(`/api/v1/admin/policies/${encodeURIComponent(policyKey)}`, {
    method: 'PUT',
    body: JSON.stringify(input),
  })
}

export async function adminDeletePolicy(policyKey: string): Promise<void> {
  await apiFetch(`/api/v1/admin/policies/${encodeURIComponent(policyKey)}`, { method: 'DELETE' })
}

// 匿名图床配置（2.0：目标由 Site Capability 绑定决定，仅保留入口开关）
export async function adminGetAnonymousSettings(): Promise<{
  enabled: boolean
  key: string
}> {
  return apiFetch('/api/v1/admin/image-bed/anonymous-settings')
}

export async function adminSetAnonymousSettings(input: {
  enabled: boolean
}): Promise<void> {
  await apiFetch('/api/v1/admin/image-bed/anonymous-settings', {
    method: 'PUT',
    body: JSON.stringify(input),
  })
}

// 静态资源托管配置（2.0 AST）
export interface StaticAssetConfig {
  enabled: boolean
  source_key: string
  source_name: string
  source_disabled: boolean
  publish_root: string
  public_asset_id: string
  public_origin: string
  base_url: string
  cache_mode: 'short' | 'no-cache' | 'long'
  cors_mode: 'none' | 'public' | 'allowlist'
  allowed_origins: string[]
  revision: number
  updated_at: string
}

export interface StaticAssetPreflight {
  source_key: string
  publish_root: string
  root_exists: boolean
  sample_entries: string[]
  sample_truncated: boolean
  supported_types: string[]
  warnings: string[]
}

export interface StaticHttpEndpointSummary {
  source_key: string
  source_name: string
  public_asset_id: string
  base_url: string
}

export interface StaticHttpSettings {
  public_origin: string
  cache_mode: 'short' | 'no-cache' | 'long'
  cors_mode: 'none' | 'public' | 'allowlist'
  allowed_origins: string[]
  sources: StaticHttpEndpointSummary[]
}

export async function adminGetStaticSettings(): Promise<StaticHttpSettings> {
  return apiFetch<StaticHttpSettings>('/api/v1/admin/static-access/settings')
}

export async function adminUpdateStaticSettings(input: {
  public_origin: string
  cache_mode: string
  cors_mode: string
  allowed_origins: string[]
}): Promise<StaticHttpSettings> {
  return apiFetch<StaticHttpSettings>('/api/v1/admin/static-access/settings', {
    method: 'PUT',
    body: JSON.stringify(input),
  })
}

export async function adminGetStaticConfig(sourceKey: string): Promise<StaticAssetConfig> {
  return apiFetch<StaticAssetConfig>(`/api/v1/admin/sources/${encodeURIComponent(sourceKey)}/static-access/config`)
}

export async function adminConfigureStaticAssets(sourceKey: string, input: {
  publish_root: string
  enabled?: boolean
  cache_mode?: string
  cors_mode?: string
  public_origin?: string
  allowed_origins?: string[]
  confirm_publish_root?: boolean
}): Promise<StaticAssetConfig> {
  return apiFetch(`/api/v1/admin/sources/${encodeURIComponent(sourceKey)}/static-access/config`, { method: 'POST', body: JSON.stringify(input) })
}

export async function adminUpdateStaticAssets(sourceKey: string, input: {
  enabled?: boolean
  cache_mode?: string
  cors_mode?: string
  public_origin?: string
  allowed_origins?: string[]
  expected_revision?: number
}): Promise<StaticAssetConfig> {
  return apiFetch(`/api/v1/admin/sources/${encodeURIComponent(sourceKey)}/static-access/config`, { method: 'PUT', body: JSON.stringify(input) })
}

export async function adminRebindStaticAssets(sourceKey: string, input: {
  source_key: string
  publish_root: string
  mode: 'reset' | 'relocate'
  confirm_publish_root?: boolean
  confirm_relocate?: boolean
  expected_revision?: number
}): Promise<StaticAssetConfig> {
  return apiFetch(`/api/v1/admin/sources/${encodeURIComponent(sourceKey)}/static-access/rebind`, { method: 'POST', body: JSON.stringify(input) })
}

export async function adminPreflightStaticAssets(sourceKey: string, input: {
  publish_root: string
}): Promise<StaticAssetPreflight> {
  return apiFetch(`/api/v1/admin/sources/${encodeURIComponent(sourceKey)}/static-access/preflight`, { method: 'POST', body: JSON.stringify(input) })
}

export async function adminRemoveStaticConfig(sourceKey: string, revision: number): Promise<{ ok: boolean }> {
  return apiFetch(`/api/v1/admin/sources/${encodeURIComponent(sourceKey)}/static-access/config`, {
    method: 'DELETE', body: JSON.stringify({ expected_revision: revision }),
  })
}

// 仪表盘健康快照与维护操作
export interface OperationsSourceHealth {
  key: string
  name: string
  root_path: string
  disabled: boolean
  root_accessible: boolean
  usage_bytes: number
  ledger_bytes: number
  quota_bytes: number
  trash_count: number
  webdav_enabled: boolean
  s3_enabled: boolean
  static_enabled: boolean
  root_issue?: string
}

export interface OperationsCapabilityHealth {
  capability: string
  enabled: boolean
  source_key?: string
  source_name?: string
  source_disabled: boolean
  detail: string
}

export interface OperationsStatus {
  version: string
  data_dir: string
  generated_at: string
  database: {
    size_bytes: number
    wal_bytes: number
    quick_check: string
    table_rows: Record<string, number>
  }
  sources: OperationsSourceHealth[]
  capabilities: OperationsCapabilityHealth[]
  storage: {
    thumbnail_cache_bytes: number
    trash_entries: number
    trash_bytes: number
    multipart_uploads: number
  }
  recent_failed_audits: Array<{
    id: number
    action: string
    error_code: string | null
    created_at: string
  }>
}

export interface IntegrityReport {
  integrity: string[]
  fk_violations: number
  ok: boolean
}

export async function runIntegrityCheck(): Promise<IntegrityReport> {
  return apiFetch('/api/v1/admin/operations/integrity-check', { method: 'POST' })
}

export interface CleanupResult {
  webdav_locks: number
  sessions: number
  multipart_uploads: number
  multipart_orphans: number
  thumbnails: number
}

export async function runOperationsCleanup(): Promise<CleanupResult> {
  return apiFetch('/api/v1/admin/operations/cleanup', { method: 'POST' })
}

// 图床保留策略（2.0 IMG：0 = 不自动过期；登录用户与匿名相互独立）
export interface ImageBedRetention {
  user_retention_days: number
  anonymous_retention_days: number
}

export async function adminGetImageBedRetention(): Promise<ImageBedRetention> {
  return apiFetch<ImageBedRetention>('/api/v1/admin/image-bed/retention')
}

export async function adminSetImageBedRetention(input: Partial<ImageBedRetention>): Promise<ImageBedRetention> {
  return apiFetch('/api/v1/admin/image-bed/retention', {
    method: 'PUT',
    body: JSON.stringify(input),
  })
}

// 审计日志
export async function adminFetchAuditLogs(query: AuditLogQuery): Promise<AuditLogPage> {
  const params = new URLSearchParams({
    page: String(query.page),
    page_size: String(query.page_size),
  })
  if (query.actor_type) params.set('actor_type', query.actor_type)
  if (query.entry_type) params.set('entry_type', query.entry_type)
  if (query.status) params.set('status', query.status)
  if (query.q) params.set('q', query.q)

  const data = await apiFetch<AuditLogPage>(`/api/v1/admin/audit-logs?${params}`)
  return {
    items: data.items ?? [],
    total: data.total ?? 0,
    page: data.page ?? query.page,
    page_size: data.page_size ?? query.page_size,
  }
}

// 系统配置包导出（ZIP 二进制响应，不走 JSON envelope）。
export async function adminExportSystemConfig(): Promise<string> {
  const response = await fetch('/api/v1/admin/system/config-export', {
    headers: { Accept: 'application/zip' },
  })
  if (!response.ok) {
    let message = '导出系统配置包失败'
    let code = 'INTERNAL_ERROR'
    let requestId = ''
    try {
      const body = await response.json() as {
        error?: { code?: string; message?: string }
        request_id?: string
      }
      message = body.error?.message ?? message
      code = body.error?.code ?? code
      requestId = body.request_id ?? ''
    } catch {
      // 非 JSON 错误响应使用通用提示。
    }
    throw new ApiRequestError({ code, message }, requestId)
  }

  const disposition = response.headers.get('Content-Disposition') ?? ''
  const match = disposition.match(/filename="?([^";]+)"?/i)
  const filename = match?.[1] ?? 'omnistore-system-config.zip'
  const objectURL = URL.createObjectURL(await response.blob())
  const link = document.createElement('a')
  link.href = objectURL
  link.download = filename
  document.body.appendChild(link)
  link.click()
  link.remove()
  URL.revokeObjectURL(objectURL)
  return filename
}

// 仪表盘：连接配置 + 运维服务的唯一健康快照。
export interface OverviewSystem {
  http_addr: string
  public_url: string
  s3_enabled: boolean
  s3_status: string
  webdav_status: string
}
export interface AdminOverview {
  anonymous_image_bed_on: boolean
  system: OverviewSystem
  operations: OperationsStatus
}
export async function fetchAdminOverview(): Promise<AdminOverview> {
  return apiFetch<AdminOverview>('/api/v1/admin/overview')
}

// 用户自助
export async function updateProfile(displayName: string): Promise<User> {
  return apiFetch('/api/v1/me/profile', {
    method: 'PATCH',
    body: JSON.stringify({ display_name: displayName }),
  })
}

export async function changePassword(oldPassword: string, newPassword: string): Promise<void> {
  await apiFetch('/api/v1/me/password', {
    method: 'POST',
    body: JSON.stringify({ old_password: oldPassword, new_password: newPassword }),
  })
}

// 实例品牌信息（1.2.0）
export async function adminGetBranding(): Promise<{ instance_name: string }> {
  return apiFetch('/api/v1/admin/branding')
}

export async function adminSetBranding(instanceName: string): Promise<void> {
  await apiFetch('/api/v1/admin/branding', {
    method: 'PUT',
    body: JSON.stringify({ instance_name: instanceName }),
  })
}
