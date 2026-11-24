import { useId, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Input } from '@fluentui/react-components'
import { ApiRequestError } from '../../../../api/client'
import { createImageBedToken, deleteImageBedToken, fetchImageBedTokens, type ImageBedToken } from '../../../../api/imagebed'
import { Button } from '../../../../components/ui/Button'
import { DialogWrap } from '../../../../components/ui/Dialog'
import { toastError } from '../../../../components/ui/Toast'
import { FormField } from '../../../../components/shell/FormLayout'
import { StateView } from '../../../../components/shell/StateView'
import { IconPlus, IconTrash } from '../../../../components/ui/Icon'
import { formatDate } from '../../../../utils/format'
import { useStyles } from '../profile.styles'
import { CredentialGroupHeader } from './CredentialGroupHeader'

export function ImageBedTokenManager() {
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
      toastError(error instanceof ApiRequestError ? error.message : '撤销失败')
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
            <Input
              id={revealedTokenInputId}
              className={css.tokenRevealInput}
              readOnly
              autoFocus
              onFocus={(event) => event.currentTarget.select()}
              value={revealedToken}
            />
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
