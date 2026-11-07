import { expect, test, type Page } from '@playwright/test'

// 回收站高级场景：自定义路径恢复、冲突恢复拒绝、路径校验、
// 跨用户可见性（普通用户只看自己的条目、管理员看全部）与永久清理释放配额。
test.describe.configure({ timeout: 60_000 })

const baseURL = process.env.OMNISTORE_E2E_BASE_URL ?? 'http://127.0.0.1:18080'

let demoCookies: string | null = null
let demoCSRF = ''
let adminCookies: string | null = null
let adminCSRF = ''

async function loginAs(page: Page, username: string, password: string): Promise<{ cookie: string; csrf: string }> {
  const isDemo = username === 'demo'
  const cachedCookie = isDemo ? demoCookies : adminCookies
  if (cachedCookie) return { cookie: cachedCookie, csrf: isDemo ? demoCSRF : adminCSRF }
  const response = await page.request.post(`${baseURL}/api/v1/auth/login`, {
    data: { username, password },
  })
  expect(response.ok()).toBeTruthy()
  const body = await response.json()
  const cookie = response.headersArray().find((header) => header.name.toLowerCase() === 'set-cookie')!.value.split(';')[0]
  if (isDemo) {
    demoCookies = cookie
    demoCSRF = body.data.csrf_token
  } else {
    adminCookies = cookie
    adminCSRF = body.data.csrf_token
  }
  return { cookie, csrf: body.data.csrf_token }
}

async function apiAs(page: Page, who: 'demo' | 'admin', method: 'GET' | 'POST' | 'DELETE', path: string, data?: unknown) {
  const auth = await loginAs(page, who, who === 'demo' ? 'OmniStore-Test-Demo!' : 'OmniStore-Test-Admin!')
  const response = await page.request.fetch(`${baseURL}${path}`, {
    method,
    headers: { cookie: auth.cookie, 'X-CSRF-Token': auth.csrf },
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
  const sources = await apiAs(page, 'demo', 'GET', '/api/v1/sources')
  const team = sources.body.data.items.find((item: { name: string }) => item.name === '团队文件')
  expect(team).toBeTruthy()
  return team.key
}

async function uploadViaApi(page: Page, sourceKey: string, name: string, content: string) {
  const auth = await loginAs(page, 'demo', 'OmniStore-Test-Demo!')
  const response = await page.request.post(
    `${baseURL}/api/v1/sources/${encodeURIComponent(sourceKey)}/upload?path=/`,
    {
      headers: { cookie: auth.cookie, 'X-CSRF-Token': auth.csrf },
      multipart: { file: { name, mimeType: 'text/plain', buffer: Buffer.from(content) } },
    },
  )
  expect(response.status()).toBe(200)
}

/** 删除文件并从回收站永久清理（支持以任意用户身份）。 */
async function purgeFile(page: Page, who: 'demo' | 'admin', sourceKey: string, name: string) {
  await apiAs(page, who, 'DELETE', `/api/v1/sources/${encodeURIComponent(sourceKey)}/files?path=${encodeURIComponent(`/${name}`)}`)
  const trash = await apiAs(page, who, 'GET', `/api/v1/sources/${encodeURIComponent(sourceKey)}/trash`)
  const items: Array<{ key: string; name: string }> = trash.body?.data?.items ?? []
  for (const entry of items.filter((item) => item.name === name)) {
    await apiAs(page, who, 'DELETE', `/api/v1/sources/${encodeURIComponent(sourceKey)}/trash/${entry.key}`)
  }
}

async function loginUI(page: Page, username: string, password: string) {
  await page.goto('/login')
  await page.getByLabel('用户名').fill(username)
  await page.getByLabel('密码').fill(password)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page).toHaveURL(/\/app$/)
  await page.getByRole('button', { name: '打开存储源 团队文件' }).click()
  await expect(page.getByRole('heading', { name: '团队文件' })).toBeVisible()
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

async function trashToUI(page: Page, name: string) {
  await page.getByRole('row', { name: new RegExp(name) }).getByRole('button', { name: `更多操作 ${name}` }).click()
  await page.getByRole('menuitem', { name: '删除', exact: true }).click()
  await page.getByRole('dialog', { name: '移入回收站' }).getByRole('button', { name: '移入回收站' }).click()
  await expect(page.getByRole('row', { name: new RegExp(name) })).toHaveCount(0)
}

test('trash restores to a custom path, rejects occupied targets and validates input', async ({ page }) => {
  const suffix = Date.now()
  const targetDir = `e2e-restore-target-${suffix}`
  const fileName = `e2e-restore-${suffix}.txt`
  const blockerName = `e2e-blocker-${suffix}.txt`
  const sourceKey = await teamSourceKey(page)

  await loginUI(page, 'demo', 'OmniStore-Test-Demo!')

  // 0. 准备：目标目录 + 待恢复文件。
  await page.getByRole('button', { name: '创建文件夹' }).click()
  const createDialog = page.getByRole('dialog', { name: '新建文件夹' })
  await createDialog.getByLabel('目录名').fill(targetDir)
  await createDialog.getByRole('button', { name: '创建', exact: true }).click()
  await expect(page.getByRole('row', { name: new RegExp(targetDir) })).toBeVisible()
  await page.locator('input[type="file"]').setInputFiles({ name: fileName, mimeType: 'text/plain', buffer: Buffer.from('restore me\n') })
  await expect(page.getByRole('status', { name: '上传任务' })).toContainText('已上传 1 个文件')
  await expect(page.getByRole('row', { name: new RegExp(fileName) })).toBeVisible()

  // 1. 删除 → 恢复对话框预填原路径 → 改成目标目录下 → 恢复成功。
  await trashToUI(page, fileName)
  await openTrash(page)
  await trashRows(page, fileName).first().getByRole('button', { name: '恢复' }).click()
  const restoreDialog = page.getByRole('dialog', { name: '恢复文件' })
  await expect(restoreDialog.getByLabel('恢复路径')).toHaveValue(`/${fileName}`)

  // 1a. 路径校验：不以 / 开头直接被前端拒绝。
  await restoreDialog.getByLabel('恢复路径').fill(`${targetDir}/${fileName}`)
  await restoreDialog.getByRole('button', { name: '恢复', exact: true }).click()
  await expect(restoreDialog).toContainText('恢复路径必须以 / 开头')

  await restoreDialog.getByLabel('恢复路径').fill(`/${targetDir}/${fileName}`)
  await restoreDialog.getByRole('button', { name: '恢复', exact: true }).click()
  await expect(page.getByRole('status').filter({ hasText: `已恢复 ${fileName}` })).toBeVisible()
  await expect(restoreDialog).toHaveCount(0)

  // 2. 返回文件管理器，恢复后的文件确实出现在目标目录。
  await page.getByRole('button', { name: '返回 团队文件' }).click()
  await expect(page.getByRole('row', { name: new RegExp(targetDir) })).toBeVisible()
  await page.getByRole('row', { name: new RegExp(targetDir) }).getByRole('button', { name: targetDir, exact: true }).click()
  await expect(page.getByRole('row', { name: new RegExp(fileName) })).toBeVisible()

  // 3. 再次删除 → 原路径放上同名文件 → 恢复到占用路径被 409 拒绝。
  await trashToUI(page, fileName)
  await page.locator('nav[aria-label="面包屑"]').getByText('团队文件', { exact: true }).click()
  await page.locator('input[type="file"]').setInputFiles({ name: blockerName, mimeType: 'text/plain', buffer: Buffer.from('occupy\n') })
  await expect(page.getByRole('status', { name: '上传任务' })).toContainText('已上传 1 个文件')
  await page.getByRole('row', { name: new RegExp(blockerName) }).getByRole('button', { name: `更多操作 ${blockerName}` }).click()
  await page.getByRole('menuitem', { name: '重命名', exact: true }).click()
  const renameDialog = page.getByRole('dialog', { name: '重命名' })
  await renameDialog.getByLabel('新名称').fill(fileName)
  await renameDialog.getByRole('button', { name: '保存', exact: true }).click()
  await expect(page.getByRole('row', { name: new RegExp(fileName) })).toBeVisible()

  await openTrash(page)
  await trashRows(page, fileName).first().getByRole('button', { name: '恢复' }).click()
  const conflictDialog = page.getByRole('dialog', { name: '恢复文件' })
  await conflictDialog.getByLabel('恢复路径').fill(`/${fileName}`)
  await conflictDialog.getByRole('button', { name: '恢复', exact: true }).click()
  await expect(conflictDialog.getByRole('alert')).toContainText('目标已存在')
  await conflictDialog.getByRole('button', { name: '取消', exact: true }).click()

  // 4. 清理：占用文件与全部同名回收站条目、目标目录都由 API 永久清理。
  await purgeFile(page, 'demo', sourceKey, fileName)
  await purgeFile(page, 'demo', sourceKey, targetDir)
  await page.getByRole('button', { name: '返回 团队文件' }).click()
  await openTrash(page)
  await expect(trashRows(page, fileName)).toHaveCount(0)
  await expect(trashRows(page, targetDir)).toHaveCount(0)
})

test('trash entries are user-scoped for regular users and admin sees all; purge releases quota', async ({ page }) => {
  const suffix = Date.now()
  const demoFile = `e2e-trash-demo-${suffix}.txt`
  const adminFile = `e2e-trash-admin-${suffix}.txt`
  const sourceKey = await teamSourceKey(page)

  // 0. demo 与 admin 各产生一条回收站（admin 用 API 上传 + 删除）。
  await uploadViaApi(page, sourceKey, demoFile, 'demo trash\n')
  await apiAs(page, 'demo', 'DELETE', `/api/v1/sources/${encodeURIComponent(sourceKey)}/files?path=${encodeURIComponent(`/${demoFile}`)}`)
  {
    const auth = await loginAs(page, 'admin', 'OmniStore-Test-Admin!')
    const upload = await page.request.post(
      `${baseURL}/api/v1/sources/${encodeURIComponent(sourceKey)}/upload?path=/`,
      {
        headers: { cookie: auth.cookie, 'X-CSRF-Token': auth.csrf },
        multipart: { file: { name: adminFile, mimeType: 'text/plain', buffer: Buffer.from('admin trash\n') } },
      },
    )
    expect(upload.status()).toBe(200)
  }
  await apiAs(page, 'admin', 'DELETE', `/api/v1/sources/${encodeURIComponent(sourceKey)}/files?path=${encodeURIComponent(`/${adminFile}`)}`)

  // 1. demo 只看到自己的条目（admin 的条目不可见）。
  await loginUI(page, 'demo', 'OmniStore-Test-Demo!')
  await openTrash(page)
  await expect(trashRows(page, demoFile).first()).toBeVisible()
  await expect(trashRows(page, adminFile)).toHaveCount(0)

  // 2. admin 看到全部条目（demo 与 admin 的都在）。
  await page.getByRole('button', { name: '账号菜单：' }).click()
  await page.getByRole('menuitem', { name: '退出登录' }).click()
  await expect(page).toHaveURL(/\/login$/)
  await loginUI(page, 'admin', 'OmniStore-Test-Admin!')
  await openTrash(page)
  await expect(trashRows(page, demoFile).first()).toBeVisible()
  await expect(trashRows(page, adminFile).first()).toBeVisible()

  // 3. admin 永久清理 demo 的条目后，demo 的配额台账下降。
  const quotaBefore = await apiAs(page, 'demo', 'GET', '/api/v1/me/quota')
  const demoRows = trashRows(page, demoFile)
  await demoRows.first().getByRole('button', { name: '永久删除' }).click()
  await page.getByRole('dialog', { name: '永久删除' }).getByRole('button', { name: '永久删除' }).click()
  await expect(demoRows).toHaveCount(0)
  await expect.poll(async () => {
    const quota = await apiAs(page, 'demo', 'GET', '/api/v1/me/quota')
    return quota.body.data.usage_bytes
  }, { timeout: 10_000 }).toBeLessThan(quotaBefore.body.data.usage_bytes)

  // 4. 清理 admin 自己的条目。
  const adminRows = trashRows(page, adminFile)
  await adminRows.first().getByRole('button', { name: '永久删除' }).click()
  await page.getByRole('dialog', { name: '永久删除' }).getByRole('button', { name: '永久删除' }).click()
  await expect(adminRows).toHaveCount(0)
})
