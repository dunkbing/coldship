import { useEffect, useRef, useState } from 'react'

// LiveLog follows a deployment's log as it is written: build steps, image
// pulls, the container start and health checks. onEnd gets the deployment's
// final status. The lines come from the build and the app: plain text only.
export function LiveLog({ deployment, onEnd }: { deployment: string; onEnd?: (status: string) => void }) {
  const [lines, setLines] = useState<string[]>([])
  const [status, setStatus] = useState<string>()
  const box = useRef<HTMLPreElement>(null)
  const follow = useRef(true)
  const endRef = useRef(onEnd)
  endRef.current = onEnd

  useEffect(() => {
    setLines([])
    setStatus(undefined)
    const es = new EventSource(`/v1/deployments/${encodeURIComponent(deployment)}/log/stream`)
    es.onmessage = (e) => setLines((ls) => (ls.length > 2000 ? [...ls.slice(-1500), e.data] : [...ls, e.data]))
    es.addEventListener('end', (e) => {
      const s = (e as MessageEvent).data as string
      setStatus(s)
      es.close()
      endRef.current?.(s)
    })
    es.onerror = () => {
      // The server ends the stream after the "end" event; anything else is
      // a dropped connection, which EventSource retries on its own.
    }
    return () => es.close()
  }, [deployment])

  useEffect(() => {
    const el = box.current
    if (el && follow.current) el.scrollTop = el.scrollHeight
  }, [lines])

  const running = status === undefined
  return (
    <div className="live-log">
      <div className="live-log-head">
        <span className={`pill ${running ? 'health-deploying' : status === 'running' ? 'health-healthy' : 'health-failing'}`}>
          <span className="dot" aria-hidden />
          {running ? 'In progress' : status === 'running' ? 'Deployed' : status === 'failed' ? 'Failed' : status}
        </span>
        <span className="muted small mono">{deployment}</span>
      </div>
      <pre
        ref={box}
        className="logs log-box"
        onScroll={(e) => {
          const el = e.currentTarget
          follow.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40
        }}
      >
        {lines.length === 0 ? 'Waiting for output…' : lines.join('\n')}
        {running && '\n▍'}
      </pre>
    </div>
  )
}
