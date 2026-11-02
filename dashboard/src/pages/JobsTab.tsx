import { Fragment, useState, type FormEvent } from 'react'
import { CalendarClock, Pencil, Play, Plus, Trash2 } from 'lucide-react'
import { api, type AppView, type Job, type JobRun, type JobStatus } from '@/api'
import { useSession } from '@/App'
import { timeAgo, timeUntil, useLoad } from '@/hooks'
import { Empty, ErrorBox, Mono, StatusDot, TableSkeleton } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

const presets: { label: string; cron: string }[] = [
  { label: 'Every 15 minutes', cron: '*/15 * * * *' },
  { label: 'Every hour', cron: '@hourly' },
  { label: 'Every day at 03:00 UTC', cron: '0 3 * * *' },
  { label: 'Every Monday at 06:00 UTC', cron: '0 6 * * 1' },
  { label: 'Every month', cron: '@monthly' },
]

export function RunStatus({ status }: { status: JobStatus }) {
  switch (status) {
    case 'succeeded':
      return <StatusDot dot="bg-ok" text="text-ok" label="Succeeded" />
    case 'running':
      return <StatusDot dot="bg-info animate-pulse" text="text-info" label="Running" />
    case 'skipped':
      return <StatusDot dot="bg-muted-foreground/40" text="text-muted-foreground" label="Skipped" />
    case 'interrupted':
      return <StatusDot dot="bg-warn" text="text-warn" label="Interrupted" />
    case 'timed_out':
      return <StatusDot dot="bg-destructive" text="text-destructive" label="Timed out" />
    default:
      return <StatusDot dot="bg-destructive" text="text-destructive" label="Failed" />
  }
}

function TableCard({ children }: { children: React.ReactNode }) {
  return <div className="overflow-x-auto rounded-xl border bg-card [&_td:first-child]:pl-4 [&_th:first-child]:pl-4">{children}</div>
}

// patchJobs plans a jobs-only change and applies it: it never redeploys.
async function patchJobs(app: string, patch: { set_jobs?: Job[]; remove_jobs?: string[] }, reason: string) {
  const p = await api.planPatch({ name: app, ...patch })
  if (p.requires_approval) throw new Error(`This change needs an admin's approval first: plan ${p.id}.`)
  if (p.action !== 'config' && p.action !== 'noop') throw new Error(`Unexpected plan (${p.action}): apply it from the CLI to review it.`)
  if (p.action === 'config') await api.apply(p.id, reason)
}

export function JobsTab({ app }: { app: AppView }) {
  const me = useSession()
  const canRun = me.scope !== 'read'
  const jobs = useLoad(() => api.jobs(app.name), [app.name], 5000)
  const runs = useLoad(() => api.jobRuns(app.name), [app.name], 5000)
  const [editing, setEditing] = useState<{ job?: Job }>()
  const [error, setError] = useState<Error>()
  const [busy, setBusy] = useState('')
  const [open, setOpen] = useState<string>()
  const isStatic = app.latest_deployment?.static

  async function act(key: string, f: () => Promise<unknown>) {
    setBusy(key)
    setError(undefined)
    try {
      await f()
      jobs.reload()
      runs.reload()
    } catch (e) {
      setError(e as Error)
    } finally {
      setBusy('')
    }
  }

  if (jobs.error) return <ErrorBox error={jobs.error} />
  if (!jobs.data) return <TableSkeleton rows={2} cols={5} />
  const list = jobs.data
  return (
    <div className="space-y-4">
      {isStatic && (
        <p className="rounded-md bg-warn-soft px-3 py-2 text-sm text-warn">
          This app is served as static files, with no image to run jobs in.
        </p>
      )}
      {error && <ErrorBox error={error} />}
      {list.length === 0 ? (
        <Empty title="No scheduled jobs" icon={<CalendarClock />}>
          Run a command on a schedule, like a cleanup every night, in a one-off container from this app's image with its env, databases and volumes.
          {canRun && (
            <div className="mt-4">
              <Button onClick={() => setEditing({})}>
                <Plus /> Add a job
              </Button>
            </div>
          )}
        </Empty>
      ) : (
        <>
          <TableCard>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Job</TableHead>
                  <TableHead>Schedule (UTC)</TableHead>
                  <TableHead>Next</TableHead>
                  <TableHead>Last run</TableHead>
                  <TableHead>
                    <span className="sr-only">Actions</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {list.map((j) => (
                  <TableRow key={j.name}>
                    <TableCell>
                      <div className="font-medium">{j.name}</div>
                      <Mono className="text-xs break-all text-muted-foreground">{j.command}</Mono>
                    </TableCell>
                    <TableCell>
                      <Mono>{j.schedule}</Mono>
                    </TableCell>
                    <TableCell className="text-muted-foreground">{j.next ? timeUntil(j.next) : '-'}</TableCell>
                    <TableCell>
                      {j.running ? (
                        <RunStatus status="running" />
                      ) : j.last ? (
                        <div className="flex flex-wrap items-center gap-2">
                          <RunStatus status={j.last.status} />
                          <span className="text-xs text-muted-foreground">{timeAgo(j.last.started_at)}</span>
                        </div>
                      ) : (
                        <span className="text-muted-foreground">never</span>
                      )}
                    </TableCell>
                    <TableCell className="text-right whitespace-nowrap">
                      {canRun && (
                        <>
                          <Button
                            variant="outline"
                            size="sm"
                            disabled={!!j.running || !!busy || isStatic}
                            onClick={() => act('run:' + j.name, () => api.runJob(app.name, j.name))}
                          >
                            <Play /> {busy === 'run:' + j.name ? 'Starting…' : 'Run now'}
                          </Button>
                          <Button variant="ghost" size="icon" aria-label={`Edit ${j.name}`} onClick={() => setEditing({ job: j })}>
                            <Pencil />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            aria-label={`Remove ${j.name}`}
                            disabled={!!busy}
                            onClick={() =>
                              confirm(`Remove the job ${j.name}?`) &&
                              act('rm:' + j.name, () => patchJobs(app.name, { remove_jobs: [j.name] }, `remove job ${j.name}, from the dashboard`))
                            }
                          >
                            <Trash2 />
                          </Button>
                        </>
                      )}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </TableCard>
          {canRun && (
            <Button variant="outline" onClick={() => setEditing({})}>
              <Plus /> Add a job
            </Button>
          )}
        </>
      )}

      {runs.data && runs.data.length > 0 && (
        <>
          <h3 className="pt-2 text-sm font-semibold">Recent runs</h3>
          <TableCard>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Job</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Started</TableHead>
                  <TableHead>Took</TableHead>
                  <TableHead>By</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {runs.data.map((r) => (
                  <Fragment key={r.id}>
                    <TableRow className="cursor-pointer" onClick={() => setOpen(open === r.id ? undefined : r.id)}>
                      <TableCell>
                        <button type="button" className="font-medium hover:underline" aria-expanded={open === r.id}>
                          {r.job}
                        </button>
                      </TableCell>
                      <TableCell>
                        <RunStatus status={r.status} />
                      </TableCell>
                      <TableCell className="text-muted-foreground">{timeAgo(r.started_at)}</TableCell>
                      <TableCell className="text-muted-foreground">{took(r)}</TableCell>
                      <TableCell className="text-muted-foreground">{r.trigger}</TableCell>
                    </TableRow>
                    {open === r.id && (
                      <TableRow>
                        <TableCell colSpan={5} className="bg-muted/30">
                          <RunLog app={app.name} id={r.id} />
                        </TableCell>
                      </TableRow>
                    )}
                  </Fragment>
                ))}
              </TableBody>
            </Table>
          </TableCard>
        </>
      )}

      {editing && (
        <JobDialog
          app={app.name}
          job={editing.job}
          taken={list.map((j) => j.name)}
          onClose={() => setEditing(undefined)}
          onSaved={() => {
            setEditing(undefined)
            jobs.reload()
          }}
        />
      )}
    </div>
  )
}

function took(r: JobRun) {
  if (!r.finished_at) return '-'
  const ms = new Date(r.finished_at).getTime() - new Date(r.started_at).getTime()
  return ms < 1000 ? `${ms} ms` : ms < 60000 ? `${(ms / 1000).toFixed(1)} s` : `${Math.round(ms / 60000)} min`
}

function RunLog({ app, id }: { app: string; id: string }) {
  const r = useLoad(() => api.jobRun(app, id), [app, id])
  if (r.error) return <ErrorBox error={r.error} />
  if (!r.data) return <Skeleton className="h-16 w-full" />
  const x = r.data
  return (
    <div className="space-y-2 py-1">
      <div className="text-xs text-muted-foreground">
        <Mono>{x.id}</Mono> · <Mono>{x.command}</Mono>
        {x.exit_code !== undefined && <> · exit code {x.exit_code}</>}
      </div>
      {x.error && x.status !== 'succeeded' && <p className="text-sm text-destructive">{x.error}</p>}
      <pre className="max-h-80 overflow-auto rounded-md bg-background p-3 font-mono text-xs whitespace-pre-wrap">{x.log || '(no output)'}</pre>
    </div>
  )
}

function JobDialog({
  app,
  job,
  taken,
  onClose,
  onSaved,
}: {
  app: string
  job?: Job
  taken: string[]
  onClose: () => void
  onSaved: () => void
}) {
  const [name, setName] = useState(job?.name ?? '')
  const [schedule, setSchedule] = useState(job?.schedule ?? '0 3 * * *')
  const [command, setCommand] = useState(job?.command ?? '')
  const [timeout, setTimeout_] = useState(job?.timeout ?? '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<Error>()
  const preset = presets.find((p) => p.cron === schedule)?.cron ?? 'custom'
  const clash = !job && taken.includes(name.trim())

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(undefined)
    try {
      const j: Job = { name: name.trim(), schedule: schedule.trim(), command: command.trim() }
      if (timeout.trim()) j.timeout = timeout.trim()
      await patchJobs(app, { set_jobs: [j] }, `${job ? 'change' : 'add'} job ${j.name}, from the dashboard`)
      onSaved()
    } catch (err) {
      setError(err as Error)
      setBusy(false)
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{job ? `Edit ${job.name}` : 'Add a job'}</DialogTitle>
          <DialogDescription>
            Runs with <Mono>sh -c</Mono> in a one-off container from the app's current image. Saving doesn't redeploy the app.
          </DialogDescription>
        </DialogHeader>
        <form className="grid gap-4" onSubmit={submit}>
          <div className="grid gap-2">
            <Label htmlFor="job-name">Name</Label>
            <Input id="job-name" value={name} disabled={!!job} onChange={(e) => setName(e.target.value)} placeholder="cleanup" className="font-mono" />
            {clash && <p className="text-xs text-destructive">There's already a job named {name.trim()}.</p>}
          </div>
          <div className="grid gap-2">
            <Label htmlFor="job-cmd">Command</Label>
            <Input id="job-cmd" value={command} onChange={(e) => setCommand(e.target.value)} placeholder="npm run cleanup" className="font-mono" />
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="grid gap-2">
              <Label htmlFor="job-when">When</Label>
              <Select value={preset} onValueChange={(v) => v !== 'custom' && setSchedule(v)}>
                <SelectTrigger id="job-when" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {presets.map((p) => (
                    <SelectItem key={p.cron} value={p.cron}>
                      {p.label}
                    </SelectItem>
                  ))}
                  <SelectItem value="custom">Custom</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-2">
              <Label htmlFor="job-cron">Cron (UTC)</Label>
              <Input id="job-cron" value={schedule} onChange={(e) => setSchedule(e.target.value)} className="font-mono" />
            </div>
          </div>
          <div className="grid gap-2">
            <Label htmlFor="job-timeout">
              Timeout <span className="font-normal text-muted-foreground">(optional, default 30m)</span>
            </Label>
            <Input id="job-timeout" value={timeout} onChange={(e) => setTimeout_(e.target.value)} placeholder="10m" className="font-mono" />
          </div>
          {error && <ErrorBox error={error} />}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={busy || !name.trim() || !command.trim() || !schedule.trim() || clash}>
              {busy ? 'Saving…' : 'Save'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
