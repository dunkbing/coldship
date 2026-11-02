import { useEffect, useState, type FormEvent } from 'react'
import { api, type LinkInfo, type WhoAmI } from '../api'
import { ErrorBox } from '@/components/common'
import { Skeleton } from '@/components/ui/skeleton'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { AuthCard, Submit } from './Login'
import { roleLabel } from './Team'

// Invite accepts an invite link (/invite#secret) or sets a password from a
// reset link (/reset#secret). The secret is in the fragment, which never
// reaches a server log.
export default function Invite({ me, onDone }: { me: WhoAmI | null; onDone: (w: WhoAmI) => void }) {
  const [secret] = useState(() => window.location.hash.slice(1))
  const [info, setInfo] = useState<LinkInfo>()
  const [error, setError] = useState<Error>()
  const [busy, setBusy] = useState(false)
  const [email, setEmail] = useState('')
  const [name, setName] = useState('')
  const [password, setPassword] = useState('')

  useEffect(() => {
    // Keep the secret out of the address bar and history.
    history.replaceState(null, '', window.location.pathname)
    api.lookupLink(secret).then(setInfo, setError)
  }, [secret])

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(undefined)
    try {
      const w = await api.acceptLink(secret, joining ? {} : { email: email.trim(), name: name.trim(), password })
      history.replaceState(null, '', '/apps')
      onDone(w)
    } catch (err) {
      setError(err as Error)
      setBusy(false)
    }
  }

  const reset = info?.kind === 'reset'
  // Signed in as a person already: one click joins.
  const joining = !reset && !!me?.user

  if (!info)
    return (
      <AuthCard description="Checking the link…">
        {error ? (
          <ErrorBox error={error} />
        ) : (
          <div className="space-y-3">
            <Skeleton className="h-9 w-full" />
            <Skeleton className="h-9 w-full" />
          </div>
        )}
      </AuthCard>
    )

  return (
    <AuthCard
      description={
        reset ? (
          <>
            Set a new password for <strong className="text-foreground">{info.email}</strong>. Your other sessions are signed out.
          </>
        ) : (
          <>
            {info.created_by} invited you to <strong className="text-foreground">{info.org_name}</strong> as {roleLabel(info.role).toLowerCase()}.
          </>
        )
      }
    >
      <form className="grid gap-4" onSubmit={submit}>
        {joining ? (
          <p className="text-sm text-muted-foreground">
            You're signed in as <strong className="text-foreground">{me?.user?.email}</strong>.
          </p>
        ) : (
          <>
            {!reset && (
              <>
                <div className="grid gap-2">
                  <Label htmlFor="name">Your name</Label>
                  <Input id="name" autoComplete="name" value={name} onChange={(e) => setName(e.target.value)} autoFocus />
                </div>
                <div className="grid gap-2">
                  <Label htmlFor="email">Email</Label>
                  <Input id="email" type="email" autoComplete="username" value={email} onChange={(e) => setEmail(e.target.value)} />
                </div>
              </>
            )}
            <div className="grid gap-2">
              <Label htmlFor="password">{reset ? 'New password' : 'Password'}</Label>
              <Input
                id="password"
                type="password"
                autoComplete="new-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoFocus={reset}
              />
              <p className="text-xs text-muted-foreground">At least 8 characters.</p>
            </div>
          </>
        )}
        {error && <ErrorBox error={error} />}
        <Submit busy={busy} disabled={!joining && (password.length < 8 || (!reset && (!email.trim() || !name.trim())))} busyText="One moment…">
          {reset ? 'Set password' : joining ? `Join ${info.org_name}` : 'Create account'}
        </Submit>
        {!reset && !joining && (
          <p className="text-xs text-muted-foreground">Already have an account? Sign in first, then open the link again.</p>
        )}
      </form>
    </AuthCard>
  )
}
