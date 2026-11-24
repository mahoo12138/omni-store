import { useCallback, useEffect, useMemo, useState, type DragEvent } from 'react'
import { ToggleButton } from '@fluentui/react-components'
import { useNavigate, useParams, useSearch } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  copyFile,
  deleteFile,
  downloadArchive,
  downloadFileUrl,
  fetchMySources,
  fetchPathPermission,
  listFiles,
  moveFile,
  sourceRawUrl,
  type FileEntry,
  type UserSource,
} from '../api/sources'
import { ApiRequestError } from '../api/client'
import { addFavorite, fetchMyFavorites, removeFavorite, type FavoriteItem } from '../api/favorites'
import { AppFrame } from '../components/shell/AppFrame'
import { PageHeader } from '../components/shell/PageHeader'
import { StateView } from '../components/shell/StateView'
import { useFileClipboard, type FileClipboardItem } from '../components/files/FileClipboard'
import { FileTable } from '../components/files/FileTable'
import { Breadcrumb } from '../components/files/Breadcrumb'
import { GridView } from '../components/files/GridView'
import { EntryActionsMenu } from '../components/files/EntryActionsMenu'
import { Button } from '../components/ui/Button'
import type { MenuOption } from '../components/ui/Menu'
import { Select } from '../components/ui/Select'
import { toastError, toastInfo, toastSuccess } from '../components/ui/Toast'
import { Tooltip } from '../components/ui/Tooltip'
import {
  IconChevronLeft,
  IconChevronRight,
  IconCopy,
  IconDownload,
  IconEdit,
  IconExternalLink,
  IconFolderPlus,
  IconGrid,
  IconImage,
  IconLink,
  IconList,
  IconRefresh,
  IconSearch,
  IconScissors,
  IconStar,
  IconTrash,
  IconUpload,
} from '../components/ui/Icon'
import { resolvePreview } from '../preview/resolver'
import { PreviewModal } from '../preview/PreviewModal'
import type { PreviewItem } from '../preview/PreviewContent'
import { readFilePreferences, writeFilePreferences, type FileSortKey, type FileSortOrder } from '../utils/filePreferences'
import { BatchDeleteTaskToastHost, PasteTaskToastHost, UploadTaskToastHost, type BatchDeleteTaskSnapshot, type PasteTaskSnapshot } from './file-manager/TaskToastHosts'
import {
  BatchDeleteDialog,
  BatchShareDialog,
  DeleteDialog,
  DirectoryUploadDialog,
  MkdirDialog,
  RenameDialog,
  ShareDialog,
  UploadConflictDialog,
} from './file-manager/FileDialogs'
import { ClipboardBar, NoSourceView, SelectionToolbar } from './file-manager/FileManagerBars'
import { getPasteDisabledReason, hasDirectoryPicker, pageRange } from './file-manager/fileManagerHelpers'
import { useFileSelection } from './file-manager/useFileSelection'
import { useFileUpload } from './file-manager/useFileUpload'
import { joinPath } from '../utils/filePaths'
import { useStyles } from './FileManager.styles'

// /app/sources/$sourceKey（docs/file.png / file-1.png）：
//   - 有存储源：页头 / 面包屑 / 工具条 / 表格或网格 / 分页
//   - 没有可用存储源：统一空状态
export function FileManagerPage() {
  const { sourceKey } = useParams({ from: '/app/sources/$sourceKey' })
  const sources = useQuery({ queryKey: ['my-sources'], queryFn: fetchMySources })
  const source = sources.data?.find((s) => s.key === sourceKey)

  // 1) 加载中
  if (sources.isPending) {
    return (
      <AppFrame title="文件">
        <StateView kind="pending" title="加载中…" />
      </AppFrame>
    )
  }

  // 2) 无任何存储源或 URL 指定的 sourceKey 不可用
  if (sources.isSuccess && (sources.data.length === 0 || !source)) {
    return (
      <AppFrame title="文件管理">
        <NoSourceView />
      </AppFrame>
    )
  }

  // 3) 正常文件管理视图
  if (!source) return null
  return <FileManagerView key={source.key} source={source} sources={sources.data ?? []} />
}

// --- 主视图 ---

function FileManagerView({ source, sources }: { source: UserSource; sources: UserSource[] }) {
  const css = useStyles()
  const sourceKey = source.key
  const clipboard = useFileClipboard()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const search = useSearch({ from: '/app/sources/$sourceKey' })
  const currentPath = search.path || '/'
  const page = search.page ?? 1

  const [filter, setFilter] = useState('')
  const [preferences, setPreferences] = useState(() => readFilePreferences(sourceKey))
  const { view, pageSize, sort, order } = preferences
  const [pasteTask, setPasteTask] = useState<PasteTaskSnapshot | null>(null)
  const [batchDeleteTask, setBatchDeleteTask] = useState<BatchDeleteTaskSnapshot | null>(null)
  const [archiveDownloading, setArchiveDownloading] = useState(false)
  const closePasteTask = useCallback(() => setPasteTask(null), [])

  // 各种操作弹窗
  const [mkdirOpen, setMkdirOpen] = useState(false)
  const [renameTarget, setRenameTarget] = useState<{ name: string } | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<{ name: string; type: string } | null>(null)
  const [batchDeleteOpen, setBatchDeleteOpen] = useState(false)
  const [batchShareEntries, setBatchShareEntries] = useState<FileEntry[] | null>(null)
  const [shareTarget, setShareTarget] = useState<{ name: string; type: 'file' | 'dir' } | null>(null)
  const [previewState, setPreviewState] = useState<{ items: PreviewItem[]; index: number } | null>(null)

  const permissionQuery = useQuery({
    queryKey: ['source-permission', sourceKey, currentPath],
    queryFn: () => fetchPathPermission(sourceKey, currentPath),
  })
  const currentPermission = permissionQuery.data?.permission ?? 'read_only'
  const canWrite = currentPermission === 'read_write'
  const favoritesQuery = useQuery({ queryKey: ['my-favorites'], queryFn: fetchMyFavorites })
  const favoriteByKey = new Map(
    (favoritesQuery.data ?? []).map((item) => [`${item.source_key}\0${item.path}`, item]),
  )
  const favoriteMutation = useMutation({
    mutationFn: async (variables: { path: string; favorite?: FavoriteItem }) => {
      if (variables.favorite) await removeFavorite(variables.favorite.id)
      else await addFavorite(sourceKey, variables.path)
    },
    onSuccess: (_result, variables) => {
      void queryClient.invalidateQueries({ queryKey: ['my-favorites'] })
      toastSuccess(variables.favorite ? '已取消收藏。' : '已添加到收藏。')
    },
    onError: (error) => toastError(error instanceof ApiRequestError ? error.message : '收藏操作失败，请重试。'),
  })

  function favoriteForEntry(entry: FileEntry): FavoriteItem | undefined {
    const relativePath = joinPath(currentPath, entry.name).replace(/^\/+/, '')
    return favoriteByKey.get(`${sourceKey}\0${relativePath}`)
  }

  function toggleFavorite(entry: FileEntry) {
    favoriteMutation.mutate({ path: joinPath(currentPath, entry.name), favorite: favoriteForEntry(entry) })
  }

  useEffect(() => {
    writeFilePreferences(sourceKey, preferences)
  }, [sourceKey, preferences])

  const filesQuery = useQuery({
    queryKey: ['files', sourceKey, currentPath, page, pageSize, sort, order],
    queryFn: () => listFiles(sourceKey, { path: currentPath, page, pageSize, sort, order }),
  })

  const total = filesQuery.data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / pageSize))

  const entries = useMemo(
    () =>
      filesQuery.data?.items.filter((e) => {
        if (!filter.trim()) return true
        return e.name.toLowerCase().includes(filter.trim().toLowerCase())
      }) ?? [],
    [filesQuery.data, filter],
  )

  const selectionResetKey = `${sourceKey}\0${currentPath}\0${page}\0${pageSize}\0${sort}\0${order}`
  const {
    selectedNames,
    setSelectedNames,
    selectedEntries,
    allVisibleSelected,
    toggleSelected,
    toggleAllSelected,
    clearSelection,
  } = useFileSelection(entries, selectionResetKey)

  const refresh = useCallback((changedSourceKeys: string[] = [sourceKey]) => {
    for (const changedSourceKey of new Set(changedSourceKeys)) {
      queryClient.invalidateQueries({ queryKey: ['files', changedSourceKey] })
      queryClient.invalidateQueries({ queryKey: ['source-quota', changedSourceKey] })
    }
    queryClient.invalidateQueries({ queryKey: ['my-quota'] })
  }, [queryClient, sourceKey])

  const onError = useCallback((err: unknown) => {
    toastError(err instanceof ApiRequestError ? err.message : '操作失败，请重试。')
  }, [])

  const nonUploadTaskMessage = pasteTask?.status === 'running'
    ? '正在粘贴，请稍候…'
    : batchDeleteTask?.status === 'running'
      ? '正在批量删除，请稍候…'
      : archiveDownloading
        ? '正在打包下载，请稍候…'
        : ''

  const upload = useFileUpload({
    sourceKey,
    currentPath,
    canWrite,
    runningTaskMessage: nonUploadTaskMessage,
    onRefresh: refresh,
    onError,
  })

  const runningFileTask = upload.uploadTask?.status === 'running'
    ? '正在上传，请稍候…'
    : nonUploadTaskMessage
  const pasteDisabledReason = runningFileTask || getPasteDisabledReason(clipboard.items, clipboard.operation, sourceKey, currentPath, canWrite)

  function updatePreferences(next: Partial<typeof preferences>) {
    setPreferences((current) => ({ ...current, ...next }))
    clearSelection()
  }

  function changeSort(value: string) {
    const [nextSort, nextOrder] = value.split(':') as [FileSortKey, FileSortOrder]
    updatePreferences({ sort: nextSort, order: nextOrder })
    navigate({ to: '/app/sources/$sourceKey', params: { sourceKey }, search: { path: currentPath, page: 1 } })
  }

  function goTo(seg: string) {
    setFilter('')
    const abs = currentPath === '/' ? `/${seg}` : `${currentPath}/${seg}`
    navigate({ to: '/app/sources/$sourceKey', params: { sourceKey }, search: { path: abs, page: 1 } })
  }

  function previewItemFor(entry: FileEntry): PreviewItem {
    return {
      name: entry.name,
      size: entry.size,
      url: sourceRawUrl(sourceKey, joinPath(currentPath, entry.name)),
    }
  }

  function openPreview(entry: FileEntry) {
    const items = entries.filter((e) => e.type === 'file').map(previewItemFor)
    const index = Math.max(0, items.findIndex((item) => item.name === entry.name))
    setPreviewState({ items, index })
  }

  function openOrDownload(entry: FileEntry) {
    if (resolvePreview({ name: entry.name, size: entry.size }) !== 'unsupported') {
      openPreview(entry)
      return
    }
    const link = document.createElement('a')
    link.href = downloadFileUrl(sourceKey, joinPath(currentPath, entry.name))
    link.click()
  }

  function upOne() {
    const parent = currentPath.replace(/\/[^/]+$/, '') || '/'
    navigate({ to: '/app/sources/$sourceKey', params: { sourceKey }, search: { path: parent, page: 1 } })
  }

  function goPage(p: number) {
    navigate({
      to: '/app/sources/$sourceKey',
      params: { sourceKey },
      search: { path: currentPath, page: p },
    })
  }

  function clipboardItem(entry: FileEntry): FileClipboardItem {
    const path = currentPath === '/' ? `/${entry.name}` : `${currentPath}/${entry.name}`
    return {
      id: `${sourceKey}:${path}`,
      sourceKey,
      sourceName: source.name,
      path,
      name: entry.name,
      type: entry.type === 'dir' ? 'dir' : 'file',
    }
  }

  function copyEntries(entriesToCopy: FileEntry[]) {
    const items = entriesToCopy.filter((entry) => entry.type !== 'unsupported').map(clipboardItem)
    if (items.length === 0) return
    clipboard.copy(items)
    clearSelection()
    toastSuccess(`已复制 ${items.length} 项到剪贴板。`)
  }

  function cutEntries(entriesToCut: FileEntry[]) {
    const items = entriesToCut.filter((entry) => entry.type !== 'unsupported').map(clipboardItem)
    if (items.length === 0) return
    clipboard.cut(items)
    clearSelection()
    toastSuccess(`已剪切 ${items.length} 项到剪贴板。`)
  }

  async function downloadSelectedEntries() {
    if (selectedEntries.length === 0 || archiveDownloading) return
    const paths = selectedEntries.map((entry) => joinPath(currentPath, entry.name))
    setArchiveDownloading(true)
    toastInfo(`正在打包 ${paths.length} 项，请稍候…`)
    try {
      await downloadArchive(sourceKey, paths)
      clearSelection()
      toastSuccess(`已开始下载 ${paths.length} 项。`)
    } catch (error) {
      onError(error)
    } finally {
      setArchiveDownloading(false)
    }
  }

  async function pasteClipboard() {
    const items = clipboard.items
    const operation = clipboard.operation
    const targetPath = currentPath
    if (!operation || items.length === 0 || pasteDisabledReason || pasteTask?.status === 'running') return

    let completed = 0
    let failed = 0
    const errors: string[] = []
    const failedItems: FileClipboardItem[] = []
    setPasteTask({
      status: 'running',
      operation,
      total: items.length,
      completed,
      failed,
      current: items[0]?.name ?? '',
      targetPath,
      errors: [],
    })

    for (const item of items) {
      setPasteTask({ status: 'running', operation, total: items.length, completed, failed, current: item.name, targetPath, errors: [...errors] })
      const destinationPath = joinPath(targetPath, item.name)
      try {
        if (operation === 'copy') await copyFile(item.sourceKey, item.path, sourceKey, destinationPath)
        else await moveFile(item.sourceKey, item.path, sourceKey, destinationPath)
        completed += 1
      } catch (error) {
        failed += 1
        failedItems.push(item)
        errors.push(`${item.name}：${error instanceof ApiRequestError ? error.message : '粘贴失败'}`)
      }
      setPasteTask({ status: 'running', operation, total: items.length, completed, failed, current: item.name, targetPath, errors: [...errors] })
    }

    setPasteTask({
      status: failed > 0 ? 'completed_with_errors' : 'completed',
      operation,
      total: items.length,
      completed,
      failed,
      current: '',
      targetPath,
      errors: [...errors],
    })
    refresh([sourceKey, ...items.map((item) => item.sourceKey)])
    if (operation === 'cut') {
      if (failed === 0) clipboard.clear()
      else clipboard.cut(failedItems)
    } else if (failed > 0) {
      clipboard.copy(failedItems)
    }
  }

  function startInternalDrag(entry: FileEntry, event: DragEvent<HTMLElement>) {
    const names = selectedNames.has(entry.name) ? selectedEntries.map((item) => item.name) : [entry.name]
    event.dataTransfer.setData('application/x-omnistore-file-items', JSON.stringify(names))
  }

  function buildContextMenuItems(entry: FileEntry): MenuOption[] {
    if (entry.type === 'unsupported') return []
    const entryType: 'file' | 'dir' = entry.type
    const items: MenuOption[] = entry.type === 'dir'
      ? [{ id: 'open', label: '打开', icon: <IconExternalLink size={15} />, onSelect: () => goTo(entry.name) }]
      : [{ id: 'download', label: '下载', icon: <IconDownload size={15} />, onSelect: () => {
        const link = document.createElement('a')
        link.href = downloadFileUrl(sourceKey, joinPath(currentPath, entry.name))
        link.click()
      } }]
    if (entry.type === 'file' && resolvePreview({ name: entry.name, size: entry.size }) !== 'unsupported') {
      items.unshift({ id: 'preview', label: '预览', icon: <IconImage size={15} />, onSelect: () => openPreview(entry) })
    }
    items.push({
      id: 'favorite',
      label: favoriteForEntry(entry) ? '取消收藏' : '收藏',
      icon: <IconStar size={15} />,
      onSelect: () => toggleFavorite(entry),
    })
    items.push({ id: 'copy', label: '复制', icon: <IconCopy size={15} />, onSelect: () => copyEntries([entry]) })
    if (canWrite) {
      items.push(
        { id: 'share', label: '创建分享', icon: <IconLink size={15} />, onSelect: () => setShareTarget({ name: entry.name, type: entryType }) },
        { id: 'rename', label: '重命名', icon: <IconEdit size={15} />, onSelect: () => setRenameTarget({ name: entry.name }) },
        { id: 'cut', label: '剪切', icon: <IconScissors size={15} />, onSelect: () => cutEntries([entry]) },
        { id: 'delete', label: '移入回收站', icon: <IconTrash size={15} />, danger: true, onSelect: () => setDeleteTarget({ name: entry.name, type: entry.type }) },
      )
    }
    return items
  }

  async function dropEntriesOnDirectory(directory: FileEntry, event: DragEvent<HTMLElement>) {
    if (!canWrite || directory.type !== 'dir') return
    if (runningFileTask) {
      toastInfo(runningFileTask)
      return
    }

    let names: unknown
    try {
      names = JSON.parse(event.dataTransfer.getData('application/x-omnistore-file-items'))
    } catch {
      names = null
    }
    const draggedNames = Array.isArray(names) ? new Set(names.filter((name): name is string => typeof name === 'string')) : new Set<string>()
    const dragged = entries.filter((entry) => draggedNames.has(entry.name) && entry.type !== 'unsupported')
    if (dragged.length === 0) return

    const targetPath = joinPath(currentPath, directory.name)
    const items = dragged.map(clipboardItem)
    const nestedMoves = items.filter((item) => targetPath === item.path || targetPath.startsWith(`${item.path}/`))
    if (nestedMoves.length > 0) {
      toastError(`不能将“${nestedMoves[0].name}”移动到自身或其子目录。`)
      return
    }

    let completed = 0
    let failed = 0
    const errors: string[] = []
    const failedItems: FileClipboardItem[] = []
    setPasteTask({ status: 'running', operation: 'cut', action: 'drag', total: items.length, completed, failed, current: items[0].name, targetPath, errors: [] })
    for (const item of items) {
      setPasteTask({ status: 'running', operation: 'cut', action: 'drag', total: items.length, completed, failed, current: item.name, targetPath, errors: [...errors] })
      try {
        await moveFile(item.sourceKey, item.path, sourceKey, joinPath(targetPath, item.name))
        completed += 1
      } catch (error) {
        failed += 1
        failedItems.push(item)
        errors.push(`${item.name}：${error instanceof ApiRequestError ? error.message : '移动失败'}`)
      }
      setPasteTask({ status: 'running', operation: 'cut', action: 'drag', total: items.length, completed, failed, current: item.name, targetPath, errors: [...errors] })
    }

    setSelectedNames(new Set(failedItems.map((item) => item.name)))
    refresh()
    setPasteTask({
      status: failed > 0 ? 'completed_with_errors' : 'completed',
      operation: 'cut',
      action: 'drag',
      total: items.length,
      completed,
      failed,
      current: '',
      targetPath,
      errors,
    })
  }

  async function deleteSelectedEntries() {
    if (!canWrite || selectedEntries.length === 0 || batchDeleteTask?.status === 'running') return
    const targets = [...selectedEntries]
    setBatchDeleteOpen(false)
    setBatchDeleteTask({ status: 'running', total: targets.length, completed: 0, failed: 0, current: targets[0].name, errors: [] })

    let completed = 0
    let failed = 0
    const failedNames: string[] = []
    const errors: string[] = []
    for (const entry of targets) {
      setBatchDeleteTask({ status: 'running', total: targets.length, completed, failed, current: entry.name, errors: [...errors] })
      try {
        await deleteFile(sourceKey, joinPath(currentPath, entry.name))
        completed += 1
      } catch (error) {
        failed += 1
        failedNames.push(entry.name)
        errors.push(`${entry.name}：${error instanceof ApiRequestError ? error.message : '移入回收站失败'}`)
      }
      setBatchDeleteTask({ status: 'running', total: targets.length, completed, failed, current: entry.name, errors: [...errors] })
    }

    setSelectedNames(new Set(failedNames))
    refresh()
    setBatchDeleteTask({
      status: failed > 0 ? 'completed_with_errors' : 'completed',
      total: targets.length,
      completed,
      failed,
      current: '',
      errors,
    })
  }

  useEffect(() => {
    function handleClipboardShortcut(event: KeyboardEvent) {
      const target = event.target
      if (target instanceof HTMLElement && target.closest('input, textarea, select, [contenteditable="true"]')) return
      if (!(event.metaKey || event.ctrlKey) || event.altKey) return

      const key = event.key.toLowerCase()
      if (key === 'c' && selectedEntries.length > 0) {
        event.preventDefault()
        copyEntries(selectedEntries)
      } else if (key === 'x' && canWrite && selectedEntries.length > 0) {
        event.preventDefault()
        cutEntries(selectedEntries)
      } else if (key === 'v' && clipboard.items.length > 0 && !pasteDisabledReason && pasteTask?.status !== 'running') {
        event.preventDefault()
        void pasteClipboard()
      }
    }

    window.addEventListener('keydown', handleClipboardShortcut)
    return () => window.removeEventListener('keydown', handleClipboardShortcut)
  }, [canWrite, clipboard.items, pasteDisabledReason, pasteTask?.status, selectedEntries])

  return (
    <AppFrame title={source.name}>
      <div
        className={css.dropSurface}
        onDragEnter={upload.handleDragEnter}
        onDragOver={upload.handleDragOver}
        onDragLeave={upload.handleDragLeave}
        onDrop={(event) => void upload.handleDrop(event)}
      >
        <PageHeader
          title={source.name}
          description={source.description || undefined}
          actions={
            <>
              <Button
                variant="secondary"
                onClick={() => navigate({ to: '/app/sources/$sourceKey/trash', params: { sourceKey } })} icon={<IconTrash size={14} />}>回收站</Button>
              {canWrite && (
                <>
                  <Button onClick={upload.openFilePicker} disabled={Boolean(runningFileTask)} icon={<IconUpload size={14} />}>上传文件</Button>
                  <Button variant="secondary" onClick={upload.openFolderPicker} disabled={Boolean(runningFileTask)} icon={<IconFolderPlus size={14} />}>上传目录</Button>
                  <Button variant="secondary" onClick={() => setMkdirOpen(true)} disabled={Boolean(runningFileTask)} icon={<IconFolderPlus size={14} />}>创建文件夹</Button>
                  <input
                    ref={upload.fileInput}
                    type="file"
                    multiple
                    hidden
                    disabled={Boolean(runningFileTask)}
                    onChange={(e) => upload.startUpload(
                      e.target.files,
                      upload.folderUploadMode.current ? 'directory' : e.target.files?.length === 1 ? 'file' : 'files',
                    )}
                  />
                </>
              )}
            </>
          }
        />

        {upload.uploadTask && (
          <UploadTaskToastHost
            task={upload.uploadTask}
            onCancel={upload.cancelUpload}
            onRetry={upload.retryUpload}
            onClose={upload.closeUploadTask}
          />
        )}

        {pasteTask && (
          <PasteTaskToastHost
            task={pasteTask}
            onClose={closePasteTask}
          />
        )}

        {batchDeleteTask && (
          <BatchDeleteTaskToastHost
            task={batchDeleteTask}
            onClose={() => setBatchDeleteTask(null)}
          />
        )}

        <div className={css.layout}>
          <div className={css.main}>
            {/* 面包屑：存储源列表 / 源名 / 子路径 */}
            <Breadcrumb
              sourceKey={sourceKey}
              sourceName={source.name}
              sources={sources}
              currentPath={currentPath}
              upOne={upOne}
            />

            {/* 工具条：当前位置 / 搜索 / 视图切换 / 刷新 */}
            <div className={css.toolbar}>
              <span className={css.toolbarLocation}>
                当前位置: <strong className={css.toolbarLocationPath}>{currentPath}</strong>
              </span>
              <span className={css.searchBox}>
                <span className={css.searchIcon}>
                  <IconSearch size={14} />
                </span>
                <input
                  className={css.searchInput}
                  placeholder="搜索当前文件夹"
                  value={filter}
                  onChange={(e) => setFilter(e.target.value)}
                />
              </span>
              <Select
                value={`${sort}:${order}`}
                onValueChange={changeSort}
                options={[
                  { value: 'name:asc', label: '名称升序' },
                  { value: 'name:desc', label: '名称降序' },
                  { value: 'size:asc', label: '大小升序' },
                  { value: 'size:desc', label: '大小降序' },
                  { value: 'mtime:asc', label: '修改时间升序' },
                  { value: 'mtime:desc', label: '修改时间降序' },
                ]}
                ariaLabel="排序方式"
                width="content"
              />
              <button className={css.iconBtn} aria-label="刷新" onClick={() => refresh()}>
                <IconRefresh size={16} />
              </button>
              <div className={css.viewToggle}>
                <ToggleButton
                  icon={<IconList size={16} />}
                  aria-label="列表视图"
                  checked={view === 'list'}
                  onClick={() => updatePreferences({ view: 'list' })}
                />
                <ToggleButton
                  icon={<IconGrid size={16} />}
                  aria-label="网格视图"
                  checked={view === 'grid'}
                  onClick={() => updatePreferences({ view: 'grid' })}
                />
              </div>
            </div>

            {selectedEntries.length > 0 ? (
              <SelectionToolbar
                count={selectedEntries.length}
                canDownload={!runningFileTask}
                canCut={canWrite}
                canDelete={canWrite && !runningFileTask}
                canShare={canWrite && !runningFileTask}
                onCopy={() => copyEntries(selectedEntries)}
                onDownload={() => void downloadSelectedEntries()}
                onCut={() => cutEntries(selectedEntries)}
                onShare={() => setBatchShareEntries([...selectedEntries])}
                onDelete={() => setBatchDeleteOpen(true)}
                onClear={clearSelection}
              />
            ) : clipboard.items.length > 0 ? (
              <ClipboardBar
                operation={clipboard.operation!}
                items={clipboard.items}
                canPaste={!pasteDisabledReason && pasteTask?.status !== 'running'}
                disabledReason={pasteDisabledReason}
                onPaste={() => void pasteClipboard()}
                onClear={clipboard.clear}
              />
            ) : null}

            {/* 文件表格 / 网格 */}
            {view === 'list' ? (
              <FileTable
                entries={filesQuery.isError ? [] : entries}
                loading={filesQuery.isPending}
                showType
                emptyTitle={
                  filesQuery.isError
                    ? '加载失败'
                    : filter
                      ? '没有匹配的条目'
                      : '目录为空'
                }
                emptyHint={filesQuery.isError ? '请稍后重试' : undefined}
                onOpenDir={goTo}
                onOpenFile={openOrDownload}
                selectable
                selectedNames={selectedNames}
                allSelected={allVisibleSelected}
                onToggleSelected={toggleSelected}
                onToggleAll={toggleAllSelected}
                onDragEntryStart={canWrite ? startInternalDrag : undefined}
                onDropOnDirectory={canWrite ? (entry, event) => void dropEntriesOnDirectory(entry, event) : undefined}
                contextMenuItems={buildContextMenuItems}
                fileHref={(entry) =>
                  downloadFileUrl(sourceKey, currentPath === '/' ? `/${entry.name}` : `${currentPath}/${entry.name}`)
                }
                renderActions={(entry) => {
                  if (entry.type === 'unsupported') return null
                  if (entry.type === 'file') {
                    return (
                      <span className={css.actions}>
                        <Tooltip content={favoriteForEntry(entry) ? '取消收藏' : '收藏'}>
                          <button
                            className={css.actionBtn}
                            aria-label={`${favoriteForEntry(entry) ? '取消收藏' : '收藏'} ${entry.name}`}
                            aria-pressed={Boolean(favoriteForEntry(entry))}
                            onClick={() => toggleFavorite(entry)}
                            disabled={favoriteMutation.isPending}
                            style={favoriteForEntry(entry) ? { color: 'var(--colorBrandBackground)' } : undefined}
                          >
                            <IconStar size={15} />
                          </button>
                        </Tooltip>
                        <Tooltip content="下载">
                          <a
                            className={css.actionBtn}
                            href={downloadFileUrl(
                              sourceKey,
                              currentPath === '/' ? `/${entry.name}` : `${currentPath}/${entry.name}`,
                            )}
                            aria-label={`下载 ${entry.name}`}
                          >
                            <IconDownload size={15} />
                          </a>
                        </Tooltip>
                        <Tooltip content="复制">
                          <button
                            className={css.actionBtn}
                            aria-label={`复制 ${entry.name}`}
                            onClick={() => copyEntries([entry])}
                          >
                            <IconCopy size={15} />
                          </button>
                        </Tooltip>
                        {canWrite && (
                          <EntryActionsMenu
                            entryName={entry.name}
                            items={[
                              ...(resolvePreview({ name: entry.name, size: entry.size }) !== 'unsupported'
                                ? [{ id: 'preview', label: '预览', icon: <IconImage size={15} />, onSelect: () => openPreview(entry) }]
                                : []),
                              {
                                id: 'share',
                                label: '创建分享',
                                icon: <IconLink size={15} />,
                                onSelect: () => setShareTarget({ name: entry.name, type: 'file' }),
                              },
                              {
                                id: 'rename',
                                label: '重命名',
                                icon: <IconEdit size={15} />,
                                onSelect: () => setRenameTarget({ name: entry.name }),
                              },
                              {
                                id: 'cut',
                                label: '剪切',
                                icon: <IconScissors size={15} />,
                                onSelect: () => cutEntries([entry]),
                              },
                              {
                                id: 'delete',
                                label: '删除',
                                icon: <IconTrash size={15} />,
                                danger: true,
                                onSelect: () => setDeleteTarget({ name: entry.name, type: entry.type }),
                              },
                            ]}
                          />
                        )}
                      </span>
                    )
                  }
                  return (
                    <span className={css.actions}>
                      <Tooltip content={favoriteForEntry(entry) ? '取消收藏' : '收藏'}>
                        <button
                          className={css.actionBtn}
                          aria-label={`${favoriteForEntry(entry) ? '取消收藏' : '收藏'} ${entry.name}`}
                          aria-pressed={Boolean(favoriteForEntry(entry))}
                          onClick={() => toggleFavorite(entry)}
                          disabled={favoriteMutation.isPending}
                          style={favoriteForEntry(entry) ? { color: 'var(--colorBrandBackground)' } : undefined}
                        >
                          <IconStar size={15} />
                        </button>
                      </Tooltip>
                      <Tooltip content="复制">
                        <button
                          className={css.actionBtn}
                          aria-label={`复制 ${entry.name}`}
                          onClick={() => copyEntries([entry])}
                        >
                          <IconCopy size={15} />
                        </button>
                      </Tooltip>
                      {canWrite && (
                        <>
                          <Tooltip content="创建分享">
                            <button
                              className={css.actionBtn}
                              aria-label={`创建分享 ${entry.name}`}
                              onClick={() => setShareTarget({ name: entry.name, type: 'dir' })}
                            >
                              <IconLink size={15} />
                            </button>
                          </Tooltip>
                          <EntryActionsMenu
                            entryName={entry.name}
                            items={[
                              {
                                id: 'rename',
                                label: '重命名',
                                icon: <IconEdit size={15} />,
                                onSelect: () => setRenameTarget({ name: entry.name }),
                              },
                              {
                                id: 'cut',
                                label: '剪切',
                                icon: <IconScissors size={15} />,
                                onSelect: () => cutEntries([entry]),
                              },
                              {
                                id: 'delete',
                                label: '删除',
                                icon: <IconTrash size={15} />,
                                danger: true,
                                onSelect: () => setDeleteTarget({ name: entry.name, type: entry.type }),
                              },
                            ]}
                          />
                        </>
                      )}
                    </span>
                  )
                }}
              />
            ) : (
              <GridView
                entries={entries}
                loading={filesQuery.isPending}
                onOpenDir={goTo}
                onOpenFile={openOrDownload}
                fileHref={(entry) => downloadFileUrl(sourceKey, currentPath === '/' ? `/${entry.name}` : `${currentPath}/${entry.name}`)}
                onDelete={(name, type) => setDeleteTarget({ name, type })}
                onRename={(name) => setRenameTarget({ name })}
                onCopy={(name) => {
                  const entry = entries.find((item) => item.name === name)
                  if (entry) copyEntries([entry])
                }}
                onCut={(name) => {
                  const entry = entries.find((item) => item.name === name)
                  if (entry) cutEntries([entry])
                }}
                onShare={(name, type) => setShareTarget({ name, type })}
                onToggleFavorite={(name) => {
                  const entry = entries.find((item) => item.name === name)
                  if (entry) toggleFavorite(entry)
                }}
                isFavorite={(name) => {
                  const entry = entries.find((item) => item.name === name)
                  return entry ? Boolean(favoriteForEntry(entry)) : false
                }}
                canWrite={canWrite}
                filter={filter}
                selectedNames={selectedNames}
                onToggleSelected={toggleSelected}
                onDragEntryStart={canWrite ? startInternalDrag : undefined}
                onDropOnDirectory={canWrite ? (entry, event) => void dropEntriesOnDirectory(entry, event) : undefined}
                contextMenuItems={buildContextMenuItems}
              />
            )}

            {/* 分页 */}
            {total > 0 && (
              <div className={css.pager}>
                <span className={css.pagerInfo}>
                  共 {total} 个项目 · 第 {page} / {totalPages} 页
                </span>
                <div className={css.pagerNav}>
                  <button
                    className={css.pagerBtn}
                    disabled={page <= 1}
                    onClick={() => goPage(page - 1)}
                    aria-label="上一页"
                  >
                    <IconChevronLeft size={14} />
                  </button>
                  {pageRange(page, totalPages).map((p, i) =>
                    p === '…' ? (
                      <span key={`g${i}`} className={css.pagerEllipsis}>…</span>
                    ) : (
                      <button
                        key={p}
                        className={p === page ? css.pagerBtnActive : css.pagerBtn}
                        onClick={() => goPage(p)}
                      >
                        {p}
                      </button>
                    ),
                  )}
                  <button
                    className={css.pagerBtn}
                    disabled={page >= totalPages}
                    onClick={() => goPage(page + 1)}
                    aria-label="下一页"
                  >
                    <IconChevronRight size={14} />
                  </button>
                  <Select
                    value={String(pageSize)}
                    onValueChange={(nextPageSize) => {
                      updatePreferences({ pageSize: Number(nextPageSize) as 20 | 50 | 100 })
                      navigate({
                        to: '/app/sources/$sourceKey',
                        params: { sourceKey },
                        search: { path: currentPath, page: 1 },
                      })
                    }}
                    options={[
                      { value: '20', label: '20 条/页' },
                      { value: '50', label: '50 条/页' },
                      { value: '100', label: '100 条/页' },
                    ]}
                    ariaLabel="每页条数"
                    width="content"
                  />
                </div>
              </div>
            )}
          </div>
        </div>

        {/* 新建文件夹 */}
        <MkdirDialog
          open={mkdirOpen}
          onOpenChange={setMkdirOpen}
          onCreated={refresh}
          sourceKey={sourceKey}
          currentPath={currentPath}
        />

        <DirectoryUploadDialog
          open={upload.directoryPickerOpen}
          onOpenChange={upload.setDirectoryPickerOpen}
          supportsDirectoryPicker={hasDirectoryPicker()}
          onChoose={() => void upload.chooseDirectory()}
        />

        {upload.uploadConflict && (
          <UploadConflictDialog
            item={upload.uploadConflict.item}
            onDecision={upload.resolveUploadConflict}
          />
        )}

        {/* 重命名 */}
        {renameTarget && (
          <RenameDialog
            sourceKey={sourceKey}
            currentPath={currentPath}
            target={renameTarget}
            onClose={() => setRenameTarget(null)}
            onChanged={refresh}
          />
        )}

        {/* 创建分享 */}
        {shareTarget && (
          <ShareDialog
            sourceKey={sourceKey}
            currentPath={currentPath}
            target={shareTarget}
            onClose={() => setShareTarget(null)}
          />
        )}

        {batchShareEntries && (
          <BatchShareDialog
            sourceKey={sourceKey}
            currentPath={currentPath}
            entries={batchShareEntries}
            onClose={() => setBatchShareEntries(null)}
          />
        )}

        {/* 删除 */}
        {deleteTarget && (
          <DeleteDialog
            sourceKey={sourceKey}
            currentPath={currentPath}
            target={deleteTarget}
            onClose={() => setDeleteTarget(null)}
            onChanged={() => {
              refresh()
              toastSuccess(`已将 ${deleteTarget.name} 移入回收站。`)
            }}
            onError={onError}
          />
        )}

        {batchDeleteOpen && (
          <BatchDeleteDialog
            entries={selectedEntries}
            onClose={() => setBatchDeleteOpen(false)}
            onConfirm={() => void deleteSelectedEntries()}
          />
        )}

        {(upload.fileDragActive || upload.readingDroppedFiles) && (
          <div className={css.dropOverlay} aria-live="polite">
            <div className={css.dropOverlayCard}>
              <IconUpload size={24} />
              <strong className={css.dropOverlayTitle}>{upload.readingDroppedFiles ? '正在读取拖入内容…' : '松开以上传到当前目录'}</strong>
              <span className={css.dropOverlayPath}>{currentPath}</span>
            </div>
          </div>
        )}

        {previewState ? (
          <PreviewModal
            items={previewState.items}
            index={previewState.index}
            onIndexChange={(next) => setPreviewState((current) => (current ? { ...current, index: next } : current))}
            onClose={() => setPreviewState(null)}
          />
        ) : null}
      </div>
    </AppFrame>
  )
}
