import { api, type Usage } from '../api'
import { useLoad } from '../hooks'
import type { ReactNode } from 'react'
import { ErrorBox, Mono, PageHeader, PageSkeleton } from '@/components/common'
import { AskAgent } from '@/components/AskAgent'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Meter, bytes, duration } from '@/components/meter'

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
        actions={
          <AskAgent
            size="default"
            title="Ask an agent about the server"
            prompt="Check the server's health: disk, memory and CPU, and which apps and data services use the most. Tell me about anything that needs attention."
          />
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
