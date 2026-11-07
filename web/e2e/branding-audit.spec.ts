import { existsSync, readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { expect, test, type Page } from '@playwright/test'

// 品牌信息落在 system_settings 并实时反映到公开侧外壳；
// 审计日志支持主体/入口/结果/关键字组合筛选，非法分页参数被 400 拒绝。
const baseURL = process.env.OMNISTORE_E2E_BASE_URL ?? 'http://127.0.0.1:18080'

interface ProtocolCredentials {
  http_endpoint: string
  username: string
  webdav_token: string
  team_bucket: string
}

function loadProtocolCredentials(): ProtocolCredentials | null {
  const defaultPath = resolve(dirname(fileURLToPath(import.meta.url)), '../../.testdata/protocol-credentials.json')
  const path = process.env.OMNISTORE_E2E_PROTOCOL_CREDENTIALS ?? defaultPath
  if (!existsSync(path)) return null
  return JSON.parse(readFileSync(path, 'utf8')) as ProtocolCredentials
}

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

async function adminAPI(page: Page, method: 'GET' | 'PUT' | 'POST', path: string, data?: unknown) {
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

test('instance branding updates the public shell and restores the default name', async ({ page }) => {
  const brandName = `E2E 品牌 ${Date.now()}`

  await page.goto('/login')
  await page.getByLabel('用户名').fill('admin')
  await page.getByLabel('密码').fill('OmniStore-Test-Admin!')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page).toHaveURL(/\/app$/)
  await page.goto('/app/admin?section=site&tab=general')

  // 品牌卡片当前值行：kvLabel「实例名称」的父级即 kvRow（含当前名称）。
  const brandingValue = page.getByText('实例名称', { exact: true }).locator('..')
  await expect(brandingValue).toContainText('OmniStore')

  // 1. 修改实例名称并保存；表单关闭回到 kv 展示后断言新名称。
  await page.getByRole('button', { name: '修改名称' }).click()
  await page.getByRole('textbox', { name: /实例名称/ }).fill(brandName)
  await page.getByRole('button', { name: '保存', exact: true }).click()
  await expect(page.getByRole('textbox', { name: /实例名称/ })).toHaveCount(0)
  await expect(brandingValue).toContainText(brandName)

  // 2. 公开侧外壳立即展示新名称（头部品牌与页脚）。
  await page.goto('/')
  await expect(page.getByRole('link', { name: `返回 ${brandName} 公开网盘` })).toBeVisible()

  // 3. 恢复默认名称 OmniStore，公开侧同步回退。
  // （注：输入框带原生 required，"清空保存" 会被浏览器拦截，无法从 UI 走通。）
  await page.goto('/app/admin?section=site&tab=general')
  await page.getByRole('button', { name: '修改名称' }).click()
  await page.getByRole('textbox', { name: /实例名称/ }).fill('OmniStore')
  await page.getByRole('button', { name: '保存', exact: true }).click()
  await expect(page.getByRole('textbox', { name: /实例名称/ })).toHaveCount(0)
  await expect(brandingValue).toContainText('OmniStore')
  await page.goto('/')
  await expect(page.getByRole('link', { name: '返回 OmniStore 公开网盘' })).toBeVisible()
})

test('audit log filters by entry type, keyword and status; invalid pagination is rejected', async ({ page }) => {
  const credentials = loadProtocolCredentials()
  test.skip(!credentials, '协议凭据文件不存在，跳过 WebDAV 审计来源')

  // 1. 以 demo 通过 WebDAV 上传小文件产生审计（PUT 才被审计；GET/PROPFIND 不记）。
  const basicAuth = `Basic ${Buffer.from(`${credentials!.username}:${credentials!.webdav_token}`).toString('base64')}`
  const davFile = `e2e-audit-dav-${Date.now()}.txt`
  const davURL = `${credentials!.http_endpoint}/dav/${encodeURIComponent(credentials!.team_bucket)}/${davFile}`
  const davPut = await page.request.put(davURL, {
    headers: { Authorization: basicAuth },
    data: 'audit via webdav\n',
  })
  expect(davPut.status()).toBe(201)

  // 测后清理：WebDAV DELETE 进回收站，再用 API 永久清理。
  try {
    await page.request.delete(davURL, { headers: { Authorization: basicAuth } })
    const demoLogin = await page.request.post(`${baseURL}/api/v1/auth/login`, {
      data: { username: 'demo', password: 'OmniStore-Test-Demo!' },
    })
    const demoBody = (await demoLogin.json()) as { data: { csrf_token: string } }
    const demoCookie = demoLogin.headersArray().find((header) => header.name.toLowerCase() === 'set-cookie')!.value.split(';')[0]
    const headers = { cookie: demoCookie, 'X-CSRF-Token': demoBody.data.csrf_token }
    const trash = await page.request.fetch(
      `${baseURL}/api/v1/sources/${encodeURIComponent(credentials!.team_bucket)}/trash`, { headers },
    )
    const items = ((await trash.json()) as { data?: { items?: Array<{ key: string; name: string }> | null } }).data?.items ?? []
    for (const entry of items.filter((item) => item.name === davFile)) {
      await page.request.fetch(
        `${baseURL}/api/v1/sources/${encodeURIComponent(credentials!.team_bucket)}/trash/${entry.key}`,
        { method: 'DELETE', headers },
      )
    }
  } finally {
    // 清理失败不影响断言展示。
  }

  await page.goto('/login')
  await page.getByLabel('用户名').fill('admin')
  await page.getByLabel('密码').fill('OmniStore-Test-Admin!')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page).toHaveURL(/\/app$/)
  await page.goto('/app/admin?section=audit')
  await expect(page.getByText(/共 \d+ 条/).first()).toBeVisible()

  // 2. 展开更多筛选 → 入口=WebDAV + 关键字命中路径 → 查询命中该条上传审计。
  await page.getByRole('button', { name: '展开更多筛选' }).click()
  await page.getByRole('combobox', { name: '筛选审计入口' }).click()
  await page.getByRole('option', { name: 'WebDAV' }).click()
  await page.getByLabel('关键字').fill('e2e-audit-dav')
  await page.getByRole('button', { name: '查询', exact: true }).click()
  const matchedRow = page.getByRole('row', { name: /e2e-audit-dav/ }).filter({ hasText: 'WebDAV' })
  await expect(matchedRow.first()).toBeVisible()
  await expect(matchedRow.first()).toContainText('成功')
  await expect(matchedRow.first()).toContainText('团队文件')

  // 3. 结果=成功组合筛选依然命中；改为失败则清空。
  await page.getByRole('combobox', { name: '筛选审计结果' }).click()
  await page.getByRole('option', { name: '成功', exact: true }).click()
  await page.getByRole('button', { name: '查询', exact: true }).click()
  await expect(matchedRow.first()).toBeVisible()
  await page.getByRole('combobox', { name: '筛选审计结果' }).click()
  await page.getByRole('option', { name: '失败', exact: true }).click()
  await page.getByRole('button', { name: '查询', exact: true }).click()
  await expect(page.getByText('没有符合条件的日志')).toBeVisible()

  // 4. API 非法分页参数直接 400。
  const invalid = await adminAPI(page, 'GET', '/api/v1/admin/audit-logs?page_size=500')
  expect(invalid.status).toBe(400)
})
