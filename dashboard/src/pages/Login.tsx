import { useEffect, useState, type FormEvent, type ReactNode } from 'react'
import { api, type WhoAmI } from '../api'
import { Loader2, Ship } from 'lucide-react'
import { ErrorBox } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

// AuthCard is the centered card the sign-in and invite pages use.
export function AuthCard({ description, children }: { description: ReactNode; children: ReactNode }) {
  return (
    <main className="flex min-h-svh items-center justify-center bg-muted/40 px-4 py-10">
      <Card className="w-full max-w-sm">
        <CardHeader className="gap-3">
          <div className="flex items-center gap-2.5">
            <div className="flex size-9 items-center justify-center rounded-lg bg-primary text-primary-foreground">
              <Ship className="size-5" aria-hidden />
            </div>
            <h1 className="text-xl font-semibold tracking-tight">Coldship</h1>
          </div>
          <CardDescription>{description}</CardDescription>
        </CardHeader>
        <CardContent>{children}</CardContent>
      </Card>
    </main>
  )
}

export function Submit({ busy, disabled, children, busyText }: { busy: boolean; disabled?: boolean; children: ReactNode; busyText: string }) {
  return (
    <Button type="submit" size="lg" className="w-full" disabled={busy || disabled}>
      {busy && <Loader2 className="animate-spin" aria-hidden />}
      {busy ? busyText : children}
    </Button>
  )
}

export default function Login({ onLogin, initialError }: { onLogin: (w: WhoAmI) => void; initialError?: Error }) {
  const [tab, setTab] = useState<'email' | 'token'>('email')
  const [hasUsers, setHasUsers] = useState<boolean>()
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [token, setTok] = useState('')
  const [error, setError] = useState<Error | undefined>(initialError)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    api.authInfo().then(
      (i) => {
        setHasUsers(i.has_users)
        if (!i.has_users) setTab('token')
      },
      () => {},
    )
  }, [])

  async function run(fn: () => Promise<WhoAmI>) {
    setBusy(true)
    setError(undefined)
    try {
      onLogin(await fn())
    } catch (err) {
      setError(err as Error)
    } finally {
      setBusy(false)
    }
  }

  function signIn(e: FormEvent) {
    e.preventDefault()
    run(() => api.signIn(email.trim(), password))
  }

  function tokenSignIn(e: FormEvent) {
    e.preventDefault()
    run(async () => {
      const me = await api.login(token.trim())
      setTok('')
      return me
    })
  }

  return (
    <AuthCard description="Sign in to deploy and manage your apps.">
      <Tabs value={tab} onValueChange={(v) => setTab(v as 'email' | 'token')}>
        <TabsList className="mb-4 grid w-full grid-cols-2">
          <TabsTrigger value="email">Email</TabsTrigger>
          <TabsTrigger value="token">API token</TabsTrigger>
        </TabsList>
        <TabsContent value="email">
          <form className="grid gap-4" onSubmit={signIn}>
            {hasUsers === false && (
              <p className="rounded-lg border border-dashed p-3 text-sm text-muted-foreground">
                No accounts yet. Run <code className="font-mono text-foreground">coldship invite</code> on the server and open the link, or sign in with an
                admin token and create your account.
              </p>
            )}
            <div className="grid gap-2">
              <Label htmlFor="email">Email</Label>
              <Input id="email" type="email" autoComplete="username" value={email} onChange={(e) => setEmail(e.target.value)} autoFocus />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="password">Password</Label>
              <Input id="password" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
            </div>
            {error && tab === 'email' && <ErrorBox error={error} />}
            <Submit busy={busy} disabled={!email.trim() || !password} busyText="Signing in…">
              Sign in
            </Submit>
            <p className="text-xs text-muted-foreground">Forgot your password? Ask an admin of the server for a reset link.</p>
          </form>
        </TabsContent>
        <TabsContent value="token">
          <form className="grid gap-4" onSubmit={tokenSignIn}>
            <div className="grid gap-2">
              <Label htmlFor="token">API token</Label>
              <Input
                id="token"
                type="password"
                autoComplete="off"
                placeholder="cst_…"
                className="font-mono"
                value={token}
                onChange={(e) => setTok(e.target.value)}
              />
            </div>
            {error && tab === 'token' && <ErrorBox error={error} />}
            <Submit busy={busy} disabled={!token.trim()} busyText="Checking…">
              Sign in
            </Submit>
            <p className="text-xs text-muted-foreground">
              The token is exchanged for a 12-hour session and isn't stored in the browser. <code className="font-mono">coldship token create</code> makes
              one; the CLI keeps yours in <code className="font-mono">~/.config/coldship/config.json</code>.
            </p>
          </form>
        </TabsContent>
      </Tabs>
    </AuthCard>
  )
}
