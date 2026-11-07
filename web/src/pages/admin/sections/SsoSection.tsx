import { useState, type FormEvent, type ReactNode } from 'react'
import { makeStyles, shorthands, tokens } from '@fluentui/react-components'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { adminGetSso, adminSetSso, adminTestSso, type SsoProbeResult } from '../../../api/sso'
import { ApiRequestError } from '../../../api/client'
import { Badge } from '../../../components/ui/Badge'
import { Button } from '../../../components/ui/Button'
import { Checkbox, Input } from '@fluentui/react-components'
import { Banner } from '../../../components/shell/Banner'
import { toastError, toastSuccess } from '../../../components/ui/Toast'
import { FormField, FormLayout } from '../../../components/shell/FormLayout'
import { IconCopy, IconKey, IconPlug } from '../../../components/ui/Icon'
import { SectionCard } from '../SectionCard'
import { useStyles as useSiteStyles } from './site.styles'

const useStyles = makeStyles({
  toggleHint: {
    'margin': '0px',
    'fontSize': tokens.fontSizeBase200,
    'lineHeight': tokens.lineHeightBase200,
    'color': tokens.colorNeutralForeground2,
    'maxWidth': '52ch',
  },
  toggleBlock: {
    display: 'grid',
    ...shorthands.gap(tokens.spacingVerticalSNudge),
  },
  formActions: {
    display: 'flex',
    ...shorthands.gap(tokens.spacingHorizontalS),
    'alignItems': 'center',
  },
  redirectRow: {
    display: 'flex',
    ...shorthands.gap(tokens.spacingHorizontalS),
    'alignItems': 'center',
    'minWidth': '0px',
  },
})

// OIDC 单点登录（2.1.0）：配置存 system_settings，热生效，无需重启。
// 表单模式与品牌设置一致：SectionCard + 编辑态切换 + useMutation 保存。
export function SsoSection() {
  const css = useStyles()
  const queryClient = useQueryClient()
  const sso = useQuery({ queryKey: ['admin-sso'], queryFn: adminGetSso, retry: false })
  const [editing, setEditing] = useState(false)
  const [enabled, setEnabled] = useState(false)
  const [issuer, setIssuer] = useState('')
  const [clientId, setClientId] = useState('')
  const [clientSecret, setClientSecret] = useState('')
  const [scopes, setScopes] = useState('')
  const [autoLink, setAutoLink] = useState(false)
  const [autoCreate, setAutoCreate] = useState(false)
  const [msg, setMsg] = useState('')
  const [probe, setProbe] = useState<SsoProbeResult | null>(null)
  const [probing, setProbing] = useState(false)

  const save = useMutation({
    mutationFn: () => adminSetSso({
      enabled,
      issuer: issuer.trim(),
      client_id: clientId.trim(),
      client_secret: clientSecret.trim() || undefined,
      scopes: scopesTextToList(scopes),
      auto_link_username: autoLink,
      auto_create_users: autoCreate,
    }),
    onSuccess: async (data) => {
      setEditing(false)
      setMsg('')
      setProbe(null)
      await Promise.all(
        ['admin-sso', 'system-status', 'auth-status'].map(
          (key) => queryClient.invalidateQueries({ queryKey: [key] }),
        ),
      )
      toastSuccess(data.enabled ? 'SSO 配置已保存并生效。' : 'SSO 配置已保存，登录入口已关闭。')
    },
    onError: (error) => {
      setMsg(error instanceof ApiRequestError ? error.message : '保存失败，请重试。')
    },
  })

  function openEditor() {
    const data = sso.data
    setEnabled(!!data?.enabled)
    setIssuer(data?.issuer ?? '')
    setClientId(data?.client_id ?? '')
    setClientSecret('')
    setScopes(data?.scopes.join(' ') ?? '')
    setAutoLink(!!data?.auto_link_username)
    setAutoCreate(!!data?.auto_create_users)
    setMsg('')
    setProbe(null)
    setEditing((open) => !open)
  }

  function submit(event: FormEvent) {
    event.preventDefault()
    if (enabled && !issuer.trim()) { setMsg('启用 SSO 时必须填写 Issuer 地址。'); return }
    if (enabled && !clientId.trim()) { setMsg('启用 SSO 时必须填写 Client ID。'); return }
    if (enabled && !clientSecret.trim() && !sso.data?.has_secret) {
      setMsg('启用 SSO 时必须填写 Client Secret。')
      return
    }
    save.mutate()
  }

  async function testConnection() {
    const target = issuer.trim() || sso.data?.issuer || ''
    if (!target) { setMsg('请先填写 Issuer 地址。'); return }
    setProbing(true)
    setProbe(null)
    try {
      setProbe(await adminTestSso(target))
    } catch (error) {
      setProbe({ ok: false, error: error instanceof ApiRequestError ? error.message : '测试请求失败' })
    } finally {
      setProbing(false)
    }
  }

  async function copyRedirectURI() {
    if (!sso.data?.redirect_uri) return
    try {
      await navigator.clipboard.writeText(sso.data.redirect_uri)
      toastSuccess('回调地址已复制。')
    } catch {
      toastError('复制失败，请手动选择并复制。')
    }
  }

  const data = sso.data
  return (
    <SectionCard
      icon={<IconKey size={18} />}
      tone="brand"
      title="单点登录（SSO）"
      titleId="sso-title"
      titleAs="h3"
      badge={data?.enabled ? <Badge color="green">已启用</Badge> : <Badge color="gray">未启用</Badge>}
      actions={(
        <Button variant="secondary" disabled={!data || save.isPending} onClick={openEditor}>
          {editing ? '取消' : '配置'}
        </Button>
      )}
    >
      {editing ? (
        <form onSubmit={submit} style={{ display: 'grid', gap: 12, maxWidth: 520 }}>
          <Checkbox label="启用 SSO 登录" checked={enabled} disabled={save.isPending}
            onChange={(_, d) => setEnabled(!!d.checked)} />
          <FormLayout columns={2}>
            <FormField label="Issuer 地址" required info="如 https://casdoor.example.com，服务端会读取其 OIDC 发现文档。">
              <Input value={issuer} onChange={(e) => setIssuer(e.target.value)} placeholder="https://casdoor.example.com" autoFocus />
            </FormField>
            <FormField label="Client ID" required info="在 Casdoor 应用设置中创建并复制。">
              <Input value={clientId} onChange={(e) => setClientId(e.target.value)} placeholder="omnistore" />
            </FormField>
          </FormLayout>
          <FormField
            label="Client Secret"
            required={enabled && !data?.has_secret}
            info={data?.has_secret ? '已保存密钥不回显；留空表示保留原值，输入新值即覆盖。' : '在 Casdoor 应用设置中复制。'}
          >
            <Input type="password" value={clientSecret} onChange={(e) => setClientSecret(e.target.value)}
              placeholder={data?.has_secret ? '••••••••（已设置）' : ''} autoComplete="new-password" />
          </FormField>
          <FormLayout columns={2}>
            <FormField label="授权范围（Scopes）" info="空格分隔，至少包含 openid；默认 openid profile email。">
              <Input value={scopes} onChange={(e) => setScopes(e.target.value)} placeholder="openid profile email" />
            </FormField>
            <FormField label="回调地址（登记到 IdP）" info="由实例对外地址自动派生，需原样填入 Casdoor 的 Redirect URLs。">
              <div className={css.redirectRow}>
                <Input value={data?.redirect_uri ?? ''} readOnly />
                <Button type="button" variant="secondary" icon={<IconCopy size={16} />} onClick={() => void copyRedirectURI()}>复制</Button>
              </div>
            </FormField>
          </FormLayout>
          <div className={css.toggleBlock}>
            <Checkbox label="按用户名自动绑定已有账号" checked={autoLink} disabled={save.isPending}
              onChange={(_, d) => setAutoLink(!!d.checked)} />
            <p className={css.toggleHint}>
              IdP 用户名与本地普通账号同名时自动绑定登录；不会自动绑定管理员账号。仅绑定，不自动创建账号。
            </p>
          </div>
          <div className={css.toggleBlock}>
            <Checkbox label="自动创建新账号（JIT）" checked={autoCreate} disabled={save.isPending}
              onChange={(_, d) => setAutoCreate(!!d.checked)} />
            <p className={css.toggleHint}>
              未能绑定的 IdP 用户首次登录时自动创建无密码的普通账号；这类账号无法使用密码登录。
            </p>
          </div>
          <div className={css.formActions}>
            <Button type="submit" disabled={save.isPending}>{save.isPending ? '保存中…' : '保存'}</Button>
            <Button type="button" variant="secondary" icon={<IconPlug size={16} />} disabled={probing} onClick={() => void testConnection()}>
              {probing ? '测试中…' : '测试连接'}
            </Button>
          </div>
          {probe?.ok ? <Banner intent="success">连接成功：授权端点 {probe.authorization_endpoint}</Banner>
            : probe ? <Banner intent="error">连接失败：{probe.error ?? '未知错误'}</Banner> : null}
          {msg ? <Banner intent="error">{msg}</Banner> : null}
        </form>
      ) : (
        <SsoSummary />
      )}
    </SectionCard>
  )
}

function SsoSummary() {
  const site = useSiteStyles()
  const sso = useQuery({ queryKey: ['admin-sso'], queryFn: adminGetSso, retry: false })
  if (sso.isError) {
    return <Banner intent="error" action={<Button variant="ghost" onClick={() => void sso.refetch()}>重试</Button>}>SSO 配置读取失败：{sso.error.message}</Banner>
  }
  const data = sso.data
  return (
    <div style={{ display: 'grid', gap: 8 }}>
      <Row site={site} label="状态">{sso.isPending ? '加载中…' : data?.enabled ? '已启用' : '未启用'}</Row>
      <Row site={site} label="Issuer">{data?.issuer || '—'}</Row>
      <Row site={site} label="Client ID">{data?.client_id || '—'}</Row>
      <Row site={site} label="回调地址">
        {data?.redirect_uri || '—'}
        {data?.redirect_uri ? (
          <Button variant="ghost" size="small" icon={<IconCopy size={14} />} onClick={() => void navigator.clipboard.writeText(data.redirect_uri).then(() => toastSuccess('回调地址已复制。'), () => toastError('复制失败，请手动复制。'))}>
            复制
          </Button>
        ) : null}
      </Row>
      <Row site={site} label="自动绑定 / 自动建号">
        {`${data?.auto_link_username ? '开' : '关'} / ${data?.auto_create_users ? '开' : '关'}`}
      </Row>
    </div>
  )
}

function Row({ site, label, children }: { site: ReturnType<typeof useSiteStyles>; label: string; children: ReactNode }) {
  return (
    <div className={site.kvRow}>
      <span className={site.kvLabel}>{label}</span>
      <span className={site.kvValue}>{children}</span>
    </div>
  )
}

function scopesTextToList(text: string): string[] {
  return text.split(/[\s,]+/).map((item) => item.trim()).filter(Boolean)
}
