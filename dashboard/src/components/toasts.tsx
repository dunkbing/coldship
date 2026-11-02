import { useEffect } from 'react'
import { useNavigate, type NavigateFunction } from 'react-router-dom'
import { toast } from 'sonner'
import { ApiError, type ApplyResult } from '@/api'
import { useIsMobile } from '@/hooks/use-mobile'
import { Toaster } from '@/components/ui/sonner'

// Toasts confirm what an action did. Errors stay inline (ErrorBox), since
// they carry a hint and a fix; secrets never go in a toast.

let go: NavigateFunction | undefined

// Toasts outlive the page that raised them, so their links use the root
// router's navigate.
function link(label: string, to: string) {
  return { label, onClick: () => go?.(to) }
}

export function Toasts() {
  const nav = useNavigate()
  const mobile = useIsMobile()
  useEffect(() => {
    go = nav
  }, [nav])
  // At the top on phones, below the header and clear of the agent composer.
  return <Toaster position={mobile ? 'top-center' : 'bottom-right'} offset={mobile ? { top: 56 } : undefined} mobileOffset={{ top: 56, left: 16, right: 16 }} closeButton />
}

export function notify(message: string, opts?: { description?: string; to?: string; label?: string }) {
  toast.success(message, {
    description: opts?.description,
    action: opts?.to ? link(opts.label ?? 'Open', opts.to) : undefined,
  })
}

export function failed(err: unknown, what?: string) {
  const e = err as Error
  toast.error(what ?? e.message, { duration: 12000, description: clip(what ? e.message : (e as ApiError).hint) })
}

// The full error is on the page a toast links to.
const clip = (s?: string) => (s && s.length > 160 ? s.slice(0, 157).trimEnd() + '…' : s)

const live = new Map<string, Promise<unknown>>()

// trackDeploy reports how an apply ends, even after its dialog closed or
// the page changed. Apply rejects when the deploy fails.
export function trackDeploy(app: string, applying: Promise<ApplyResult>, kind: 'deploy' | 'delete' = 'deploy') {
  if (live.get(app) === applying) return
  live.set(app, applying)
  // The loading toast steps aside after a while; the result shows up anyway.
  const id = toast.loading(kind === 'delete' ? `Deleting ${app}…` : `Deploying ${app}…`, {
    duration: 8000,
    action: kind === 'deploy' ? link('Watch', `/apps/${app}?tab=deployments`) : undefined,
  })
  applying.then(
    (r) => {
      if (kind === 'delete') toast.success(`Deleted ${app}`, { id, action: undefined })
      else if (!r.deployment) toast.success(r.message ? `${app}: ${r.message}` : `${app} updated`, { id, action: undefined })
      else toast.success(`${app} is live`, { id, action: link('Open', `/apps/${app}`) })
    },
    (err: unknown) => {
      const ae = err instanceof ApiError ? err : undefined
      const kept = ae?.details?.previous_version_running === true
      const opts = { id, duration: 12000, description: clip(ae?.message ?? String(err)), action: link('See the log', `/apps/${app}?tab=deployments`) }
      if (kind === 'delete') toast.error(`${app} was not deleted`, opts)
      else if (kept) toast.warning(`${app} didn't deploy; the previous version is still serving`, opts)
      else toast.error(`${app} failed to deploy`, opts)
    },
  ).finally(() => {
    if (live.get(app) === applying) live.delete(app)
  })
}
