import type { ReactNode } from 'react'
import { ApiError, type AppView, type Change, type Deployment, type Plan, type Spec, type Stage } from '../api'
import { fmtTime, timeAgo, timeUntil } from '../hooks'

export function StageBadge({ stage }: { stage?: Stage }) {
  if (!stage) return null
  return <span className={`badge stage-${stage}`}>{stage}</span>
}

export type Health = 'healthy' | 'failing' | 'stopped' | 'deploying' | 'unknown'

export function healthOf(v: AppView | undefined): Health {
  if (!v) return 'unknown'
  const ld = v.latest_deployment
  if (ld?.status === 'deploying' || ld?.status === 'building') return 'deploying'
  const rt = v.runtime
  if (!rt) return 'unknown'
  if (!rt.exists) return 'stopped'
  // A failed deployment that kept the previous version running (failed
  // build, or a new version that never got healthy) is not an outage.
  const deployFailed = ld?.status === 'failed' && !ld.kept_previous
  if (rt.running && rt.restart_count === 0 && !deployFailed) return 'healthy'
  return 'failing'
}

const healthLabel: Record<Health, string> = {
  healthy: 'Healthy',
  failing: 'Failing',
  stopped: 'Not running',
  deploying: 'Deploying',
  unknown: 'Unknown',
}

export function HealthPill({ health }: { health: Health }) {
  return (
    <span className={`pill health-${health}`}>
      <span className="dot" aria-hidden />
      {healthLabel[health]}
    </span>
  )
}

export function DeployStatus({ status }: { status: Deployment['status'] }) {
  return <span className={`badge dep-${status}`}>{status}</span>
}

// shortRepo turns https://github.com/acme/api.git into acme/api.
export function shortRepo(url: string): string {
  return url
    .replace(/^(https?|ssh):\/\/([^@/]*@)?[^/]+\//, '')
    .replace(/^[^@]+@[^:]+:/, '')
    .replace(/\.git$/, '')
}

// Source shows where an app's image comes from: an image, or repo@ref (commit).
export function Source({ spec }: { spec?: Spec }) {
  if (!spec) return <>—</>
  if (!spec.build) return <Mono>{spec.image}</Mono>
  const b = spec.build
  return (
    <span className="source" title={b.git}>
      <GitIcon />
      <Mono>
        {shortRepo(b.git)}
        {b.ref ? `@${b.ref}` : ''}
      </Mono>
      {b.commit && <span className="muted mono small">{b.commit.slice(0, 7)}</span>}
    </span>
  )
}

export function GitIcon() {
  return (
    <svg className="git-icon" viewBox="0 0 16 16" width="13" height="13" aria-hidden>
      <circle cx="4" cy="3.5" r="1.8" fill="none" stroke="currentColor" strokeWidth="1.4" />
      <circle cx="4" cy="12.5" r="1.8" fill="none" stroke="currentColor" strokeWidth="1.4" />
      <circle cx="12" cy="6" r="1.8" fill="none" stroke="currentColor" strokeWidth="1.4" />
      <path d="M4 5.3v5.4M12 7.8c0 2.4-3 2.6-7 3.6" fill="none" stroke="currentColor" strokeWidth="1.4" />
    </svg>
  )
}

export function Mono({ children }: { children: ReactNode }) {
  return <code className="mono">{children}</code>
}

export function Time({ iso }: { iso?: string }) {
  return (
    <time dateTime={iso} title={fmtTime(iso)}>
      {timeAgo(iso)}
    </time>
  )
}

export function ErrorBox({ error }: { error: Error }) {
  const ae = error instanceof ApiError ? error : undefined
  return (
    <div className="callout callout-error" role="alert">
      <div>
        {ae && <Mono>{ae.code}</Mono>} {error.message}
      </div>
      {ae?.hint && <div className="muted small">Hint: {ae.hint}</div>}
      {ae?.suggestedFix !== undefined && (
        <div className="muted small">
          Suggested fix: <Mono>{JSON.stringify(ae.suggestedFix)}</Mono>
        </div>
      )}
    </div>
  )
}

export function Empty({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="empty">
      <div className="empty-title">{title}</div>
      {children && <div className="muted">{children}</div>}
    </div>
  )
}

const opSymbol: Record<Change['op'], string> = { create: '+', update: '~', delete: '−', redeploy: '↻' }

function fmtVal(v: unknown) {
  return typeof v === 'string' ? v : JSON.stringify(v)
}

export function ChangeList({ changes }: { changes: Change[] }) {
  if (changes.length === 0) return <div className="muted small">No changes.</div>
  return (
    <ul className="changes">
      {changes.map((c, i) => (
        <li key={i} className={`change op-${c.op}${c.destructive ? ' destructive' : ''}`}>
          <span className="op">{opSymbol[c.op]}</span>
          <span className="mono">{c.path}</span>
          {c.from !== undefined && c.to !== undefined ? (
            <span className="vals">
              <span className="from">{fmtVal(c.from)}</span> → <span className="to">{fmtVal(c.to)}</span>
            </span>
          ) : c.to !== undefined ? (
            <span className="vals to">{fmtVal(c.to)}</span>
          ) : c.from !== undefined ? (
            <span className="vals from">{fmtVal(c.from)}</span>
          ) : null}
          {c.destructive && <span className="badge danger">destructive</span>}
          {c.reason && <span className="change-reason">{c.reason}</span>}
        </li>
      ))}
    </ul>
  )
}

export function PlanMeta({ plan }: { plan: Plan }) {
  return (
    <div className="plan-meta">
      <span>
        by <strong>{plan.created_by}</strong> <Time iso={plan.created_at} />
      </span>
      {plan.status === 'pending' && <span>expires {timeUntil(plan.expires_at)}</span>}
      {plan.downtime && <span className="badge warn">downtime</span>}
      {plan.destructive && <span className="badge danger">destructive</span>}
      {plan.approved_by && <span className="badge ok">approved by {plan.approved_by}</span>}
    </div>
  )
}
