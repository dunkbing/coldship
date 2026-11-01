import { useLayoutEffect, useRef, useState } from 'react'

export interface Series {
  name: string
  slot: 1 | 2 // categorical slot: --series-1 (blue), --series-2 (orange)
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
    <figure className="chart" ref={wrap}>
      <figcaption className="chart-head">
        <span className="chart-title">{title}</span>
        {series.length > 1 && (
          <span className="chart-legend">
            {series.map((s) => (
              <span key={s.name} className="legend-item">
                <span className={`legend-swatch series-${s.slot}`} aria-hidden />
                {s.name}
              </span>
            ))}
          </span>
        )}
        <span className="chart-now mono">{series.map((s, i) => (series.length > 1 ? `${s.name} ` : '') + format(last[i])).join(' · ')}</span>
      </figcaption>
      {n < 2 ? (
        <div className="chart-empty muted small">Collecting… a point every 15 seconds.</div>
      ) : (
        <svg width={w} height={H} role="img" aria-label={`${title}, last hour`}>
          {ticks.map((t) => (
            <g key={t}>
              <line className="chart-grid" x1={PAD.left} x2={PAD.left + iw} y1={y(t)} y2={y(t)} />
              <text className="chart-axis" x={PAD.left - 6} y={y(t) + 4} textAnchor="end">
                {format(t)}
              </text>
            </g>
          ))}
          <text className="chart-axis" x={PAD.left} y={H - 4}>
            {fmtTime(times[0])}
          </text>
          <text className="chart-axis" x={PAD.left + iw} y={H - 4} textAnchor="end">
            {fmtTime(times[n - 1])}
          </text>
          {refLine && refLine.value <= top && (
            <g>
              <line className="chart-ref" x1={PAD.left} x2={PAD.left + iw} y1={y(refLine.value)} y2={y(refLine.value)} />
              <text className="chart-axis" x={PAD.left + iw + 4} y={y(refLine.value) + 4}>
                {refLine.label}
              </text>
            </g>
          )}
          {series.map((s) => (
            <polyline
              key={s.name}
              className={`chart-line series-${s.slot}`}
              points={s.values.map((v, i) => `${x(i)},${y(v)}`).join(' ')}
            />
          ))}
          {series.length > 1 &&
            series.map((s, i) => (
              <text key={s.name} className="chart-label" x={PAD.left + iw + 4} y={labelY[i]}>
                {s.name}
              </text>
            ))}
          {hover !== undefined && (
            <g>
              <line className="chart-cross" x1={x(hover)} x2={x(hover)} y1={PAD.top} y2={PAD.top + ih} />
              {series.map((s) => (
                <circle key={s.name} className={`chart-dot series-${s.slot}`} cx={x(hover)} cy={y(s.values[hover] ?? 0)} r={4} />
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
        <div className="chart-tip" style={{ left: Math.min(x(hover) + 10, w - 170) }}>
          <div className="muted small">{fmtTime(times[hover])}</div>
          {series.map((s) => (
            <div key={s.name} className="small">
              <span className={`legend-swatch series-${s.slot}`} aria-hidden /> {s.name}: <b>{format(s.values[hover] ?? 0)}</b>
            </div>
          ))}
        </div>
      )}
    </figure>
  )
}
