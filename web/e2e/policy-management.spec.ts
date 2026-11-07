import { expect, test, type Page } from '@playwright/test'

// 访问策略 CRUD：新建带子路径覆盖的策略并绑定 demo → 子目录只读生效；
// 编辑为读写后实时生效；删除自建策略；删除种子策略后 demo 失去访问权（finally 原样恢复）。
test.describe.configure({ timeout: 60_000 })

const baseURL = process.env.OMNISTORE_E2E_BASE_URL ?? 'http://127.0.0.1:18080'
const SEEDED_POLICY_NAME = '演示用户基础访问'

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

async function adminAPI(page: Page, method: 'GET' | 'POST' | 'PUT' | 'DELETE', path: string, data?: unknown) {
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

test('policy CRUD takes effect on user capabilities in real time', async ({ page }) => {
  // 全流程含 4 次用户切换登录与多段管理操作，整体耗时高于 60s 档位。
  test.setTimeout(150_000)
  const policyName = `E2E策略-${Date.now()}`

  // 0. 快照种子策略，任何一步失败都在 finally 里原样恢复。
  const seeded = await adminAPI(page, 'GET', '/api/v1/admin/policies')
  expect(seeded.status).toBe(200)
  const seededPolicy = seeded.body.data.items.find((item: { name: string }) => item.name === SEEDED_POLICY_NAME)
  expect(seededPolicy).toBeTruthy()
  try {
    await loginUI(page, 'admin', 'OmniStore-Test-Admin!')
    await page.goto('/app/admin?section=policies')

    // 1. 新建策略：团队文件默认读写 + private 子路径只读，绑定演示用户。
    await page.getByRole('button', { name: '新建策略' }).click()
    const dialog = page.getByRole('dialog', { name: '新建访问策略' })
    await dialog.getByLabel('策略名称').fill(policyName)
    // Fluent Checkbox 的可访问名会粘连 FormField 分组文本，且多个复选框共享同一控件 id，
    // 按名称/label 匹配会勾错；label 文本位于不含 input 的 __label 元素里。
    // 用 Fluent 根节点 .fui-Checkbox + 源名过滤定位到正确的 input。
    const sourceCheckbox = (name: string) =>
      dialog.locator('.fui-Checkbox').filter({ hasText: name }).locator('input[type="checkbox"]')
    await sourceCheckbox('团队文件').check()
    await expect(sourceCheckbox('团队文件')).toBeChecked()
    await dialog.getByRole('combobox', { name: '团队文件默认权限' }).click()
    await dialog.getByRole('combobox', { name: '团队文件默认权限' }).click()
    await page.screenshot({ path: '/tmp/policy-debug-1.png', fullPage: false })
    // Fluent Dropdown 的弹层挂在 portal；键盘高亮 + Enter 比跨 portal 点击稳定。
    await page.keyboard.press('ArrowDown')
    await page.keyboard.press('ArrowDown')
    await page.keyboard.press('Enter')
    await page.screenshot({ path: '/tmp/policy-debug-2.png', fullPage: false })
    await expect(dialog.getByRole('combobox', { name: '团队文件默认权限' })).toContainText('默认读写')
    await dialog.getByRole('button', { name: '添加子路径覆盖' }).click()
    await dialog.getByRole('textbox', { name: '团队文件子路径' }).fill('private')
    const ruleSelect = dialog.getByRole('combobox', { name: 'private子路径权限' })
    await ruleSelect.click()
    await page.keyboard.press('ArrowUp')
    await page.keyboard.press('Enter')
    await expect(ruleSelect).toContainText('只读')
    const demoUserCheckbox = dialog.locator('.fui-Checkbox').filter({ hasText: '演示用户' }).locator('input[type="checkbox"]')
    await demoUserCheckbox.check()
    await expect(demoUserCheckbox).toBeChecked()
    await dialog.getByRole('button', { name: '保存策略' }).click()
    await expect(dialog).toHaveCount(0)
    await expect(page.getByRole('row', { name: new RegExp(policyName) })).toBeVisible()

    // 2. demo 视角：根目录可写，private 目录只读（写按钮消失）。
    await loginUI(page, 'demo', 'OmniStore-Test-Demo!')
    await page.getByRole('button', { name: '打开存储源 团队文件' }).click()
    await page.getByRole('button', { name: '创建文件夹' }).click()
    const createDialog = page.getByRole('dialog', { name: '新建文件夹' })
    await createDialog.getByLabel('目录名').fill('private')
    await createDialog.getByRole('button', { name: '创建', exact: true }).click()
    await expect(page.getByRole('row', { name: /private/ })).toBeVisible()
    await page.getByRole('row', { name: /private/ }).getByRole('button', { name: 'private', exact: true }).click()
    await expect(page).toHaveURL(/path=%2Fprivate/)
    await expect(page.getByRole('button', { name: '创建文件夹' })).toHaveCount(0)
    await page.locator('nav[aria-label="面包屑"]').getByText('团队文件', { exact: true }).click()
    await expect(page.getByRole('button', { name: '创建文件夹' })).toBeVisible()

    // 3. admin 把子路径规则改为读写 → demo 刷新后 private 可写。
    await loginUI(page, 'admin', 'OmniStore-Test-Admin!')
    await page.goto('/app/admin?section=policies')
    await page.getByRole('row', { name: new RegExp(policyName) }).getByRole('button', { name: '编辑策略' }).click()
    const editDialog = page.getByRole('dialog', { name: '编辑访问策略' })
    const editRuleSelect = editDialog.getByRole('combobox', { name: 'private子路径权限' })
    await editRuleSelect.click()
    await page.keyboard.press('ArrowDown')
    await page.keyboard.press('Enter')
    await expect(editRuleSelect).toContainText('读写')
    await editDialog.getByRole('button', { name: '保存策略' }).click()
    await expect(editDialog).toHaveCount(0)

    await loginUI(page, 'demo', 'OmniStore-Test-Demo!')
    await page.goto('/app/sources/' + (await demoTeamKey(page)) + '?path=%2Fprivate')
    await expect(page.getByRole('button', { name: '创建文件夹' }).first()).toBeVisible()

    // 4. admin 删除自建策略。
    await loginUI(page, 'admin', 'OmniStore-Test-Admin!')
    await page.goto('/app/admin?section=policies')
    await page.getByRole('row', { name: new RegExp(policyName) }).getByRole('button', { name: '删除策略' }).click()
    const deleteDialog = page.getByRole('dialog', { name: /删除策略|删除访问策略/ })
    await deleteDialog.getByRole('button', { name: '确认删除' }).click()
    await expect(page.getByRole('row', { name: new RegExp(policyName) })).toHaveCount(0)

    // 5. 删除种子策略 → demo 失去团队文件访问权（finally 恢复）。
    const seededKey = seededPolicy.key
    const removed = await adminAPI(page, 'DELETE', `/api/v1/admin/policies/${encodeURIComponent(seededKey)}`)
    expect(removed.status).toBe(200)
    await page.goto('/login')
    await page.getByLabel('用户名').fill('demo')
    await page.getByLabel('密码').fill('OmniStore-Test-Demo!')
    await page.getByRole('button', { name: '登录', exact: true }).click()
    await expect(page).toHaveURL(/\/app$/)
    await expect(page.getByRole('button', { name: '打开存储源 团队文件' })).toHaveCount(0)
  } finally {
    // 原样恢复种子策略（path-permissions 等既有用例依赖它）。
    // 已删除的策略对 PUT 返回 404，缺失时走 POST 重建。
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
    const probe = await adminAPI(page, 'GET', `/api/v1/admin/policies/${encodeURIComponent(seededPolicy.key)}`)
    const restore = probe.status === 200
      ? await adminAPI(page, 'PUT', `/api/v1/admin/policies/${encodeURIComponent(seededPolicy.key)}`, payload)
      : await adminAPI(page, 'POST', '/api/v1/admin/policies', payload)
    expect(restore.status).toBe(200)
    // 兜底清理自建策略。
    const list = await adminAPI(page, 'GET', '/api/v1/admin/policies')
    for (const item of (list.body?.data?.items ?? []) as Array<{ key: string; name: string }>) {
      if (item.name.startsWith('E2E策略-')) {
        await adminAPI(page, 'DELETE', `/api/v1/admin/policies/${encodeURIComponent(item.key)}`)
      }
    }
  }
})

async function demoTeamKey(page: Page): Promise<string> {
  const auth = await (async () => {
    const response = await page.request.post(`${baseURL}/api/v1/auth/login`, {
      data: { username: 'demo', password: 'OmniStore-Test-Demo!' },
    })
    const body = await response.json()
    const cookie = response.headersArray().find((header) => header.name.toLowerCase() === 'set-cookie')!.value.split(';')[0]
    return { cookie, csrf: body.data.csrf_token }
  })()
  const sources = await page.request.fetch(`${baseURL}/api/v1/sources`, { headers: { cookie: auth.cookie } })
  const body = await sources.json()
  const team = body.data.items.find((item: { name: string }) => item.name === '团队文件')
  expect(team).toBeTruthy()
  return team.key
}
