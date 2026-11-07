import { expect, test, type Page } from '@playwright/test'

// 用户全生命周期：创建 → 首登空态 → 授权 → 配额（UI 调整 + API 字节级）→
// 禁用（会话即时失效、登录通用报错）→ 启用 → 回收站非空阻止删除 → 清理后可删。
test.describe.configure({ timeout: 150_000 })

const baseURL = process.env.OMNISTORE_E2E_BASE_URL ?? 'http://127.0.0.1:18080'
const SEEDED_POLICY_NAME = '演示用户基础访问'
const NEW_USER_PASSWORD = 'E2E-User-Pass!2026'

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

async function adminAPI(page: Page, method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE', path: string, data?: unknown) {
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

async function loginUI(page: Page, username: string, password: string) {
  await page.goto('/login')
  await page.getByLabel('用户名').fill(username)
  await page.getByLabel('密码').fill(password)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page).toHaveURL(/\/app$/)
}

async function adminUsersSection(page: Page) {
  await loginUI(page, 'admin', 'OmniStore-Test-Admin!')
  await page.goto('/app/admin?section=users')
  await expect(page.getByRole('heading', { name: '用户', exact: true })).toBeVisible()
}

test('admin manages a user through create, quota, disable, enable and delete', async ({ page, request }) => {
  test.setTimeout(180_000)
  const username = `e2e-user-${Date.now()}`
  const displayName = 'E2E 生命周期用户'
  const fileName = `e2e-um-${Date.now()}.txt`

  // 0. 快照种子策略，finally 原样恢复。
  const seeded = await adminAPI(page, 'GET', '/api/v1/admin/policies')
  expect(seeded.status).toBe(200)
  const seededPolicy = seeded.body.data.items.find((item: { name: string }) => item.name === SEEDED_POLICY_NAME)
  expect(seededPolicy).toBeTruthy()

  try {
    // 1. 管理员创建用户。
    await adminUsersSection(page)
    await page.getByRole('button', { name: '创建用户' }).click()
    const createDialog = page.getByRole('dialog', { name: '创建用户' })
    await createDialog.getByLabel('用户名').fill(username)
    await createDialog.getByLabel('显示名').fill(displayName)
    // FormField 的 label 文本带必填星号（如「密码 *」），getByLabel 需用子串匹配。
    await createDialog.getByLabel('密码').fill(NEW_USER_PASSWORD)
    await createDialog.getByRole('button', { name: '创建', exact: true }).click()
    await expect(createDialog).toHaveCount(0)
    const userRow = page.getByRole('row', { name: new RegExp(username) })
    await expect(userRow).toBeVisible()

    // 2. 新用户首次登录：无任何存储源的引导空态。
    await loginUI(page, username, NEW_USER_PASSWORD)
    await expect(page.getByText('还没有可用的存储源')).toBeVisible()

    // 3. 通过种子策略授权（API 绑定新用户）。
    const users = await adminAPI(page, 'GET', '/api/v1/admin/users')
    const created = users.body.data.items.find((item: { username: string }) => item.username === username)
    expect(created).toBeTruthy()
    const bind = await adminAPI(page, 'PUT', `/api/v1/admin/policies/${encodeURIComponent(seededPolicy.key)}`, {
      name: seededPolicy.name,
      description: seededPolicy.description,
      sources: seededPolicy.sources.map((source: { source_key: string; permission: string; path_rules: Array<{ path_prefix: string; permission: string }> }) => ({
        source_key: source.source_key,
        permission: source.permission,
        path_rules: source.path_rules.map((rule) => ({ path_prefix: rule.path_prefix, permission: rule.permission })),
      })),
      user_ids: [...seededPolicy.users.map((user: { user_id: number }) => user.user_id), created.id],
    })
    expect(bind.status).toBe(200)

    await page.reload()
    await page.getByRole('button', { name: '打开存储源 团队文件' }).click()
    await expect(page.getByRole('heading', { name: '团队文件' })).toBeVisible()

    // 4. 上传并移入回收站，制造“删除用户被回收站阻止”的前提。
    await page.locator('input[type="file"]').setInputFiles({ name: fileName, mimeType: 'text/plain', buffer: Buffer.from('user lifecycle\n') })
    await expect(page.getByRole('status', { name: '上传任务' })).toContainText('已上传 1 个文件')
    await page.getByRole('row', { name: new RegExp(fileName) }).getByRole('button', { name: `更多操作 ${fileName}` }).click()
    await page.getByRole('menuitem', { name: '删除', exact: true }).click()
    await page.getByRole('dialog', { name: '移入回收站' }).getByRole('button', { name: '移入回收站' }).click()
    await expect(page.getByRole('row', { name: new RegExp(fileName) })).toHaveCount(0)

    // 5. API 字节级配额：设为略高于当前用量 → 上传 507；随后恢复不限额。
    const usersNow = await adminAPI(page, 'GET', '/api/v1/admin/users')
    const usageNow = usersNow.body.data.items.find((item: { username: string }) => item.username === username).usage_bytes
    const tightened = await adminAPI(page, 'PATCH', `/api/v1/admin/users/${created.id}/quota`, { quota_bytes: usageNow + 200 })
    expect(tightened.status).toBe(200)
    await page.locator('input[type="file"]').setInputFiles({ name: `e2e-um-quota-${Date.now()}.txt`, mimeType: 'text/plain', buffer: Buffer.alloc(4096, 0x78) })
    const quotaCard = page.getByRole('status', { name: '上传任务' })
    await expect(quotaCard).toContainText('部分文件失败')
    await expect(quotaCard).toContainText(/配额|不足/)
    await quotaCard.getByRole('button', { name: '关闭' }).click({ force: true })
    const relaxed = await adminAPI(page, 'PATCH', `/api/v1/admin/users/${created.id}/quota`, { quota_bytes: 0 })
    expect(relaxed.status).toBe(200)

    // 6. UI 调整配额入口可用（硬配额 GiB 语义）。
    await adminUsersSection(page)
    await userRow.getByRole('button', { name: '调整配额' }).click()
    const quotaDialog = page.getByRole('dialog', { name: `用户配额：${displayName}` })
    await expect(quotaDialog.getByRole('spinbutton')).toHaveValue(/0/)
    await quotaDialog.getByRole('button', { name: '取消' }).click()

    // 7. 禁用用户：该用户已有会话即时失效，登录返回统一错误。
    // 用隔离的 request fixture（不与浏览器共享 cookie），避免覆盖浏览器的 admin 会话。
    const userLogin = await request.post(`${baseURL}/api/v1/auth/login`, {
      data: { username, password: NEW_USER_PASSWORD },
    })
    expect(userLogin.ok()).toBeTruthy()
    const userCookie = userLogin.headersArray().find((header) => header.name.toLowerCase() === 'set-cookie')!.value.split(';')[0]
    const meBefore = await request.fetch(`${baseURL}/api/v1/auth/me`, { headers: { cookie: userCookie } })
    expect(meBefore.status()).toBe(200)

    await userRow.getByRole('button', { name: `用户 ${username} 的更多操作` }).click()
    await page.getByRole('menuitem', { name: '禁用用户' }).click()
    await expect(userRow).toContainText('已禁用')

    const meAfter = await request.fetch(`${baseURL}/api/v1/auth/me`, { headers: { cookie: userCookie } })
    expect(meAfter.status()).toBe(401)

    // 禁用状态下登录被拒（统一文案，不泄露禁用状态）。
    await page.goto('/login')
    await page.getByLabel('用户名').fill(username)
    await page.getByLabel('密码').fill(NEW_USER_PASSWORD)
    await page.getByRole('button', { name: '登录', exact: true }).click()
    await expect(page.getByRole('alert')).toContainText('用户名或密码错误')

    // 8. 启用后可正常登录。
    await adminUsersSection(page)
    await userRow.getByRole('button', { name: `用户 ${username} 的更多操作` }).click()
    await page.getByRole('menuitem', { name: '启用用户' }).click()
    await expect(userRow).not.toContainText('已禁用')
    await loginUI(page, username, NEW_USER_PASSWORD)

    // 9. 回收站非空阻止删除（原生 alert），清理后删除成功。
    await adminUsersSection(page)
    let alertMessage = ''
    page.once('dialog', (dialog) => {
      alertMessage = dialog.message()
      void dialog.dismiss()
    })
    await userRow.getByRole('button', { name: `用户 ${username} 的更多操作` }).click()
    await page.getByRole('menuitem', { name: '删除用户' }).click()
    const deleteDialog = page.getByRole('dialog', { name: '删除用户' })
    await deleteDialog.getByRole('button', { name: '确认删除' }).click()
    await expect.poll(() => alertMessage, { timeout: 10_000 }).toContain('回收站')
    // 失败后对话框保持打开（模态），先取消再清理回收站。
    await deleteDialog.getByRole('button', { name: '取消' }).click()
    await expect(deleteDialog).toHaveCount(0)

    // admin 清理该用户的回收站条目后，删除成功。
    const teamSource = seededPolicy.sources.find((source: { source_name: string }) => source.source_name === '团队文件')
    expect(teamSource).toBeTruthy()
    const trash = await adminAPI(page, 'GET', `/api/v1/sources/${encodeURIComponent(teamSource.source_key)}/trash`)
    const items: Array<{ key: string; name: string }> = trash.body?.data?.items ?? []
    for (const entry of items.filter((item) => item.name === fileName)) {
      await adminAPI(page, 'DELETE', `/api/v1/sources/${encodeURIComponent(teamSource.source_key)}/trash/${entry.key}`)
    }
    await userRow.getByRole('button', { name: `用户 ${username} 的更多操作` }).click()
    await page.getByRole('menuitem', { name: '删除用户' }).click()
    await page.getByRole('dialog', { name: '删除用户' }).getByRole('button', { name: '确认删除' }).click()
    await expect(userRow).toHaveCount(0)
  } finally {
    // 原样恢复种子策略；兜底删除测试用户与其残留。
    const list = await adminAPI(page, 'GET', '/api/v1/admin/policies')
    const stillSeeded = (list.body?.data?.items ?? []).find((item: { key: string }) => item.key === seededPolicy.key)
    const payload = {
      name: seededPolicy.name,
      description: seededPolicy.description,
      sources: seededPolicy.sources.map((source: { source_key: string; permission: string; path_rules: Array<{ path_prefix: string; permission: string }> }) => ({
        source_key: source.source_key,
        permission: source.permission,
        path_rules: source.path_rules.map((rule) => ({ path_prefix: rule.path_prefix, permission: rule.permission })),
      })),
      user_ids: seededPolicy.users.map((user: { user_id: number }) => user.user_id),
    }
    if (stillSeeded) {
      await adminAPI(page, 'PUT', `/api/v1/admin/policies/${encodeURIComponent(seededPolicy.key)}`, payload)
    } else {
      await adminAPI(page, 'POST', '/api/v1/admin/policies', payload)
    }
    const users = await adminAPI(page, 'GET', '/api/v1/admin/users')
    const leftover = (users.body?.data?.items ?? []).find((item: { username: string }) => item.username === username)
    if (leftover) {
      await adminAPI(page, 'DELETE', `/api/v1/admin/users/${leftover.id}`)
    }
  }
})
