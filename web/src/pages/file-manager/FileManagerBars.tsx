import { useNavigate } from '@tanstack/react-router'
import type { FileClipboardItem, FileClipboardOperation } from '../../components/files/FileClipboard'
import { CommandBar, type CommandBarAction } from '../../components/shell/CommandBar'
import { PageHeader } from '../../components/shell/PageHeader'
import { StateView } from '../../components/shell/StateView'
import { Button } from '../../components/ui/Button'
import { IconClipboard, IconCopy, IconDownload, IconLink, IconScissors, IconTrash } from '../../components/ui/Icon'
import { Tooltip } from '../../components/ui/Tooltip'
import { parentPath } from '../../utils/filePaths'
import { useStyles } from '../FileManager.styles'

export function ClipboardBar({
  operation,
  items,
  canPaste,
  disabledReason,
  onPaste,
  onClear,
}: {
  operation: FileClipboardOperation
  items: FileClipboardItem[]
  canPaste: boolean
  disabledReason: string
  onPaste: () => void
  onClear: () => void
}) {
  const css = useStyles()
  const first = items[0]
  const sourceLocation = first ? `${first.sourceName} · ${parentPath(first.path)}` : ''
  const operationLabel = operation === 'copy' ? '已复制' : '已剪切'

  return (
    <section className={css.clipboardBar} role="region" aria-label="文件剪贴板">
      <span className={css.clipboardIcon} aria-hidden="true">
        {operation === 'copy' ? <IconCopy size={17} /> : <IconScissors size={17} />}
      </span>
      <div className={css.clipboardContent}>
        <div className={css.clipboardSummary}>
          <strong>{operationLabel} {items.length} 项</strong>
          <span className={css.clipboardMeta}>来源：{sourceLocation}</span>
        </div>
      </div>
      <div className={css.clipboardActions}>
        {disabledReason ? (
          <Tooltip content={disabledReason} focusable={!canPaste}>
            <Button onClick={onPaste} disabled={!canPaste} icon={<IconClipboard size={14} />}>粘贴到此处</Button>
          </Tooltip>
        ) : (
          <Button onClick={onPaste} disabled={!canPaste} icon={<IconClipboard size={14} />}>粘贴到此处</Button>
        )}
        <Button variant="ghost" onClick={onClear}>清空剪贴板</Button>
      </div>
    </section>
  )
}

export function SelectionToolbar({
  count,
  canDownload,
  canCut,
  canDelete,
  canShare,
  onCopy,
  onDownload,
  onCut,
  onShare,
  onDelete,
  onClear,
}: {
  count: number
  canDownload: boolean
  canCut: boolean
  canDelete: boolean
  canShare: boolean
  onCopy: () => void
  onDownload: () => void
  onCut: () => void
  onShare: () => void
  onDelete: () => void
  onClear: () => void
}) {
  const actions: CommandBarAction[] = [
    { id: 'download', label: '下载', icon: <IconDownload size={14} />, variant: 'secondary', disabled: !canDownload, onClick: onDownload, priority: 5 },
    { id: 'copy', label: '复制', icon: <IconCopy size={14} />, variant: 'secondary', onClick: onCopy, priority: 3 },
  ]
  if (canCut) actions.push({ id: 'cut', label: '剪切', icon: <IconScissors size={14} />, onClick: onCut, priority: 3 })
  if (canShare) actions.push({ id: 'share', label: '创建分享', icon: <IconLink size={14} />, variant: 'secondary', onClick: onShare, priority: 2 })
  if (canCut) {
    actions.push({ id: 'delete', label: '移入回收站', icon: <IconTrash size={14} />, variant: 'dangerGhost', disabled: !canDelete, onClick: onDelete, priority: 5 })
  }
  actions.push({ id: 'clear', label: '取消选择', variant: 'ghost', onClick: onClear, priority: 1 })
  return (
    <CommandBar
      label="批量文件操作"
      status={`已选择 ${count} 项`}
      actions={actions}
    />
  )
}

export function NoSourceView() {
  const navigate = useNavigate()
  return (
    <>
      <PageHeader
        title="文件管理"
        actions={<Button onClick={() => navigate({ to: '/app' })}>切换存储源</Button>}
      />
      <StateView
        kind="unavailable"
        title="你还没有被分配存储源"
        description="请联系系统管理员为你分配存储源，或切换到已有访问权限的存储源。"
        action={<Button onClick={() => navigate({ to: '/app' })}>返回存储源列表</Button>}
      />
    </>
  )
}
