import { readFileSync } from 'node:fs'

import { expect, test, type Page } from '@playwright/test'

// 上传冲突三选决策（跳过/覆盖/取消）是真实整理文件时的核心交互；
// API 侧覆盖目录上传 relative_path、只读父目录、管理命名空间与体积上限。
test.describe.configure({ timeout: 60_000 })

const baseURL = process.env.OMNISTORE_E2E_BASE_URL ?? 'http://127.0.0.1:18080'

let demoCookies: string | null = null
let demoCSRF = ''

async function demoLogin(page: Page): Promise<{ cookie: string; csrf: string }> {
  if (demoCookies) return { cookie: demoCookies, csrf: demoCSRF }
  const response = await page.request.post(`${baseURL}/api/v1/auth/login`, {
    data: { username: 'demo', password: 'OmniStore-Test-Demo!' },
  })
  expect(response.ok()).toBeTruthy()
  const body = await response.json()
  demoCookies = response.headersArray().find((header) => header.name.toLowerCase() === 'set-cookie')!.value.split(';')[0]
  demoCSRF = body.data.csrf_token
  return { cookie: demoCookies, csrf: demoCSRF }
}

async function adminLogin(page: Page): Promise<{ cookie: string; csrf: string }> {
  const response = await page.request.post(`${baseURL}/api/v1/auth/login`, {
    data: { username: 'admin', password: 'OmniStore-Test-Admin!' },
  })
  expect(response.ok()).toBeTruthy()
  const body = await response.json()
  const cookie = response.headersArray().find((header) => header.name.toLowerCase() === 'set-cookie')!.value.split(';')[0]
  return { cookie, csrf: body.data.csrf_token }
}

async function demoAPI(page: Page, method: 'GET' | 'POST' | 'DELETE', path: string, data?: unknown) {
  const { cookie, csrf } = await demoLogin(page)
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

async function teamSourceKey(page: Page): Promise<string> {
  const sources = await demoAPI(page, 'GET', '/api/v1/sources')
  expect(sources.status).toBe(200)
  const team = sources.body.data.items.find((item: { name: string }) => item.name === '团队文件')
  expect(team).toBeTruthy()
  return team.key
}

/** 删除文件并立即从回收站永久清理（清空配额与列表）。 */
async function purgeFile(page: Page, sourceKey: string, path: string, name: string) {
  await demoAPI(page, 'DELETE', `/api/v1/sources/${encodeURIComponent(sourceKey)}/files?path=${encodeURIComponent(path)}`)
  const trash = await demoAPI(page, 'GET', `/api/v1/sources/${encodeURIComponent(sourceKey)}/trash`)
  const items: Array<{ key: string; name: string }> = trash.body?.data?.items ?? []
  for (const entry of items.filter((item) => item.name === name)) {
    await demoAPI(page, 'DELETE', `/api/v1/sources/${encodeURIComponent(sourceKey)}/trash/${entry.key}`)
  }
}

async function closeUploadTaskCard(page: Page) {
  await expect(async () => {
    const card = page.getByRole('status', { name: '上传任务' })
    await card.getByRole('button', { name: '关闭' }).click({ force: true })
    await expect(card).toHaveCount(0)
  }).toPass({ timeout: 15_000 })
}

async function openTeamFiles(page: Page) {
  await page.goto('/login')
  await page.getByLabel('用户名').fill('demo')
  await page.getByLabel('密码').fill('OmniStore-Test-Demo!')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page).toHaveURL(/\/app$/)
  await page.getByRole('button', { name: '打开存储源 团队文件' }).click()
  await expect(page.getByRole('heading', { name: '团队文件' })).toBeVisible()
}

async function downloadRowContent(page: Page, name: string): Promise<string> {
  const downloadPromise = page.waitForEvent('download')
  await page.getByRole('row', { name: new RegExp(name) }).getByRole('link', { name: `下载 ${name}` }).click()
  const download = await downloadPromise
  expect(download.suggestedFilename()).toBe(name)
  return readFileSync(await download.path(), 'utf8')
}

test('upload conflict dialog drives skip, overwrite and cancel decisions', async ({ page }) => {
  const base = `e2e-conflict-${Date.now()}`
  const fileA = `${base}-a.txt`
  const fileB = `${base}-b.txt`
  const fileC = `${base}-c.txt`

  await openTeamFiles(page)
  const fileInput = page.locator('input[type="file"]')

  // 0. 上传基准文件 A（v1）。
  await fileInput.setInputFiles({ name: fileA, mimeType: 'text/plain', buffer: Buffer.from('version-1\n') })
  await expect(page.getByRole('status', { name: '上传任务' })).toContainText('已上传 1 个文件')
  await closeUploadTaskCard(page)
  await expect(page.getByRole('row', { name: new RegExp(fileA) })).toBeVisible()

  // 1. 再传 [A(v2), B] → 冲突对话框点名 A → 跳过冲突文件：B 入库、A 仍是 v1。
  await fileInput.setInputFiles([
    { name: fileA, mimeType: 'text/plain', buffer: Buffer.from('version-2\n') },
    { name: fileB, mimeType: 'text/plain', buffer: Buffer.from('new-b\n') },
  ])
  const conflictDialog = page.getByRole('dialog', { name: '发现重名文件' })
  await expect(conflictDialog).toBeVisible()
  await expect(conflictDialog).toContainText(fileA)
  await expect(conflictDialog).toContainText('已存在')
  await conflictDialog.getByRole('button', { name: '跳过冲突文件' }).click()
  await expect(page.getByRole('status', { name: '上传任务' })).toContainText('已上传 1 个文件')
  await closeUploadTaskCard(page)
  await expect(page.getByRole('row', { name: new RegExp(fileB) })).toBeVisible()
  expect(await downloadRowContent(page, fileA)).toBe('version-1\n')

  // 2. 重传 A(v3) → 覆盖冲突文件：内容被替换。
  await fileInput.setInputFiles({ name: fileA, mimeType: 'text/plain', buffer: Buffer.from('version-3\n') })
  await expect(conflictDialog).toBeVisible()
  await conflictDialog.getByRole('button', { name: '覆盖冲突文件' }).click()
  await expect(page.getByRole('status', { name: '上传任务' })).toContainText('已上传 1 个文件')
  await closeUploadTaskCard(page)
  expect(await downloadRowContent(page, fileA)).toBe('version-3\n')

  // 3. 传 [A, C]：拦截 C 的上传请求保持挂起（并发 4，C 本会在冲突处理前传完），
  // 冲突对话框出现后取消整个任务：挂起请求被中止，C 不入库、A 不受影响。
  await page.route(/\/upload\?/, async (route) => {
    const body = route.request().postData() ?? ''
    if (body.includes(fileA)) {
      await route.continue()
      return
    }
    await new Promise(() => {})
  })
  await fileInput.setInputFiles([
    { name: fileA, mimeType: 'text/plain', buffer: Buffer.from('version-4\n') },
    { name: fileC, mimeType: 'text/plain', buffer: Buffer.from('cancelled-c\n') },
  ])
  await expect(conflictDialog).toBeVisible()
  await conflictDialog.getByRole('button', { name: '取消整个上传任务' }).click()
  await expect(page.getByRole('status', { name: '上传任务' })).toContainText('已取消')
  await closeUploadTaskCard(page)
  await page.unrouteAll()
  await page.getByRole('button', { name: '刷新' }).click()
  await expect(page.getByRole('row', { name: new RegExp(fileC) })).toHaveCount(0)
  expect(await downloadRowContent(page, fileA)).toBe('version-3\n')

  // 4. 清理：移入回收站并永久删除。
  for (const name of [fileA, fileB]) {
    await page.getByRole('row', { name: new RegExp(name) }).getByRole('button', { name: `更多操作 ${name}` }).click()
    await page.getByRole('menuitem', { name: '删除', exact: true }).click()
    await page.getByRole('dialog', { name: '移入回收站' }).getByRole('button', { name: '移入回收站' }).click()
    await expect(page.getByRole('row', { name: new RegExp(name) })).toHaveCount(0)
  }
  await page.getByRole('button', { name: '回收站' }).click()
  await expect(page.getByRole('heading', { name: '回收站', exact: true })).toBeVisible()
  await expect(page.getByRole('status').filter({ hasText: '移入回收站' })).toHaveCount(0, { timeout: 12_000 })
  for (const name of [fileA, fileB]) {
    const rows = page.getByRole('row')
      .filter({ has: page.getByText(name, { exact: true }) })
      .filter({ has: page.getByRole('button', { name: '永久删除' }) })
    await expect(rows.first()).toBeVisible()
    await rows.first().getByRole('button', { name: '永久删除' }).click()
    await page.getByRole('dialog', { name: '永久删除' }).getByRole('button', { name: '永久删除' }).click()
    await expect(rows).toHaveCount(0)
  }
})

test('api upload enforces relative paths, read-only parents, managed namespace and size cap', async ({ page }) => {
  const sourceKey = await teamSourceKey(page)

  // 1. 目录上传：relative_path 自动创建缺失父目录。
  // 注意：服务端按 part 顺序扫描，relative_path 必须排在 file 之前（与前端 FormData 一致）。
  const relName = `e2e-rel-${Date.now()}.md`
  const relUpload = await page.request.post(`${baseURL}/api/v1/sources/${encodeURIComponent(sourceKey)}/upload?path=/`, {
    ...headersFrom(await demoLogin(page)),
    multipart: {
      relative_path: `docs/nested/${relName}`,
      file: { name: relName, mimeType: 'text/markdown', buffer: Buffer.from('# nested\n') },
    },
  })
  expect(relUpload.status()).toBe(200)
  const listed = await demoAPI(page, 'GET', `/api/v1/sources/${encodeURIComponent(sourceKey)}/files?path=/docs/nested`)
  expect(listed.status).toBe(200)
  expect(((listed.body.data?.items ?? []) as Array<{ name: string }>).map((item) => item.name)).toContain(relName)

  // 2. 只读父目录（种子策略 projects/ 只读）下的目录上传被整链拒绝。
  const readOnly = await page.request.post(`${baseURL}/api/v1/sources/${encodeURIComponent(sourceKey)}/upload?path=/`, {
    ...headersFrom(await demoLogin(page)),
    multipart: {
      relative_path: 'projects/inner/blocked.txt',
      file: { name: 'blocked.txt', mimeType: 'text/plain', buffer: Buffer.from('x') },
    },
  })
  expect(readOnly.status()).toBe(403)

  // 3. 绝对 relative_path 与管理命名空间都被 400 拒绝。
  const absolute = await page.request.post(`${baseURL}/api/v1/sources/${encodeURIComponent(sourceKey)}/upload?path=/`, {
    ...headersFrom(await demoLogin(page)),
    multipart: {
      relative_path: '/etc/escape.txt',
      file: { name: 'abs.txt', mimeType: 'text/plain', buffer: Buffer.from('x') },
    },
  })
  expect(absolute.status()).toBe(400)
  const managed = await page.request.post(`${baseURL}/api/v1/sources/${encodeURIComponent(sourceKey)}/upload?path=${encodeURIComponent('/.omnistore')}`, {
    ...headersFrom(await demoLogin(page)),
    multipart: { file: { name: 'evil.txt', mimeType: 'text/plain', buffer: Buffer.from('x') } },
  })
  // 管理命名空间按安全设计返回不可区分的 404。
  expect(managed.status()).toBe(404)

  // 4. 超过 max_file_size_mb（64MB）→ 413。
  test.setTimeout(90_000)
  const oversized = Buffer.alloc(65 * 1024 * 1024 + 1024, 0x61)
  const tooLarge = await page.request.post(`${baseURL}/api/v1/sources/${encodeURIComponent(sourceKey)}/upload?path=/`, {
    ...headersFrom(await demoLogin(page)),
    multipart: { file: { name: 'too-big.bin', mimeType: 'application/octet-stream', buffer: oversized } },
  })
  expect(tooLarge.status()).toBe(413)

  // 5. 清理目录上传产生的文件与目录链。
  await purgeFile(page, sourceKey, `/docs/nested/${relName}`, relName)
})

function headersFrom(auth: { cookie: string; csrf: string }) {
  return { headers: { cookie: auth.cookie, 'X-CSRF-Token': auth.csrf } }
}
