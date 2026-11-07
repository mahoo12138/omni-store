import { expect, test } from '@playwright/test'

// 初始化防护：种子环境必然已初始化——/setup 入口必须关闭，
// 重复引导被拒绝且不在用户表留下任何痕迹。
test('initialized instance closes setup entry and rejects re-bootstrap', async ({ page }) => {
  // 1. 已初始化实例访问 /setup：只显示关闭提示与登录入口，不再出现表单。
  await page.goto('/setup')
  await expect(page.getByText('系统已初始化，初始化入口已关闭。')).toBeVisible()
  await expect(page.getByRole('button', { name: /创建/ })).toHaveCount(0)

  // 2. 引导状态公开可查：必须报告已初始化。
  const status = await page.request.get('/api/v1/setup/status')
  expect(status.status()).toBe(200)
  expect(((await status.json()) as { data: { initialized: boolean } }).data.initialized).toBe(true)

  // 3. 直连引导接口：凭据无效时先行拒绝（403），不得创建账号。
  const badToken = await page.request.post('/api/v1/setup/admin', {
    data: {
      username: 'e2e-bootstrap',
      display_name: '引导测试',
      password: 'Bootstrap-E2E-Pass!',
      bootstrap_token: 'e2e-invalid-bootstrap-token',
    },
  })
  expect(badToken.status()).toBe(403)

  // 4. 管理员视角核对：用户表未新增引导账号。
  const login = await page.request.post('/api/v1/auth/login', {
    data: { username: 'admin', password: 'OmniStore-Test-Admin!' },
  })
  expect(login.ok()).toBeTruthy()
  const loginBody = (await login.json()) as { data: { csrf_token: string } }
  const cookie = login.headersArray().find((header) => header.name.toLowerCase() === 'set-cookie')!.value.split(';')[0]
  const users = await page.request.fetch('/api/v1/admin/users', {
    headers: { cookie, 'X-CSRF-Token': loginBody.data.csrf_token },
  })
  expect(users.status()).toBe(200)
  const usernames = ((await users.json()) as { data: { items: Array<{ username: string }> } })
    .data.items.map((item) => item.username)
  expect(usernames).not.toContain('e2e-bootstrap')
})
