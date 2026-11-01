import { useState } from 'react'
import { api, type AppView } from '../api'
import { useLoad } from '../hooks'
import { ErrorBox } from '../components/ui'
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
    <div className="stat">
      <div className="stat-label">{label}</div>
      <div className="stat-value">{value}</div>
      {note && <div className="muted small">{note}</div>}
    </div>
  )
}

export function MetricsTab({ app }: { app: AppView }) {
  const { data: m, error } = useLoad(() => api.metrics(app.name), [app.name], 15000)
  const [table, setTable] = useState(false)
  if (error) return <ErrorBox error={error} />
  if (!m) return <div className="muted">Loading…</div>
  const pts = m.points
  const times = pts.map((p) => new Date(p.t))
  const now = pts[pts.length - 1]
  const limit = app.spec?.memory ? now?.memory_limit_bytes : undefined

  return (
    <div className="metrics">
      <div className="metrics-bar">
        <span className="muted small">Last hour, a point every {m.interval_seconds} seconds. Kept in memory: it restarts with the server.</span>
        <button className="btn btn-ghost btn-sm" onClick={() => setTable((t) => !t)}>
          {table ? 'Show charts' : 'Show as table'}
        </button>
      </div>

      {now && (
        <div className="stats">
          {m.container && <Tile label="CPU" value={pct(now.cpu_percent)} note="100% = one core" />}
          {m.container && <Tile label="Memory" value={bytes(now.memory_bytes)} note={limit ? `of ${bytes(limit)} limit` : 'no limit set'} />}
          {m.http && <Tile label="Requests" value={perS(now.requests_per_s)} />}
          {m.http && <Tile label="Server errors (5xx)" value={perS(now.errors_5xx_per_s)} />}
          {m.http && <Tile label="Avg response" value={ms(now.avg_latency_ms)} />}
        </div>
      )}

      {!m.container && <p className="muted small">A static site has no container, so there's no CPU, memory or disk to show.</p>}
      {!m.http && <p className="muted small">HTTP numbers come from the proxy: give the app a domain to see requests, errors and response times.</p>}

      {table ? (
        <div className="card table-card">
          <table className="table">
            <thead>
              <tr>
                <th>Time</th>
                {m.container && <th>CPU</th>}
                {m.container && <th>Memory</th>}
                {m.container && <th>Net in / out</th>}
                {m.http && <th>Requests</th>}
                {m.http && <th>4xx / 5xx</th>}
                {m.http && <th>Avg response</th>}
              </tr>
            </thead>
            <tbody>
              {[...pts].reverse().map((p) => (
                <tr key={p.t}>
                  <td className="small">{new Date(p.t).toLocaleTimeString()}</td>
                  {m.container && <td className="small mono">{pct(p.cpu_percent)}</td>}
                  {m.container && <td className="small mono">{bytes(p.memory_bytes)}</td>}
                  {m.container && <td className="small mono">{rate(p.net_in_bps)} / {rate(p.net_out_bps)}</td>}
                  {m.http && <td className="small mono">{perS(p.requests_per_s)}</td>}
                  {m.http && <td className="small mono">{perS(p.errors_4xx_per_s)} / {perS(p.errors_5xx_per_s)}</td>}
                  {m.http && <td className="small mono">{ms(p.avg_latency_ms)}</td>}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <div className="chart-grid">
          {m.container && (
            <LineChart title="CPU (100% = one core)" times={times} format={pct} series={[{ name: 'CPU', slot: 1, values: pts.map((p) => p.cpu_percent) }]} />
          )}
          {m.container && (
            <LineChart
              title="Memory"
              times={times}
              format={bytes}
              series={[{ name: 'Memory', slot: 1, values: pts.map((p) => p.memory_bytes) }]}
              refLine={limit ? { value: limit, label: 'limit' } : undefined}
            />
          )}
          {m.http && (
            <LineChart title="Requests per second" times={times} format={perS} series={[{ name: 'Requests', slot: 1, values: pts.map((p) => p.requests_per_s) }]} />
          )}
          {m.http && (
            <LineChart
              title="Errors per second"
              times={times}
              format={perS}
              series={[
                { name: '4xx', slot: 1, values: pts.map((p) => p.errors_4xx_per_s) },
                { name: '5xx', slot: 2, values: pts.map((p) => p.errors_5xx_per_s) },
              ]}
            />
          )}
          {m.http && (
            <LineChart title="Average response time" times={times} format={ms} series={[{ name: 'Response', slot: 1, values: pts.map((p) => p.avg_latency_ms) }]} />
          )}
          {m.container && (
            <LineChart
              title="Network"
              times={times}
              format={rate}
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
