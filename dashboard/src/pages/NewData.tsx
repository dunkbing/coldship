import { useState, type FormEvent } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { api, type DataEngine } from '../api'
import { ErrorBox } from '@/components/common'
import { DataMark, dataKinds } from '../components/data'

export default function NewData() {
  const nav = useNavigate()
  const [params] = useSearchParams()
  const initial = (params.get('kind') as DataEngine) || 'postgres'
  const [kind, setKind] = useState<DataEngine>(dataKinds[initial] ? initial : 'postgres')
  const [name, setName] = useState('')
  const [version, setVersion] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<Error>()
  const k = dataKinds[kind]
  const ver = version || k.versions[k.versions.length - 1]

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(undefined)
    try {
      const d = await api.createData(kind, name.trim(), ver)
      nav(`/data/${d.name}`)
    } catch (err) {
      setError(err as Error)
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <header className="page-head">
        <div>
          <h1>Add a database or storage</h1>
          <p className="muted">It runs on this server, reachable only by your apps. Connect apps to it after it's created.</p>
        </div>
      </header>

      <form className="card form-card" onSubmit={submit}>
        <div className="kind-picker" role="radiogroup" aria-label="Kind">
          {(Object.keys(dataKinds) as DataEngine[]).map((key) => (
            <button
              type="button"
              role="radio"
              aria-checked={kind === key}
              key={key}
              className={`kind-option${kind === key ? ' selected' : ''}`}
              onClick={() => {
                setKind(key)
                setVersion('')
              }}
            >
              <DataMark engine={key} />
              <span>
                <span className="kind-title">{dataKinds[key].label}</span>
                <span className="muted small">{dataKinds[key].blurb}</span>
              </span>
            </button>
          ))}
        </div>

        <div className="form-row">
          <label className="field grow">
            <span>Name</span>
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={kind === 's3' ? 'uploads' : 'maindb'}
              pattern="[a-z][a-z0-9-]{0,39}"
              title="lowercase letters, digits and dashes, starting with a letter"
              required
              autoFocus
            />
          </label>
          <label className="field">
            <span>Version</span>
            <select value={ver} onChange={(e) => setVersion(e.target.value)}>
              {[...k.versions].reverse().map((v) => (
                <option key={v} value={v}>
                  {v}
                </option>
              ))}
            </select>
          </label>
        </div>

        <p className="muted small">
          Apps you connect get <code>{k.gives}</code>.{' '}
          {kind === 's3' ? 'Snapshots of S3 storage aren’t supported yet.' : 'It’s snapshotted daily and before anything destructive.'}
        </p>

        {error && <ErrorBox error={error} />}
        <div className="inline">
          <button className="btn btn-primary" disabled={busy || !name.trim()}>
            {busy ? `Creating ${k.label}… (up to a minute)` : `Create ${k.label}`}
          </button>
          <Link className="btn btn-ghost" to="/data">
            Cancel
          </Link>
        </div>
      </form>
    </>
  )
}
