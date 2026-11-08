import { useState, type DragEvent, type ReactNode } from 'react'
import {
  Checkbox,
  Table,
  TableBody,
  TableCell,
  TableHeader,
  TableHeaderCell,
  TableRow,
  mergeClasses,
} from '@fluentui/react-components'
import type { FileEntry } from '../../api/sources'
import { EntryIcon } from '../ui/Icon'
import { ContextMenu } from '../ui/ContextMenu'
import type { MenuOption } from '../ui/Menu'
import { StateView } from '../shell/StateView'
import { formatBytes, formatDate } from '../../utils/format'
import { useStyles } from './FileTable.styles'

// 公开侧与私有侧共用的文件表格（docs/index.png）。
// 只负责渲染；导航、下载和写操作由调用方通过回调提供。
// 内核是 Fluent Table（原生 <table> 语义），选择状态完全由调用方受控。
export function FileTable({
  entries,
  loading,
  emptyTitle = '目录为空',
  emptyHint,
  onOpenDir,
  onOpenFile,
  fileHref,
  fileTarget,
  renderActions,
  showType = false,
  selectable = false,
  selectedNames,
  onToggleSelected,
  allSelected = false,
  onToggleAll,
  onDragEntryStart,
  onDropOnDirectory,
  contextMenuItems,
}: {
  entries: FileEntry[] | undefined
  loading?: boolean
  emptyTitle?: string
  emptyHint?: string
  onOpenDir: (name: string) => void
  // 文件名点击回调；传入后文件名渲染为按钮（统一预览入口），未传入时回退 fileHref 链接。
  onOpenFile?: (entry: FileEntry) => void
  // 文件名点击目标；不传则文件名不可点。
  fileHref?: (entry: FileEntry) => string
  // 预览链接可显式新开标签页；下载链接保持在当前页面触发浏览器下载。
  fileTarget?: '_blank'
  renderActions?: (entry: FileEntry) => ReactNode
  // 是否展示"类型"列（私有管理侧使用，按扩展名推断）
  showType?: boolean
  selectable?: boolean
  selectedNames?: ReadonlySet<string>
  onToggleSelected?: (name: string, selected: boolean) => void
  allSelected?: boolean
  onToggleAll?: (selected: boolean) => void
  onDragEntryStart?: (entry: FileEntry, event: DragEvent<HTMLElement>) => void
  onDropOnDirectory?: (entry: FileEntry, event: DragEvent<HTMLElement>) => void
  contextMenuItems?: (entry: FileEntry) => MenuOption[]
}) {
  const css = useStyles()
  const [dropTargetName, setDropTargetName] = useState<string | null>(null)

  // 全选头三态：全部选中为 true，部分选中为 mixed（此前部分选中仍显示未选中）。
  const selectableEntries = entries?.filter((entry) => entry.type !== 'unsupported') ?? []
  const selectedCount = selectableEntries.filter((entry) => selectedNames?.has(entry.name)).length
  const headerChecked: boolean | 'mixed' = allSelected
    ? true
    : selectedCount > 0
      ? (selectedCount === selectableEntries.length ? true : 'mixed')
      : false

  return (
    <div className={css.tableWrap}>
      <Table className={css.table} size="small">
        <TableHeader>
          <TableRow className={selectable ? css.selectableRow : css.mobileRow}>
            {selectable && (
              <TableHeaderCell className={css.selectionTh}>
                <label className={css.checkboxTarget}>
                  <Checkbox
                    aria-label="选择当前页全部条目"
                    checked={headerChecked}
                    onChange={(_, data) => onToggleAll?.(data.checked === true)}
                  />
                </label>
              </TableHeaderCell>
            )}
            <TableHeaderCell className={css.th}>名称</TableHeaderCell>
            {showType && <TableHeaderCell className={css.detailHeading}>类型</TableHeaderCell>}
            <TableHeaderCell className={css.detailHeading}>大小</TableHeaderCell>
            <TableHeaderCell className={css.detailHeading}>修改时间</TableHeaderCell>
            {renderActions && <TableHeaderCell className={css.detailHeading} aria-label="操作" />}
          </TableRow>
        </TableHeader>
        <TableBody>
          {entries?.map((entry) => {
            const selected = selectedNames?.has(entry.name) ?? false
            return (
              <ContextMenu
                key={entry.name}
                ariaLabel={`文件操作 ${entry.name}`}
                items={contextMenuItems?.(entry) ?? []}
                trigger={
                  <TableRow
                    className={mergeClasses(
                      css.row,
                      selectable ? css.selectableRow : css.mobileRow,
                      dropTargetName === entry.name && css.dropTarget,
                    )}
                    appearance={selected ? 'brand' : 'none'}
                    draggable={Boolean(onDragEntryStart && entry.type !== 'unsupported')}
                    onDragStart={onDragEntryStart ? (event) => {
                      if (entry.type === 'unsupported') {
                        event.preventDefault()
                        return
                      }
                      event.dataTransfer.effectAllowed = 'move'
                      event.dataTransfer.setData('application/x-omnistore-file-items', 'internal')
                      onDragEntryStart(entry, event)
                    } : undefined}
                    onDragOver={onDropOnDirectory && entry.type === 'dir' ? (event) => {
                      if (!event.dataTransfer.types.includes('application/x-omnistore-file-items')) return
                      event.preventDefault()
                      event.dataTransfer.dropEffect = 'move'
                      setDropTargetName(entry.name)
                    } : undefined}
                    onDragLeave={onDropOnDirectory && entry.type === 'dir' ? (event) => {
                      if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setDropTargetName(null)
                    } : undefined}
                    onDrop={onDropOnDirectory && entry.type === 'dir' ? (event) => {
                      if (!event.dataTransfer.types.includes('application/x-omnistore-file-items')) return
                      event.preventDefault()
                      setDropTargetName(null)
                      onDropOnDirectory(entry, event)
                    } : undefined}
                  >
                    {selectable && (
                      <TableCell className={css.selectionCell}>
                        <label className={css.checkboxTarget}>
                          <Checkbox
                            aria-label={`选择 ${entry.name}`}
                            checked={selected}
                            disabled={entry.type === 'unsupported'}
                            onChange={(_, data) => onToggleSelected?.(entry.name, data.checked === true)}
                          />
                        </label>
                      </TableCell>
                    )}
                    <TableCell className={css.nameCell}>
                      <span className={css.nameInner}>
                        <EntryIcon name={entry.name} type={entry.type} />
                        {entry.type === 'dir' ? (
                          <button className={css.nameLink} onClick={() => onOpenDir(entry.name)}>
                            {entry.name}
                          </button>
                        ) : entry.type === 'file' && onOpenFile ? (
                          fileHref ? (
                            // 同时给了 href 时保留链接语义（中键/复制链接/新标签仍可用），
                            // 只把左键点击接管成页内预览。
                            <a
                              className={css.nameLink}
                              href={fileHref(entry)}
                              target={fileTarget}
                              rel={fileTarget === '_blank' ? 'noreferrer' : undefined}
                              onClick={(event) => {
                                if (event.metaKey || event.ctrlKey || event.button !== 0) return
                                event.preventDefault()
                                onOpenFile(entry)
                              }}
                            >
                              {entry.name}
                            </a>
                          ) : (
                            <button className={css.nameLink} onClick={() => onOpenFile(entry)}>
                              {entry.name}
                            </button>
                          )
                        ) : entry.type === 'file' && fileHref ? (
                          <a
                            className={css.nameLink}
                            href={fileHref(entry)}
                            target={fileTarget}
                            rel={fileTarget === '_blank' ? 'noreferrer' : undefined}
                          >
                            {entry.name}
                          </a>
                        ) : entry.type === 'file' ? (
                          <span className={css.nameStatic}>{entry.name}</span>
                        ) : (
                          <span className={css.nameMuted}>{entry.name}（符号链接，不支持）</span>
                        )}
                      </span>
                      <span className={css.mobileMeta}>
                        {entry.type === 'file' ? formatBytes(entry.size) : '文件夹'} · {formatDate(entry.mtime)}
                      </span>
                    </TableCell>
                    {showType && <TableCell className={css.detailCell}>{entry.type === 'dir' ? '文件夹' : guessType(entry.name)}</TableCell>}
                    <TableCell className={css.detailCell}>{entry.type === 'file' ? formatBytes(entry.size) : '–'}</TableCell>
                    <TableCell className={css.detailCell}>{formatDate(entry.mtime)}</TableCell>
                    {renderActions && <TableCell className={css.actionsCell}>{renderActions(entry)}</TableCell>}
                  </TableRow>
                }
              />
            )
          })}
        </TableBody>
      </Table>
      {loading && (
        <div aria-busy="true" aria-label="加载中">
          <div className={css.skeletonRow} />
          <div className={css.skeletonRow} />
          <div className={css.skeletonRow} />
        </div>
      )}
      {!loading && entries?.length === 0 && (
        <StateView layout="plain" kind="empty" title={emptyTitle} description={emptyHint} />
      )}
    </div>
  )
}

// 按文件名推断文件类型描述（与 EntryIcon 中的 fileTypeMap / imageExts 保持一致）。
// 导出供分享页等需要「这份文件是什么」一句话描述的地方复用。
export function guessType(name: string): string {
  const dot = name.lastIndexOf('.')
  if (dot < 0) return '文件'
  const ext = name.slice(dot + 1).toLowerCase()
  if (['jpg', 'jpeg', 'png', 'gif', 'webp', 'svg', 'bmp', 'avif'].includes(ext)) {
    return `${ext.toUpperCase()} 图片`
  }
  const map: Record<string, string> = {
    pdf: 'PDF 文档',
    doc: 'Word 文档',
    docx: 'Word 文档',
    xls: 'Excel 表格',
    xlsx: 'Excel 表格',
    csv: 'Excel 表格',
    ppt: 'PPT 演示',
    pptx: 'PPT 演示',
    zip: '压缩包',
    rar: '压缩包',
    '7z': '压缩包',
    gz: '压缩包',
    mp4: 'MP4 视频',
    mkv: 'MKV 视频',
    mov: 'MOV 视频',
    mp3: '音频',
    flac: '音频',
    txt: '文本文档',
    md: 'Markdown',
  }
  return map[ext] || `${ext.toUpperCase()} 文件`
}
