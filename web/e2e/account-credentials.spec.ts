import { createHash, createHmac } from 'node:crypto'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { expect, test, type Page } from '@playwright/test'

// 个人凭据自服务：显示名即时生效、WebDAV Token 重置后旧凭据失效、
// S3 凭据创建后可签名访问 S3 API、撤销后立即拒绝。
test.describe.configure({ timeout: 120_000 })

const baseURL = process.env.OMNISTORE_E2E_BASE_URL ?? 'http://127.0.0.1:18080'

interface ProtocolCredentials {
  http_endpoint: string
  s3_endpoint: string
  username: string
  webdav_token: string
  region: string
  team_bucket: string
}

function protocolCredentialsPath(): string {
  return process.env.OMNISTORE_E2E_PROTOCOL_CREDENTIALS
    ?? resolve(dirname(fileURLToPath(import.meta.url)), '../../.testdata/protocol-credentials.json')
}

function loadProtocolCredentials(): ProtocolCredentials | null {
  const path = protocolCredentialsPath()
  if (!existsSync(path)) return null
  return JSON.parse(readFileSync(path, 'utf8')) as ProtocolCredentials
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

test('display name change takes effect immediately and can be restored', async ({ page }) => {
  const newName = `E2E用户${Date.now()}`

  await loginUI(page)
  await openProfile(page)

  // 1. 修改显示名并保存。
  await page.getByRole('button', { name: '修改显示名' }).click()
  const dialog = page.getByRole('dialog', { name: '修改显示名' })
  await dialog.getByLabel('新的显示名').fill(newName)
  await dialog.getByRole('button', { name: '保存', exact: true }).click()
  await expect(dialog).toHaveCount(0)

  // 2. 账号菜单与问候语立即更新。
  await page.goto('/app')
  await expect(page.getByRole('button', { name: `账号菜单：${newName}` })).toBeVisible()
  await expect(page.getByText(newName).first()).toBeVisible()

  // 3. 恢复默认显示名。
  await openProfile(page)
  await page.getByRole('button', { name: '修改显示名' }).click()
  const restoreDialog = page.getByRole('dialog', { name: '修改显示名' })
  await restoreDialog.getByLabel('新的显示名').fill('演示用户')
  await restoreDialog.getByRole('button', { name: '保存', exact: true }).click()
  await expect(restoreDialog).toHaveCount(0)
})

test('WebDAV token reset invalidates the old credential immediately', async ({ page, request }) => {
  const credentials = loadProtocolCredentials()
  test.skip(!credentials, '协议凭据文件不存在，跳过 WebDAV 重置验证')

  await loginUI(page)
  await openProfile(page)

  // 1. 展开 WebDAV 凭据组并重置 Token：确认后一次性展示新 Token。
  // 1. 打开 WebDAV 凭据模态框并重置 Token：确认后一次性展示新 Token。
  const webdavDialog = await openCredentialDialog(page, '管理 WebDAV', 'WebDAV')
  await webdavDialog.getByRole('button', { name: '重置 Token' }).click()
  const confirmDialog = page.getByRole('dialog', { name: '重置 WebDAV' })
  await confirmDialog.getByRole('button', { name: '确认重置' }).click()
  const revealDialog = page.getByRole('dialog', { name: '新的 Token' })
  await expect(revealDialog).toBeVisible()
  const newToken = await revealDialog.getByLabel('Token').inputValue()
  expect(newToken.length).toBeGreaterThanOrEqual(16)
  await revealDialog.getByRole('button', { name: '关闭', exact: true }).click()
  await expect(revealDialog).toHaveCount(0)

  // 2. 新 Token 可访问 WebDAV；旧 Token 立即 401。
  const davRoot = `${credentials!.http_endpoint}/dav/${encodeURIComponent(credentials!.team_bucket)}/`
  const basic = (token: string) => `Basic ${Buffer.from(`${credentials!.username}:${token}`).toString('base64')}`
  const withNew = await request.fetch(davRoot, { method: 'PROPFIND', headers: { Authorization: basic(newToken), Depth: '0' } })
  expect(withNew.status()).toBe(207)
  const withOld = await request.fetch(davRoot, { method: 'PROPFIND', headers: { Authorization: basic(credentials!.webdav_token), Depth: '0' } })
  expect(withOld.status()).toBe(401)

  // 种子 Token 已被本次重置作废：把新 Token 回写协议凭据文件，
  // 后续用例（审计日志、协议互通）在运行时从该文件读取当前 Token。
  writeFileSync(protocolCredentialsPath(), JSON.stringify({ ...credentials!, webdav_token: newToken }, null, 2))
})

test('S3 credentials can be created, used against the S3 API and revoked', async ({ page, request }) => {
  const credentials = loadProtocolCredentials()
  test.skip(!credentials, '协议凭据文件不存在，跳过 S3 凭据验证')
  const credentialName = `e2e-s3-${Date.now()}`

  // 本地最小 SigV4 实现（与 protocol-interoperability.spec 同一模式）。
  const sha256 = (value: Buffer | string) => createHash('sha256').update(value).digest('hex')
  const hmac = (key: Buffer | string, value: string) => createHmac('sha256', key).update(value).digest()
  const awsEncode = (value: string) => encodeURIComponent(value).replace(/[!'()*]/g, (c) => `%${c.charCodeAt(0).toString(16).toUpperCase()}`)
  const listObjects = (accessKeyId: string, secretAccessKey: string) => {
    const url = new URL(`/${awsEncode(credentials!.team_bucket)}?list-type=2`, credentials!.s3_endpoint)
    const now = new Date()
    const timestamp = now.toISOString().replace(/[:-]|\.\d{3}/g, '')
    const date = timestamp.slice(0, 8)
    const scope = `${date}/${credentials!.region}/s3/aws4_request`
    const canonicalQuery = 'list-type=2'
    const signedHeaders = 'host;x-amz-date'
    const canonicalHeaders = `host:${url.host}\nx-amz-date:${timestamp}\n`
    const canonicalRequest = ['GET', url.pathname, canonicalQuery, canonicalHeaders, signedHeaders, sha256(Buffer.alloc(0))].join('\n')
    const stringToSign = ['AWS4-HMAC-SHA256', timestamp, scope, sha256(canonicalRequest)].join('\n')
    const signingKey = hmac(hmac(hmac(hmac(`AWS4${secretAccessKey}`, date), credentials!.region), 's3'), 'aws4_request')
    const signature = hmac(signingKey, stringToSign).toString('hex')
    return request.fetch(url.toString(), {
      headers: {
        Authorization: `AWS4-HMAC-SHA256 Credential=${accessKeyId}/${scope}, SignedHeaders=${signedHeaders}, Signature=${signature}`,
        'X-Amz-Content-Sha256': sha256(Buffer.alloc(0)),
        'X-Amz-Date': timestamp,
      },
    })
  }

  await loginUI(page)
  await openProfile(page)

  // 1. 展开 S3 凭据组、创建凭据并读取一次性展示的密钥。
  // 1. 打开 S3 凭据模态框、创建凭据并读取一次性展示的密钥。
  const s3Dialog = await openCredentialDialog(page, '管理 S3', 'S3')
  await s3Dialog.getByRole('button', { name: '新建凭据' }).click()
  const createDialog = page.getByRole('dialog', { name: '新建 S3 凭据' })
  await createDialog.getByLabel('凭据名称').fill(credentialName)
  await createDialog.getByRole('button', { name: '创建凭据' }).click()
  const revealDialog = page.getByRole('dialog', { name: '新的 S3 凭据' })
  await expect(revealDialog).toBeVisible()
  const accessKeyId = await revealDialog.getByLabel('Access Key ID').inputValue()
  const secretAccessKey = await revealDialog.getByLabel('Secret Access Key').inputValue()
  expect(accessKeyId).toBeTruthy()
  expect(secretAccessKey).toBeTruthy()
  await revealDialog.getByRole('button', { name: '我已保存' }).click()
  await expect(revealDialog).toHaveCount(0)

  // 2. 新凭据可列出 S3 对象。
  const listWithNew = await listObjects(accessKeyId, secretAccessKey)
  expect(listWithNew.status()).toBe(200)
  expect(await listWithNew.text()).toContain(credentials!.team_bucket)

  // 3. 撤销后立即 403。
  await s3Dialog.getByRole('button', { name: `撤销 ${credentialName}` }).click()
  const revokeDialog = page.getByRole('dialog', { name: '撤销 S3 凭据' })
  await revokeDialog.getByRole('button', { name: '确认撤销' }).click()
  await expect(revokeDialog).toHaveCount(0)
  const listWithRevoked = await listObjects(accessKeyId, secretAccessKey)
  expect(listWithRevoked.status()).toBe(403)
  await s3Dialog.getByRole('button', { name: '关闭弹窗' }).click()
})
