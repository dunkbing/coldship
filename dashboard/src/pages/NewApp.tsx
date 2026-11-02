import { LiveLog } from '../components/LiveLog'
import { useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { Box, GitBranch, Info, Loader2 } from 'lucide-react'
import { api, type ApplyResult, type Patch, type Plan } from '../api'
import { trackDeploy } from '@/components/toasts'
import { ErrorBox, PageHeader } from '@/components/common'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'

// nameFrom suggests an app name: https://github.com/acme/web-shop.git ->
// web-shop, ghcr.io/acme/api:1.2 -> api.
function nameFrom(kind: 'git' | 'image', source: string): string {
  const s = source.trim()
  const base =
    kind === 'git'
      ? s.replace(/\.git$/, '').split(/[/:]/).pop()
      : s.replace(/@.*$/, '').replace(/:[^/]*$/, '').split('/').pop()
  return (base ?? '')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^[^a-z]+|-+$/g, '')
    .slice(0, 40)
}

// Field is a label above a control, with an optional muted hint.
function Field({ id, label, hint, className, children }: { id: string; label: string; hint?: string; className?: string; children: ReactNode }) {
  return (
    <div className={className ?? 'grid gap-2'}>
      <Label htmlFor={id} className="flex-wrap gap-x-1.5 gap-y-1 leading-snug">
        {label}
        {hint && <span className="font-normal text-muted-foreground">({hint})</span>}
      </Label>
      {children}
    </div>
  )
}

// Check is a checkbox with a label and a muted note beside it.
function Check({ id, label, note, checked, onChange }: { id: string; label: string; note: ReactNode; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <div className="flex items-start gap-2.5">
      <Checkbox id={id} checked={checked} onCheckedChange={(v) => onChange(v === true)} className="mt-0.5" />
      <Label htmlFor={id} className="flex-wrap gap-x-2 gap-y-1 leading-snug font-normal">
        <span className="font-medium">{label}</span>
        <span className="text-xs text-muted-foreground">{note}</span>
      </Label>
    </div>
  )
}

function parseEnv(text: string): Record<string, string> | string {
  const env: Record<string, string> = {}
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (!line || line.startsWith('#')) continue
    const i = line.indexOf('=')
    if (i < 1) return `"${line}" isn't KEY=value`
    env[line.slice(0, i).trim()] = line.slice(i + 1)
  }
  return env
}

export default function NewApp() {
  const nav = useNavigate()
  // A GitHub pick on the launch page arrives as ?git=…&ref=…
  const [params] = useSearchParams()
  const [kind, setKind] = useState<'git' | 'image'>(params.get('image') ? 'image' : 'git')
  const [source, setSource] = useState(params.get('git') ?? params.get('image') ?? '')
  const [ref, setRef] = useState(params.get('ref') ?? '')
  const [name, setName] = useState('')
  const [nameTouched, setNameTouched] = useState(false)
  const [port, setPort] = useState('')
  const [domain, setDomain] = useState('')
  const [envText, setEnvText] = useState('')
  const [postgres, setPostgres] = useState(false)
  const [redis, setRedis] = useState(false)
  const [production, setProduction] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<Error>()
  const [waiting, setWaiting] = useState<Plan>()
  const [launched, setLaunched] = useState<{ app: string; deployment?: string; status?: string; error?: Error }>()
  // Leaving before the deploy ends hands it to a toast.
  const pending = useRef<{ app: string; applying: Promise<ApplyResult> }>(undefined)
  useEffect(
    () => () => {
      if (pending.current) trackDeploy(pending.current.app, pending.current.applying)
    },
    [],
  )

  const appName = nameTouched ? name : nameFrom(kind, source)

  async function submit(e: FormEvent) {
    e.preventDefault()
    setError(undefined)
    const env = parseEnv(envText)
    if (typeof env === 'string') {
      setError(new Error(`Environment: ${env}`))
      return
    }
    const patch: Patch = { name: appName }
    if (kind === 'git') patch.build = { git: source.trim(), ref: ref.trim() || undefined }
    else patch.image = source.trim()
    if (port) patch.port = Number(port)
    if (domain.trim()) patch.add_domains = [domain.trim().toLowerCase()]
    if (Object.keys(env).length) patch.env = env
    const dbs: Patch['add_databases'] = []
    if (postgres) dbs.push({ engine: 'postgres' })
    if (redis) dbs.push({ engine: 'redis', name: postgres ? 'cache' : undefined })
    if (dbs.length) patch.add_databases = dbs
    if (production) patch.stage = 'production'

    setBusy(true)
    try {
      const plan = await api.planPatch(patch)
      if (plan.requires_approval) {
        setWaiting(plan)
        return
      }
      if (plan.action === 'noop') {
        nav(`/apps/${plan.app}`)
        return
      }
      // Apply runs until the deploy finishes; follow its log meanwhile.
      setLaunched({ app: plan.app })
      const applying = api.apply(plan.id, 'created from the dashboard')
      pending.current = { app: plan.app, applying }
      applying
        .catch(() => undefined)
        .finally(() => {
          if (pending.current?.applying === applying) pending.current = undefined
        })
      applying.catch((err) => setLaunched((l) => (l ? { ...l, error: err as Error } : l)))
      for (let i = 0; i < 100; i++) {
        const p = await api.plan(plan.id)
        if (p.deployment) {
          setLaunched((l) => (l ? { ...l, deployment: p.deployment } : l))
          break
        }
        await new Promise((r) => setTimeout(r, 300))
      }
    } catch (err) {
      setError(err as Error)
    } finally {
      setBusy(false)
    }
  }

  if (launched) {
    return (
      <>
        <PageHeader title={`Launching ${launched.app}`} description="Building and starting it. The log updates as it goes." />
        <Card>
          <CardContent className="space-y-3">
            {launched.deployment ? (
              <LiveLog deployment={launched.deployment} onEnd={(s) => setLaunched((l) => (l ? { ...l, status: s } : l))} />
            ) : (
              <div className="flex items-center gap-2 text-sm text-muted-foreground" role="status">
                <Loader2 className="size-4 animate-spin" aria-hidden />
                Starting…
              </div>
            )}
            {launched.error && <ErrorBox error={launched.error} />}
            {launched.status && (
              <div className="flex flex-wrap items-center gap-2">
                <Button asChild>
                  <Link to={`/apps/${launched.app}`}>
                    {launched.status === 'running' ? `Open ${launched.app}` : `See what went wrong in ${launched.app}`}
                  </Link>
                </Button>
                {launched.status !== 'running' && (
                  <Button variant="ghost" onClick={() => setLaunched(undefined)}>
                    Back to the form
                  </Button>
                )}
              </div>
            )}
          </CardContent>
        </Card>
      </>
    )
  }

  return (
    <>
      <PageHeader
        crumbs={<Link to="/apps/new" className="hover:underline">Launch an app</Link>}
        title="Configure and deploy"
        description="Deploy from a Git repository (with or without a Dockerfile) or a container image."
      />

      <Card className="max-w-2xl">
        <CardContent>
          <form className="grid gap-5" onSubmit={submit}>
            <Tabs value={kind} onValueChange={(v) => setKind(v as 'git' | 'image')}>
              <TabsList>
                <TabsTrigger value="git">
                  <GitBranch /> Git repository
                </TabsTrigger>
                <TabsTrigger value="image">
                  <Box /> Container image
                </TabsTrigger>
              </TabsList>
            </Tabs>

            <div className="grid gap-4 sm:grid-cols-[1fr_12rem]">
              <Field id="source" label={kind === 'git' ? 'Repository URL' : 'Image'} className={kind === 'git' ? 'grid gap-2' : 'grid gap-2 sm:col-span-2'}>
                <Input
                  id="source"
                  className="font-mono"
                  value={source}
                  onChange={(e) => setSource(e.target.value)}
                  placeholder={kind === 'git' ? 'https://github.com/you/app.git' : 'nginx:1.27-alpine'}
                  required
                  autoFocus
                />
              </Field>
              {kind === 'git' && (
                <Field id="ref" label="Branch">
                  <Input id="ref" className="font-mono" value={ref} onChange={(e) => setRef(e.target.value)} placeholder="default branch" />
                </Field>
              )}
            </div>

            <div className="grid gap-4 sm:grid-cols-[1fr_12rem]">
              <Field id="name" label="Name">
                <Input
                  id="name"
                  className="font-mono"
                  value={appName}
                  onChange={(e) => {
                    setNameTouched(true)
                    setName(e.target.value)
                  }}
                  placeholder="web"
                  pattern="[a-z][a-z0-9\-]{0,39}"
                  title="lowercase letters, digits and dashes, starting with a letter"
                  required
                />
              </Field>
              <Field id="port" label="Port">
                <Input
                  id="port"
                  type="number"
                  min={1}
                  max={65535}
                  value={port}
                  onChange={(e) => setPort(e.target.value)}
                  placeholder="detected"
                />
              </Field>
            </div>

            <Field id="domain" label="Domain" hint="optional, gets HTTPS automatically">
              <Input id="domain" value={domain} onChange={(e) => setDomain(e.target.value)} placeholder="app.example.com" />
            </Field>

            <Field id="env" label="Environment" hint="optional, one KEY=value per line; values are never shown again">
              <Textarea
                id="env"
                className="font-mono"
                value={envText}
                onChange={(e) => setEnvText(e.target.value)}
                rows={4}
                spellCheck={false}
                placeholder="API_KEY=…"
              />
            </Field>

            <fieldset className="grid gap-3">
              <legend className="mb-3 text-sm font-medium">Databases</legend>
              <Check id="db-postgres" label="Postgres" note={<code className="font-mono">DATABASE_URL</code>} checked={postgres} onChange={setPostgres} />
              <Check id="db-redis" label="Redis" note={<code className="font-mono">REDIS_URL</code>} checked={redis} onChange={setRedis} />
            </fieldset>

            <Check
              id="production"
              label="Production"
              note="destructive changes (by you or an agent) will need an admin's approval"
              checked={production}
              onChange={setProduction}
            />

            {error && <ErrorBox error={error} />}
            {waiting && (
              <Alert role="status" className="bg-info-soft text-info">
                <Info />
                <AlertDescription className="text-info">
                  <p>
                    This change needs an admin's approval first.{' '}
                    <Link className="font-medium underline underline-offset-4" to="/approvals">
                      Review plan {waiting.id}
                    </Link>
                  </p>
                </AlertDescription>
              </Alert>
            )}
            <div className="flex flex-wrap items-center gap-2">
              <Button type="submit" disabled={busy || !source.trim() || !appName}>
                {busy && <Loader2 className="animate-spin" aria-hidden />}
                {busy ? 'Deploying…' : 'Deploy'}
              </Button>
              <Button variant="ghost" asChild>
                <Link to="/apps">Cancel</Link>
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>
    </>
  )
}
