import { useCallback, useEffect, useRef, useState } from 'react'

export interface Loadable<T> {
  data: T | undefined
  error: Error | undefined
  loading: boolean
  reload: () => void
}

// useLoad runs fn on mount and whenever deps change, optionally polling.
export function useLoad<T>(fn: () => Promise<T>, deps: unknown[], pollMs?: number): Loadable<T> {
  const [data, setData] = useState<T>()
  const [error, setError] = useState<Error>()
  const [loading, setLoading] = useState(true)
  const fnRef = useRef(fn)
  fnRef.current = fn
  const seq = useRef(0)

  const reload = useCallback(() => {
    const id = ++seq.current
    fnRef.current().then(
      (d) => {
        if (id !== seq.current) return
        setData(d)
        setError(undefined)
        setLoading(false)
      },
      (e: Error) => {
        if (id !== seq.current) return
        setError(e)
        setLoading(false)
      },
    )
  }, [])

  useEffect(() => {
    setLoading(true)
    reload()
    if (!pollMs) return
    const t = setInterval(reload, pollMs)
    return () => clearInterval(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps)

  return { data, error, loading, reload }
}

export function timeAgo(iso: string | undefined, now = Date.now()): string {
  if (!iso) return '—'
  const s = Math.round((now - new Date(iso).getTime()) / 1000)
  if (s < 0) return timeUntil(iso, now)
  if (s < 60) return `${s}s ago`
  if (s < 3600) return `${Math.floor(s / 60)}m ago`
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`
  return `${Math.floor(s / 86400)}d ago`
}

export function timeUntil(iso: string, now = Date.now()): string {
  const s = Math.round((new Date(iso).getTime() - now) / 1000)
  if (s <= 0) return 'expired'
  if (s < 60) return `in ${s}s`
  if (s < 3600) return `in ${Math.floor(s / 60)}m`
  return `in ${Math.floor(s / 3600)}h`
}

export function fmtTime(iso: string | undefined): string {
  if (!iso) return '—'
  return new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'medium' })
}
