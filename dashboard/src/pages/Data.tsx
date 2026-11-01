import { Link } from 'react-router-dom'
import { api, type DataView } from '../api'
import { useLoad } from '../hooks'
import { Empty, ErrorBox, Time } from '../components/ui'
import { DataMark, dataKinds, envSummary } from '../components/data'

function Status({ d }: { d: DataView }) {
  if (d.status.running)
    return (
      <span className="pill health-healthy">
        <span className="dot" aria-hidden />
        Running
      </span>
    )
  return (
    <span className="pill health-failing">
      <span className="dot" aria-hidden />
      {d.status.exists ? d.status.state || 'Stopped' : 'Missing'}
    </span>
  )
}

export default function Data() {
  const { data, error, loading } = useLoad(api.data, [], 5000)
  const services = data?.filter((d) => !d.app) ?? []
  const owned = data?.filter((d) => d.app) ?? []

  return (
    <>
      <header className="page-head">
        <div>
          <h1>Data &amp; Storage</h1>
          <p className="muted">Databases and object storage your apps connect to. Only reachable from Coldship's network.</p>
        </div>
        <Link className="btn btn-primary" to="/data/new">
          Add
        </Link>
      </header>

      {error && <ErrorBox error={error} />}
      {loading && !data && <div className="muted">Loading…</div>}

      {data && services.length === 0 && (
        <div className="kind-grid">
          {(Object.keys(dataKinds) as (keyof typeof dataKinds)[]).map((k) => (
            <Link key={k} className="card kind-card" to={`/data/new?kind=${k}`}>
              <DataMark engine={k} />
              <div>
                <div className="kind-title">{dataKinds[k].label}</div>
                <div className="muted small">{dataKinds[k].blurb}</div>
              </div>
            </Link>
          ))}
        </div>
      )}

      {services.length > 0 && (
        <div className="card table-card">
          <table className="table">
            <thead>
              <tr>
                <th>Name</th>
                <th>Status</th>
                <th>Used by</th>
                <th>Gives apps</th>
                <th>Latest snapshot</th>
              </tr>
            </thead>
            <tbody>
              {services.map((d) => (
                <tr key={d.name}>
                  <td>
                    <Link className="row-link data-name" to={`/data/${d.name}`}>
                      <DataMark engine={d.engine} size={22} />
                      {d.name}
                    </Link>
                    <div className="muted small">
                      {dataKinds[d.engine]?.label} {d.version}
                    </div>
                  </td>
                  <td>
                    <Status d={d} />
                  </td>
                  <td>
                    {d.used_by.length === 0 ? (
                      <span className="muted">—</span>
                    ) : (
                      d.used_by.map((a, i) => (
                        <span key={a}>
                          {i > 0 && ', '}
                          <Link to={`/apps/${a}`}>{a}</Link>
                        </span>
                      ))
                    )}
                  </td>
                  <td className="mono small">{envSummary(d.env)}</td>
                  <td className="muted small">{d.latest_snapshot ? <Time iso={d.latest_snapshot.created_at} /> : '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {owned.length > 0 && (
        <>
          <h2 className="section-title">Databases owned by apps</h2>
          <p className="muted small">Declared in an app's spec; they're deleted with the app (after a snapshot).</p>
          <div className="card table-card">
            <table className="table">
              <thead>
                <tr>
                  <th>Database</th>
                  <th>App</th>
                  <th>Status</th>
                  <th>Gives the app</th>
                  <th>Latest snapshot</th>
                </tr>
              </thead>
              <tbody>
                {owned.map((d) => (
                  <tr key={d.app + '/' + d.name}>
                    <td>
                      <span className="data-name">
                        <DataMark engine={d.engine} size={22} />
                        {d.name}
                      </span>
                      <div className="muted small">
                        {dataKinds[d.engine]?.label} {d.version}
                      </div>
                    </td>
                    <td>
                      <Link to={`/apps/${d.app}`}>{d.app}</Link>
                    </td>
                    <td>
                      <Status d={d} />
                    </td>
                    <td className="mono small">{envSummary(d.env)}</td>
                    <td className="muted small">{d.latest_snapshot ? <Time iso={d.latest_snapshot.created_at} /> : '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}

      {data && data.length === 0 && (
        <Empty title="No databases or storage yet">
          Pick a kind above, or run <code>coldship data create postgres maindb</code>.
        </Empty>
      )}
    </>
  )
}
