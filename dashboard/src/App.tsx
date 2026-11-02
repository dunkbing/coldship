import { createContext, useContext, useEffect, useState, type FormEvent } from 'react'
import { NavLink, Navigate, Route, Routes, useLocation, useNavigate } from 'react-router-dom'
import {
  Bell,
  Bot,
  Boxes,
  Building2,
  Check,
  ChevronsUpDown,
  Database,
  DatabaseBackup,
  Gauge,
  KeyRound,
  LogOut,
  Plus,
  ScrollText,
  Settings,
  ShieldCheck,
  Ship,
  UserRound,
  Users,
  X,
} from 'lucide-react'
import { api, ApiError, type WhoAmI } from './api'
import { useLoad } from './hooks'
import Login from './pages/Login'
import NewApp from './pages/NewApp'
import Launch from './pages/Launch'
import Data from './pages/Data'
import NewData from './pages/NewData'
import DataDetail from './pages/DataDetail'
import Backups from './pages/Backups'
import Notifications from './pages/Notifications'
import Server from './pages/Server'
import Apps from './pages/Apps'
import AppDetail from './pages/AppDetail'
import Approvals from './pages/Approvals'
import Audit from './pages/Audit'
import Invite from './pages/Invite'
import Team from './pages/Team'
import Tokens from './pages/Tokens'
import OrgSettings from './pages/OrgSettings'
import Account from './pages/Account'
import Agents from './pages/Agents'
import AgentSession from './pages/AgentSession'
import UpdateBanner from './components/UpdateBanner'
import { ErrorBox, PageSkeleton } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { TooltipProvider } from '@/components/ui/tooltip'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { Card, CardContent } from '@/components/ui/card'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarInset,
  SidebarMenu,
  SidebarMenuBadge,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarProvider,
  SidebarTrigger,
} from '@/components/ui/sidebar'

const SessionContext = createContext<WhoAmI | null>(null)
export const useSession = () => useContext(SessionContext)!

export default function App() {
  const [me, setMe] = useState<WhoAmI | null>(null)
  const [checking, setChecking] = useState(true)
  const [bootError, setBootError] = useState<Error>()

  useEffect(() => {
    api.whoami().then(
      (w) => {
        setMe(w)
        setChecking(false)
      },
      (e) => {
        if (!(e instanceof ApiError && e.status === 401)) setBootError(e)
        setChecking(false)
      },
    )
  }, [])

  // Any later 401 (session expired, signed out elsewhere) returns to sign-in.
  useEffect(() => {
    const onSignedOut = () => setMe(null)
    const onChanged = () => api.whoami().then(setMe, () => {})
    window.addEventListener('coldship:signed-out', onSignedOut)
    window.addEventListener('coldship:session-changed', onChanged)
    return () => {
      window.removeEventListener('coldship:signed-out', onSignedOut)
      window.removeEventListener('coldship:session-changed', onChanged)
    }
  }, [])
  // An invite or reset link (/invite#secret) shows its own page, signed in
  // or not.
  const [linkPage, setLinkPage] = useState(() => /^\/(invite|reset)$/.test(window.location.pathname) && window.location.hash.length > 1)

  if (checking)
    return (
      <div className="flex min-h-svh">
        <div className="hidden w-64 space-y-3 border-r bg-sidebar p-4 md:block">
          <Skeleton className="h-8 w-32" />
          {Array.from({ length: 5 }, (_, i) => (
            <Skeleton key={i} className="h-7 w-full" />
          ))}
        </div>
        <div className="flex-1 p-8">
          <PageSkeleton />
        </div>
      </div>
    )
  if (linkPage)
    return (
      <Invite
        me={me}
        onDone={(w) => {
          setMe(w)
          setLinkPage(false)
        }}
      />
    )
  if (!me) return <Login onLogin={setMe} initialError={bootError} />

  return (
    <SessionContext.Provider value={me}>
      <TooltipProvider>
        <SidebarProvider>
          <AppSidebar
            me={me}
            onSwitch={setMe}
            onSignOut={() => {
              api.logout().finally(() => setMe(null))
            }}
          />
          <SidebarInset>
            <header className="sticky top-0 z-10 flex h-12 items-center gap-2 border-b bg-background/80 px-4 backdrop-blur md:hidden">
              <SidebarTrigger />
              <span className="font-semibold">Coldship</span>
              <span className="truncate text-sm text-muted-foreground">· {me.org?.name}</span>
            </header>
            <main className="mx-auto w-full max-w-7xl px-4 py-6 md:px-10 md:py-8">
              <UpdateBanner />
              <AccountPrompt me={me} onCreated={setMe} />
              <Routes key={me.org?.id}>
                <Route path="/" element={<Navigate to="/apps" replace />} />
                <Route path="/apps" element={<Apps />} />
                <Route path="/apps/new" element={<Launch />} />
                <Route path="/apps/new/configure" element={<NewApp />} />
                <Route path="/apps/:name" element={<AppDetail />} />
                <Route path="/data" element={<Data />} />
                <Route path="/data/new" element={<NewData />} />
                <Route path="/data/:name" element={<DataDetail />} />
                <Route path="/backups" element={<Backups />} />
                <Route path="/server" element={<Server />} />
                <Route path="/approvals" element={<Approvals />} />
                <Route path="/audit" element={<Audit />} />
                <Route path="/org/team" element={<Team />} />
                <Route path="/org/tokens" element={me.scope === 'admin' ? <Tokens /> : <Navigate to="/org/team" replace />} />
                <Route path="/org/notifications" element={<Notifications />} />
                <Route path="/org/settings" element={<OrgSettings />} />
                <Route path="/account" element={<Account />} />
                <Route path="/agents" element={me.apps?.length ? <Navigate to="/apps" replace /> : <Agents />} />
                <Route path="/agents/sessions/:id" element={<AgentSession />} />
                <Route path="*" element={<Navigate to="/apps" replace />} />
              </Routes>
            </main>
          </SidebarInset>
        </SidebarProvider>
      </TooltipProvider>
    </SessionContext.Provider>
  )
}

function NavItem({ to, icon, label, badge }: { to: string; icon: React.ReactNode; label: string; badge?: number }) {
  const { pathname } = useLocation()
  return (
    <SidebarMenuItem>
      <SidebarMenuButton asChild isActive={pathname === to || pathname.startsWith(to + '/')} tooltip={label}>
        <NavLink to={to}>
          {icon}
          <span>{label}</span>
        </NavLink>
      </SidebarMenuButton>
      {badge ? <SidebarMenuBadge className="rounded-full bg-warn-soft text-warn">{badge}</SidebarMenuBadge> : null}
    </SidebarMenuItem>
  )
}

function AppSidebar({ me, onSwitch, onSignOut }: { me: WhoAmI; onSwitch: (w: WhoAmI) => void; onSignOut: () => void }) {
  const pending = useLoad(() => api.plans('pending'), [me.org?.id], 10000)
  const needApproval = pending.data?.filter((p) => p.requires_approval && !p.approved_by).length ?? 0

  return (
    <Sidebar>
      <SidebarHeader>
        <div className="flex items-center gap-2.5 px-2 pt-1.5">
          <span className="flex size-7 items-center justify-center rounded-lg bg-primary text-primary-foreground" aria-hidden>
            <Ship className="size-4" strokeWidth={2.2} />
          </span>
          <span className="text-base font-bold tracking-tight">Coldship</span>
        </div>
        <OrgSwitcher me={me} onSwitch={onSwitch} />
      </SidebarHeader>
      <SidebarContent>
        <SidebarGroup>
          <SidebarGroupLabel>Deploy</SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu>
              <NavItem to="/apps" icon={<Boxes />} label="Apps" />
              <NavItem to="/data" icon={<Database />} label="Data & Storage" />
              {!me.apps?.length && <NavItem to="/backups" icon={<DatabaseBackup />} label="Backups" />}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
        <SidebarGroup>
          <SidebarGroupLabel>Operate</SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu>
              <NavItem to="/approvals" icon={<ShieldCheck />} label="Approvals" badge={needApproval} />
              {!me.apps?.length && <NavItem to="/agents" icon={<Bot />} label="Agents" />}
              <NavItem to="/audit" icon={<ScrollText />} label="Audit log" />
              {me.org?.id === 'default' && !me.apps?.length && <NavItem to="/server" icon={<Gauge />} label="Server" />}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
        <SidebarGroup>
          <SidebarGroupLabel>Organization</SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu>
              <NavItem to="/org/team" icon={<Users />} label="Team" />
              {me.scope === 'admin' && <NavItem to="/org/tokens" icon={<KeyRound />} label="Tokens" />}
              {!me.apps?.length && <NavItem to="/org/notifications" icon={<Bell />} label="Notifications" />}
              <NavItem to="/org/settings" icon={<Settings />} label="Settings" />
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
      </SidebarContent>
      <SidebarFooter>
        <UserMenu me={me} onSignOut={onSignOut} />
      </SidebarFooter>
    </Sidebar>
  )
}

function initialsOf(s: string) {
  const parts = s.split(/[\s@._-]+/).filter(Boolean)
  return ((parts[0]?.[0] ?? '') + (parts[1]?.[0] ?? '')).toUpperCase() || '?'
}

// OrgSwitcher shows the current organization and switches to another.
function OrgSwitcher({ me, onSwitch }: { me: WhoAmI; onSwitch: (w: WhoAmI) => void }) {
  const nav = useNavigate()
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState<Error>()
  const orgs = me.orgs ?? []

  async function switchTo(id: string) {
    if (id === me.org?.id) return
    try {
      const w = await api.switchOrg(id)
      onSwitch(w)
      nav('/apps')
    } catch (e) {
      setError(e as Error)
    }
  }

  return (
    <>
      <SidebarMenu>
        <SidebarMenuItem>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <SidebarMenuButton size="lg" className="border bg-background data-[state=open]:bg-sidebar-accent" aria-label="Switch organization">
                <span className="flex size-8 flex-none items-center justify-center rounded-md bg-accent text-xs font-semibold text-accent-foreground" aria-hidden>
                  {initialsOf(me.org?.name ?? '')}
                </span>
                <span className="grid min-w-0 flex-1 text-left leading-tight">
                  <span className="truncate text-sm font-semibold">{me.org?.name}</span>
                  <span className="truncate text-xs text-muted-foreground">{roleName(me.scope)}</span>
                </span>
                <ChevronsUpDown className="ml-auto text-muted-foreground" />
              </SidebarMenuButton>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start" className="w-(--radix-dropdown-menu-trigger-width) min-w-56">
              <DropdownMenuLabel className="text-xs text-muted-foreground">Organizations</DropdownMenuLabel>
              {orgs.map((o) => (
                <DropdownMenuItem key={o.id} onSelect={() => switchTo(o.id)} disabled={!me.user && o.id !== me.org?.id}>
                  <Building2 />
                  <span className="flex-1 truncate">{o.name}</span>
                  {o.id === me.org?.id && <Check className="text-primary" />}
                </DropdownMenuItem>
              ))}
              {me.can_create_orgs && (
                <>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem onSelect={() => setCreating(true)}>
                    <Plus /> Create organization
                  </DropdownMenuItem>
                </>
              )}
              {!me.user && (
                <p className="px-2 py-1.5 text-xs text-muted-foreground">Tokens belong to one organization. Sign in with an account to switch.</p>
              )}
            </DropdownMenuContent>
          </DropdownMenu>
        </SidebarMenuItem>
      </SidebarMenu>
      {error && <ErrorBox error={error} />}
      <CreateOrg
        open={creating}
        onClose={() => setCreating(false)}
        onCreated={async () => {
          setCreating(false)
          onSwitch(await api.whoami())
          nav('/org/team')
        }}
      />
    </>
  )
}

function roleName(s: string) {
  return ({ admin: 'Admin', deploy: 'Deployer', read: 'Viewer' } as Record<string, string>)[s] ?? s
}

function CreateOrg({ open, onClose, onCreated }: { open: boolean; onClose: () => void; onCreated: () => void }) {
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<Error>()

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(undefined)
    try {
      await api.createOrg(name.trim())
      setName('')
      onCreated()
    } catch (err) {
      setError(err as Error)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Create organization</DialogTitle>
          <DialogDescription>A separate space for a team or client, with its own apps, data, members and tokens. You'll be its admin.</DialogDescription>
        </DialogHeader>
        <form className="grid gap-4" onSubmit={submit}>
          <div className="grid gap-2">
            <Label htmlFor="orgname-new">Name</Label>
            <Input id="orgname-new" value={name} onChange={(e) => setName(e.target.value)} placeholder="Acme" autoFocus />
          </div>
          {error && <ErrorBox error={error} />}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={busy || !name.trim()}>
              Create
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function UserMenu({ me, onSignOut }: { me: WhoAmI; onSignOut: () => void }) {
  const nav = useNavigate()
  const label = me.user?.name || me.name
  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <SidebarMenuButton size="lg" className="data-[state=open]:bg-sidebar-accent" aria-label="Account menu">
              <Avatar className="size-8">
                <AvatarFallback className="bg-accent text-xs text-accent-foreground">{me.user ? initialsOf(label) : <KeyRound className="size-4" />}</AvatarFallback>
              </Avatar>
              <span className="grid min-w-0 flex-1 text-left leading-tight">
                <span className="truncate text-sm font-semibold">{label}</span>
                <span className="truncate text-xs text-muted-foreground">
                  {me.user ? me.user.email : `token · ${me.scope}`}
                  {me.apps && me.apps.length > 0 && ` · ${me.apps.join(', ')}`}
                </span>
              </span>
              <ChevronsUpDown className="ml-auto text-muted-foreground" />
            </SidebarMenuButton>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" side="top" className="w-(--radix-dropdown-menu-trigger-width) min-w-56">
            {me.user && (
              <DropdownMenuItem onSelect={() => nav('/account')}>
                <UserRound /> Account
              </DropdownMenuItem>
            )}
            <DropdownMenuItem onSelect={onSignOut}>
              <LogOut /> Sign out
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarMenuItem>
    </SidebarMenu>
  )
}

// AccountPrompt asks an admin signed in with a token to create a personal
// account, so people sign in with email and password from then on.
function AccountPrompt({ me, onCreated }: { me: WhoAmI; onCreated: (w: WhoAmI) => void }) {
  const key = 'coldship:account-prompt-dismissed'
  const [hidden, setHidden] = useState(() => {
    try {
      return localStorage.getItem(key) === '1'
    } catch {
      return false
    }
  })
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<Error>()
  if (me.user || me.scope !== 'admin' || me.apps?.length || hidden) return null

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(undefined)
    try {
      await api.createAccount(email.trim(), name.trim(), password)
      onCreated(await api.signIn(email.trim(), password))
    } catch (err) {
      setError(err as Error)
      setBusy(false)
    }
  }

  return (
    <>
      <Card className="mb-6 border-primary/30 bg-accent/30 py-4">
        <CardContent className="flex flex-wrap items-center gap-3 px-4">
          <UserRound className="size-5 text-primary" aria-hidden />
          <p className="min-w-0 flex-1 text-sm">
            You're signed in with the token <strong>{me.name}</strong>. Create your own account to sign in with email and password, and invite your team.
          </p>
          <Button size="sm" onClick={() => setOpen(true)}>
            Create account
          </Button>
          <Button
            size="icon"
            variant="ghost"
            aria-label="Dismiss"
            onClick={() => {
              setHidden(true)
              try {
                localStorage.setItem(key, '1')
              } catch {
                /* private mode */
              }
            }}
          >
            <X />
          </Button>
        </CardContent>
      </Card>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Create your account</DialogTitle>
            <DialogDescription>You'll be an admin of {me.org?.name}.</DialogDescription>
          </DialogHeader>
          <form className="grid gap-4" onSubmit={submit}>
            <div className="grid gap-2">
              <Label htmlFor="acc-name">Name</Label>
              <Input id="acc-name" autoComplete="name" value={name} onChange={(e) => setName(e.target.value)} autoFocus />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="acc-email">Email</Label>
              <Input id="acc-email" type="email" autoComplete="username" value={email} onChange={(e) => setEmail(e.target.value)} />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="acc-pw">Password</Label>
              <Input id="acc-pw" type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} />
              <p className="text-xs text-muted-foreground">At least 8 characters.</p>
            </div>
            {error && <ErrorBox error={error} />}
            <DialogFooter>
              <Button type="submit" disabled={busy || !name.trim() || !email.trim() || password.length < 8}>
                Create account
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </>
  )
}
