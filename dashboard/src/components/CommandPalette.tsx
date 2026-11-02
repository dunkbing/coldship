import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'
import { useNavigate } from 'react-router-dom'
import { Boxes, Building2, Database, LogOut, MessageSquare, Plus, Search, Sparkles } from 'lucide-react'
import { api, type Agent, type AgentSession, type App, type DataView, type WhoAmI } from '../api'
import { accountPage, navGroups } from '../nav'
import { cn } from '@/lib/utils'
import { Command, CommandDialog, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList, CommandShortcut } from '@/components/ui/command'

const PaletteContext = createContext<() => void>(() => {})

// useOpenPalette returns a function that opens the command palette.
export const useOpenPalette = () => useContext(PaletteContext)

export const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform)
export const paletteKeys = isMac ? '⌘K' : 'Ctrl K'

// PaletteProvider owns the palette and its shortcut (⌘K, or Ctrl+K).
export function PaletteProvider({
  me,
  onSwitch,
  onSignOut,
  children,
}: {
  me: WhoAmI
  onSwitch: (w: WhoAmI) => void
  onSignOut: () => void
  children: ReactNode
}) {
  const [open, setOpenState] = useState(false)
  // Each opening starts fresh, even while the last one is still fading out.
  const [opens, setOpens] = useState(0)
  const setOpen = (o: boolean | ((o: boolean) => boolean)) =>
    setOpenState((cur) => {
      const next = typeof o === 'function' ? o(cur) : o
      if (next && !cur) setOpens((n) => n + 1)
      return next
    })
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key.toLowerCase() === 'k' && (e.metaKey || e.ctrlKey) && !e.altKey && !e.shiftKey) {
        e.preventDefault()
        setOpen((o) => !o)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])
  return (
    <PaletteContext.Provider value={() => setOpen(true)}>
      {children}
      <CommandDialog open={open} onOpenChange={setOpen} title="Go to" description="Search pages, apps, data and agent sessions, or run an action.">
        <PaletteBody key={opens} me={me} close={() => setOpen(false)} onSwitch={onSwitch} onSignOut={onSignOut} />
      </CommandDialog>
    </PaletteContext.Provider>
  )
}

// The tabs of an app's page worth jumping to.
const appTabs = [
  ['logs', 'Logs'],
  ['deployments', 'Deployments'],
  ['metrics', 'Metrics'],
  ['jobs', 'Jobs'],
  ['settings', 'Settings'],
] as const

// score ranks an entry for a search: every word must appear in its
// keywords (the first is its name); matches at the start of a word, and of
// the name, rank first. Letters merely appearing in order don't count.
function score(search: string, keywords: string[]): number {
  const words = search.toLowerCase().split(/\s+/).filter(Boolean)
  const name = (keywords[0] ?? '').toLowerCase()
  const hay = ' ' + keywords.join(' ').toLowerCase()
  let n = 0
  for (const w of words) {
    if (hay.includes(' ' + w) || hay.includes('-' + w)) n += 2
    else if (hay.includes(w)) n += 1
    else return 0
  }
  if (name === words.join(' ')) n += 4
  else if (name.startsWith(words[0])) n += 2
  return n / (words.length * 2 + 4)
}

interface Found {
  apps?: App[]
  data?: DataView[]
  sessions?: AgentSession[]
  agents?: Agent[]
}

// PaletteBody mounts while the palette is open, so it fetches each time
// it opens and never on other pages.
function PaletteBody({ me, close, onSwitch, onSignOut }: { me: WhoAmI; close: () => void; onSwitch: (w: WhoAmI) => void; onSignOut: () => void }) {
  const nav = useNavigate()
  const [search, setSearch] = useState('')
  const [found, setFound] = useState<Found>({})
  const [busy, setBusy] = useState('')
  const everyApp = !me.apps?.length
  const canChange = me.scope !== 'read'

  useEffect(() => {
    let alive = true
    const set = (f: Found) => alive && setFound((cur) => ({ ...cur, ...f }))
    api.apps().then((apps) => set({ apps }), () => {})
    api.data().then((data) => set({ data }), () => {})
    if (everyApp) {
      api.sessions().then((s) => set({ sessions: s.slice(0, 8) }), () => {})
      if (canChange) api.agents().then((a) => set({ agents: a.agents }), () => {})
    }
    return () => {
      alive = false
    }
  }, [everyApp, canChange])

  const go = (to: string) => {
    close()
    nav(to)
  }
  const q = search.trim()
  const agents = found.agents ?? []
  const otherOrgs = me.user ? (me.orgs ?? []).filter((o) => o.id !== me.org?.id) : []

  async function startAsk() {
    if (!agents.length || busy) return
    setBusy('ask')
    try {
      const s = await api.startSession(agents[0].name, q)
      go(`/agents/sessions/${s.id}`)
    } catch {
      setBusy('')
      go('/agents')
    }
  }

  async function switchTo(id: string) {
    setBusy(id)
    try {
      onSwitch(await api.switchOrg(id))
      go('/')
    } catch {
      setBusy('')
    }
  }

  // Every entry the palette can show, in its group. With a search, groups
  // and entries are ordered by how well they match (cmdk's own sorting
  // didn't reorder groups reliably, so the palette ranks them itself).
  const groups: { heading: string; entries: Entry[] }[] = [
    ...navGroups(me).map((g) => ({
      heading: g.label,
      entries: g.pages.map((p) => ({ id: `page:${p.to}`, keywords: [p.label, ...(p.keywords ?? [])], icon: p.icon, label: p.label, run: () => go(p.to) })),
    })),
    {
      heading: 'Apps',
      entries: [
        ...(found.apps ?? []).map((a) => ({
          id: `app:${a.name}`,
          keywords: [a.name, a.spec?.stage ?? '', a.preview_of ? 'preview' : 'app'],
          icon: <Boxes />,
          label: a.name,
          meta: a.preview_of ? `preview of ${a.preview_of}` : a.spec?.stage,
          run: () => go(`/apps/${a.name}`),
        })),
        // Tabs only while searching, so the list stays short.
        ...(q
          ? (found.apps ?? []).flatMap((a) =>
              appTabs.map(([tab, label]) => ({
                id: `app:${a.name}/${tab}`,
                keywords: [`${a.name} ${label.toLowerCase()}`, 'tab'],
                icon: <Boxes className="opacity-50" />,
                label: (
                  <>
                    {a.name} <span className="text-muted-foreground">›</span> {label}
                  </>
                ),
                run: () => go(`/apps/${a.name}?tab=${tab}`),
              })),
            )
          : []),
      ],
    },
    {
      heading: 'Data & Storage',
      entries: (found.data ?? []).map((d) => ({
        id: `data:${d.app ?? ''}/${d.name}`,
        keywords: [d.name, d.engine, d.app ?? ''],
        icon: <Database />,
        label: d.name,
        meta: d.app ? `${d.engine} · ${d.app}` : d.engine,
        run: () => go(d.app ? `/apps/${d.app}?tab=databases` : `/data/${d.name}`),
      })),
    },
    {
      heading: 'Agent sessions',
      entries: (found.sessions ?? []).map((s) => ({
        id: `session:${s.id}`,
        // Titles can come from the agent: plain text only.
        keywords: [s.title || `session with ${s.agent}`, s.agent, 'session', 'agent'],
        icon: <MessageSquare />,
        label: s.title || `Session with ${s.agent}`,
        meta: s.waiting ? 'waiting for you' : s.status,
        metaClass: s.waiting ? 'text-warn' : undefined,
        run: () => go(`/agents/sessions/${s.id}`),
      })),
    },
    {
      heading: 'Actions',
      entries: [
        ...(canChange
          ? [
              { id: 'action:new-app', keywords: ['new app', 'create', 'deploy', 'launch'], icon: <Plus />, label: 'New app', run: () => go('/apps/new') },
              {
                id: 'action:new-data',
                keywords: ['new database or bucket', 'data', 'create', 'postgres', 'mysql', 'redis', 's3', 'bucket'],
                icon: <Plus />,
                label: 'New database or bucket',
                run: () => go('/data/new'),
              },
            ]
          : []),
        ...otherOrgs.map((o) => ({
          id: `org:${o.id}`,
          keywords: [`switch to ${o.name}`, 'organization', o.name],
          icon: <Building2 />,
          label: `Switch to ${o.name}`,
          disabled: !!busy,
          run: () => switchTo(o.id),
        })),
        { id: `page:${accountPage.to}`, keywords: [accountPage.label, ...(accountPage.keywords ?? [])], icon: accountPage.icon, label: accountPage.label, run: () => go(accountPage.to) },
        {
          id: 'action:sign-out',
          keywords: ['sign out', 'log out', 'logout'],
          icon: <LogOut />,
          label: 'Sign out',
          run: () => {
            close()
            onSignOut()
          },
        },
      ],
    },
  ]

  // On a tie, a thing (app, database, session) beats a page about things.
  const thing = new Set(['Apps', 'Data & Storage', 'Agent sessions'])
  let shown = groups.map((g) => ({
    ...g,
    ranked: g.entries
      .map((e) => {
        const n = q ? score(q, e.keywords) : 1
        return { e, score: n > 0 && thing.has(g.heading) ? n + 0.01 : n }
      })
      .filter((r) => r.score > 0),
  }))
  if (q) {
    for (const g of shown) g.ranked.sort((x, y) => y.score - x.score)
    shown = shown.filter((g) => g.ranked.length).sort((x, y) => y.ranked[0].score - x.ranked[0].score)
  }
  const ask = q && agents.length > 0
  // The best match is selected as the search changes.
  const first = shown.find((g) => g.ranked.length)?.ranked[0].e.id ?? (ask ? 'ask' : '')
  const [selected, setSelected] = useState('')
  useEffect(() => setSelected(first), [first, q])

  return (
    <Command shouldFilter={false} loop value={selected} onValueChange={setSelected}>
      <CommandInput autoFocus placeholder="Search pages, apps, data…" value={search} onValueChange={setSearch} />
      <CommandList>
        {!shown.some((g) => g.ranked.length) && !ask && <CommandEmpty>Nothing matches “{q}”.</CommandEmpty>}
        {shown.map(
          (g) =>
            g.ranked.length > 0 && (
              <CommandGroup key={g.heading} heading={g.heading}>
                {g.ranked.map(({ e }) => (
                  <CommandItem key={e.id} value={e.id} onSelect={e.run} disabled={e.disabled}>
                    {e.icon}
                    <span className="truncate">{e.label}</span>
                    {e.meta && <Meta className={e.metaClass}>{e.meta}</Meta>}
                  </CommandItem>
                ))}
              </CommandGroup>
            ),
        )}
        {ask && (
          <CommandGroup heading="Ask an agent">
            <CommandItem value="ask" onSelect={startAsk} disabled={busy === 'ask'}>
              <Sparkles />
              <span className="truncate">
                Ask {agents[0].name}: “{q}”
              </span>
            </CommandItem>
          </CommandGroup>
        )}
      </CommandList>
      <div className="flex items-center gap-3 border-t px-3 py-2 text-xs text-muted-foreground">
        <span>
          <Kbd>↑</Kbd> <Kbd>↓</Kbd> move
        </span>
        <span>
          <Kbd>↵</Kbd> open
        </span>
        <span>
          <Kbd>esc</Kbd> close
        </span>
      </div>
    </Command>
  )
}

interface Entry {
  id: string
  keywords: string[] // the first is the entry's name
  icon: ReactNode
  label: ReactNode
  meta?: string
  metaClass?: string
  disabled?: boolean
  run: () => void
}

// Meta is an item's note on the right (the shortcut slot, so cmdk's check
// mark stays hidden).
function Meta({ children, className }: { children: ReactNode; className?: string }) {
  return <CommandShortcut className={cn('max-w-[45%] flex-none truncate tracking-normal', className)}>{children}</CommandShortcut>
}

export function Kbd({ children, className }: { children: ReactNode; className?: string }) {
  return <kbd className={cn('rounded border bg-muted px-1 font-sans text-[0.6875rem] font-medium text-muted-foreground', className)}>{children}</kbd>
}

// SearchButton opens the palette: a search field in the sidebar.
export function SearchButton({ className }: { className?: string }) {
  const open = useOpenPalette()
  return (
    <button
      type="button"
      onClick={open}
      className={cn(
        'flex h-8 w-full items-center gap-2 rounded-md border bg-background px-2 text-sm text-muted-foreground shadow-xs transition-colors hover:bg-accent hover:text-accent-foreground',
        className,
      )}
    >
      <Search className="size-4" aria-hidden />
      <span className="flex-1 text-left">Search…</span>
      <Kbd>{paletteKeys}</Kbd>
    </button>
  )
}
