import { useEffect, useId, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiRequestError } from '../../../api/client'
import { changePassword, updateProfile } from '../../../api/admin'
import { fetchMe } from '../../../api/auth'
import { unbindMySso } from '../../../api/sso'
import { createImageBedToken, deleteImageBedToken, fetchImageBedTokens, fetchTokenStatus, resetToken, type ImageBedToken } from '../../../api/imagebed'
import { createS3Credential, deleteS3Credential, fetchS3Credentials, setS3CredentialDisabled, type S3Credential } from '../../../api/s3'
import { fetchMySources } from '../../../api/sources'
import { Input, mergeClasses } from '@fluentui/react-components'
import { Badge } from '../../../components/ui/Badge'
import { Button } from '../../../components/ui/Button'
import { DialogWrap } from '../../../components/ui/Dialog'
import { ExternalLinkButton } from '../../../components/ui/LinkButton'
import { Banner } from '../../../components/shell/Banner'
import { FormField } from '../../../components/shell/FormLayout'
import { StateView } from '../../../components/shell/StateView'
import { toastSuccess } from '../../../components/ui/Toast'
import { IconChevronRight, IconCloud, IconImage, IconKey, IconLink, IconPlus, IconTrash, IconWarning } from '../../../components/ui/Icon'
import { formatDate } from '../../../utils/format'
import { useStyles } from './profile.styles'
import { SectionCard } from '../SectionCard'

const credentialViews = [
  {
    key: 'webdav',
    label: 'WebDAV',
    meta: '桌面挂载',
    icon: <IconLink size={18} />,
  },
  {
    key: 'image-api',
    label: '图床 API',
    meta: '上传客户端',
    icon: <IconImage size={18} />,
  },
  {
    key: 's3',
    label: 'S3',
    meta: 'CLI 与 SDK',
    icon: <IconCloud size={18} />,
  },
] as const

type CredentialView = typeof credentialViews[number]['key']

export function ProfileSection() {
  const css = useStyles()
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const search = useSearch({ strict: false }) as { sso_bound?: string }
  const ssoBoundToastShown = useRef(false)
  const me = useQuery({ queryKey: ['me'], queryFn: fetchMe, retry: false })
  const tokens = useQuery({ queryKey: ['token-status'], queryFn: fetchTokenStatus })
  const s3Credentials = useQuery({ queryKey: ['s3-credentials'], queryFn: fetchS3Credentials })

  const [profileOpen, setProfileOpen] = useState(false)
  const [pwdOpen, setPwdOpen] = useState(false)
  const [unbindSsoOpen, setUnbindSsoOpen] = useState(false)
  const [displayName, setDisplayName] = useState('')
  const [profileMsg, setProfileMsg] = useState('')
  const [oldPwd, setOldPwd] = useState('')
  const [newPwd, setNewPwd] = useState('')
  const [pwdMsg, setPwdMsg] = useState('')
  const [unbindMsg, setUnbindMsg] = useState('')
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
      setProfileMsg('已保存')
      queryClient.invalidateQueries({ queryKey: ['me'] })
      setTimeout(() => { setProfileOpen(false); setProfileMsg('') }, 600)
    },
    onError: (err) => setProfileMsg(err instanceof ApiRequestError ? err.message : '保存失败'),
  })

  const pwdMut = useMutation({
    mutationFn: ({ o, n }: { o: string; n: string }) => changePassword(o, n),
    onSuccess: () => {
      setPwdMsg('密码已修改')
      setOldPwd('')
      setNewPwd('')
      setTimeout(() => { setPwdOpen(false); setPwdMsg('') }, 600)
    },
    onError: (err) => setPwdMsg(err instanceof ApiRequestError ? err.message : '修改失败'),
  })

  const unbindSsoMut = useMutation({
    mutationFn: (password: string) => unbindMySso(password),
    onSuccess: async () => {
      setUnbindSsoOpen(false)
      setOldPwd('')
      setUnbindMsg('')
      await queryClient.invalidateQueries({ queryKey: ['me'] })
      toastSuccess('已解除 SSO 绑定。')
    },
    onError: (err) => setUnbindMsg(err instanceof ApiRequestError ? err.message : '解除绑定失败'),
  })

  const resetMut = useMutation({
    mutationFn: resetToken,
    onSuccess: (data, type) => {
      setNewTokens((prev) => ({ ...prev, [type]: data.token }))
      queryClient.invalidateQueries({ queryKey: ['token-status'] })
    },
    onError: () => alert('重置失败'),
  })

  function dismissNewToken(type: 'webdav') {
    setNewTokens((prev) => ({ ...prev, [type]: '' }))
  }

  function onSaveProfile(e: FormEvent) {
    e.preventDefault()
    if (displayName.trim()) profileMut.mutate(displayName.trim())
  }
  function onChangePassword(e: FormEvent) {
    e.preventDefault()
    pwdMut.mutate({ o: oldPwd, n: newPwd })
  }

  function credentialStatus(view: CredentialView) {
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
          <Button variant="secondary" onClick={() => setProfileOpen(true)}>
            修改显示名
          </Button>
          <Button variant="secondary" onClick={() => setPwdOpen(true)}>
            修改密码
          </Button>
          {me.data?.sso_bound
            ? <Button variant="dangerGhost" onClick={() => setUnbindSsoOpen(true)}>解除 SSO 绑定</Button>
            : <ExternalLinkButton variant="secondary" icon={<IconKey size={16} />} href="/api/v1/auth/oidc/login?mode=bind">绑定 SSO 身份</ExternalLinkButton>}
        </div>
      </section>

      {/* 解除 SSO 绑定确认 */}
      <DialogWrap
        open={unbindSsoOpen}
        onOpenChange={(o) => { setUnbindSsoOpen(o); if (!o) setUnbindMsg('') }}
        title="解除 SSO 绑定"
        description="解除后该 IdP 身份将无法再登录本站；重新登录后可再次绑定。"
        footer={
          <>
            <Button variant="ghost" onClick={() => setUnbindSsoOpen(false)}>取消</Button>
            <Button variant="danger" onClick={() => unbindSsoMut.mutate(oldPwd)} disabled={unbindSsoMut.isPending || !oldPwd}>
              {unbindSsoMut.isPending ? '解除中…' : '解除绑定'}
            </Button>
          </>
        }
      >
        <FormField label="当前密码" required info="为防止账号被冒用，解除绑定需确认当前登录密码。">
          <Input type="password" value={oldPwd} onChange={(e) => setOldPwd(e.target.value)} autoFocus />
        </FormField>
        {unbindMsg ? <Banner intent="error">{unbindMsg}</Banner> : null}
      </DialogWrap>

      {/* 修改显示名 弹窗 */}
      <DialogWrap
        open={profileOpen}
        onOpenChange={(o) => { setProfileOpen(o); if (!o) { setDisplayName(''); setProfileMsg('') } }}
        title="修改显示名"
        footer={
          <>
            <Button variant="ghost" onClick={() => setProfileOpen(false)}>
              取消
            </Button>
            <Button
              onClick={onSaveProfile}
              disabled={profileMut.isPending || !displayName.trim()}
            >
              保存
            </Button>
          </>
        }
      >
        <FormField label="新的显示名" required error={profileMsg}>
          <Input
            autoFocus
            value={displayName}
            onChange={(e) => setDisplayName(e.target.value)}
            placeholder={me.data?.display_name ?? '显示名'}
          />
        </FormField>
      </DialogWrap>

      {/* 修改密码 弹窗 */}
      <DialogWrap
        open={pwdOpen}
        onOpenChange={(o) => { setPwdOpen(o); if (!o) { setOldPwd(''); setNewPwd(''); setPwdMsg('') } }}
        title="修改密码"
        description="修改后当前设备保持登录，其他设备的登录会话立即失效。"
        footer={
          <>
            <Button variant="ghost" onClick={() => setPwdOpen(false)}>
              取消
            </Button>
            <Button
              onClick={onChangePassword}
              disabled={pwdMut.isPending || !oldPwd || newPwd.length < 8}
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
            value={oldPwd}
            onChange={(e) => setOldPwd(e.target.value)}
            autoComplete="current-password"
          />
        </FormField>
        <FormField
          label="新密码（至少 8 位）"
          required
          error={pwdMsg}
        >
          <Input
            type="password"
            value={newPwd}
            onChange={(e) => setNewPwd(e.target.value)}
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
          {credentialViews.map((view) => {
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

function CredentialGroupHeader({
  hint,
  status,
  action,
}: {
  hint: string
  status: React.ReactNode
  action: React.ReactNode
}) {
  const css = useStyles()
  return (
    <header className={css.credentialGroupHeader}>
      <div className={css.credentialGroupCopy}>
        <div className={css.credentialTitleLine}>{status}</div>
        <p className={css.credentialGroupHint}>{hint}</p>
      </div>
      <div className={css.credentialGroupAction}>{action}</div>
    </header>
  )
}

function TokenBlock({
  type,
  hint,
  status,
  newToken,
  onReset,
  onDismiss,
  pending,
  error,
  resetting,
}: {
  type: 'webdav'
  hint: string
  status?: { exists: boolean; created_at?: string | null; last_used_at?: string | null }
  newToken?: string
  onReset: (t: 'webdav') => void
  onDismiss: (t: 'webdav') => void
  pending: boolean
  error: boolean
  resetting: boolean
}) {
  const css = useStyles()
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [showOpen, setShowOpen] = useState(false)
  const tokenInputId = useId()
  useEffect(() => {
    if (newToken) setShowOpen(true)
  }, [newToken])
  const closeTokenReveal = () => {
    setShowOpen(false)
    onDismiss(type)
  }
  return (
    <section className={css.credentialGroup} aria-label="WebDAV">
      <CredentialGroupHeader
        hint={hint}
        status={pending ? (
          <span className={css.statusBadgeMuted}>正在读取</span>
        ) : error ? (
          <span className={css.credentialStatusError}>读取失败</span>
        ) : status?.exists ? (
          <span className={css.statusBadge}><i className={css.statusDotSmall} />已启用</span>
        ) : (
          <span className={css.statusBadgeMuted}>未生成</span>
        )}
        action={(
          <Button
            variant={status?.exists ? 'dangerGhost' : 'primary'}
            disabled={pending || error || resetting}
            onClick={() => status?.exists ? setConfirmOpen(true) : onReset(type)}
          >
            {resetting ? '处理中…' : status?.exists ? '重置 Token' : '生成 Token'}
          </Button>
        )}
      />

      <div className={css.credentialBody}>
        {error ? (
          <StateView layout="plain" kind="error" title="WebDAV 凭据状态读取失败" description="请刷新页面后重试。" />
        ) : (
          <div className={css.credentialFacts}>
            <div className={css.credentialFact}>
              <span className={css.credentialFactLabel}>挂载路径</span>
              <code className={css.credentialFactValue}>/dav</code>
            </div>
            <div className={css.credentialFact}>
              <span className={css.credentialFactLabel}>生成时间</span>
              <span className={css.credentialFactValue}>
                {pending ? '读取中…' : status?.created_at ? formatDate(status.created_at) : '尚未生成'}
              </span>
            </div>
            <div className={css.credentialFact}>
              <span className={css.credentialFactLabel}>最近使用</span>
              <span className={css.credentialFactValue}>
                {pending ? '读取中…' : status?.last_used_at ? formatDate(status.last_used_at) : '从未使用'}
              </span>
            </div>
          </div>
        )}
      </div>

      {/* 重置 Token 确认弹窗 */}
      <DialogWrap
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title="重置 WebDAV"
        description="重置后旧 Token 立即失效，相关客户端需要更新配置。"
        footer={
          <>
            <Button variant="ghost" onClick={() => setConfirmOpen(false)}>
              取消
            </Button>
            <Button
              variant="danger"
              onClick={() => { onReset(type); setConfirmOpen(false) }}
            >
              确认重置
            </Button>
          </>
        }
      >
        <p style={{ margin: 0, fontSize: '13px', color: 'var(--colorNeutralForeground1)' }}>
          确定要重置该 Token 吗？此操作无法撤销。
        </p>
      </DialogWrap>

      {/* 新生成的 Token 展示弹窗 */}
      <DialogWrap
        open={showOpen}
        onOpenChange={(open) => {
          if (open) setShowOpen(true)
          else closeTokenReveal()
        }}
        title="新的 Token"
        description="请立即复制保存，关闭后不再显示。"
        footer={
          <Button variant="secondary" onClick={closeTokenReveal}>
            关闭
          </Button>
        }
      >
        {newToken ? (
          <FormField label="Token" htmlFor={tokenInputId}>
            <div style={{ display: 'flex', gap: 8 }}>
              <Input id={tokenInputId} readOnly value={newToken} />
              <Button
                variant="secondary"
                onClick={() => navigator.clipboard.writeText(newToken)}
              >
                复制
              </Button>
            </div>
          </FormField>
        ) : null}
      </DialogWrap>
    </section>
  )
}

function ImageBedTokenManager() {
  const css = useStyles()
  const queryClient = useQueryClient()
  const tokens = useQuery({ queryKey: ['image-bed-tokens'], queryFn: fetchImageBedTokens })
  const [createOpen, setCreateOpen] = useState(false)
  const [label, setLabel] = useState('')
  const [message, setMessage] = useState('')
  const [revealedToken, setRevealedToken] = useState('')
  const [revealOpen, setRevealOpen] = useState(false)
  const [deleting, setDeleting] = useState<ImageBedToken | null>(null)
  const revealedTokenInputId = useId()

  const refreshTokenQueries = () => {
    void Promise.all([
      queryClient.invalidateQueries({ queryKey: ['image-bed-tokens'] }),
      queryClient.invalidateQueries({ queryKey: ['token-status'] }),
    ])
  }

  const createMut = useMutation({
    mutationFn: createImageBedToken,
    onSuccess: (data) => {
      setCreateOpen(false)
      setLabel('')
      setMessage('')
      setRevealedToken(data.token)
      setRevealOpen(true)
      refreshTokenQueries()
    },
    onError: (error) => {
      setMessage(error instanceof ApiRequestError ? error.message : '创建失败')
    },
  })

  const deleteMut = useMutation({
    mutationFn: deleteImageBedToken,
    onSuccess: () => {
      setDeleting(null)
      refreshTokenQueries()
    },
    onError: (error) => {
      alert(error instanceof ApiRequestError ? error.message : '撤销失败')
    },
  })

  const tokenItems = tokens.data ?? []
  const atLimit = tokenItems.length >= 10

  return (
    <section className={css.credentialGroup} aria-label="图床 API">
      <CredentialGroupHeader
        hint="最多 10 个，明文仅显示一次。"
        status={tokens.isPending ? (
          <span className={css.statusBadgeMuted}>正在读取</span>
        ) : tokens.isError ? (
          <span className={css.credentialStatusError}>读取失败</span>
        ) : tokenItems.length > 0 ? (
          <span className={css.statusBadge}><i className={css.statusDotSmall} />已启用 {tokenItems.length} 个</span>
        ) : (
          <span className={css.statusBadgeMuted}>未生成</span>
        )}
        action={(
          <Button
            disabled={atLimit || tokens.isPending || tokens.isError}
            title={atLimit ? '已达到 10 个 Token 上限' : undefined}
            onClick={() => setCreateOpen(true)} icon={<IconPlus size={14} />}>新建 Token</Button>
        )}
      />

      <div className={css.credentialBody}>
        {tokens.isPending ? <StateView layout="plain" kind="pending" title="正在加载 Token…" /> : null}
        {tokens.isError ? (
          <StateView layout="plain" kind="error" title="Token 列表加载失败" description="请刷新页面后重试。" />
        ) : null}
        {tokens.isSuccess && tokenItems.length === 0 ? (
          <StateView layout="plain" kind="unavailable" title="暂无图床 Token" />
        ) : null}
        {tokenItems.length > 0 ? (
          <div className={css.credentialList} role="list" aria-label="图床 Token 列表">
            {tokenItems.map((token) => (
              <div key={token.token_id} className={css.credentialItem} role="listitem">
                <div className={css.credentialItemIdentity}>
                  <strong className={css.credentialItemName}>{token.label}</strong>
                  <code className={css.credentialItemId}>{token.token_id}</code>
                </div>
                <div className={css.credentialItemMeta}>
                  <span>创建于 {formatDate(token.created_at)}</span>
                  <span>{token.last_used_at ? `最近使用 ${formatDate(token.last_used_at)}` : '从未使用'}</span>
                </div>
                <div className={css.credentialItemActions}>
                  <Button variant="dangerGhost" onClick={() => setDeleting(token)} aria-label={`撤销 ${token.label}`} icon={<IconTrash size={15} />}>撤销</Button>
                </div>
              </div>
            ))}
          </div>
        ) : null}
      </div>

      <DialogWrap
        open={createOpen}
        onOpenChange={(open) => {
          setCreateOpen(open)
          if (!open) {
            setLabel('')
            setMessage('')
          }
        }}
        title="新建图床 Token"
        footer={
          <>
            <Button variant="ghost" onClick={() => setCreateOpen(false)}>取消</Button>
            <Button
              disabled={createMut.isPending || !label.trim()}
              onClick={() => createMut.mutate(label.trim())}
            >
              {createMut.isPending ? '创建中…' : '创建 Token'}
            </Button>
          </>
        }
      >
        <FormField label="Token 名称" required error={message} hint={message ? undefined : '例如：MacBook PicGo'}>
          <Input
            autoFocus
            value={label}
            maxLength={32}
            onChange={(event) => setLabel(event.target.value)}
            placeholder="1-32 个字符"
          />
        </FormField>
      </DialogWrap>

      <DialogWrap
        open={revealOpen}
        onOpenChange={(open) => {
          setRevealOpen(open)
          if (!open) setRevealedToken('')
        }}
        title="新的图床 Token"
        description="请立即复制保存。关闭后无法再次查看明文。"
        footer={<Button variant="secondary" onClick={() => setRevealOpen(false)}>我已保存</Button>}
      >
        <FormField label="Token" htmlFor={revealedTokenInputId}>
          <div className={css.tokenRevealRow}>
            <Input id={revealedTokenInputId} readOnly value={revealedToken} />
            <Button variant="secondary" onClick={() => navigator.clipboard.writeText(revealedToken)}>复制</Button>
          </div>
        </FormField>
      </DialogWrap>

      <DialogWrap
        open={deleting !== null}
        onOpenChange={(open) => { if (!open) setDeleting(null) }}
        title="撤销图床 Token"
        description="撤销后，使用该 Token 的客户端将立即无法上传。"
        footer={
          <>
            <Button variant="ghost" onClick={() => setDeleting(null)}>取消</Button>
            <Button
              variant="danger"
              disabled={deleteMut.isPending}
              onClick={() => deleting && deleteMut.mutate(deleting.token_id)}
            >
              {deleteMut.isPending ? '撤销中…' : '确认撤销'}
            </Button>
          </>
        }
      >
        <p style={{ margin: 0, fontSize: '13px', color: 'var(--colorNeutralForeground1)' }}>
          即将撤销“{deleting?.label}”。其他图床 Token 不受影响。
        </p>
      </DialogWrap>
    </section>
  )
}

function S3CredentialManager() {
  const css = useStyles()
  const queryClient = useQueryClient()
  const credentials = useQuery({ queryKey: ['s3-credentials'], queryFn: fetchS3Credentials })
  const sources = useQuery({ queryKey: ['my-sources'], queryFn: fetchMySources })
  const [createOpen, setCreateOpen] = useState(false)
  const [name, setName] = useState('')
  const [message, setMessage] = useState('')
  const [revealed, setRevealed] = useState<{ accessKeyId: string; secret: string } | null>(null)
  const [deleting, setDeleting] = useState<S3Credential | null>(null)
  const [copiedBucket, setCopiedBucket] = useState<string | null>(null)
  const accessKeyInputId = useId()
  const secretKeyInputId = useId()

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ['s3-credentials'] })
  }

  const createMut = useMutation({
    mutationFn: createS3Credential,
    onSuccess: (data) => {
      setCreateOpen(false)
      setName('')
      setMessage('')
      setRevealed({ accessKeyId: data.item.access_key_id, secret: data.secret_access_key })
      refresh()
    },
    onError: (error) => setMessage(error instanceof ApiRequestError ? error.message : '创建失败'),
  })

  const toggleMut = useMutation({
    mutationFn: ({ accessKeyId, disabled }: { accessKeyId: string; disabled: boolean }) =>
      setS3CredentialDisabled(accessKeyId, disabled),
    onSuccess: refresh,
    onError: (error) => alert(error instanceof ApiRequestError ? error.message : '更新失败'),
  })

  const deleteMut = useMutation({
    mutationFn: deleteS3Credential,
    onSuccess: () => {
      setDeleting(null)
      refresh()
    },
    onError: (error) => alert(error instanceof ApiRequestError ? error.message : '撤销失败'),
  })

  const items = credentials.data ?? []
  const enabledCount = items.reduce((count, item) => count + (item.is_disabled ? 0 : 1), 0)
  const atLimit = items.length >= 10

  function copyBucket(bucket: string) {
    void navigator.clipboard.writeText(bucket).then(() => {
      setCopiedBucket(bucket)
      setTimeout(() => setCopiedBucket((current) => current === bucket ? null : current), 1400)
    })
  }

  return (
    <section className={css.credentialGroup} aria-label="S3 Access Key">
      <CredentialGroupHeader
        hint="最多 10 个，Secret 仅显示一次。"
        status={credentials.isPending ? (
          <span className={css.statusBadgeMuted}>正在读取</span>
        ) : credentials.isError ? (
          <span className={css.credentialStatusError}>读取失败</span>
        ) : enabledCount > 0 ? (
          <span className={css.statusBadge}><i className={css.statusDotSmall} />已启用 {enabledCount} 个</span>
        ) : items.length > 0 ? (
          <span className={css.statusBadgeMuted}>{items.length} 个已禁用</span>
        ) : (
          <span className={css.statusBadgeMuted}>未生成</span>
        )}
        action={(
          <Button
            disabled={atLimit || credentials.isPending || credentials.isError}
            title={atLimit ? '已达到 10 个凭据上限' : undefined}
            onClick={() => setCreateOpen(true)} icon={<IconPlus size={14} />}>新建凭据</Button>
        )}
      />

      <div className={css.credentialBody}>
        {credentials.isPending ? <StateView layout="plain" kind="pending" title="正在加载 S3 凭据…" /> : null}
        {credentials.isError ? (
          <StateView layout="plain" kind="error" title="S3 凭据列表加载失败" description="请刷新页面后重试。" />
        ) : null}
        {credentials.isSuccess && items.length === 0 ? (
          <StateView layout="plain" kind="unavailable" title="暂无 S3 凭据" />
        ) : null}
        {items.length > 0 ? (
          <div className={css.credentialList} role="list" aria-label="S3 凭据列表">
            {items.map((credential) => (
              <div key={credential.access_key_id} className={css.credentialItem} role="listitem">
                <div className={css.credentialItemIdentity}>
                  <strong className={css.credentialItemName}>{credential.name}</strong>
                  <code className={css.credentialItemId}>{credential.access_key_id}</code>
                </div>
                <div className={css.credentialItemMeta}>
                  <Badge color={credential.is_disabled ? 'gray' : 'green'}>
                    {credential.is_disabled ? '已禁用' : '已启用'}
                  </Badge>
                  <span>创建于 {formatDate(credential.created_at)}</span>
                  <span>{credential.last_used_at ? `最近使用 ${formatDate(credential.last_used_at)}` : '从未使用'}</span>
                </div>
                <div className={css.credentialItemActions}>
                  <Button
                    variant="ghost"
                    disabled={toggleMut.isPending}
                    onClick={() => toggleMut.mutate({
                      accessKeyId: credential.access_key_id,
                      disabled: !credential.is_disabled,
                    })}
                  >
                    {credential.is_disabled ? '启用' : '禁用'}
                  </Button>
                  <Button variant="dangerGhost" onClick={() => setDeleting(credential)} aria-label={`撤销 ${credential.name}`} icon={<IconTrash size={15} />}>撤销</Button>
                </div>
              </div>
            ))}
          </div>
        ) : null}
        {sources.isSuccess && sources.data.length > 0 ? (
          <div className={css.credentialSubsection}>
            <div className={css.credentialSubsectionHeader}>
              <h4 className={css.credentialSubsectionTitle}>Bucket 映射</h4>
            </div>
            <div className={css.bucketList} role="list" aria-label="S3 Bucket 映射">
              {sources.data.map((source) => (
                <div key={source.key} className={css.bucketRow} role="listitem">
                  <div className={css.credentialItemIdentity}>
                    <strong className={css.credentialItemName}>{source.name}</strong>
                    <code className={css.credentialItemId}>{source.key}</code>
                  </div>
                  <Button variant="ghost" onClick={() => copyBucket(source.key)}>
                    {copiedBucket === source.key ? '已复制' : '复制 Bucket'}
                  </Button>
                </div>
              ))}
            </div>
          </div>
        ) : null}
      </div>

      <DialogWrap
        open={createOpen}
        onOpenChange={(open) => {
          setCreateOpen(open)
          if (!open) {
            setName('')
            setMessage('')
          }
        }}
        title="新建 S3 凭据"
        footer={
          <>
            <Button variant="ghost" onClick={() => setCreateOpen(false)}>取消</Button>
            <Button disabled={createMut.isPending || !name.trim()} onClick={() => createMut.mutate(name.trim())}>
              {createMut.isPending ? '创建中…' : '创建凭据'}
            </Button>
          </>
        }
      >
        <FormField label="凭据名称" required error={message} hint={message ? undefined : '例如：MacBook rclone'}>
          <Input
            autoFocus
            value={name}
            maxLength={32}
            onChange={(event) => setName(event.target.value)}
            placeholder="1-32 个字符"
          />
        </FormField>
      </DialogWrap>

      <DialogWrap
        open={revealed !== null}
        onOpenChange={(open) => { if (!open) setRevealed(null) }}
        title="新的 S3 凭据"
        description="请立即复制 Access Key ID 与 Secret Access Key。关闭后无法再次查看 Secret。"
        footer={<Button variant="secondary" onClick={() => setRevealed(null)}>我已保存</Button>}
      >
        <FormField label="Access Key ID" htmlFor={accessKeyInputId}>
          <div className={css.tokenRevealRow}>
            <Input id={accessKeyInputId} readOnly value={revealed?.accessKeyId ?? ''} />
            <Button variant="secondary" onClick={() => navigator.clipboard.writeText(revealed?.accessKeyId ?? '')}>复制</Button>
          </div>
        </FormField>
        <FormField label="Secret Access Key" htmlFor={secretKeyInputId}>
          <div className={css.tokenRevealRow}>
            <Input id={secretKeyInputId} readOnly value={revealed?.secret ?? ''} />
            <Button variant="secondary" onClick={() => navigator.clipboard.writeText(revealed?.secret ?? '')}>复制</Button>
          </div>
        </FormField>
      </DialogWrap>

      <DialogWrap
        open={deleting !== null}
        onOpenChange={(open) => { if (!open) setDeleting(null) }}
        title="撤销 S3 凭据"
        description="撤销后，使用该 Access Key 的客户端将立即无法访问。"
        footer={
          <>
            <Button variant="ghost" onClick={() => setDeleting(null)}>取消</Button>
            <Button
              variant="danger"
              disabled={deleteMut.isPending}
              onClick={() => deleting && deleteMut.mutate(deleting.access_key_id)}
            >
              {deleteMut.isPending ? '撤销中…' : '确认撤销'}
            </Button>
          </>
        }
      >
        <p style={{ margin: 0, fontSize: '13px', color: 'var(--colorNeutralForeground1)' }}>
          即将撤销“{deleting?.name}”。其他 S3 凭据不受影响。
        </p>
      </DialogWrap>
    </section>
  )
}
