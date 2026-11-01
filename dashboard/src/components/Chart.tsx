import { useLayoutEffect, useRef, useState } from 'react'
import { cn } from '@/lib/utils'

export interface Series {
  name: string
  slot: 1 | 2 // categorical slot: chart-1 (blue), chart-2 (orange)
  values: number[]
}

// Fewest "nice" ticks (1, 2, 5 × 10^n, in units of 1024^k for bytes) covering max.
function niceMax(max: number, binary = false): { top: number; step: number } {
  if (max <= 0) return { top: 1, step: 0.5 }
  if (binary) {
    const unit = 1024 ** Math.max(0, Math.floor(Math.log(max) / Math.log(1024)))
    const n = niceMax(max / unit)
    return { top: n.top * unit, step: n.step * unit }
  }
  const raw = max / 3
  const pow = 10 ** Math.floor(Math.log10(raw))
  const step = [1, 2, 5, 10].map((m) => m * pow).find((s) => s >= raw) ?? 10 * pow
  return { top: Math.ceil(max / step) * step, step }
}

const H = 150

// Literal class names per slot, so Tailwind sees them.
const stroke = { 1: 'stroke-chart-1', 2: 'stroke-chart-2' } as const
const dot = { 1: 'fill-chart-1', 2: 'fill-chart-2' } as const
const swatch = { 1: 'bg-chart-1', 2: 'bg-chart-2' } as const
const PAD = { top: 10, right: 54, bottom: 22, left: 52 }

// LineChart plots one measure over time: one or two series, one y-axis.
export function LineChart({
  title,
  times,
  series,
  format,
  refLine,
  minTop = 0,
  binary = false,
}: {
  title: string
  times: Date[]
  series: Series[]
  format: (v: number) => string
  refLine?: { value: number; label: string }
  minTop?: number // smallest axis top, so tiny values don't get a microscopic scale
  binary?: boolean // byte values: ticks in powers of 1024
}) {
  const wrap = useRef<HTMLDivElement>(null)
  const [w, setW] = useState(480)
  const [hover, setHover] = useState<number>()
  useLayoutEffect(() => {
    const el = wrap.current
    if (!el) return
    const ro = new ResizeObserver(() => setW(el.clientWidth))
    ro.observe(el)
    setW(el.clientWidth)
    return () => ro.disconnect()
  }, [])

  const n = times.length
  const max = Math.max(minTop, ...series.flatMap((s) => s.values), refLine?.value ?? 0)
  const { top, step } = niceMax(max, binary)
  const iw = Math.max(10, w - PAD.left - PAD.right)
  const ih = H - PAD.top - PAD.bottom
  const x = (i: number) => PAD.left + (n <= 1 ? iw : (i / (n - 1)) * iw)
  const y = (v: number) => PAD.top + ih - (v / top) * ih
  const ticks: number[] = []
  for (let v = 0; v <= top + step / 2; v += step) ticks.push(v)
  const fmtTime = (d: Date) => d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  const last = series.map((s) => s.values[n - 1] ?? 0)
  // End labels: keep two at least 12px apart.
  const labelY = last.map((v) => y(v) + 4)
  if (labelY.length === 2 && Math.abs(labelY[0] - labelY[1]) < 12) {
    const mid = (labelY[0] + labelY[1]) / 2
    const up = labelY[0] <= labelY[1] ? 0 : 1
    labelY[up] = mid - 6
    labelY[1 - up] = mid + 6
  }

  function onMove(e: React.MouseEvent<SVGRectElement>) {
    const r = e.currentTarget.getBoundingClientRect()
    const i = Math.round(((e.clientX - r.left) / r.width) * (n - 1))
    setHover(Math.max(0, Math.min(n - 1, i)))
  }

  return (
    <figure className="min-w-0 rounded-xl border bg-card p-4">
      <figcaption className="mb-2 flex flex-wrap items-baseline gap-x-3 gap-y-1 text-sm">
        <span className="font-medium">{title}</span>
        {series.length > 1 && (
          <span className="flex items-center gap-3 text-xs text-muted-foreground">
            {series.map((s) => (
              <span key={s.name} className="inline-flex items-center gap-1.5">
                <span className={cn('inline-block h-0.5 w-3 rounded-full', swatch[s.slot])} aria-hidden />
                {s.name}
              </span>
            ))}
          </span>
        )}
        <span className="ml-auto font-mono text-xs text-muted-foreground">
          {series.map((s, i) => (series.length > 1 ? `${s.name} ` : '') + format(last[i])).join(' · ')}
        </span>
      </figcaption>
      <div className="relative" ref={wrap}>
        {n < 2 ? (
          <div className="flex h-[150px] items-center justify-center text-sm text-muted-foreground">
            Collecting… a point every 15 seconds.
          </div>
        ) : (
          <svg width={w} height={H} role="img" aria-label={`${title}, last hour`} className="block overflow-visible text-[11px]">
            {ticks.map((t) => (
              <g key={t}>
                <line className="stroke-border" x1={PAD.left} x2={PAD.left + iw} y1={y(t)} y2={y(t)} />
                <text className="fill-muted-foreground" x={PAD.left - 6} y={y(t) + 4} textAnchor="end">
                  {format(t)}
                </text>
              </g>
            ))}
            <text className="fill-muted-foreground" x={PAD.left} y={H - 4}>
              {fmtTime(times[0])}
            </text>
            <text className="fill-muted-foreground" x={PAD.left + iw} y={H - 4} textAnchor="end">
              {fmtTime(times[n - 1])}
            </text>
            {refLine && refLine.value <= top && (
              <g>
                <line
                  className="stroke-muted-foreground"
                  strokeDasharray="4 3"
                  x1={PAD.left}
                  x2={PAD.left + iw}
                  y1={y(refLine.value)}
                  y2={y(refLine.value)}
                />
                <text className="fill-muted-foreground" x={PAD.left + iw + 4} y={y(refLine.value) + 4}>
                  {refLine.label}
                </text>
              </g>
            )}
            {series.map((s) => (
              <polyline
                key={s.name}
                className={cn('fill-none', stroke[s.slot])}
                strokeWidth={2}
                strokeLinejoin="round"
                strokeLinecap="round"
                points={s.values.map((v, i) => `${x(i)},${y(v)}`).join(' ')}
              />
            ))}
            {series.length > 1 &&
              series.map((s, i) => (
                <text key={s.name} className="fill-foreground font-medium" x={PAD.left + iw + 4} y={labelY[i]}>
                  {s.name}
                </text>
              ))}
            {hover !== undefined && (
              <g>
                <line className="stroke-muted-foreground" x1={x(hover)} x2={x(hover)} y1={PAD.top} y2={PAD.top + ih} />
                {series.map((s) => (
                  <circle
                    key={s.name}
                    className={cn('stroke-card', dot[s.slot])}
                    strokeWidth={2}
                    cx={x(hover)}
                    cy={y(s.values[hover] ?? 0)}
                    r={4}
                  />
                ))}
              </g>
            )}
            <rect
              x={PAD.left}
              y={PAD.top}
              width={iw}
              height={ih}
              fill="transparent"
              onMouseMove={onMove}
              onMouseLeave={() => setHover(undefined)}
            />
          </svg>
        )}
        {hover !== undefined && n >= 2 && (
          <div
            className="pointer-events-none absolute top-2 z-10 min-w-36 rounded-md border bg-popover px-2.5 py-1.5 text-popover-foreground shadow-md"
            style={{ left: Math.max(0, Math.min(x(hover) + 10, w - 170)) }}
          >
            <div className="text-xs text-muted-foreground">{fmtTime(times[hover])}</div>
            {series.map((s) => (
              <div key={s.name} className="flex items-center gap-1.5 text-xs">
                <span className={cn('inline-block size-2 rounded-full', swatch[s.slot])} aria-hidden /> {s.name}:{' '}
                <b className="font-mono">{format(s.values[hover] ?? 0)}</b>
              </div>
            ))}
          </div>
        )}
      </div>
    </figure>
  )
}
