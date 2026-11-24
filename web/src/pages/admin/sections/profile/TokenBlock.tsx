import { useEffect, useId, useState } from 'react'
import { Input } from '@fluentui/react-components'
import { Button } from '../../../../components/ui/Button'
import { DialogWrap } from '../../../../components/ui/Dialog'
import { FormField } from '../../../../components/shell/FormLayout'
import { StateView } from '../../../../components/shell/StateView'
import { formatDate } from '../../../../utils/format'
import { useStyles } from '../profile.styles'
import { CredentialGroupHeader } from './CredentialGroupHeader'

export function TokenBlock({
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
            <div className={css.tokenRevealRow}>
              <Input
                id={tokenInputId}
                className={css.tokenRevealInput}
                readOnly
                autoFocus
                onFocus={(event) => event.currentTarget.select()}
                value={newToken}
              />
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
