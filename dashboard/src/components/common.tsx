import type { ReactNode } from 'react'
import { AlertCircle, GitBranch } from 'lucide-react'
import { ApiError, type AppView, type Change, type Deployment, type Plan, type Spec, type Stage } from '@/api'
import { fmtTime, timeAgo, timeUntil } from '@/hooks'
import { cn } from '@/lib/utils'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Skeleton } from '@/components/ui/skeleton'

// Soft status colors (defined in index.css), for badges and pills.
const tone = {
  ok: 'bg-ok-soft text-ok border-transparent',
  warn: 'bg-warn-soft text-warn border-transparent',
  danger: 'bg-danger-soft text-destructive border-transparent',
  info: 'bg-info-soft text-info border-transparent',
  muted: 'bg-muted text-muted-foreground border-transparent',
  accent: 'bg-accent text-accent-foreground border-transparent',
}
export type Tone = keyof typeof tone

export function ToneBadge({ t, children, className }: { t: Tone; children: ReactNode; className?: string }) {
  return (
    <Badge variant="outline" className={cn('font-medium', tone[t], className)}>
      {children}
    </Badge>
  )
}

export function StageBadge({ stage }: { stage?: Stage }) {
  if (!stage) return null
  const t: Tone = stage === 'production' ? 'warn' : stage === 'preview' ? 'info' : 'accent'
  return <ToneBadge t={t}>{stage}</ToneBadge>
}

export type Health = 'healthy' | 'failing' | 'stopped' | 'deploying' | 'unknown'

export function healthOf(v: AppView | undefined): Health {
  if (!v) return 'unknown'
  const ld = v.latest_deployment
  if (ld?.status === 'deploying' || ld?.status === 'building') return 'deploying'
  const rt = v.runtime
  if (!rt) return 'unknown'
  if (!rt.exists) return 'stopped'
  // A failed deploy that kept the previous version running isn't an outage.
  const deployFailed = ld?.status === 'failed' && !ld.kept_previous
  if (rt.running && rt.restart_count === 0 && !deployFailed) return 'healthy'
  return 'failing'
}

const health: Record<Health, { label: string; dot: string; text: string }> = {
  healthy: { label: 'Healthy', dot: 'bg-ok', text: 'text-ok' },
  failing: { label: 'Failing', dot: 'bg-destructive', text: 'text-destructive' },
  stopped: { label: 'Not running', dot: 'bg-destructive', text: 'text-destructive' },
  deploying: { label: 'Deploying', dot: 'bg-info animate-pulse', text: 'text-info' },
  unknown: { label: 'Unknown', dot: 'bg-muted-foreground', text: 'text-muted-foreground' },
}

// StatusDot is a colored dot with a label; never color alone.
export function StatusDot({ dot, text, label }: { dot: string; text: string; label: ReactNode }) {
  return (
    <span className={cn('inline-flex items-center gap-1.5 text-sm font-medium', text)}>
      <span className={cn('size-2 rounded-full', dot)} aria-hidden />
      {label}
    </span>
  )
}

export function HealthPill({ health: h }: { health: Health }) {
  return <StatusDot {...health[h]} />
}

const depTone: Record<Deployment['status'], Tone> = {
  running: 'ok',
  failed: 'danger',
  building: 'info',
  deploying: 'info',
  superseded: 'muted',
  deleted: 'muted',
}

export function DeployStatus({ status }: { status: Deployment['status'] }) {
  return <ToneBadge t={depTone[status] ?? 'muted'}>{status}</ToneBadge>
}

// shortRepo turns https://github.com/acme/api.git into acme/api.
export function shortRepo(url: string): string {
  return url
    .replace(/^(https?|ssh):\/\/([^@/]*@)?[^/]+\//, '')
    .replace(/^[^@]+@[^:]+:/, '')
    .replace(/\.git$/, '')
}

// Source shows where an app's image comes from: an image, or repo@ref.
export function Source({ spec }: { spec?: Spec }) {
  if (!spec) return <>—</>
  if (!spec.build) return <Mono>{spec.image}</Mono>
  const b = spec.build
  return (
    <span className="inline-flex items-center gap-1.5" title={b.git}>
      <GitBranch className="size-3.5 text-muted-foreground" aria-hidden />
      <Mono>
        {shortRepo(b.git)}
        {b.ref ? `@${b.ref}` : ''}
      </Mono>
      {b.commit && <span className="font-mono text-xs text-muted-foreground">{b.commit.slice(0, 7)}</span>}
    </span>
  )
}

export function Mono({ children, className }: { children: ReactNode; className?: string }) {
  return <code className={cn('font-mono text-[0.8125rem]', className)}>{children}</code>
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
    <Alert variant="destructive" className="my-3">
      <AlertCircle />
      <AlertTitle className="font-normal">
        {ae && <Mono className="mr-1.5 font-semibold">{ae.code}</Mono>}
        {error.message}
      </AlertTitle>
      {(ae?.hint || ae?.suggestedFix !== undefined) && (
        <AlertDescription>
          {ae?.hint && <p>Hint: {ae.hint}</p>}
          {ae?.suggestedFix !== undefined && (
            <p>
              Suggested fix: <Mono>{JSON.stringify(ae.suggestedFix)}</Mono>
            </p>
          )}
        </AlertDescription>
      )}
    </Alert>
  )
}

export function Empty({ title, children, icon }: { title: string; children?: ReactNode; icon?: ReactNode }) {
  return (
    <div className="flex flex-col items-center justify-center gap-2 rounded-xl border border-dashed bg-card px-6 py-12 text-center">
      {icon && <div className="text-muted-foreground [&_svg]:size-8">{icon}</div>}
      <div className="font-semibold">{title}</div>
      {children && <div className="max-w-md text-sm text-muted-foreground">{children}</div>}
    </div>
  )
}

// PageHeader: title, an optional line under it, and actions on the right.
export function PageHeader({
  title,
  description,
  actions,
  crumbs,
}: {
  title: ReactNode
  description?: ReactNode
  actions?: ReactNode
  crumbs?: ReactNode
}) {
  return (
    <header className="mb-6 flex flex-wrap items-start justify-between gap-4">
      <div className="min-w-0">
        {crumbs && <div className="mb-1 text-sm text-muted-foreground">{crumbs}</div>}
        <h1 className="flex flex-wrap items-center gap-2 text-2xl font-semibold tracking-tight">{title}</h1>
        {description && <div className="mt-1 max-w-[70ch] text-sm text-muted-foreground">{description}</div>}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </header>
  )
}

// --- skeletons, shown while a page loads ---

export function TableSkeleton({ rows = 4, cols = 4 }: { rows?: number; cols?: number }) {
  return (
    <div className="overflow-hidden rounded-xl border bg-card">
      <div className="flex gap-6 border-b bg-muted/50 px-4 py-3">
        {Array.from({ length: cols }, (_, i) => (
          <Skeleton key={i} className="h-3 flex-1" />
        ))}
      </div>
      {Array.from({ length: rows }, (_, r) => (
        <div key={r} className="flex items-center gap-6 border-b px-4 py-4 last:border-0">
          {Array.from({ length: cols }, (_, c) => (
            <Skeleton key={c} className={cn('h-4 flex-1', c === 0 && 'max-w-40')} />
          ))}
        </div>
      ))}
    </div>
  )
}

export function CardsSkeleton({ count = 2, height = 'h-48' }: { count?: number; height?: string }) {
  return (
    <div className="grid gap-4 md:grid-cols-2">
      {Array.from({ length: count }, (_, i) => (
        <Skeleton key={i} className={cn('rounded-xl', height)} />
      ))}
    </div>
  )
}

export function PageSkeleton({ kind = 'table' }: { kind?: 'table' | 'detail' | 'form' }) {
  return (
    <div aria-busy="true" aria-label="Loading">
      <div className="mb-6 space-y-2">
        <Skeleton className="h-7 w-48" />
        <Skeleton className="h-4 w-80" />
      </div>
      {kind === 'table' && <TableSkeleton />}
      {kind === 'detail' && (
        <div className="space-y-4">
          <CardsSkeleton />
          <Skeleton className="h-9 w-80" />
          <Skeleton className="h-64 rounded-xl" />
        </div>
      )}
      {kind === 'form' && <Skeleton className="h-96 max-w-2xl rounded-xl" />}
    </div>
  )
}

// --- plans ---

const opSymbol: Record<Change['op'], string> = { create: '+', update: '~', delete: '−', redeploy: '↻' }
const opColor: Record<Change['op'], string> = {
  create: 'text-ok',
  update: 'text-info',
  delete: 'text-destructive',
  redeploy: 'text-muted-foreground',
}

function fmtVal(v: unknown) {
  return typeof v === 'string' ? v : JSON.stringify(v)
}

export function ChangeList({ changes }: { changes: Change[] }) {
  if (changes.length === 0) return <div className="text-sm text-muted-foreground">No changes.</div>
  return (
    <ul className="space-y-1.5">
      {changes.map((c, i) => (
        <li key={i} className={cn('flex flex-wrap items-baseline gap-2 text-sm', c.destructive && 'rounded-md bg-danger-soft px-2 py-1')}>
          <span className={cn('w-3 font-mono font-bold', opColor[c.op])}>{opSymbol[c.op]}</span>
          <Mono>{c.path}</Mono>
          {c.from !== undefined && c.to !== undefined ? (
            <span className="font-mono text-xs text-muted-foreground">
              <span className="line-through">{fmtVal(c.from)}</span> → <span className="text-foreground">{fmtVal(c.to)}</span>
            </span>
          ) : c.to !== undefined ? (
            <span className="font-mono text-xs">{fmtVal(c.to)}</span>
          ) : c.from !== undefined ? (
            <span className="font-mono text-xs text-muted-foreground line-through">{fmtVal(c.from)}</span>
          ) : null}
          {c.destructive && <ToneBadge t="danger">destructive</ToneBadge>}
          {c.reason && <span className="w-full pl-5 text-xs text-muted-foreground">{c.reason}</span>}
        </li>
      ))}
    </ul>
  )
}

export function PlanMeta({ plan }: { plan: Plan }) {
  return (
    <div className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
      <span>
        by <strong className="text-foreground">{plan.created_by}</strong> <Time iso={plan.created_at} />
      </span>
      {plan.status === 'pending' && <span>· expires {timeUntil(plan.expires_at)}</span>}
      {plan.downtime && <ToneBadge t="warn">downtime</ToneBadge>}
      {plan.destructive && <ToneBadge t="danger">destructive</ToneBadge>}
      {plan.approved_by && <ToneBadge t="ok">approved by {plan.approved_by}</ToneBadge>}
    </div>
  )
}

export function GitIcon() {
  return <GitBranch className="size-3.5" aria-hidden />
}
