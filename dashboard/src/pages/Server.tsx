import { api, type Usage } from '../api'
import { useLoad } from '../hooks'
import { ErrorBox } from '../components/ui'

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
    <div className="meter">
      <div className="meter-head">
        <span>{label}</span>
        <span className="mono small">{detail ?? `${pct.toFixed(0)}%`}</span>
      </div>
      <div className="meter-bar" role="meter" aria-valuenow={Math.round(pct)} aria-valuemin={0} aria-valuemax={100} aria-label={label}>
        <span className={`meter-fill ${level}`} style={{ width: `${pct}%` }} />
      </div>
    </div>
  )
}

function UsageMeter({ label, u }: { label: string; u: Usage }) {
  if (u.total === 0) return <Meter label={label} used={0} total={1} detail="none" />
  return <Meter label={label} used={u.used} total={u.total} detail={`${bytes(u.used)} of ${bytes(u.total)}`} />
}

export default function Server() {
  const { data: s, error } = useLoad(api.serverStatus, [], 5000)
  if (error) return <ErrorBox error={error} />
  if (!s) return <div className="muted">Loading…</div>
  const h = s.host
  const started = (Date.now() - new Date(s.started_at).getTime()) / 1000

  return (
    <>
      <header className="page-head">
        <div>
          <h1>Server</h1>
          <p className="muted">
            {h.hostname} · {h.os} · {h.arch} · up {duration(h.uptime_seconds)}. Refreshes every 5 seconds.
          </p>
        </div>
      </header>

      <div className="grid-2">
        <section className="card">
          <h2 className="card-title">Resources</h2>
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
        </section>

        <section className="card">
          <h2 className="card-title">Container engine</h2>
          {s.engine_error ? (
            <p className="danger-text small">{s.engine_error}</p>
          ) : s.engine ? (
            <dl className="facts">
              <dt>Engine</dt>
              <dd>
                {s.engine.name === 'podman' ? 'Podman' : 'Docker'} {s.engine.version}
              </dd>
              <dt>Containers</dt>
              <dd>
                {s.engine.running} running of {s.engine.containers}
              </dd>
              <dt>Images</dt>
              <dd>{s.engine.images}</dd>
              <dt>Storage</dt>
              <dd className="mono small">
                {s.engine.storage_driver} · {s.engine.root_dir}
              </dd>
              <dt>Socket</dt>
              <dd className="mono small">{s.engine.host}</dd>
            </dl>
          ) : null}

          <h2 className="card-title" style={{ marginTop: 16 }}>
            Coldship
          </h2>
          <dl className="facts">
            <dt>Version</dt>
            <dd>{s.version}</dd>
            <dt>Running for</dt>
            <dd>{duration(started)}</dd>
            <dt>Apps</dt>
            <dd>{s.apps}</dd>
            <dt>Data services</dt>
            <dd>{s.data_services}</dd>
            <dt>Proxy</dt>
            <dd>
              {!s.proxy
                ? 'off'
                : s.proxy.running
                  ? `running on ports ${s.proxy.http_port} and ${s.proxy.https_port}`
                  : `not running${s.proxy.error ? `: ${s.proxy.error}` : ''}`}
            </dd>
            <dt>Data</dt>
            <dd className="mono small">{s.data_dir}</dd>
            <dt>Kernel</dt>
            <dd className="mono small">{h.kernel}</dd>
          </dl>
        </section>
      </div>
    </>
  )
}
