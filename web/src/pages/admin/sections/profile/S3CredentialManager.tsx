import { useId, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Input } from '@fluentui/react-components'
import { ApiRequestError } from '../../../../api/client'
import { createS3Credential, deleteS3Credential, fetchS3Credentials, setS3CredentialDisabled, type S3Credential } from '../../../../api/s3'
import { fetchMySources } from '../../../../api/sources'
import { Badge } from '../../../../components/ui/Badge'
import { Button } from '../../../../components/ui/Button'
import { DialogWrap } from '../../../../components/ui/Dialog'
import { toastError } from '../../../../components/ui/Toast'
import { FormField } from '../../../../components/shell/FormLayout'
import { StateView } from '../../../../components/shell/StateView'
import { IconPlus, IconTrash } from '../../../../components/ui/Icon'
import { formatDate } from '../../../../utils/format'
import { useStyles } from '../profile.styles'
import { CredentialGroupHeader } from './CredentialGroupHeader'

export function S3CredentialManager() {
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
    onError: (error) => toastError(error instanceof ApiRequestError ? error.message : '更新失败'),
  })

  const deleteMut = useMutation({
    mutationFn: deleteS3Credential,
    onSuccess: () => {
      setDeleting(null)
      refresh()
    },
    onError: (error) => toastError(error instanceof ApiRequestError ? error.message : '撤销失败'),
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
            <Input
              id={accessKeyInputId}
              className={css.tokenRevealInput}
              readOnly
              autoFocus
              onFocus={(event) => event.currentTarget.select()}
              value={revealed?.accessKeyId ?? ''}
            />
            <Button variant="secondary" onClick={() => navigator.clipboard.writeText(revealed?.accessKeyId ?? '')}>复制</Button>
          </div>
        </FormField>
        <FormField label="Secret Access Key" htmlFor={secretKeyInputId}>
          <div className={css.tokenRevealRow}>
            <Input
              id={secretKeyInputId}
              className={css.tokenRevealInput}
              readOnly
              onFocus={(event) => event.currentTarget.select()}
              value={revealed?.secret ?? ''}
            />
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
