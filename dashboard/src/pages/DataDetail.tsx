import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api } from '../api'
import { useLoad } from '../hooks'
import { ErrorBox, Time } from '@/components/common'
import { DataMark, dataKinds, envSummary } from '../components/data'
import { useSession } from '../App'

function Copy({ text }: { text: string }) {
  const [done, setDone] = useState(false)
  return (
    <button
      type="button"
      className="btn btn-ghost btn-sm"
      onClick={() => {
        navigator.clipboard?.writeText(text)
        setDone(true)
        setTimeout(() => setDone(false), 1500)
      }}
    >
      {done ? 'Copied' : 'Copy'}
    </button>
  )
}

export default function DataDetail() {
  const { name = '' } = useParams()
  const me = useSession()
  const nav = useNavigate()
  const admin = me.scope === 'admin'
  const { data: d, error, reload } = useLoad(() => api.dataOne(name), [name], 5000)
  const snaps = useLoad(() => api.dataSnapshots(name), [name])
  const [conn, setConn] = useState<Record<string, string>>()
  const [busy, setBusy] = useState(false)
  const [actionError, setActionError] = useState<Error>()
  const [message, setMessage] = useState('')
  const [confirmDelete, setConfirmDelete] = useState('')
  const [deleting, setDeleting] = useState(false)

  async function act(f: () => Promise<unknown>, done?: string) {
    setBusy(true)
    setActionError(undefined)
    setMessage('')
    try {
      await f()
      if (done) setMessage(done)
      reload()
      snaps.reload()
    } catch (err) {
      setActionError(err as Error)
    } finally {
      setBusy(false)
    }
  }

  if (error) return <ErrorBox error={error} />
  if (!d) return <div className="muted">Loading…</div>
  const kind = dataKinds[d.engine]
  const connectCmd = `coldship deploy --name <app> --use ${d.name}`

  return (
    <>
      <header className="page-head">
        <div>
          <div className="muted small">
            <Link to="/data">Data &amp; Storage</Link> /
          </div>
          <h1 className="data-name">
            <DataMark engine={d.engine} />
            {d.name}
          </h1>
          <p className="muted">
            {kind?.label} {d.version} · {d.status.running ? 'running' : d.status.state || 'not running'} · host{' '}
            <code>{d.host}</code>
          </p>
        </div>
      </header>

      {actionError && <ErrorBox error={actionError} />}
      {message && (
        <div className="callout" role="status">
          {message}
        </div>
      )}

      <div className="grid-2">
        <section className="card">
          <h2 className="card-title">Connect an app</h2>
          <p className="muted small">
            The app gets <code>{envSummary(d.env)}</code> on its next deploy.
          </p>
          <div className="copy-line">
            <code>{connectCmd}</code>
            <Copy text={connectCmd} />
          </div>
          <p className="muted small">
            Used by:{' '}
            {d.used_by.length === 0
              ? 'no apps yet'
              : d.used_by.map((a, i) => (
                  <span key={a}>
                    {i > 0 && ', '}
                    <Link to={`/apps/${a}`}>{a}</Link>
                  </span>
                ))}
          </p>
        </section>

        <section className="card">
          <h2 className="card-title">Connection details</h2>
          {!admin ? (
            <p className="muted small">Passwords are shown to admin tokens only.</p>
          ) : !conn ? (
            <>
              <p className="muted small">For connecting with your own tools from Coldship's network. Revealing is recorded in the audit log.</p>
              <button className="btn btn-sm" disabled={busy} onClick={() => act(async () => setConn(await api.dataConnection(d.name)))}>
                Show connection details
              </button>
            </>
          ) : (
            <div className="env-list">
              {Object.entries(conn)
                .sort()
                .map(([k, v]) => (
                  <div className="copy-line" key={k}>
                    <code className="small">
                      {k}={v}
                    </code>
                    <Copy text={v} />
                  </div>
                ))}
            </div>
          )}
        </section>
      </div>

      <section className="card">
        <div className="card-head">
          <h2 className="card-title">Snapshots</h2>
          {d.engine !== 's3' && (
            <button className="btn btn-sm" disabled={busy} onClick={() => act(() => api.snapshotData(d.name), 'Snapshot saved.')}>
              Snapshot now
            </button>
          )}
        </div>
        {d.engine === 's3' ? (
          <p className="muted small">Snapshots of S3 storage aren't supported yet. Back up the bucket with an S3 tool such as rclone.</p>
        ) : !snaps.data || snaps.data.length === 0 ? (
          <p className="muted small">None yet. Snapshots are taken daily and before anything destructive.</p>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>Snapshot</th>
                <th>Taken</th>
                <th>Size</th>
                <th>Reason</th>
                {admin && <th />}
              </tr>
            </thead>
            <tbody>
              {snaps.data.map((s) => (
                <tr key={s.id}>
                  <td className="mono small">{s.id}</td>
                  <td className="small">
                    <Time iso={s.created_at} />
                  </td>
                  <td className="small">{(s.size / 1024).toFixed(1)} KB</td>
                  <td className="small muted">{s.reason}</td>
                  {admin && (
                    <td>
                      <button
                        className="btn btn-ghost btn-sm"
                        disabled={busy}
                        onClick={() => {
                          const reason = window.prompt(
                            `Restore ${d.name} from ${s.id}? All current data is replaced (it's snapshotted first). Why?`,
                          )
                          if (reason?.trim()) act(async () => setMessage((await api.restoreData(d.name, s.id, reason)).message))
                        }}
                      >
                        Restore
                      </button>
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>

      {admin && (
        <section className="card danger-zone">
          <h2 className="card-title">Delete</h2>
          {d.used_by.length > 0 ? (
            <p className="muted small">Disconnect {d.used_by.join(', ')} first (remove it from each app's uses).</p>
          ) : !deleting ? (
            <button className="btn btn-danger btn-sm" onClick={() => setDeleting(true)}>
              Delete {d.name}
            </button>
          ) : (
            <div className="form-row">
              <label className="field grow">
                <span>
                  Type <code>{d.name}</code> to delete it and all its data{d.engine !== 's3' && ' (a snapshot is kept)'}
                </span>
                <input value={confirmDelete} onChange={(e) => setConfirmDelete(e.target.value)} autoFocus />
              </label>
              <div className="inline">
                <button
                  className="btn btn-danger btn-sm"
                  disabled={busy || confirmDelete !== d.name}
                  onClick={() => act(async () => {
                    await api.deleteData(d.name)
                    nav('/data')
                  })}
                >
                  Delete
                </button>
                <button className="btn btn-ghost btn-sm" onClick={() => setDeleting(false)}>
                  Cancel
                </button>
              </div>
            </div>
          )}
        </section>
      )}
    </>
  )
}
