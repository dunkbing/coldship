import { useEffect, useLayoutEffect, useRef, useState, type FormEvent, type KeyboardEvent } from 'react'
import { Link, useParams } from 'react-router-dom'
import {
  ArrowLeft,
  Brain,
  CheckCircle2,
  ChevronRight,
  Circle,
  CircleDashed,
  Loader2,
  Send,
  ShieldQuestion,
  Square,
  SquareTerminal,
  Wrench,
  XCircle,
} from 'lucide-react'
import { api, type AgentSession as Session, type SessionEvent, type SessionItem } from '../api'
import { useSession } from '../App'
import { ErrorBox, PageSkeleton, ToneBadge } from '@/components/common'
import { AgentMark, AgentText, agentLabels } from '@/components/agent'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { SessionStatusDot } from './Agents'

// useLiveSession follows a session over server-sent events: a snapshot,
// then item and status changes. EventSource reconnects by itself and the
// server starts each connection with a fresh snapshot.
function useLiveSession(id: string) {
  const [session, setSession] = useState<Session>()
  const [items, setItems] = useState<SessionItem[]>([])
  const [error, setError] = useState<Error>()

  useEffect(() => {
    let alive = true
    api.session(id).then(
      (s) => {
        if (!alive) return
        setSession(s)
        setItems(s.items ?? [])
      },
      (e) => alive && setError(e),
    )
    const es = new EventSource(`/v1/agent-sessions/${encodeURIComponent(id)}/stream`)
    es.onmessage = (m) => {
      const e = JSON.parse(m.data) as SessionEvent
      if (e.type === 'snapshot' && e.session) {
        setSession(e.session)
        setItems(e.session.items ?? [])
      } else if (e.type === 'status') {
        setSession((s) => (s ? { ...s, status: e.status!, error: e.error } : s))
      } else if (e.type === 'item' && e.item) {
        const it = e.item
        setItems((list) => {
          const i = list.findIndex((x) => x.key === it.key)
          if (i < 0) return [...list, it]
          const next = list.slice()
          next[i] = it
          return next
        })
      }
    }
    return () => {
      alive = false
      es.close()
    }
  }, [id])

  return { session, items, error }
}

export default function AgentSession() {
  const { id = '' } = useParams()
  const me = useSession()
  const { session, items, error } = useLiveSession(id)
  const [text, setText] = useState('')
  const [actionError, setActionError] = useState<Error>()
  const scroller = useRef<HTMLDivElement>(null)
  const stick = useRef(true)

  // Follow new output while the reader is at the bottom.
  useLayoutEffect(() => {
    const el = document.scrollingElement
    if (el && stick.current) el.scrollTop = el.scrollHeight
  }, [items])
  useEffect(() => {
    const onScroll = () => {
      const el = document.scrollingElement
      if (el) stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 120
    }
    window.addEventListener('scroll', onScroll, { passive: true })
    return () => window.removeEventListener('scroll', onScroll)
  }, [])

  if (!session) return error ? <ErrorBox error={error} /> : <PageSkeleton kind="detail" />

  const canAct = me.scope !== 'read'
  const live = session.status !== 'ended' && session.status !== 'failed'

  async function act(fn: () => Promise<unknown>) {
    setActionError(undefined)
    try {
      await fn()
    } catch (e) {
      setActionError(e as Error)
    }
  }

  function send(e?: FormEvent) {
    e?.preventDefault()
    const t = text.trim()
    if (!t) return
    stick.current = true
    act(async () => {
      await api.prompt(id, t)
      setText('')
    })
  }

  function onKey(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
      e.preventDefault()
      send()
    }
  }

  return (
    <div className="mx-auto flex max-w-3xl flex-col">
      <div className="mb-4">
        <Link to="/agents" className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
          <ArrowLeft className="size-4" /> Agents
        </Link>
      </div>
      <header className="mb-4 flex flex-wrap items-center gap-3">
        <AgentMark kind={session.kind} size={40} />
        <div className="min-w-0 flex-1">
          <h1 className="text-xl font-semibold tracking-tight">{session.agent}</h1>
          <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
            <SessionStatusDot status={session.status} />
            <span>· {session.agent_info || agentLabels[session.kind] || session.kind}</span>
            <ToneBadge t={session.scope === 'deploy' ? 'info' : 'muted'}>{session.scope === 'deploy' ? 'Deployer' : 'Viewer'}</ToneBadge>
            <span>· started by {session.started_by}</span>
          </div>
        </div>
        {live && canAct && (
          <Button variant="outline" size="sm" onClick={() => act(() => api.endSession(id))}>
            End session
          </Button>
        )}
      </header>

      <div ref={scroller} className="flex-1 space-y-3 pb-40" aria-live="polite">
        {items.map((it) => (
          <Entry key={it.key} item={it} sessionID={id} canAct={canAct && live} onError={setActionError} />
        ))}
        {session.status === 'working' && (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="size-4 animate-spin" /> Working…
          </div>
        )}
        {session.status === 'starting' && (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="size-4 animate-spin" /> Starting the agent (the first start downloads it, which can take a minute)…
          </div>
        )}
        {session.status === 'failed' && session.error && <ErrorBox error={new Error(session.error)} />}
      </div>

      {live && canAct && (
        <form
          onSubmit={send}
          className="sticky bottom-0 -mx-4 border-t bg-background/90 px-4 pt-3 pb-4 backdrop-blur md:mx-0 md:rounded-t-xl md:border-x"
        >
          {actionError && <ErrorBox error={actionError} />}
          <div className="flex items-end gap-2">
            <Textarea
              value={text}
              onChange={(e) => setText(e.target.value)}
              onKeyDown={onKey}
              rows={2}
              placeholder={session.status === 'ready' ? 'Ask the agent: “why is web failing?”, “deploy my repo”…' : 'Wait for the agent…'}
              className="max-h-48 min-h-11 resize-none"
              aria-label="Message"
            />
            {session.status === 'working' ? (
              <Button type="button" variant="outline" onClick={() => act(() => api.cancelTurn(id))} aria-label="Stop">
                <Square /> Stop
              </Button>
            ) : (
              <Button type="submit" disabled={session.status !== 'ready' || !text.trim()} aria-label="Send">
                <Send /> Send
              </Button>
            )}
          </div>
          <p className="mt-1.5 text-xs text-muted-foreground">Enter sends, Shift+Enter for a new line. Answers come from an AI and can be wrong.</p>
        </form>
      )}
      {!live && (
        <p className="mt-4 text-center text-sm text-muted-foreground">
          This session has {session.status === 'failed' ? 'failed' : 'ended'}. <Link to="/agents" className="text-primary hover:underline">Start a new one</Link>.
        </p>
      )}
    </div>
  )
}

function Entry({ item, sessionID, canAct, onError }: { item: SessionItem; sessionID: string; canAct: boolean; onError: (e: Error) => void }) {
  switch (item.kind) {
    case 'user':
      return (
        <div className="flex justify-end">
          <div className="max-w-[85%] rounded-2xl rounded-br-md bg-primary px-4 py-2.5 text-primary-foreground">
            <div className="text-sm whitespace-pre-wrap wrap-anywhere">{item.text}</div>
            {item.by && <div className="mt-1 text-right text-[0.6875rem] opacity-75">{item.by}</div>}
          </div>
        </div>
      )
    case 'agent':
      return (
        <div className="max-w-[95%]">
          <AgentText text={item.text ?? ''} />
        </div>
      )
    case 'thought':
      return (
        <Collapsible icon={<Brain className="size-4" />} title="Thinking" muted>
          <div className="text-sm whitespace-pre-wrap text-muted-foreground wrap-anywhere">{item.text}</div>
        </Collapsible>
      )
    case 'tool':
      return <Tool item={item} />
    case 'plan':
      return (
        <div className="rounded-xl border bg-card p-3">
          <div className="mb-2 text-xs font-medium text-muted-foreground uppercase">Plan</div>
          <ul className="space-y-1.5">
            {item.plan?.map((p, i) => (
              <li key={i} className="flex items-start gap-2 text-sm">
                {p.status === 'completed' ? (
                  <CheckCircle2 className="mt-0.5 size-4 flex-none text-ok" />
                ) : p.status === 'in_progress' ? (
                  <CircleDashed className="mt-0.5 size-4 flex-none animate-spin text-info [animation-duration:3s]" />
                ) : (
                  <Circle className="mt-0.5 size-4 flex-none text-muted-foreground" />
                )}
                <span className={cn(p.status === 'completed' && 'text-muted-foreground line-through')}>{p.content}</span>
              </li>
            ))}
          </ul>
        </div>
      )
    case 'permission':
      return <Permission item={item} sessionID={sessionID} canAct={canAct} onError={onError} />
    case 'log':
      return (
        <Collapsible icon={<SquareTerminal className="size-4" />} title="Startup output" muted>
          <pre className="overflow-x-auto font-mono text-xs whitespace-pre-wrap text-muted-foreground wrap-anywhere">{item.text}</pre>
        </Collapsible>
      )
    default:
      return <p className="text-center text-xs text-muted-foreground">{item.text}</p>
  }
}

function Collapsible({ icon, title, children, muted, extra }: { icon: React.ReactNode; title: React.ReactNode; children: React.ReactNode; muted?: boolean; extra?: React.ReactNode }) {
  const [open, setOpen] = useState(false)
  return (
    <div className={cn('rounded-xl border', muted ? 'bg-muted/30' : 'bg-card')}>
      <button
        type="button"
        onClick={() => setOpen(!open)}
        aria-expanded={open}
        className="flex w-full items-center gap-2 px-3 py-2 text-left text-sm focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
      >
        <ChevronRight className={cn('size-4 flex-none text-muted-foreground transition-transform', open && 'rotate-90')} />
        <span className="flex-none text-muted-foreground">{icon}</span>
        <span className="min-w-0 flex-1 truncate">{title}</span>
        {extra}
      </button>
      {open && <div className="border-t px-3 py-2.5">{children}</div>}
    </div>
  )
}

function Tool({ item }: { item: SessionItem }) {
  const t = item.tool!
  const status =
    t.status === 'completed' ? (
      <CheckCircle2 className="size-4 text-ok" aria-label="done" />
    ) : t.status === 'failed' ? (
      <XCircle className="size-4 text-destructive" aria-label="failed" />
    ) : (
      <Loader2 className="size-4 animate-spin text-muted-foreground" aria-label="running" />
    )
  return (
    <Collapsible icon={<Wrench className="size-4" />} title={<span className="font-mono text-[0.8125rem]">{t.title}</span>} extra={status}>
      {t.input && (
        <>
          <div className="mb-1 text-xs font-medium text-muted-foreground">Input</div>
          <pre className="mb-2 max-h-64 overflow-auto rounded-lg bg-muted p-2 font-mono text-xs whitespace-pre-wrap wrap-anywhere">{t.input}</pre>
        </>
      )}
      {t.output ? (
        <>
          <div className="mb-1 text-xs font-medium text-muted-foreground">Output</div>
          <pre className="max-h-80 overflow-auto rounded-lg bg-muted p-2 font-mono text-xs whitespace-pre-wrap wrap-anywhere">{t.output}</pre>
        </>
      ) : (
        !t.input && <p className="text-xs text-muted-foreground">No details yet.</p>
      )}
    </Collapsible>
  )
}

function Permission({ item, sessionID, canAct, onError }: { item: SessionItem; sessionID: string; canAct: boolean; onError: (e: Error) => void }) {
  const p = item.permission!
  const [busy, setBusy] = useState(false)
  const pending = p.status === 'pending'
  const chosen = p.options.find((o) => o.optionId === p.chosen)
  return (
    <div className={cn('rounded-xl border p-3', pending ? 'border-warn/50 bg-warn-soft/40' : 'bg-card')}>
      <div className="flex items-start gap-2">
        <ShieldQuestion className={cn('mt-0.5 size-4 flex-none', pending ? 'text-warn' : 'text-muted-foreground')} />
        <div className="min-w-0 flex-1">
          <div className="text-sm">
            {pending ? 'The agent asks to run ' : 'Asked to run '}
            <span className="font-mono text-[0.8125rem] font-medium">{p.title}</span>
          </div>
          {p.input && (
            <pre className="mt-2 max-h-48 overflow-auto rounded-lg bg-muted p-2 font-mono text-xs whitespace-pre-wrap wrap-anywhere">{p.input}</pre>
          )}
          {pending && canAct && (
            <div className="mt-3 flex flex-wrap gap-2">
              {p.options.map((o) => (
                <Button
                  key={o.optionId}
                  size="sm"
                  disabled={busy}
                  variant={o.kind.startsWith('allow') ? (o.kind === 'allow_once' ? 'default' : 'outline') : 'outline'}
                  onClick={() => {
                    setBusy(true)
                    api.answer(sessionID, p.id, o.optionId).catch((e) => {
                      onError(e)
                      setBusy(false)
                    })
                  }}
                >
                  {o.name}
                </Button>
              ))}
            </div>
          )}
          {!pending && (
            <div className="mt-1 text-xs text-muted-foreground">
              {p.status === 'cancelled' ? 'Cancelled.' : `Answered “${chosen?.name ?? p.chosen}” by ${p.answered_by}.`}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
