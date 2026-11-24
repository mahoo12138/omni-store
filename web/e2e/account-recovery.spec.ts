import { expect, test, type Page } from '@playwright/test'

const adminPassword = 'OmniStore-Test-Admin!'

async function login(page: Page, username: string, password: string) {
  await page.goto('/login')
  await page.getByLabel('用户名').fill(username)
  await page.getByLabel('密码').fill(password)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page).toHaveURL(/\/app$/)
}

test('password change closes other sessions and administrator can revoke all credentials', async ({ browser, page }) => {
  const username = `recovery-${Date.now()}`
  const initialPassword = 'Recovery-Initial-2026!'
  const recoveredPassword = 'Recovery-Changed-2026!'

  await login(page, 'admin', adminPassword)
  // 系统设置已从侧栏移到账号菜单里。
  await page.getByRole('button', { name: /^账号菜单/ }).click()
  await page.getByRole('menuitem', { name: '系统设置', exact: true }).click()
  await page.getByRole('button', { name: '用户', exact: true }).click()
  await page.getByRole('button', { name: '创建用户' }).click()
  const createDialog = page.getByRole('dialog', { name: '创建用户' })
  await createDialog.getByLabel('用户名').fill(username)
  await createDialog.getByLabel('密码').fill(initialPassword)
  await createDialog.getByRole('button', { name: '创建', exact: true }).click()
  const userRow = page.getByRole('row', { name: new RegExp(username) })
  await expect(userRow).toBeVisible()

  const currentContext = await browser.newContext()
  const currentPage = await currentContext.newPage()
  const otherContext = await browser.newContext()
  const otherPage = await otherContext.newPage()
  try {
    await login(currentPage, username, initialPassword)
    await login(otherPage, username, initialPassword)

    await currentPage.getByRole('button', { name: new RegExp('账号菜单') }).click()
    await currentPage.getByRole('menuitem', { name: '账户设置' }).click()
    await currentPage.getByRole('button', { name: '账户操作', exact: true }).click()
    await currentPage.getByRole('menuitem', { name: '修改密码', exact: true }).click()
    const passwordDialog = currentPage.getByRole('dialog', { name: '修改密码' })
    await passwordDialog.getByLabel('旧密码').fill(initialPassword)
    await passwordDialog.getByLabel('新密码（至少 8 位）').fill(recoveredPassword)
    await passwordDialog.getByRole('button', { name: '修改密码' }).click()
    await expect(passwordDialog.getByText('密码已修改')).toBeVisible()

    await currentPage.goto('/app')
    await expect(currentPage).toHaveURL(/\/app$/)
    await otherPage.reload()
    await expect(otherPage).toHaveURL(/\/login$/)

    await userRow.getByRole('button', { name: `用户 ${username} 的更多操作` }).click()
    await page.getByRole('menuitem', { name: '撤销访问凭据', exact: true }).click()
    const revokeDialog = page.getByRole('dialog', { name: '撤销全部凭据' })
    await expect(revokeDialog).toContainText('所有网页登录会话、WebDAV Token、图床 Token 和 S3 Key 将立即失效')
    page.once('dialog', (dialog) => dialog.accept())
    await revokeDialog.getByRole('button', { name: '撤销全部凭据' }).click()
    await expect(revokeDialog).toHaveCount(0)

    await currentPage.reload()
    await expect(currentPage).toHaveURL(/\/login$/)
    await login(currentPage, username, recoveredPassword)
  } finally {
    await currentContext.close()
    await otherContext.close()
  }

  await userRow.getByRole('button', { name: `用户 ${username} 的更多操作` }).click()
  await page.getByRole('menuitem', { name: '删除用户', exact: true }).click()
  await page.getByRole('dialog', { name: '删除用户' }).getByRole('button', { name: '确认删除' }).click()
  await expect(userRow).toHaveCount(0)
})
