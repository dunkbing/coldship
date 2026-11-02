import { SettingsTab } from './AppSettings'
import { MetricsTab } from './MetricsTab'
import { JobsTab } from './JobsTab'
import { LiveLog } from '../components/LiveLog'
import { useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { ExternalLink, Info } from 'lucide-react'
import { api, type AppView, type Deployment } from '../api'
import { useSession } from '../App'
import { fmtTime, timeUntil, useLoad } from '../hooks'
import { cn } from '@/lib/utils'
import {
  DeployStatus,
  Empty,
  ErrorBox,
  GitIcon,
  HealthPill,
  healthOf,
  Mono,
  PageHeader,
  PageSkeleton,
  shortRepo,
  StageBadge,
  TableSkeleton,
  Time,
  ToneBadge,
  type Tone,
} from '@/components/common'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

type Tab = 'diagnose' | 'metrics' | 'deployments' | 'logs' | 'jobs' | 'databases' | 'webhook' | 'settings'

// Shared look for log boxes.
const logBox =
  'max-h-96 overflow-auto rounded-lg border bg-muted p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap break-words'

// Wraps a table in a bordered card; Table scrolls horizontally itself.
function TableCard({ children }: { children: ReactNode }) {
  return <div className="overflow-hidden rounded-xl border bg-card [&_td:first-child]:pl-4 [&_th:first-child]:pl-4">{children}</div>
}

function Chip({ children, title }: { children: ReactNode; title?: string }) {
  return (
    <span title={title} className="inline-flex max-w-full rounded-md bg-muted px-1.5 py-0.5 font-mono text-xs break-all">
      {children}
    </span>
  )
}

function Chips({ children }: { children: ReactNode }) {
  return <span className="flex flex-wrap gap-1">{children}</span>
}

function SectionTitle({ children }: { children: ReactNode }) {
  return <h2 className="mt-8 mb-3 text-lg font-semibold tracking-tight">{children}</h2>
}

function Note({ children }: { children: ReactNode }) {
  return <p className="mt-3 text-sm text-muted-foreground">{children}</p>
}

const kv = 'grid grid-cols-[max-content_minmax(0,1fr)] gap-x-4 gap-y-2 text-sm [&_dt]:text-muted-foreground [&_dd]:min-w-0 [&_dd]:break-words'

export default function AppDetail() {
  const { name = '' } = useParams()
  const [params, setParams] = useSearchParams()
  const tab = (params.get('tab') as Tab) || 'diagnose'
  const app = useLoad(() => api.app(name), [name], 5000)
  const me = useSession()

  if (app.error) return <ErrorBox error={app.error} />
  if (!app.data) return <PageSkeleton kind="detail" />
  const a = app.data
  const tabs = [
    'diagnose',
    'metrics',
    'deployments',
    'logs',
    ...(!a.preview_of ? ['jobs'] : []),
    ...(a.databases?.length ? ['databases'] : []),
    ...(a.spec?.build && !a.preview_of ? ['webhook'] : []),
    ...(me.scope !== 'read' && !a.preview_of ? ['settings'] : []),
  ] as Tab[]

  return (
    <>
      <PageHeader
        crumbs={
          <>
            <Link to="/apps" className="hover:text-foreground hover:underline">
              Apps
            </Link>{' '}
            / <span>{a.name}</span>
          </>
        }
        title={
          <>
            <span className="break-all">{a.name}</span> <StageBadge stage={a.spec?.stage} /> <HealthPill health={healthOf(a)} />
          </>
        }
        description={
          <>
            <p>
              Current deployment <Mono>{a.current_deployment || 'none'}</Mono> · updated <Time iso={a.updated_at} />
            </p>
            {a.urls && a.urls.length > 0 && (
              <p className="mt-1 flex flex-wrap gap-x-3 gap-y-1">
                {a.urls.map((u) => (
                  <a
                    key={u}
                    href={u}
                    target="_blank"
                    rel="noreferrer"
                    className="inline-flex items-center gap-1 break-all text-primary hover:underline"
                  >
                    {u.replace(/^https:\/\//, '')}
                    <ExternalLink className="size-3.5 shrink-0" aria-hidden />
                  </a>
                ))}
              </p>
            )}
          </>
        }
        actions={<RollbackButton app={a} onDone={app.reload} />}
      />

      {a.preview_of && <PreviewBanner a={a} />}

      {a.latest_deployment && (a.latest_deployment.status === 'building' || a.latest_deployment.status === 'deploying') && (
        <Card className="mb-4">
          <CardHeader>
            <CardTitle className="break-all">Deploying {a.latest_deployment.id}</CardTitle>
          </CardHeader>
          <CardContent>
            <LiveLog deployment={a.latest_deployment.id} onEnd={() => app.reload()} />
          </CardContent>
        </Card>
      )}

      <div className="mb-6 grid gap-4 md:grid-cols-2">
        <SpecCard a={a} />
        <RuntimeCard a={a} />
      </div>

      <Tabs value={tab} onValueChange={(t) => setParams({ tab: t })} className="gap-4">
        <TabsList className="max-w-full justify-start overflow-x-auto">
          {tabs.map((t) => (
            <TabsTrigger key={t} value={t} className="flex-none px-3">
              {t[0].toUpperCase() + t.slice(1)}
            </TabsTrigger>
          ))}
        </TabsList>
        <TabsContent value="diagnose">
          <DiagnoseTab name={name} />
        </TabsContent>
        <TabsContent value="metrics">
          <MetricsTab app={a} />
        </TabsContent>
        <TabsContent value="deployments">
          <DeploymentsTab name={name} />
        </TabsContent>
        <TabsContent value="logs">
          <LogsTab name={name} />
        </TabsContent>
        <TabsContent value="jobs">
          <JobsTab app={a} />
        </TabsContent>
        <TabsContent value="databases">
          <DatabasesTab app={a} onChange={app.reload} />
        </TabsContent>
        <TabsContent value="webhook">
          <WebhookTab name={name} />
        </TabsContent>
        <TabsContent value="settings">
          <SettingsTab app={a} onChange={app.reload} />
        </TabsContent>
      </Tabs>
    </>
  )
}

function PreviewBanner({ a }: { a: AppView }) {
  const me = useSession()
  const nav = useNavigate()
  const [confirming, setConfirming] = useState(false)
  const [error, setError] = useState<Error>()
  async function del() {
    try {
      await api.deletePreview(a.name)
      nav('/apps')
    } catch (e) {
      setError(e as Error)
    }
  }
  return (
    <Alert className="mb-4 border-info/30 bg-info-soft">
      <Info className="text-info" />
      <AlertTitle className="font-normal">
        Preview of{' '}
        <Link to={`/apps/${a.preview_of}`} className="font-medium">
          {a.preview_of}
        </Link>
        . It is deleted automatically{' '}
        <strong title={fmtTime(a.expires_at)}>{a.expires_at ? timeUntil(a.expires_at) : 'soon'}</strong>, with its volumes.
      </AlertTitle>
      <AlertDescription className="text-foreground">
        {error && <ErrorBox error={error} />}
        {me.scope !== 'read' && (
          <div className="mt-2 flex flex-wrap items-center gap-2">
            {!confirming ? (
              <Button variant="outline" size="sm" onClick={() => setConfirming(true)}>
                Delete preview…
              </Button>
            ) : (
              <>
                <Button variant="destructive" size="sm" onClick={del}>
                  Delete {a.name}
                </Button>
                <Button variant="ghost" size="sm" onClick={() => setConfirming(false)}>
                  Cancel
                </Button>
              </>
            )}
          </div>
        )}
      </AlertDescription>
    </Alert>
  )
}

function SpecCard({ a }: { a: AppView }) {
  const s = a.spec
  if (!s)
    return (
      <Card>
        <CardContent className="text-muted-foreground">No successful deployment yet.</CardContent>
      </Card>
    )
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2>Spec</h2>
        </CardTitle>
      </CardHeader>
      <CardContent>
        <dl className={kv}>
          {s.build ? (
            <>
              <dt>Repository</dt>
              <dd>
                <a
                  href={s.build.git.startsWith('http') ? s.build.git : undefined}
                  className="inline-flex items-center gap-1.5 hover:underline"
                  target="_blank"
                  rel="noreferrer"
                >
                  <GitIcon />
                  <Mono className="break-all">{shortRepo(s.build.git)}</Mono>
                </a>
              </dd>
              <dt>Ref</dt>
              <dd>
                <Mono>{s.build.ref || 'default branch'}</Mono>
                {s.build.commit && <span className="font-mono text-xs text-muted-foreground"> · {s.build.commit.slice(0, 12)}</span>}
              </dd>
              <dt>Built with</dt>
              <dd>
                {a.latest_deployment?.detected ? (
                  <span title="Coldship generated the Dockerfile; override with build.auto">
                    generated Dockerfile · <Mono>{a.latest_deployment.detected}</Mono>
                  </span>
                ) : s.build.builder === 'auto' ? (
                  'generated Dockerfile'
                ) : (
                  <Mono className="break-all">
                    {s.build.context === '.' || !s.build.context ? s.build.dockerfile : `${s.build.context}/${s.build.dockerfile}`}
                  </Mono>
                )}
              </dd>
              {s.build.args && Object.keys(s.build.args).length > 0 && (
                <>
                  <dt>Build args</dt>
                  <dd>
                    <Chips>
                      {Object.entries(s.build.args).map(([k, v]) => (
                        <Chip key={k}>
                          {k}={v}
                        </Chip>
                      ))}
                    </Chips>
                  </dd>
                </>
              )}
            </>
          ) : (
            <>
              <dt>Image</dt>
              <dd>
                <Mono className="break-all">{s.image}</Mono>
              </dd>
            </>
          )}
          <dt>Port</dt>
          <dd>{s.port ? (s.host_port ? `${s.host_port} → ${s.port}` : s.port) : '—'}</dd>
          <dt>Domains</dt>
          <dd>
            {s.domains?.length ? (
              <Chips>
                {s.domains.map((d) => (
                  <Chip key={d}>{d}</Chip>
                ))}
              </Chips>
            ) : (
              '—'
            )}
          </dd>
          <dt>Health check</dt>
          <dd>
            {s.port ? (
              <>
                {s.health?.path ? <Mono>GET {s.health.path}</Mono> : 'port accepts connections'}
                <span className="text-muted-foreground"> · within {s.health?.timeout ?? '60s'}</span>
              </>
            ) : (
              'container stays up'
            )}
          </dd>
          <dt>Deploys</dt>
          <dd>{s.host_port || s.volumes?.length ? 'stop and replace (brief downtime)' : 'zero downtime'}</dd>
          <dt>Memory</dt>
          <dd>{s.memory ?? 'unlimited'}</dd>
          <dt>Env</dt>
          <dd>
            {s.env && Object.keys(s.env).length ? (
              <Chips>
                {Object.keys(s.env)
                  .sort()
                  .map((k) => (
                    <Chip key={k} title="Value redacted">
                      {k}
                    </Chip>
                  ))}
              </Chips>
            ) : (
              '—'
            )}
          </dd>
          <dt>Databases</dt>
          <dd>
            {s.databases?.length ? (
              <Chips>
                {s.databases.map((d) => (
                  <Chip key={d.name} title={`connection URL injected as ${d.env}`}>
                    {d.name} · {d.engine} {d.version} → {d.env}
                  </Chip>
                ))}
              </Chips>
            ) : (
              '—'
            )}
          </dd>
          <dt>Data services</dt>
          <dd>
            {s.uses?.length ? (
              <Chips>
                {s.uses.map((u) => {
                  const [name, env] = u.split('=')
                  return (
                    <Link key={u} to={`/data/${name}`} className="hover:underline">
                      <Chip title={env ? `URL injected as ${env}` : 'connection env injected'}>
                        {name}
                        {env ? ` → ${env}` : ''}
                      </Chip>
                    </Link>
                  )
                })}
              </Chips>
            ) : (
              '—'
            )}
          </dd>
          <dt>Volumes</dt>
          <dd>
            {s.volumes?.length
              ? s.volumes.map((v) => (
                  <div key={v.name}>
                    <Mono>{v.name}</Mono> <span className="text-muted-foreground">at</span> <Mono>{v.path}</Mono>
                  </div>
                ))
              : '—'}
          </dd>
        </dl>
      </CardContent>
    </Card>
  )
}

function RuntimeCard({ a }: { a: AppView }) {
  const rt = a.runtime
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2>Container</h2>
        </CardTitle>
      </CardHeader>
      <CardContent>
        {!rt || !rt.exists ? (
          <p className="text-muted-foreground">No container is running for this app.</p>
        ) : (
          <dl className={kv}>
            <dt>State</dt>
            <dd>{rt.state}</dd>
            <dt>Restarts</dt>
            <dd className={cn(rt.restart_count > 0 && 'font-medium text-destructive')}>{rt.restart_count}</dd>
            <dt>Exit code</dt>
            <dd>{rt.exit_code}</dd>
            <dt>OOM killed</dt>
            <dd className={cn(rt.oom_killed && 'font-medium text-destructive')}>{rt.oom_killed ? 'yes' : 'no'}</dd>
            <dt>Started</dt>
            <dd title={fmtTime(rt.started_at)}>
              <Time iso={rt.started_at} />
            </dd>
          </dl>
        )}
      </CardContent>
    </Card>
  )
}

function RollbackButton({ app, onDone }: { app: AppView; onDone: () => void }) {
  const me = useSession()
  const [open, setOpen] = useState(false)
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<Error>()
  const [msg, setMsg] = useState('')
  if (me.scope === 'read') return null

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(undefined)
    try {
      const r = await api.rollback(app.name, reason.trim())
      setMsg(`Rolled back: ${r.deployment?.id ?? ''} is ${r.deployment?.status ?? r.message}`)
      setOpen(false)
      setReason('')
      onDone()
    } catch (err) {
      setError(err as Error)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex flex-col items-end gap-2">
      {msg && (
        <span className="text-sm text-ok" role="status">
          {msg}
        </span>
      )}
      {!open ? (
        <Button variant="outline" onClick={() => setOpen(true)}>
          Roll back…
        </Button>
      ) : (
        <Card size="sm" className="w-full max-w-sm">
          <CardContent>
            <form className="space-y-3" onSubmit={submit}>
              <div className="space-y-1.5">
                <Label htmlFor="rollback-reason">Reason (recorded in the audit log)</Label>
                <Input
                  id="rollback-reason"
                  value={reason}
                  onChange={(e) => setReason(e.target.value)}
                  placeholder="e.g. new image crash-loops"
                  autoFocus
                />
              </div>
              {error && <ErrorBox error={error} />}
              <div className="flex flex-wrap gap-2">
                <Button type="submit" disabled={busy || !reason.trim()}>
                  {busy ? 'Rolling back…' : 'Roll back to last good'}
                </Button>
                <Button type="button" variant="ghost" onClick={() => setOpen(false)}>
                  Cancel
                </Button>
              </div>
            </form>
          </CardContent>
        </Card>
      )}
    </div>
  )
}

const sevTone: Record<string, Tone> = { info: 'info', warning: 'warn', error: 'danger' }
const sevBorder: Record<string, string> = { info: 'border-l-info', warning: 'border-l-warn', error: 'border-l-destructive' }

function DiagnoseTab({ name }: { name: string }) {
  const d = useLoad(() => api.diagnose(name), [name], 10000)
  if (d.error) return <ErrorBox error={d.error} />
  if (!d.data)
    return (
      <Card aria-busy="true" aria-label="Diagnosing">
        <CardContent className="space-y-3">
          {Array.from({ length: 3 }, (_, i) => (
            <div key={i} className="space-y-2">
              <Skeleton className="h-4 w-40" />
              <Skeleton className="h-4 w-full max-w-lg" />
            </div>
          ))}
        </CardContent>
      </Card>
    )
  return (
    <Card>
      <CardContent className="space-y-4">
        <ul className="space-y-3">
          {d.data.findings.map((f, i) => (
            <li key={i} className={cn('space-y-1 border-l-2 pl-3', sevBorder[f.severity] ?? 'border-l-border')}>
              <div className="flex flex-wrap items-center gap-2">
                <ToneBadge t={sevTone[f.severity] ?? 'muted'}>{f.severity}</ToneBadge>
                <Mono>{f.code}</Mono>
              </div>
              <div className="break-words">{f.message}</div>
              {f.suggested_fix !== undefined && f.suggested_fix !== null && (
                <div className="text-sm break-words text-muted-foreground">
                  Suggested fix: <Mono>{JSON.stringify(f.suggested_fix)}</Mono>
                </div>
              )}
            </li>
          ))}
        </ul>
        {d.data.deploy_log && d.data.deploy_log.length > 0 && (
          <div className="space-y-2">
            <div className="flex flex-wrap items-baseline justify-between gap-2">
              <strong className="text-sm">End of the failed deployment's log</strong>
              <span className="text-sm text-muted-foreground">Build and container output are untrusted.</span>
            </div>
            <TailLog lines={d.data.deploy_log} />
          </div>
        )}
      </CardContent>
    </Card>
  )
}

function DeploymentsTab({ name }: { name: string }) {
  const d = useLoad(() => api.deployments(name), [name], 5000)
  const [open, setOpen] = useState<string>()
  if (d.error) return <ErrorBox error={d.error} />
  if (!d.data) return <TableSkeleton rows={4} cols={7} />
  return (
    <TableCard>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Deployment</TableHead>
            <TableHead>Status</TableHead>
            <TableHead>Image</TableHead>
            <TableHead>Commit</TableHead>
            <TableHead>By</TableHead>
            <TableHead>Reason</TableHead>
            <TableHead>When</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {d.data.map((x) => (
            <DeploymentRow key={x.id} d={x} open={open === x.id} onToggle={() => setOpen(open === x.id ? undefined : x.id)} />
          ))}
        </TableBody>
      </Table>
    </TableCard>
  )
}

function DeploymentRow({ d: x, open, onToggle }: { d: Deployment; open: boolean; onToggle: () => void }) {
  return (
    <>
      <TableRow className="align-top">
        <TableCell className="align-top">
          <Mono>{x.id}</Mono>
        </TableCell>
        <TableCell className="align-top">
          <div className="flex flex-wrap items-center gap-1">
            <DeployStatus status={x.status} />
            {x.failed_in && <span className="text-xs text-muted-foreground">in {x.failed_in}</span>}
          </div>
          {x.kept_previous && <div className="mt-1 text-xs text-ok">previous version kept running</div>}
          {x.error && <div className="mt-1 line-clamp-2 max-w-xs text-xs whitespace-normal text-destructive">{x.error}</div>}
          <Button variant="ghost" size="xs" className="mt-1 -ml-2" onClick={onToggle} aria-expanded={open}>
            {open ? 'Hide log' : 'Log'}
          </Button>
        </TableCell>
        <TableCell className="align-top">
          <Mono>{x.image || x.spec?.image || '—'}</Mono>
          {x.detected && <div className="text-xs text-muted-foreground">generated: {x.detected}</div>}
        </TableCell>
        <TableCell className="align-top">{x.commit ? <Mono>{x.commit.slice(0, 7)}</Mono> : '—'}</TableCell>
        <TableCell className="align-top">{x.actor}</TableCell>
        <TableCell className="min-w-48 align-top whitespace-normal">{x.reason}</TableCell>
        <TableCell className="align-top">
          <Time iso={x.created_at} />
        </TableCell>
      </TableRow>
      {open && (
        <TableRow className="hover:bg-transparent">
          <TableCell colSpan={7} className="bg-muted/30 whitespace-normal">
            <DeployLogView deployment={x.id} />
          </TableCell>
        </TableRow>
      )}
    </>
  )
}

// TailLog is a log viewer that starts scrolled to the end, where the failing
// step of a build is.
function TailLog({ lines }: { lines?: string[] }) {
  const ref = useRef<HTMLPreElement>(null)
  useEffect(() => {
    if (ref.current) ref.current.scrollTop = ref.current.scrollHeight
  }, [lines])
  if (!lines) return <Skeleton className="h-48 w-full rounded-lg" />
  return (
    <pre className={logBox} ref={ref}>
      {lines.join('\n') || '(empty)'}
    </pre>
  )
}

function DeployLogView({ deployment }: { deployment: string }) {
  const l = useLoad(() => api.deployLog(deployment), [deployment])
  if (l.error) return <ErrorBox error={l.error} />
  return (
    <div className="space-y-2">
      {l.data?.note && <div className="text-xs text-muted-foreground">{l.data.note}</div>}
      <TailLog lines={l.data?.lines} />
    </div>
  )
}

const resultTone: Record<string, Tone> = {
  deployed: 'ok',
  preview: 'ok',
  preview_deleted: 'muted',
  planned: 'warn',
  queued: 'warn',
  deploying: 'info',
  failed: 'danger',
  superseded: 'muted',
  ignored: 'muted',
}

function WebhookTab({ name }: { name: string }) {
  const me = useSession()
  const w = useLoad(() => api.webhook(name), [name], 5000)
  const [secret, setSecret] = useState<string>()
  const [error, setError] = useState<Error>()
  const [confirm, setConfirm] = useState<'rotate' | 'disable'>()
  async function run(fn: () => Promise<{ secret?: string } | unknown>) {
    setError(undefined)
    try {
      const r = (await fn()) as { secret?: string }
      setSecret(r?.secret)
      setConfirm(undefined)
      w.reload()
    } catch (e) {
      setError(e as Error)
    }
  }
  if (w.error) return <ErrorBox error={w.error} />
  if (!w.data)
    return (
      <div className="space-y-6" aria-busy="true" aria-label="Loading webhook">
        <Skeleton className="h-32 rounded-xl" />
        <TableSkeleton rows={3} cols={6} />
      </div>
    )
  const info = w.data
  const admin = me.scope === 'admin'
  return (
    <>
      <Card>
        <CardContent className="space-y-3">
          <div className="flex flex-wrap items-start justify-between gap-4">
            <div className="min-w-0 space-y-1">
              <h2 className="flex flex-wrap items-center gap-2 text-base font-medium">
                Git webhook {info.enabled ? <ToneBadge t="ok">enabled</ToneBadge> : <ToneBadge t="muted">off</ToneBadge>}
              </h2>
              {info.enabled && (
                <div className="text-sm break-all">
                  Payload URL <Mono>{info.url}</Mono>
                </div>
              )}
              <div className="text-sm text-muted-foreground">
                {info.on_push ? (
                  <>
                    Pushes to <Mono>{info.ref || 'the default branch'}</Mono>{' '}
                    {info.on_push.action === 'deploy' ? 'deploy automatically' : 'create a plan to review'}
                    {info.on_push.previews ? '; other branches get previews' : ''}.
                  </>
                ) : (
                  <>The spec has no on_push section, so pushes are recorded but do nothing.</>
                )}
              </div>
            </div>
            {admin && (
              <div className="flex flex-wrap items-center gap-2">
                {!info.enabled && (
                  <Button size="sm" onClick={() => run(() => api.enableWebhook(name))}>
                    Enable
                  </Button>
                )}
                {info.enabled && !confirm && (
                  <>
                    <Button variant="outline" size="sm" onClick={() => setConfirm('rotate')}>
                      Rotate secret…
                    </Button>
                    <Button variant="ghost" size="sm" onClick={() => setConfirm('disable')}>
                      Disable…
                    </Button>
                  </>
                )}
                {confirm === 'rotate' && (
                  <Button variant="destructive" size="sm" className="h-auto min-h-7 whitespace-normal" onClick={() => run(() => api.enableWebhook(name, true))}>
                    Yes, rotate (the old secret stops working)
                  </Button>
                )}
                {confirm === 'disable' && (
                  <Button variant="destructive" size="sm" onClick={() => run(() => api.disableWebhook(name))}>
                    Yes, disable
                  </Button>
                )}
                {confirm && (
                  <Button variant="ghost" size="sm" onClick={() => setConfirm(undefined)}>
                    Cancel
                  </Button>
                )}
              </div>
            )}
          </div>
          {secret && (
            <Alert className="border-warn/30 bg-warn-soft">
              <AlertTitle className="font-normal break-all">
                <strong>Secret (shown once):</strong> <Mono>{secret}</Mono>
              </AlertTitle>
              <AlertDescription>
                Add the payload URL and this secret to your Git host's webhook settings, with content type application/json and push
                events.
              </AlertDescription>
            </Alert>
          )}
          {error && <ErrorBox error={error} />}
        </CardContent>
      </Card>

      <SectionTitle>Deliveries</SectionTitle>
      {info.deliveries.length === 0 ? (
        <Empty title="No pushes received yet." />
      ) : (
        <TableCard>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>When</TableHead>
                <TableHead>Ref</TableHead>
                <TableHead>Commit</TableHead>
                <TableHead>By</TableHead>
                <TableHead>Result</TableHead>
                <TableHead>Detail</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {info.deliveries.map((d) => (
                <TableRow key={d.id}>
                  <TableCell>
                    <Time iso={d.created_at} />
                  </TableCell>
                  <TableCell>{d.ref || '—'}</TableCell>
                  <TableCell>{d.commit ? <Mono>{d.commit.slice(0, 7)}</Mono> : '—'}</TableCell>
                  <TableCell>{d.pusher || '—'}</TableCell>
                  <TableCell>
                    <ToneBadge t={resultTone[d.result] ?? 'muted'}>{d.result.replace('_', ' ')}</ToneBadge>
                  </TableCell>
                  <TableCell className="min-w-48 text-sm whitespace-normal">
                    {d.detail}
                    {d.result === 'planned' && d.plan && (
                      <div>
                        <Link to="/approvals" className="text-primary hover:underline">
                          Review plan
                        </Link>
                      </div>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableCard>
      )}
      <Note>{info.note}</Note>
    </>
  )
}

function DatabasesTab({ app, onChange }: { app: AppView; onChange: () => void }) {
  const me = useSession()
  const snaps = useLoad(() => api.snapshots(app.name), [app.name], 15000)
  const [busy, setBusy] = useState<string>()
  const [error, setError] = useState<Error>()
  async function snapshot(db: string) {
    setBusy(db)
    setError(undefined)
    try {
      await api.snapshot(app.name, db)
      snaps.reload()
      onChange()
    } catch (e) {
      setError(e as Error)
    } finally {
      setBusy(undefined)
    }
  }
  return (
    <>
      {error && <ErrorBox error={error} />}
      <TableCard>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Database</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Injected as</TableHead>
              <TableHead>Host</TableHead>
              <TableHead>Latest snapshot</TableHead>
              <TableHead>
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {app.databases?.map((d) => (
              <TableRow key={d.name}>
                <TableCell>
                  <strong className="font-medium">{d.name}</strong>
                  <div className="text-xs text-muted-foreground">
                    {d.engine} {d.version}
                  </div>
                </TableCell>
                <TableCell>
                  <HealthPill health={d.status.running ? 'healthy' : d.status.exists ? 'failing' : 'stopped'} />
                </TableCell>
                <TableCell>
                  <Mono>{d.env}</Mono>
                </TableCell>
                <TableCell>
                  <Mono>{d.host}</Mono>
                </TableCell>
                <TableCell>
                  {d.last_snapshot ? <Time iso={d.last_snapshot.created_at} /> : <span className="text-muted-foreground">none yet</span>}
                </TableCell>
                <TableCell className="text-right">
                  {me.scope !== 'read' && (
                    <Button variant="outline" size="sm" disabled={busy === d.name} onClick={() => snapshot(d.name)}>
                      {busy === d.name ? 'Snapshotting…' : 'Snapshot now'}
                    </Button>
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </TableCard>
      <Note>
        Coldship snapshots every database daily and before any change that would delete data. Credentials are never shown; the app
        receives the connection URL in its env.
      </Note>

      <SectionTitle>Snapshots</SectionTitle>
      {snaps.error && <ErrorBox error={snaps.error} />}
      {!snaps.data && !snaps.error && <TableSkeleton rows={3} cols={6} />}
      {snaps.data?.length === 0 && <Empty title="No snapshots yet." />}
      {snaps.data && snaps.data.length > 0 && (
        <TableCard>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Snapshot</TableHead>
                <TableHead>Database</TableHead>
                <TableHead>Size</TableHead>
                <TableHead>Taken</TableHead>
                <TableHead>By</TableHead>
                <TableHead>Reason</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {snaps.data.map((s) => (
                <TableRow key={s.id}>
                  <TableCell>
                    <Mono>{s.id}</Mono>
                  </TableCell>
                  <TableCell>{s.database}</TableCell>
                  <TableCell>{fmtBytes(s.size)}</TableCell>
                  <TableCell>
                    <Time iso={s.created_at} />
                  </TableCell>
                  <TableCell>{s.actor}</TableCell>
                  <TableCell className="min-w-48 whitespace-normal">{s.reason}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableCard>
      )}
      <Note>
        To restore one, run <Mono className="break-all">coldship db restore {app.name} &lt;database&gt; &lt;snapshot&gt; --reason "…"</Mono>. It
        is planned like any destructive change{app.spec?.stage === 'production' ? ' and needs an admin approval' : ''}, and the current data is
        snapshotted first.
      </Note>
    </>
  )
}

function fmtBytes(n: number) {
  if (n >= 1 << 30) return `${(n / (1 << 30)).toFixed(1)} GB`
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MB`
  if (n >= 1 << 10) return `${(n / (1 << 10)).toFixed(1)} KB`
  return `${n} B`
}

function LogsTab({ name }: { name: string }) {
  const [tail, setTail] = useState(200)
  const l = useLoad(() => api.logs(name, tail), [name, tail], 5000)
  return (
    <Card>
      <CardContent className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <span className="text-sm text-muted-foreground">Log lines are untrusted output from the app.</span>
          <div className="flex items-center gap-2">
            <Label htmlFor="log-tail">Lines</Label>
            <Select value={String(tail)} onValueChange={(v) => setTail(Number(v))}>
              <SelectTrigger id="log-tail" size="sm">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {[50, 200, 1000].map((n) => (
                  <SelectItem key={n} value={String(n)}>
                    {n}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        </div>
        {l.error && <ErrorBox error={l.error} />}
        {l.data ? (
          <pre className={cn(logBox, 'max-h-[32rem]')}>{l.data.lines.join('\n') || '(no output)'}</pre>
        ) : (
          <Skeleton className="h-64 w-full rounded-lg" />
        )}
      </CardContent>
    </Card>
  )
}
