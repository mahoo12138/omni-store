import type { ReactNode } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { mergeClasses } from '@fluentui/react-components'
import { ApiRequestError } from '../api/client'
import { fetchMe } from '../api/auth'
import { downloadFileUrl, fetchMySources } from '../api/sources'
import { fetchRecentFiles, type RecentFile } from '../api/recentFiles'
import { AppFrame } from '../components/shell/AppFrame'
import { Banner } from '../components/shell/Banner'
import { Button } from '../components/ui/Button'
import { LinkButton } from '../components/ui/LinkButton'
import { openSearchDialog } from '../components/search/SearchDialog'
import {
  EntryIcon,
  IconDownload,
  IconFolder,
  IconSearch,
} from '../components/ui/Icon'
import { formatBytes, formatDate, formatRelativeTime } from '../utils/format'
import { layout } from '../styles/layout'
import { useStyles } from './AppHome.styles'
import { DriveConstellation, EmptyTray } from './home/HomeIllustrations'
import { LoopingSourceList } from './home/LoopingSourceList'

// /app 首页：报头（日期 / 问候 / 命令搜索 / 等距几何）+ 01 存储源目录牌
// + 02 最近文件台账。无存储源时换成整页引导，不展示注定为空的台账。
//
// 约定：存储源行的可访问名固定为「打开存储源 {name}」，最近文件 section 由
// 「最近文件」h2 命名，行内保留文件名按钮、位置按钮与「下载 {name}」链接，
// e2e 与键盘路径都靠这三条语义定位。

const IS_MAC = typeof navigator !== 'undefined' && /Mac|iPhone|iPad|iPod/i.test(navigator.userAgent)

export function AppHomePage() {
  const css = useStyles()
  const navigate = useNavigate()
  const me = useQuery({ queryKey: ['me'], queryFn: fetchMe, retry: false })
  const sources = useQuery({ queryKey: ['my-sources'], queryFn: fetchMySources })
  const recent = useQuery({ queryKey: ['recent-files'], queryFn: () => fetchRecentFiles(50) })

  const sourceList = sources.data ?? []
  const isAdmin = me.data?.role === 'super_admin'
  const now = new Date()
  const displayName = me.data?.display_name
  const onboarding = sources.isSuccess && sourceList.length === 0

  function openSource(sourceKey: string) {
    navigate({
      to: '/app/sources/$sourceKey',
      params: { sourceKey },
      search: { path: '/', page: 1 },
    })
  }

  function openRecentParent(sourceKey: string, filePath: string) {
    const parent = filePath.includes('/') ? filePath.slice(0, filePath.lastIndexOf('/')) : ''
    navigate({
      to: '/app/sources/$sourceKey',
      params: { sourceKey },
      search: { path: parent ? `/${parent}` : '/', page: 1 },
    })
  }

  return (
    <AppFrame title="文件">
      <header className={css.pageHeader}>
        <div className={css.headerCopy}>
          <p className={mergeClasses(css.kicker, css.riseKicker)}>
            <time dateTime={now.toISOString()}>{formatKickerDate(now)}</time>
          </p>
          <span className={css.titleMask}>
            <h1 className={css.pageTitle}>{displayName ? `${timeGreeting(now)}，${displayName}` : '我的文件'}</h1>
          </span>
          <button
            type="button"
            className={css.commandBar}
            aria-label="搜索文件与目录"
            aria-haspopup="dialog"
            onClick={openSearchDialog}
          >
            <IconSearch size={16} />
            <span className={css.commandText}>搜索文件与目录</span>
            <kbd className={css.commandKbd} aria-hidden="true">{IS_MAC ? '⌘ K' : 'Ctrl K'}</kbd>
          </button>
        </div>
        <DriveConstellation className={css.stage} />
      </header>

      {onboarding ? (
        <NoSourcePanel isAdmin={isAdmin} />
      ) : (
        <div className={css.workspace}>
          <section className={css.section} aria-labelledby="sources-title">
            <SectionHeading
              index="01"
              title="存储源"
              titleId="sources-title"
              meta={sources.isSuccess ? `${sourceList.length} 个可用` : undefined}
            />
            {sources.isPending ? (
              <SourceListLoading />
            ) : sources.isError ? (
              <Banner
                intent="error"
                title="无法加载存储源"
                action={<Button variant="secondary" onClick={() => sources.refetch()}>重新加载</Button>}
              >请检查网络连接后重试。</Banner>
            ) : (
              <LoopingSourceList
                sources={sourceList}
                onOpen={openSource}
              />
            )}
          </section>

          <RecentFilesPanel
            files={recent.data}
            pending={recent.isPending}
            error={recent.error}
            onRetry={() => recent.refetch()}
            onOpenParent={openRecentParent}
          />
        </div>
      )}
    </AppFrame>
  )
}

function SectionHeading({
  index,
  title,
  titleId,
  meta,
}: {
  index: string
  title: string
  titleId: string
  meta?: ReactNode
}) {
  const css = useStyles()
  return (
    <div className={css.sectionHeader}>
      <span className={css.sectionIndex} aria-hidden="true">{index}</span>
      <h2 id={titleId} className={css.sectionTitle}>{title}</h2>
      <span className={css.sectionRule} aria-hidden="true" />
      {meta ? <span className={css.sectionMeta}>{meta}</span> : null}
    </div>
  )
}

function SourceListLoading() {
  const css = useStyles()
  return (
    <div className={css.sourceViewport} role="status" aria-busy="true" aria-label="正在加载存储源">
      <div className={css.sourceList}>
        {[0, 1].map((item) => (
          <div className={css.sourceSkeletonPlate} key={item} aria-hidden="true">
            <span className={css.skeletonGlyph} />
            <span className={css.skeletonName} />
            <span className={css.skeletonLine} />
            <span className={css.skeletonFoot} />
          </div>
        ))}
      </div>
    </div>
  )
}

function NoSourcePanel({ isAdmin }: { isAdmin: boolean }) {
  const css = useStyles()
  return (
    <section className={css.section} aria-labelledby="sources-title">
      <SectionHeading
        index="01"
        title="存储源"
        titleId="sources-title"
        meta={isAdmin ? '尚未配置' : '暂无可用'}
      />
      <div className={css.onboardingPanel}>
        <div className={css.onboardingCopy}>
          <h3 className={css.onboardingTitle}>
            {isAdmin ? '创建第一个存储源' : '还没有可用的存储源'}
          </h3>
          {isAdmin ? null : (
            <p className={css.onboardingText}>请联系系统管理员分配访问权限。</p>
          )}
          {isAdmin ? (
            <div className={css.onboardingActions}>
              <LinkButton to="/app/admin" search={{ section: 'sources' }} size="large">配置存储源</LinkButton>
            </div>
          ) : null}
        </div>
        <EmptyTray className={css.onboardingArt} />
      </div>
    </section>
  )
}

function RecentFilesPanel({
  files,
  pending,
  error,
  onRetry,
  onOpenParent,
}: {
  files: RecentFile[] | undefined
  pending: boolean
  error: unknown
  onRetry: () => void
  onOpenParent: (sourceKey: string, filePath: string) => void
}) {
  const css = useStyles()
  const items = files ?? []
  return (
    <section className={css.section} aria-labelledby="recent-title">
      <SectionHeading
        index="02"
        title="最近文件"
        titleId="recent-title"
        meta={pending || error ? undefined : `${items.length} / 50`}
      />
      {pending ? (
        <div className={css.recentList} role="status" aria-busy="true" aria-label="正在读取最近文件">
          {[0, 1, 2, 3].map((item) => (
            <div className={css.recentSkeletonRow} key={item} aria-hidden="true">
              <span className={css.recentSkeletonIcon} />
              <span className={css.recentSkeletonLines}><i /><i /></span>
              <span className={css.recentSkeletonMeta} />
            </div>
          ))}
        </div>
      ) : error ? (
        <Banner
          intent="error"
          title="最近文件加载失败"
          action={<Button variant="secondary" onClick={onRetry}>重新加载</Button>}
        >{error instanceof ApiRequestError ? error.message : '请检查连接后重试。'}</Banner>
      ) : items.length === 0 ? (
        <RecentEmpty />
      ) : (
        <div className={css.recentList}>
          {items.map((file, index) => {
            const parent = file.path.includes('/') ? file.path.slice(0, file.path.lastIndexOf('/')) : ''
            return (
              <article
                className={css.recentRow}
                key={`${file.source_key}:${file.path}`}
                style={{ animationDelay: `${160 + Math.min(index, 8) * 35}ms` }}
              >
                <span className={css.recentIcon} data-recent-icon aria-hidden="true">
                  <EntryIcon name={file.name} type="file" size={layout.appHomeRecentIconPx} />
                </span>
                <button
                  type="button"
                  className={css.recentFileName}
                  onClick={() => onOpenParent(file.source_key, file.path)}
                >
                  {file.name}
                </button>
                <button
                  type="button"
                  className={css.recentLocation}
                  onClick={() => onOpenParent(file.source_key, file.path)}
                >
                  <IconFolder size={12} />
                  <span className={css.recentSource}>{file.source_name}</span>
                  {parent ? <span className={css.recentPath}>/{parent}</span> : null}
                </button>
                <span className={css.recentMeta}>
                  <span className={css.recentSize}>{formatBytes(file.size)}</span>
                  <time dateTime={file.accessed_at} title={formatDate(file.accessed_at)}>
                    最近操作 {formatRelativeTime(file.accessed_at)}
                  </time>
                </span>
                <a
                  className={css.recentDownload}
                  href={downloadFileUrl(file.source_key, `/${file.path}`)}
                  download={file.name}
                  aria-label={`下载 ${file.name}`}
                  title={`下载 ${file.name}`}
                >
                  <IconDownload size={16} />
                </a>
              </article>
            )
          })}
        </div>
      )}
    </section>
  )
}

function RecentEmpty() {
  const css = useStyles()
  return (
    <div className={css.recentEmpty}>
      <EmptyTray className={css.emptyArt} />
      <p className={css.emptyTitle}>还没有最近文件</p>
    </div>
  )
}

function timeGreeting(date: Date): string {
  const hour = date.getHours()
  if (hour < 6) return '夜深了'
  if (hour < 11) return '早上好'
  if (hour < 14) return '中午好'
  if (hour < 18) return '下午好'
  return '晚上好'
}

function formatKickerDate(date: Date): string {
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  const weekday = new Intl.DateTimeFormat('zh-CN', { weekday: 'long' }).format(date)
  return `${date.getFullYear()}.${month}.${day} · ${weekday}`
}
