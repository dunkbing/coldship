import { useState } from 'react'
import { api, type AppView } from '../api'
import { useLoad } from '../hooks'
import { CardsSkeleton, ErrorBox } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { LineChart } from '../components/Chart'

export function bytes(n: number): string {
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  while (n >= 1024 && i < u.length - 1) {
    n /= 1024
    i++
  }
  return `${n >= 100 || i === 0 ? n.toFixed(0) : n.toFixed(1)} ${u[i]}`
}
const rate = (n: number) => `${bytes(n)}/s`
const pct = (n: number) => `${n.toFixed(n < 10 ? 1 : 0)}%`
const perS = (n: number) => `${n < 10 ? n.toFixed(2) : n.toFixed(0)}/s`
const ms = (n: number) => `${n < 10 ? n.toFixed(1) : n.toFixed(0)} ms`

function Tile({ label, value, note }: { label: string; value: string; note?: string }) {
  return (
    <Card size="sm" className="gap-1 px-4">
      <div className="text-xs font-medium text-muted-foreground">{label}</div>
      <div className="text-2xl font-semibold tracking-tight tabular-nums">{value}</div>
      {note && <div className="text-xs text-muted-foreground">{note}</div>}
    </Card>
  )
}

export function MetricsTab({ app }: { app: AppView }) {
  const { data: m, error } = useLoad(() => api.metrics(app.name), [app.name], 15000)
  const [table, setTable] = useState(false)
  if (error) return <ErrorBox error={error} />
  if (!m)
    return (
      <div className="space-y-4" aria-busy="true" aria-label="Loading metrics">
        <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
          {Array.from({ length: 4 }, (_, i) => (
            <Skeleton key={i} className="h-20 rounded-xl" />
          ))}
        </div>
        <CardsSkeleton count={4} height="h-52" />
      </div>
    )
  const pts = m.points
  const times = pts.map((p) => new Date(p.t))
  const now = pts[pts.length - 1]
  const limit = app.spec?.memory ? now?.memory_limit_bytes : undefined

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-sm text-muted-foreground">
          Last hour, a point every {m.interval_seconds} seconds. Kept in memory: it restarts with the server.
        </span>
        <Button variant="ghost" size="sm" onClick={() => setTable((t) => !t)}>
          {table ? 'Show charts' : 'Show as table'}
        </Button>
      </div>

      {now && (
        <div className="grid grid-cols-2 gap-3 md:grid-cols-3 lg:grid-cols-5">
          {m.container && <Tile label="CPU" value={pct(now.cpu_percent)} note="100% = one core" />}
          {m.container && <Tile label="Memory" value={bytes(now.memory_bytes)} note={limit ? `of ${bytes(limit)} limit` : 'no limit set'} />}
          {m.http && <Tile label="Requests" value={perS(now.requests_per_s)} />}
          {m.http && <Tile label="Server errors (5xx)" value={perS(now.errors_5xx_per_s)} />}
          {m.http && <Tile label="Avg response" value={ms(now.avg_latency_ms)} />}
        </div>
      )}

      {!m.container && <p className="text-sm text-muted-foreground">A static site has no container, so there's no CPU, memory or disk to show.</p>}
      {!m.http && <p className="text-sm text-muted-foreground">HTTP numbers come from the proxy: give the app a domain to see requests, errors and response times.</p>}

      {table ? (
        <div className="overflow-hidden rounded-xl border bg-card">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Time</TableHead>
                {m.container && <TableHead>CPU</TableHead>}
                {m.container && <TableHead>Memory</TableHead>}
                {m.container && <TableHead>Net in / out</TableHead>}
                {m.http && <TableHead>Requests</TableHead>}
                {m.http && <TableHead>4xx / 5xx</TableHead>}
                {m.http && <TableHead>Avg response</TableHead>}
              </TableRow>
            </TableHeader>
            <TableBody>
              {[...pts].reverse().map((p) => (
                <TableRow key={p.t}>
                  <TableCell>{new Date(p.t).toLocaleTimeString()}</TableCell>
                  {m.container && <TableCell className="font-mono text-xs">{pct(p.cpu_percent)}</TableCell>}
                  {m.container && <TableCell className="font-mono text-xs">{bytes(p.memory_bytes)}</TableCell>}
                  {m.container && (
                    <TableCell className="font-mono text-xs">
                      {rate(p.net_in_bps)} / {rate(p.net_out_bps)}
                    </TableCell>
                  )}
                  {m.http && <TableCell className="font-mono text-xs">{perS(p.requests_per_s)}</TableCell>}
                  {m.http && (
                    <TableCell className="font-mono text-xs">
                      {perS(p.errors_4xx_per_s)} / {perS(p.errors_5xx_per_s)}
                    </TableCell>
                  )}
                  {m.http && <TableCell className="font-mono text-xs">{ms(p.avg_latency_ms)}</TableCell>}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      ) : (
        <div className="grid gap-4 md:grid-cols-2">
          {m.container && (
            <LineChart title="CPU (100% = one core)" times={times} format={pct} minTop={1} series={[{ name: 'CPU', slot: 1, values: pts.map((p) => p.cpu_percent) }]} />
          )}
          {m.container && (
            <LineChart
              title="Memory"
              times={times}
              format={bytes}
              binary
              minTop={1 << 20}
              series={[{ name: 'Memory', slot: 1, values: pts.map((p) => p.memory_bytes) }]}
              refLine={limit ? { value: limit, label: 'limit' } : undefined}
            />
          )}
          {m.http && (
            <LineChart title="Requests per second" times={times} format={perS} minTop={1} series={[{ name: 'Requests', slot: 1, values: pts.map((p) => p.requests_per_s) }]} />
          )}
          {m.http && (
            <LineChart
              title="Errors per second"
              times={times}
              format={perS}
              minTop={1}
              series={[
                { name: '4xx', slot: 1, values: pts.map((p) => p.errors_4xx_per_s) },
                { name: '5xx', slot: 2, values: pts.map((p) => p.errors_5xx_per_s) },
              ]}
            />
          )}
          {m.http && (
            <LineChart title="Average response time" times={times} format={ms} minTop={1} series={[{ name: 'Response', slot: 1, values: pts.map((p) => p.avg_latency_ms) }]} />
          )}
          {m.container && (
            <LineChart
              title="Network"
              times={times}
              format={rate}
              binary
              minTop={1024}
              series={[
                { name: 'In', slot: 1, values: pts.map((p) => p.net_in_bps) },
                { name: 'Out', slot: 2, values: pts.map((p) => p.net_out_bps) },
              ]}
            />
          )}
          {m.container && (
            <LineChart
              title="Disk"
              times={times}
              format={rate}
              binary
              minTop={1024}
              series={[
                { name: 'Read', slot: 1, values: pts.map((p) => p.disk_read_bps) },
                { name: 'Write', slot: 2, values: pts.map((p) => p.disk_write_bps) },
              ]}
            />
          )}
        </div>
      )}
    </div>
  )
}
