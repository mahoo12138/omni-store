import { expect, test, type Locator, type Page } from '@playwright/test'

type Capability = 'public_drive' | 'image_bed'
interface Binding {
  capability: Capability
  enabled: boolean
  source_key?: string
  source_name?: string
  revision: number
}
interface Source { key: string; name: string; is_disabled: boolean }
interface AnonymousSettings { enabled: boolean; key: string }
interface Retention { user_retention_days: number; anonymous_retention_days: number }

const services: Array<{ capability: Capability; name: string }> = [
  { capability: 'public_drive', name: '公开网盘' },
  { capability: 'image_bed', name: '快速图床' },
]

async function login(page: Page) {
  await page.goto('/login')
  await page.getByLabel('用户名').fill('admin')
  await page.getByLabel('密码').fill('OmniStore-Test-Admin!')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page).toHaveURL(/\/app$/)
  return (await read<{ csrf_token: string }>(page, '/api/v1/auth/me')).csrf_token
}

async function read<T>(page: Page, path: string): Promise<T> {
  // Requests inherit Playwright's baseURL (OMNISTORE_E2E_BASE_URL in isolated runs).
  const response = await page.request.get(path)
  expect(response.status(), `GET ${path}`).toBe(200)
  return (await response.json()).data as T
}

async function bindings(page: Page) {
  return (await read<{ items: Binding[] }>(page, '/api/v1/admin/capabilities')).items
}

async function binding(page: Page, capability: Capability) {
  const result = (await bindings(page)).find((item) => item.capability === capability)
  expect(result, `binding for ${capability}`).toBeDefined()
  return result!
}

async function mutate(page: Page, csrf: string, path: string, data?: unknown, method = 'PUT') {
  const response = await page.request.fetch(path, {
    method,
    headers: { 'X-CSRF-Token': csrf },
    data,
  })
  expect(response.status(), `${method} ${path}: ${await response.text()}`).toBe(200)
  return response
}

function serviceCard(page: Page, name: string) {
  return page.getByRole('article', { name: `站点服务 ${name}`, exact: true })
}

async function openService(page: Page, name: string) {
  await serviceCard(page, name).getByRole('button', { name: '配置', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: `配置：${name}`, exact: true })
  await expect(dialog).toBeVisible()
  return dialog
}

async function selectSource(page: Page, dialog: Locator, name: string) {
  await dialog.getByRole('combobox', { name: '绑定存储源', exact: true }).click()
  await page.getByRole('option', { name, exact: true }).click()
}

async function saveService(page: Page, dialog: Locator, capability: Capability, sourceKey: string, enabled: boolean) {
  const current = await binding(page, capability)
  const responsePromise = page.waitForResponse((response) => (
    new URL(response.url()).pathname === `/api/v1/admin/capabilities/${capability}`
    && response.request().method() === 'PUT'
  ))
  await dialog.getByRole('button', { name: '保存服务设置', exact: true }).click()
  const response = await responsePromise
  expect(response.request().postDataJSON()).toMatchObject({
    storage_source_key: sourceKey,
    enabled,
    expected_revision: current.revision,
  })
  expect(response.status()).toBe(200)
  await expect(dialog).toBeHidden()
  const persisted = await binding(page, capability)
  expect(persisted.source_key ?? '').toBe(sourceKey)
  expect(persisted.enabled).toBe(enabled)
  expect(persisted.revision).toBe(current.revision + 1)
}

async function expectNoOverflow(page: Page, element?: Locator) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true)
  if (element) {
    expect(await element.evaluate((node) => node.scrollWidth <= node.clientWidth)).toBe(true)
    const box = await element.boundingBox()
    expect(box).not.toBeNull()
    expect(box!.x).toBeGreaterThanOrEqual(0)
    expect(box!.x + box!.width).toBeLessThanOrEqual(page.viewportSize()!.width)
  }
}

test('site services persist state transitions, reject failed saves and recover concurrent edits', async ({ page, baseURL }) => {
  test.setTimeout(120_000)
  expect(baseURL, 'Playwright must supply the fixture baseURL').toBeTruthy()
  const csrf = await login(page)
  const originalBindings = await bindings(page)
  const originalAnonymous = await read<AnonymousSettings>(page, '/api/v1/admin/image-bed/anonymous-settings')
  const originalRetention = await read<Retention>(page, '/api/v1/admin/image-bed/retention')
  const originalSources = (await read<{ items: Source[] }>(page, '/api/v1/admin/sources')).items
  const publicSource = originalSources.find((source) => source.name === '公开演示资料')!
  const teamSource = originalSources.find((source) => source.name === '团队文件')!
  expect(publicSource).toBeDefined()
  expect(teamSource).toBeDefined()
  expect(publicSource.is_disabled).toBe(false)
  expect(teamSource.is_disabled).toBe(false)

  try {
    await mutate(page, csrf, '/api/v1/admin/image-bed/anonymous-settings', { enabled: true })
    await page.goto('/app/admin?section=site')
    for (const service of services) {
      const initial = await binding(page, service.capability)
      const firstSource = initial.source_key === teamSource.key ? publicSource : teamSource
      const dialog = await openService(page, service.name)
      await selectSource(page, dialog, firstSource.name)
      await dialog.getByRole('checkbox', { name: '启用服务', exact: true }).check()
      await saveService(page, dialog, service.capability, firstSource.key, true)

      await openService(page, service.name)
      await dialog.getByRole('checkbox', { name: '启用服务', exact: true }).uncheck()
      await saveService(page, dialog, service.capability, firstSource.key, false)
      if (service.capability === 'image_bed') {
        // The disabled upload page must lead to the service configuration, not source protocols.
        await page.goto('/app/image-bed')
        await expect(page.getByRole('heading', { name: '图床尚未开启', exact: true })).toBeVisible()
        await page.getByRole('link', { name: /前往系统设置/ }).click()
        await expect.poll(() => new URL(page.url()).searchParams.get('section')).toBe('site')
        // Closing a saved anonymous gate remains possible while the parent service is off.
        const gateDialog = await openService(page, service.name)
        const gate = gateDialog.getByRole('checkbox', { name: '允许匿名上传', exact: true })
        await expect(gate).toBeChecked()
        await expect(gate).toBeEnabled()
        const gateClosed = page.waitForResponse((response) => response.url().endsWith('/api/v1/admin/image-bed/anonymous-settings') && response.request().method() === 'PUT')
        await gate.uncheck()
        await saveService(page, gateDialog, service.capability, firstSource.key, false)
        expect((await gateClosed).status()).toBe(200)
        expect((await read<AnonymousSettings>(page, '/api/v1/admin/image-bed/anonymous-settings')).enabled).toBe(false)
        const reopenedGateDialog = await openService(page, service.name)
        await expect(reopenedGateDialog.getByRole('checkbox', { name: '允许匿名上传', exact: true })).toBeDisabled()
        await reopenedGateDialog.getByRole('button', { name: '取消', exact: true }).click()
      }

      await openService(page, service.name)
      await dialog.getByRole('checkbox', { name: '启用服务', exact: true }).check()
      await saveService(page, dialog, service.capability, firstSource.key, true)

      await openService(page, service.name)
      await selectSource(page, dialog, '未绑定')
      await expect(dialog.getByRole('checkbox', { name: '启用服务', exact: true })).toBeChecked()
      await expect(dialog.getByRole('button', { name: '保存服务设置', exact: true })).toBeDisabled()
      await dialog.getByRole('checkbox', { name: '启用服务', exact: true }).uncheck()
      await saveService(page, dialog, service.capability, '', false)
      if (service.capability === 'image_bed') {
        const unboundDialog = await openService(page, service.name)
        await expect(unboundDialog.getByRole('checkbox', { name: '允许匿名上传', exact: true })).toBeDisabled()
        await unboundDialog.getByRole('button', { name: '取消', exact: true }).click()
      }

      await openService(page, service.name)
      await selectSource(page, dialog, publicSource.name)
      await dialog.getByRole('checkbox', { name: '启用服务', exact: true }).check()
      await saveService(page, dialog, service.capability, publicSource.key, true)
    }

    // A second administrator changes the live binding while this dialog holds a draft.
    const conflictDialog = await openService(page, '公开网盘')
    const stale = await binding(page, 'public_drive')
    await selectSource(page, conflictDialog, teamSource.name)
    await mutate(page, csrf, '/api/v1/admin/capabilities/public_drive', {
      enabled: false, storage_source_key: publicSource.key, expected_revision: stale.revision,
    })
    const conflictResponse = page.waitForResponse((response) => (
      new URL(response.url()).pathname === '/api/v1/admin/capabilities/public_drive'
      && response.request().method() === 'PUT'
    ))
    const conflictRefresh = page.waitForResponse((response) => (
      new URL(response.url()).pathname === '/api/v1/admin/capabilities'
      && response.request().method() === 'GET'
    ))
    await conflictDialog.getByRole('button', { name: '保存服务设置', exact: true }).click()
    const rejected = await conflictResponse
    expect(rejected.status()).toBe(409)
    expect((await conflictRefresh).status()).toBe(200)
    expect(rejected.request().postDataJSON().expected_revision).toBe(stale.revision)
    await expect(conflictDialog).toBeVisible()
    await expect(conflictDialog.getByText(/配置已被其他管理员修改/)).toBeVisible()
    await expect(conflictDialog.getByRole('combobox', { name: '绑定存储源', exact: true })).toContainText(teamSource.name)
    await expect(conflictDialog.getByRole('checkbox', { name: '启用服务', exact: true })).toBeChecked()
    await saveService(page, conflictDialog, 'public_drive', teamSource.key, true)

    // Failed writes leave the dialog and draft available and never change live state.
    const beforeFailedSave = await binding(page, 'image_bed')
    const failedDialog = await openService(page, '快速图床')
    await selectSource(page, failedDialog, teamSource.name)
    const failureMessage = 'E2E 服务保存失败，配置未生效'
    await page.route('**/api/v1/admin/capabilities/image_bed', async (route) => {
      if (route.request().method() !== 'PUT') return route.continue()
      await route.fulfill({ status: 500, json: { error: { code: 'INTERNAL_ERROR', message: failureMessage }, request_id: 'site-save-failure' } })
    })
    await failedDialog.getByRole('button', { name: '保存服务设置', exact: true }).click()
    await expect(failedDialog.getByText(failureMessage, { exact: true })).toBeVisible()
    await expect(failedDialog).toBeVisible()
    await expect(failedDialog.getByRole('combobox', { name: '绑定存储源', exact: true })).toContainText(teamSource.name)
    expect(await binding(page, 'image_bed')).toEqual(beforeFailedSave)
    await page.unroute('**/api/v1/admin/capabilities/image_bed')
    await failedDialog.getByRole('button', { name: '取消', exact: true }).click()

    const imageCard = serviceCard(page, '快速图床')
    await expect(imageCard.getByRole('checkbox', { name: '允许匿名上传', exact: true })).toHaveCount(0)
    await expect(imageCard.getByRole('button', { name: '保留策略', exact: true })).toHaveCount(0)
    // Starting from off exercises the old disabled-settings API returning an empty key.
    await mutate(page, csrf, '/api/v1/admin/image-bed/anonymous-settings', { enabled: false })
    await page.reload()
    const anonymousFailure = 'E2E 匿名设置保存失败'
    const failedAnonDialog = await openService(page, '快速图床')
    const failedAnonCheckbox = failedAnonDialog.getByRole('checkbox', { name: '允许匿名上传', exact: true })
    await expect(failedAnonCheckbox).toBeEnabled()
    await expect(failedAnonCheckbox).not.toBeChecked()
    await page.route('**/api/v1/admin/image-bed/anonymous-settings', async (route) => {
      if (route.request().method() !== 'PUT') return route.continue()
      await route.fulfill({ status: 500, json: { error: { code: 'INTERNAL_ERROR', message: anonymousFailure }, request_id: 'site-anonymous-failure' } })
    })
    await failedAnonCheckbox.check()
    await failedAnonDialog.getByRole('button', { name: '保存服务设置', exact: true }).click()
    await expect(failedAnonDialog.getByText(anonymousFailure, { exact: true })).toBeVisible()
    await expect(failedAnonCheckbox).toBeChecked()
    expect((await read<AnonymousSettings>(page, '/api/v1/admin/image-bed/anonymous-settings')).enabled).toBe(false)
    await page.unroute('**/api/v1/admin/image-bed/anonymous-settings')
    await failedAnonDialog.getByRole('button', { name: '取消', exact: true }).click()

    for (const enabled of [true, false, true]) {
      const anonDialog = await openService(page, '快速图床')
      const anonymous = anonDialog.getByRole('checkbox', { name: '允许匿名上传', exact: true })
      const saved = page.waitForResponse((response) => (
        new URL(response.url()).pathname === '/api/v1/admin/image-bed/anonymous-settings'
        && response.request().method() === 'PUT'
      ))
      await expect(anonymous).toBeChecked({ checked: !enabled })
      await anonymous.click()
      await saveService(page, anonDialog, 'image_bed', publicSource.key, true)
      expect((await saved).status()).toBe(200)
      const persisted = await read<AnonymousSettings>(page, '/api/v1/admin/image-bed/anonymous-settings')
      expect(persisted.enabled).toBe(enabled)
      expect((await binding(page, 'image_bed')).source_key).toBe(publicSource.key)
    }

    // Retention writes inside the service modal persist cleanly alongside the service binding.
    const retentionDialog = await openService(page, '快速图床')
    await retentionDialog.getByRole('spinbutton', { name: '登录用户图片保留天数' }).fill('30')
    await retentionDialog.getByRole('spinbutton', { name: '匿名图片保留天数' }).fill('7')
    const retentionSaved = page.waitForResponse((response) => (
      new URL(response.url()).pathname === '/api/v1/admin/image-bed/retention'
      && response.request().method() === 'PUT'
    ))
    await saveService(page, retentionDialog, 'image_bed', publicSource.key, true)
    expect((await retentionSaved).status()).toBe(200)
    expect(await read<Retention>(page, '/api/v1/admin/image-bed/retention')).toMatchObject({ user_retention_days: 30, anonymous_retention_days: 7 })
    expect((await read<AnonymousSettings>(page, '/api/v1/admin/image-bed/anonymous-settings')).enabled).toBe(true)
    const reopenedRetention = await openService(page, '快速图床')
    await expect(reopenedRetention.getByRole('spinbutton', { name: '登录用户图片保留天数' })).toHaveValue('30')
    await expect(reopenedRetention.getByRole('spinbutton', { name: '匿名图片保留天数' })).toHaveValue('7')
    await reopenedRetention.getByRole('button', { name: '取消', exact: true }).click()

    // A disabled carrier cannot be selected; an existing anonymous gate can still be closed.
    await mutate(page, csrf, `/api/v1/admin/sources/${publicSource.key}/disable`, undefined, 'POST')
    await page.reload()
    const disabledSourceDialog = await openService(page, '快速图床')
    const disabledGateCheckbox = disabledSourceDialog.getByRole('checkbox', { name: '允许匿名上传', exact: true })
    await expect(disabledGateCheckbox).toBeEnabled()
    const disabledGateClosed = page.waitForResponse((response) => response.url().endsWith('/api/v1/admin/image-bed/anonymous-settings') && response.request().method() === 'PUT')
    await disabledGateCheckbox.uncheck()
    await saveService(page, disabledSourceDialog, 'image_bed', publicSource.key, true)
    expect((await disabledGateClosed).status()).toBe(200)
    expect((await read<AnonymousSettings>(page, '/api/v1/admin/image-bed/anonymous-settings')).enabled).toBe(false)
    const checkDisabledDialog = await openService(page, '快速图床')
    await expect(checkDisabledDialog.getByRole('checkbox', { name: '允许匿名上传', exact: true })).toBeDisabled()
    await checkDisabledDialog.getByRole('combobox', { name: '绑定存储源', exact: true }).click()
    await expect(page.getByRole('option', { name: new RegExp(publicSource.name) })).toHaveAttribute('aria-disabled', 'true')
    await page.keyboard.press('Escape')
    await checkDisabledDialog.getByRole('button', { name: '取消', exact: true }).click()
    await mutate(page, csrf, `/api/v1/admin/sources/${publicSource.key}/enable`, undefined, 'POST')
    await page.reload()
    const enabledAgainDialog = await openService(page, '快速图床')
    await expect(enabledAgainDialog.getByRole('checkbox', { name: '允许匿名上传', exact: true })).toBeEnabled()
    await expect(enabledAgainDialog.getByRole('checkbox', { name: '允许匿名上传', exact: true })).not.toBeChecked()
    await enabledAgainDialog.getByRole('button', { name: '取消', exact: true }).click()
  } finally {
    await page.unroute('**/api/v1/admin/capabilities/image_bed')
    await page.unroute('**/api/v1/admin/image-bed/anonymous-settings')
    for (const source of originalSources) {
      const current = (await read<{ items: Source[] }>(page, '/api/v1/admin/sources')).items.find((item) => item.key === source.key)
      if (current?.is_disabled !== source.is_disabled) {
        await mutate(page, csrf, `/api/v1/admin/sources/${source.key}/${source.is_disabled ? 'disable' : 'enable'}`, undefined, 'POST')
      }
    }
    for (const original of originalBindings) {
      const current = await binding(page, original.capability)
      await mutate(page, csrf, `/api/v1/admin/capabilities/${original.capability}`, {
        enabled: original.enabled,
        storage_source_key: original.source_key ?? '',
        expected_revision: current.revision,
      })
    }
    await mutate(page, csrf, '/api/v1/admin/image-bed/anonymous-settings', { enabled: originalAnonymous.enabled })
    await mutate(page, csrf, '/api/v1/admin/image-bed/retention', originalRetention)
    const restored = await bindings(page)
    for (const original of originalBindings) {
      const current = restored.find((item) => item.capability === original.capability)!
      expect(current.enabled).toBe(original.enabled)
      expect(current.source_key ?? '').toBe(original.source_key ?? '')
    }
    expect((await read<AnonymousSettings>(page, '/api/v1/admin/image-bed/anonymous-settings')).enabled).toBe(originalAnonymous.enabled)
    expect(await read<Retention>(page, '/api/v1/admin/image-bed/retention')).toMatchObject(originalRetention)
  }
})

test('site navigation groups services and general settings without desktop or mobile overflow', async ({ page, baseURL }) => {
  test.setTimeout(90_000)
  expect(baseURL, 'Playwright must supply the fixture baseURL').toBeTruthy()
  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  await login(page)
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.goto('/app/admin?section=site')
  const serviceTab = page.getByRole('tab', { name: '站点服务', exact: true })
  const generalTab = page.getByRole('tab', { name: '通用配置', exact: true })
  await expect(serviceTab).toHaveAttribute('aria-selected', 'true')
  for (const service of services) await expect(serviceCard(page, service.name)).toBeVisible()
  const nav = page.getByRole('navigation', { name: '设置分组', exact: true })
  await expect(nav.getByRole('button', { name: '站点配置', exact: true })).toHaveAttribute('aria-current', 'page')
  for (const oldName of ['配置导出', '品牌信息', '匿名图床']) {
    await expect(nav.getByRole('button', { name: oldName, exact: true })).toHaveCount(0)
  }
  await page.screenshot({ path: '/tmp/omni-site-desktop.png', fullPage: true })
  await generalTab.click()
  await expect(generalTab).toHaveAttribute('aria-selected', 'true')
  await expect.poll(() => new URL(page.url()).searchParams.get('tab')).toBe('general')
  for (const heading of ['品牌信息', '运行信息', '导出系统配置包']) {
    await expect(page.getByRole('heading', { name: heading, exact: true })).toBeVisible()
  }

  for (const oldSection of ['backup', 'branding', 'image-bed']) {
    await page.goto(`/app/admin?section=${oldSection}`)
    await expect.poll(() => new URL(page.url()).searchParams.get('section')).toBe('site')
    if (oldSection === 'image-bed') {
      await expect(serviceTab).toHaveAttribute('aria-selected', 'true')
      await expect(serviceCard(page, '快速图床')).toBeVisible()
    } else {
      await expect(generalTab).toHaveAttribute('aria-selected', 'true')
      await expect.poll(() => new URL(page.url()).searchParams.get('tab')).toBe('general')
    }
  }

  for (const width of [320, 375]) {
    await page.setViewportSize({ width, height: 812 })
    await page.goto('/app/admin?section=site')
    await expect(serviceTab).toBeInViewport()
    await expect(generalTab).toBeInViewport()
    await expectNoOverflow(page)
    for (const service of services) {
      const card = serviceCard(page, service.name)
      await expect(card).toBeVisible()
      expect(await card.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
      const dialog = await openService(page, service.name)
      if (service.capability === 'image_bed') {
        await expect(dialog.getByRole('checkbox', { name: '允许匿名上传', exact: true })).toBeVisible()
        await expect(dialog.getByRole('spinbutton', { name: '登录用户图片保留天数' })).toBeVisible()
        await expect(dialog.getByRole('spinbutton', { name: '匿名图片保留天数' })).toBeVisible()
      }
      await expectNoOverflow(page, dialog)
      await dialog.getByRole('button', { name: '取消', exact: true }).click()
    }
    if (width === 375) await page.screenshot({ path: '/tmp/omni-site-mobile.png', fullPage: true })
    await generalTab.click()
    for (const heading of ['品牌信息', '运行信息', '导出系统配置包']) {
      await expect(page.getByRole('heading', { name: heading, exact: true })).toBeVisible()
    }
    await expectNoOverflow(page)
  }
  expect(errors).toEqual([])
})
