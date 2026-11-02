import { cn } from '@/lib/utils'

export function bytes(n: number): string {
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i++
  }
  return `${n >= 10 || i === 0 ? n.toFixed(0) : n.toFixed(1)} ${units[i]}`
}

export function duration(s: number): string {
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  return d > 0 ? `${d}d ${h}h` : h > 0 ? `${h}h ${m}m` : `${m}m`
}

// Meter shows a used/total bar; it turns amber at 80% and red at 90%.
export function Meter({ label, used, total, detail }: { label: string; used: number; total: number; detail?: string }) {
  const pct = total > 0 ? Math.min(100, (100 * used) / total) : 0
  const level = pct >= 90 ? 'danger' : pct >= 80 ? 'warn' : 'ok'
  return (
    <div className="space-y-1.5">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5 text-sm">
        <span className="font-medium">{label}</span>
        <span className={cn('font-mono text-xs', level === 'ok' ? 'text-muted-foreground' : level === 'warn' ? 'text-warn' : 'text-destructive')}>
          {level !== 'ok' && <span className="font-sans font-medium">{level === 'warn' ? 'High · ' : 'Critical · '}</span>}
          {detail ?? `${pct.toFixed(0)}%`}
        </span>
      </div>
      <div
        className="h-2 overflow-hidden rounded-full bg-muted"
        role="meter"
        aria-valuenow={Math.round(pct)}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-label={label}
      >
        <span
          className={cn('block h-full rounded-full transition-[width]', level === 'ok' ? 'bg-ok' : level === 'warn' ? 'bg-warn' : 'bg-destructive')}
          style={{ width: `${pct}%` }}
        />
      </div>
    </div>
  )
}
