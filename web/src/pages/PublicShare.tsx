import { useEffect, useMemo, useState } from 'react'
import { useNavigate, useParams, useSearch } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiRequestError } from '../api/client'
import {
  browsePublicShare,
  fetchPublicShare,
  publicShareArchiveUrl,
  publicShareRawUrl,
  unlockPublicShare,
  type PublicShareInfo,
} from '../api/shares'
import { PublicFrame } from '../components/shell/PublicFrame'
import { FileTable, guessType } from '../components/files/FileTable'
import { Button } from '../components/ui/Button'
import { ExternalLinkButton, LinkButton } from '../components/ui/LinkButton'
import { Tooltip } from '../components/ui/Tooltip'
import { Banner } from '../components/shell/Banner'
import { StateView, type StateTone } from '../components/shell/StateView'
import { Image, Input } from '@fluentui/react-components'
import { PreviewModal } from '../preview/PreviewModal'
import { PreviewContent, type PreviewItem } from '../preview/PreviewContent'
import { resolvePreview } from '../preview/resolver'
import { EntryIcon, IconDownload, IconFolder, IconGrid, IconImage, IconKey, IconList } from '../components/ui/Icon'
import { formatDate } from '../utils/format'
import { useStyles as useFtStyles } from '../components/files/FileTable.styles'
import { useStyles } from './PublicShare.styles'
import { ViewSwitcher } from '../components/shell/ViewSwitcher'

export function PublicSharePage() {
  const css = useStyles()
  const { shareKey } = useParams({ from: '/s/$shareKey' })
  const search = useSearch({ from: '/s/$shareKey' })
  const queryClient = useQueryClient()
  const [password, setPassword] = useState('')
  const [passwordError, setPasswordError] = useState('')
  const info = useQuery({ queryKey: ['public-share', shareKey], queryFn: () => fetchPublicShare(shareKey), retry: false })

  const unlock = useMutation({
    mutationFn: () => unlockPublicShare(shareKey, password),
    onSuccess: async () => {
      setPasswordError('')
      await queryClient.invalidateQueries({ queryKey: ['public-share', shareKey] })
    },
    onError: (error) => setPasswordError(error instanceof ApiRequestError ? error.message : '验证失败，请重试。'),
  })

  if (info.isPending) return <PublicFrame><div className={css.empty}>正在加载分享…</div></PublicFrame>

  // 1.2.0：过期 / 次数用完不再伪装成 404，访客能看到明确状态（docs/design/share-preview-system.md）。
  // 只有带明确业务码的响应才是终态；网络/实例故障保留重试，不能误报「分享不存在」。
  if (info.isError || !info.data) {
    const code = info.error instanceof ApiRequestError ? info.error.code : ''
    if (code === 'SHARE_EXPIRED') {
      return <ShareStatus title="分享已过期" hint="该分享已超过有效期，无法继续访问。" label="有效期已结束" statusHint="请联系分享者重新分享" />
    }
    if (code === 'SHARE_EXHAUSTED') {
      return <ShareStatus title="下载次数已用完" hint="该分享的下载次数已达上限，无法继续访问。" label="下载额度已用完" statusHint="请联系分享者重新分享" />
    }
    if (code === 'FILE_NOT_FOUND') {
      return (
        <ShareStatus
          title="分享不存在或已失效"
          hint="链接可能已被撤销或不存在。"
          label="链接无效"
          tone="neutral"
          statusHint="请向分享者确认链接"
        />
      )
    }
    return (
      <ShareStatus
        title="暂时无法打开此分享"
        hint="无法连接到实例服务，请稍后重试。"
        label="实例服务未响应"
        statusHint="如持续出现，请联系分享者确认"
        action={<Button onClick={() => info.refetch()}>重新加载</Button>}
      />
    )
  }

  if (info.data.protected && !info.data.access_granted) {
    return (
      <PublicFrame>
        <div className={css.unlockWrap}>
          <section className={css.unlockCard}>
            <span className={css.unlockIcon}><IconKey size={28} /></span>
            <h1>此分享受密码保护</h1>
            <form className={css.unlockForm} onSubmit={(event) => { event.preventDefault(); setPasswordError(''); unlock.mutate() }}>
              <Input autoFocus type="password" value={password} onChange={(event) => setPassword(event.target.value)} placeholder="访问密码" aria-label="访问密码" />
              {passwordError ? <Banner intent="error">{passwordError}</Banner> : null}
              <Button type="submit" disabled={unlock.isPending || !password}>{unlock.isPending ? '验证中…' : '查看分享'}</Button>
            </form>
          </section>
        </div>
      </PublicFrame>
    )
  }

  return info.data.type === 'file'
    ? <PublicFile shareKey={shareKey} info={info.data} />
    : <PublicDirectory shareKey={shareKey} info={info.data} path={search.path} page={search.page} />
}

function ShareStatus({
  title,
  hint,
  label,
  statusHint,
  tone = 'warning',
  action,
}: {
  title: string
  hint: string
  label: string
  statusHint?: string
  tone?: StateTone
  action?: React.ReactNode
}) {
  return (
    <PublicFrame>
      <StateView
        kind="error"
        brand
        eyebrow="分享链接 · 访问状态"
        titleAs={1}
        title={title}
        description={hint}
        action={
          <>
            {action}
            <LinkButton variant={action ? 'secondary' : 'primary'} to="/">返回公开网盘</LinkButton>
          </>
        }
        status={{ tone, label, hint: statusHint }}
      />
    </PublicFrame>
  )
}

// —— 文件分享：按统一预览类型内嵌展示（图片/PDF/文本/音视频），不支持则仅下载 ——
// 不可预览时身份块升格为居中舞台：类型砖 + 全名 + meta + 下载就是页面主体，
// 剩余视口成为主体周围的负空间，而不是顶部一行加死白。
function PublicFile({ shareKey, info }: { shareKey: string; info: PublicShareInfo }) {
  const css = useStyles()
  const remaining = shareRemaining(info)
  const previewable = resolvePreview({ name: info.name, size: info.size }) !== 'unsupported'

  if (!previewable) {
    return (
      <PublicFrame>
        <div className={css.stage}>
          <div className={css.stageInner}>
            <EntryIcon name={info.name} type="file" size={96} />
            <h1 className={css.stageTitle}>{info.name}</h1>
            <div className={css.stageMeta}>
              <span>{guessType(info.name)}</span>
              {info.size != null ? <span>{formatShareSize(info.size)}</span> : null}
              <span>{info.expires_at ? `有效至 ${formatDate(info.expires_at)}` : '长期有效'}</span>
              {remaining !== null ? <span>剩余 {remaining} / {info.max_downloads} 次下载</span> : null}
            </div>
            <ExternalLinkButton
              size="large"
              href={publicShareRawUrl(shareKey, '', true)}
              icon={<IconDownload size={18} />}
            >下载文件</ExternalLinkButton>
          </div>
        </div>
      </PublicFrame>
    )
  }

  const item: PreviewItem = {
    name: info.name,
    size: info.size ?? null,
    url: publicShareRawUrl(shareKey, ''),
    downloadUrl: publicShareRawUrl(shareKey, '', true),
  }
  return (
    <PublicFrame>
      <ShareHero info={info}>
        <ExternalLinkButton
          href={publicShareRawUrl(shareKey, '', true)}
          icon={<IconDownload size={16} />}
        >下载文件</ExternalLinkButton>
      </ShareHero>
      <div className={css.embedPreview}>
        <PreviewContent item={item} />
      </div>
    </PublicFrame>
  )
}

// —— 目录分享：列表 / 画廊两种视图 + 目录流式 ZIP 打包下载 ——
function PublicDirectory({ shareKey, info, path, page }: { shareKey: string; info: PublicShareInfo; path: string; page: number }) {
  const ft = useFtStyles()
  const css = useStyles()
  const navigateRoute = useNavigate()
  const [view, setView] = useState<'list' | 'gallery'>('list')
  const [previewState, setPreviewState] = useState<{ items: PreviewItem[]; index: number } | null>(null)
  const browse = useQuery({
    queryKey: ['public-share-browse', shareKey, path, page],
    queryFn: () => browsePublicShare(shareKey, path, page),
    retry: false,
  })
  const segments = useMemo(() => path.split('/').filter(Boolean), [path])
  const entries = browse.data?.items ?? []
  const galleryImages = useMemo(
    () => entries.filter((entry) => entry.type === 'file' && resolvePreview({ name: entry.name, size: entry.size }) === 'image'),
    [entries],
  )
  const remaining = shareRemaining(info)

  // 切换目录时回到列表视图，画廊只对含图片的目录开放。
  useEffect(() => { setView('list') }, [path])

  function navigate(nextPath: string, nextPage = 1) {
    void navigateRoute({
      to: '/s/$shareKey',
      params: { shareKey },
      search: { path: nextPath, page: nextPage },
    })
  }

  function childPath(name: string) {
    return [...segments, name].join('/')
  }

  function previewItemFor(entry: { name: string; size: number }): PreviewItem {
    return {
      name: entry.name,
      size: entry.size,
      url: publicShareRawUrl(shareKey, childPath(entry.name)),
      downloadUrl: publicShareRawUrl(shareKey, childPath(entry.name), true),
    }
  }

  function openPreview(entry: { name: string; size: number }) {
    const files = entries.filter((entry) => entry.type === 'file')
    const items = files.map(previewItemFor)
    const index = Math.max(0, items.findIndex((item) => item.name === entry.name))
    setPreviewState({ items, index })
  }

  function openOrDownload(entry: { name: string; size: number }) {
    if (resolvePreview({ name: entry.name, size: entry.size }) !== 'unsupported') {
      openPreview(entry)
      return
    }
    const link = document.createElement('a')
    link.href = publicShareRawUrl(shareKey, childPath(entry.name), true)
    link.click()
  }

  const zipHint = remaining !== null
    ? `打包下载当前目录将消耗 1 次下载次数（剩余 ${remaining} 次）`
    : '打包下载当前目录'

  return (
    <PublicFrame>
      <ShareHero info={info} />
      <div className={css.toolbar}>
        <nav className={css.crumbs} aria-label="分享目录位置">
          <button className={css.crumbButton} onClick={() => navigate('')}>{info.name}</button>
          {segments.map((segment, index) => {
            const current = index === segments.length - 1
            return <span key={`${segment}-${index}`}> / {current ? <span className={css.currentCrumb}>{segment}</span> : <button className={css.crumbButton} onClick={() => navigate(segments.slice(0, index + 1).join('/'))}>{segment}</button>}</span>
          })}
        </nav>
        <div className={css.toolbarActions}>
          {remaining === null || remaining > 0 ? (
            <ExternalLinkButton
              variant="secondary"
              href={publicShareArchiveUrl(shareKey, path)}
              title={zipHint}
              aria-label="打包下载当前目录 ZIP"
              icon={<IconDownload size={15} />}
            >下载全部 (ZIP)</ExternalLinkButton>
          ) : null}
          <ViewSwitcher
            ariaLabel="视图切换"
            value={view}
            onChange={(next) => setView(next as 'list' | 'gallery')}
            options={[
              { value: 'list', label: '列表', icon: <IconList size={14} /> },
              {
                value: 'gallery',
                label: '画廊',
                icon: <IconGrid size={14} />,
                disabled: galleryImages.length === 0,
                title: galleryImages.length === 0 ? '当前目录没有图片' : undefined,
              },
            ]}
          />
        </div>
      </div>
      {view === 'gallery' && galleryImages.length > 0 ? (
        <div className={css.gallery}>
          {galleryImages.map((entry) => (
            <div className={css.galleryCell} key={entry.name}>
              <button
                type="button"
                className={css.galleryItem}
                onClick={() => openPreview(entry)}
                aria-label={`预览图片 ${entry.name}`}
              >
                <Image
                  className={css.galleryImg}
                  fit="cover"
                  src={publicShareRawUrl(shareKey, childPath(entry.name))}
                  alt={entry.name}
                  loading="lazy"
                />
              </button>
              <span className={css.galleryName}>{entry.name}</span>
            </div>
          ))}
        </div>
      ) : browse.isError ? (
        <StateView
          kind="error"
          icon={<IconFolder size={22} />}
          title="目录暂不可访问"
          description="无法读取这个目录。分享可能已经失效，或实例服务暂时不可用。"
          action={<Button onClick={() => browse.refetch()}>重新加载目录</Button>}
        />
      ) : (
        <FileTable
          entries={entries}
          loading={browse.isPending}
          showType
          emptyTitle="目录为空"
          onOpenDir={(name) => navigate([...segments, name].join('/'))}
          onOpenFile={openOrDownload}
          fileHref={(entry) => publicShareRawUrl(shareKey, childPath(entry.name))}
          renderActions={(entry) => entry.type === 'file' ? (
            <span className={ft.actions}>
              <Tooltip content={resolvePreview({ name: entry.name, size: entry.size }) !== 'unsupported' ? '预览' : '下载'}>
                <button className={ft.actionBtn} onClick={() => openOrDownload(entry)} aria-label={`${resolvePreview({ name: entry.name, size: entry.size }) !== 'unsupported' ? '预览' : '下载'} ${entry.name}`}>
                  <IconImage size={16} />
                </button>
              </Tooltip>
              <Tooltip content="下载">
                <a className={ft.actionBtn} href={publicShareRawUrl(shareKey, childPath(entry.name), true)} aria-label={`下载 ${entry.name}`}><IconDownload size={16} /></a>
              </Tooltip>
            </span>
          ) : null}
        />
      )}
      {view === 'gallery' && galleryImages.length > 0 ? <span className={css.galleryCount}>共 {galleryImages.length} 张图片</span> : null}
      {browse.data && (browse.data.has_next || page > 1) ? (
        <div className={css.pager}>
          <span>共 {browse.data.total} 项</span>
          <Button variant="secondary" disabled={page <= 1} onClick={() => navigate(path, page - 1)}>上一页</Button>
          <Button variant="secondary" disabled={!browse.data.has_next} onClick={() => navigate(path, page + 1)}>下一页</Button>
        </div>
      ) : null}
      {previewState ? (
        <PreviewModal
          items={previewState.items}
          index={previewState.index}
          onIndexChange={(next) => setPreviewState((current) => (current ? { ...current, index: next } : current))}
          onClose={() => setPreviewState(null)}
        />
      ) : null}
    </PublicFrame>
  )
}

function ShareHero({ info, children }: { info: PublicShareInfo; children?: React.ReactNode }) {
  const css = useStyles()
  const remaining = shareRemaining(info)
  return (
    <header className={css.hero}>
      <div className={css.identity}>
        <EntryIcon name={info.name} type={info.type} size={56} />
        <div style={{ minWidth: 0 }}>
          <h1 className={css.title}>{info.name}</h1>
          <div className={css.meta}>
            <span>{info.type === 'dir' ? '共享文件夹' : guessType(info.name)}</span>
            {info.size != null && info.type === 'file' ? <span>{formatShareSize(info.size)}</span> : null}
            <span>{info.expires_at ? `有效至 ${formatDate(info.expires_at)}` : '长期有效'}</span>
            {remaining !== null ? <span>剩余 {remaining} / {info.max_downloads} 次下载</span> : null}
          </div>
        </div>
      </div>
      {children ? <div className={css.actions}>{children}</div> : null}
    </header>
  )
}

function shareRemaining(info: PublicShareInfo): number | null {
  return info.max_downloads > 0 ? Math.max(0, info.max_downloads - info.download_count) : null
}

function formatShareSize(size: number): string {
  if (size < 1024) return `${size} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let value = size / 1024
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit += 1
  }
  return `${value >= 100 ? Math.round(value) : value.toFixed(1)} ${units[unit]}`
}
