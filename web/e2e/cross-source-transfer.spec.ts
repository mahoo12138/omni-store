import { readFileSync } from 'node:fs'

import { expect, test, type Page } from '@playwright/test'

// 跨源传输：剪贴板携带来源存储源，粘贴到另一存储源即触发跨源复制/移动；
// 只读目标目录禁用粘贴；API 侧目标重名冲突 409。
test.describe.configure({ timeout: 60_000 })

const baseURL = process.env.OMNISTORE_E2E_BASE_URL ?? 'http://127.0.0.1:18080'

let adminCookies: string | null = null
let adminCSRF = ''

async function adminLogin(page: Page): Promise<{ cookie: string; csrf: string }> {
  if (adminCookies) return { cookie: adminCookies, csrf: adminCSRF }
  const response = await page.request.post(`${baseURL}/api/v1/auth/login`, {
    data: { username: 'admin', password: 'OmniStore-Test-Admin!' },
  })
  expect(response.ok()).toBeTruthy()
  const body = await response.json()
  adminCookies = response.headersArray().find((header) => header.name.toLowerCase() === 'set-cookie')!.value.split(';')[0]
  adminCSRF = body.data.csrf_token
  return { cookie: adminCookies, csrf: adminCSRF }
}

async function adminAPI(page: Page, method: 'GET' | 'POST' | 'DELETE', path: string, data?: unknown) {
  const { cookie, csrf } = await adminLogin(page)
  const response = await page.request.fetch(`${baseURL}${path}`, {
    method,
    headers: { cookie, 'X-CSRF-Token': csrf },
    data,
  })
  let body: any = null
  try {
    body = await response.json()
  } catch {
    body = null
  }
  return { status: response.status(), body }
}

async function sourceKeys(page: Page): Promise<{ team: string; pub: string }> {
  const sources = await adminAPI(page, 'GET', '/api/v1/admin/sources')
  const items = sources.body.data.items
  const team = items.find((item: { name: string }) => item.name === '团队文件')
  const pub = items.find((item: { name: string }) => item.name === '公开演示资料')
  expect(team).toBeTruthy()
  expect(pub).toBeTruthy()
  return { team: team.key, pub: pub.key }
}

async function loginUI(page: Page, username: string, password: string) {
  await page.goto('/login')
  await page.getByLabel('用户名').fill(username)
  await page.getByLabel('密码').fill(password)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page).toHaveURL(/\/app$/)
}

/** 通过面包屑的存储源列表菜单切换当前存储源。 */
async function switchSource(page: Page, name: string) {
  await page.locator('nav[aria-label="面包屑"]').getByRole('button', { name: '存储源列表' }).click()
  await page.getByRole('menuitem', { name }).click()
  await expect(page.getByRole('heading', { name })).toBeVisible()
}

async function closePasteTaskCard(page: Page) {
  await expect(async () => {
    const card = page.getByRole('status', { name: '粘贴任务' })
    await expect(card).toContainText('粘贴完成')
    await card.getByRole('button', { name: '关闭' }).click({ force: true })
    await expect(card).toHaveCount(0)
  }).toPass({ timeout: 15_000 })
}

async function openTrash(page: Page) {
  await page.getByRole('button', { name: '回收站' }).click()
  await expect(page.getByRole('heading', { name: '回收站', exact: true })).toBeVisible()
  await expect(page.getByRole('status').filter({ hasText: '移入回收站' })).toHaveCount(0, { timeout: 12_000 })
}

function trashRows(page: Page, name: string) {
  return page.getByRole('row')
    .filter({ has: page.getByText(name, { exact: true }) })
    .filter({ has: page.getByRole('button', { name: '永久删除' }) })
}

test('clipboard copies and moves files across sources with content intact', async ({ page }) => {
  const suffix = Date.now()
  const fileName = `e2e-xsrc-${suffix}.txt`
  const moveTarget = `e2e-xsrc-target-${suffix}`

  await loginUI(page, 'admin', 'OmniStore-Test-Admin!')
  await page.getByRole('button', { name: '打开存储源 团队文件' }).click()
  await expect(page.getByRole('heading', { name: '团队文件' })).toBeVisible()

  // 0. 准备：根目录文件 + 移动目标目录。
  await page.locator('input[type="file"]').setInputFiles({ name: fileName, mimeType: 'text/plain', buffer: Buffer.from('cross source payload\n') })
  await expect(page.getByRole('status', { name: '上传任务' })).toContainText('已上传 1 个文件')
  await page.getByRole('button', { name: '创建文件夹' }).click()
  const createDialog = page.getByRole('dialog', { name: '新建文件夹' })
  await createDialog.getByLabel('目录名').fill(moveTarget)
  await createDialog.getByRole('button', { name: '创建', exact: true }).click()
  await expect(page.getByRole('row', { name: new RegExp(moveTarget) })).toBeVisible()

  // 1. 复制 → 切到公开演示资料 → 粘贴（跨源复制）。
  await page.getByRole('row', { name: new RegExp(fileName) }).getByRole('button', { name: `复制 ${fileName}` }).click()
  const clipboard = page.getByRole('region', { name: '文件剪贴板' })
  await expect(clipboard).toContainText('已复制 1 项')
  await expect(clipboard).toContainText('来源：团队文件')
  await switchSource(page, '公开演示资料')
  await expect(clipboard).toContainText('已复制 1 项')
  await clipboard.getByRole('button', { name: '粘贴到此处' }).click()
  await expect(page.getByRole('status', { name: '粘贴任务' })).toContainText('已粘贴 1 项')
  await expect(page.getByRole('row', { name: new RegExp(fileName) })).toBeVisible()
  await closePasteTaskCard(page)

  // 2. 在公开源剪切 → 切回团队文件的子目录粘贴（跨源移动）。
  await page.getByRole('row', { name: new RegExp(fileName) }).getByRole('button', { name: `更多操作 ${fileName}` }).click()
  await page.getByRole('menuitem', { name: '剪切', exact: true }).click()
  await expect(clipboard).toContainText('已剪切 1 项')
  await switchSource(page, '团队文件')
  await page.getByRole('row', { name: new RegExp(moveTarget) }).getByRole('button', { name: moveTarget, exact: true }).click()
  await clipboard.getByRole('button', { name: '粘贴到此处' }).click()
  await expect(page.getByRole('status', { name: '粘贴任务' })).toContainText('已粘贴 1 项')
  await expect(page.getByRole('row', { name: new RegExp(fileName) })).toBeVisible()
  await closePasteTaskCard(page)

  // 3. 移动语义：公开源里不再有该文件；团队文件子目录里的内容完好。
  await switchSource(page, '公开演示资料')
  await expect(page.getByRole('row', { name: new RegExp(fileName) })).toHaveCount(0)
  await switchSource(page, '团队文件')
  await page.getByRole('row', { name: new RegExp(moveTarget) }).getByRole('button', { name: moveTarget, exact: true }).click()
  const downloadPromise = page.waitForEvent('download')
  await page.getByRole('row', { name: new RegExp(fileName) }).getByRole('link', { name: `下载 ${fileName}` }).click()
  expect((await downloadPromise).suggestedFilename()).toBe(fileName)
  expect(readFileSync(await (await downloadPromise).path(), 'utf8')).toBe('cross source payload\n')

  // 4. 清理：回收站永久删除根文件与目标目录。
  for (const name of [fileName, moveTarget]) {
    await page.locator('nav[aria-label="面包屑"]').getByText('团队文件', { exact: true }).click()
    await page.getByRole('row', { name: new RegExp(name) }).getByRole('button', { name: `更多操作 ${name}` }).click()
    await page.getByRole('menuitem', { name: '删除', exact: true }).click()
    await page.getByRole('dialog', { name: '移入回收站' }).getByRole('button', { name: '移入回收站' }).click()
    await expect(page.getByRole('row', { name: new RegExp(name) })).toHaveCount(0)
  }
  await openTrash(page)
  for (const name of [fileName, moveTarget]) {
    const rows = trashRows(page, name)
    while (await rows.count() > 0) {
      const before = await rows.count()
      await rows.first().getByRole('button', { name: '永久删除' }).click()
      await page.getByRole('dialog', { name: '永久删除' }).getByRole('button', { name: '永久删除' }).click()
      await expect(rows).toHaveCount(before - 1)
    }
  }
})

test('read-only target directory disables paste with an explanatory tooltip', async ({ page }) => {
  await loginUI(page, 'demo', 'OmniStore-Test-Demo!')
  await page.getByRole('button', { name: '打开存储源 团队文件' }).click()
  await expect(page.getByRole('heading', { name: '团队文件' })).toBeVisible()

  // 1. demo 进入只读的 projects 目录，复制其中的种子文件（复制只需读权限）。
  await page.getByRole('row', { name: 'projects' }).getByRole('button', { name: 'projects', exact: true }).click()
  await expect(page).toHaveURL(/path=%2Fprojects/)
  await page.getByRole('row', { name: /roadmap\.md/ }).getByRole('button', { name: '复制 roadmap.md' }).click()
  const clipboard = page.getByRole('region', { name: '文件剪贴板' })
  await expect(clipboard).toContainText('已复制 1 项')

  // 2. 目标是只读源：粘贴按钮禁用，tooltip 说明原因。
  await switchSource(page, '公开演示资料')
  const pasteButton = clipboard.getByRole('button', { name: '粘贴到此处' })
  await expect(pasteButton).toBeDisabled()
  await expect(page.getByRole('status').filter({ hasText: '已复制' })).toHaveCount(0, { timeout: 12_000 })
  await expect(async () => {
    const trigger = pasteButton.locator('..')
    await trigger.hover()
    await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur())
    await trigger.focus()
    await expect(page.getByRole('tooltip')).toContainText('当前目录只读，无法粘贴', { timeout: 2_000 })
  }).toPass({ timeout: 15_000 })
})

test('api cross-source copy refuses duplicate targets', async ({ page }) => {
  const fileName = `e2e-xsrc-api-${Date.now()}.txt`
  const { team, pub } = await sourceKeys(page)

  // 0. 上传到团队文件。
  const auth = await adminLogin(page)
  const upload = await page.request.post(`${baseURL}/api/v1/sources/${encodeURIComponent(team)}/upload?path=/`, {
    headers: { cookie: auth.cookie, 'X-CSRF-Token': auth.csrf },
    multipart: { file: { name: fileName, mimeType: 'text/plain', buffer: Buffer.from('api copy\n') } },
  })
  expect(upload.status()).toBe(200)

  // 1. 跨源复制到公开演示资料 → 成功。
  const first = await adminAPI(page, 'POST', `/api/v1/sources/${encodeURIComponent(team)}/files/copy`, {
    path: `/${fileName}`,
    target_source_key: pub,
    target_path: `/${fileName}`,
  })
  expect(first.status).toBe(200)

  // 2. 再次复制到同一目标 → 409。
  const duplicate = await adminAPI(page, 'POST', `/api/v1/sources/${encodeURIComponent(team)}/files/copy`, {
    path: `/${fileName}`,
    target_source_key: pub,
    target_path: `/${fileName}`,
  })
  expect(duplicate.status).toBe(409)

  // 3. 清理两个源里的文件。
  for (const key of [team, pub]) {
    await adminAPI(page, 'DELETE', `/api/v1/sources/${encodeURIComponent(key)}/files?path=${encodeURIComponent(`/${fileName}`)}`)
    const trash = await adminAPI(page, 'GET', `/api/v1/sources/${encodeURIComponent(key)}/trash`)
    const items: Array<{ key: string; name: string }> = trash.body?.data?.items ?? []
    for (const entry of items.filter((item) => item.name === fileName)) {
      await adminAPI(page, 'DELETE', `/api/v1/sources/${encodeURIComponent(key)}/trash/${entry.key}`)
    }
  }
})
