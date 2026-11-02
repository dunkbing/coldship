import { useState, type FormEvent } from 'react'
import { api } from '../api'
import { useSession } from '../App'
import { ErrorBox, PageHeader, ToneBadge } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { roleLabel } from './Team'

function useSubmit() {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<Error>()
  const [done, setDone] = useState(false)
  async function run(fn: () => Promise<unknown>) {
    setBusy(true)
    setError(undefined)
    setDone(false)
    try {
      await fn()
      setDone(true)
    } catch (e) {
      setError(e as Error)
    } finally {
      setBusy(false)
    }
  }
  return { busy, error, done, run }
}

export default function Account() {
  const me = useSession()
  const [name, setName] = useState(me.user?.name ?? '')
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const profile = useSubmit()
  const password = useSubmit()

  if (!me.user)
    return (
      <>
        <PageHeader title="Account" />
        <p className="text-sm text-muted-foreground">You're signed in with the token {me.name}, not an account.</p>
      </>
    )

  function saveProfile(e: FormEvent) {
    e.preventDefault()
    profile.run(async () => {
      await api.updateAccount(name.trim())
      window.dispatchEvent(new Event('coldship:session-changed'))
    })
  }

  function savePassword(e: FormEvent) {
    e.preventDefault()
    password.run(async () => {
      await api.changePassword(current, next)
      setCurrent('')
      setNext('')
    })
  }

  return (
    <>
      <PageHeader title="Account" description={me.user.email} />
      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>Profile</CardTitle>
          </CardHeader>
          <CardContent>
            <form className="grid gap-4" onSubmit={saveProfile}>
              <div className="grid gap-2">
                <Label htmlFor="name">Name</Label>
                <Input id="name" autoComplete="name" value={name} onChange={(e) => setName(e.target.value)} />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="email">Email</Label>
                <Input id="email" value={me.user.email} disabled />
              </div>
              {profile.error && <ErrorBox error={profile.error} />}
              <Button type="submit" className="justify-self-start" disabled={profile.busy || !name.trim() || name.trim() === me.user.name}>
                {profile.done && name.trim() === me.user.name ? 'Saved' : 'Save'}
              </Button>
            </form>
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle>Password</CardTitle>
            <CardDescription>Changing it signs out your other sessions.</CardDescription>
          </CardHeader>
          <CardContent>
            <form className="grid gap-4" onSubmit={savePassword}>
              <input type="email" autoComplete="username" value={me.user.email} readOnly hidden />
              <div className="grid gap-2">
                <Label htmlFor="current">Current password</Label>
                <Input id="current" type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="next">New password</Label>
                <Input id="next" type="password" autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} />
                <p className="text-xs text-muted-foreground">At least 8 characters.</p>
              </div>
              {password.error && <ErrorBox error={password.error} />}
              {password.done && <p className="text-sm text-ok">Password changed.</p>}
              <Button type="submit" className="justify-self-start" disabled={password.busy || !current || next.length < 8}>
                Change password
              </Button>
            </form>
          </CardContent>
        </Card>
        <Card className="lg:col-span-2">
          <CardHeader>
            <CardTitle>Organizations</CardTitle>
          </CardHeader>
          <CardContent>
            <ul className="divide-y rounded-lg border">
              {me.orgs?.map((o) => (
                <li key={o.id} className="flex items-center justify-between gap-3 px-3 py-2 text-sm">
                  <span className="font-medium">
                    {o.name} {o.id === me.org?.id && <span className="font-normal text-muted-foreground">(current)</span>}
                  </span>
                  <ToneBadge t={o.role === 'admin' ? 'info' : 'muted'}>{roleLabel(o.role)}</ToneBadge>
                </li>
              ))}
            </ul>
          </CardContent>
        </Card>
      </div>
    </>
  )
}
