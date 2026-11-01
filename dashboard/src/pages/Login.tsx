import { useState, type FormEvent } from 'react'
import { api, setToken, type WhoAmI } from '../api'
import { ErrorBox } from '../components/ui'

export default function Login({ onLogin }: { onLogin: (w: WhoAmI) => void }) {
  const [token, setTok] = useState('')
  const [error, setError] = useState<Error>()
  const [busy, setBusy] = useState(false)

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(undefined)
    try {
      const me = await api.whoami(token.trim())
      setToken(token.trim())
      onLogin(me)
    } catch (err) {
      setError(err as Error)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="login">
      <form className="card login-card" onSubmit={submit}>
        <div className="brand brand-lg">
          <svg viewBox="0 0 32 32" width="32" height="32" aria-hidden>
            <rect width="32" height="32" rx="7" fill="var(--accent)" />
            <path d="M6 23V13l10-6 10 6v10h-4v-7H10v7z" fill="#fff" />
          </svg>
          <span>Hangar</span>
        </div>
        <p className="muted">Sign in with an API token. Use an admin token to approve plans.</p>
        <label className="field">
          <span>API token</span>
          <input
            type="password"
            autoComplete="off"
            placeholder="hgr_…"
            value={token}
            onChange={(e) => setTok(e.target.value)}
            autoFocus
          />
        </label>
        {error && <ErrorBox error={error} />}
        <button className="btn btn-primary" disabled={busy || !token.trim()}>
          {busy ? 'Checking…' : 'Sign in'}
        </button>
        <p className="muted small">
          The first admin token is printed when <code>hangar server</code> starts for the first time.
        </p>
      </form>
    </div>
  )
}
