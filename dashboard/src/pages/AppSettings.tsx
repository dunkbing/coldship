import { useState, type ReactNode } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { ArrowRightLeft, GitBranch, Package, Plus, Trash2, Unplug, X } from 'lucide-react'
import { api, type AppView, type Patch, type Plan } from '@/api'
import { useLoad } from '@/hooks'
import { useSession } from '@/App'
import { ChangeList, ErrorBox, Mono, PlanMeta, ToneBadge } from '@/components/common'
import { LiveLog } from '@/components/LiveLog'
import { DataMark } from '@/components/data'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Checkbox } from '@/components/ui/checkbox'

type Step =
  | { kind: 'review'; plan: Plan; title: string }
  | { kind: 'approval'; plan: Plan; title: string }
  | { kind: 'deploying'; plan: Plan; title: string; deployment?: string; status?: string; error?: Error }

// useChange plans a change, shows it for review, then applies it and follows the deploy.
function useChange(app: AppView, onDone: () => void) {
  const [step, setStep] = useState<Step>()
  const [error, setError] = useState<Error>()
  const [busy, setBusy] = useState(false)

  async function propose(title: string, plan: () => Promise<Plan>) {
    setBusy(true)
    setError(undefined)
    try {
      const p = await plan()
      if (p.action === 'noop') {
        setError(new Error('Nothing to change: the app already looks like that.'))
        return
      }
      setStep({ kind: p.requires_approval ? 'approval' : 'review', plan: p, title })
    } catch (e) {
      setError(e as Error)
    } finally {
      setBusy(false)
    }
  }

  async function confirm() {
    if (step?.kind !== 'review') return
    const { plan, title } = step
    setStep({ kind: 'deploying', plan, title })
    const applying = api.apply(plan.id, `${title}, from the dashboard`)
    applying.catch((e) => setStep((s) => (s?.kind === 'deploying' ? { ...s, error: e as Error } : s)))
    if (plan.action === 'delete') {
      await applying.catch(() => undefined)
      return
    }
    for (let i = 0; i < 100; i++) {
      const p = await api.plan(plan.id)
      if (p.deployment) {
        setStep((s) => (s?.kind === 'deploying' ? { ...s, deployment: p.deployment } : s))
        return
      }
      await new Promise((r) => setTimeout(r, 300))
    }
  }

  const dialog = (
    <Dialog
      open={!!step}
      onOpenChange={(open) => {
        if (!open) {
          setStep(undefined)
          onDone()
        }
      }}
    >
      <DialogContent className="sm:max-w-2xl">
        {step && (
          <>
            <DialogHeader>
              <DialogTitle>{step.title}</DialogTitle>
              <DialogDescription>
                {step.kind === 'deploying'
                  ? step.plan.action === 'delete'
                    ? `Deleting ${app.name}…`
                    : 'Deploying the change. The log updates as it goes.'
                  : step.kind === 'approval'
                    ? 'This is a destructive change to a production app, so an admin must approve it first.'
                    : 'Review what changes. Applying redeploys the app.'}
              </DialogDescription>
            </DialogHeader>
            {step.kind !== 'deploying' ? (
              <div className="space-y-3">
                <ChangeList changes={step.plan.changes} />
                <PlanMeta plan={step.plan} />
              </div>
            ) : step.deployment ? (
              <LiveLog deployment={step.deployment} onEnd={(s) => setStep((x) => (x?.kind === 'deploying' ? { ...x, status: s } : x))} />
            ) : null}
            {step.kind === 'deploying' && step.error && <ErrorBox error={step.error} />}
            <DialogFooter>
              {step.kind === 'review' && (
                <>
                  <Button variant="ghost" onClick={() => setStep(undefined)}>
                    Cancel
                  </Button>
                  <Button variant={step.plan.destructive ? 'destructive' : 'default'} onClick={confirm}>
                    {step.plan.action === 'delete' ? `Delete ${app.name}` : 'Apply'}
                  </Button>
                </>
              )}
              {step.kind === 'approval' && (
                <Button asChild>
                  <Link to="/approvals">Go to Approvals</Link>
                </Button>
              )}
              {step.kind === 'deploying' && (step.status || step.error) && (
                <Button
                  onClick={() => {
                    setStep(undefined)
                    onDone()
                  }}
                >
                  Done
                </Button>
              )}
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  )

  const change = (title: string, patch: Omit<Patch, 'name'>) => propose(title, () => api.planPatch({ name: app.name, ...patch }))
  return { change, propose, dialog, error, busy, deleting: step?.kind === 'deploying' && step.plan.action === 'delete' }
}

function Section({ title, description, children }: { title: string; description?: ReactNode; children: ReactNode }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2 className="text-base font-semibold">{title}</h2>
        </CardTitle>
        {description && <CardDescription>{description}</CardDescription>}
      </CardHeader>
      <CardContent className="space-y-4">{children}</CardContent>
    </Card>
  )
}

function Row({ children }: { children: ReactNode }) {
  return <div className="flex flex-col gap-3 sm:flex-row sm:items-end">{children}</div>
}

function Field({ id, label, children, grow }: { id: string; label: string; children: ReactNode; grow?: boolean }) {
  return (
    <div className={grow ? 'flex-1 space-y-1.5' : 'space-y-1.5'}>
      <Label htmlFor={id}>{label}</Label>
      {children}
    </div>
  )
}

export function SettingsTab({ app, onChange }: { app: AppView; onChange: () => void }) {
  const me = useSession()
  const nav = useNavigate()
  const s = app.spec
  const ch = useChange(app, onChange)
  const data = useLoad(api.data, [])
  const [git, setGit] = useState(s?.build?.git ?? '')
  const [ref, setRef] = useState(s?.build?.ref ?? '')
  const [image, setImage] = useState(s?.build ? '' : (s?.image ?? ''))
  const [port, setPort] = useState(s?.port ? String(s.port) : '')
  const [hostPort, setHostPort] = useState(s?.host_port ? String(s.host_port) : '')
  const [domain, setDomain] = useState('')
  const [envKey, setEnvKey] = useState('')
  const [envVal, setEnvVal] = useState('')
  const [memory, setMemory] = useState(s?.memory ?? '')
  const [use, setUse] = useState('')
  const [confirmName, setConfirmName] = useState('')
  if (!s) return null

  const current = app.latest_deployment?.status === 'running' ? app.latest_deployment.image : undefined
  const services = (data.data ?? []).filter((d) => !d.app && !(s.uses ?? []).some((u) => u.split('=')[0] === d.name))
  const busy = ch.busy

  return (
    <div className="space-y-4">
      {ch.error && <ErrorBox error={ch.error} />}
      {ch.dialog}

      <Section title="Source" description="Where the app's code comes from. Changing it rebuilds and redeploys.">
        {s.build ? (
          <>
            <Row>
              <Field id="git" label="Git repository" grow>
                <Input id="git" value={git} onChange={(e) => setGit(e.target.value)} className="font-mono" />
              </Field>
              <Field id="ref" label="Branch or tag">
                <Input id="ref" value={ref} onChange={(e) => setRef(e.target.value)} placeholder="default branch" />
              </Field>
              <Button
                disabled={busy || !git.trim()}
                onClick={() => ch.change('Change the repository', { build: { git: git.trim(), ref: ref.trim() || undefined } })}
              >
                <GitBranch /> Save
              </Button>
            </Row>
            {current && (
              <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-dashed p-3 text-sm">
                <span className="text-muted-foreground">
                  Stop building from Git and keep running the current build (<Mono>{current}</Mono>).
                </span>
                <Button variant="outline" size="sm" disabled={busy} onClick={() => ch.change('Disconnect the repository', { image: current })}>
                  <Unplug /> Disconnect repository
                </Button>
              </div>
            )}
          </>
        ) : (
          <>
            <Row>
              <Field id="image" label="Container image" grow>
                <Input id="image" value={image} onChange={(e) => setImage(e.target.value)} className="font-mono" />
              </Field>
              <Button disabled={busy || !image.trim() || image === s.image} onClick={() => ch.change('Change the image', { image: image.trim() })}>
                <Package /> Save
              </Button>
            </Row>
            <Row>
              <Field id="newgit" label="Or connect a Git repository" grow>
                <Input id="newgit" value={git} onChange={(e) => setGit(e.target.value)} placeholder="https://github.com/you/app.git" className="font-mono" />
              </Field>
              <Field id="newref" label="Branch">
                <Input id="newref" value={ref} onChange={(e) => setRef(e.target.value)} placeholder="default branch" />
              </Field>
              <Button
                variant="outline"
                disabled={busy || !git.trim()}
                onClick={() => ch.change('Connect a Git repository', { build: { git: git.trim(), ref: ref.trim() || undefined } })}
              >
                <GitBranch /> Connect
              </Button>
            </Row>
          </>
        )}
      </Section>

      <Section title="Networking" description="The port the app listens on, a port on the server, and domains (with automatic HTTPS).">
        <Row>
          <Field id="port" label="App port">
            <Input id="port" type="number" min={1} max={65535} value={port} onChange={(e) => setPort(e.target.value)} className="sm:w-32" />
          </Field>
          <Button variant="outline" disabled={busy || !port || Number(port) === s.port} onClick={() => ch.change('Change the port', { port: Number(port) })}>
            Save port
          </Button>
          <Field id="hostport" label="Server port">
            <Input id="hostport" type="number" min={1} max={65535} value={hostPort} onChange={(e) => setHostPort(e.target.value)} placeholder="none" className="sm:w-32" />
          </Field>
          <Button
            variant="outline"
            disabled={busy || (hostPort === '' ? !s.host_port : Number(hostPort) === s.host_port)}
            onClick={() =>
              ch.change(hostPort ? 'Publish on a server port' : 'Stop publishing on a server port', hostPort ? { host_port: Number(hostPort) } : { remove_host_port: true })
            }
          >
            Save server port
          </Button>
        </Row>
        <div className="space-y-2">
          <Label>Domains</Label>
          <div className="flex flex-wrap gap-2">
            {(s.domains ?? []).length === 0 && <span className="text-sm text-muted-foreground">No domains yet.</span>}
            {(s.domains ?? []).map((d) => (
              <span key={d} className="inline-flex items-center gap-1 rounded-md border bg-muted py-0.5 pr-1 pl-2 font-mono text-xs">
                {d}
                <Button variant="ghost" size="icon" className="size-6" aria-label={`Remove ${d}`} disabled={busy} onClick={() => ch.change(`Remove ${d}`, { remove_domains: [d] })}>
                  <X />
                </Button>
              </span>
            ))}
          </div>
          <Row>
            <Field id="domain" label="Add a domain" grow>
              <Input id="domain" value={domain} onChange={(e) => setDomain(e.target.value.toLowerCase())} placeholder="app.example.com" />
            </Field>
            <Button variant="outline" disabled={busy || !domain.trim()} onClick={() => ch.change(`Add ${domain.trim()}`, { add_domains: [domain.trim()] })}>
              <Plus /> Add domain
            </Button>
          </Row>
        </div>
      </Section>

      <Section title="Environment" description="Values are write-only: Coldship never shows them again. Setting an existing name replaces its value.">
        <div className="flex flex-wrap gap-2">
          {Object.keys(s.env ?? {}).length === 0 && <span className="text-sm text-muted-foreground">No variables.</span>}
          {Object.keys(s.env ?? {})
            .sort()
            .map((k) => (
              <span key={k} className="inline-flex items-center gap-1 rounded-md border bg-muted py-0.5 pr-1 pl-2 font-mono text-xs">
                {k}
                <Button variant="ghost" size="icon" className="size-6" aria-label={`Remove ${k}`} disabled={busy} onClick={() => ch.change(`Remove ${k}`, { unset_env: [k] })}>
                  <X />
                </Button>
              </span>
            ))}
        </div>
        <Row>
          <Field id="envkey" label="Name">
            <Input id="envkey" value={envKey} onChange={(e) => setEnvKey(e.target.value)} placeholder="API_KEY" className="font-mono sm:w-48" />
          </Field>
          <Field id="envval" label="Value" grow>
            <Input id="envval" type="password" autoComplete="off" value={envVal} onChange={(e) => setEnvVal(e.target.value)} className="font-mono" />
          </Field>
          <Button
            variant="outline"
            disabled={busy || !/^[A-Za-z_][A-Za-z0-9_]*$/.test(envKey)}
            onClick={() => {
              ch.change(`Set ${envKey}`, { env: { [envKey]: envVal } })
              setEnvVal('')
            }}
          >
            Set variable
          </Button>
        </Row>
      </Section>

      <Section
        title="Data services"
        description={
          <>
            Databases and storage the app connects to. Manage them in <Link to="/data" className="text-primary hover:underline">Data &amp; Storage</Link>.
          </>
        }
      >
        <div className="flex flex-wrap gap-2">
          {(s.uses ?? []).length === 0 && <span className="text-sm text-muted-foreground">None connected.</span>}
          {(s.uses ?? []).map((u) => {
            const name = u.split('=')[0]
            const d = data.data?.find((x) => !x.app && x.name === name)
            return (
              <span key={u} className="inline-flex items-center gap-2 rounded-lg border bg-card py-1 pr-1 pl-2 text-sm">
                {d && <DataMark engine={d.engine} size={20} />}
                <Link to={`/data/${name}`} className="font-medium hover:underline">
                  {u}
                </Link>
                <Button variant="ghost" size="icon" className="size-7" aria-label={`Disconnect ${name}`} disabled={busy} onClick={() => ch.change(`Disconnect ${name}`, { remove_uses: [u] })}>
                  <X />
                </Button>
              </span>
            )
          })}
        </div>
        {services.length > 0 && (
          <Row>
            <div className="flex-1 space-y-1.5">
              <Label htmlFor="use">Connect</Label>
              <Select value={use} onValueChange={setUse}>
                <SelectTrigger id="use" className="w-full">
                  <SelectValue placeholder="Pick a data service" />
                </SelectTrigger>
                <SelectContent>
                  {services.map((d) => (
                    <SelectItem key={d.name} value={d.name}>
                      {d.name} · {d.engine}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <Button variant="outline" disabled={busy || !use} onClick={() => ch.change(`Connect ${use}`, { add_uses: [use] })}>
              <Plus /> Connect
            </Button>
          </Row>
        )}
      </Section>

      <Section title="Resources and stage">
        <Row>
          <Field id="memory" label="Memory limit">
            <Input id="memory" value={memory} onChange={(e) => setMemory(e.target.value)} placeholder="unlimited, or 512m, 1g" className="sm:w-48" />
          </Field>
          <Button
            variant="outline"
            disabled={busy || memory === (s.memory ?? '')}
            onClick={() => ch.change(memory ? `Limit memory to ${memory}` : 'Remove the memory limit', memory ? { memory } : { remove_memory: true })}
          >
            Save
          </Button>
        </Row>
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border p-3">
          <div className="flex items-start gap-3">
            <Checkbox id="prod" checked={s.stage === 'production'} disabled aria-readonly />
            <div>
              <Label htmlFor="prod">Production</Label>
              <p className="text-sm text-muted-foreground">Destructive changes (by anyone, including agents) need an admin's approval.</p>
            </div>
          </div>
          <Button
            variant="outline"
            size="sm"
            disabled={busy || s.stage === 'preview'}
            onClick={() =>
              ch.change(s.stage === 'production' ? 'Move out of production' : 'Move to production', { stage: s.stage === 'production' ? 'staging' : 'production' })
            }
          >
            {s.stage === 'production' ? 'Move to staging' : 'Move to production'}
          </Button>
        </div>
      </Section>

      <MoveSection app={app} />

      <Card className="border-destructive/40">
        <CardHeader>
          <CardTitle>
            <h2 className="text-base font-semibold text-destructive">Delete app</h2>
          </CardTitle>
          <CardDescription>
            Deletes {app.name}, its containers, volumes and its own databases (those are snapshotted first). Data services it uses stay.
            {s.stage === 'production' && (
              <>
                {' '}
                <ToneBadge t="warn">production: needs approval</ToneBadge>
              </>
            )}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <Row>
            <Field id="confirm" label={`Type ${app.name} to confirm`} grow>
              <Input id="confirm" value={confirmName} onChange={(e) => setConfirmName(e.target.value)} autoComplete="off" />
            </Field>
            <Button
              variant="destructive"
              disabled={busy || confirmName !== app.name || me.scope === 'read'}
              onClick={() => ch.propose(`Delete ${app.name}`, () => api.planDelete(app.name))}
            >
              <Trash2 /> Delete
            </Button>
          </Row>
        </CardContent>
      </Card>
      {ch.deleting && <DeletedRedirect onGone={() => nav('/apps')} name={app.name} />}
    </div>
  )
}

// DeletedRedirect goes back to the app list once the app is gone.
function DeletedRedirect({ name, onGone }: { name: string; onGone: () => void }) {
  useLoad(
    () =>
      api.app(name).then(
        () => undefined,
        () => onGone(),
      ),
    [name],
    1500,
  )
  return null
}

// MoveSection moves the app to another organization the user is an admin of.
function MoveSection({ app }: { app: AppView }) {
  const me = useSession()
  const nav = useNavigate()
  const targets = (me.orgs ?? []).filter((o) => o.id !== me.org?.id && o.role === 'admin')
  const [to, setTo] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<Error>()
  if (!me.user || me.scope !== 'admin' || app.preview_of) return null
  const uses = app.spec?.uses ?? []

  async function move() {
    setBusy(true)
    setError(undefined)
    try {
      await api.moveApp(app.name, to)
      await api.switchOrg(to)
      nav(`/apps/${encodeURIComponent(app.name)}`)
      window.location.reload()
    } catch (e) {
      setError(e as Error)
      setBusy(false)
    }
  }

  return (
    <Section
      title="Organization"
      description={
        <>
          {app.name} belongs to <strong>{me.org?.name}</strong>. Moving it takes its previews and own databases along; the containers keep running.
        </>
      }
    >
      {error && <ErrorBox error={error} />}
      {targets.length === 0 ? (
        <p className="text-sm text-muted-foreground">You aren't an admin of any other organization.</p>
      ) : uses.length > 0 ? (
        <p className="text-sm text-muted-foreground">
          It uses data services from this organization ({uses.map((u) => u.split('=')[0]).join(', ')}): disconnect them above before moving it.
        </p>
      ) : (
        <Row>
          <Field id="moveto" label="Move to" grow>
            <Select value={to} onValueChange={setTo}>
              <SelectTrigger id="moveto" className="w-full">
                <SelectValue placeholder="Choose an organization" />
              </SelectTrigger>
              <SelectContent>
                {targets.map((o) => (
                  <SelectItem key={o.id} value={o.id}>
                    {o.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Button variant="outline" disabled={busy || !to} onClick={move}>
            <ArrowRightLeft /> Move app
          </Button>
        </Row>
      )}
    </Section>
  )
}
