import { useEffect, useState, type FormEvent, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { Trash2 } from 'lucide-react'
import { siGithub } from 'simple-icons'
import { api } from '../api'
import { useSession } from '../App'
import { useLoad } from '../hooks'
import { ErrorBox, Mono, PageHeader, PageSkeleton, Time } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

function Section({ title, description, children, danger }: { title: string; description?: ReactNode; children: ReactNode; danger?: boolean }) {
  return (
    <Card className={danger ? 'border-destructive/40' : undefined}>
      <CardHeader>
        <CardTitle>
          <h2 className={danger ? 'text-base font-semibold text-destructive' : 'text-base font-semibold'}>{title}</h2>
        </CardTitle>
        {description && <CardDescription>{description}</CardDescription>}
      </CardHeader>
      <CardContent className="space-y-3">{children}</CardContent>
    </Card>
  )
}

export default function OrgSettings() {
  const me = useSession()
  const org = useLoad(api.org, [me.org?.id])
  const github = useLoad(api.github, [me.org?.id])
  const admin = me.scope === 'admin'
  const [name, setName] = useState('')
  const [confirm, setConfirm] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<Error>()
  const [saved, setSaved] = useState(false)

  useEffect(() => {
    if (org.data) setName(org.data.name)
  }, [org.data])

  async function rename(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(undefined)
    try {
      await api.renameOrg(name.trim())
      setSaved(true)
      window.dispatchEvent(new Event('coldship:session-changed'))
      org.reload()
    } catch (err) {
      setError(err as Error)
    } finally {
      setBusy(false)
    }
  }

  async function remove() {
    setBusy(true)
    setError(undefined)
    try {
      await api.deleteOrg()
      window.location.assign('/')
    } catch (err) {
      setError(err as Error)
      setBusy(false)
    }
  }

  if (!org.data) return org.error ? <ErrorBox error={org.error} /> : <PageSkeleton kind="form" />
  const o = org.data
  const empty = o.counts.apps === 0 && o.counts.data === 0

  return (
    <>
      <PageHeader title="Settings" description={`${o.name}: created ${new Date(o.created_at).toLocaleDateString()}.`} />
      <div className="space-y-4">
        {error && <ErrorBox error={error} />}
        <Section title="General">
          <form className="flex flex-col gap-3 sm:flex-row sm:items-end" onSubmit={rename}>
            <div className="flex-1 space-y-1.5">
              <Label htmlFor="orgname">Name</Label>
              <Input
                id="orgname"
                value={name}
                onChange={(e) => {
                  setName(e.target.value)
                  setSaved(false)
                }}
                disabled={!admin}
              />
            </div>
            <div className="space-y-1.5">
              <Label>ID</Label>
              <div className="flex h-9 items-center">
                <Mono className="text-muted-foreground">{o.id}</Mono>
              </div>
            </div>
            {admin && (
              <Button type="submit" disabled={busy || !name.trim() || name.trim() === o.name}>
                {saved && name.trim() === o.name ? 'Saved' : 'Save'}
              </Button>
            )}
          </form>
          <dl className="grid grid-cols-2 gap-3 pt-2 sm:grid-cols-4">
            {(
              [
                ['Apps', o.counts.apps, '/apps'],
                ['Data services', o.counts.data, '/data'],
                ['Members', o.counts.members, '/org/team'],
                ['Tokens', o.counts.tokens, '/org/tokens'],
              ] as const
            ).map(([label, n, to]) => (
              <Link key={label} to={to} className="rounded-lg border p-3 transition-colors hover:bg-accent/40">
                <dt className="text-xs text-muted-foreground">{label}</dt>
                <dd className="text-2xl font-semibold tabular-nums">{n}</dd>
              </Link>
            ))}
          </dl>
        </Section>

        <Section title="GitHub" description="Lets the organization list its repositories and deploy private ones. Each organization connects its own.">
          <div className="flex flex-wrap items-center justify-between gap-3 text-sm">
            <span className="inline-flex items-center gap-2">
              <svg viewBox="0 0 24 24" className="size-5 fill-current" aria-hidden>
                <path d={siGithub.path} />
              </svg>
              {github.data?.connected ? (
                <>
                  Connected as <strong>{github.data.account}</strong>
                  {github.data.since && (
                    <span className="text-muted-foreground">
                      · <Time iso={github.data.since} />
                    </span>
                  )}
                </>
              ) : (
                <span className="text-muted-foreground">Not connected</span>
              )}
            </span>
            <Button variant="outline" size="sm" asChild>
              <Link to="/apps/new">{github.data?.connected ? 'Deploy from GitHub' : 'Connect GitHub'}</Link>
            </Button>
          </div>
        </Section>

        {admin && me.user && o.id !== 'default' && (
          <Section title="Delete organization" danger description="Deletes its members, tokens and invites. It must have no apps or data services left.">
            {!empty ? (
              <p className="text-sm text-muted-foreground">
                It still has {o.counts.apps} apps and {o.counts.data} data services: delete them, or move apps to another organization from their settings.
              </p>
            ) : (
              <div className="flex flex-col gap-3 sm:flex-row sm:items-end">
                <div className="flex-1 space-y-1.5">
                  <Label htmlFor="confirmorg">Type {o.id} to confirm</Label>
                  <Input id="confirmorg" value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="off" />
                </div>
                <Button variant="destructive" disabled={busy || confirm !== o.id} onClick={remove}>
                  <Trash2 /> Delete organization
                </Button>
              </div>
            )}
          </Section>
        )}
        {o.id === 'default' && (
          <p className="text-sm text-muted-foreground">
            This is the server's default organization: its admins also manage the server itself (upgrades, secrets, server status), and it can't be deleted.
          </p>
        )}
      </div>
    </>
  )
}
