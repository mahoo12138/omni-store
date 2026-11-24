import type { ReactNode } from 'react'
import { useStyles } from '../profile.styles'

export function CredentialGroupHeader({
  hint,
  status,
  action,
}: {
  hint: string
  status: ReactNode
  action: ReactNode
}) {
  const css = useStyles()
  return (
    <header className={css.credentialGroupHeader}>
      <div className={css.credentialGroupCopy}>
        <div className={css.credentialTitleLine}>{status}</div>
        <p className={css.credentialGroupHint}>{hint}</p>
      </div>
      <div className={css.credentialGroupAction}>{action}</div>
    </header>
  )
}
