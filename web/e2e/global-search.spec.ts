import { expect, test, type Page } from '@playwright/test'

// 全局搜索：AppHome 顶栏按钮与 ⌘K 均可呼出；关键字不足 2 字符不给搜索；
// 结果行提供「打开所在目录」跳转与下载链接；存储源筛选收窄结果范围。
async function login(page: Page, username: string, password: string) {
  await page.goto('/login')
  await page.getByLabel('用户名').fill(username)
  await page.getByLabel('密码').fill(password)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page).toHaveURL(/\/app$/)
}

async function openSearch(page: Page) {
  const dialog = page.getByRole('dialog', { name: '全局搜索' })
  await expect(dialog).toBeVisible()
  return dialog
}

test('global search finds seeded files, filters by source and navigates to parent folder', async ({ page }) => {
  await login(page, 'demo', 'OmniStore-Test-Demo!')

  // 1. ⌘K 快捷键呼出。
  await page.keyboard.press('ControlOrMeta+k')
  let dialog = await openSearch(page)
  await dialog.getByRole('button', { name: '关闭弹窗' }).click()
  await expect(page.getByRole('dialog', { name: '全局搜索' })).toHaveCount(0)

  // 2. 顶栏按钮呼出；关键字不足 2 字符时搜索按钮禁用。
  await page.getByRole('button', { name: '搜索文件与目录' }).click()
  dialog = await openSearch(page)
  const queryInput = dialog.getByRole('textbox', { name: '搜索关键字' })
  await queryInput.fill('r')
  await expect(dialog.getByRole('button', { name: '搜索', exact: true })).toBeDisabled()

  // 3. 搜索种子文件：结果行带存储源、路径、大小与两个操作入口。
  await queryInput.fill('roadmap')
  await dialog.getByRole('button', { name: '搜索', exact: true }).click()
  const results = dialog.getByRole('region', { name: '搜索结果' })
  await expect(results).toBeVisible()
  await expect(results).toContainText('roadmap.md')
  await expect(results).toContainText('团队文件 · /projects/roadmap.md')
  const firstRow = results.locator('article').first()
  await expect(firstRow.getByRole('link', { name: '下载 roadmap.md' })).toBeVisible()

  // 4. 「打开所在目录」跳转到父目录的文件管理器。
  await firstRow.getByRole('button', { name: '打开所在目录' }).click()
  await expect(page).toHaveURL(/\/app\/sources\/[^/]+\?path=%2Fprojects/)
  await expect(page.getByRole('row', { name: /roadmap\.md/ })).toBeVisible()
})

test('global search filters results by source and reports empty matches', async ({ page }) => {
  await login(page, 'demo', 'OmniStore-Test-Demo!')
  await page.getByRole('button', { name: '搜索文件与目录' }).click()
  const dialog = await openSearch(page)

  // 1. 筛选公开演示资料：README.txt 只在公开源，结果只来自该源。
  await dialog.getByRole('textbox', { name: '搜索关键字' }).fill('readme')
  await dialog.getByRole('combobox', { name: '筛选存储源' }).click()
  await page.getByRole('option', { name: '公开演示资料' }).click()
  await dialog.getByRole('button', { name: '搜索', exact: true }).click()
  const results = dialog.getByRole('region', { name: '搜索结果' })
  await expect(results).toContainText('README.txt')
  await expect(results).toContainText('公开演示资料 · /README.txt')
  await expect(results.getByText('团队文件 ·')).toHaveCount(0)

  // 2. 同关键字切到团队文件：没有 README，展示空态。
  await dialog.getByRole('combobox', { name: '筛选存储源' }).click()
  await page.getByRole('option', { name: '团队文件' }).click()
  await dialog.getByRole('button', { name: '搜索', exact: true }).click()
  await expect(dialog.getByText('没有找到匹配文件')).toBeVisible()

  // 3. 无匹配关键字的通配搜索同样展示空态并给出提示。
  await dialog.getByRole('textbox', { name: '搜索关键字' }).fill('e2e-no-such-file')
  await dialog.getByRole('button', { name: '搜索', exact: true }).click()
  await expect(dialog.getByText('尝试缩短关键字，或切换到其他存储源。')).toBeVisible()
})
