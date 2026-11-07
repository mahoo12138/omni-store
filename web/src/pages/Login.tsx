import { useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { fetchAuthStatus, login } from '../api/auth'
import { SSO_ERROR_MESSAGES } from '../api/sso'
import { ApiRequestError } from '../api/client'
import { Button } from '../components/ui/Button'
import { Banner } from '../components/shell/Banner'
import { FormField } from '../components/shell/FormLayout'
import { Input } from '@fluentui/react-components'
import { IconChevronRight, IconKey, LogoMark } from '../components/ui/Icon'
import { AsciiTransfer } from '../components/login/AsciiTransfer'
import { useStyles } from './AuthForm.styles'

export function LoginPage() {
  const css = useStyles()
  const cardRef = useRef<HTMLDivElement>(null)
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  // 回调失败时后端 302 到 /login?sso_error=<code>。
  const search = useSearch({ strict: false }) as { sso_error?: string }
  const authStatus = useQuery({ queryKey: ['auth-status'], queryFn: fetchAuthStatus, staleTime: 30_000 })
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const ssoError = search.sso_error ? SSO_ERROR_MESSAGES[search.sso_error] ?? 'SSO 登录失败，请重试。' : ''

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    setError('')
    setSubmitting(true)
    try {
      const user = await login(username, password)
      queryClient.setQueryData(['me'], user)
      queryClient.setQueryData(['auth-status'], { authenticated: true })
      await navigate({ to: '/app' })
    } catch (err) {
      setError(err instanceof ApiRequestError ? err.message : '登录失败，请稍后重试')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <main className={css.page}>
      <AsciiTransfer className={css.canvas} anchorRef={cardRef} />
      <div className={css.card} ref={cardRef}>
        <div className={css.content}>
          <header className={css.brandRow}>
            <span className={css.brand}>
              <LogoMark size={28} />
              <span className={css.brandName}>OmniStore</span>
            </span>
            <span className={css.selfHosted}>SELF-HOSTED</span>
          </header>
          <div className={css.intro}>
            <h1 className={css.title}>登录到你的存储中心</h1>
          </div>
          {ssoError && <Banner intent="error">{ssoError}</Banner>}
          <form className={css.form} onSubmit={onSubmit}>
            <FormField label="用户名">
              <Input
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                autoComplete="username"
                autoFocus
                required
              />
            </FormField>
            <FormField label="密码">
              <Input
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoComplete="current-password"
                required
              />
            </FormField>
            {error && <Banner intent="error">{error}</Banner>}
            <Button
              type="submit"
              className={css.submit}
              icon={<IconChevronRight size={16} />}
              iconPosition="after"
              disabled={submitting}
            >
              {submitting ? '登录中…' : '登录'}
            </Button>
          </form>
          {authStatus.data?.oidc_enabled && (
            <div className={css.sso}>
              <div className={css.ssoDivider} role="separator" aria-label="或使用单点登录">
                <span>或</span>
              </div>
              {/* 整页跳转到后端发起端点，由后端驱动 302 完成 OIDC 流程。 */}
              <a className={css.ssoButton} href="/api/v1/auth/oidc/login">
                <IconKey size={16} />
                使用 SSO 登录
              </a>
            </div>
          )}
          <footer className={css.footer}>
            <a className={css.footerLink} href="/">返回公开网盘</a>
            <span className={css.footerMeta}>CLIENT → STORE → DELIVER</span>
          </footer>
        </div>
      </div>
    </main>
  )
}
