import { LiveLog } from '../components/LiveLog'
import { useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api, type Patch, type Plan } from '../api'
import { ErrorBox } from '../components/ui'

// nameFrom suggests an app name: https://github.com/acme/web-shop.git ->
// web-shop, ghcr.io/acme/api:1.2 -> api.
function nameFrom(kind: 'git' | 'image', source: string): string {
  const s = source.trim()
  const base =
    kind === 'git'
      ? s.replace(/\.git$/, '').split(/[/:]/).pop()
      : s.replace(/@.*$/, '').replace(/:[^/]*$/, '').split('/').pop()
  return (base ?? '')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^[^a-z]+|-+$/g, '')
    .slice(0, 40)
}

function parseEnv(text: string): Record<string, string> | string {
  const env: Record<string, string> = {}
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (!line || line.startsWith('#')) continue
    const i = line.indexOf('=')
    if (i < 1) return `"${line}" isn't KEY=value`
    env[line.slice(0, i).trim()] = line.slice(i + 1)
  }
  return env
}

export default function NewApp() {
  const nav = useNavigate()
  const [kind, setKind] = useState<'git' | 'image'>('git')
  const [source, setSource] = useState('')
  const [ref, setRef] = useState('')
  const [name, setName] = useState('')
  const [nameTouched, setNameTouched] = useState(false)
  const [port, setPort] = useState('')
  const [domain, setDomain] = useState('')
  const [envText, setEnvText] = useState('')
  const [postgres, setPostgres] = useState(false)
  const [redis, setRedis] = useState(false)
  const [production, setProduction] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<Error>()
  const [waiting, setWaiting] = useState<Plan>()
  const [launched, setLaunched] = useState<{ app: string; deployment?: string; status?: string; error?: Error }>()

  const appName = nameTouched ? name : nameFrom(kind, source)

  async function submit(e: FormEvent) {
    e.preventDefault()
    setError(undefined)
    const env = parseEnv(envText)
    if (typeof env === 'string') {
      setError(new Error(`Environment: ${env}`))
      return
    }
    const patch: Patch = { name: appName }
    if (kind === 'git') patch.build = { git: source.trim(), ref: ref.trim() || undefined }
    else patch.image = source.trim()
    if (port) patch.port = Number(port)
    if (domain.trim()) patch.add_domains = [domain.trim().toLowerCase()]
    if (Object.keys(env).length) patch.env = env
    const dbs: Patch['add_databases'] = []
    if (postgres) dbs.push({ engine: 'postgres' })
    if (redis) dbs.push({ engine: 'redis', name: postgres ? 'cache' : undefined })
    if (dbs.length) patch.add_databases = dbs
    if (production) patch.stage = 'production'

    setBusy(true)
    try {
      const plan = await api.planPatch(patch)
      if (plan.requires_approval) {
        setWaiting(plan)
        return
      }
      if (plan.action === 'noop') {
        nav(`/apps/${plan.app}`)
        return
      }
      // Apply runs until the deploy finishes; follow its log meanwhile.
      setLaunched({ app: plan.app })
      const applying = api.apply(plan.id, 'created from the dashboard')
      applying.catch((err) => setLaunched((l) => (l ? { ...l, error: err as Error } : l)))
      for (let i = 0; i < 100; i++) {
        const p = await api.plan(plan.id)
        if (p.deployment) {
          setLaunched((l) => (l ? { ...l, deployment: p.deployment } : l))
          break
        }
        await new Promise((r) => setTimeout(r, 300))
      }
    } catch (err) {
      setError(err as Error)
    } finally {
      setBusy(false)
    }
  }

  if (launched) {
    return (
      <>
        <header className="page-head">
          <div>
            <h1>Launching {launched.app}</h1>
            <p className="muted">Building and starting it. The log updates as it goes.</p>
          </div>
        </header>
        <section className="card">
          {launched.deployment ? (
            <LiveLog deployment={launched.deployment} onEnd={(s) => setLaunched((l) => (l ? { ...l, status: s } : l))} />
          ) : (
            <div className="muted">Starting…</div>
          )}
          {launched.error && <ErrorBox error={launched.error} />}
          {launched.status && (
            <div className="inline" style={{ marginTop: 12 }}>
              <Link className="btn btn-primary" to={`/apps/${launched.app}`}>
                {launched.status === 'running' ? `Open ${launched.app}` : `See what went wrong in ${launched.app}`}
              </Link>
              {launched.status !== 'running' && (
                <button className="btn btn-ghost" onClick={() => setLaunched(undefined)}>
                  Back to the form
                </button>
              )}
            </div>
          )}
        </section>
      </>
    )
  }

  return (
    <>
      <header className="page-head">
        <div>
          <h1>New app</h1>
          <p className="muted">Deploy from a Git repository (with or without a Dockerfile) or a container image.</p>
        </div>
      </header>

      <form className="card form-card" onSubmit={submit}>
        <div className="tabs" role="tablist">
          <button type="button" className={kind === 'git' ? 'active' : ''} onClick={() => setKind('git')}>
            Git repository
          </button>
          <button type="button" className={kind === 'image' ? 'active' : ''} onClick={() => setKind('image')}>
            Container image
          </button>
        </div>

        <div className="form-row">
          <label className="field grow">
            <span>{kind === 'git' ? 'Repository URL' : 'Image'}</span>
            <input
              value={source}
              onChange={(e) => setSource(e.target.value)}
              placeholder={kind === 'git' ? 'https://github.com/you/app.git' : 'nginx:1.27-alpine'}
              required
              autoFocus
            />
          </label>
          {kind === 'git' && (
            <label className="field">
              <span>Branch</span>
              <input value={ref} onChange={(e) => setRef(e.target.value)} placeholder="default branch" />
            </label>
          )}
        </div>

        <div className="form-row">
          <label className="field grow">
            <span>Name</span>
            <input
              value={appName}
              onChange={(e) => {
                setNameTouched(true)
                setName(e.target.value)
              }}
              placeholder="web"
              pattern="[a-z][a-z0-9-]{0,39}"
              title="lowercase letters, digits and dashes, starting with a letter"
              required
            />
          </label>
          <label className="field">
            <span>Port</span>
            <input
              type="number"
              min={1}
              max={65535}
              value={port}
              onChange={(e) => setPort(e.target.value)}
              placeholder="detected"
            />
          </label>
        </div>

        <label className="field">
          <span>Domain <span className="muted">(optional, gets HTTPS automatically)</span></span>
          <input value={domain} onChange={(e) => setDomain(e.target.value)} placeholder="app.example.com" />
        </label>

        <label className="field">
          <span>Environment <span className="muted">(optional, one KEY=value per line; values are never shown again)</span></span>
          <textarea value={envText} onChange={(e) => setEnvText(e.target.value)} rows={4} spellCheck={false} placeholder="API_KEY=…" />
        </label>

        <fieldset className="checks">
          <legend>Databases</legend>
          <label>
            <input type="checkbox" checked={postgres} onChange={(e) => setPostgres(e.target.checked)} /> Postgres{' '}
            <span className="muted small">DATABASE_URL</span>
          </label>
          <label>
            <input type="checkbox" checked={redis} onChange={(e) => setRedis(e.target.checked)} /> Redis{' '}
            <span className="muted small">REDIS_URL</span>
          </label>
        </fieldset>

        <label className="check">
          <input type="checkbox" checked={production} onChange={(e) => setProduction(e.target.checked)} /> Production{' '}
          <span className="muted small">destructive changes (by you or an agent) will need an admin's approval</span>
        </label>

        {error && <ErrorBox error={error} />}
        {waiting && (
          <div className="callout" role="status">
            This change needs an admin's approval first. <Link to="/approvals">Review plan {waiting.id}</Link>
          </div>
        )}
        <div className="inline">
          <button className="btn btn-primary" disabled={busy || !source.trim() || !appName}>
            {busy ? 'Deploying…' : 'Deploy'}
          </button>
          <Link className="btn btn-ghost" to="/apps">
            Cancel
          </Link>
        </div>
      </form>
    </>
  )
}
