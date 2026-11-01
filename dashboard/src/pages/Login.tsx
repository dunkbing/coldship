import { useState, type FormEvent } from 'react'
import { api, type WhoAmI } from '../api'
import { Loader2, Ship } from 'lucide-react'
import { ErrorBox } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

export default function Login({ onLogin, initialError }: { onLogin: (w: WhoAmI) => void; initialError?: Error }) {
  const [token, setTok] = useState('')
  const [error, setError] = useState<Error | undefined>(initialError)
  const [busy, setBusy] = useState(false)

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(undefined)
    try {
      const me = await api.login(token.trim())
      setTok('')
      onLogin(me)
    } catch (err) {
      setError(err as Error)
    } finally {
      setBusy(false)
    }
  }

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
          <CardDescription>
            Sign in with an API token. Use an admin token to approve plans. The token is exchanged for a 12-hour session and is not
            stored in the browser.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form className="grid gap-4" onSubmit={submit}>
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
                autoFocus
              />
            </div>
            {error && <ErrorBox error={error} />}
            <Button type="submit" size="lg" className="w-full" disabled={busy || !token.trim()}>
              {busy && <Loader2 className="animate-spin" aria-hidden />}
              {busy ? 'Checking…' : 'Sign in'}
            </Button>
            <p className="text-xs text-muted-foreground">
              The first admin token is printed when <code className="font-mono">coldship server</code> starts for the first time.
            </p>
          </form>
        </CardContent>
      </Card>
    </main>
  )
}
