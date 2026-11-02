import { useEffect, useState, type FormEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { Sparkles } from 'lucide-react'
import { api, type Agent } from '../api'
import { useSession } from '../App'
import { ErrorBox } from './common'
import { AgentMark, agentLabels } from './agent'
import { Button } from './ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from './ui/dialog'
import { Label } from './ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from './ui/select'
import { Textarea } from './ui/textarea'

// The org's agents, fetched once a minute at most and shared by every
// "Ask an agent" button on the page.
let cache: { org: string; at: number; list: Promise<Agent[]> } | undefined

function useAgents(org: string, enabled: boolean): Agent[] {
  const [list, setList] = useState<Agent[]>([])
  useEffect(() => {
    if (!enabled) return
    if (!cache || cache.org !== org || Date.now() - cache.at > 60000) {
      cache = { org, at: Date.now(), list: api.agents().then((v) => v.agents, () => []) }
    }
    let alive = true
    cache.list.then((l) => alive && setList(l))
    return () => {
      alive = false
    }
  }, [org, enabled])
  return list
}

// AskAgent starts an agent session about something on the page, with a
// first message the person can edit. It shows nothing when the org has no
// agents or the person can't run them. Prompts carry names and ids only:
// the agent reads logs and errors itself, through its tools, where
// Coldship marks them as untrusted.
export function AskAgent({
  prompt,
  label = 'Ask an agent',
  title = 'Ask an agent',
  variant = 'outline',
  size = 'sm',
}: {
  prompt: string
  label?: string
  title?: string
  variant?: 'outline' | 'ghost' | 'default' | 'secondary'
  size?: 'sm' | 'xs' | 'default'
}) {
  const me = useSession()
  const canRun = me.scope !== 'read'
  const agents = useAgents(me.org?.id ?? '', canRun)
  const nav = useNavigate()
  const [open, setOpen] = useState(false)
  const [text, setText] = useState(prompt)
  const [agent, setAgent] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<Error>()

  if (!canRun || agents.length === 0) return null
  const chosen = agent || agents[0].name

  async function start(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(undefined)
    try {
      const s = await api.startSession(chosen, text.trim())
      nav(`/agents/sessions/${s.id}`)
    } catch (err) {
      setError(err as Error)
      setBusy(false)
    }
  }

  return (
    <>
      <Button
        type="button"
        variant={variant}
        size={size}
        onClick={(e) => {
          e.stopPropagation()
          setText(prompt)
          setError(undefined)
          setOpen(true)
        }}
      >
        <Sparkles /> {label}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <form onSubmit={start} className="contents">
            <DialogHeader>
              <DialogTitle>{title}</DialogTitle>
              <DialogDescription>
                It starts a session and looks into it with Coldship's tools. It asks before it changes anything, and production changes still wait for an
                admin.
              </DialogDescription>
            </DialogHeader>
            {agents.length > 1 && (
              <div className="space-y-1.5">
                <Label htmlFor="ask-agent">Agent</Label>
                <Select value={chosen} onValueChange={setAgent}>
                  <SelectTrigger id="ask-agent" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {agents.map((a) => (
                      <SelectItem key={a.name} value={a.name}>
                        <AgentMark kind={a.kind} size={18} /> {a.name}
                        <span className="text-muted-foreground">{agentLabels[a.kind] ?? a.kind}</span>
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            )}
            <div className="space-y-1.5">
              <Label htmlFor="ask-text">First message</Label>
              <Textarea id="ask-text" value={text} onChange={(e) => setText(e.target.value)} rows={4} className="resize-y" />
            </div>
            {error && <ErrorBox error={error} />}
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setOpen(false)}>
                Cancel
              </Button>
              <Button type="submit" disabled={busy || !text.trim()}>
                <Sparkles /> {busy ? 'Starting…' : 'Start'}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </>
  )
}
