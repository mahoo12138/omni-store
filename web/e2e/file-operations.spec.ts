import { expect, test, type Page } from '@playwright/test'

const baseURL = process.env.OMNISTORE_E2E_BASE_URL ?? 'http://127.0.0.1:18080'

// 这些用例包含通知退场和 tooltip 重试等待，长于全局 30s 上限。
test.describe.configure({ timeout: 60_000 })

// 粘贴任务卡是常驻 toast（duration Infinity），任务状态每次更新都会重建节点。
// 等"粘贴完成"定格后用 force 点击关闭，避免命中 detach 重试超时。
async function closePasteTaskToast(page: Page) {
  const status = page.getByRole('status', { name: '粘贴任务' })
  if (await status.count() === 0) return
  await expect(status.first()).toContainText('粘贴完成')
  await status.first().getByRole('button', { name: '关闭' }).click({ force: true })
}

async function openTrash(page: Page) {
  await page.getByRole('button', { name: '回收站' }).click()
  await expect(page.getByRole('heading', { name: '回收站', exact: true })).toBeVisible()
  // 右下角通知卡会自动消失；在它退场前点击表格行会被拦截。
  await expect(page.getByRole('status').filter({ hasText: '移入回收站' })).toHaveCount(0, { timeout: 12_000 })
}

// 路由切换期间旧文件列表可能仍在 DOM；只匹配带“永久删除”的回收站行，
// 避免把文件列表行当成回收站行。
function trashRows(page: Page, name: string) {
  return page.getByRole('row')
    .filter({ has: page.getByText(name, { exact: true }) })
    .filter({ has: page.getByRole('button', { name: '永久删除' }) })
}

// API 登录：清理类操作走 API，避免依赖慢环境下易抖动的回收站 UI 交互。
async function apiAuth(page: Page, username: string, password: string) {
  const response = await page.request.post(`${baseURL}/api/v1/auth/login`, {
    data: { username, password },
  })
  expect(response.ok()).toBeTruthy()
  const body = await response.json()
  const cookie = response.headersArray().find((header) => header.name.toLowerCase() === 'set-cookie')!.value.split(';')[0]
  return { cookie, csrf: body.data.csrf_token as string }
}

test('directory can be created, renamed, copied, cut-pasted and cleaned up', async ({ page }) => {
  const suffix = Date.now()
  const originalName = `e2e-ops-${suffix}`
  const renamedName = `${originalName}-renamed`
  const pasteDestinationName = `${originalName}-paste-target`
  const moveDestinationName = `${originalName}-move-target`

  await page.goto('/login')
  await page.getByLabel('用户名').fill('demo')
  await page.getByLabel('密码').fill('OmniStore-Test-Demo!')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await page.getByRole('button', { name: '打开存储源 团队文件' }).click()

  await page.getByRole('button', { name: '上传目录' }).click()
  const directoryDialog = page.getByRole('dialog', { name: '上传目录', exact: true })
  await expect(directoryDialog).toBeVisible()
  await expect(directoryDialog).toContainText('保留子目录结构')
  await directoryDialog.getByRole('button', { name: '取消', exact: true }).click()

  await page.getByRole('button', { name: '创建文件夹' }).click()
  const createDialog = page.getByRole('dialog', { name: '新建文件夹' })
  await createDialog.getByLabel('目录名').fill(originalName)
  await createDialog.getByRole('button', { name: '创建', exact: true }).click()
  let row = page.getByRole('row', { name: new RegExp(originalName) })
  await expect(row).toBeVisible()

  await row.getByRole('button', { name: `更多操作 ${originalName}` }).click()
  await page.getByRole('menuitem', { name: '重命名', exact: true }).click()
  const renameDialog = page.getByRole('dialog', { name: '重命名' })
  await renameDialog.getByLabel('新名称').fill(renamedName)
  await renameDialog.getByRole('button', { name: '保存', exact: true }).click()
  await expect(page.getByRole('row', { name: new RegExp(renamedName) })).toBeVisible()
  await expect(page.getByRole('row', { name: new RegExp(`${originalName}$`) })).toHaveCount(0)

  for (const name of [pasteDestinationName, moveDestinationName]) {
    await page.getByRole('button', { name: '创建文件夹' }).click()
    const createDialog = page.getByRole('dialog', { name: '新建文件夹' })
    await createDialog.getByLabel('目录名').fill(name)
    await createDialog.getByRole('button', { name: '创建', exact: true }).click()
    await expect(page.getByRole('row', { name: new RegExp(name) })).toBeVisible()
  }

  row = page.getByRole('row', { name: new RegExp(renamedName) })
  await row.getByRole('button', { name: '复制' }).click()
  const clipboard = page.getByRole('region', { name: '文件剪贴板' })
  await expect(clipboard).toContainText('已复制 1 项')
  const pasteButton = clipboard.getByRole('button', { name: '粘贴到此处' })
  await expect(pasteButton).toBeDisabled()
  // Tooltip 包装层本身可聚焦；聚焦比悬停稳定（disabled 控件不派发鼠标事件）。
  // 通知卡可能仍在退场，且重复 focus 不会再次触发 focus 事件；等待通知消失，
  // 再以悬停 + 失焦/聚焦重试 tooltip。
  await expect(page.getByRole('status').filter({ hasText: '已复制' })).toHaveCount(0, { timeout: 12_000 })
  await expect(async () => {
    const trigger = pasteButton.locator('..')
    await trigger.hover()
    await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur())
    await trigger.focus()
    await expect(page.getByRole('tooltip')).toContainText(`目标目录与“${renamedName}”的来源目录相同`, { timeout: 2_000 })
  }).toPass({ timeout: 15_000 })

  await page.getByRole('row', { name: new RegExp(pasteDestinationName) }).getByRole('button', { name: pasteDestinationName, exact: true }).click()
  await clipboard.getByRole('button', { name: '粘贴到此处' }).click()
  await expect(page.getByRole('status', { name: '粘贴任务' })).toContainText('已粘贴 1 项')
  await expect(page.getByRole('row', { name: new RegExp(renamedName) })).toBeVisible()
  await closePasteTaskToast(page)

  row = page.getByRole('row', { name: new RegExp(renamedName) })
  await row.getByRole('button', { name: `更多操作 ${renamedName}` }).click()
  await page.getByRole('menuitem', { name: '剪切', exact: true }).click()
  await expect(page.getByRole('region', { name: '文件剪贴板' })).toContainText('已剪切 1 项')
  await page.locator('nav[aria-label="面包屑"]').getByText('团队文件', { exact: true }).click()
  await page.getByRole('row', { name: new RegExp(moveDestinationName) }).getByRole('button', { name: moveDestinationName, exact: true }).click()
  await page.getByRole('region', { name: '文件剪贴板' }).getByRole('button', { name: '粘贴到此处' }).click()
  await expect(page.getByRole('status', { name: '粘贴任务' })).toContainText('已粘贴 1 项')
  await expect(page.getByRole('row', { name: new RegExp(renamedName) })).toBeVisible()
  await closePasteTaskToast(page)

  await page.locator('nav[aria-label="面包屑"]').getByText('团队文件', { exact: true }).click()

  for (const name of [renamedName, pasteDestinationName, moveDestinationName]) {
    row = page.getByRole('row', { name: new RegExp(name) })
    await row.getByRole('button', { name: `更多操作 ${name}` }).click()
    await page.getByRole('menuitem', { name: '删除', exact: true }).click()
    await page.getByRole('dialog', { name: '移入回收站' }).getByRole('button', { name: '移入回收站' }).click()
    await expect(row).toHaveCount(0)
  }

  await openTrash(page)
  for (const name of [renamedName, pasteDestinationName, moveDestinationName]) {
    const row = trashRows(page, name)
    await expect(row.first()).toBeVisible()
    await row.first().getByRole('button', { name: '永久删除' }).click()
    await page.getByRole('dialog', { name: '永久删除' }).getByRole('button', { name: '永久删除' }).click()
    await expect(row).toHaveCount(0)
  }
})

test('multi-select paste reports partial conflicts and keeps only failed copies for retry', async ({ page }) => {
  const suffix = Date.now()
  const sourceA = `e2e-partial-a-${suffix}`
  const sourceB = `e2e-partial-b-${suffix}`
  const target = `e2e-partial-target-${suffix}`

  await page.goto('/login')
  await page.getByLabel('用户名').fill('demo')
  await page.getByLabel('密码').fill('OmniStore-Test-Demo!')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await page.getByRole('button', { name: '打开存储源 团队文件' }).click()

  async function createFolder(name: string) {
    await page.getByRole('button', { name: '创建文件夹' }).click()
    const dialog = page.getByRole('dialog', { name: '新建文件夹' })
    await dialog.getByLabel('目录名').fill(name)
    await dialog.getByRole('button', { name: '创建', exact: true }).click()
    await expect(page.getByRole('row', { name: new RegExp(name) })).toBeVisible()
  }

  await createFolder(sourceA)
  await createFolder(sourceB)
  await createFolder(target)
  await page.getByRole('row', { name: new RegExp(target) }).getByRole('button', { name: target, exact: true }).click()
  await createFolder(sourceA)
  await page.locator('nav[aria-label="面包屑"]').getByText('团队文件', { exact: true }).click()

  await page.getByRole('row', { name: new RegExp(sourceA) }).getByRole('checkbox', { name: `选择 ${sourceA}` }).check()
  await page.getByRole('row', { name: new RegExp(sourceB) }).getByRole('checkbox', { name: `选择 ${sourceB}` }).check()
  const selectionToolbar = page.getByRole('toolbar', { name: '批量文件操作' })
  await expect(selectionToolbar).toContainText('已选择 2 项')
  await selectionToolbar.getByRole('button', { name: '复制', exact: true }).click()

  const clipboard = page.getByRole('region', { name: '文件剪贴板' })
  await expect(clipboard).toContainText('已复制 2 项')
  await page.getByRole('row', { name: new RegExp(target) }).getByRole('button', { name: target, exact: true }).click()
  await clipboard.getByRole('button', { name: '粘贴到此处' }).click()

  const pasteStatus = page.getByRole('status', { name: '粘贴任务' })
  await expect(pasteStatus).toContainText('1 项成功，1 项失败')
  await expect(page.getByRole('row', { name: new RegExp(sourceB) })).toBeVisible()
  await expect(page.getByRole('row', { name: new RegExp(sourceA) })).toBeVisible()
  await expect(clipboard).toContainText('已复制 1 项')
  await page.screenshot({ path: '/tmp/omni-store-partial-copy-paste.png', fullPage: false })
  await closePasteTaskToast(page)

  const conflictRow = page.getByRole('row', { name: new RegExp(sourceA) })
  await conflictRow.getByRole('button', { name: `更多操作 ${sourceA}` }).click()
  await page.getByRole('menuitem', { name: '删除', exact: true }).click()
  await page.getByRole('dialog', { name: '移入回收站' }).getByRole('button', { name: '移入回收站' }).click()
  await expect(conflictRow).toHaveCount(0)

  await clipboard.getByRole('button', { name: '粘贴到此处' }).click()
  await expect(page.getByRole('row', { name: new RegExp(sourceA) })).toBeVisible()
  await closePasteTaskToast(page)
  await clipboard.getByRole('button', { name: '清空剪贴板' }).click()

  await page.locator('nav[aria-label="面包屑"]').getByText('团队文件', { exact: true }).click()
  for (const name of [sourceA, sourceB, target]) {
    const row = page.getByRole('row', { name: new RegExp(name) })
    await row.getByRole('button', { name: `更多操作 ${name}` }).click()
    await page.getByRole('menuitem', { name: '删除', exact: true }).click()
    await page.getByRole('dialog', { name: '移入回收站' }).getByRole('button', { name: '移入回收站' }).click()
    await expect(row).toHaveCount(0)
  }

  // 清理改走 API：慢环境下回收站行在路由切换/重渲染窗口会让 UI 清理偶发超时；
  // 被清对象与断言无关，按条目 key 直接永久删除即可。
  const demo = await apiAuth(page, 'demo', 'OmniStore-Test-Demo!')
  const sourcesResponse = await page.request.fetch(`${baseURL}/api/v1/sources`, { headers: { cookie: demo.cookie } })
  const teamKey = ((await sourcesResponse.json()).data.items as Array<{ key: string; name: string }>)
    .find((item) => item.name === '团队文件')!.key
  const trashResponse = await page.request.fetch(`${baseURL}/api/v1/sources/${teamKey}/trash`, { headers: { cookie: demo.cookie } })
  const entries = ((await trashResponse.json()).data.items as Array<{ key: string; original_path: string }>)
  for (const entry of entries) {
    if (![sourceA, sourceB, target].some((name) => entry.original_path.includes(name))) continue
    const purge = await page.request.fetch(
      `${baseURL}/api/v1/sources/${teamKey}/trash/${encodeURIComponent(entry.key)}`,
      { method: 'DELETE', headers: { cookie: demo.cookie, 'X-CSRF-Token': demo.csrf } },
    )
    expect(purge.status()).toBe(200)
  }
})

test('batch delete confirms selection and keeps failed entries selected for retry', async ({ page }) => {
  const suffix = Date.now()
  const first = `e2e-batch-delete-a-${suffix}`
  const second = `e2e-batch-delete-b-${suffix}`
  const browserErrors: string[] = []
  page.on('pageerror', (error) => browserErrors.push(error.message))
  page.on('console', (message) => {
    if (message.type() === 'error' && !message.text().startsWith('Failed to load resource: the server responded with a status of 500')) {
      browserErrors.push(message.text())
    }
  })

  await page.goto('/login')
  await page.getByLabel('用户名').fill('demo')
  await page.getByLabel('密码').fill('OmniStore-Test-Demo!')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await page.getByRole('button', { name: '打开存储源 团队文件' }).click()
  await expect(page).toHaveTitle('OmniStore')
  await expect(page).toHaveURL(/\/app\/sources\/[^/]+/)
  await expect(page.getByRole('heading', { name: '团队文件' })).toBeVisible()

  async function createFolder(name: string) {
    await page.getByRole('button', { name: '创建文件夹' }).click()
    const dialog = page.getByRole('dialog', { name: '新建文件夹' })
    await dialog.getByLabel('目录名').fill(name)
    await dialog.getByRole('button', { name: '创建', exact: true }).click()
    await expect(page.getByRole('row', { name: new RegExp(name) })).toBeVisible()
  }

  await createFolder(first)
  await createFolder(second)
  await page.getByRole('row', { name: new RegExp(first) }).getByRole('checkbox', { name: `选择 ${first}` }).check()
  await page.getByRole('row', { name: new RegExp(second) }).getByRole('checkbox', { name: `选择 ${second}` }).check()

  const selectionToolbar = page.getByRole('toolbar', { name: '批量文件操作' })
  await selectionToolbar.getByRole('button', { name: '移入回收站' }).click()
  const confirmation = page.getByRole('dialog', { name: '将 2 项移入回收站？' })
  await expect(confirmation).toContainText(first)
  await expect(confirmation).toContainText(second)
  await confirmation.getByRole('button', { name: '取消' }).click()
  await expect(selectionToolbar).toContainText('已选择 2 项')

  await page.route('**/api/v1/sources/**/files?*', async (route) => {
    const request = route.request()
    const requestUrl = new URL(request.url())
    if (request.method() === 'DELETE' && requestUrl.searchParams.get('path') === `/${second}`) {
      await route.fulfill({
        status: 500,
        contentType: 'application/json',
        body: JSON.stringify({ error: { code: 'INTERNAL_ERROR', message: '测试用删除失败' }, request_id: 'e2e' }),
      })
      return
    }
    await route.continue()
  })

  await selectionToolbar.getByRole('button', { name: '移入回收站' }).click()
  await confirmation.getByRole('button', { name: '移入回收站' }).click()
  const deleteStatus = page.getByRole('status', { name: '批量删除任务' })
  await expect(deleteStatus).toContainText('1 项成功，1 项失败')
  await expect(deleteStatus).toBeInViewport({ ratio: 0.9 })
  await expect(page.getByRole('row', { name: new RegExp(first) })).toHaveCount(0)
  await expect(page.getByRole('row', { name: new RegExp(second) }).getByRole('checkbox', { name: `选择 ${second}` })).toBeChecked()
  await page.screenshot({ path: '/tmp/omni-store-batch-delete-partial.png', fullPage: false })

  await page.unrouteAll()
  await expect(selectionToolbar).toContainText('已选择 1 项')
  await selectionToolbar.getByRole('button', { name: '移入回收站' }).click()
  await page.getByRole('dialog', { name: '将 1 项移入回收站？' }).getByRole('button', { name: '移入回收站' }).click()
  await expect(deleteStatus).toContainText('已处理 1 项')
  await expect(page.getByRole('row', { name: new RegExp(second) })).toHaveCount(0)

  await openTrash(page)
  for (const name of [first, second]) {
    const row = trashRows(page, name)
    await expect(row.first()).toBeVisible()
    await row.first().getByRole('button', { name: '永久删除' }).click()
    await page.getByRole('dialog', { name: '永久删除' }).getByRole('button', { name: '永久删除', exact: true }).click()
    await expect(row).toHaveCount(0)
  }
  expect(browserErrors).toEqual([])
})
