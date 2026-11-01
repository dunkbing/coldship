import { Link } from 'react-router-dom'
import { api, type AppView } from '../api'
import { timeUntil, useLoad } from '../hooks'
import { DeployStatus, Empty, ErrorBox, HealthPill, healthOf, Source, StageBadge, Time } from '../components/ui'

async function loadApps(): Promise<AppView[]> {
  const apps = await api.apps()
  return Promise.all(apps.map((a) => api.app(a.name).catch(() => a as AppView)))
}

export default function Apps() {
  const { data: all, error, loading } = useLoad(loadApps, [], 5000)
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
      <header className="page-head">
        <div>
          <h1>Apps</h1>
          <p className="muted">Everything Coldship runs on this node. This view is live.</p>
        </div>
        <Link className="btn btn-primary" to="/apps/new">
          New app
        </Link>
      </header>

      {data && data.length > 0 && (
        <div className="stats">
          <div className="stat">
            <div className="stat-label">Apps</div>
            <div className="stat-value">{data.length}</div>
          </div>
          <div className="stat">
            <div className="stat-label">Healthy</div>
            <div className="stat-value ok-text">{counts.healthy}</div>
          </div>
          <div className="stat">
            <div className="stat-label">Need attention</div>
            <div className={`stat-value${counts.failing ? ' danger-text' : ''}`}>{counts.failing}</div>
          </div>
        </div>
      )}

      {error && <ErrorBox error={error} />}
      {loading && !data && <div className="muted">Loading…</div>}
      {data?.length === 0 && previews.length === 0 && (
        <Empty title="No apps yet">
          <Link to="/apps/new">Create one here</Link>, run <code>coldship deploy</code> inside a Git repository, or ask your agent to.
        </Empty>
      )}

      {data && data.length > 0 && (
        <div className="card table-card">
          <table className="table">
            <thead>
              <tr>
                <th>App</th>
                <th>Status</th>
                <th>Stage</th>
                <th>Source</th>
                <th>Last deployment</th>
              </tr>
            </thead>
            <tbody>
              {data.map((a) => (
                <tr key={a.name}>
                  <td>
                    <Link className="row-link" to={`/apps/${a.name}`}>
                      {a.name}
                    </Link>
                    {a.urls?.[0] && (
                      <div className="small">
                        <a href={a.urls[0]} target="_blank" rel="noreferrer" className="muted">
                          {a.urls[0].replace(/^https:\/\//, '')}
                        </a>
                      </div>
                    )}
                  </td>
                  <td>
                    <HealthPill health={healthOf(a)} />
                  </td>
                  <td>
                    <StageBadge stage={a.spec?.stage} />
                  </td>
                  <td>
                    <Source spec={a.spec} />
                  </td>
                  <td>
                    {a.latest_deployment ? (
                      <span className="inline">
                        <DeployStatus status={a.latest_deployment.status} />
                        <span className="muted small">
                          {a.latest_deployment.actor} · <Time iso={a.latest_deployment.created_at} />
                        </span>
                      </span>
                    ) : (
                      '—'
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {previews.length > 0 && <PreviewTable previews={previews} />}
    </>
  )
}

function PreviewTable({ previews }: { previews: AppView[] }) {
  return (
    <>
      <h2 className="section-title">Previews</h2>
      <div className="card table-card">
        <table className="table">
          <thead>
            <tr>
              <th>Preview</th>
              <th>Status</th>
              <th>Of</th>
              <th>Source</th>
              <th>Expires</th>
            </tr>
          </thead>
          <tbody>
            {previews.map((a) => (
              <tr key={a.name}>
                <td>
                  <Link className="row-link" to={`/apps/${a.name}`}>
                    {a.name}
                  </Link>
                  {a.urls?.[0] && (
                    <div className="small">
                      <a href={a.urls[0]} target="_blank" rel="noreferrer" className="muted">
                        {a.urls[0].replace(/^https:\/\//, '')}
                      </a>
                    </div>
                  )}
                </td>
                <td>
                  <HealthPill health={healthOf(a)} />
                </td>
                <td>
                  <Link to={`/apps/${a.preview_of}`}>{a.preview_of}</Link>
                </td>
                <td>
                  <Source spec={a.spec} />
                </td>
                <td className="muted">{a.expires_at ? timeUntil(a.expires_at) : '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  )
}
