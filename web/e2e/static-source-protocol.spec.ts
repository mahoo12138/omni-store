import { expect, test } from '@playwright/test'

// 存储源内一键开启静态 HTTP 访问；CDN / 对外域名、缓存与跨域等在「应用与客户端连接 → 静态 HTTP」中管理。
for (const viewport of [{ width: 1440, height: 1000 }, { width: 375, height: 812 }]) {
  test(`static HTTP is configured per source at ${viewport.width}px`, async ({ page, request }) => {
    await page.setViewportSize(viewport)
    const errors: string[] = []
    page.on('pageerror', (error) => errors.push(error.message))
    page.on('console', (message) => { if (message.type() === 'error') errors.push(message.text()) })
    await page.goto('/login')
    await page.getByLabel('用户名').fill('admin')
    await page.getByLabel('密码').fill('OmniStore-Test-Admin!')
    await page.getByRole('button', { name: '登录', exact: true }).click()
    await expect(page).toHaveURL(/\/app$/)
    const me = await (await page.request.get('/api/v1/auth/me')).json()
    const headers = { 'X-CSRF-Token': me.data.csrf_token }
    const sourcesResponse = await page.request.get('/api/v1/admin/sources')
    const sources: Array<{ key: string; name: string }> = (await sourcesResponse.json()).data.items
    const selected = sources.filter((source) => ['公开演示资料', '团队文件'].includes(source.name))
    expect(selected).toHaveLength(2)
    const filename = `static-${Date.now()} 空格%#.png`
    const published: Array<{ key: string; url: string; id: string; bytes: Buffer }> = []
    try {
      await page.goto('/app/admin?section=sources')
      for (const source of selected) {
        const bytes = Buffer.concat([Buffer.from('89504e470d0a1a0a', 'hex'), Buffer.from(source.name)])
        const upload = await page.request.post(`/api/v1/sources/${source.key}/upload?path=`, {
          headers, multipart: { file: { name: filename, mimeType: 'image/png', buffer: bytes } },
        })
        expect(upload.status()).toBe(200)
        await page.getByRole('button', { name: `配置存储源 ${source.name}` }).click()
        const dialog = page.getByRole('dialog', { name: `配置：${source.name}`, exact: true })
        await expect(dialog).toContainText(source.name)

        // 与 WebDAV / S3 一致：在存储源访问协议中勾选即可立即生效。
        await dialog.getByRole('checkbox', { name: '启用静态 HTTP', exact: true }).check()
        await dialog.getByRole('button', { name: '保存设置', exact: true }).click()
        await expect(dialog).toBeHidden()

        const config = (await (await page.request.get(`/api/v1/admin/sources/${source.key}/static-access/config`)).json()).data
        expect(config.enabled).toBe(true)
        expect(config.publish_root).toBe('')
        published.push({ key: source.key, id: config.public_asset_id, url: `${config.base_url}${encodeURIComponent(filename)}`, bytes })
      }
      expect(published[0].id).not.toBe(published[1].id)
      for (const publication of published) {
        const response = await request.get(publication.url)
        expect(response.status()).toBe(200)
        expect(await response.body()).toEqual(publication.bytes)
      }

      const first = selected[0]
      await page.getByRole('button', { name: `配置存储源 ${first.name}` }).click()
      const dialog = page.getByRole('dialog', { name: `配置：${first.name}`, exact: true })
      await dialog.getByRole('checkbox', { name: '启用静态 HTTP', exact: true }).uncheck()
      await dialog.getByRole('button', { name: '保存设置', exact: true }).click()
      await expect(dialog).toBeHidden()
      expect((await request.get(published[0].url)).status()).toBe(404)
      expect((await request.get(published[1].url)).status()).toBe(200)

      await page.getByRole('button', { name: `配置存储源 ${first.name}` }).click()
      await dialog.getByRole('checkbox', { name: '启用静态 HTTP', exact: true }).check()
      await dialog.getByRole('button', { name: '保存设置', exact: true }).click()
      await expect(dialog).toBeHidden()
      expect((await request.get(published[0].url)).status()).toBe(200)

      // 在「应用与客户端连接 → 静态 HTTP」只配置全局 HTTP 参数（CDN / 缓存 / 跨域），与存储源分离。
      await page.goto('/app/settings?section=profile')
      await page.getByRole('navigation', { name: '连接类型' }).getByRole('button', { name: '管理 静态 HTTP' }).click()
      const staticDialog = page.getByRole('dialog', { name: '静态 HTTP', exact: true })
      await expect(staticDialog).toBeVisible()
      await expect(staticDialog).toContainText(published[0].id)
      await staticDialog.getByLabel('静态资源对外 origin').fill('https://cdn.example.test')
      await staticDialog.getByRole('button', { name: '保存', exact: true }).click()
      await expect(staticDialog).toBeHidden()
      await page.getByRole('navigation', { name: '连接类型' }).getByRole('button', { name: '管理 静态 HTTP' }).click()
      await expect(staticDialog).toContainText('https://cdn.example.test/assets/')
      await expect(staticDialog.getByRole('button', { name: '复制', exact: true }).first()).toBeVisible()
      expect(await staticDialog.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true)
      await staticDialog.getByLabel('静态资源对外 origin').fill('')
      await staticDialog.getByRole('button', { name: '保存', exact: true }).click()
      await expect(staticDialog).toBeHidden()
      expect(errors).toEqual([])
    } finally {
      // 每个测试只撤销自己创建的公开配置，普通文件清理走既有回收站流程。
      for (const publication of published) {
        const path = `/api/v1/admin/sources/${publication.key}/static-access/config`
        const disabled = await page.request.put(path, { headers, data: { enabled: false } })
        const config = (await disabled.json()).data
        expect((await page.request.delete(path, { headers, data: { expected_revision: config.revision } })).status()).toBe(200)
        expect((await page.request.delete(`/api/v1/sources/${publication.key}/files?path=${encodeURIComponent(filename)}`, { headers })).status()).toBe(200)
      }
    }
  })
}
