const staticCacheModeLabels: Record<string, string> = {
  short: '短缓存（默认，5 分钟）',
  'no-cache': '协商缓存（使用前验证）',
  long: '长缓存（内容变化必须换 URL）',
}

const staticCorsModeLabels: Record<string, string> = {
  public: '公开跨域（ACAO: *，无凭据）',
  allowlist: '白名单（精确匹配 Origin）',
  none: '不添加 CORS 头',
}

import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiRequestError } from '../../../api/client'
import {
  adminGetStaticSettings,
  adminUpdateStaticSettings,
  type StaticHttpSettings,
} from '../../../api/admin'
import { Input, Textarea } from '@fluentui/react-components'
import { Button } from '../../../components/ui/Button'
import { DialogWrap } from '../../../components/ui/Dialog'
import { Banner } from '../../../components/shell/Banner'
import { FormField } from '../../../components/shell/FormLayout'
import { Select } from '../../../components/ui/Select'
import { StateView } from '../../../components/shell/StateView'
import { toastSuccess } from '../../../components/ui/Toast'
import { useStyles } from '../sections/sources.styles'

export function StaticHttpSettingsDialog({
  open,
  onClose,
}: {
  open: boolean
  onClose: () => void
}) {
  const css = useStyles()
  const queryClient = useQueryClient()
  const settingsQuery = useQuery({
    queryKey: ['static-http-settings'],
    queryFn: adminGetStaticSettings,
    enabled: open,
  })

  const [publicOriginDraft, setPublicOriginDraft] = useState<string | null>(null)
  const [cacheModeDraft, setCacheModeDraft] = useState<StaticHttpSettings['cache_mode'] | null>(null)
  const [corsModeDraft, setCorsModeDraft] = useState<StaticHttpSettings['cors_mode'] | null>(null)
  const [allowedOriginsDraft, setAllowedOriginsDraft] = useState<string | null>(null)
  const [copiedKey, setCopiedKey] = useState<string | null>(null)
  const [err, setErr] = useState('')

  const data = settingsQuery.data

  useEffect(() => {
    if (open && data) {
      setPublicOriginDraft(null)
      setCacheModeDraft(null)
      setCorsModeDraft(null)
      setAllowedOriginsDraft(null)
      setErr('')
    }
  }, [open, data])

  const publicOrigin = publicOriginDraft ?? (data?.public_origin ?? '')
  const cacheMode = cacheModeDraft ?? (data?.cache_mode ?? 'short')
  const corsMode = corsModeDraft ?? (data?.cors_mode ?? 'public')
  const allowedOriginsText = allowedOriginsDraft ?? (data?.allowed_origins.join('\n') ?? '')

  const draftOrigins = allowedOriginsText.split('\n').map((o) => o.trim()).filter(Boolean)
  const originalOrigins = (data?.allowed_origins ?? []).map((o) => o.trim()).filter(Boolean)
  const originsChanged = draftOrigins.length !== originalOrigins.length
    || draftOrigins.some((origin, index) => origin !== originalOrigins[index])

  const isDirty = settingsQuery.isSuccess && !!data && (
    publicOrigin.trim() !== data.public_origin
    || cacheMode !== data.cache_mode
    || corsMode !== data.cors_mode
    || originsChanged
  )

  const saveMut = useMutation({
    mutationFn: () => adminUpdateStaticSettings({
      public_origin: publicOrigin.trim(),
      cache_mode: cacheMode,
      cors_mode: corsMode,
      allowed_origins: draftOrigins,
    }),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['static-http-settings'] }),
        queryClient.invalidateQueries({ queryKey: ['static-assets-config'] }),
        queryClient.invalidateQueries({ queryKey: ['admin-sources'] }),
      ])
      toastSuccess('静态 HTTP 参数已保存。')
      onClose()
    },
    onError: (e) => setErr(e instanceof ApiRequestError ? e.message : '保存失败'),
  })

  function copyBaseURL(sourceKey: string, url: string) {
    if (!url) return
    void navigator.clipboard.writeText(url).then(() => {
      setCopiedKey(sourceKey)
      window.setTimeout(() => setCopiedKey((current) => current === sourceKey ? null : current), 1400)
    })
  }

  return (
    <DialogWrap
      open={open}
      onOpenChange={(next) => { if (!next) onClose() }}
      title="静态 HTTP"
      wide
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>取消</Button>
          <Button disabled={!isDirty || saveMut.isPending} onClick={() => { setErr(''); saveMut.mutate() }}>
            {saveMut.isPending ? '保存中…' : '保存'}
          </Button>
        </>
      }
    >
      {settingsQuery.isPending ? (
        <StateView layout="plain" kind="pending" title="正在加载静态 HTTP 参数…" />
      ) : settingsQuery.isError ? (
        <StateView layout="plain" kind="error" title="静态 HTTP 参数加载失败" description={settingsQuery.error.message} />
      ) : (
        <div className={css.staticProtocolBody} role="region" aria-label="静态 HTTP">
          <FormField
            label="CDN / 对外 Origin（可选）"
            hint="对接 CDN 或独立域名时填写，例如 https://cdn.example.com；不自动配置 DNS/TLS/反向代理。"
          >
            <Input
              value={publicOrigin}
              onChange={(event) => { setPublicOriginDraft(event.target.value); setErr('') }}
              placeholder="https://cdn.example.com"
              aria-label="静态资源对外 origin"
            />
          </FormField>

          <FormField label="缓存档位" hint="对接 CDN 回源时可按需选择；long 仅适合内容变化就更换 URL 的资源。">
            <Select
              value={cacheMode}
              onValueChange={(value) => { setCacheModeDraft(value as StaticHttpSettings['cache_mode']); setErr('') }}
              options={Object.entries(staticCacheModeLabels).map(([value, label]) => ({ value, label }))}
              ariaLabel="缓存档位"
            />
          </FormField>

          <FormField label="跨域档位" hint="CORS 不是图片保密机制；公开媒体可跨站嵌入。">
            <Select
              value={corsMode}
              onValueChange={(value) => { setCorsModeDraft(value as StaticHttpSettings['cors_mode']); setErr('') }}
              options={Object.entries(staticCorsModeLabels).map(([value, label]) => ({ value, label }))}
              ariaLabel="跨域档位"
            />
          </FormField>

          {corsMode === 'allowlist' && (
            <FormField label="允许的 Origin 白名单（每行一个完整 origin）" hint="精确匹配，不支持任意后缀。">
              <Textarea
                aria-label="静态 HTTP Origin 白名单"
                className={css.formTextarea}
                rows={4}
                resize="vertical"
                value={allowedOriginsText}
                onChange={(event) => { setAllowedOriginsDraft(event.target.value); setErr('') }}
              />
            </FormField>
          )}

          {data && data.sources.length > 0 && (
            <FormField label="已开启静态 HTTP 的存储源">
              <div className={css.staticProtocolBody} role="list" aria-label="静态 HTTP 访问地址">
                {data.sources.map((src) => {
                  const previewOrigin = publicOrigin.trim()
                  const displayUrl = previewOrigin && src.public_asset_id
                    ? `${previewOrigin.replace(/\/+$/, '')}/assets/${src.public_asset_id}/`
                    : (src.base_url || `${window.location.origin}/assets/${src.public_asset_id}/`)
                  return (
                    <div key={src.source_key} className={css.staticBaseUrlBar} role="listitem">
                      <div className={css.staticBaseUrlMeta}>
                        <strong style={{ fontSize: '13px', color: 'var(--colorNeutralForeground1)' }}>{src.source_name}</strong>
                        <code className={css.staticBaseUrlCode} title={displayUrl}>{displayUrl}</code>
                      </div>
                      <Button variant="ghost" size="small" onClick={() => copyBaseURL(src.source_key, displayUrl)}>
                        {copiedKey === src.source_key ? '已复制' : '复制'}
                      </Button>
                    </div>
                  )
                })}
              </div>
            </FormField>
          )}

          {err && <Banner intent="error">{err}</Banner>}
        </div>
      )}
    </DialogWrap>
  )
}
