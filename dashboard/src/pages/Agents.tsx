import { useEffect, useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Bell, Bot, MessageSquarePlus, MessagesSquare, Pencil, Plus, ShieldCheck, Trash2, X } from 'lucide-react'
import { api, type Agent, type AgentPreset, type AgentSession, type Channel, type Scope } from '../api'
import { useSession } from '../App'
import { useLoad } from '../hooks'
import { CardsSkeleton, Empty, ErrorBox, Mono, PageHeader, StatusDot, Time, ToneBadge } from '@/components/common'
import { AgentMark, agentBlurbs, agentLabels } from '@/components/agent'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { notify } from '@/components/toasts'
import { paletteKeys } from '@/components/CommandPalette'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

const kinds = ['claude', 'codex', 'gemini', 'custom']

export const alertLabels: Record<string, string> = {
  'app.down': 'An app goes down',
  'deploy.failed': 'A deploy or build fails',
  'job.failed': 'A scheduled job fails',
  'backup.failing': 'Off-site backups fail',
  'disk.low': "The server's disk is almost full",
}

export function SessionStatusDot({ status }: { status: AgentSession['status'] }) {
  switch (status) {
    case 'ready':
      return <StatusDot dot="bg-ok" text="text-ok" label="Ready" />
    case 'working':
      return <StatusDot dot="bg-info animate-pulse" text="text-info" label="Working" />
    case 'starting':
      return <StatusDot dot="bg-warn animate-pulse" text="text-warn" label="Starting" />
    case 'failed':
      return <StatusDot dot="bg-destructive" text="text-destructive" label="Failed" />
    default:
      return <StatusDot dot="bg-muted-foreground/40" text="text-muted-foreground" label="Ended" />
  }
}

export default function Agents() {
  const me = useSession()
  const nav = useNavigate()
  const admin = me.scope === 'admin'
  const canRun = me.scope !== 'read'
  const list = useLoad(api.agents, [me.org?.id], 10000)
  const sessions = useLoad(api.sessions, [me.org?.id], 5000)
  const [editing, setEditing] = useState<{ kind: string; agent?: Agent }>()
  const [removing, setRemoving] = useState<Agent>()
  const [error, setError] = useState<Error>()
  const [starting, setStarting] = useState('')

  async function start(name: string) {
    setStarting(name)
    setError(undefined)
    try {
      const s = await api.startSession(name)
      nav(`/agents/sessions/${s.id}`)
    } catch (e) {
      setError(e as Error)
      setStarting('')
    }
  }

  const agents = list.data?.agents ?? []
  return (
    <>
      <PageHeader
        title="Agents"
        description="AI agents that look after this server with you. Each session runs the agent in its own container with Coldship's tools and the role you give it; it asks before it acts, and production changes still wait for an admin."
        actions={
          admin && agents.length > 0 ? (
            <Button onClick={() => setEditing({ kind: 'claude' })}>
              <Plus /> Connect agent
            </Button>
          ) : undefined
        }
      />
      {(list.error || error) && <ErrorBox error={(list.error || error)!} />}
      {list.loading && !list.data && <CardsSkeleton count={2} height="h-24" />}

      {list.data && agents.length === 0 && (
        <>
          {!admin && (
            <Empty title="No agents yet" icon={<Bot />}>
              An admin connects agents here.
            </Empty>
          )}
          {admin && (
            <div className="grid gap-3 md:grid-cols-2">
              {kinds.map((k) => (
                <button
                  key={k}
                  type="button"
                  onClick={() => setEditing({ kind: k })}
                  className="flex items-start gap-3 rounded-xl border bg-card p-4 text-left transition-colors hover:border-primary hover:bg-accent/40 focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
                >
                  <AgentMark kind={k} />
                  <div className="min-w-0">
                    <div className="font-medium">
                      {agentLabels[k]} {k === 'gemini' && <ToneBadge t="warn">experimental</ToneBadge>}
                    </div>
                    <p className="text-sm text-muted-foreground">{agentBlurbs[k]}</p>
                  </div>
                </button>
              ))}
            </div>
          )}
        </>
      )}

      {agents.length > 0 && (
        <div className="grid gap-3 lg:grid-cols-2">
          {agents.map((a) => (
            <Card key={a.name} className="py-4">
              <CardContent className="flex flex-wrap items-center gap-3 px-4">
                <AgentMark kind={a.kind} size={40} />
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2 font-medium">
                    {a.name}
                    <span className="text-sm font-normal text-muted-foreground">{agentLabels[a.kind] ?? a.kind}</span>
                  </div>
                  <div className="mt-1 flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
                    <ToneBadge t={a.scope === 'deploy' ? 'info' : 'muted'}>
                      <ShieldCheck className="size-3" /> {a.scope === 'deploy' ? 'Deployer' : 'Viewer'}
                    </ToneBadge>
                    {a.apps?.length ? <Mono>{a.apps.join(', ')}</Mono> : <span>every app</span>}
                    {a.triggers.length > 0 && (
                      <ToneBadge t="warn">
                        <Bell className="size-3" /> on {a.triggers.length === 1 ? alertLabels[a.triggers[0]]?.toLowerCase() : `${a.triggers.length} alerts`}
                      </ToneBadge>
                    )}
                    {a.running > 0 && <ToneBadge t="ok">{a.running} running</ToneBadge>}
                  </div>
                </div>
                <div className="flex items-center gap-1">
                  {admin && (
                    <>
                      <Button variant="ghost" size="icon" aria-label={`Edit ${a.name}`} onClick={() => setEditing({ kind: a.kind, agent: a })}>
                        <Pencil />
                      </Button>
                      <Button variant="ghost" size="icon" aria-label={`Remove ${a.name}`} onClick={() => setRemoving(a)}>
                        <Trash2 />
                      </Button>
                    </>
                  )}
                  {canRun && (
                    <Button onClick={() => start(a.name)} disabled={!!starting}>
                      <MessageSquarePlus /> {starting === a.name ? 'Starting…' : 'New session'}
                    </Button>
                  )}
                </div>
              </CardContent>
            </Card>
          ))}
        </div>
      )}

      {sessions.data && sessions.data.length === 0 && agents.length > 0 && (
        <>
          <h2 className="mt-8 mb-3 text-lg font-semibold">Sessions</h2>
          <Empty title="No sessions yet" icon={<MessagesSquare />} compact>
            {canRun
              ? `Start one with New session above, or press ${paletteKeys} and type a question. Sessions an alert starts show up here too.`
              : 'Sessions people and alerts start show up here.'}
          </Empty>
        </>
      )}

      {sessions.data && sessions.data.length > 0 && (
        <>
          <h2 className="mt-8 mb-3 text-lg font-semibold">Sessions</h2>
          <Card className="gap-0 py-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="pl-4">Session</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead className="hidden sm:table-cell">Started by</TableHead>
                  <TableHead>Last active</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {sessions.data.map((s) => (
                  <TableRow key={s.id}>
                    <TableCell className="max-w-0 pl-4 sm:w-1/2">
                      <Link to={`/agents/sessions/${s.id}`} className="flex min-w-0 items-center gap-2.5 hover:underline">
                        <AgentMark kind={s.kind} size={28} />
                        <span className="min-w-0">
                          <span className="block truncate font-medium">{s.title || 'New conversation'}</span>
                          <span className="block truncate text-xs text-muted-foreground">{s.agent}</span>
                        </span>
                      </Link>
                    </TableCell>
                    <TableCell>
                      <SessionStatusDot status={s.status} />
                    </TableCell>
                    <TableCell className="hidden text-muted-foreground sm:table-cell">
                      {s.alert ? (
                        <span className="inline-flex items-center gap-1.5">
                          <Bell className="size-3.5 text-warn" />
                          {alertLabels[s.alert] ?? s.alert}
                        </span>
                      ) : (
                        s.started_by
                      )}
                    </TableCell>
                    <TableCell className="text-muted-foreground">
                      <Time iso={s.updated_at && !s.updated_at.startsWith('0001') ? s.updated_at : s.created_at} />
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </Card>
          <p className="mt-2 text-xs text-muted-foreground">
            The last 100 sessions are kept, encrypted like other secrets. Open an ended one to read it, or resume it to carry on.
          </p>
        </>
      )}

      {editing && (
        <AgentDialog
          kind={editing.kind}
          agent={editing.agent}
          presets={list.data?.presets ?? []}
          onClose={() => setEditing(undefined)}
          onSaved={(n) => {
            notify(editing.agent ? `Saved ${n}` : `Connected ${n}`)
            setEditing(undefined)
            list.reload()
          }}
        />
      )}

      <Dialog open={!!removing} onOpenChange={(o) => !o && setRemoving(undefined)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Remove {removing?.name}?</DialogTitle>
            <DialogDescription>Its running sessions end, and its API keys are deleted from this server.</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setRemoving(undefined)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={() => {
                const a = removing!
                setRemoving(undefined)
                api.deleteAgent(a.name).then(() => (notify(`Removed ${a.name}`), list.reload()), setError)
              }}
            >
              Remove
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

function AgentDialog({
  kind: initialKind,
  agent,
  presets,
  onClose,
  onSaved,
}: {
  kind: string
  agent?: Agent
  presets: AgentPreset[]
  onClose: () => void
  onSaved: (name: string) => void
}) {
  const [kind, setKind] = useState(initialKind)
  const [name, setName] = useState(agent?.name ?? initialKind)
  const [command, setCommand] = useState(agent?.command ?? '')
  const preset = presets.find((p) => p.kind === kind)
  const [keyName, setKeyName] = useState(agent?.keys.find((k) => preset?.keys.includes(k)) ?? preset?.keys[0] ?? '')
  const [key, setKey] = useState('')
  const [env, setEnv] = useState<{ k: string; v: string }[]>(kind === 'custom' && !agent ? [{ k: '', v: '' }] : [])
  const [scope, setScope] = useState<Scope>(agent?.scope ?? 'deploy')
  const [apps, setApps] = useState(agent?.apps?.join(', ') ?? '')
  const [triggers, setTriggers] = useState<string[]>(agent?.triggers ?? [])
  const [alertFix, setAlertFix] = useState(agent?.alert_fix ?? false)
  const me = useSession()
  const alertKinds = Object.keys(alertLabels).filter((k) => k !== 'disk.low' || (me.org?.id ?? 'default') === 'default')
  const [channels, setChannels] = useState<Channel[]>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<Error>()
  const wantChannels = triggers.length > 0

  useEffect(() => {
    if (wantChannels && !channels) api.channels().then(setChannels, () => setChannels([]))
  }, [wantChannels, channels])
  const noReports = wantChannels && channels && !channels.some((c) => c.events.includes('agent.report'))

  function pickKind(k: string) {
    setKind(k)
    if (!agent) setName(k)
    setKeyName(presets.find((p) => p.kind === k)?.keys[0] ?? '')
    if (k === 'custom' && env.length === 0) setEnv([{ k: '', v: '' }])
  }

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(undefined)
    const vars: Record<string, string> = {}
    if (preset && keyName) vars[keyName] = key
    for (const { k, v } of env) if (k.trim()) vars[k.trim()] = v
    try {
      await api.saveAgent(name.trim(), {
        kind,
        command: kind === 'custom' ? command : undefined,
        env: vars,
        scope,
        apps: apps.split(/[\s,]+/).filter(Boolean),
        triggers,
        alert_fix: alertFix,
      })
      onSaved(name.trim())
    } catch (err) {
      setError(err as Error)
      setBusy(false)
    }
  }

  const hasKey = !!agent?.keys.includes(keyName)
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90svh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{agent ? `Edit ${agent.name}` : 'Connect an agent'}</DialogTitle>
          <DialogDescription>
            Keys are stored encrypted and passed only to the agent's container. The agent works through Coldship's tools, with no access to the server
            itself.
          </DialogDescription>
        </DialogHeader>
        <form className="grid gap-4" onSubmit={submit}>
          {!agent && (
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
              {kinds.map((k) => (
                <button
                  key={k}
                  type="button"
                  onClick={() => pickKind(k)}
                  aria-pressed={kind === k}
                  className={`flex flex-col items-center gap-1.5 rounded-lg border p-2.5 text-xs transition-colors ${kind === k ? 'border-primary bg-accent/50' : 'hover:bg-accent/30'}`}
                >
                  <AgentMark kind={k} size={28} />
                  {agentLabels[k]}
                </button>
              ))}
            </div>
          )}
          <div className="grid gap-2">
            <Label htmlFor="agname">Name</Label>
            <Input id="agname" value={name} onChange={(e) => setName(e.target.value)} disabled={!!agent} />
          </div>
          {preset && (
            <div className="grid gap-2">
              <Label htmlFor="agkey">{keyName}</Label>
              <div className="flex gap-2">
                {preset.keys.length > 1 && (
                  <Select value={keyName} onValueChange={setKeyName}>
                    <SelectTrigger className="w-44" aria-label="Key variable">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {preset.keys.map((k) => (
                        <SelectItem key={k} value={k}>
                          {k}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
                <Input
                  id="agkey"
                  type="password"
                  autoComplete="off"
                  className="font-mono"
                  value={key}
                  onChange={(e) => setKey(e.target.value)}
                  placeholder={hasKey ? 'saved: leave empty to keep it' : ''}
                />
              </div>
              <p className="text-xs text-muted-foreground">
                Runs <Mono>{preset.cmd.slice(2).join(' ')}</Mono> from npm in the container.
              </p>
            </div>
          )}
          {kind === 'custom' && (
            <>
              <div className="grid gap-2">
                <Label htmlFor="agcmd">Command</Label>
                <Input id="agcmd" className="font-mono" value={command} onChange={(e) => setCommand(e.target.value)} placeholder="npx -y my-agent --acp" />
                <p className="text-xs text-muted-foreground">Runs in a Node 22 container and must speak ACP on stdin and stdout.</p>
              </div>
              <div className="grid gap-2">
                <Label>Environment</Label>
                {agent?.keys.length ? (
                  <p className="text-xs text-muted-foreground">
                    Saved: <Mono>{agent.keys.join(', ')}</Mono>. Add a variable to set or replace one.
                  </p>
                ) : null}
                {env.map((row, i) => (
                  <div key={i} className="flex gap-2">
                    <Input
                      aria-label="Variable name"
                      className="w-40 font-mono"
                      placeholder="API_KEY"
                      value={row.k}
                      onChange={(e) => setEnv(env.map((r, j) => (j === i ? { ...r, k: e.target.value.toUpperCase() } : r)))}
                    />
                    <Input
                      aria-label="Value"
                      type="password"
                      className="flex-1 font-mono"
                      value={row.v}
                      onChange={(e) => setEnv(env.map((r, j) => (j === i ? { ...r, v: e.target.value } : r)))}
                    />
                    <Button type="button" variant="ghost" size="icon" aria-label="Remove variable" onClick={() => setEnv(env.filter((_, j) => j !== i))}>
                      <X />
                    </Button>
                  </div>
                ))}
                <Button type="button" variant="outline" size="sm" className="justify-self-start" onClick={() => setEnv([...env, { k: '', v: '' }])}>
                  <Plus /> Variable
                </Button>
              </div>
            </>
          )}
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="grid gap-2">
              <Label htmlFor="agscope">Role</Label>
              <Select value={scope} onValueChange={(v) => setScope(v as Scope)}>
                <SelectTrigger id="agscope" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="deploy">Deployer</SelectItem>
                  <SelectItem value="read">Viewer</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-2">
              <Label htmlFor="agapps">Apps it may use</Label>
              <Input id="agapps" className="font-mono" value={apps} onChange={(e) => setApps(e.target.value)} placeholder="every app" />
            </div>
          </div>
          <p className="text-xs text-muted-foreground">
            A deployer agent can change apps and data; destructive production changes still wait for an admin. A session never gets more than the
            role of the person who starts it.
          </p>
          <fieldset>
            <legend className="mb-1 text-sm font-medium">Start on its own when</legend>
            <p className="mb-2 text-xs text-muted-foreground">
              It looks into the alert, then its report goes to the organization's notification channels.
              {apps.trim() && ' With apps set, only alerts about those apps.'}
            </p>
            <div className="grid gap-2 sm:grid-cols-2">
              {alertKinds.map((k) => (
                <label key={k} className="flex items-center gap-2 text-sm">
                  <Checkbox
                    checked={triggers.includes(k)}
                    onCheckedChange={(v) => setTriggers(v === true ? [...triggers, k] : triggers.filter((t) => t !== k))}
                  />
                  {alertLabels[k]}
                </label>
              ))}
            </div>
            {noReports && (
              <p className="mt-3 rounded-md bg-warn-soft px-3 py-2 text-xs text-warn">
                No notification channel gets agent reports yet.{' '}
                <Link to="/org/notifications" className="font-medium underline">
                  Add one
                </Link>{' '}
                with “An agent reports on an alert” picked, or read reports in the session.
              </p>
            )}
            {triggers.length > 0 && scope === 'deploy' && (
              <label className="mt-3 flex items-start gap-2 text-sm">
                <Checkbox className="mt-0.5" checked={alertFix} onCheckedChange={(v) => setAlertFix(v === true)} />
                <span>
                  Let it fix things
                  <span className="block text-xs text-muted-foreground">
                    Off: on alerts it only looks (a viewer). On: it may roll back or restart, as a deployer.
                  </span>
                </span>
              </label>
            )}
          </fieldset>
          {error && <ErrorBox error={error} />}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={busy || !name.trim() || (kind === 'custom' && !command.trim()) || (!!preset && !key && !hasKey)}>
              {agent ? 'Save' : 'Connect'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
