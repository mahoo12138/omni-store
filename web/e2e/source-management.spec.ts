import { mkdirSync, rmSync } from 'node:fs'
import { dirname, join } from 'node:path'

import { expect, test, type Page } from '@playwright/test'

// 存储源生命周期：预检 → 创建 → 上传可用 → 能力绑定守卫 → 回收站守卫 → 删除。
test.describe.configure({ timeout: 120_000 })

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

async function adminSourcesSection(page: Page) {
  await page.goto('/login')
  await page.getByLabel('用户名').fill('admin')
  await page.getByLabel('密码').fill('OmniStore-Test-Admin!')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page).toHaveURL(/\/app$/)
  await page.goto('/app/admin?section=sources')
  await expect(page.getByRole('heading', { name: '存储源', exact: true })).toBeVisible()
}

test('admin creates a source through preflight and deletes it under guards', async ({ page }) => {
  test.setTimeout(180_000)
  const sourceName = `e2e-src-${Date.now()}`
  const fileName = `e2e-src-file-${Date.now()}.txt`

  // 0. 在种子源同级目录下准备一个空目录作为新源根。
  const sources = await adminAPI(page, 'GET', '/api/v1/admin/sources')
  expect(sources.status).toBe(200)
  const team = sources.body.data.items.find((item: { name: string }) => item.name === '团队文件')
  expect(team).toBeTruthy()
  const rootPath = join(dirname(team.root_path), sourceName)
  mkdirSync(rootPath, { recursive: true })

  try {
    await adminSourcesSection(page)

    // 1. 新建存储源：填写名称与绝对路径 → 预检通过 → 确认创建。
    await page.getByRole('button', { name: '新建存储源' }).click()
    const dialog = page.getByRole('dialog', { name: '新建存储源' })
    await dialog.getByLabel('名称').fill(sourceName)
    await dialog.getByLabel('真实目录绝对路径').fill(rootPath)
    await dialog.getByRole('button', { name: '预检目录' }).click()
    const preview = page.getByRole('region', { name: '目录预检结果' })
    await expect(preview).toContainText('安全与读写预检通过')
    await dialog.getByRole('button', { name: '确认创建' }).click()
    await expect(dialog).toHaveCount(0)
    await expect(page.getByText(sourceName).first()).toBeVisible()

    // 2. 新源立即可用：上传文件成功。
    await page.goto('/app')
    await page.getByRole('button', { name: `打开存储源 ${sourceName}` }).click()
    await expect(page.getByRole('heading', { name: sourceName })).toBeVisible()
    await page.locator('input[type="file"]').setInputFiles({ name: fileName, mimeType: 'text/plain', buffer: Buffer.from('new source\n') })
    await expect(page.getByRole('status', { name: '上传任务' })).toContainText('已上传 1 个文件')
    await expect(page.getByRole('row', { name: new RegExp(fileName) })).toBeVisible()

    // 3. 能力绑定守卫：删除被公开网盘绑定的种子源被拒绝（原生 alert）。
    await adminSourcesSection(page)
    const publicCard = page.locator('article').filter({ hasText: '公开演示资料' }).first()
    await publicCard.getByRole('button', { name: '删除存储源 公开演示资料' }).click()
    const guardDialog = page.getByRole('dialog', { name: '删除存储源' })
    let guardAlert = ''
    page.once('dialog', (alertDialog) => {
      guardAlert = alertDialog.message()
      void alertDialog.dismiss()
    })
    await guardDialog.getByRole('button', { name: '确认删除' }).click()
    await expect.poll(() => guardAlert, { timeout: 10_000 }).not.toBe('')
    expect(guardAlert).toMatch(/能力|绑定/)
    await guardDialog.getByRole('button', { name: '取消' }).click()
    await expect(guardDialog).toHaveCount(0)

    // 4. 回收站守卫：先把文件移入回收站，删除源被拒；清空回收站后可删。
    await page.goto('/app')
    await page.getByRole('button', { name: `打开存储源 ${sourceName}` }).click()
    await page.getByRole('row', { name: new RegExp(fileName) }).getByRole('button', { name: `更多操作 ${fileName}` }).click()
    await page.getByRole('menuitem', { name: '删除', exact: true }).click()
    await page.getByRole('dialog', { name: '移入回收站' }).getByRole('button', { name: '移入回收站' }).click()
    await expect(page.getByRole('row', { name: new RegExp(fileName) })).toHaveCount(0)

    await adminSourcesSection(page)
    const newCard = page.locator('article').filter({ hasText: sourceName }).first()
    await newCard.getByRole('button', { name: `删除存储源 ${sourceName}` }).click()
    const deleteDialog = page.getByRole('dialog', { name: '删除存储源' })
    let trashAlert = ''
    page.once('dialog', (alertDialog) => {
      trashAlert = alertDialog.message()
      void alertDialog.dismiss()
    })
    await deleteDialog.getByRole('button', { name: '确认删除' }).click()
    await expect.poll(() => trashAlert, { timeout: 10_000 }).toContain('回收站')
    await deleteDialog.getByRole('button', { name: '取消' }).click()
    await expect(deleteDialog).toHaveCount(0)

    await page.goto('/app')
    await page.getByRole('button', { name: `打开存储源 ${sourceName}` }).click()
    await page.getByRole('button', { name: '回收站' }).click()
    await expect(page.getByRole('heading', { name: '回收站', exact: true })).toBeVisible()
    await expect(page.getByRole('status').filter({ hasText: '移入回收站' })).toHaveCount(0, { timeout: 12_000 })
    const rows = page.getByRole('row')
      .filter({ has: page.getByText(fileName, { exact: true }) })
      .filter({ has: page.getByRole('button', { name: '永久删除' }) })
    await expect(rows.first()).toBeVisible()
    await rows.first().getByRole('button', { name: '永久删除' }).click()
    await page.getByRole('dialog', { name: '永久删除' }).getByRole('button', { name: '永久删除' }).click()
    await expect(rows).toHaveCount(0)

    await adminSourcesSection(page)
    await newCard.getByRole('button', { name: `删除存储源 ${sourceName}` }).click()
    const finalDialog = page.getByRole('dialog', { name: '删除存储源' })
    await finalDialog.getByRole('button', { name: '确认删除' }).click()
    await expect(finalDialog).toHaveCount(0)
    await expect(page.locator('article').filter({ hasText: sourceName })).toHaveCount(0)
  } finally {
    // 兜底：API 删除残留源（先清其回收站），并移除磁盘目录。
    const list = await adminAPI(page, 'GET', '/api/v1/admin/sources')
    const leftover = (list.body?.data?.items ?? []).find((item: { name: string }) => item.name === sourceName)
    if (leftover) {
      const trash = await adminAPI(page, 'GET', `/api/v1/sources/${encodeURIComponent(leftover.key)}/trash`)
      for (const entry of ((trash.body?.data?.items ?? []) as Array<{ key: string }>)) {
        await adminAPI(page, 'DELETE', `/api/v1/sources/${encodeURIComponent(leftover.key)}/trash/${entry.key}`)
      }
      await adminAPI(page, 'DELETE', `/api/v1/admin/sources/${encodeURIComponent(leftover.key)}`)
    }
    rmSync(rootPath, { recursive: true, force: true })
  }
})
