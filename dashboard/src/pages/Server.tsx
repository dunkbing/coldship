import { api, type Usage } from '../api'
import { useLoad } from '../hooks'
import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'
import { ErrorBox, Mono, PageHeader, PageSkeleton } from '@/components/common'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'

function bytes(n: number): string {
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i++
  }
  return `${n >= 10 || i === 0 ? n.toFixed(0) : n.toFixed(1)} ${units[i]}`
}

function duration(s: number): string {
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  return d > 0 ? `${d}d ${h}h` : h > 0 ? `${h}h ${m}m` : `${m}m`
}

// Meter shows a used/total bar; it turns amber at 80% and red at 90%.
function Meter({ label, used, total, detail }: { label: string; used: number; total: number; detail?: string }) {
  const pct = total > 0 ? Math.min(100, (100 * used) / total) : 0
  const level = pct >= 90 ? 'danger' : pct >= 80 ? 'warn' : 'ok'
  return (
    <div className="space-y-1.5">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5 text-sm">
        <span className="font-medium">{label}</span>
        <span className={cn('font-mono text-xs', level === 'ok' ? 'text-muted-foreground' : level === 'warn' ? 'text-warn' : 'text-destructive')}>
          {level !== 'ok' && <span className="font-sans font-medium">{level === 'warn' ? 'High · ' : 'Critical · '}</span>}
          {detail ?? `${pct.toFixed(0)}%`}
        </span>
      </div>
      <div
        className="h-2 overflow-hidden rounded-full bg-muted"
        role="meter"
        aria-valuenow={Math.round(pct)}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-label={label}
      >
        <span
          className={cn('block h-full rounded-full transition-[width]', level === 'ok' ? 'bg-ok' : level === 'warn' ? 'bg-warn' : 'bg-destructive')}
          style={{ width: `${pct}%` }}
        />
      </div>
    </div>
  )
}

// Facts is a two-column definition list of label/value pairs.
function Facts({ items }: { items: [string, ReactNode, boolean?][] }) {
  return (
    <dl className="grid grid-cols-[minmax(6rem,auto)_1fr] gap-x-4 gap-y-2 text-sm">
      {items.map(([k, v, mono]) => (
        <div key={k} className="contents">
          <dt className="text-muted-foreground">{k}</dt>
          <dd className="min-w-0 break-words">{mono ? <Mono className="text-xs break-all">{v}</Mono> : v}</dd>
        </div>
      ))}
    </dl>
  )
}

function UsageMeter({ label, u }: { label: string; u: Usage }) {
  if (u.total === 0) return <Meter label={label} used={0} total={1} detail="none" />
  return <Meter label={label} used={u.used} total={u.total} detail={`${bytes(u.used)} of ${bytes(u.total)}`} />
}

export default function Server() {
  const { data: s, error } = useLoad(api.serverStatus, [], 5000)
  if (error) return <ErrorBox error={error} />
  if (!s) return <PageSkeleton kind="detail" />
  const h = s.host
  const started = (Date.now() - new Date(s.started_at).getTime()) / 1000

  return (
    <>
      <PageHeader
        title="Server"
        description={
          <>
            {h.hostname} · {h.os} · {h.arch} · up {duration(h.uptime_seconds)}. Refreshes every 5 seconds.
          </>
        }
      />

      <div className="grid gap-4 md:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>
              <h2>Resources</h2>
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <Meter
              label={`CPU (${h.cpus} cores)`}
              used={h.cpu_used_percent}
              total={100}
              detail={`${h.cpu_used_percent.toFixed(0)}% · load ${h.load.map((l) => l.toFixed(2)).join(' ')}`}
            />
            <UsageMeter label="Memory" u={h.memory} />
            <UsageMeter label="Swap" u={h.swap} />
            {h.disks.map((d) => (
              <UsageMeter key={d.path} label={`Disk ${d.path} (${d.for})`} u={d} />
            ))}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>
              <h2>Container engine</h2>
            </CardTitle>
          </CardHeader>
          <CardContent>
            {s.engine_error ? (
              <p className="text-sm text-destructive">{s.engine_error}</p>
            ) : s.engine ? (
              <Facts
                items={[
                  ['Engine', `${s.engine.name === 'podman' ? 'Podman' : 'Docker'} ${s.engine.version}`],
                  ['Containers', `${s.engine.running} running of ${s.engine.containers}`],
                  ['Images', s.engine.images],
                  ['Storage', `${s.engine.storage_driver} · ${s.engine.root_dir}`, true],
                  ['Socket', s.engine.host, true],
                ]}
              />
            ) : null}
          </CardContent>

          <CardHeader className="mt-2 border-t pt-4">
            <CardTitle>
              <h2>Coldship</h2>
            </CardTitle>
          </CardHeader>
          <CardContent>
            <Facts
              items={[
                ['Version', s.version],
                ['Running for', duration(started)],
                ['Apps', s.apps],
                ['Data services', s.data_services],
                [
                  'Proxy',
                  !s.proxy
                    ? 'off'
                    : s.proxy.running
                      ? `running on ports ${s.proxy.http_port} and ${s.proxy.https_port}`
                      : `not running${s.proxy.error ? `: ${s.proxy.error}` : ''}`,
                ],
                ['Data', s.data_dir, true],
                ['Kernel', h.kernel, true],
              ]}
            />
          </CardContent>
        </Card>
      </div>
    </>
  )
}
