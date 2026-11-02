import { Fragment, useEffect, useLayoutEffect, useMemo, useRef, useState, type FormEvent, type KeyboardEvent, type ReactNode } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import {
  ArrowLeft,
  Bell,
  Brain,
  CheckCircle2,
  ChevronRight,
  Circle,
  CircleDashed,
  Loader2,
  Play,
  Send,
  ShieldQuestion,
  Square,
  SquareTerminal,
  Trash2,
  Wrench,
  XCircle,
} from 'lucide-react'
import { api, type AgentCommand, type AgentConfigValue, type AgentSession as Session, type SessionEvent, type SessionItem } from '../api'
import { useSession } from '../App'
import { ErrorBox, PageSkeleton, ToneBadge } from '@/components/common'
import { AgentMark, agentLabels, inputSummary, toolName, toolOutput } from '@/components/agent'
import { AgentText } from '@/components/AgentText'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { SessionStatusDot, alertLabels } from './Agents'

// useLiveSession follows a session over server-sent events: a snapshot,
// then item, status and settings changes. EventSource reconnects by itself
// and the server starts each connection with a fresh snapshot.
function useLiveSession(id: string) {
  const [session, setSession] = useState<Session>()
  const [items, setItems] = useState<SessionItem[]>([])
  const [error, setError] = useState<Error>()

  useEffect(() => {
    let alive = true
    setSession(undefined)
    setItems([])
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
      } else if (e.type === 'session' && e.session) {
        const v = e.session
        setSession((s) => ({ ...s, ...v, items: undefined }))
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

const suggestions = ['What is running, and is anything unhealthy?', 'Why did the last deploy fail?', 'Is the disk or memory running low?']

export default function AgentSession() {
  const { id = '' } = useParams()
  const me = useSession()
  const nav = useNavigate()
  const { session, items, error } = useLiveSession(id)
  const [actionError, setActionError] = useState<Error>()
  const [busy, setBusy] = useState(false)
  const [deleting, setDeleting] = useState(false)
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

  const blocks = useMemo(() => group(items), [items])

  if (!session) return error ? <ErrorBox error={error} /> : <PageSkeleton kind="detail" />

  const canAct = me.scope !== 'read'
  const live = session.status !== 'ended' && session.status !== 'failed'
  const talked = items.some((it) => it.kind === 'user')

  async function act(fn: () => Promise<unknown>) {
    setActionError(undefined)
    setBusy(true)
    try {
      await fn()
    } catch (e) {
      setActionError(e as Error)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="mx-auto flex max-w-3xl flex-col">
      <div className="mb-4">
        <Link to="/agents" className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
          <ArrowLeft className="size-4" /> Agents
        </Link>
      </div>
      <header className="mb-4 flex flex-wrap items-start gap-3">
        <AgentMark kind={session.kind} size={40} />
        <div className="min-w-0 flex-1">
          <h1 className="truncate text-xl font-semibold tracking-tight" title={session.title || session.agent}>
            {session.title || session.agent}
          </h1>
          <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
            <SessionStatusDot status={session.status} />
            <span>
              · {session.title ? `${session.agent}, ` : ''}
              {session.agent_info || agentLabels[session.kind] || session.kind}
            </span>
            <ToneBadge t={session.scope === 'deploy' ? 'info' : 'muted'}>{session.scope === 'deploy' ? 'Deployer' : 'Viewer'}</ToneBadge>
            {session.alert ? (
              <ToneBadge t="warn">
                <Bell className="size-3" /> {alertLabels[session.alert] ?? session.alert}
                {session.alert_app ? `: ${session.alert_app}` : ''}
              </ToneBadge>
            ) : (
              <span>· started by {session.started_by}</span>
            )}
          </div>
        </div>
        {canAct && (
          <div className="flex items-center gap-1.5">
            {live ? (
              <Button variant="outline" size="sm" disabled={busy} onClick={() => act(() => api.endSession(id))}>
                End session
              </Button>
            ) : (
              <>
                {session.resumable && (
                  <Button size="sm" disabled={busy} onClick={() => act(() => api.resumeSession(id))}>
                    <Play /> Resume
                  </Button>
                )}
                <Button variant="ghost" size="icon" aria-label="Delete transcript" onClick={() => setDeleting(true)}>
                  <Trash2 />
                </Button>
              </>
            )}
          </div>
        )}
      </header>
      {!live && actionError && <ErrorBox error={actionError} />}

      <div className="flex-1 space-y-3 pb-48" aria-live="polite">
        {blocks.map((b) =>
          b.kind === 'tools' ? (
            <ToolGroup key={b.items[0].key} items={b.items} />
          ) : (
            <Entry key={b.item.key} item={b.item} sessionID={id} canAct={canAct && live} onError={setActionError} />
          ),
        )}
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
        {!live && (
          <p className="pt-2 text-center text-sm text-muted-foreground">
            This session has {session.status === 'failed' ? 'failed' : 'ended'}.{' '}
            {canAct && session.resumable ? 'Resume it to carry on, or ' : ''}
            <Link to="/agents" className="text-primary hover:underline">
              {canAct && session.resumable ? 'start a new one' : 'Start a new one'}
            </Link>
            .
          </p>
        )}
      </div>

      {live && canAct && (
        <Composer
          session={session}
          showSuggestions={!talked && session.status === 'ready'}
          error={actionError}
          onError={setActionError}
          onSend={() => (stick.current = true)}
        />
      )}

      <Dialog open={deleting} onOpenChange={setDeleting}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete this transcript?</DialogTitle>
            <DialogDescription>The conversation is removed from Coldship for everyone. The audit log keeps what the agent did.</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleting(false)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={() =>
                act(async () => {
                  await api.forgetSession(id)
                  nav('/agents')
                }).finally(() => setDeleting(false))
              }
            >
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

type Block = { kind: 'item'; item: SessionItem } | { kind: 'tools'; items: SessionItem[] }

// group folds runs of three or more tool calls into one block, so a busy
// turn reads as "Used 6 tools" instead of a wall of cards.
function group(items: SessionItem[]): Block[] {
  const out: Block[] = []
  let run: SessionItem[] = []
  const flush = () => {
    if (run.length >= 3) out.push({ kind: 'tools', items: run })
    else run.forEach((it) => out.push({ kind: 'item', item: it }))
    run = []
  }
  for (const it of items) {
    if (it.kind === 'tool') run.push(it)
    else {
      flush()
      out.push({ kind: 'item', item: it })
    }
  }
  flush()
  return out
}

function Composer({
  session,
  showSuggestions,
  error,
  onError,
  onSend,
}: {
  session: Session
  showSuggestions: boolean
  error?: Error
  onError: (e?: Error) => void
  onSend: () => void
}) {
  const [text, setText] = useState('')
  const [pick, setPick] = useState(0)
  const [sending, setSending] = useState(false)
  const box = useRef<HTMLTextAreaElement>(null)
  const id = session.id

  // Slash commands the agent offers, while the first word is being typed.
  const commands = session.commands ?? []
  const typing = /^\/(\S*)$/.exec(text)
  const matches = typing ? commands.filter((c) => c.name.toLowerCase().startsWith(typing[1].toLowerCase())).slice(0, 8) : []
  const chosen = /^\/(\S+)\s/.exec(text)
  const hint = chosen ? commands.find((c) => c.name === chosen[1])?.input?.hint : undefined

  async function send(e?: FormEvent) {
    e?.preventDefault()
    const t = text.trim()
    if (!t || sending || session.status !== 'ready') return
    onError(undefined)
    onSend()
    setSending(true)
    try {
      await api.prompt(id, t)
      setText('')
    } catch (err) {
      onError(err as Error)
    } finally {
      setSending(false)
    }
  }

  function complete(c: AgentCommand) {
    setText(`/${c.name} `)
    setPick(0)
    box.current?.focus()
  }

  function onKey(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (matches.length > 0) {
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault()
        setPick((p) => (p + (e.key === 'ArrowDown' ? 1 : matches.length - 1)) % matches.length)
        return
      }
      if (e.key === 'Tab' || (e.key === 'Enter' && !e.shiftKey && matches[pick] && `/${matches[pick].name}` !== text)) {
        e.preventDefault()
        complete(matches[Math.min(pick, matches.length - 1)])
        return
      }
      if (e.key === 'Escape') {
        setText(text + ' ')
        return
      }
    }
    if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
      e.preventDefault()
      send()
    }
  }

  return (
    <form
      onSubmit={send}
      className="sticky bottom-0 -mx-4 border-t bg-background/90 px-4 pt-3 pb-4 backdrop-blur md:mx-0 md:rounded-t-xl md:border-x"
    >
      {error && <ErrorBox error={error} />}
      {showSuggestions && !text && (
        <div className="mb-2 flex flex-wrap gap-1.5">
          {suggestions.map((s) => (
            <button
              key={s}
              type="button"
              onClick={() => {
                setText(s)
                box.current?.focus()
              }}
              className="rounded-full border bg-card px-3 py-1 text-xs text-muted-foreground transition-colors hover:border-primary hover:text-foreground"
            >
              {s}
            </button>
          ))}
        </div>
      )}
      <div className="relative">
        {matches.length > 0 && (
          <ul role="listbox" aria-label="Commands" className="absolute bottom-full left-0 z-10 mb-1.5 w-full max-w-md overflow-hidden rounded-lg border bg-popover py-1 shadow-md">
            {matches.map((c, i) => (
              <li key={c.name} role="option" aria-selected={i === pick}>
                <button
                  type="button"
                  onMouseDown={(e) => e.preventDefault()}
                  onClick={() => complete(c)}
                  className={cn('flex w-full items-baseline gap-2 px-3 py-1.5 text-left text-sm', i === pick && 'bg-accent')}
                >
                  <span className="font-mono text-[0.8125rem]">/{c.name}</span>
                  <span className="truncate text-xs text-muted-foreground">{c.description}</span>
                </button>
              </li>
            ))}
          </ul>
        )}
        <div className="flex items-end gap-2">
          <Textarea
            ref={box}
            value={text}
            onChange={(e) => {
              setText(e.target.value)
              setPick(0)
            }}
            onKeyDown={onKey}
            rows={2}
            placeholder={
              session.status === 'starting'
                ? 'Wait for the agent…'
                : commands.length
                  ? 'Ask the agent anything, or type / for its commands'
                  : 'Ask the agent: “why is web failing?”, “deploy my repo”…'
            }
            className="max-h-48 min-h-11 resize-none"
            aria-label="Message"
          />
          {session.status === 'working' ? (
            <Button type="button" variant="outline" onClick={() => api.cancelTurn(id).catch(onError)} aria-label="Stop">
              <Square /> Stop
            </Button>
          ) : (
            <Button type="submit" disabled={session.status !== 'ready' || !text.trim() || sending} aria-label="Send">
              <Send /> Send
            </Button>
          )}
        </div>
      </div>
      <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1.5 text-xs text-muted-foreground">
        <Settings session={session} onError={onError} />
        <span className="mr-auto min-w-0 truncate" title="Answers come from an AI and can be wrong.">
          {hint ? <>Then: {hint}</> : 'Enter sends, Shift+Enter for a new line. Answers can be wrong.'}
        </span>
        <Usage usage={session.usage} />
      </div>
    </form>
  )
}

// Settings shows the modes and options the agent offers (ACP), like its
// model, and changes them for this session.
function Settings({ session, onError }: { session: Session; onError: (e?: Error) => void }) {
  const [saving, setSaving] = useState('')
  const disabled = session.status === 'starting' || !!saving
  async function change(key: string, setting: { mode?: string; option?: string; value?: string }) {
    setSaving(key)
    onError(undefined)
    try {
      await api.configureSession(session.id, setting)
    } catch (e) {
      onError(e as Error)
    } finally {
      setSaving('')
    }
  }
  const modes = session.modes
  const options = (session.config_options ?? []).filter((o) => o.type === 'select' && o.options?.length)
  // An agent offering its mode as a config option too: show it once.
  const showModes = modes && modes.availableModes.length > 1 && !options.some((o) => o.category === 'mode')
  if (!showModes && options.length === 0) return null
  return (
    <>
      {showModes && (
        <Select value={modes.currentModeId} disabled={disabled} onValueChange={(v) => change('mode', { mode: v })}>
          <SelectTrigger size="sm" aria-label="Mode" className="h-7 text-xs">
            {saving === 'mode' && <Loader2 className="animate-spin" />}
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {modes.availableModes.map((m) => (
              <SelectItem key={m.id} value={m.id}>
                <span>{m.name}</span>
                {m.description && <span className="sr-only">: {m.description}</span>}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      )}
      {options.map((o) => (
        <Select key={o.id} value={String(o.currentValue)} disabled={disabled} onValueChange={(v) => change(o.id, { option: o.id, value: v })}>
          <SelectTrigger size="sm" aria-label={o.name} title={o.description || o.name} className="h-7 max-w-52 text-xs">
            {saving === o.id && <Loader2 className="animate-spin" />}
            <SelectValue />
          </SelectTrigger>
          <SelectContent>{choices(o.options ?? [])}</SelectContent>
        </Select>
      ))}
    </>
  )
}

function choices(list: AgentConfigValue[]): ReactNode {
  return list.map((c, i) =>
    c.group ? (
      <SelectGroup key={`g-${c.group}-${i}`}>
        <SelectLabel>{c.name}</SelectLabel>
        {choices(c.options ?? [])}
      </SelectGroup>
    ) : (
      <SelectItem key={c.value} value={c.value!}>
        {c.name}
      </SelectItem>
    ),
  )
}

function Usage({ usage }: { usage?: Session['usage'] }) {
  if (!usage || !usage.size) return null
  const pct = Math.min(100, Math.round((usage.used / usage.size) * 100))
  const cost = usage.cost ? ` · ${usage.cost.amount.toFixed(2)} ${usage.cost.currency}` : ''
  return (
    <span className="inline-flex items-center gap-1.5" title={`${usage.used.toLocaleString()} of ${usage.size.toLocaleString()} tokens of context used`}>
      <span className="h-1.5 w-12 overflow-hidden rounded-full bg-muted" aria-hidden>
        <span className={cn('block h-full rounded-full', pct > 85 ? 'bg-warn' : 'bg-primary/60')} style={{ width: `${pct}%` }} />
      </span>
      {pct}% context{cost}
    </span>
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
        <div className="max-w-[95%] px-1">
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
      return (
        <div className="flex items-center gap-3 py-1 text-xs text-muted-foreground">
          <span className="h-px flex-1 bg-border" />
          <span className="max-w-[80%] text-center">{item.text}</span>
          <span className="h-px flex-1 bg-border" />
        </div>
      )
  }
}

function Collapsible({
  icon,
  title,
  children,
  muted,
  extra,
  flat,
}: {
  icon: ReactNode
  title: ReactNode
  children: ReactNode
  muted?: boolean
  extra?: ReactNode
  flat?: boolean
}) {
  const [open, setOpen] = useState(false)
  return (
    <div className={cn(!flat && 'rounded-xl border', !flat && (muted ? 'bg-muted/30' : 'bg-card'))}>
      <button
        type="button"
        onClick={() => setOpen(!open)}
        aria-expanded={open}
        className="flex w-full items-center gap-2 rounded-xl px-3 py-2 text-left text-sm focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
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

function ToolStatus({ status }: { status: string }) {
  if (status === 'completed') return <CheckCircle2 className="size-4 flex-none text-ok" aria-label="done" />
  if (status === 'failed') return <XCircle className="size-4 flex-none text-destructive" aria-label="failed" />
  return <Loader2 className="size-4 flex-none animate-spin text-muted-foreground" aria-label="running" />
}

function ToolTitle({ title, input }: { title: string; input?: string }) {
  const summary = inputSummary(input)
  return (
    <span className="flex min-w-0 items-baseline gap-2">
      <span className="flex-none font-medium">{toolName(title)}</span>
      {summary && <span className="truncate font-mono text-xs text-muted-foreground">{summary}</span>}
    </span>
  )
}

function ToolBody({ input, output }: { input?: string; output?: string }) {
  const out = toolOutput(output)
  return (
    <>
      {input && input !== '{}' && (
        <>
          <div className="mb-1 text-xs font-medium text-muted-foreground">Input</div>
          <pre className="mb-2 max-h-64 overflow-auto rounded-lg bg-muted p-2 font-mono text-xs whitespace-pre-wrap wrap-anywhere">{input}</pre>
        </>
      )}
      {out ? (
        <>
          <div className="mb-1 text-xs font-medium text-muted-foreground">Output</div>
          <pre className="max-h-80 overflow-auto rounded-lg bg-muted p-2 font-mono text-xs whitespace-pre-wrap wrap-anywhere">{out}</pre>
        </>
      ) : (
        <p className="text-xs text-muted-foreground">No output yet.</p>
      )}
    </>
  )
}

function Tool({ item, flat }: { item: SessionItem; flat?: boolean }) {
  const t = item.tool!
  return (
    <Collapsible flat={flat} icon={<Wrench className="size-4" />} title={<ToolTitle title={t.title} input={t.input} />} extra={<ToolStatus status={t.status} />}>
      <ToolBody input={t.input} output={t.output} />
    </Collapsible>
  )
}

function ToolGroup({ items }: { items: SessionItem[] }) {
  const [open, setOpen] = useState(false)
  const running = items.some((it) => it.tool!.status !== 'completed' && it.tool!.status !== 'failed')
  const failed = items.filter((it) => it.tool!.status === 'failed').length
  const names = [...new Set(items.map((it) => toolName(it.tool!.title)))]
  return (
    <div className="rounded-xl border bg-card">
      <button
        type="button"
        onClick={() => setOpen(!open)}
        aria-expanded={open}
        className="flex w-full items-center gap-2 rounded-xl px-3 py-2 text-left text-sm focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
      >
        <ChevronRight className={cn('size-4 flex-none text-muted-foreground transition-transform', open && 'rotate-90')} />
        <Wrench className="size-4 flex-none text-muted-foreground" />
        <span className="min-w-0 flex-1 truncate">
          <span className="font-medium">Used {items.length} tools</span>
          <span className="text-muted-foreground"> · {names.join(', ')}</span>
        </span>
        {failed > 0 && <ToneBadge t="danger">{failed} failed</ToneBadge>}
        <ToolStatus status={running ? 'pending' : 'completed'} />
      </button>
      {open && (
        <div className="divide-y border-t">
          {items.map((it) => (
            <Fragment key={it.key}>
              <Tool item={it} flat />
            </Fragment>
          ))}
        </div>
      )}
    </div>
  )
}

function Permission({ item, sessionID, canAct, onError }: { item: SessionItem; sessionID: string; canAct: boolean; onError: (e: Error) => void }) {
  const p = item.permission!
  const [busy, setBusy] = useState(false)
  const pending = p.status === 'pending'
  const chosen = p.options.find((o) => o.optionId === p.chosen)
  const summary = inputSummary(p.input)
  return (
    <div className={cn('rounded-xl border p-3', pending ? 'border-warn/50 bg-warn-soft/40' : 'bg-card')}>
      <div className="flex items-start gap-2">
        <ShieldQuestion className={cn('mt-0.5 size-4 flex-none', pending ? 'text-warn' : 'text-muted-foreground')} />
        <div className="min-w-0 flex-1">
          <div className="text-sm">
            {pending ? 'The agent asks to use ' : 'Asked to use '}
            <span className="font-medium">{toolName(p.title)}</span>
            {summary && <span className="ml-2 font-mono text-xs text-muted-foreground">{summary}</span>}
          </div>
          {p.input && pending && p.input !== '{}' && (
            <pre className="mt-2 max-h-48 overflow-auto rounded-lg bg-muted p-2 font-mono text-xs whitespace-pre-wrap wrap-anywhere">{p.input}</pre>
          )}
          {pending && canAct && (
            <div className="mt-3 flex flex-wrap gap-2">
              {p.options.map((o) => (
                <Button
                  key={o.optionId}
                  size="sm"
                  disabled={busy}
                  variant={o.kind === 'allow_once' ? 'default' : 'outline'}
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
          {pending && !canAct && <div className="mt-1 text-xs text-muted-foreground">Waiting for someone who can deploy to answer.</div>}
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
