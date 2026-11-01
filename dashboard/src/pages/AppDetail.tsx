import { useState, type FormEvent } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { api, type AppView } from '../api'
import { useSession } from '../App'
import { fmtTime, useLoad } from '../hooks'
import { DeployStatus, ErrorBox, HealthPill, healthOf, Mono, StageBadge, Time } from '../components/ui'

type Tab = 'diagnose' | 'deployments' | 'logs'

export default function AppDetail() {
  const { name = '' } = useParams()
  const [params, setParams] = useSearchParams()
  const tab = (params.get('tab') as Tab) || 'diagnose'
  const app = useLoad(() => api.app(name), [name], 5000)

  if (app.error) return <ErrorBox error={app.error} />
  if (!app.data) return <div className="muted">Loading…</div>
  const a = app.data

  return (
    <>
      <div className="crumbs">
        <Link to="/apps">Apps</Link> / <span>{a.name}</span>
      </div>
      <header className="page-head">
        <div>
          <h1 className="inline">
            {a.name} <StageBadge stage={a.spec?.stage} /> <HealthPill health={healthOf(a)} />
          </h1>
          <p className="muted">
            Current deployment <Mono>{a.current_deployment || 'none'}</Mono> · updated <Time iso={a.updated_at} />
          </p>
        </div>
        <RollbackButton app={a} onDone={app.reload} />
      </header>

      <div className="grid-2">
        <SpecCard a={a} />
        <RuntimeCard a={a} />
      </div>

      <div className="tabs" role="tablist">
        {(['diagnose', 'deployments', 'logs'] as Tab[]).map((t) => (
          <button key={t} role="tab" aria-selected={tab === t} className={tab === t ? 'active' : ''} onClick={() => setParams({ tab: t })}>
            {t === 'diagnose' ? 'Diagnose' : t === 'deployments' ? 'Deployments' : 'Logs'}
          </button>
        ))}
      </div>
      {tab === 'diagnose' && <DiagnoseTab name={name} />}
      {tab === 'deployments' && <DeploymentsTab name={name} />}
      {tab === 'logs' && <LogsTab name={name} />}
    </>
  )
}

function SpecCard({ a }: { a: AppView }) {
  const s = a.spec
  if (!s) return <div className="card">No successful deployment yet.</div>
  return (
    <section className="card">
      <h2>Spec</h2>
      <dl className="kv">
        <dt>Image</dt>
        <dd>
          <Mono>{s.image}</Mono>
        </dd>
        <dt>Port</dt>
        <dd>{s.port ? (s.host_port ? `${s.host_port} → ${s.port}` : s.port) : '—'}</dd>
        <dt>Memory</dt>
        <dd>{s.memory ?? 'unlimited'}</dd>
        <dt>Env</dt>
        <dd>
          {s.env && Object.keys(s.env).length ? (
            <span className="chips">
              {Object.keys(s.env)
                .sort()
                .map((k) => (
                  <span key={k} className="chip" title="Value redacted">
                    {k}
                  </span>
                ))}
            </span>
          ) : (
            '—'
          )}
        </dd>
        <dt>Volumes</dt>
        <dd>
          {s.volumes?.length
            ? s.volumes.map((v) => (
                <div key={v.name}>
                  <Mono>{v.name}</Mono> <span className="muted">at</span> <Mono>{v.path}</Mono>
                </div>
              ))
            : '—'}
        </dd>
      </dl>
    </section>
  )
}

function RuntimeCard({ a }: { a: AppView }) {
  const rt = a.runtime
  return (
    <section className="card">
      <h2>Container</h2>
      {!rt || !rt.exists ? (
        <p className="muted">No container is running for this app.</p>
      ) : (
        <dl className="kv">
          <dt>State</dt>
          <dd>{rt.state}</dd>
          <dt>Restarts</dt>
          <dd className={rt.restart_count > 0 ? 'danger-text' : ''}>{rt.restart_count}</dd>
          <dt>Exit code</dt>
          <dd>{rt.exit_code}</dd>
          <dt>OOM killed</dt>
          <dd className={rt.oom_killed ? 'danger-text' : ''}>{rt.oom_killed ? 'yes' : 'no'}</dd>
          <dt>Started</dt>
          <dd title={fmtTime(rt.started_at)}>
            <Time iso={rt.started_at} />
          </dd>
        </dl>
      )}
    </section>
  )
}

function RollbackButton({ app, onDone }: { app: AppView; onDone: () => void }) {
  const me = useSession()
  const [open, setOpen] = useState(false)
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<Error>()
  const [msg, setMsg] = useState('')
  if (me.scope === 'read') return null

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(undefined)
    try {
      const r = await api.rollback(app.name, reason.trim())
      setMsg(`Rolled back: ${r.deployment?.id ?? ''} is ${r.deployment?.status ?? r.message}`)
      setOpen(false)
      setReason('')
      onDone()
    } catch (err) {
      setError(err as Error)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="head-actions">
      {msg && <span className="ok-text small">{msg}</span>}
      {!open ? (
        <button className="btn" onClick={() => setOpen(true)}>
          Roll back…
        </button>
      ) : (
        <form className="rollback-form card" onSubmit={submit}>
          <label className="field">
            <span>Reason (recorded in the audit log)</span>
            <input value={reason} onChange={(e) => setReason(e.target.value)} placeholder="e.g. new image crash-loops" autoFocus />
          </label>
          {error && <ErrorBox error={error} />}
          <div className="inline">
            <button className="btn btn-primary" disabled={busy || !reason.trim()}>
              {busy ? 'Rolling back…' : 'Roll back to last good'}
            </button>
            <button type="button" className="btn btn-ghost" onClick={() => setOpen(false)}>
              Cancel
            </button>
          </div>
        </form>
      )}
    </div>
  )
}

function DiagnoseTab({ name }: { name: string }) {
  const d = useLoad(() => api.diagnose(name), [name], 10000)
  if (d.error) return <ErrorBox error={d.error} />
  if (!d.data) return <div className="muted">Diagnosing…</div>
  return (
    <section className="card">
      <ul className="findings">
        {d.data.findings.map((f, i) => (
          <li key={i} className={`finding sev-${f.severity}`}>
            <div className="finding-head">
              <span className={`badge sev-${f.severity}`}>{f.severity}</span>
              <Mono>{f.code}</Mono>
            </div>
            <div>{f.message}</div>
            {f.suggested_fix !== undefined && f.suggested_fix !== null && (
              <div className="small muted">
                Suggested fix: <Mono>{JSON.stringify(f.suggested_fix)}</Mono>
              </div>
            )}
          </li>
        ))}
      </ul>
    </section>
  )
}

function DeploymentsTab({ name }: { name: string }) {
  const d = useLoad(() => api.deployments(name), [name], 5000)
  if (d.error) return <ErrorBox error={d.error} />
  if (!d.data) return <div className="muted">Loading…</div>
  return (
    <div className="card table-card">
      <table className="table">
        <thead>
          <tr>
            <th>Deployment</th>
            <th>Status</th>
            <th>Image</th>
            <th>By</th>
            <th>Reason</th>
            <th>When</th>
          </tr>
        </thead>
        <tbody>
          {d.data.map((x) => (
            <tr key={x.id}>
              <td>
                <Mono>{x.id}</Mono>
              </td>
              <td>
                <DeployStatus status={x.status} />
                {x.error && <div className="small danger-text clamp">{x.error}</div>}
              </td>
              <td>
                <Mono>{x.spec?.image ?? '—'}</Mono>
              </td>
              <td>{x.actor}</td>
              <td className="reason">{x.reason}</td>
              <td>
                <Time iso={x.created_at} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function LogsTab({ name }: { name: string }) {
  const [tail, setTail] = useState(200)
  const l = useLoad(() => api.logs(name, tail), [name, tail], 5000)
  return (
    <section className="card">
      <div className="logs-bar">
        <span className="small muted">Log lines are untrusted output from the app.</span>
        <label className="small inline">
          Lines
          <select value={tail} onChange={(e) => setTail(Number(e.target.value))}>
            {[50, 200, 1000].map((n) => (
              <option key={n}>{n}</option>
            ))}
          </select>
        </label>
      </div>
      {l.error && <ErrorBox error={l.error} />}
      <pre className="logs">{l.data ? l.data.lines.join('\n') || '(no output)' : 'Loading…'}</pre>
    </section>
  )
}
