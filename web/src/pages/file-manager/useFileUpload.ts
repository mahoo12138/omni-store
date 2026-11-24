import { useEffect, useRef, useState, type DragEvent } from 'react'
import { uploadFile } from '../../api/sources'
import { toastError, toastInfo } from '../../components/ui/Toast'
import { collectDroppedUpload } from '../../utils/dropFiles'
import {
  UploadTaskController,
  type UploadConflictDecision,
  type UploadTaskItem,
  type UploadTaskSnapshot,
} from '../../utils/uploadTask'
import { getDirectoryPickerWindow, hasDirectoryPicker, isAbortError, readDirectoryFiles } from './fileManagerHelpers'

interface UseFileUploadOptions {
  sourceKey: string
  currentPath: string
  canWrite: boolean
  runningTaskMessage: string
  onRefresh: () => void
  onError: (err: unknown) => void
}

export function useFileUpload({
  sourceKey,
  currentPath,
  canWrite,
  runningTaskMessage,
  onRefresh,
  onError,
}: UseFileUploadOptions) {
  const fileInput = useRef<HTMLInputElement>(null)
  const folderUploadMode = useRef(false)
  const uploadController = useRef<UploadTaskController | null>(null)
  const [uploadTask, setUploadTask] = useState<UploadTaskSnapshot | null>(null)
  const [directoryPickerOpen, setDirectoryPickerOpen] = useState(false)
  const [fileDragActive, setFileDragActive] = useState(false)
  const [readingDroppedFiles, setReadingDroppedFiles] = useState(false)
  const [uploadConflict, setUploadConflict] = useState<{
    item: UploadTaskItem
    resolve: (decision: UploadConflictDecision) => void
  } | null>(null)

  useEffect(() => {
    return () => uploadController.current?.cancel()
  }, [])

  const activeMessage = uploadTask?.status === 'running' ? '正在上传，请稍候…' : runningTaskMessage

  function startUpload(files: FileList | File[] | null, kind: 'file' | 'files' | 'directory') {
    if (!files?.length) return
    if (!canWrite) {
      toastError('当前目录只读，无法上传文件。')
      return
    }
    if (activeMessage) {
      toastInfo(activeMessage)
      return
    }
    const controller = new UploadTaskController({
      sourceKey,
      targetPath: currentPath,
      files: Array.from(files),
      kind,
      conflictPolicy: 'ask',
      concurrency: 4,
      uploader: (item, options) =>
        uploadFile(sourceKey, currentPath, item.file, {
          relativePath: item.relativePath,
          overwrite: options.overwrite,
          signal: options.signal,
        }),
      resolveConflict: (item) => new Promise<UploadConflictDecision>((resolve) => {
        setUploadConflict({ item, resolve })
      }),
      onChange: setUploadTask,
    })
    uploadController.current = controller
    void controller.start().then(() => {
      onRefresh()
    }).catch(onError)
    if (fileInput.current) fileInput.current.value = ''
    fileInput.current?.removeAttribute('webkitdirectory')
    fileInput.current?.removeAttribute('directory')
    folderUploadMode.current = false
  }

  function handleDragEnter(event: DragEvent<HTMLDivElement>) {
    if (!event.dataTransfer.types.includes('Files')) return
    event.preventDefault()
    if (canWrite) setFileDragActive(true)
  }

  function handleDragOver(event: DragEvent<HTMLDivElement>) {
    if (!event.dataTransfer.types.includes('Files')) return
    event.preventDefault()
    event.dataTransfer.dropEffect = canWrite && !activeMessage ? 'copy' : 'none'
  }

  function handleDragLeave(event: DragEvent<HTMLDivElement>) {
    if (!fileDragActive || event.currentTarget.contains(event.relatedTarget as Node | null)) return
    setFileDragActive(false)
  }

  async function handleDrop(event: DragEvent<HTMLDivElement>) {
    if (!event.dataTransfer.types.includes('Files')) return
    event.preventDefault()
    setFileDragActive(false)
    if (!canWrite) {
      toastError('当前目录只读，无法上传文件。')
      return
    }
    if (activeMessage) {
      toastInfo(activeMessage)
      return
    }
    setReadingDroppedFiles(true)
    try {
      const dropped = await collectDroppedUpload(event.dataTransfer)
      if (dropped.files.length === 0) {
        toastInfo('拖入内容中没有可上传的文件。')
        return
      }
      startUpload(dropped.files, dropped.kind)
    } catch (error) {
      onError(error)
    } finally {
      setReadingDroppedFiles(false)
    }
  }

  function openFilePicker() {
    folderUploadMode.current = false
    fileInput.current?.removeAttribute('webkitdirectory')
    fileInput.current?.removeAttribute('directory')
    fileInput.current?.click()
  }

  function openFolderPicker() {
    setDirectoryPickerOpen(true)
  }

  function openLegacyFolderPicker() {
    folderUploadMode.current = true
    fileInput.current?.setAttribute('webkitdirectory', '')
    fileInput.current?.setAttribute('directory', '')
    fileInput.current?.click()
  }

  async function chooseDirectory() {
    setDirectoryPickerOpen(false)
    if (!hasDirectoryPicker()) {
      openLegacyFolderPicker()
      return
    }
    try {
      const directoryHandle = await getDirectoryPickerWindow().showDirectoryPicker?.({ mode: 'read' })
      if (!directoryHandle) return
      const files = await readDirectoryFiles(directoryHandle)
      if (files.length === 0) {
        toastInfo('所选目录中没有可上传的文件。')
        return
      }
      startUpload(files, 'directory')
    } catch (error) {
      if (isAbortError(error)) return
      onError(error)
    }
  }

  function cancelUpload() {
    uploadController.current?.cancel()
  }

  function retryUpload() {
    void uploadController.current?.retryFailed()
  }

  function closeUploadTask() {
    if (uploadTask?.status !== 'running') {
      uploadController.current = null
      setUploadTask(null)
    }
  }

  function resolveUploadConflict(decision: UploadConflictDecision) {
    if (!uploadConflict) return
    const pending = uploadConflict
    setUploadConflict(null)
    pending.resolve(decision)
  }

  return {
    fileInput,
    folderUploadMode,
    uploadTask,
    directoryPickerOpen,
    setDirectoryPickerOpen,
    fileDragActive,
    readingDroppedFiles,
    uploadConflict,
    startUpload,
    handleDragEnter,
    handleDragOver,
    handleDragLeave,
    handleDrop,
    openFilePicker,
    openFolderPicker,
    chooseDirectory,
    cancelUpload,
    retryUpload,
    closeUploadTask,
    resolveUploadConflict,
  }
}
