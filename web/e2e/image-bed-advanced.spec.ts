import { expect, test, type Page } from '@playwright/test'

// 图床进阶：匿名上传页真实流程与拒绝语义、PicGo Token 全流程、
// 保留策略对缓存头的影响、管理员匿名图管理。
test.describe.configure({ timeout: 120_000 })

const baseURL = process.env.OMNISTORE_E2E_BASE_URL ?? 'http://127.0.0.1:18080'

// 1×1 PNG。
const PNG_1PX = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==',
  'base64',
)

let demoCookies: string | null = null
let demoCSRF = ''
let adminCookies: string | null = null
let adminCSRF = ''

async function loginAPI(page: Page, who: 'demo' | 'admin'): Promise<{ cookie: string; csrf: string }> {
  const cachedCookie = who === 'demo' ? demoCookies : adminCookies
  if (cachedCookie) return { cookie: cachedCookie, csrf: who === 'demo' ? demoCSRF : adminCSRF }
  const response = await page.request.post(`${baseURL}/api/v1/auth/login`, {
    data: { username: who, password: who === 'demo' ? 'OmniStore-Test-Demo!' : 'OmniStore-Test-Admin!' },
  })
  expect(response.ok()).toBeTruthy()
  const body = await response.json()
  const cookie = response.headersArray().find((header) => header.name.toLowerCase() === 'set-cookie')!.value.split(';')[0]
  if (who === 'demo') {
    demoCookies = cookie
    demoCSRF = body.data.csrf_token
  } else {
    adminCookies = cookie
    adminCSRF = body.data.csrf_token
  }
  return { cookie, csrf: body.data.csrf_token }
}

async function apiAs(page: Page, who: 'demo' | 'admin', method: 'GET' | 'POST' | 'PUT' | 'DELETE', path: string, data?: unknown) {
  const { cookie, csrf } = await loginAPI(page, who)
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

async function loginUI(page: Page) {
  await page.goto('/login')
  await page.getByLabel('用户名').fill('demo')
  await page.getByLabel('密码').fill('OmniStore-Test-Demo!')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page).toHaveURL(/\/app$/)
}

async function openProfile(page: Page) {
  await page.getByRole('button', { name: '账号菜单：' }).click()
  await page.getByRole('menuitem', { name: '账户设置' }).click()
  await expect(page).toHaveURL(/\/app\/settings/)
}

/** 凭据入口点击后打开独立模态框（一次只允许一个），返回该模态框。 */
async function openCredentialDialog(page: Page, triggerName: string, dialogName: string) {
  await page.getByRole('button', { name: triggerName }).click()
  const dialog = page.getByRole('dialog', { name: dialogName, exact: true })
  await expect(dialog).toBeVisible()
  return dialog
}

test('anonymous upload page serves links and rejects invalid or oversized images', async ({ page, request }) => {
  await page.goto('/upload')
  await expect(page.getByText(/拖拽图片，或点击选择/)).toBeVisible()

  // 1. 合法 PNG：条目进入上传完成态，直链可访问。
  await page.locator('input[type="file"]').setInputFiles({ name: 'e2e-anon-ok.png', mimeType: 'image/png', buffer: PNG_1PX })
  const doneItem = page.getByText('上传完成', { exact: true }).first()
  await expect(doneItem).toBeVisible({ timeout: 15_000 })
  const directLink = page.locator('a[href*="/i/"]').first()
  await expect(directLink).toBeVisible()
  const imageUrl = await directLink.getAttribute('href')
  expect(imageUrl).toContain('/i/')
  const imagePath = imageUrl.slice(imageUrl.indexOf('/i/'))
  const fetched = await request.fetch(`${baseURL}${imagePath}`)
  expect(fetched.status()).toBe(200)
  expect(fetched.headers()['content-type']).toContain('image/png')

  // 2. 非图片扩展名被客户端拒绝。
  await page.locator('input[type="file"]').setInputFiles({ name: 'e2e-anon-bad.txt', mimeType: 'text/plain', buffer: Buffer.from('nope') })
  await expect(page.getByText('仅支持 JPG、PNG、WebP 或 GIF 图片').first()).toBeVisible()

  // 3. 超过匿名上限（5MB）被客户端拒绝。
  await page.locator('input[type="file"]').setInputFiles({
    name: 'e2e-anon-big.png', mimeType: 'image/png', buffer: Buffer.alloc(6 * 1024 * 1024, 0x61),
  })
  await expect(page.getByText(/图片超过 5 MB 上限/).first()).toBeVisible()

  // 4. 管理员可见匿名图并可删除（删后 404）。
  const list = await apiAs(page, 'admin', 'GET', '/api/v1/admin/image-bed/anonymous-images')
  expect(list.status).toBe(200)
  const items = (list.body?.data?.items ?? []) as Array<{ image_id: string; public_url?: string; relative_path?: string }>
  expect(items.length).toBeGreaterThan(0)
  const imageFile = imagePath.split('/i/')[1]
  const imageId = (items.find((item) => item.public_url?.includes(imageFile) || item.relative_path?.includes(imageFile)) ?? items[0]).image_id
  expect(imageId).toBeTruthy()
  const removed = await apiAs(page, 'admin', 'DELETE', `/api/v1/admin/image-bed/anonymous-images/${imageId}`)
  expect(removed.status).toBe(200)
  const afterDelete = await request.fetch(`${baseURL}${imagePath}`)
  expect(afterDelete.status()).toBe(404)
})

test('PicGo token drives Bearer uploads and dies after revoke', async ({ page, request }) => {
  const tokenLabel = `e2e-picgo-${Date.now()}`

  await loginUI(page)
  await openProfile(page)

  // 1. 打开图床凭据模态框、创建 Token 并读取一次性展示值。
  const tokenDialog = await openCredentialDialog(page, '管理 图床 API', '图床 API')
  await tokenDialog.getByRole('button', { name: '新建 Token' }).click()
  const createDialog = page.getByRole('dialog', { name: '新建图床 Token' })
  await createDialog.getByLabel('Token 名称').fill(tokenLabel)
  await createDialog.getByRole('button', { name: '创建 Token' }).click()
  const revealDialog = page.getByRole('dialog', { name: '新的图床 Token' })
  await expect(revealDialog).toBeVisible()
  const token = await revealDialog.getByLabel('Token').inputValue()
  expect(token.length).toBeGreaterThanOrEqual(16)
  await revealDialog.getByRole('button', { name: '我已保存' }).click()

  // 2. Bearer 上传成功，图床历史出现该图。
  const upload = await request.fetch(`${baseURL}/api/v1/image-bed/upload`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${token}` },
    multipart: { file: { name: 'e2e-picgo.png', mimeType: 'image/png', buffer: PNG_1PX } },
  })
  expect(upload.status()).toBe(200)
  const uploadBody = (await upload.json()) as { success: boolean; data: { url: string } }
  expect(uploadBody.success).toBe(true)
  expect(uploadBody.data.url).toMatch(/\/i\//)

  await page.goto('/app/image-bed')
  await expect(page.getByText(/e2e-picgo\.png/).first()).toBeVisible()

  // 3. 撤销 Token 后同一请求 401（导航会关闭模态框，需重新打开）。
  await openProfile(page)
  const revokeHost = await openCredentialDialog(page, '管理 图床 API', '图床 API')
  await revokeHost.getByRole('button', { name: `撤销 ${tokenLabel}` }).click()
  const revokeDialog = page.getByRole('dialog', { name: '撤销图床 Token' })
  await revokeDialog.getByRole('button', { name: '确认撤销' }).click()
  await expect(revokeDialog).toHaveCount(0)
  await revokeHost.getByRole('button', { name: '关闭弹窗' }).click()
  const afterRevoke = await request.fetch(`${baseURL}/api/v1/image-bed/upload`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${token}` },
    multipart: { file: { name: 'e2e-picgo2.png', mimeType: 'image/png', buffer: PNG_1PX } },
  })
  expect(afterRevoke.status()).toBe(401)
})

test('image retention policy caps cache lifetime and is restorable', async ({ page, request }) => {
  // 0. 快照保留策略。
  const snapshot = await apiAs(page, 'admin', 'GET', '/api/v1/admin/image-bed/retention')
  expect(snapshot.status).toBe(200)
  try {
    // 1. 登录用户保留 1 天 → 上传后原图缓存头按剩余寿命封顶。
    const updated = await apiAs(page, 'admin', 'PUT', '/api/v1/admin/image-bed/retention', {
      user_retention_days: 1,
      anonymous_retention_days: snapshot.body.data.anonymous_retention_days ?? 0,
    })
    expect(updated.status).toBe(200)
    const { cookie, csrf } = await loginAPI(page, 'demo')
    const upload = await page.request.post(`${baseURL}/api/v1/image-bed/uploads`, {
      headers: { cookie, 'X-CSRF-Token': csrf },
      multipart: { file: { name: 'e2e-retention.png', mimeType: 'image/png', buffer: PNG_1PX } },
    })
    expect(upload.status()).toBe(200)
    const uploadBody = (await upload.json()) as { data: { public_url?: string; url?: string } }
    const publicUrl = uploadBody.data.public_url ?? uploadBody.data.url
    expect(publicUrl).toBeTruthy()
    const imageFile = (publicUrl as string).split('/i/')[1]
    expect(imageFile).toBeTruthy()
    const served = await request.fetch(`${baseURL}/i/${imageFile}`)
    expect(served.status()).toBe(200)
    const cacheControl = served.headers()['cache-control'] ?? ''
    const maxAge = Number(/max-age=(\d+)/.exec(cacheControl)?.[1] ?? Number.POSITIVE_INFINITY)
    expect(maxAge).toBeLessThanOrEqual(3600)

    // 2. 恢复 0（永不过期）后，新上传的图走长期缓存；
    // 已盖章的图保留寿命上限（expires_at 在上传时落库，不受后续策略影响）。
    await apiAs(page, 'admin', 'PUT', '/api/v1/admin/image-bed/retention', {
      user_retention_days: 0,
      anonymous_retention_days: snapshot.body.data.anonymous_retention_days ?? 0,
    })
    const servedAgain = await request.fetch(`${baseURL}/i/${imageFile}`)
    expect(servedAgain.headers()['cache-control']).toContain('max-age=3600')
    const upload2 = await page.request.post(`${baseURL}/api/v1/image-bed/uploads`, {
      headers: { cookie, 'X-CSRF-Token': csrf },
      multipart: { file: { name: 'e2e-retention-2.png', mimeType: 'image/png', buffer: PNG_1PX } },
    })
    expect(upload2.status()).toBe(200)
    const upload2Body = (await upload2.json()) as { data: { public_url?: string; url?: string } }
    const imageFile2 = ((upload2Body.data.public_url ?? upload2Body.data.url) as string).split('/i/')[1]
    const servedFresh = await request.fetch(`${baseURL}/i/${imageFile2}`)
    expect(servedFresh.headers()['cache-control']).toContain('max-age=31536000')

    // 3. 清理两张图。
    const history = await apiAs(page, 'demo', 'GET', '/api/v1/image-bed/images')
    for (const item of (history.body?.data?.items ?? []) as Array<{ image_id: string; public_url: string }>) {
      if (item.public_url?.endsWith(imageFile) || item.public_url?.endsWith(imageFile2)) {
        await apiAs(page, 'demo', 'DELETE', `/api/v1/image-bed/images/${item.image_id}`)
      }
    }
  } finally {
    await apiAs(page, 'admin', 'PUT', '/api/v1/admin/image-bed/retention', snapshot.body.data)
  }
})
