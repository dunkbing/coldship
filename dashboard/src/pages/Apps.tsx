import { Link } from 'react-router-dom'
import { Boxes, Plus } from 'lucide-react'
import { useSession } from '../App'
import { AskAgent } from '@/components/AskAgent'
import { api, type AppView } from '../api'
import { timeUntil, useLoad } from '../hooks'
import { cn } from '@/lib/utils'
import {
  DeployStatus,
  Empty,
  ErrorBox,
  HealthPill,
  healthOf,
  PageHeader,
  Source,
  StageBadge,
  TableSkeleton,
  Time,
} from '@/components/common'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

async function loadApps(): Promise<AppView[]> {
  const apps = await api.apps()
  return Promise.all(apps.map((a) => api.app(a.name).catch(() => a as AppView)))
}

function Stat({ label, value, className }: { label: string; value: number; className?: string }) {
  return (
    <Card size="sm">
      <CardContent>
        <div className="text-sm text-muted-foreground">{label}</div>
        <div className={cn('text-2xl font-semibold tabular-nums', className)}>{value}</div>
      </CardContent>
    </Card>
  )
}

// AppName links to the app and shows its first URL under it.
function AppName({ app }: { app: AppView }) {
  return (
    <div className="min-w-0">
      <Link className="font-medium hover:underline" to={`/apps/${app.name}`}>
        {app.name}
      </Link>
      {app.urls?.[0] && (
        <div className="text-xs">
          <a href={app.urls[0]} target="_blank" rel="noreferrer" className="text-muted-foreground hover:underline">
            {app.urls[0].replace(/^https:\/\//, '')}
          </a>
        </div>
      )}
    </div>
  )
}

export default function Apps() {
  const { data: all, error, loading } = useLoad(loadApps, [], 5000)
  const canChange = useSession().scope !== 'read'
  const data = all?.filter((a) => !a.preview_of)
  const previews = all?.filter((a) => a.preview_of) ?? []

  const counts = { healthy: 0, failing: 0 }
  for (const a of data ?? []) {
    const h = healthOf(a)
    if (h === 'healthy') counts.healthy++
    else if (h === 'failing' || h === 'stopped') counts.failing++
  }

  return (
    <>
      <PageHeader
        title="Apps"
        description="Everything Coldship runs on this node. This view is live."
        actions={
          canChange && (
            <Button asChild>
              <Link to="/apps/new">
                <Plus /> New app
              </Link>
            </Button>
          )
        }
      />

      {data && data.length > 0 && (
        <div className="mb-6 grid grid-cols-2 gap-3 md:grid-cols-3">
          <Stat label="Apps" value={data.length} />
          <Stat label="Healthy" value={counts.healthy} className="text-ok" />
          <Stat label="Need attention" value={counts.failing} className={counts.failing ? 'text-destructive' : undefined} />
        </div>
      )}

      {error && <ErrorBox error={error} />}
      {loading && !data && (
        <div aria-busy="true" aria-label="Loading" className="space-y-6">
          <div className="grid grid-cols-2 gap-3 md:grid-cols-3">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-20 rounded-xl" />
            ))}
          </div>
          <TableSkeleton cols={5} />
        </div>
      )}
      {data?.length === 0 && previews.length === 0 && (
        <Empty
          title="No apps yet"
          icon={<Boxes />}
          action={
            canChange && (
              <>
                <Button asChild>
                  <Link to="/apps/new">
                    <Plus /> New app
                  </Link>
                </Button>
                <AskAgent prompt="Deploy an app for me. Ask me which repository or image to use." label="Ask an agent to deploy" size="default" />
              </>
            )
          }
        >
          {canChange ? (
            <>
              Deploy from a Git repository or an image, or run <code className="font-mono">coldship deploy</code> inside a repository.
            </>
          ) : (
            'Apps show up here once someone deploys one.'
          )}
        </Empty>
      )}

      {data && data.length > 0 && (
        <Card className="py-0">
          <Table>
            <TableHeader className="bg-muted/50">
              <TableRow>
                <TableHead className="px-4">App</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Stage</TableHead>
                <TableHead>Source</TableHead>
                <TableHead className="px-4">Last deployment</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.map((a) => (
                <TableRow key={a.name}>
                  <TableCell className="px-4 py-3">
                    <AppName app={a} />
                  </TableCell>
                  <TableCell>
                    <HealthPill health={healthOf(a)} />
                  </TableCell>
                  <TableCell>
                    <StageBadge stage={a.spec?.stage} />
                  </TableCell>
                  <TableCell>
                    <Source spec={a.spec} />
                  </TableCell>
                  <TableCell className="px-4">
                    {a.latest_deployment ? (
                      <span className="inline-flex items-center gap-2">
                        <DeployStatus status={a.latest_deployment.status} />
                        <span className="text-xs text-muted-foreground">
                          {a.latest_deployment.actor} · <Time iso={a.latest_deployment.created_at} />
                        </span>
                      </span>
                    ) : (
                      '—'
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
      {previews.length > 0 && <PreviewTable previews={previews} />}
    </>
  )
}

function PreviewTable({ previews }: { previews: AppView[] }) {
  return (
    <>
      <h2 className="mt-8 mb-3 text-lg font-semibold">Previews</h2>
      <Card className="py-0">
        <Table>
          <TableHeader className="bg-muted/50">
            <TableRow>
              <TableHead className="px-4">Preview</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Of</TableHead>
              <TableHead>Source</TableHead>
              <TableHead className="px-4">Expires</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {previews.map((a) => (
              <TableRow key={a.name}>
                <TableCell className="px-4 py-3">
                  <AppName app={a} />
                </TableCell>
                <TableCell>
                  <HealthPill health={healthOf(a)} />
                </TableCell>
                <TableCell>
                  <Link className="text-primary hover:underline" to={`/apps/${a.preview_of}`}>
                    {a.preview_of}
                  </Link>
                </TableCell>
                <TableCell>
                  <Source spec={a.spec} />
                </TableCell>
                <TableCell className="px-4 text-muted-foreground">{a.expires_at ? timeUntil(a.expires_at) : '—'}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Card>
    </>
  )
}
