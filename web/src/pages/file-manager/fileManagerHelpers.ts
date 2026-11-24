import type { FileClipboardItem, FileClipboardOperation } from '../../components/files/FileClipboard'
import { isPathInside, normalizePath, parentPath } from '../../utils/filePaths'
import { uploadRelativePath } from '../../utils/uploadTask'

export type DirectoryHandleWithValues = FileSystemDirectoryHandle & {
  values: () => AsyncIterableIterator<FileSystemFileHandle | DirectoryHandleWithValues>
}

export type DirectoryPickerWindow = Window & {
  showDirectoryPicker?: (options?: { mode?: 'read' | 'readwrite' }) => Promise<DirectoryHandleWithValues>
}

export function getDirectoryPickerWindow() {
  return window as DirectoryPickerWindow
}

export function hasDirectoryPicker() {
  return typeof window !== 'undefined' && typeof getDirectoryPickerWindow().showDirectoryPicker === 'function'
}

function withRelativePath(file: File, relativePath: string) {
  const copy = new File([file], file.name, { type: file.type, lastModified: file.lastModified })
  Object.defineProperty(copy, 'webkitRelativePath', { value: relativePath })
  return copy
}

export async function readDirectoryFiles(
  directory: DirectoryHandleWithValues,
  basePath = directory.name,
): Promise<File[]> {
  const files: File[] = []
  for await (const entry of directory.values()) {
    const relativePath = `${basePath}/${entry.name}`
    if (entry.kind === 'directory') {
      files.push(...await readDirectoryFiles(entry as DirectoryHandleWithValues, relativePath))
    } else {
      const file = await (entry as FileSystemFileHandle).getFile()
      files.push(withRelativePath(file, relativePath))
    }
  }
  return files.sort((a, b) => uploadRelativePath(a).localeCompare(uploadRelativePath(b)))
}

export function isAbortError(error: unknown) {
  return error instanceof DOMException && error.name === 'AbortError'
}

export function getPasteDisabledReason(
  items: FileClipboardItem[],
  operation: FileClipboardOperation | null,
  targetSourceKey: string,
  targetPath: string,
  canWrite: boolean,
) {
  if (!operation || items.length === 0) return '剪贴板为空'
  if (!canWrite) return '当前目录只读，无法粘贴'

  const normalizedTarget = normalizePath(targetPath)
  for (const item of items) {
    if (item.sourceKey !== targetSourceKey) continue
    if (normalizedTarget === parentPath(item.path)) {
      return `目标目录与“${item.name}”的来源目录相同`
    }
    if (item.type === 'dir' && isPathInside(normalizedTarget, item.path)) {
      return `不能把“${item.name}”粘贴到自身目录内`
    }
  }
  return ''
}

export function pageRange(current: number, total: number): (number | '…')[] {
  if (total <= 7) return Array.from({ length: total }, (_, i) => i + 1)
  if (current <= 4) return [1, 2, 3, 4, 5, '…', total]
  if (current >= total - 3) return [1, '…', total - 4, total - 3, total - 2, total - 1, total]
  return [1, '…', current - 1, current, current + 1, '…', total]
}
