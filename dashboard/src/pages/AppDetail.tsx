import { MetricsTab } from './MetricsTab'
import { LiveLog } from '../components/LiveLog'
import { useEffect, useRef, useState, type FormEvent } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { api, type AppView, type Deployment } from '../api'
import { useSession } from '../App'
import { fmtTime, timeUntil, useLoad } from '../hooks'
import { DeployStatus, ErrorBox, GitIcon, HealthPill, healthOf, Mono, shortRepo, StageBadge, Time } from '@/components/common'

type Tab = 'diagnose' | 'metrics' | 'deployments' | 'logs' | 'databases' | 'webhook'

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
          {a.urls && a.urls.length > 0 && (
            <p className="urls">
              {a.urls.map((u) => (
                <a key={u} href={u} target="_blank" rel="noreferrer">
                  {u.replace(/^https:\/\//, '')} ↗
                </a>
              ))}
            </p>
          )}
        </div>
        <RollbackButton app={a} onDone={app.reload} />
      </header>

      {a.preview_of && <PreviewBanner a={a} />}

      {a.latest_deployment && (a.latest_deployment.status === 'building' || a.latest_deployment.status === 'deploying') && (
        <section className="card live-card">
          <h2 className="card-title">Deploying {a.latest_deployment.id}</h2>
          <LiveLog deployment={a.latest_deployment.id} onEnd={() => app.reload()} />
        </section>
      )}

      <div className="grid-2">
        <SpecCard a={a} />
        <RuntimeCard a={a} />
      </div>

      <div className="tabs" role="tablist">
        {(['diagnose', 'metrics', 'deployments', 'logs', ...(a.databases?.length ? ['databases'] : []), ...(a.spec?.build && !a.preview_of ? ['webhook'] : [])] as Tab[]).map((t) => (
          <button key={t} role="tab" aria-selected={tab === t} className={tab === t ? 'active' : ''} onClick={() => setParams({ tab: t })}>
            {t[0].toUpperCase() + t.slice(1)}
          </button>
        ))}
      </div>
      {tab === 'diagnose' && <DiagnoseTab name={name} />}
      {tab === 'metrics' && <MetricsTab app={a} />}
      {tab === 'deployments' && <DeploymentsTab name={name} />}
      {tab === 'logs' && <LogsTab name={name} />}
      {tab === 'databases' && <DatabasesTab app={a} onChange={app.reload} />}
      {tab === 'webhook' && <WebhookTab name={name} />}
    </>
  )
}

function PreviewBanner({ a }: { a: AppView }) {
  const me = useSession()
  const nav = useNavigate()
  const [confirming, setConfirming] = useState(false)
  const [error, setError] = useState<Error>()
  async function del() {
    try {
      await api.deletePreview(a.name)
      nav('/apps')
    } catch (e) {
      setError(e as Error)
    }
  }
  return (
    <div className="preview-banner">
      <div>
        Preview of <Link to={`/apps/${a.preview_of}`}>{a.preview_of}</Link>. It is deleted automatically{' '}
        <strong title={fmtTime(a.expires_at)}>{a.expires_at ? timeUntil(a.expires_at) : 'soon'}</strong>, with its volumes.
        {error && <ErrorBox error={error} />}
      </div>
      {me.scope !== 'read' &&
        (!confirming ? (
          <button className="btn btn-sm" onClick={() => setConfirming(true)}>
            Delete preview…
          </button>
        ) : (
          <span className="inline">
            <button className="btn btn-danger btn-sm" onClick={del}>
              Delete {a.name}
            </button>
            <button className="btn btn-ghost btn-sm" onClick={() => setConfirming(false)}>
              Cancel
            </button>
          </span>
        ))}
    </div>
  )
}

function SpecCard({ a }: { a: AppView }) {
  const s = a.spec
  if (!s) return <div className="card">No successful deployment yet.</div>
  return (
    <section className="card">
      <h2>Spec</h2>
      <dl className="kv">
        {s.build ? (
          <>
            <dt>Repository</dt>
            <dd>
              <a href={s.build.git.startsWith('http') ? s.build.git : undefined} className="source" target="_blank" rel="noreferrer">
                <GitIcon />
                <Mono>{shortRepo(s.build.git)}</Mono>
              </a>
            </dd>
            <dt>Ref</dt>
            <dd>
              <Mono>{s.build.ref || 'default branch'}</Mono>
              {s.build.commit && <span className="muted mono small"> · {s.build.commit.slice(0, 12)}</span>}
            </dd>
            <dt>Built with</dt>
            <dd>
              {a.latest_deployment?.detected ? (
                <span title="Coldship generated the Dockerfile; override with build.auto">
                  generated Dockerfile · <Mono>{a.latest_deployment.detected}</Mono>
                </span>
              ) : s.build.builder === 'auto' ? (
                'generated Dockerfile'
              ) : (
                <Mono>{s.build.context === '.' || !s.build.context ? s.build.dockerfile : `${s.build.context}/${s.build.dockerfile}`}</Mono>
              )}
            </dd>
            {s.build.args && Object.keys(s.build.args).length > 0 && (
              <>
                <dt>Build args</dt>
                <dd className="chips">
                  {Object.entries(s.build.args).map(([k, v]) => (
                    <span key={k} className="chip">
                      {k}={v}
                    </span>
                  ))}
                </dd>
              </>
            )}
          </>
        ) : (
          <>
            <dt>Image</dt>
            <dd>
              <Mono>{s.image}</Mono>
            </dd>
          </>
        )}
        <dt>Port</dt>
        <dd>{s.port ? (s.host_port ? `${s.host_port} → ${s.port}` : s.port) : '—'}</dd>
        <dt>Domains</dt>
        <dd>{s.domains?.length ? <span className="chips">{s.domains.map((d) => <span key={d} className="chip">{d}</span>)}</span> : '—'}</dd>
        <dt>Health check</dt>
        <dd>
          {s.port ? (
            <>
              {s.health?.path ? <Mono>GET {s.health.path}</Mono> : 'port accepts connections'}
              <span className="muted"> · within {s.health?.timeout ?? '60s'}</span>
            </>
          ) : (
            'container stays up'
          )}
        </dd>
        <dt>Deploys</dt>
        <dd>{s.host_port || s.volumes?.length ? 'stop and replace (brief downtime)' : 'zero downtime'}</dd>
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
        <dt>Databases</dt>
        <dd>
          {s.databases?.length ? (
            <span className="chips">
              {s.databases.map((d) => (
                <span key={d.name} className="chip" title={`connection URL injected as ${d.env}`}>
                  {d.name} · {d.engine} {d.version} → {d.env}
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
      {d.data.deploy_log && d.data.deploy_log.length > 0 && (
        <div className="build-log">
          <div className="logs-bar">
            <strong className="small">End of the failed deployment's log</strong>
            <span className="small muted">Build and container output are untrusted.</span>
          </div>
          <TailLog lines={d.data.deploy_log} />
        </div>
      )}
    </section>
  )
}

function DeploymentsTab({ name }: { name: string }) {
  const d = useLoad(() => api.deployments(name), [name], 5000)
  const [open, setOpen] = useState<string>()
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
            <th>Commit</th>
            <th>By</th>
            <th>Reason</th>
            <th>When</th>
          </tr>
        </thead>
        <tbody>
          {d.data.map((x) => (
            <DeploymentRow key={x.id} d={x} open={open === x.id} onToggle={() => setOpen(open === x.id ? undefined : x.id)} />
          ))}
        </tbody>
      </table>
    </div>
  )
}

function DeploymentRow({ d: x, open, onToggle }: { d: Deployment; open: boolean; onToggle: () => void }) {
  return (
    <>
      <tr>
        <td>
          <Mono>{x.id}</Mono>
        </td>
        <td>
          <DeployStatus status={x.status} />
          {x.failed_in && <span className="muted small"> in {x.failed_in}</span>}
          {x.kept_previous && <div className="small ok-text">previous version kept running</div>}
          {x.error && <div className="small danger-text clamp">{x.error}</div>}
          <button className="btn btn-ghost btn-sm log-btn" onClick={onToggle} aria-expanded={open}>
            {open ? 'Hide log' : 'Log'}
          </button>
        </td>
        <td>
          <Mono>{x.image || x.spec?.image || '—'}</Mono>
          {x.detected && <div className="small muted">generated: {x.detected}</div>}
        </td>
        <td>
          {x.commit ? <Mono>{x.commit.slice(0, 7)}</Mono> : '—'}
        </td>
        <td>{x.actor}</td>
        <td className="reason">{x.reason}</td>
        <td>
          <Time iso={x.created_at} />
        </td>
      </tr>
      {open && (
        <tr className="expanded">
          <td colSpan={7}>
            <DeployLogView deployment={x.id} />
          </td>
        </tr>
      )}
    </>
  )
}

// TailLog is a log viewer that starts scrolled to the end, where the failing
// step of a build is.
function TailLog({ lines }: { lines?: string[] }) {
  const ref = useRef<HTMLPreElement>(null)
  useEffect(() => {
    if (ref.current) ref.current.scrollTop = ref.current.scrollHeight
  }, [lines])
  return (
    <pre className="logs" ref={ref}>
      {lines ? lines.join('\n') || '(empty)' : 'Loading…'}
    </pre>
  )
}

function DeployLogView({ deployment }: { deployment: string }) {
  const l = useLoad(() => api.deployLog(deployment), [deployment])
  if (l.error) return <ErrorBox error={l.error} />
  return (
    <div>
      <div className="small muted">{l.data?.note}</div>
      <TailLog lines={l.data?.lines} />
    </div>
  )
}

const resultClass: Record<string, string> = {
  deployed: 'dep-running',
  preview: 'dep-running',
  preview_deleted: '',
  planned: 'warn',
  queued: 'warn',
  deploying: 'dep-deploying',
  failed: 'danger',
  superseded: '',
  ignored: '',
}

function WebhookTab({ name }: { name: string }) {
  const me = useSession()
  const w = useLoad(() => api.webhook(name), [name], 5000)
  const [secret, setSecret] = useState<string>()
  const [error, setError] = useState<Error>()
  const [confirm, setConfirm] = useState<'rotate' | 'disable'>()
  async function run(fn: () => Promise<{ secret?: string } | unknown>) {
    setError(undefined)
    try {
      const r = (await fn()) as { secret?: string }
      setSecret(r?.secret)
      setConfirm(undefined)
      w.reload()
    } catch (e) {
      setError(e as Error)
    }
  }
  if (w.error) return <ErrorBox error={w.error} />
  if (!w.data) return <div className="muted">Loading…</div>
  const info = w.data
  const admin = me.scope === 'admin'
  return (
    <>
      <section className="card webhook-card">
        <div className="webhook-head">
          <div>
            <h2>Git webhook {info.enabled ? <span className="badge ok">enabled</span> : <span className="badge">off</span>}</h2>
            {info.enabled && (
              <div className="small">
                Payload URL <Mono>{info.url}</Mono>
              </div>
            )}
            <div className="small muted">
              {info.on_push ? (
                <>
                  Pushes to <Mono>{info.ref || 'the default branch'}</Mono>{' '}
                  {info.on_push.action === 'deploy' ? 'deploy automatically' : 'create a plan to review'}
                  {info.on_push.previews ? '; other branches get previews' : ''}.
                </>
              ) : (
                <>The spec has no on_push section, so pushes are recorded but do nothing.</>
              )}
            </div>
          </div>
          {admin && (
            <div className="inline">
              {!info.enabled && (
                <button className="btn btn-primary btn-sm" onClick={() => run(() => api.enableWebhook(name))}>
                  Enable
                </button>
              )}
              {info.enabled && !confirm && (
                <>
                  <button className="btn btn-sm" onClick={() => setConfirm('rotate')}>
                    Rotate secret…
                  </button>
                  <button className="btn btn-ghost btn-sm" onClick={() => setConfirm('disable')}>
                    Disable…
                  </button>
                </>
              )}
              {confirm === 'rotate' && (
                <button className="btn btn-danger btn-sm" onClick={() => run(() => api.enableWebhook(name, true))}>
                  Yes, rotate (the old secret stops working)
                </button>
              )}
              {confirm === 'disable' && (
                <button className="btn btn-danger btn-sm" onClick={() => run(() => api.disableWebhook(name))}>
                  Yes, disable
                </button>
              )}
              {confirm && (
                <button className="btn btn-ghost btn-sm" onClick={() => setConfirm(undefined)}>
                  Cancel
                </button>
              )}
            </div>
          )}
        </div>
        {secret && (
          <div className="callout secret-callout">
            <strong>Secret (shown once):</strong> <Mono>{secret}</Mono>
            <div className="small muted">
              Add the payload URL and this secret to your Git host's webhook settings, with content type application/json and push
              events.
            </div>
          </div>
        )}
        {error && <ErrorBox error={error} />}
      </section>

      <h2 className="section-title">Deliveries</h2>
      {info.deliveries.length === 0 ? (
        <div className="muted">No pushes received yet.</div>
      ) : (
        <div className="card table-card">
          <table className="table">
            <thead>
              <tr>
                <th>When</th>
                <th>Ref</th>
                <th>Commit</th>
                <th>By</th>
                <th>Result</th>
                <th>Detail</th>
              </tr>
            </thead>
            <tbody>
              {info.deliveries.map((d) => (
                <tr key={d.id}>
                  <td>
                    <Time iso={d.created_at} />
                  </td>
                  <td>{d.ref || '—'}</td>
                  <td>{d.commit ? <Mono>{d.commit.slice(0, 7)}</Mono> : '—'}</td>
                  <td>{d.pusher || '—'}</td>
                  <td>
                    <span className={`badge ${resultClass[d.result] ?? ''}`}>{d.result.replace('_', ' ')}</span>
                  </td>
                  <td className="reason small">
                    {d.detail}
                    {d.result === 'planned' && d.plan && (
                      <div>
                        <Link to="/approvals">Review plan</Link>
                      </div>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <p className="small muted section-note">{info.note}</p>
    </>
  )
}

function DatabasesTab({ app, onChange }: { app: AppView; onChange: () => void }) {
  const me = useSession()
  const snaps = useLoad(() => api.snapshots(app.name), [app.name], 15000)
  const [busy, setBusy] = useState<string>()
  const [error, setError] = useState<Error>()
  async function snapshot(db: string) {
    setBusy(db)
    setError(undefined)
    try {
      await api.snapshot(app.name, db)
      snaps.reload()
      onChange()
    } catch (e) {
      setError(e as Error)
    } finally {
      setBusy(undefined)
    }
  }
  return (
    <>
      {error && <ErrorBox error={error} />}
      <div className="card table-card">
        <table className="table">
          <thead>
            <tr>
              <th>Database</th>
              <th>Status</th>
              <th>Injected as</th>
              <th>Host</th>
              <th>Latest snapshot</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {app.databases?.map((d) => (
              <tr key={d.name}>
                <td>
                  <strong>{d.name}</strong>
                  <div className="small muted">
                    {d.engine} {d.version}
                  </div>
                </td>
                <td>
                  <HealthPill health={d.status.running ? 'healthy' : d.status.exists ? 'failing' : 'stopped'} />
                </td>
                <td>
                  <Mono>{d.env}</Mono>
                </td>
                <td>
                  <Mono>{d.host}</Mono>
                </td>
                <td>{d.last_snapshot ? <Time iso={d.last_snapshot.created_at} /> : <span className="muted">none yet</span>}</td>
                <td>
                  {me.scope !== 'read' && (
                    <button className="btn btn-sm" disabled={busy === d.name} onClick={() => snapshot(d.name)}>
                      {busy === d.name ? 'Snapshotting…' : 'Snapshot now'}
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <p className="small muted section-note">
        Coldship snapshots every database daily and before any change that would delete data. Credentials are never shown; the
        app receives the connection URL in its env.
      </p>

      <h2 className="section-title">Snapshots</h2>
      {snaps.error && <ErrorBox error={snaps.error} />}
      {snaps.data?.length === 0 && <div className="muted">No snapshots yet.</div>}
      {snaps.data && snaps.data.length > 0 && (
        <div className="card table-card">
          <table className="table">
            <thead>
              <tr>
                <th>Snapshot</th>
                <th>Database</th>
                <th>Size</th>
                <th>Taken</th>
                <th>By</th>
                <th>Reason</th>
              </tr>
            </thead>
            <tbody>
              {snaps.data.map((s) => (
                <tr key={s.id}>
                  <td>
                    <Mono>{s.id}</Mono>
                  </td>
                  <td>{s.database}</td>
                  <td>{fmtBytes(s.size)}</td>
                  <td>
                    <Time iso={s.created_at} />
                  </td>
                  <td>{s.actor}</td>
                  <td className="reason">{s.reason}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <p className="small muted section-note">
        To restore one, run <Mono>coldship db restore {app.name} &lt;database&gt; &lt;snapshot&gt; --reason "…"</Mono>. It is planned like any
        destructive change{app.spec?.stage === 'production' ? ' and needs an admin approval' : ''}, and the current data is snapshotted first.
      </p>
    </>
  )
}

function fmtBytes(n: number) {
  if (n >= 1 << 30) return `${(n / (1 << 30)).toFixed(1)} GB`
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MB`
  if (n >= 1 << 10) return `${(n / (1 << 10)).toFixed(1)} KB`
  return `${n} B`
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
