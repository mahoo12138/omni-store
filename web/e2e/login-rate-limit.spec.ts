import { expect, test } from '@playwright/test'

// 登录限流：config.test.yaml 开启（15 分钟窗口，每 IP 50 次、每用户名 10 次）。
// 每次运行使用独立的幽灵用户名 + 独立 X-Forwarded-For（trusted_proxies 含 127.0.0.1，
// 服务端信任该头），因此不污染共享种子的 IP 桶，也不影响其他用例的正常登录。
const baseURL = process.env.OMNISTORE_E2E_BASE_URL ?? 'http://127.0.0.1:18080'

test('repeated failed logins hit the per-username limit while normal login keeps working', async ({ page }) => {
  const ghostUser = `e2e-lock-ghost-${Date.now()}`
  const ghostIP = `10.207.36.${(Date.now() % 200) + 10}`
  const attempt = (password: string) => page.request.post(`${baseURL}/api/v1/auth/login`, {
    headers: { 'X-Forwarded-For': ghostIP },
    data: { username: ghostUser, password },
  })

  // 1. 前 10 次错误密码：统一返回 401「用户名或密码错误」（不枚举用户名）。
  for (let index = 0; index < 10; index += 1) {
    const response = await attempt(`wrong-pass-${index}`)
    expect(response.status()).toBe(401)
    const body = (await response.json()) as { error: { code: string; message: string } }
    expect(body.error.message).toBe('用户名或密码错误')
  }

  // 2. 第 11 次：触发每用户名限流 → 429 + Retry-After，文案不再泛化为密码错误。
  const locked = await attempt('wrong-pass-11')
  expect(locked.status()).toBe(429)
  expect(Number(locked.headers()['retry-after'])).toBeGreaterThan(0)
  const lockedBody = (await locked.json()) as { error: { code: string; message: string } }
  expect(lockedBody.error.message).toBe('登录尝试过于频繁，请稍后再试')

  // 3. 同 IP 桶未被耗尽（50 次上限）：正确凭据的界面登录照常工作。
  await page.goto('/login')
  await page.getByLabel('用户名').fill('admin')
  await page.getByLabel('密码').fill('OmniStore-Test-Admin!')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page).toHaveURL(/\/app$/)

  // 4. 界面端同样拒绝被限流用户名的下一次尝试（错误横幅）。
  await page.getByRole('button', { name: '账号菜单：' }).click()
  await page.getByRole('menuitem', { name: '退出登录' }).click()
  await expect(page).toHaveURL(/\/login$/)
  await page.getByLabel('用户名').fill(ghostUser)
  await page.getByLabel('密码').fill('whatever-pass')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('登录尝试过于频繁')
})
