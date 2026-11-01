import type { ReactNode } from 'react'
import { ApiError, type AppView, type Change, type Deployment, type Plan, type Stage } from '../api'
import { fmtTime, timeAgo, timeUntil } from '../hooks'

export function StageBadge({ stage }: { stage?: Stage }) {
  if (!stage) return null
  return <span className={`badge stage-${stage}`}>{stage}</span>
}

export type Health = 'healthy' | 'failing' | 'stopped' | 'deploying' | 'unknown'

export function healthOf(v: AppView | undefined): Health {
  if (!v) return 'unknown'
  if (v.latest_deployment?.status === 'deploying') return 'deploying'
  const rt = v.runtime
  if (!rt) return 'unknown'
  if (!rt.exists) return 'stopped'
  if (rt.running && rt.restart_count === 0 && v.latest_deployment?.status !== 'failed') return 'healthy'
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
