import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http'
import { createSign, generateKeyPairSync, type KeyObject } from 'node:crypto'
import { expect, request as playwrightRequest, test, type APIRequestContext } from '@playwright/test'

// OIDC 单点登录（2.1.0）：spec 内嵌 fake IdP（Node http 随机端口 + RS256 签名），
// 经 admin API 注入配置（issuer 指向 127.0.0.1 随机端口）。该拓扑不依赖固定端口
// 与环境变量，本地 test-env.sh（18080）与发布门禁 verify-browser.mjs（随机端口）
// 均可运行；不依赖真实 Casdoor。

const FAKE_CODE = 'e2e-auth-code'
const FAKE_CLIENT_ID = 'e2e-omnistore-client'

interface FakeClaims {
  sub: string
  preferred_username: string
  name: string
  email: string
}

class FakeIdP {
  private server: Server
  private privateKey: KeyObject
  private kid = 'e2e-key-1'
  issuer = ''
  claims: FakeClaims = { sub: 'e2e-sub-1', preferred_username: 'e2e-sso-user', name: 'E2E SSO 用户', email: 'sso@example.com' }

  constructor() {
    const { privateKey, publicKey } = generateKeyPairSync('rsa', { modulusLength: 2048 })
    this.privateKey = privateKey
    const publicJwk = publicKey.export({ format: 'jwk', type: 'spki' }) as Record<string, string>
    this.server = createServer((req, res) => this.route(req, res, publicJwk))
  }

  async start(): Promise<string> {
    await new Promise<void>((resolve) => this.server.listen(0, '127.0.0.1', resolve))
    const address = this.server.address()
    if (!address || typeof address === 'string') throw new Error('fake IdP listen failed')
    this.issuer = `http://127.0.0.1:${address.port}`
    return this.issuer
  }

  async stop(): Promise<void> {
    await new Promise<void>((resolve) => this.server.close(() => resolve()))
  }

  private async route(req: IncomingMessage, res: ServerResponse, publicJwk: Record<string, string>): Promise<void> {
    const url = new URL(req.url ?? '/', this.issuer)
    if (url.pathname === '/.well-known/openid-configuration') {
      return this.json(res, {
        issuer: this.issuer,
        authorization_endpoint: `${this.issuer}/authorize`,
        token_endpoint: `${this.issuer}/token`,
        jwks_uri: `${this.issuer}/jwks`,
        response_types_supported: ['code'],
        subject_types_supported: ['public'],
        id_token_signing_alg_values_supported: ['RS256'],
      })
    }
    // 授权端点：自动同意，透传 nonce（回调侧 ID token 校验必需），
    // 并直接携带 code/state 302 回 OmniStore 回调。
    if (url.pathname === '/authorize') {
      this.currentNonce = url.searchParams.get('nonce') ?? ''
      const redirect = url.searchParams.get('redirect_uri') ?? ''
      const state = url.searchParams.get('state') ?? ''
      res.writeHead(302, { Location: `${redirect}?code=${FAKE_CODE}&state=${encodeURIComponent(state)}` })
      res.end()
      return
    }
    if (url.pathname === '/jwks') {
      return this.json(res, { keys: [{ kty: 'RSA', use: 'sig', alg: 'RS256', kid: this.kid, n: publicJwk.n, e: publicJwk.e }] })
    }
    if (url.pathname === '/token') {
      const body = await this.readForm(req)
      if (body.get('code') !== FAKE_CODE) {
        res.writeHead(400, { 'Content-Type': 'application/json' })
        res.end(JSON.stringify({ error: 'invalid_grant' }))
        return
      }
      return this.json(res, {
        access_token: 'e2e-access-token', token_type: 'Bearer', expires_in: 3600,
        id_token: this.signIDToken(),
      })
    }
    res.writeHead(404)
    res.end()
  }

  private signIDToken(): string {
    const now = Math.floor(Date.now() / 1000)
    const header = Buffer.from(JSON.stringify({ alg: 'RS256', typ: 'JWT', kid: this.kid })).toString('base64url')
    const payload = Buffer.from(JSON.stringify({
      iss: this.issuer, sub: this.claims.sub, aud: FAKE_CLIENT_ID,
      exp: now + 3600, iat: now, nonce: this.currentNonce,
      preferred_username: this.claims.preferred_username,
      name: this.claims.name, email: this.claims.email,
    })).toString('base64url')
    const signature = createSign('RSA-SHA256').update(`${header}.${payload}`).sign(this.privateKey)
    return `${header}.${payload}.${signature.toString('base64url')}`
  }

  private currentNonce = ''

  /** 每次发起 SSO 后从授权 URL 中取 nonce 再设置 claims，保证 nonce 一致。 */
  setClaims(claims: FakeClaims): void {
    this.claims = claims
  }

  setNonce(nonce: string): void {
    this.currentNonce = nonce
  }

  private json(res: ServerResponse, body: unknown): void {
    res.writeHead(200, { 'Content-Type': 'application/json' })
    res.end(JSON.stringify(body))
  }

  private readForm(req: IncomingMessage): Promise<URLSearchParams> {
    return new Promise((resolve) => {
      let raw = ''
      req.on('data', (chunk) => { raw += chunk })
      req.on('end', () => resolve(new URLSearchParams(raw)))
    })
  }
}

async function loginAdmin(baseURL: string): Promise<{ request: APIRequestContext; csrf: string }> {
  const api = await playwrightRequest.newContext({ baseURL })
  const response = await api.post('/api/v1/auth/login', {
    data: { username: 'admin', password: 'OmniStore-Test-Admin!' },
  })
  expect(response.ok()).toBeTruthy()
  const body = (await response.json()) as { data: { csrf_token: string } }
  return { request: api, csrf: body.data.csrf_token }
}

async function configureSso(api: APIRequestContext, csrf: string, options: {
  issuer: string
  autoCreate: boolean
  autoLink: boolean
}): Promise<void> {
  const response = await api.put('/api/v1/admin/oidc', {
    headers: { 'X-CSRF-Token': csrf },
    data: {
      enabled: true, issuer: options.issuer, client_id: FAKE_CLIENT_ID,
      client_secret: 'e2e-secret', scopes: ['openid', 'profile', 'email'],
      auto_create_users: options.autoCreate, auto_link_username: options.autoLink,
    },
  })
  expect(response.ok()).toBeTruthy()
}

test.describe('OIDC 单点登录', () => {
  test('JIT 自动建号后经登录页 SSO 按钮完成登录', async ({ page }) => {
    const idp = new FakeIdP()
    const issuer = await idp.start()
    const { request: api, csrf } = await loginAdmin(process.env.OMNISTORE_E2E_BASE_URL ?? 'http://127.0.0.1:18080')
    await configureSso(api, csrf, { issuer, autoCreate: true, autoLink: false })

    // 登录页出现 SSO 入口（auth-status 暴露 oidc_enabled）。
    await page.goto('/login')
    const ssoButton = page.getByRole('link', { name: '使用 SSO 登录' })
    await expect(ssoButton).toBeVisible()

    // 发起登录：浏览器 302 链（后端 → fake IdP 授权 → 回调）直达 /app。
    await Promise.all([
      page.waitForURL(/\/app$/),
      ssoButton.click(),
    ])

    // 回调建立会话：当前浏览器身份即 JIT 创建的用户。
    const me = await page.request.get('/api/v1/auth/me')
    expect(me.ok()).toBeTruthy()
    const meBody = (await me.json()) as { data: { user: { username: string; sso_bound: boolean; role: string } } }
    expect(meBody.data.user.username).toBe('e2e-sso-user')
    expect(meBody.data.user.sso_bound).toBe(true)
    expect(meBody.data.user.role).toBe('user')

    await api.dispose()
    await idp.stop()
  })

  test('未开通的 IdP 账号被拒绝并展示中文错误横幅', async ({ page }) => {
    const idp = new FakeIdP()
    const issuer = await idp.start()
    const { request: api, csrf } = await loginAdmin(process.env.OMNISTORE_E2E_BASE_URL ?? 'http://127.0.0.1:18080')
    await configureSso(api, csrf, { issuer, autoCreate: false, autoLink: false })
    idp.setClaims({ sub: 'e2e-sub-stranger', preferred_username: 'stranger', name: 'Stranger', email: 's@example.com' })

    await page.goto('/login')
    const ssoButton = page.getByRole('link', { name: '使用 SSO 登录' })
    await expect(ssoButton).toBeVisible()
    await ssoButton.click()
    await page.waitForURL(/\/login\?sso_error=oidc_identity_unbound$/)
    await expect(page.getByText('当前 IdP 账号尚未开通本站访问，请联系管理员绑定或开通。')).toBeVisible()

    await api.dispose()
    await idp.stop()
  })
})
