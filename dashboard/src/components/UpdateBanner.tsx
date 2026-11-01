import { useEffect, useState } from 'react'
import { api, type SystemStatus } from '../api'
import { useSession } from '../App'
import { ErrorBox } from './ui'

const DISMISS_KEY = 'coldship.dismissed-update'

function dismissedVersion(): string {
  try {
    return localStorage.getItem(DISMISS_KEY) ?? ''
  } catch {
    return ''
  }
}

// UpdateBanner tells people a new Coldship version is available, and lets
// admins install it. While the server upgrades it restarts; the banner waits
// for it to come back on the new version and reloads the page so the new
// dashboard loads too.
export default function UpdateBanner() {
  const me = useSession()
  const [st, setSt] = useState<SystemStatus>()
  const [upgradingTo, setUpgradingTo] = useState<string>()
  const [error, setError] = useState<Error>()
  const [dismissed, setDismissed] = useState(dismissedVersion)

  useEffect(() => {
    let alive = true
    const load = () =>
      api.system().then(
        (s) => {
          if (!alive) return
          setSt(s)
          if (upgradingTo && s.version === upgradingTo) window.location.reload()
          if (s.upgrade_state !== 'idle' && s.latest) setUpgradingTo(s.latest)
          if (upgradingTo && s.upgrade_state === 'idle' && s.upgrade_error) {
            setUpgradingTo(undefined)
            setError(new Error(s.upgrade_error))
          }
        },
        () => {
          /* restarting during an upgrade, or offline: try again */
        },
      )
    load()
    const t = setInterval(load, upgradingTo ? 2000 : 60000)
    return () => {
      alive = false
      clearInterval(t)
    }
  }, [upgradingTo])

  if (!st) return null
  if (upgradingTo) {
    return (
      <div className="update-banner" role="status">
        <span className="spinner" aria-hidden />
        <div>
          <strong>Upgrading Coldship to {upgradingTo}…</strong>
          <div className="small muted">
            The server finishes running deployments, installs the new version and restarts. This page reloads when it is back.
          </div>
        </div>
      </div>
    )
  }
  if (!st.update_available || !st.latest || dismissed === st.latest) return null

  async function upgrade() {
    setError(undefined)
    try {
      const s = await api.upgrade()
      setUpgradingTo(s.latest)
    } catch (e) {
      setError(e as Error)
    }
  }

  const how = st.auto_blocked
    ? 'This is a major version change, so it will not install automatically. Read the release notes first.'
    : st.auto_upgrade
      ? 'It will be installed automatically when no deployment is running.'
      : 'Automatic upgrades are off.'

  return (
    <div className="update-banner" role="status">
      <div className="update-text">
        <div>
          <strong>Coldship {st.latest} is available</strong> <span className="muted">(you are on {st.version}).</span> {how}
        </div>
        {st.notes && <div className="small">{st.notes}</div>}
        {st.notes_url?.startsWith('https://') && (
          <a className="small" href={st.notes_url} target="_blank" rel="noreferrer">
            Release notes ↗
          </a>
        )}
        {error && <ErrorBox error={error} />}
      </div>
      <div className="update-actions">
        {me.scope === 'admin' ? (
          <button className="btn btn-primary btn-sm" onClick={upgrade}>
            Upgrade now
          </button>
        ) : (
          <span className="small muted">An admin can upgrade now.</span>
        )}
        <button
          className="btn btn-ghost btn-sm"
          onClick={() => {
            try {
              localStorage.setItem(DISMISS_KEY, st.latest!)
            } catch {
              /* ignore */
            }
            setDismissed(st.latest!)
          }}
        >
          Dismiss
        </button>
      </div>
    </div>
  )
}
