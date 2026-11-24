import { useEffect, useReducer, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiRequestError } from '../../../api/client'
import { adminListSources, changePassword, updateProfile } from '../../../api/admin'
import { fetchMe } from '../../../api/auth'
import { unbindMySso } from '../../../api/sso'
import { fetchTokenStatus, resetToken } from '../../../api/imagebed'
import { fetchS3Credentials } from '../../../api/s3'
import { Input, mergeClasses } from '@fluentui/react-components'
import { Badge } from '../../../components/ui/Badge'
import { Button } from '../../../components/ui/Button'
import { DialogWrap } from '../../../components/ui/Dialog'
import { Menu } from '../../../components/ui/Menu'
import { Banner } from '../../../components/shell/Banner'
import { FormField } from '../../../components/shell/FormLayout'
import { toastError, toastSuccess } from '../../../components/ui/Toast'
import { IconChevronRight, IconCloud, IconEdit, IconGlobe, IconImage, IconKey, IconLink, IconMore, IconWarning } from '../../../components/ui/Icon'
import { useStyles } from './profile.styles'
import { SectionCard } from '../SectionCard'
import { StaticHttpSettingsDialog } from '../dialogs/StaticAssetsSection'
import { ImageBedTokenManager } from './profile/ImageBedTokenManager'
import { S3CredentialManager } from './profile/S3CredentialManager'
import { TokenBlock } from './profile/TokenBlock'

const credentialViews = [
  {
    key: 'webdav',
    label: 'WebDAV',
    meta: '桌面挂载',
    icon: <IconLink size={18} />,
    adminOnly: false,
  },
  {
    key: 'image-api',
    label: '图床 API',
    meta: '上传客户端',
    icon: <IconImage size={18} />,
    adminOnly: false,
  },
  {
    key: 's3',
    label: 'S3',
    meta: 'CLI 与 SDK',
    icon: <IconCloud size={18} />,
    adminOnly: false,
  },
  {
    key: 'static-http',
    label: '静态 HTTP',
    meta: 'CDN 与公开访问',
    icon: <IconGlobe size={18} />,
    adminOnly: true,
  },
] as const

type CredentialView = typeof credentialViews[number]['key']

type AccountModalKind = 'none' | 'profile' | 'password' | 'unbindSso'

interface AccountModalState {
  modal: AccountModalKind
  displayName: string
  oldPwd: string
  newPwd: string
  message: string
}

type AccountModalAction =
  | { type: 'open'; modal: Exclude<AccountModalKind, 'none'> }
  | { type: 'close' }
  | { type: 'setField'; field: 'displayName' | 'oldPwd' | 'newPwd' | 'message'; value: string }

const initialAccountModalState: AccountModalState = {
  modal: 'none',
  displayName: '',
  oldPwd: '',
  newPwd: '',
  message: '',
}

function accountModalReducer(state: AccountModalState, action: AccountModalAction): AccountModalState {
  switch (action.type) {
    case 'open':
      return { ...initialAccountModalState, modal: action.modal }
    case 'close':
      return initialAccountModalState
    case 'setField':
      return { ...state, [action.field]: action.value }
  }
}

export function ProfileSection() {
  const css = useStyles()
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const search = useSearch({ strict: false }) as { sso_bound?: string }
  const ssoBoundToastShown = useRef(false)
  const me = useQuery({ queryKey: ['me'], queryFn: fetchMe, retry: false })
  const isAdmin = me.data?.role === 'super_admin'
  const tokens = useQuery({ queryKey: ['token-status'], queryFn: fetchTokenStatus })
  const s3Credentials = useQuery({ queryKey: ['s3-credentials'], queryFn: fetchS3Credentials })
  const adminSources = useQuery({ queryKey: ['admin-sources'], queryFn: adminListSources, enabled: isAdmin })

  const [accountModal, dispatchAccountModal] = useReducer(accountModalReducer, initialAccountModalState)
  const [newTokens, setNewTokens] = useState<Record<string, string>>({})
  const [credentialView, setCredentialView] = useState<CredentialView | null>(null)

  // 绑定流程从 IdP 回跳后带 ?sso_bound=1 落地，提示一次并清掉参数。
  useEffect(() => {
    if (search.sso_bound === '1' && !ssoBoundToastShown.current) {
      ssoBoundToastShown.current = true
      toastSuccess('SSO 身份绑定成功，下次可直接使用 SSO 登录。')
      void navigate({ to: '/app/settings', search: { section: 'profile' }, replace: true })
    }
  }, [search.sso_bound, navigate])

  const profileMut = useMutation({
    mutationFn: updateProfile,
    onSuccess: () => {
      dispatchAccountModal({ type: 'setField', field: 'message', value: '已保存' })
      queryClient.invalidateQueries({ queryKey: ['me'] })
      setTimeout(() => { dispatchAccountModal({ type: 'close' }) }, 600)
    },
    onError: (err) => dispatchAccountModal({
      type: 'setField',
      field: 'message',
      value: err instanceof ApiRequestError ? err.message : '保存失败',
    }),
  })

  const pwdMut = useMutation({
    mutationFn: ({ o, n }: { o: string; n: string }) => changePassword(o, n),
    onSuccess: () => {
      dispatchAccountModal({ type: 'setField', field: 'message', value: '密码已修改' })
      dispatchAccountModal({ type: 'setField', field: 'oldPwd', value: '' })
      dispatchAccountModal({ type: 'setField', field: 'newPwd', value: '' })
      setTimeout(() => { dispatchAccountModal({ type: 'close' }) }, 600)
    },
    onError: (err) => dispatchAccountModal({
      type: 'setField',
      field: 'message',
      value: err instanceof ApiRequestError ? err.message : '修改失败',
    }),
  })

  const unbindSsoMut = useMutation({
    mutationFn: (password: string) => unbindMySso(password),
    onSuccess: async () => {
      dispatchAccountModal({ type: 'close' })
      await queryClient.invalidateQueries({ queryKey: ['me'] })
      toastSuccess('已解除 SSO 绑定。')
    },
    onError: (err) => dispatchAccountModal({
      type: 'setField',
      field: 'message',
      value: err instanceof ApiRequestError ? err.message : '解除绑定失败',
    }),
  })

  const resetMut = useMutation({
    mutationFn: resetToken,
    onSuccess: (data, type) => {
      setNewTokens((prev) => ({ ...prev, [type]: data.token }))
      queryClient.invalidateQueries({ queryKey: ['token-status'] })
    },
    onError: (err) => toastError(err instanceof ApiRequestError ? err.message : '重置失败'),
  })

  function dismissNewToken(type: 'webdav') {
    setNewTokens((prev) => ({ ...prev, [type]: '' }))
  }

  function onSaveProfile(e: FormEvent) {
    e.preventDefault()
    if (accountModal.displayName.trim()) profileMut.mutate(accountModal.displayName.trim())
  }
  function onChangePassword(e: FormEvent) {
    e.preventDefault()
    pwdMut.mutate({ o: accountModal.oldPwd, n: accountModal.newPwd })
  }

  function credentialStatus(view: CredentialView) {
    if (view === 'static-http') {
      if (adminSources.isPending) return { label: '正在读取', active: false }
      if (adminSources.isError) return { label: '状态未知', active: false, error: true }
      const enabled = (adminSources.data ?? []).filter((item) => item.static_enabled).length
      return enabled > 0
        ? { label: `已启用 ${enabled} 个`, active: true }
        : { label: '未启用', active: false }
    }
    if (view === 's3') {
      if (s3Credentials.isPending) return { label: '正在读取', active: false }
      if (s3Credentials.isError) return { label: '状态未知', active: false, error: true }
      const enabled = (s3Credentials.data ?? []).filter((item) => !item.is_disabled).length
      return enabled > 0
        ? { label: `已启用 ${enabled} 个`, active: true }
        : { label: '未生成', active: false }
    }

    if (tokens.isPending) return { label: '正在读取', active: false }
    if (tokens.isError) return { label: '状态未知', active: false, error: true }
    const status = view === 'webdav' ? tokens.data?.webdav : tokens.data?.image_bed
    return status?.exists
      ? { label: view === 'webdav' ? '已启用' : `已启用 ${status.count} 个`, active: true }
      : { label: '未生成', active: false }
  }

  const visibleCredentialViews = credentialViews.filter((view) => !view.adminOnly || isAdmin)

  function closeCredentialModal() {
    const previous = credentialView
    setCredentialView(null)
    requestAnimationFrame(() => {
      if (previous) document.getElementById(`credential-entry-${previous}`)?.focus()
    })
  }

  return (
    <div className={css.profilePage}>
      <section className={css.accountPanel}>
        <div className={css.accountIdentity}>
          <span className={css.accountAvatar} aria-hidden="true">
            {(me.data?.display_name || me.data?.username || 'U').slice(0, 1).toUpperCase()}
          </span>
          <div className={css.accountCopy}>
            <span className={css.eyebrow}>个人账户</span>
            <h2 className={css.accountName}>
              {me.data?.display_name || me.data?.username || '加载中…'}
            </h2>
            <p className={css.accountMeta}>
              <span>@{me.data?.username ?? '-'}</span>
              <span className={css.metaDivider} aria-hidden="true" />
              <span>用户名不可修改</span>
              {me.data?.sso_bound && (
                <>
                  <span className={css.metaDivider} aria-hidden="true" />
                  <span><Badge color="green">SSO 已绑定</Badge></span>
                </>
              )}
            </p>
          </div>
        </div>
        <div className={css.accountActions}>
          <Menu
            ariaLabel="账户操作"
            triggerTitle="账户操作"
            trigger={<IconMore size={18} />}
            triggerClassName={css.accountMenuTrigger}
            items={[
              {
                id: 'display-name',
                label: '修改显示名',
                icon: <IconEdit size={15} />,
                onSelect: () => dispatchAccountModal({ type: 'open', modal: 'profile' }),
              },
              {
                id: 'password',
                label: '修改密码',
                icon: <IconKey size={15} />,
                onSelect: () => dispatchAccountModal({ type: 'open', modal: 'password' }),
              },
              me.data?.sso_bound
                ? {
                    id: 'unbind-sso',
                    label: '解除 SSO 绑定',
                    icon: <IconLink size={15} />,
                    danger: true,
                    dividerBefore: true,
                    onSelect: () => dispatchAccountModal({ type: 'open', modal: 'unbindSso' }),
                  }
                : {
                    id: 'bind-sso',
                    label: '绑定 SSO 身份',
                    icon: <IconLink size={15} />,
                    onSelect: () => { window.location.assign('/api/v1/auth/oidc/login?mode=bind') },
                  },
            ]}
          />
        </div>
      </section>

      {/* 解除 SSO 绑定确认 */}
      <DialogWrap
        open={accountModal.modal === 'unbindSso'}
        onOpenChange={(o) => { if (!o) dispatchAccountModal({ type: 'close' }) }}
        title="解除 SSO 绑定"
        description="解除后该 IdP 身份将无法再登录本站；重新登录后可再次绑定。"
        footer={
          <>
            <Button variant="ghost" onClick={() => dispatchAccountModal({ type: 'close' })}>取消</Button>
            <Button
              variant="danger"
              onClick={() => unbindSsoMut.mutate(accountModal.oldPwd)}
              disabled={unbindSsoMut.isPending || !accountModal.oldPwd}
            >
              {unbindSsoMut.isPending ? '解除中…' : '解除绑定'}
            </Button>
          </>
        }
      >
        <FormField label="当前密码" required info="为防止账号被冒用，解除绑定需确认当前登录密码。">
          <Input
            type="password"
            value={accountModal.oldPwd}
            onChange={(e) => dispatchAccountModal({ type: 'setField', field: 'oldPwd', value: e.target.value })}
            autoFocus
          />
        </FormField>
        {accountModal.message ? <Banner intent="error">{accountModal.message}</Banner> : null}
      </DialogWrap>

      {/* 修改显示名 弹窗 */}
      <DialogWrap
        open={accountModal.modal === 'profile'}
        onOpenChange={(o) => { if (!o) dispatchAccountModal({ type: 'close' }) }}
        title="修改显示名"
        footer={
          <>
            <Button variant="ghost" onClick={() => dispatchAccountModal({ type: 'close' })}>
              取消
            </Button>
            <Button
              onClick={onSaveProfile}
              disabled={profileMut.isPending || !accountModal.displayName.trim()}
            >
              保存
            </Button>
          </>
        }
      >
        <FormField label="新的显示名" required error={accountModal.message}>
          <Input
            autoFocus
            value={accountModal.displayName}
            onChange={(e) => dispatchAccountModal({ type: 'setField', field: 'displayName', value: e.target.value })}
            placeholder={me.data?.display_name ?? '显示名'}
          />
        </FormField>
      </DialogWrap>

      {/* 修改密码 弹窗 */}
      <DialogWrap
        open={accountModal.modal === 'password'}
        onOpenChange={(o) => { if (!o) dispatchAccountModal({ type: 'close' }) }}
        title="修改密码"
        description="修改后当前设备保持登录，其他设备的登录会话立即失效。"
        footer={
          <>
            <Button variant="ghost" onClick={() => dispatchAccountModal({ type: 'close' })}>
              取消
            </Button>
            <Button
              onClick={onChangePassword}
              disabled={pwdMut.isPending || !accountModal.oldPwd || accountModal.newPwd.length < 8}
            >
              {pwdMut.isPending ? '修改中…' : '修改密码'}
            </Button>
          </>
        }
      >
        <FormField label="旧密码" required>
          <Input
            type="password"
            autoFocus
            value={accountModal.oldPwd}
            onChange={(e) => dispatchAccountModal({ type: 'setField', field: 'oldPwd', value: e.target.value })}
            autoComplete="current-password"
          />
        </FormField>
        <FormField
          label="新密码（至少 8 位）"
          required
          error={accountModal.message}
        >
          <Input
            type="password"
            value={accountModal.newPwd}
            onChange={(e) => dispatchAccountModal({ type: 'setField', field: 'newPwd', value: e.target.value })}
            autoComplete="new-password"
          />
        </FormField>
      </DialogWrap>

      <SectionCard
        icon={<IconLink size={18} />}
        tone="brand"
        title="应用与客户端连接"
        hint="Token 与 Secret 仅展示一次。"
        bodyPadding="none"
      >
        <nav className={css.credentialOverview} aria-label="连接类型">
          {visibleCredentialViews.map((view) => {
            const status = credentialStatus(view.key)
            return (
              <button
                key={view.key}
                id={`credential-entry-${view.key}`}
                type="button"
                data-credential-row
                className={css.credentialOverviewItem}
                onClick={() => setCredentialView(view.key)}
                aria-label={`管理 ${view.label}`}
                aria-describedby={`credential-status-${view.key}`}
              >
                <span className={css.credentialOverviewIcon} data-kind={view.key} aria-hidden="true">
                  {view.icon}
                </span>
                <span className={css.credentialOverviewCopy}>
                  <strong className={css.credentialOverviewTitle}>{view.label}</strong>
                  <span className={css.credentialOverviewMetaGroup}>
                    <span className={css.credentialOverviewDot} aria-hidden="true" />
                    <span className={css.credentialOverviewMeta}>{view.meta}</span>
                  </span>
                </span>
                <span
                  id={`credential-status-${view.key}`}
                  data-credential-status
                  className={mergeClasses(
                    css.credentialOverviewStatus,
                    status.error
                      ? css.credentialOverviewStatusError
                      : status.active ? css.credentialOverviewStatusActive : css.credentialOverviewStatusMuted,
                  )}
                >
                  {status.active ? <i className={css.statusDotSmall} /> : null}
                  {status.label}
                </span>
                <span className={css.credentialOverviewChevron} data-credential-chevron aria-hidden="true">
                  <IconChevronRight size={18} />
                </span>
              </button>
            )
          })}
        </nav>
        {credentialView === 'static-http' && isAdmin ? (
          <StaticHttpSettingsDialog open onClose={closeCredentialModal} />
        ) : (
          <DialogWrap
            open={credentialView !== null}
            onOpenChange={(open) => { if (!open) closeCredentialModal() }}
            title={credentialViews.find((view) => view.key === credentialView)?.label ?? '管理连接'}
            wide
            footer={<Button variant="secondary" onClick={closeCredentialModal}>完成</Button>}
          >
            <div className={css.credentialModalContent}>
              {credentialView === 'webdav' ? (
                <TokenBlock
                  type="webdav"
                  hint="通过 /dav 挂载文件，使用登录名与此 Token 认证。"
                  status={tokens.data?.webdav}
                  newToken={newTokens.webdav}
                  onReset={(t) => resetMut.mutate(t)}
                  onDismiss={dismissNewToken}
                  pending={tokens.isPending}
                  error={tokens.isError}
                  resetting={resetMut.isPending}
                />
              ) : null}
              {credentialView === 'image-api' ? <ImageBedTokenManager /> : null}
              {credentialView === 's3' ? <S3CredentialManager /> : null}
            </div>
          </DialogWrap>
        )}
      </SectionCard>

      {/* 危险操作 */}
      <SectionCard
        icon={<IconWarning size={18} />}
        tone="danger"
        title="撤销账号"
        hint="永久删除账号及其在当前实例内的全部访问权限。"
        actions={<Button variant="danger" disabled title="即将开放">撤销账号</Button>}
      />
    </div>
  )
}
