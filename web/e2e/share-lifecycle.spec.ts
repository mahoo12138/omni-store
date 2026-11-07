import { expect, test, type Page } from '@playwright/test'

// 分享高级语义：下载次数耗尽（410）、有效期校验、解锁限流防爆破、
// 目标进回收站后分享失效、恢复后复活的完整生命周期。
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
  const team = sources.body.data.items.find((item: { name: string }) => item.name === '团队文件')
  expect(team).toBeTruthy()
  return team.key
}

async function uploadViaApi(page: Page, sourceKey: string, name: string, content: string) {
  const auth = await demoLogin(page)
  const response = await page.request.post(
    `${baseURL}/api/v1/sources/${encodeURIComponent(sourceKey)}/upload?path=/`,
    {
      headers: { cookie: auth.cookie, 'X-CSRF-Token': auth.csrf },
      multipart: { file: { name, mimeType: 'text/plain', buffer: Buffer.from(content) } },
    },
  )
  expect(response.status()).toBe(200)
}

async function purgeFile(page: Page, sourceKey: string, name: string) {
  await demoAPI(page, 'DELETE', `/api/v1/sources/${encodeURIComponent(sourceKey)}/files?path=${encodeURIComponent(`/${name}`)}`)
  const trash = await demoAPI(page, 'GET', `/api/v1/sources/${encodeURIComponent(sourceKey)}/trash`)
  const items: Array<{ key: string; name: string }> = trash.body?.data?.items ?? []
  for (const entry of items.filter((item) => item.name === name)) {
    await demoAPI(page, 'DELETE', `/api/v1/sources/${encodeURIComponent(sourceKey)}/trash/${entry.key}`)
  }
}

async function loginUI(page: Page) {
  await page.goto('/login')
  await page.getByLabel('用户名').fill('demo')
  await page.getByLabel('密码').fill('OmniStore-Test-Demo!')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page).toHaveURL(/\/app$/)
  await page.getByRole('button', { name: '打开存储源 团队文件' }).click()
  await expect(page.getByRole('heading', { name: '团队文件' })).toBeVisible()
}

/** 通过 UI 为文件创建分享并返回分享链接。要求已登录且位于团队文件根目录。 */
async function createShareViaUI(page: Page, name: string, maxDownloads: string | null) {
  await page.getByRole('row', { name }).getByRole('button', { name: `更多操作 ${name}` }).click()
  await page.getByRole('menuitem', { name: '创建分享', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: '分享文件' })
  if (maxDownloads !== null) {
    await dialog.getByRole('spinbutton').fill(maxDownloads)
  }
  await dialog.getByRole('button', { name: '创建分享', exact: true }).click()
  const created = page.getByRole('dialog', { name: '分享已创建' })
  await expect(created).toBeVisible()
  const url = await created.getByRole('textbox').inputValue()
  expect(url).toMatch(/\/s\/shr-[a-f0-9]+$/)
  await created.getByRole('button', { name: '完成' }).click()
  await expect(created).toHaveCount(0)
  return url
}

async function revokeShareViaUI(page: Page, name: string, expectedCountText?: string) {
  await page.goto('/app/shares')
  await expect(page).toHaveURL(/\/app\/shares/)
  const card = page.locator('article').filter({ hasText: name }).first()
  if (expectedCountText) {
    await expect(card).toContainText(expectedCountText)
  }
  await card.getByRole('button', { name: `撤销 ${name} 的分享` }).click()
  const dialog = page.getByRole('dialog', { name: '撤销分享' })
  await dialog.getByRole('button', { name: '撤销分享' }).click()
  await expect(page.locator('article').filter({ hasText: name })).toHaveCount(0)
}

test('share with download limit exhausts, shows remaining count and dies after revoke', async ({ page }) => {
  const fileName = `e2e-share-limit-${Date.now()}.txt`
  const sourceKey = await teamSourceKey(page)
  await uploadViaApi(page, sourceKey, fileName, 'download once\n')

  await loginUI(page)
  // 下载次数语义：任何 GET raw（含分享页内嵌预览）都消耗 1 次。
  // 上限 2 = 预览 1 次 + 显式下载 1 次。
  const shareURL = await createShareViaUI(page, fileName, '2')

  // 1. 第一次访问：预览消耗 1 次，显式下载再消耗 1 次。
  await page.goto(shareURL)
  await expect(page.getByRole('heading', { name: fileName })).toBeVisible()
  const downloadPromise = page.waitForEvent('download')
  await page.getByRole('link', { name: '下载文件' }).click()
  expect((await downloadPromise).suggestedFilename()).toBe(fileName)

  // 2. 第二次访问：下载次数已用完（410 语义）。
  await page.goto(shareURL)
  await expect(page.getByRole('heading', { name: '下载次数已用完' })).toBeVisible()

  // 3. 分享管理页显示剩余 0 次下载；撤销后链接死亡。
  await revokeShareViaUI(page, fileName, '剩余 0 次下载')
  await page.goto(shareURL)
  await expect(page.getByRole('heading', { name: '分享不存在或已失效' })).toBeVisible()

  await purgeFile(page, sourceKey, fileName)
})

test('share creation rejects over-limit expiry and unlock rate limit blocks brute force', async ({ page }) => {
  const fileName = `e2e-share-api-${Date.now()}.txt`
  const sourceKey = await teamSourceKey(page)
  await uploadViaApi(page, sourceKey, fileName, 'brute force target\n')

  await loginUI(page)

  // 1. UI 有效期下拉只有合法档位（永久/1/7/30/90 天）。
  await page.getByRole('row', { name: fileName }).getByRole('button', { name: `更多操作 ${fileName}` }).click()
  await page.getByRole('menuitem', { name: '创建分享', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: '分享文件' })
  await dialog.getByRole('combobox', { name: '分享有效期' }).click()
  for (const label of ['永久有效', '1 天', '7 天', '30 天', '90 天']) {
    await expect(page.getByRole('option', { name: label })).toBeVisible()
  }
  await page.keyboard.press('Escape')
  await dialog.getByRole('button', { name: '取消', exact: true }).click()

  // 2. API 创建超过 365 天的分享被 400 拒绝。
  const farFuture = new Date(Date.now() + 400 * 24 * 60 * 60 * 1000).toISOString()
  const tooLong = await demoAPI(page, 'POST', '/api/v1/shares', {
    source_key: sourceKey,
    path: `/${fileName}`,
    expires_at: farFuture,
    max_downloads: 0,
  })
  expect(tooLong.status).toBe(400)

  // 3. 创建带密码分享：连续 10 次错误解锁后，正确密码也被限流拒绝（防爆破）。
  const created = await demoAPI(page, 'POST', '/api/v1/shares', {
    source_key: sourceKey,
    path: `/${fileName}`,
    password: 'unlock-pass-1234',
    max_downloads: 0,
  })
  expect(created.status).toBe(200)
  const shareKey = created.body.data.key
  for (let attempt = 0; attempt < 10; attempt += 1) {
    const wrong = await page.request.post(`${baseURL}/api/v1/public/shares/${shareKey}/unlock`, {
      data: { password: `wrong-${attempt}` },
    })
    expect(wrong.status()).toBe(401)
  }
  const bruteForced = await page.request.post(`${baseURL}/api/v1/public/shares/${shareKey}/unlock`, {
    data: { password: 'unlock-pass-1234' },
  })
  expect(bruteForced.status()).toBe(401)
  const body = (await bruteForced.json()) as { error: { message: string } }
  expect(body.error.message).toContain('访问密码错误')

  // 4. 清理。
  await demoAPI(page, 'DELETE', `/api/v1/shares/${shareKey}`)
  await purgeFile(page, sourceKey, fileName)
})

test('share dies when the target is trashed and revives after restore', async ({ page }) => {
  const fileName = `e2e-share-trash-${Date.now()}.txt`
  const sourceKey = await teamSourceKey(page)
  await uploadViaApi(page, sourceKey, fileName, 'trash linkage\n')

  await loginUI(page)
  const shareURL = await createShareViaUI(page, fileName, null)

  // 1. 分享可用。
  await page.goto(shareURL)
  await expect(page.getByRole('heading', { name: fileName })).toBeVisible()

  // 2. 目标进回收站 → 分享立即失效。
  await page.goto('/app/sources/' + sourceKey)
  await expect(page.getByRole('row', { name: fileName })).toBeVisible()
  await page.getByRole('row', { name: fileName }).getByRole('button', { name: `更多操作 ${fileName}` }).click()
  await page.getByRole('menuitem', { name: '删除', exact: true }).click()
  await page.getByRole('dialog', { name: '移入回收站' }).getByRole('button', { name: '移入回收站' }).click()
  await expect(page.getByRole('row', { name: fileName })).toHaveCount(0)
  await page.goto(shareURL)
  await expect(page.getByRole('heading', { name: '分享不存在或已失效' })).toBeVisible()

  // 3. 从回收站恢复 → 同一链接复活。
  await page.goto('/app/sources/' + sourceKey)
  await page.getByRole('button', { name: '回收站' }).click()
  await expect(page.getByRole('heading', { name: '回收站', exact: true })).toBeVisible()
  await expect(page.getByRole('status').filter({ hasText: '移入回收站' })).toHaveCount(0, { timeout: 12_000 })
  await page.getByRole('row', { name: fileName }).getByRole('button', { name: '恢复' }).click()
  await page.getByRole('dialog', { name: '恢复文件' }).getByRole('button', { name: '恢复', exact: true }).click()
  await expect(page.getByRole('status').filter({ hasText: `已恢复 ${fileName}` })).toBeVisible()

  await page.goto(shareURL)
  await expect(page.getByRole('heading', { name: fileName })).toBeVisible()

  // 4. 清理：撤销分享 + 永久删除。
  await revokeShareViaUI(page, fileName)
  await purgeFile(page, sourceKey, fileName)
})
