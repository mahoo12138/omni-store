import { apiFetch } from './client'

// 系统功能开关（docs/home-1.png 右栏"系统状态"）。
// 公开端点，不需鉴权。
export interface SystemStatusFlag {
  enabled: boolean
  status: string
  hint: string
}

export interface SystemStatus {
  s3: SystemStatusFlag
  webdav: SystemStatusFlag
  file_preview: SystemStatusFlag
  anonymous: SystemStatusFlag
  public_drive: SystemStatusFlag
  image_bed: SystemStatusFlag
  sso: SystemStatusFlag
  version: string
  public_url: string
  instance_name: string
}

export async function fetchSystemStatus(): Promise<SystemStatus> {
  return apiFetch<SystemStatus>('/api/v1/system/status')
}
