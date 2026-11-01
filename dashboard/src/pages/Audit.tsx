import { useMemo, useState } from 'react'
import { api } from '../api'
import { useLoad } from '../hooks'
import { Empty, ErrorBox, Mono, Time } from '../components/ui'

export default function Audit() {
  const audit = useLoad(() => api.audit(500), [], 10000)
  const [actor, setActor] = useState('')
  const actors = useMemo(() => [...new Set(audit.data?.map((e) => e.actor) ?? [])].sort(), [audit.data])
  const rows = audit.data?.filter((e) => !actor || e.actor === actor) ?? []

  return (
    <>
      <header className="page-head">
        <div>
          <h1>Audit log</h1>
          <p className="muted">Every change, by whom and why. Agents act under their own token names.</p>
        </div>
        <label className="small inline">
          Actor
          <select value={actor} onChange={(e) => setActor(e.target.value)}>
            <option value="">All</option>
            {actors.map((a) => (
              <option key={a}>{a}</option>
            ))}
          </select>
        </label>
      </header>
      {audit.error && <ErrorBox error={audit.error} />}
      {audit.data && rows.length === 0 && <Empty title="No entries" />}
      {rows.length > 0 && (
        <div className="card table-card">
          <table className="table">
            <thead>
              <tr>
                <th>When</th>
                <th>Actor</th>
                <th>Action</th>
                <th>Target</th>
                <th>Reason</th>
                <th>Detail</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((e) => (
                <tr key={e.id}>
                  <td>
                    <Time iso={e.time} />
                  </td>
                  <td>
                    <strong>{e.actor}</strong>
                  </td>
                  <td>
                    <Mono>{e.action}</Mono>
                  </td>
                  <td>{e.target}</td>
                  <td className="reason">{e.reason || <span className="muted">—</span>}</td>
                  <td className="detail">
                    {e.detail &&
                      Object.entries(e.detail).map(([k, v]) => (
                        <span key={k} className="chip">
                          {k}: {String(v)}
                        </span>
                      ))}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  )
}
