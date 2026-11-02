import type { ReactNode } from 'react'
import { Link } from 'react-router-dom'
import {
  AlertTriangle,
  ArrowRight,
  Bot,
  Boxes,
  CircleCheck,
  CircleX,
  Clock,
  Database,
  Info,
  MessageSquare,
  Plus,
  Rocket,
  Terminal,
} from 'lucide-react'
import { api, type AgentSession, type AppView, type Attention, type Overview as OverviewData } from '../api'
import { useLoad } from '../hooks'
import { useSession } from '../App'
import { cn } from '@/lib/utils'
import { DeployStatus, ErrorBox, HealthPill, healthOf, PageHeader, StageBadge, Time, type Health } from '@/components/common'
import { AskAgent } from '@/components/AskAgent'
import { SearchButton } from '@/components/CommandPalette'
import { Meter, bytes, duration } from '@/components/meter'
import { Button } from '@/components/ui/button'
import { Card, CardAction, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'

// Overview: the org at a glance. One request, every 10 seconds.
export default function Overview() {
  const me = useSession()
  const { data, error, loading } = useLoad(api.overview, [], 10000)
  const canChange = me.scope !== 'read'

  return (
    <>
      <PageHeader
        title="Overview"
        description={<>{me.org?.name} at a glance. This view is live.</>}
        actions={
          data && data.apps.length > 0 ? (
            <>
              <AskAgent prompt="Look over the apps in this organization and tell me whether anything needs attention, and why." size="default" />
              {canChange && (
                <Button asChild>
                  <Link to="/apps/new">
                    <Plus /> New app
                  </Link>
                </Button>
              )}
            </>
          ) : undefined
        }
      />
      {error && <ErrorBox error={error} />}
      {loading && !data && <OverviewSkeleton />}
      {data && data.apps.length === 0 && data.data.total === 0 && <GetStarted canChange={canChange} agents={!!data.agents} />}
      {data && (data.apps.length > 0 || data.data.total > 0) && <Glance data={data} />}
    </>
  )
}

function Glance({ data }: { data: OverviewData }) {
  const health = { healthy: 0, attention: 0 }
  for (const a of data.apps) {
    const h = healthOf(a)
    if (h === 'healthy') health.healthy++
    else if (h === 'failing' || h === 'stopped') health.attention++
  }
  const waiting = data.attention.filter((a) => a.kind === 'approval').length

  return (
    <div className="space-y-6">
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Tile
          to="/apps"
          icon={<Boxes />}
          label="Apps"
          value={data.apps.length}
          note={
            health.attention > 0 ? (
              <span className="text-destructive">{health.attention} need attention</span>
            ) : data.apps.length > 0 ? (
              <span className="text-ok">{health.healthy === data.apps.length ? 'all healthy' : `${health.healthy} healthy`}</span>
            ) : (
              'none yet'
            )
          }
        />
        <Tile
          to="/data"
          icon={<Database />}
          label="Data & Storage"
          value={data.data.total}
          note={
            data.data.running < data.data.total ? (
              <span className="text-destructive">{data.data.total - data.data.running} not running</span>
            ) : data.data.total > 0 ? (
              'all running'
            ) : (
              'none yet'
            )
          }
        />
        <Tile to="/approvals" icon={<Clock />} label="Approvals" value={waiting} note={waiting ? <span className="text-warn">waiting for an admin</span> : 'none waiting'} />
        {data.agents ? (
          <Tile
            to="/agents"
            icon={<Bot />}
            label="Agent sessions"
            value={data.agents.live}
            note={data.agents.configured ? `running · ${data.agents.configured} agent${data.agents.configured === 1 ? '' : 's'} set up` : 'no agents set up'}
          />
        ) : (
          <Tile to="/apps" icon={<Terminal />} label="Scheduled jobs" value={data.jobs} note={data.jobs ? 'across the apps' : 'none yet'} />
        )}
      </div>

      <div className="grid gap-6 lg:grid-cols-3">
        <div className="min-w-0 space-y-6 lg:col-span-2">
          <AttentionCard items={data.attention} />
          {data.apps.length > 0 && <AppsCard apps={data.apps} previews={data.previews} />}
        </div>
        <div className="min-w-0 space-y-6">
          <DeploysCard data={data} />
          {data.agents && data.agents.recent.length > 0 && <AgentsCard sessions={data.agents.recent} />}
          {data.server && <ServerCard server={data.server} />}
        </div>
      </div>
    </div>
  )
}

function Tile({ to, icon, label, value, note }: { to: string; icon: ReactNode; label: string; value: number; note: ReactNode }) {
  return (
    <Link to={to} className="group rounded-xl focus-visible:ring-[3px] focus-visible:ring-ring/50 focus-visible:outline-none">
      <Card size="sm" className="h-full transition-colors group-hover:bg-accent/40">
        <CardContent>
          <div className="flex items-center gap-2 text-sm text-muted-foreground [&_svg]:size-4">
            {icon}
            <span className="truncate">{label}</span>
          </div>
          <div className="mt-1 text-2xl font-semibold tabular-nums">{value}</div>
          <div className="truncate text-xs text-muted-foreground">{note}</div>
        </CardContent>
      </Card>
    </Link>
  )
}

const severity: Record<Attention['severity'], { icon: ReactNode; label: string; className: string }> = {
  error: { icon: <CircleX />, label: 'Problem', className: 'text-destructive' },
  warning: { icon: <AlertTriangle />, label: 'Warning', className: 'text-warn' },
  info: { icon: <Info />, label: 'Note', className: 'text-info' },
}

function AttentionCard({ items }: { items: Attention[] }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Needs attention</CardTitle>
      </CardHeader>
      <CardContent>
        {items.length === 0 ? (
          <div className="flex items-center gap-3 rounded-lg bg-ok-soft px-4 py-3 text-sm">
            <CircleCheck className="size-5 flex-none text-ok" aria-hidden />
            <div>
              <div className="font-medium text-ok">All clear</div>
              <div className="text-muted-foreground">Every app and database is running, and nothing waits for you.</div>
            </div>
          </div>
        ) : (
          <ul className="-mx-2 divide-y">
            {items.map((a, i) => {
              const s = severity[a.severity]
              return (
                <li key={`${a.kind}:${a.app ?? ''}:${i}`}>
                  <Link to={a.link} className="group flex items-start gap-3 rounded-md px-2 py-2.5 hover:bg-accent/50">
                    <span className={cn('mt-0.5 flex-none [&_svg]:size-4', s.className)} aria-label={s.label} role="img">
                      {s.icon}
                    </span>
                    <span className="min-w-0 flex-1">
                      {/* Titles and details can quote error output: plain text only. */}
                      <span className="block text-sm font-medium break-words">{a.title}</span>
                      {a.detail && <span className="line-clamp-2 block text-xs break-words text-muted-foreground">{a.detail}</span>}
                    </span>
                    {a.at && (
                      <span className="flex-none text-xs text-muted-foreground">
                        <Time iso={a.at} />
                      </span>
                    )}
                    <ArrowRight className="mt-0.5 size-4 flex-none text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100" aria-hidden />
                  </Link>
                </li>
              )
            })}
          </ul>
        )}
      </CardContent>
    </Card>
  )
}

const healthOrder: Record<Health, number> = { stopped: 0, failing: 1, deploying: 2, unknown: 3, healthy: 4 }

function AppsCard({ apps, previews }: { apps: AppView[]; previews: number }) {
  const sorted = [...apps].sort((a, b) => healthOrder[healthOf(a)] - healthOrder[healthOf(b)] || a.name.localeCompare(b.name))
  const shown = sorted.slice(0, 12)
  return (
    <Card>
      <CardHeader>
        <CardTitle>Apps</CardTitle>
        <CardAction>
          <Button asChild variant="ghost" size="sm">
            <Link to="/apps">
              {apps.length > shown.length ? `All ${apps.length}` : 'All apps'}
              {previews > 0 ? ` · ${previews} preview${previews === 1 ? '' : 's'}` : ''} <ArrowRight />
            </Link>
          </Button>
        </CardAction>
      </CardHeader>
      <CardContent>
        <ul className="grid gap-2 sm:grid-cols-2">
          {shown.map((a) => (
            <li key={a.name}>
              <Link to={`/apps/${a.name}`} className="flex h-full flex-col gap-1 rounded-lg border px-3 py-2.5 transition-colors hover:bg-accent/50">
                <span className="flex items-center justify-between gap-2">
                  <span className="truncate font-medium">{a.name}</span>
                  <StageBadge stage={a.spec?.stage} />
                </span>
                <span className="flex items-center justify-between gap-2">
                  <HealthPill health={healthOf(a)} />
                  <span className="truncate text-xs text-muted-foreground">{a.urls?.[0]?.replace(/^https:\/\//, '') ?? ''}</span>
                </span>
              </Link>
            </li>
          ))}
        </ul>
      </CardContent>
    </Card>
  )
}

function DeploysCard({ data }: { data: OverviewData }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Recent deploys</CardTitle>
      </CardHeader>
      <CardContent>
        {data.deploys.length === 0 ? (
          <p className="text-sm text-muted-foreground">No deploys yet.</p>
        ) : (
          <ul className="-mx-2 space-y-0.5">
            {data.deploys.map((d) => (
              <li key={d.id}>
                <Link to={`/apps/${d.app}?tab=deployments`} className="flex items-center gap-2 rounded-md px-2 py-1.5 hover:bg-accent/50">
                  <Rocket className="size-4 flex-none text-muted-foreground" aria-hidden />
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-sm font-medium">{d.app}</span>
                    <span className="block truncate text-xs text-muted-foreground">
                      {d.actor}
                      {d.commit ? ` · ${d.commit.slice(0, 7)}` : ''} · <Time iso={d.created_at} />
                    </span>
                  </span>
                  <DeployStatus status={d.status} />
                </Link>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  )
}

function AgentsCard({ sessions }: { sessions: AgentSession[] }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Agent sessions</CardTitle>
        <CardAction>
          <Button asChild variant="ghost" size="sm">
            <Link to="/agents">
              All <ArrowRight />
            </Link>
          </Button>
        </CardAction>
      </CardHeader>
      <CardContent>
        <ul className="-mx-2 space-y-0.5">
          {sessions.map((s) => (
            <li key={s.id}>
              <Link to={`/agents/sessions/${s.id}`} className="flex items-center gap-2 rounded-md px-2 py-1.5 hover:bg-accent/50">
                <MessageSquare className="size-4 flex-none text-muted-foreground" aria-hidden />
                <span className="min-w-0 flex-1">
                  {/* Titles can come from the agent: plain text only. */}
                  <span className="block truncate text-sm font-medium">{s.title || `Session with ${s.agent}`}</span>
                  <span className="block truncate text-xs text-muted-foreground">
                    {s.agent} · <Time iso={s.updated_at || s.created_at} />
                  </span>
                </span>
                <span className={cn('flex-none text-xs font-medium', s.waiting ? 'text-warn' : 'text-muted-foreground')}>
                  {s.waiting ? 'Waiting for you' : sessionStatus[s.status] ?? s.status}
                </span>
              </Link>
            </li>
          ))}
        </ul>
      </CardContent>
    </Card>
  )
}

const sessionStatus: Record<string, string> = { starting: 'Starting', ready: 'Ready', working: 'Working', ended: 'Ended', failed: 'Failed' }

function ServerCard({ server }: { server: NonNullable<OverviewData['server']> }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Server</CardTitle>
        <CardAction>
          <Button asChild variant="ghost" size="sm">
            <Link to="/server">
              Details <ArrowRight />
            </Link>
          </Button>
        </CardAction>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="text-xs text-muted-foreground">
          <span className="font-medium text-foreground">{server.hostname}</span> · Coldship {server.version} · up {duration(server.uptime_seconds)} · load{' '}
          {server.load[0].toFixed(2)} on {server.cpus} CPU{server.cpus === 1 ? '' : 's'}
        </div>
        <Meter label="Memory" used={server.memory.used} total={server.memory.total} detail={`${bytes(server.memory.used)} of ${bytes(server.memory.total)}`} />
        {server.disks.map((d) => (
          <Meter key={d.path} label={`Disk · ${d.for}`} used={d.used} total={d.total} detail={`${bytes(d.used)} of ${bytes(d.total)}`} />
        ))}
      </CardContent>
    </Card>
  )
}

// GetStarted is the first-run page: nothing deployed yet.
function GetStarted({ canChange, agents }: { canChange: boolean; agents: boolean }) {
  const steps: { icon: ReactNode; title: string; body: ReactNode; action?: ReactNode }[] = [
    {
      icon: <Rocket />,
      title: 'Deploy an app',
      body: (
        <>
          From a Git repository or an image, with or without a Dockerfile. Or run <code className="font-mono text-xs">coldship deploy</code> in a repository.
        </>
      ),
      action: canChange && (
        <Button asChild size="sm">
          <Link to="/apps/new">
            <Plus /> New app
          </Link>
        </Button>
      ),
    },
    {
      icon: <Database />,
      title: 'Add a database or bucket',
      body: 'Postgres, MySQL, Redis or S3-compatible storage, with snapshots and off-site backups.',
      action: canChange && (
        <Button asChild size="sm" variant="outline">
          <Link to="/data/new">
            <Plus /> New database
          </Link>
        </Button>
      ),
    },
  ]
  if (agents)
    steps.push({
      icon: <Bot />,
      title: 'Bring an AI agent',
      body: 'Claude Code, Codex or Gemini can deploy, diagnose and fix apps for you, and asks before it changes anything.',
      action: (
        <Button asChild size="sm" variant="outline">
          <Link to="/agents">Set up an agent</Link>
        </Button>
      ),
    })
  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="text-lg">Welcome to Coldship</CardTitle>
          <p className="text-sm text-muted-foreground">Nothing runs here yet. Start with one of these; it takes a minute.</p>
        </CardHeader>
        <CardContent>
          <ol className={cn('grid gap-3', steps.length === 3 ? 'md:grid-cols-3' : 'md:grid-cols-2')}>
            {steps.map((s, i) => (
              <li key={s.title} className="flex flex-col gap-2 rounded-lg border p-4">
                <span className="flex items-center gap-2 font-medium">
                  <span className="flex size-7 items-center justify-center rounded-md bg-accent text-accent-foreground [&_svg]:size-4" aria-hidden>
                    {s.icon}
                  </span>
                  <span className="text-muted-foreground">{i + 1}.</span> {s.title}
                </span>
                <span className="flex-1 text-sm text-muted-foreground">{s.body}</span>
                {s.action && <span>{s.action}</span>}
              </li>
            ))}
          </ol>
        </CardContent>
      </Card>
      <div className="flex flex-wrap items-center gap-3 text-sm text-muted-foreground">
        <span>Looking for something?</span>
        <SearchButton className="w-56" />
      </div>
    </div>
  )
}

function OverviewSkeleton() {
  return (
    <div aria-busy="true" aria-label="Loading" className="space-y-6">
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        {[0, 1, 2, 3].map((i) => (
          <Skeleton key={i} className="h-24 rounded-xl" />
        ))}
      </div>
      <div className="grid gap-6 lg:grid-cols-3">
        <Skeleton className="h-72 rounded-xl lg:col-span-2" />
        <Skeleton className="h-72 rounded-xl" />
      </div>
    </div>
  )
}
