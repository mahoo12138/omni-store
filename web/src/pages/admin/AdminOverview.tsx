import { useEffect, useRef, type ReactElement } from 'react'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { fetchMe } from '../../api/auth'
import { Nav, NavItem, NavSectionHeader } from '@fluentui/react-components'
import { AdminLayout } from './AdminLayout'
import { PageHeader } from '../../components/shell/PageHeader'
import { SiteSettingsSection } from './SiteSettings'
import { useStyles } from './AdminOverview.styles'
import { adminNav, baseNav, type SectionKey } from './nav'
import { ProfileSection } from './sections/ProfileSection'
import { PreferencesSection } from './sections/PreferencesSection'
import { DashboardSection } from './sections/DashboardSection'
import { SourcesSection } from './sections/SourcesSection'
import { PoliciesSection } from './sections/PoliciesSection'
import { UsersSection } from './sections/UsersSection'
import { AuditSection } from './sections/AuditSection'

export function AdminOverviewPage({ mode }: { mode: 'settings' | 'admin' }) {
  const css = useStyles()
  const search = useSearch({ strict: false }) as { section?: string; tab?: string }
  const navigate = useNavigate()
  const me = useQuery({ queryKey: ['me'], queryFn: fetchMe, retry: false })
  const isAdmin = me.data?.role === 'super_admin'
  const isSettings = mode === 'settings'
  // 导航永远是全量：管理员在两个入口都看得到全部设置项，
  // 只有"从哪个分区打开"不同；点击时按分区归属跳到对应路由。
  const nav = isAdmin ? baseNav.concat(adminNav) : baseNav
  const settingsNavRef = useRef<HTMLElement>(null)
  const compactNavRef = useRef<HTMLElement>(null)
  const legacySiteTabs: Record<string, 'services' | 'general'> = { backup: 'general', branding: 'general', 'image-bed': 'services' }
  const legacySiteTab = isSettings ? undefined : legacySiteTabs[search.section ?? '']
  const requestedSection = !isSettings && search.section === 'operations'
    ? 'stats'
    : legacySiteTab ? 'site' : search.section
  const siteTab = search.tab === 'general' ? 'general' : search.tab === 'auth' ? 'auth' : 'services'
  const section: SectionKey = (nav.find((n) => n.key === requestedSection)?.key ??
    (isSettings ? 'profile' : 'stats')) as SectionKey
  const currentSection = nav.find((item) => item.key === section) ?? nav[0]

  // 管理后台只对超级管理员开放：普通用户被送回账户设置。
  useEffect(() => {
    if (!isSettings && me.isSuccess && !isAdmin) void navigate({ to: '/app/settings', replace: true })
  }, [isSettings, isAdmin, me.isSuccess, navigate])

  // 深链接归位：分区属于哪条路由，就停在哪个 URL（如 /app/settings?section=sources
  // 会 replace 到 /app/admin?section=sources），避免"账户设置"页里出现管理分区。
  useEffect(() => {
    if (!me.isSuccess) return
    const personal = baseNav.some((item) => item.key === section)
    if (personal === isSettings) return
    void navigate({ to: personal ? '/app/settings' : '/app/admin', search: { section }, replace: true })
  }, [me.isSuccess, section, isSettings, navigate])

  useEffect(() => {
    // 窄屏横栏把当前分区滚到中间，左右都能看到相邻分区，暗示还有内容。
    const compactNav = compactNavRef.current
    const compactActive = compactNav?.querySelector<HTMLElement>('[aria-current="page"]')
    if (compactNav && compactActive && compactActive.getClientRects().length > 0) {
      const navRect = compactNav.getBoundingClientRect()
      const itemRect = compactActive.getBoundingClientRect()
      const delta = (itemRect.left - navRect.left) - (compactNav.clientWidth - itemRect.width) / 2
      const max = compactNav.scrollWidth - compactNav.clientWidth
      compactNav.scrollLeft = Math.max(0, Math.min(max, compactNav.scrollLeft + delta))
    }
    // 桌面竖栏与窄屏横栏互斥显示；只在有布局盒（未被 display:none）的一侧滚动。
    const desktopActive = settingsNavRef.current?.querySelector<HTMLElement>('[aria-current="page"]')
    if (desktopActive && desktopActive.getClientRects().length > 0) {
      desktopActive.scrollIntoView({ block: 'nearest', inline: 'nearest' })
    }
  }, [section])

  useEffect(() => {
    if (isSettings) return
    // 旧链接：个人分区已迁到 /app/settings。
    if (search.section === 'profile' || search.section === 'preferences') {
      void navigate({ to: '/app/settings', search: { section: search.section }, replace: true })
      return
    }
    if (search.section === 'operations') {
      void navigate({ to: '/app/admin', search: { section: 'stats' }, replace: true })
    } else if (legacySiteTab) {
      void navigate({ to: '/app/admin', search: { section: 'site', tab: legacySiteTab }, replace: true })
    }
  }, [isSettings, search.section, legacySiteTab, navigate])

  function setSection(k: SectionKey) {
    const personal = baseNav.some((item) => item.key === k)
    navigate({ to: personal ? '/app/settings' : '/app/admin', search: { section: k } })
  }

  // 窄屏横栏与桌面竖栏共用同一份数据；横栏保留分组分隔线，不重复渲染组标题。
  function compactNavItem(item: { key: SectionKey; label: string; icon: ReactElement }) {
    return (
      <button
        key={item.key}
        type="button"
        className={css.settingsCompactItem}
        aria-current={section === item.key ? 'page' : undefined}
        onClick={() => setSection(item.key)}
      >
        <span className={css.settingsCompactIcon} aria-hidden="true">{item.icon}</span>
        <span>{item.label}</span>
      </button>
    )
  }

  return (
    <AdminLayout>
      <div className={css.settingsLayout}>
        {/*
          分组子导航与主侧栏用同一套 Fluent Nav：两级导航只有一种语言。
          不传 href → NavItem 渲染 <button>，getByRole('button') 与 aria-current 契约不变。
        */}
        <nav ref={settingsNavRef} className={css.settingsSide} aria-label="设置分组">
          <Nav selectedValue={section} onNavItemSelect={(_, data) => setSection(data.value as SectionKey)}>
            <NavSectionHeader as="div" className={css.settingsGroupTitle}>基础</NavSectionHeader>
            {baseNav.map((item) => (
              <NavItem key={item.key} value={item.key} icon={item.icon}>
                {item.label}
              </NavItem>
            ))}
            {isAdmin ? (
              <>
                <NavSectionHeader as="div" className={css.settingsGroupTitle}>管理</NavSectionHeader>
                {adminNav.map((item) => (
                  <NavItem key={item.key} value={item.key} icon={item.icon}>
                    {item.label}
                  </NavItem>
                ))}
              </>
            ) : null}
          </Nav>
        </nav>

        {/* 右侧内容 */}
        <div className={css.settingsContent}>
          {/*
            头部只表达"你在哪"：eyebrow 是产品名，h1 是当前分区。
            各分区内部原本同名的那层 h2 一并移除，避免同名标题出现两次。
          */}
          <PageHeader size="compact" eyebrow={isSettings ? '账户设置' : '系统设置'} title={currentSection.label} />
          {/* ≤1200 时桌面竖栏隐藏，分区切换改为页头下方的横向胶囊栏。 */}
          <nav ref={compactNavRef} className={css.settingsCompactNav} aria-label="设置分组">
            {baseNav.map(compactNavItem)}
            {isAdmin ? (
              <>
                <span className={css.settingsCompactDivider} aria-hidden="true" />
                {adminNav.map(compactNavItem)}
              </>
            ) : null}
          </nav>
          {section === 'profile' && <ProfileSection />}
          {section === 'preferences' && <PreferencesSection />}
          {section === 'stats' && <DashboardSection />}
          {section === 'sources' && <SourcesSection />}
          {section === 'policies' && <PoliciesSection />}
          {section === 'users' && <UsersSection />}
          {section === 'audit' && <AuditSection />}
          {section === 'site' && <SiteSettingsSection tab={siteTab} onTabChange={(tab) => { void navigate({ to: '/app/admin', search: { section: 'site', tab }, replace: true }) }} />}
        </div>
      </div>

    </AdminLayout>
  )
}


export function SettingsPage() {
  return <AdminOverviewPage mode="settings" />
}

export function AdminPage() {
  return <AdminOverviewPage mode="admin" />
}
