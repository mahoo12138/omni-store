import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { IconDownload, IconFile, IconRefresh } from '../components/ui/Icon'
import { formatBytes } from '../utils/format'
import { extOf, PREVIEW_CAPS, resolvePreview, type PreviewKind } from './resolver'
import { fetchBlob, fetchCappedText } from './content'
import { CodeBlock } from './CodeBlock'
import { ImageLightbox } from './ImageLightbox'
import { MarkdownView } from './MarkdownView'
import * as css from './Preview.classes'

// 预览条目：文件管理器与分享页共用的最小文件描述。
export interface PreviewItem {
  name: string
  size?: number | null
  // 内联内容地址（未登录分享页也适用：cookie 会话随请求自动携带）。
  url: string
  // 下载地址；缺省用 url + download=1。
  downloadUrl?: string
}

function downloadUrlOf(item: PreviewItem): string {
  if (item.downloadUrl) return item.downloadUrl
  return item.url.includes('?') ? `${item.url}&download=1` : `${item.url}?download=1`
}

// 统一预览渲染器（1.2.0 验收点：文件管理器与分享页不得各自实现渲染器）。
// 弹层（PreviewModal）与分享文件页内嵌调用同一渲染器，仅容器尺寸不同。
export function PreviewContent({ item }: { item: PreviewItem }) {
  const kind = useMemo(() => resolvePreview(item), [item])
  const downloadUrl = downloadUrlOf(item)

  switch (kind) {
    case 'image':
      return <ImagePreview item={item} />
    case 'pdf':
      return <PdfPreview url={item.url} downloadUrl={downloadUrl} />
    case 'audio':
      return <MediaPreview url={item.url} kind="audio" />
    case 'video':
      return <MediaPreview url={item.url} kind="video" />
    case 'markdown':
      return <TextualPreview url={item.url} cap={PREVIEW_CAPS.MARKDOWN_BYTES} render={(text) => <MarkdownView text={text} />} />
    case 'json':
      return (
        <TextualPreview
          url={item.url}
          cap={PREVIEW_CAPS.JSON_BYTES}
          render={(text) => {
            try {
              const parsed = JSON.parse(text)
              return <CodeBlock name="x.json" code={JSON.stringify(parsed, null, 2)} />
            } catch {
              return <CodeBlock name="x.json" code={text} />
            }
          }}
        />
      )
    case 'code':
      return <TextualPreview url={item.url} cap={PREVIEW_CAPS.TEXT_BYTES} render={(text) => <CodeBlock name={item.name} code={text} />} />
    case 'yaml':
    case 'text':
      return <TextualPreview url={item.url} cap={PREVIEW_CAPS.TEXT_BYTES} render={(text) => <CodeBlock name={item.name} code={text} />} />
    default:
      return <UnsupportedPreview item={item} downloadUrl={downloadUrl} />
  }
}

// —— 图片：直连流式渲染；SVG 因活动内容限制走 blob 进 <img>（脚本不执行）——
function ImagePreview({ item }: { item: PreviewItem }) {
  const isSvg = extOf(item.name) === 'svg'
  const [blobUrl, setBlobUrl] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [zoomed, setZoomed] = useState(false)
  const [retrySeed, setRetrySeed] = useState(0)

  useEffect(() => {
    if (!isSvg) return
    let revoked: string | null = null
    const ctrl = new AbortController()
    setBlobUrl(null)
    setError(null)
    fetchBlob(item.url, PREVIEW_CAPS.SVG_BLOB_BYTES, ctrl.signal)
      .then(({ blob }) => {
        revoked = URL.createObjectURL(blob)
        setBlobUrl(revoked)
      })
      .catch((err) => {
        if (ctrl.signal.aborted) return
        setError(err instanceof Error ? err.message : '图片加载失败')
      })
    return () => {
      ctrl.abort()
      if (revoked) URL.revokeObjectURL(revoked)
    }
  }, [item.url, isSvg, retrySeed])

  if (error) {
    return <ErrorState hint={error} onRetry={() => setRetrySeed((s) => s + 1)} downloadUrl={downloadUrlOf(item)} />
  }

  const src = isSvg ? blobUrl : item.url
  return (
    <div className={css.stage}>
      {src ? (
        <button
          type="button"
          className={css.zoomBtn}
          onClick={() => setZoomed(true)}
          aria-label="放大查看图片"
          title="点击放大"
        >
          <img className={css.image} src={src} alt={item.name} loading="eager" />
        </button>
      ) : (
        <LoadingState label="正在加载图片…" />
      )}
      {src && zoomed ? (
        <ImageLightbox
          src={src}
          alt={item.name}
          onClose={() => setZoomed(false)}
        />
      ) : null}
    </div>
  )
}

// —— PDF：blob 进 iframe，走浏览器原生查看器 ——
function PdfPreview({ url, downloadUrl }: { url: string; downloadUrl: string }) {
  const [blobUrl, setBlobUrl] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [retrySeed, setRetrySeed] = useState(0)

  useEffect(() => {
    let revoked: string | null = null
    const ctrl = new AbortController()
    setBlobUrl(null)
    setError(null)
    fetchBlob(url, PREVIEW_CAPS.PDF_BLOB_BYTES, ctrl.signal)
      .then(({ blob }) => new Blob([blob], { type: 'application/pdf' }))
      .then((pdfBlob) => {
        revoked = URL.createObjectURL(pdfBlob)
        setBlobUrl(revoked)
      })
      .catch((err) => {
        if (ctrl.signal.aborted) return
        setError(err instanceof Error ? err.message : 'PDF 加载失败')
      })
    return () => {
      ctrl.abort()
      if (revoked) URL.revokeObjectURL(revoked)
    }
  }, [url, retrySeed])

  if (error) return <ErrorState hint={error} onRetry={() => setRetrySeed((s) => s + 1)} downloadUrl={downloadUrl} />
  if (!blobUrl) return <LoadingState label="正在加载 PDF…" />
  return (
    <div className={css.stage}>
      <iframe className={css.pdfFrame} src={blobUrl} title="PDF 预览" />
    </div>
  )
}

// —— 音视频：直连 URL，依赖标准 HTTP Range/206 与浏览器原生播放，无服务端转码 ——
function MediaPreview({ url, kind }: { url: string; kind: 'audio' | 'video' }) {
  const [failed, setFailed] = useState(false)
  if (failed) {
    return <UnsupportedPreview item={{ name: kind === 'audio' ? '音频' : '视频' }} downloadUrl={url} fallbackHint="当前浏览器不支持该编码格式，可下载后播放。" />
  }
  return (
    <div className={css.stage}>
      {kind === 'video' ? (
        <video className={css.media} src={url} controls preload="metadata" playsInline onError={() => setFailed(true)}>
          您的浏览器不支持视频播放。
        </video>
      ) : (
        <audio className={`${css.media} ${css.audio}`} src={url} controls preload="metadata" onError={() => setFailed(true)}>
          您的浏览器不支持音频播放。
        </audio>
      )}
    </div>
  )
}

// —— 文本族（text/code/json/yaml/markdown）：限量读取 + 截断提示 ——
function TextualPreview({ url, cap, render }: { url: string; cap: number; render: (text: string) => ReactNode }) {
  const [state, setState] = useState<{ text: string; truncated: boolean } | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [retrySeed, setRetrySeed] = useState(0)

  useEffect(() => {
    const ctrl = new AbortController()
    setState(null)
    setError(null)
    fetchCappedText(url, cap, ctrl.signal)
      .then(setState)
      .catch((err) => {
        if (ctrl.signal.aborted) return
        setError(err instanceof Error ? err.message : '内容加载失败')
      })
    return () => ctrl.abort()
  }, [url, cap, retrySeed])

  if (error) return <ErrorState hint={error} onRetry={() => setRetrySeed((s) => s + 1)} />
  if (!state) return <LoadingState label="正在加载内容…" />
  return (
    <div className={css.textWrap}>
      {state.truncated ? (
        <div className={css.truncatedBar}>
          文件较大，仅显示前 {formatBytes(cap)}；如需查看完整内容请下载。
        </div>
      ) : null}
      {render(state.text)}
    </div>
  )
}

// —— 不支持预览 → 下载 ——
export function UnsupportedPreview({ item, downloadUrl, fallbackHint }: {
  item: { name?: string; size?: number | null }
  downloadUrl: string
  fallbackHint?: string
}) {
  return (
    <div className={css.stage}>
      <div className={css.fallbackCard}>
        <span className={css.fallbackIcon}><IconFile size={30} /></span>
        <div className={css.fallbackTitle}>{item.name ?? '该文件类型暂不支持预览'}</div>
        {item.name ? <div className={css.fallbackHint}>该文件类型暂不支持在线预览</div> : null}
        {item.size != null ? <div className={css.fallbackHint}>{formatBytes(item.size)}</div> : null}
        {fallbackHint ? <div className={css.fallbackHint}>{fallbackHint}</div> : null}
        <a className={css.downloadBtn} href={downloadUrl}>
          <IconDownload size={16} /> 下载文件
        </a>
      </div>
    </div>
  )
}

function LoadingState({ label }: { label: string }) {
  return (
    <div className={css.stateBox} role="status" aria-live="polite">
      <span className={css.spinner} />
      {label}
    </div>
  )
}

export function ErrorState({ hint, onRetry, downloadUrl }: { hint: string; onRetry?: () => void; downloadUrl?: string }) {
  return (
    <div className={css.stateBox} role="alert">
      <IconFile size={24} />
      <span>{hint}</span>
      <div style={{ display: 'flex', gap: 8 }}>
        {onRetry ? <button type="button" className={css.retryBtn} onClick={onRetry}><IconRefresh size={14} /> 重试</button> : null}
        {downloadUrl ? (
          <a className={css.retryBtn} href={downloadUrl} style={{ textDecoration: 'none', display: 'inline-flex', alignItems: 'center', gap: 4 }}>
            <IconDownload size={14} /> 下载
          </a>
        ) : null}
      </div>
    </div>
  )
}

export function previewKindOf(item: PreviewItem): PreviewKind {
  return resolvePreview(item)
}
