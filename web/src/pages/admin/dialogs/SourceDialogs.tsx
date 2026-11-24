import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Tabs } from '../../../components/ui/Tabs'
import { ApiRequestError } from '../../../api/client'
import {
  adminCreateSource,
  adminGetSource,
  adminGetStaticConfig,
  adminPreflightSource,
  adminReconcileSource,
  adminUpdateSource,
  type AdminSource,
  type SourcePreflight,
} from '../../../api/admin'
import { Checkbox, Input, Textarea } from '@fluentui/react-components'
import { Badge } from '../../../components/ui/Badge'
import { Button } from '../../../components/ui/Button'
import { toastError, toastSuccess } from '../../../components/ui/Toast'
import { DialogWrap } from '../../../components/ui/Dialog'
import { FormField } from '../../../components/shell/FormLayout'
import { formatBytes } from '../../../utils/format'
import { useStyles } from '../sections/sources.styles'
import { quotaGiBInputValue } from '../shared'

export function CreateSourceDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
}) {
  const queryClient = useQueryClient()
  const [name, setName] = useState('')
  const [rootPath, setRootPath] = useState('')
  const [err, setErr] = useState('')
  const [preview, setPreview] = useState<SourcePreflight | null>(null)
  const requestedRootPath = useRef('')

  const preflightMutation = useMutation({
    mutationFn: adminPreflightSource,
    onSuccess: (result, variables) => {
      if (requestedRootPath.current === variables.root_path) setPreview(result)
    },
  })

  const createMutation = useMutation({
    mutationFn: adminCreateSource,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['admin-sources'] })
      onOpenChange(false)
    },
  })

  useEffect(() => {
    if (!open) {
      setName('')
      setRootPath('')
      setErr('')
      setPreview(null)
      requestedRootPath.current = ''
    }
  }, [open])

  function validateInput() {
    if (!name.trim()) return '请输入存储源名称'
    if (!rootPath.trim()) return '请输入服务端可访问的目录绝对路径'
    return ''
  }

  function runPreflight() {
    setErr('')
    const validationError = validateInput()
    if (validationError) {
      setErr(validationError)
      return
    }

    setPreview(null)
    requestedRootPath.current = rootPath.trim()
    preflightMutation.mutate(
      { root_path: requestedRootPath.current },
      { onError: (e) => setErr(e instanceof ApiRequestError ? e.message : '目录预检失败') },
    )
  }

  function createSource() {
    setErr('')
    const validationError = validateInput()
    if (validationError) {
      setErr(validationError)
      return
    }
    if (!preview || requestedRootPath.current !== rootPath.trim()) {
      setErr('目录路径已变更，请重新预检')
      return
    }

    createMutation.mutate(
      {
        name: name.trim(),
        description: '',
        root_path: preview.root_path,
        exclude_patterns: preview.exclude_patterns,
        import_existing: !preview.is_empty,
      },
      { onError: (e) => setErr(e instanceof ApiRequestError ? e.message : '创建失败') },
    )
  }

  return (
    <DialogWrap
      open={open}
      onOpenChange={onOpenChange}
      title="新建存储源"
      description={preview ? '目录预检已通过，请核对内容后确认创建。' : '先预检已有目录；通过后再确认创建存储源。'}
      wide
      footer={
        <>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>取消</Button>
          {preview ? (
            <>
              <Button variant="ghost" onClick={runPreflight} disabled={preflightMutation.isPending || createMutation.isPending}>
                重新预检
              </Button>
              <Button onClick={createSource} disabled={createMutation.isPending}>
                {createMutation.isPending ? '校准并创建中…' : preview.is_empty ? '确认创建' : '确认导入并校准'}
              </Button>
            </>
          ) : (
            <Button
              onClick={runPreflight}
              disabled={preflightMutation.isPending || !name.trim() || !rootPath.trim()}
            >
              {preflightMutation.isPending ? '预检中…' : '预检目录'}
            </Button>
          )}
        </>
      }
    >
      <FormField label="名称" required>
        <Input
          autoFocus
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="例如：团队文件"
        />
      </FormField>
      <FormField
        label="真实目录绝对路径"
        required
        error={err}
        hint="Docker 内为容器路径，例如 /data/photos"
      >
        <Input
          value={rootPath}
          onChange={(e) => {
            setRootPath(e.target.value)
            setPreview(null)
            setErr('')
            requestedRootPath.current = ''
          }}
          placeholder="例如 /mnt/photos 或 D:\\Photos"
        />
      </FormField>
      {preview ? <SourcePreflightPreview preview={preview} /> : null}
    </DialogWrap>
  )
}

const sourceEntryKindLabels: Record<SourcePreflight['entries'][number]['kind'], string> = {
  file: '文件',
  directory: '目录',
  symlink: '符号链接',
  unsupported: '不支持',
}

function SourcePreflightPreview({ preview }: { preview: SourcePreflight }) {
  const css = useStyles()
  const summary = preview.summary
  return (
    <section className={css.sourcePreview} aria-label="目录预检结果">
      <div className={css.sourcePreviewHeader}>
        <div>
          <span className={css.sourcePreviewEyebrow}>安全与读写预检通过</span>
          <code className={css.sourcePreviewPath}>{preview.root_path}</code>
        </div>
        <Badge color="green">可导入</Badge>
      </div>
      <div className={css.sourcePreviewStats}>
        <span><strong className={css.sourcePreviewStatValue}>{summary.total_entries}</strong>首层条目</span>
        <span><strong className={css.sourcePreviewStatValue}>{summary.files}</strong>文件</span>
        <span><strong className={css.sourcePreviewStatValue}>{summary.directories}</strong>目录</span>
        <span><strong className={css.sourcePreviewStatValue}>{summary.excluded_entries}</strong>已排除</span>
      </div>
      {preview.entries.length > 0 ? (
        <div className={css.sourcePreviewEntries}>
          {preview.entries.map((entry) => (
            <div className={css.sourcePreviewEntry} key={entry.name}>
              <span className={css.sourcePreviewEntryName} title={entry.name}>{entry.name}</span>
              <Badge color={entry.kind === 'symlink' || entry.kind === 'unsupported' ? 'red' : 'gray'}>
                {sourceEntryKindLabels[entry.kind]}
              </Badge>
            </div>
          ))}
        </div>
      ) : (
        <p className={css.sourcePreviewEmpty}>目录为空。</p>
      )}
      {preview.warnings.length > 0 ? (
        <ul className={css.sourcePreviewWarnings}>
          {preview.warnings.map((warning) => <li key={warning}>{warning}</li>)}
        </ul>
      ) : null}
    </section>
  )
}

// --- 编辑存储源：访问协议与存储设置 ---

export function EditSourceDialog({
  sourceKey,
  onClose,
}: {
  sourceKey: string
  onClose: () => void
}) {
  const css = useStyles()
  const queryClient = useQueryClient()
  const detail = useQuery({ queryKey: ['admin-source', sourceKey], queryFn: () => adminGetSource(sourceKey) })

  const [patterns, setPatterns] = useState<string | null>(null)
  const [webdavOn, setWebdavOn] = useState<boolean | null>(null)
  const [s3On, setS3On] = useState<boolean | null>(null)
  const [staticOn, setStaticOn] = useState<boolean | null>(null)
  const [quotaGiB, setQuotaGiB] = useState<string | null>(null)
  const [msg, setMsg] = useState('')

  // 打开弹窗时，把当前值同步到本地 state
  useEffect(() => {
    if (detail.isSuccess) {
      setPatterns(detail.data.exclude_patterns.join('\n'))
      setWebdavOn(detail.data.source.webdav_enabled)
      setS3On(detail.data.source.s3_enabled)
      setStaticOn(detail.data.source.static_enabled)
      setQuotaGiB(quotaGiBInputValue(detail.data.source.quota_bytes))
    }
  }, [detail.isSuccess, detail.data])

  const saveMut = useMutation({
    mutationFn: async (params: {
      webdavEnabled: boolean
      s3Enabled: boolean
      staticEnabled: boolean
      staticChanged: boolean
      quotaBytes: number
      excludePatterns: string[]
      wasStaticEnabled: boolean
    }) => {
      await adminUpdateSource(sourceKey, {
        webdav_enabled: params.webdavEnabled,
        s3_enabled: params.s3Enabled,
        ...(params.staticChanged ? { static_enabled: params.staticEnabled } : {}),
        quota_bytes: params.quotaBytes,
        exclude_patterns: params.excludePatterns,
      })
      if (params.staticEnabled && !params.wasStaticEnabled) {
        return adminGetStaticConfig(sourceKey)
      }
      return null
    },
    onSuccess: async (enabledConfig) => {
      await refresh()
      if (enabledConfig) {
        const url = enabledConfig.base_url
          || (enabledConfig.public_asset_id ? `${window.location.origin}/assets/${enabledConfig.public_asset_id}/` : '')
        toastSuccess(url ? `已启用静态 HTTP：${url}` : '已启用静态 HTTP')
      }
      onClose()
    },
    onError: (err) => setMsg(err instanceof ApiRequestError ? err.message : '保存失败'),
  })
  const reconcileMut = useMutation({
    mutationFn: () => adminReconcileSource(sourceKey),
    onSuccess: (result) => {
      toastSuccess(`台账已校准：扫描 ${result.scanned_files} 个文件，新增 ${result.added}，更新 ${result.updated}，移除 ${result.removed}`)
      void refresh()
    },
    onError: (err) => toastError(err instanceof ApiRequestError ? err.message : '校准失败'),
  })
  function refresh() {
    return Promise.all([
      queryClient.invalidateQueries({ queryKey: ['admin-source', sourceKey] }),
      queryClient.invalidateQueries({ queryKey: ['admin-sources'] }),
      queryClient.invalidateQueries({ queryKey: ['static-assets-config', sourceKey] }),
    ])
  }

  if (!detail.isSuccess) return null
  const src: AdminSource = detail.data.source
  const patternsValue = patterns ?? detail.data.exclude_patterns.join('\n')
  const quotaValue = quotaGiB ?? quotaGiBInputValue(src.quota_bytes)
  const quotaNumber = Number(quotaValue)
  const quotaBytes = Math.round(quotaNumber * 1024 ** 3)
  const quotaValid = quotaValue.trim() !== ''
    && Number.isFinite(quotaNumber)
    && quotaNumber >= 0
    && Number.isSafeInteger(quotaBytes)
  const webdavEnabled = webdavOn ?? src.webdav_enabled
  const s3Enabled = s3On ?? src.s3_enabled
  const staticEnabled = staticOn ?? src.static_enabled
  const excludePatterns = patternsValue
    .split('\n')
    .map((pattern) => pattern.trim())
    .filter(Boolean)
  const originalPatterns = detail.data.exclude_patterns.map((pattern) => pattern.trim()).filter(Boolean)
  const patternsChanged = excludePatterns.length !== originalPatterns.length
    || excludePatterns.some((pattern, index) => pattern !== originalPatterns[index])
  const staticChanged = staticEnabled !== src.static_enabled
  const isDirty = webdavEnabled !== src.webdav_enabled
    || s3Enabled !== src.s3_enabled
    || staticChanged
    || quotaBytes !== src.quota_bytes
    || patternsChanged
  const formValid = quotaValid

  function saveSettings() {
    setMsg('')
    if (!quotaValid) {
      setMsg('请输入有效的非负配额')
      return
    }
    saveMut.mutate({
      webdavEnabled,
      s3Enabled,
      staticEnabled,
      staticChanged,
      quotaBytes,
      excludePatterns,
      wasStaticEnabled: src.static_enabled,
    })
  }

  return (
    <DialogWrap
      open
      onOpenChange={(o) => { if (!o) onClose() }}
      title={<span className={`omnistore-clamp-2 ${css.sourceConfigTitle}`} title={`配置：${src.name}`}>配置：{src.name}</span>}
      description={<code className={`omnistore-clamp-2 ${css.sourceConfigPath}`} title={src.root_path}>{src.root_path}</code>}
      wide
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>取消</Button>
          <Button
            disabled={!isDirty || !formValid || saveMut.isPending}
            onClick={saveSettings}
          >
            {saveMut.isPending ? '保存中…' : '保存设置'}
          </Button>
        </>
      }
    >
      <Tabs.Root defaultValue="protocols" className={css.sourceConfig}>
        <Tabs.List aria-label="存储源配置">
          <Tabs.Tab value="protocols">访问协议</Tabs.Tab>
          <Tabs.Tab value="storage">存储设置</Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="protocols" keepMounted>
          <div className={css.sourceConfigPanelBody}>
          <div className={css.protocolList}>
            <div className={css.protocolRow}>
              <span className={css.protocolCopy}>
                <span className={css.protocolName}>WebDAV</span>
              </span>
              <Checkbox aria-label="启用 WebDAV" checked={webdavEnabled}
                onChange={(_, data) => { setWebdavOn(!!data.checked); setMsg('') }} />
            </div>
            <div className={css.protocolRow}>
              <span className={css.protocolCopy}>
                <span className={css.protocolName}>S3</span>
              </span>
              <Checkbox aria-label="启用 S3" checked={s3Enabled}
                onChange={(_, data) => { setS3On(!!data.checked); setMsg('') }} />
            </div>
            <div className={css.protocolRow}>
              <span className={css.protocolCopy}>
                <span className={css.protocolName}>静态 HTTP</span>
              </span>
              <Checkbox aria-label="启用静态 HTTP" checked={staticEnabled}
                onChange={(_, data) => { setStaticOn(!!data.checked); setMsg('') }} />
            </div>
          </div>
          </div>
        </Tabs.Panel>
        <Tabs.Panel value="storage" keepMounted>
          <div className={css.sourceConfigPanelBody}>
          <FormField
            label="存储源硬配额"
            hint={`当前已使用 ${formatBytes(detail.data.quota.usage_bytes)}；填 0 表示不限制。配额不会删除已有文件。`}
          >
            <div className={css.sourceQuotaInput}>
              <Input
                type="number"
                min="0"
                step="0.001"
                value={quotaValue}
                onChange={(event) => {
                  setQuotaGiB(event.target.value)
                  setMsg('')
                }}
                aria-label="存储源配额 GiB"
              />
              <span style={{ color: 'var(--colorNeutralForeground2)', fontSize: '13px' }}>GiB</span>
            </div>
          </FormField>

          <FormField
            label="排除规则（每行一条 glob）"
            hint="匹配的文件不会出现在文件列表中，例如 *.tmp 或 .git/*"
          >
            <Textarea
              className={css.formTextarea}
              rows={4}
              resize="vertical"
              aria-label="排除规则"
              value={patternsValue}
              onChange={(e) => {
                setPatterns(e.target.value)
                setMsg('')
              }}
            />
          </FormField>
          <FormField
            label="文件台账"
            hint={isDirty
              ? '请先保存当前配置，再扫描并校准台账。'
              : `当前台账用量 ${formatBytes(detail.data.ledger_usage_bytes)}。校准会扫描真实目录，新发现文件标记为未归属。`}
          >
            <Button variant="secondary" aria-label="扫描并校准台账" disabled={isDirty || reconcileMut.isPending || saveMut.isPending}
              onClick={() => reconcileMut.mutate()}>
              {reconcileMut.isPending ? '校准中…' : '扫描并校准台账'}
            </Button>
          </FormField>
          </div>
        </Tabs.Panel>
      </Tabs.Root>
      {msg && (
        <div
          style={{
            fontSize: '13px',
            color: 'var(--colorNeutralForeground2)',
            textAlign: 'right',
          }}
        >
          {msg}
        </div>
      )}
    </DialogWrap>
  )
}
