import { Link } from 'react-router-dom'
import { Database, DatabaseBackup, Plus } from 'lucide-react'
import { useSession } from '../App'
import { api, type DataView } from '../api'
import { useLoad } from '../hooks'
import { Empty, ErrorBox, Mono, PageHeader, StatusDot, TableSkeleton, Time } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { DataMark, dataKinds, envSummary } from '../components/data'

function Status({ d }: { d: DataView }) {
  if (d.status.running) return <StatusDot dot="bg-ok" text="text-ok" label="Running" />
  return (
    <StatusDot
      dot="bg-destructive"
      text="text-destructive"
      label={d.status.exists ? d.status.state || 'Stopped' : 'Missing'}
    />
  )
}

// Name cell: logo, name (optionally linked) and kind/version underneath.
function NameCell({ d, link }: { d: DataView; link?: boolean }) {
  const name = (
    <span className="inline-flex items-center gap-2 font-medium">
      <DataMark engine={d.engine} size={22} />
      {d.name}
    </span>
  )
  return (
    <>
      {link ? (
        <Link className="hover:underline" to={`/data/${d.name}`}>
          {name}
        </Link>
      ) : (
        name
      )}
      <div className="text-xs text-muted-foreground">
        {dataKinds[d.engine]?.label} {d.version}
      </div>
    </>
  )
}

const linkClass = 'text-primary hover:underline'

export default function Data() {
  const { data, error, loading } = useLoad(api.data, [], 5000)
  const me = useSession()
  const canChange = me.scope !== 'read'
  const services = data?.filter((d) => !d.app) ?? []
  const owned = data?.filter((d) => d.app) ?? []

  return (
    <>
      <PageHeader
        title="Data & Storage"
        description="Databases and object storage your apps connect to. Only reachable from Coldship's network."
        actions={
          <>
          {!me.apps?.length && (
            <Button asChild variant="outline">
              <Link to="/backups">
                <DatabaseBackup />
                Backups
              </Link>
            </Button>
          )}
          {canChange && (
            <Button asChild>
              <Link to="/data/new">
                <Plus />
                Add
              </Link>
            </Button>
          )}
          </>
        }
      />

      {error && <ErrorBox error={error} />}
      {loading && !data && <TableSkeleton cols={5} />}

      {data && services.length === 0 && canChange && (
        <div className="mb-6 grid gap-3 md:grid-cols-2">
          {(Object.keys(dataKinds) as (keyof typeof dataKinds)[]).map((k) => (
            <Link
              key={k}
              to={`/data/new?kind=${k}`}
              className="flex items-start gap-3 rounded-xl border bg-card p-4 transition-colors hover:border-primary hover:bg-accent/40 focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
            >
              <DataMark engine={k} />
              <div className="min-w-0">
                <div className="font-medium">{dataKinds[k].label}</div>
                <div className="text-sm text-muted-foreground">{dataKinds[k].blurb}</div>
              </div>
            </Link>
          ))}
        </div>
      )}

      {services.length > 0 && (
        <Card className="py-0">
          <Table>
            <TableHeader className="bg-muted/50">
              <TableRow>
                <TableHead className="px-4">Name</TableHead>
                <TableHead>Status</TableHead>
                <TableHead className="hidden lg:table-cell">Used by</TableHead>
                <TableHead className="hidden lg:table-cell">Gives apps</TableHead>
                <TableHead className="hidden px-4 lg:table-cell">Latest snapshot</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {services.map((d) => (
                <TableRow key={d.name}>
                  <TableCell className="px-4 py-3">
                    <NameCell d={d} link />
                    {d.used_by.length > 0 && <div className="mt-1 text-xs whitespace-normal text-muted-foreground lg:hidden">used by {d.used_by.join(', ')}</div>}
                  </TableCell>
                  <TableCell>
                    <Status d={d} />
                  </TableCell>
                  <TableCell className="hidden lg:table-cell">
                    {d.used_by.length === 0 ? (
                      <span className="text-muted-foreground">—</span>
                    ) : (
                      d.used_by.map((a, i) => (
                        <span key={a}>
                          {i > 0 && ', '}
                          <Link className={linkClass} to={`/apps/${a}`}>
                            {a}
                          </Link>
                        </span>
                      ))
                    )}
                  </TableCell>
                  <TableCell className="hidden lg:table-cell">
                    <Mono className="text-xs">{envSummary(d.env)}</Mono>
                  </TableCell>
                  <TableCell className="hidden px-4 text-muted-foreground lg:table-cell">
                    {d.latest_snapshot ? <Time iso={d.latest_snapshot.created_at} /> : '—'}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}

      {owned.length > 0 && (
        <section className="mt-8">
          <h2 className="text-lg font-semibold">Databases owned by apps</h2>
          <p className="mb-3 text-sm text-muted-foreground">
            Declared in an app's spec; they're deleted with the app (after a snapshot).
          </p>
          <Card className="py-0">
            <Table>
              <TableHeader className="bg-muted/50">
                <TableRow>
                  <TableHead className="px-4">Database</TableHead>
                  <TableHead className="hidden lg:table-cell">App</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead className="hidden lg:table-cell">Gives the app</TableHead>
                  <TableHead className="hidden px-4 lg:table-cell">Latest snapshot</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {owned.map((d) => (
                  <TableRow key={d.app + '/' + d.name}>
                    <TableCell className="px-4 py-3">
                      <NameCell d={d} />
                      <div className="mt-1 text-xs text-muted-foreground lg:hidden">
                        of{' '}
                        <Link className={linkClass} to={`/apps/${d.app}?tab=databases`}>
                          {d.app}
                        </Link>
                      </div>
                    </TableCell>
                    <TableCell className="hidden lg:table-cell">
                      <Link className={linkClass} to={`/apps/${d.app}`}>
                        {d.app}
                      </Link>
                    </TableCell>
                    <TableCell>
                      <Status d={d} />
                    </TableCell>
                    <TableCell className="hidden lg:table-cell">
                      <Mono className="text-xs">{envSummary(d.env)}</Mono>
                    </TableCell>
                    <TableCell className="hidden px-4 text-muted-foreground lg:table-cell">
                      {d.latest_snapshot ? <Time iso={d.latest_snapshot.created_at} /> : '—'}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </Card>
        </section>
      )}

      {data && data.length === 0 && (
        <Empty title="No databases or storage yet" icon={<Database />}>
          {canChange ? (
            <>
              Pick a kind above, or run <Mono>coldship data create postgres maindb</Mono>. Apps can also bring their own database.
            </>
          ) : (
            'Databases and buckets show up here once someone adds one.'
          )}
        </Empty>
      )}
    </>
  )
}
