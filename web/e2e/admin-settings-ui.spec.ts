import { expect, test, type Page } from '@playwright/test'

async function loginAsAdmin(page: Page) {
  await page.goto('/login')
  await page.getByLabel('用户名').fill('admin')
  await page.getByLabel('密码').fill('OmniStore-Test-Admin!')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page).toHaveURL(/\/app$/)
}

async function expectLocalHorizontalScroll(page: Page, regionName: string) {
  const region = page.getByRole('region', { name: regionName, exact: true })
  await expect(region).toBeVisible()

  // Source rows load asynchronously; wait for their content to make the region scrollable.
  await expect.poll(() => region.evaluate((element) => element.scrollWidth - element.clientWidth))
    .toBeGreaterThan(0)
  await expect(region).toHaveCSS('overflow-x', 'auto')

  await region.evaluate((element) => { element.scrollLeft = 0 })
  await region.hover()
  await page.mouse.wheel(480, 0)
  await expect.poll(() => region.evaluate((element) => element.scrollLeft)).toBeGreaterThan(0)

  expect(await page.evaluate(() => (
    document.documentElement.scrollWidth > document.documentElement.clientWidth
  ))).toBe(false)
}

test('storage source settings stay clear and usable on desktop and mobile', async ({ page }) => {
  await loginAsAdmin(page)
  await page.goto('/app/admin?section=sources')

  await expect(page.getByRole('heading', { name: '存储源', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '新建存储源', exact: true })).toBeVisible()
  await expect(page.getByRole('row').filter({ hasText: '公开演示资料' }).first()).toBeVisible()
  await expect(page.getByRole('row').filter({ hasText: '团队文件' }).first()).toBeVisible()
  await expect(page.getByRole('button', { name: /配置静态 HTTP/ })).toHaveCount(0)
  await page.screenshot({ path: '/tmp/omni-sources-desktop.png' })

  await page.getByRole('button', { name: '配置存储源 团队文件' }).click()
  const settingsDialog = page.getByRole('dialog', { name: '配置：团队文件' })
  await expect(settingsDialog).toBeVisible()
  await page.screenshot({ path: '/tmp/omni-protocols-desktop.png' })
  await expect(settingsDialog.getByRole('button', { name: '保存配额' })).toHaveCount(0)
  await expect(settingsDialog.getByRole('button', { name: '保存路径' })).toHaveCount(0)
  await expect(settingsDialog.getByRole('button', { name: '保存排除规则' })).toHaveCount(0)

  const webdavInput = settingsDialog.getByRole('checkbox', { name: '启用 WebDAV', exact: true })
  const originalWebdav = await webdavInput.isChecked()
  await settingsDialog.getByRole('tab', { name: '存储设置', exact: true }).click()
  const quotaInput = settingsDialog.getByLabel('存储源配额 GiB')
  const patternsInput = settingsDialog.getByLabel('排除规则', { exact: true })
  const originalQuota = await quotaInput.inputValue()
  const originalPatterns = await patternsInput.inputValue()
  const changedQuota = String(Number(originalQuota) + 0.001)
  const changedPatterns = `${originalPatterns}\n**/*.omnistore-e2e`
  const saveSettings = settingsDialog.getByRole('button', { name: '保存设置' })
  await expect(saveSettings).toBeDisabled()
  await page.screenshot({ path: '/tmp/omni-storage-desktop.png' })

  await quotaInput.fill('-1')
  await expect(saveSettings).toBeDisabled()
  await quotaInput.fill(changedQuota)
  await patternsInput.fill(changedPatterns)
  await expect(saveSettings).toBeEnabled()
  await expect(settingsDialog.getByRole('button', { name: '扫描并校准台账' })).toBeDisabled()
  await settingsDialog.getByRole('tab', { name: '访问协议', exact: true }).click()
  await webdavInput.setChecked(!originalWebdav)
  await settingsDialog.getByRole('tab', { name: '存储设置', exact: true }).click()
  await expect(quotaInput).toHaveValue(changedQuota)
  await saveSettings.click()
  await expect(settingsDialog).toBeHidden()

  await page.getByRole('button', { name: '配置存储源 团队文件' }).click()
  await expect(settingsDialog).toBeVisible()
  await expect(webdavInput).toBeChecked({ checked: !originalWebdav })
  await webdavInput.setChecked(originalWebdav)
  // Tabs supplies the native arrow-key path as well as pointer navigation.
  await settingsDialog.getByRole('tab', { name: '访问协议', exact: true }).focus()
  await page.keyboard.press('ArrowRight')
  await expect(settingsDialog.getByRole('tab', { name: '存储设置', exact: true })).toBeFocused()
  await page.keyboard.press('Enter')
  await expect(settingsDialog.getByRole('tabpanel', { name: '存储设置', exact: true })).toBeVisible()
  await expect(quotaInput).toHaveValue(changedQuota)
  await expect(patternsInput).toHaveValue(changedPatterns)

  await quotaInput.fill(originalQuota)
  await patternsInput.fill(originalPatterns)
  await settingsDialog.getByRole('button', { name: '保存设置' }).click()
  await expect(settingsDialog).toBeHidden()

  await page.setViewportSize({ width: 375, height: 812 })
  await expect(page.getByRole('button', { name: '新建存储源', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '配置存储源 公开演示资料' })).toBeVisible()
  await expect(page.getByRole('button', { name: '禁用存储源 公开演示资料' })).toBeVisible()
  await expect(page.getByRole('button', { name: '删除存储源 公开演示资料' })).toBeVisible()
  const hasHorizontalOverflow = await page.evaluate(() => (
    document.documentElement.scrollWidth > document.documentElement.clientWidth
  ))
  expect(hasHorizontalOverflow).toBe(false)
  await expectLocalHorizontalScroll(page, '存储源')
  await page.screenshot({ path: '/tmp/omni-sources-mobile.png' })
  await page.getByRole('button', { name: '配置存储源 团队文件' }).click()
  await expect(settingsDialog).toBeVisible()
  await page.screenshot({ path: '/tmp/omni-protocols-mobile.png' })
  await settingsDialog.getByRole('tab', { name: '存储设置', exact: true }).click()
  await expect(quotaInput).toBeVisible()
  await page.screenshot({ path: '/tmp/omni-storage-mobile.png' })
  await settingsDialog.getByRole('button', { name: '关闭弹窗' }).click()

  await page.getByRole('button', { name: '新建存储源', exact: true }).click()
  await expect(page.getByRole('dialog', { name: '新建存储源' })).toBeVisible()
  await page.getByRole('button', { name: '取消', exact: true }).click()
})

test('source cards and configuration reflow with long names and paths', async ({ page }) => {
  await loginAsAdmin(page)
  const longName = `跨团队共享资料与长期公开媒体-${'公开演示资料'.repeat(8)}`
  const longPath = `/srv/${'团队共享资料及国际化名称/'.repeat(12)}${'very-long-unbroken-directory-name'.repeat(8)}`
  await page.route('**/api/v1/admin/sources', async (route) => {
    const response = await route.fetch()
    const body = await response.json()
    body.data.items[0] = { ...body.data.items[0], name: longName, root_path: longPath, static_enabled: true }
    body.data.items[1] = { ...body.data.items[1], is_disabled: true, webdav_enabled: false, s3_enabled: false, static_enabled: false }
    await route.fulfill({ response, json: body })
  })
  await page.route('**/api/v1/admin/sources/*', async (route) => {
    const response = await route.fetch()
    const body = await response.json()
    body.data.source = { ...body.data.source, name: longName, root_path: longPath }
    await route.fulfill({ response, json: body })
  })
  await page.goto('/app/admin?section=sources')
  const row = page.getByRole('row').filter({ hasText: longName }).first()
  const region = page.getByRole('region', { name: '存储源', exact: true })
  await expect(row).toContainText('静态 HTTP')
  await expect(region).toContainText('均未启用')
  for (const width of [1455, 1119, 893, 727, 375, 320]) {
    await page.setViewportSize({ width, height: 887 })
    await expect(row).toBeVisible()
    const cellBounds = await row.evaluate((element) => {
      const cell = element.querySelector('td')!
      const cellRect = cell.getBoundingClientRect()
      const path = cell.querySelector('code')!.getBoundingClientRect()
      return {
        cellWidth: cellRect.width,
        pathInside: path.left >= cellRect.left - 0.5 && path.right <= cellRect.right + 0.5,
      }
    })
    expect(cellBounds.cellWidth).toBeLessThanOrEqual(400)
    expect(cellBounds.pathInside).toBe(true)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true)
    await row.getByRole('button', { name: `配置存储源 ${longName}`, exact: true }).click()
    const dialog = page.getByRole('dialog', { name: `配置：${longName}`, exact: true })
    await expect(dialog).toBeVisible()
    expect(await dialog.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    const contentArea = dialog.getByRole('tabpanel', { name: '访问协议', exact: true })
    await expect(contentArea.getByRole('checkbox', { name: '启用静态 HTTP', exact: true })).toBeInViewport()
    await expect(dialog.getByRole('button', { name: '关闭弹窗' })).toBeInViewport()
    if ([1455, 320].includes(width)) await page.screenshot({ path: `/tmp/omni-source-long-${width}.png` })
    await dialog.getByRole('button', { name: '关闭弹窗' }).click()
  }
})

test('primary settings actions belong to their module headers', async ({ page }) => {
  await loginAsAdmin(page)

  const modules = [
    {
      section: 'sources',
      heading: '存储源',
      action: '新建存储源',
      dialog: '新建存储源',
    },
    {
      section: 'policies',
      heading: '访问策略',
      action: '新建策略',
      dialog: '新建访问策略',
    },
    {
      section: 'users',
      heading: '用户',
      action: '创建用户',
      dialog: '创建用户',
    },
  ] as const

  for (const module of modules) {
    await page.goto(`/app/admin?section=${module.section}`)
    const moduleHeader = page.locator('header').filter({
      has: page.getByRole('button', { name: module.action, exact: true }),
    })
    const action = moduleHeader.getByRole('button', { name: module.action, exact: true })
    await expect(moduleHeader).toBeVisible()
    await expect(action).toBeVisible()
    await action.click()
    await expect(page.getByRole('dialog', { name: module.dialog })).toBeVisible()
    await page.getByRole('button', { name: '取消', exact: true }).click()
  }

  await page.goto('/app/admin?section=site')
  const imageBedHeader = page.locator('header').filter({
    has: page.getByRole('heading', { name: '快速图床', exact: true }),
  })
  const imageBedAction = imageBedHeader.getByRole('button', { name: '配置', exact: true })
  await expect(imageBedAction).toBeVisible()
  await imageBedAction.click()
  await expect(page.getByRole('dialog', { name: '配置：快速图床', exact: true })).toBeVisible()
  await page.getByRole('button', { name: '取消', exact: true }).click()

  await page.setViewportSize({ width: 375, height: 812 })
  for (const module of modules) {
    await page.goto(`/app/admin?section=${module.section}`)
    const action = page.locator('header').filter({
      has: page.getByRole('button', { name: module.action, exact: true }),
    }).getByRole('button', { name: module.action, exact: true })
    await expect(action).toBeVisible()
    const hasHorizontalOverflow = await page.evaluate(() => (
      document.documentElement.scrollWidth > document.documentElement.clientWidth
    ))
    expect(hasHorizontalOverflow).toBe(false)
  }
})

test('credential overview opens one isolated management modal at a time', async ({ page }) => {
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])
  await loginAsAdmin(page)
  await page.setViewportSize({ width: 1119, height: 880 })
  await page.goto('/app/settings?section=profile')

  const connectionTypes = page.getByRole('navigation', { name: '连接类型' })
  const webdavEntry = connectionTypes.getByRole('button', { name: '管理 WebDAV' })
  const imageApiEntry = connectionTypes.getByRole('button', { name: '管理 图床 API' })
  const s3Entry = connectionTypes.getByRole('button', { name: '管理 S3' })

  await expect(webdavEntry).toBeVisible()
  await expect(imageApiEntry).toBeVisible()
  await expect(s3Entry).toBeVisible()
  await expect(page.getByRole('region', { name: 'WebDAV', exact: true })).toHaveCount(0)
  await expect(page.getByRole('region', { name: '图床 API', exact: true })).toHaveCount(0)
  await expect(page.getByRole('region', { name: 'S3 Access Key', exact: true })).toHaveCount(0)

  await webdavEntry.click()
  const webdavDialog = page.getByRole('dialog', { name: 'WebDAV', exact: true })
  const webdav = webdavDialog.getByRole('region', { name: 'WebDAV', exact: true })
  await expect(webdavDialog).toBeVisible()
  await expect(webdav).toContainText('挂载路径')
  await expect(webdav).toContainText('/dav')
  const generateWebdavToken = webdavDialog.getByRole('button', { name: '生成 Token', exact: true })
  const resetWebdavToken = webdavDialog.getByRole('button', { name: '重置 Token', exact: true })
  await expect(generateWebdavToken.or(resetWebdavToken)).toBeEnabled()
  if (await generateWebdavToken.isVisible()) {
    await generateWebdavToken.click()
    const revealedWebdavToken = page.getByRole('dialog', { name: '新的 Token', exact: true })
    await expect(revealedWebdavToken).toBeVisible()
    const tokenInput = revealedWebdavToken.getByLabel('Token')
    await expect(tokenInput).not.toHaveValue('')
    const tokenInputBox = await tokenInput.boundingBox()
    expect(tokenInputBox?.height ?? Infinity).toBeLessThan(44)
    expect(tokenInputBox?.width ?? 0).toBeGreaterThan(220)
    const tokenDescBox = await revealedWebdavToken.getByText('请立即复制保存，关闭后不再显示。').boundingBox()
    expect(tokenDescBox?.height ?? Infinity).toBeLessThan(32)
    const tokenLabel = revealedWebdavToken.locator('label').filter({ hasText: 'Token' })
    expect(await tokenLabel.getAttribute('for')).toBe(await tokenInput.getAttribute('id'))
    await revealedWebdavToken.getByRole('button', { name: '关闭', exact: true }).click()
    await expect(revealedWebdavToken).toBeHidden()
  }
  await expect(resetWebdavToken).toBeVisible()
  await resetWebdavToken.click()
  await expect(page.getByRole('dialog', { name: '重置 WebDAV' })).toBeVisible()
  const webdavDialogLayers = await page.locator('[data-dialog-backdrop]').evaluateAll((elements) => elements.map((element) => ({
    depth: Number(element.getAttribute('data-dialog-depth')),
    zIndex: Number(getComputedStyle(element).zIndex),
  })))
  expect(webdavDialogLayers).toHaveLength(2)
  expect(webdavDialogLayers[1].depth).toBeGreaterThan(webdavDialogLayers[0].depth)
  expect(webdavDialogLayers[1].zIndex).toBeGreaterThan(webdavDialogLayers[0].zIndex)
  await page.getByRole('dialog', { name: '重置 WebDAV' }).getByRole('button', { name: '取消', exact: true }).click()
  await expect(webdavDialog).toBeVisible()
  await webdavDialog.getByRole('button', { name: '完成', exact: true }).click()

  await expect(webdavEntry).toBeFocused()
  await imageApiEntry.click()
  await expect(webdav).toHaveCount(0)
  const imageApiDialog = page.getByRole('dialog', { name: '图床 API', exact: true })
  const imageApi = imageApiDialog.getByRole('region', { name: '图床 API', exact: true })
  await expect(imageApiDialog).toBeVisible()
  await imageApi.getByRole('button', { name: '新建 Token', exact: true }).click()
  await expect(page.getByRole('dialog', { name: '新建图床 Token' })).toBeVisible()
  // 嵌套打开时两层的背板都已挂载(Fluent 设计:内层背板透明,遮罩由最外层承担;
  // surface 本身是 fixed inset-0 元素,因此按 data-dialog-backdrop 标记计数)
  const mountedBackdropCount = await page.locator('[data-dialog-backdrop]').count()
  expect(mountedBackdropCount).toBe(2)
  await page.getByRole('dialog', { name: '新建图床 Token' }).getByRole('button', { name: '取消', exact: true }).click()
  await imageApiDialog.getByRole('button', { name: '完成', exact: true }).click()

  await s3Entry.click()
  const s3Dialog = page.getByRole('dialog', { name: 'S3', exact: true })
  const s3 = s3Dialog.getByRole('region', { name: 'S3 Access Key', exact: true })
  await expect(s3Dialog).toBeVisible()
  const createS3Credential = s3.getByRole('button', { name: '新建凭据', exact: true })
  await expect(createS3Credential).toBeEnabled()
  const s3CredentialList = s3.getByRole('list', { name: 'S3 凭据列表' })
  const s3CredentialEmpty = s3.getByText('暂无 S3 凭据', { exact: true })
  await expect(s3CredentialList.or(s3CredentialEmpty)).toBeVisible()
  await expect(s3.getByRole('list', { name: 'S3 Bucket 映射' })).toBeVisible()
  const copyBucket = s3.getByRole('button', { name: '复制 Bucket' }).first()
  await copyBucket.click()
  await expect(s3.getByRole('button', { name: '已复制' })).toBeVisible()
  await createS3Credential.click()
  await expect(page.getByRole('dialog', { name: '新建 S3 凭据' })).toBeVisible()
  await page.getByRole('dialog', { name: '新建 S3 凭据' }).getByRole('button', { name: '取消', exact: true }).click()
  await s3Dialog.getByRole('button', { name: '完成', exact: true }).click()
  await expect(s3Entry).toBeFocused()

  await page.setViewportSize({ width: 375, height: 812 })
  await webdavEntry.click()
  const mobileWebdavDialog = page.getByRole('dialog', { name: 'WebDAV', exact: true })
  await expect(mobileWebdavDialog.getByRole('region', { name: 'WebDAV', exact: true })).toBeVisible()
  await expect(page.getByRole('dialog', { name: '新的 Token', exact: true })).toHaveCount(0)
  await mobileWebdavDialog.getByRole('button', { name: '完成', exact: true }).click()
  await imageApiEntry.click()
  const mobileImageDialog = page.getByRole('dialog', { name: '图床 API', exact: true })
  await expect(mobileImageDialog.getByRole('region', { name: '图床 API', exact: true })).toBeVisible()
  await mobileImageDialog.getByRole('button', { name: '完成', exact: true }).click()
  await s3Entry.click()
  const mobileS3Dialog = page.getByRole('dialog', { name: 'S3', exact: true })
  await expect(mobileS3Dialog.getByRole('region', { name: 'S3 Access Key', exact: true })).toBeVisible()
  expect(await page.evaluate(() => (
    document.documentElement.scrollWidth > document.documentElement.clientWidth
  ))).toBe(false)
  await mobileS3Dialog.getByRole('button', { name: '完成', exact: true }).click()
})

test('admin data regions scroll locally without squeezing narrow layouts', async ({ page }) => {
  await loginAsAdmin(page)
  await page.setViewportSize({ width: 893, height: 880 })

  const dataRegions = [
    { section: 'stats', name: '存储源健康' },
    { section: 'sources', name: '存储源' },
    { section: 'policies', name: '访问策略' },
    { section: 'users', name: '用户' },
    { section: 'audit', name: '审计日志' },
  ] as const

  for (const dataRegion of dataRegions) {
    await page.goto(`/app/admin?section=${dataRegion.section}`)
    await expectLocalHorizontalScroll(page, dataRegion.name)
  }

  await page.setViewportSize({ width: 375, height: 812 })
  for (const dataRegion of dataRegions) {
    await page.goto(`/app/admin?section=${dataRegion.section}`)
    await expectLocalHorizontalScroll(page, dataRegion.name)
  }
})

test('audit logs request an explicit page and translate action names', async ({ page }) => {
  await loginAsAdmin(page)

  const auditResponsePromise = page.waitForResponse((response) => (
    new URL(response.url()).pathname === '/api/v1/admin/audit-logs'
  ))
  await page.goto('/app/admin?section=audit')
  const auditResponse = await auditResponsePromise
  const auditURL = new URL(auditResponse.url())
  const auditPayload = await auditResponse.json()

  expect(auditURL.searchParams.get('page')).toBe('1')
  expect(auditURL.searchParams.get('page_size')).toBe('10')
  expect(auditPayload.data.page).toBe(1)
  expect(auditPayload.data.page_size).toBe(10)
  expect(auditPayload.data.items.length).toBeLessThanOrEqual(10)

  const pagination = page.getByRole('navigation', { name: '审计日志分页' })
  await expect(pagination).toBeVisible()
  await expect(pagination).toContainText('第 1 页')
  await expect(page.getByRole('cell', { name: /用户 #\d+/ }).first()).toBeVisible()
  await expect(page.getByRole('cell', { name: '网页', exact: true }).first()).toBeVisible()
  await expect(page.getByRole('cell', { name: 'web', exact: true })).toHaveCount(0)
  await expect(page.getByText('login_success', { exact: true })).toHaveCount(0)
  await expect(page.getByRole('cell', { name: '登录成功', exact: true }).first()).toBeVisible()
})

test('every admin section remains readable and keeps its active navigation visible', async ({ page }) => {
  await loginAsAdmin(page)

  // 账户设置与系统设置是两个路由：前者所有人可用，后者仅超级管理员。
  const sections = [
    { key: 'profile', nav: '我的', heading: '测试管理员', base: '/app/settings' },
    { key: 'preferences', nav: '偏好设置', heading: '偏好设置', base: '/app/settings' },
    { key: 'stats', nav: '仪表盘', heading: '系统状态', base: '/app/admin' },
    { key: 'sources', nav: '存储源', heading: '存储源', base: '/app/admin' },
    { key: 'policies', nav: '访问策略', heading: '访问策略', base: '/app/admin' },
    { key: 'users', nav: '用户', heading: '用户', base: '/app/admin' },
    { key: 'audit', nav: '审计日志', heading: '审计日志', base: '/app/admin' },
    { key: 'site', nav: '站点配置', heading: '站点配置', base: '/app/admin' },
  ] as const

  for (const viewport of [
    { width: 1280, height: 720 },
    { width: 916, height: 880 },
    { width: 375, height: 812 },
  ]) {
    await page.setViewportSize(viewport)

    for (const section of sections) {
      await page.goto(`${section.base}?section=${section.key}`)

      const settingsNav = page.getByRole('navigation', { name: '设置分组' })
      const activeItem = settingsNav.getByRole('button', { name: section.nav, exact: true })
      await expect(activeItem).toHaveAttribute('aria-current', 'page')
      await expect(activeItem).toBeInViewport()
      await expect(page.getByRole('heading', { name: section.heading, exact: true })).toBeVisible()

      expect(await page.evaluate(() => (
        document.documentElement.scrollWidth > document.documentElement.clientWidth
      ))).toBe(false)

      const clippedOutsideScrollRegions = await page.locator('main').evaluate((main) => (
        [...main.querySelectorAll<HTMLElement>('*')].filter((element) => {
          const box = element.getBoundingClientRect()
          if (box.width <= 0 || box.height <= 0) return false

          let ancestor: HTMLElement | null = element.parentElement
          while (ancestor && ancestor !== main) {
            const overflowX = getComputedStyle(ancestor).overflowX
            if (overflowX === 'auto' || overflowX === 'scroll') return false
            ancestor = ancestor.parentElement
          }

          return box.left < -1 || box.right > innerWidth + 1
        }).map((element) => element.textContent?.trim().slice(0, 80) ?? element.tagName)
      ))
      expect(clippedOutsideScrollRegions).toEqual([])
    }
  }

  await page.goto('/app/settings?section=profile')
  const accountName = page.getByRole('heading', { name: '测试管理员', exact: true })
  const accountNameBox = await accountName.boundingBox()
  expect(accountNameBox?.height ?? Infinity).toBeLessThan(48)

  await page.getByRole('button', { name: '账户操作', exact: true }).click()
  await page.getByRole('menuitem', { name: '修改显示名', exact: true }).click()
  await expect(page.getByRole('dialog', { name: '修改显示名' })).toBeVisible()
  await page.getByRole('button', { name: '取消', exact: true }).click()

  await page.goto('/app/admin?section=site')
  await page.getByRole('article', { name: '站点服务 快速图床', exact: true }).getByRole('button', { name: '配置', exact: true }).click()
  await expect(page.getByRole('dialog', { name: '配置：快速图床', exact: true })).toBeVisible()
  await page.getByRole('button', { name: '取消', exact: true }).click()
})
