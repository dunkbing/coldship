import { useEffect, useState } from 'react'
import { api, type SystemStatus } from '../api'
import { useSession } from '../App'
import { ArrowUpCircle, ExternalLink, Loader2 } from 'lucide-react'
import { ErrorBox } from '@/components/common'
import { Button } from '@/components/ui/button'

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
      <div className="mb-6 flex items-start gap-3 rounded-xl border bg-info-soft px-4 py-3 text-sm" role="status">
        <Loader2 className="mt-0.5 size-4 shrink-0 animate-spin text-info" aria-hidden />
        <div className="min-w-0">
          <strong className="font-semibold">Upgrading Coldship to {upgradingTo}…</strong>
          <div className="text-xs text-muted-foreground">
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
    <div className="mb-6 flex flex-wrap items-start gap-3 rounded-xl border bg-info-soft px-4 py-3 text-sm" role="status">
      <ArrowUpCircle className="mt-0.5 size-4 shrink-0 text-info" aria-hidden />
      <div className="min-w-0 flex-1 basis-64 space-y-1">
        <div>
          <strong className="font-semibold">Coldship {st.latest} is available</strong>{' '}
          <span className="text-muted-foreground">(you are on {st.version}).</span> {how}
        </div>
        {st.notes && <div className="text-xs">{st.notes}</div>}
        {st.notes_url?.startsWith('https://') && (
          <a className="inline-flex items-center gap-1 text-xs font-medium text-info hover:underline" href={st.notes_url} target="_blank" rel="noreferrer">
            Release notes <ExternalLink className="size-3" aria-hidden />
          </a>
        )}
        {error && <ErrorBox error={error} />}
      </div>
      <div className="flex shrink-0 flex-wrap items-center gap-2">
        {me.scope === 'admin' ? (
          <Button size="sm" onClick={upgrade}>
            Upgrade now
          </Button>
        ) : (
          <span className="text-xs text-muted-foreground">An admin can upgrade now.</span>
        )}
        <Button
          variant="ghost"
          size="sm"
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
        </Button>
      </div>
    </div>
  )
}
