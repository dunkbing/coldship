import { createContext, useContext, useEffect, useState } from 'react'
import { NavLink, Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { Boxes, Database, Gauge, LogOut, ScrollText, ShieldCheck, Ship } from 'lucide-react'
import { api, ApiError, type WhoAmI } from './api'
import { useLoad } from './hooks'
import Login from './pages/Login'
import NewApp from './pages/NewApp'
import Launch from './pages/Launch'
import Data from './pages/Data'
import NewData from './pages/NewData'
import DataDetail from './pages/DataDetail'
import Server from './pages/Server'
import Apps from './pages/Apps'
import AppDetail from './pages/AppDetail'
import Approvals from './pages/Approvals'
import Audit from './pages/Audit'
import UpdateBanner from './components/UpdateBanner'
import { PageSkeleton } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { TooltipProvider } from '@/components/ui/tooltip'
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
    window.addEventListener('coldship:signed-out', onSignedOut)
    return () => window.removeEventListener('coldship:signed-out', onSignedOut)
  }, [])

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
  if (!me) return <Login onLogin={setMe} initialError={bootError} />

  return (
    <SessionContext.Provider value={me}>
      <TooltipProvider>
        <SidebarProvider>
          <AppSidebar
            me={me}
            onSignOut={() => {
              api.logout().finally(() => setMe(null))
            }}
          />
          <SidebarInset>
            <header className="sticky top-0 z-10 flex h-12 items-center gap-2 border-b bg-background/80 px-4 backdrop-blur md:hidden">
              <SidebarTrigger />
              <span className="font-semibold">Coldship</span>
            </header>
            <main className="mx-auto w-full max-w-7xl px-4 py-6 md:px-10 md:py-8">
              <UpdateBanner />
              <Routes>
                <Route path="/" element={<Navigate to="/apps" replace />} />
                <Route path="/apps" element={<Apps />} />
                <Route path="/apps/new" element={<Launch />} />
                <Route path="/apps/new/configure" element={<NewApp />} />
                <Route path="/apps/:name" element={<AppDetail />} />
                <Route path="/data" element={<Data />} />
                <Route path="/data/new" element={<NewData />} />
                <Route path="/data/:name" element={<DataDetail />} />
                <Route path="/server" element={<Server />} />
                <Route path="/approvals" element={<Approvals />} />
                <Route path="/audit" element={<Audit />} />
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

function AppSidebar({ me, onSignOut }: { me: WhoAmI; onSignOut: () => void }) {
  const pending = useLoad(() => api.plans('pending'), [], 10000)
  const needApproval = pending.data?.filter((p) => p.requires_approval && !p.approved_by).length ?? 0

  return (
    <Sidebar>
      <SidebarHeader>
        <div className="flex items-center gap-2.5 px-2 py-1.5">
          <span className="flex size-8 items-center justify-center rounded-lg bg-primary text-primary-foreground" aria-hidden>
            <Ship className="size-[18px]" strokeWidth={2.2} />
          </span>
          <span className="text-base font-bold tracking-tight">Coldship</span>
        </div>
      </SidebarHeader>
      <SidebarContent>
        <SidebarGroup>
          <SidebarGroupLabel>Deploy</SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu>
              <NavItem to="/apps" icon={<Boxes />} label="Apps" />
              <NavItem to="/data" icon={<Database />} label="Data & Storage" />
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
        <SidebarGroup>
          <SidebarGroupLabel>Operate</SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu>
              <NavItem to="/approvals" icon={<ShieldCheck />} label="Approvals" badge={needApproval} />
              <NavItem to="/audit" icon={<ScrollText />} label="Audit log" />
              {!me.apps?.length && <NavItem to="/server" icon={<Gauge />} label="Server" />}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
      </SidebarContent>
      <SidebarFooter>
        <div className="flex items-center justify-between gap-2 rounded-lg px-2 py-1.5">
          <div className="min-w-0">
            <div className="truncate text-sm font-semibold">{me.name}</div>
            <div className="truncate text-xs text-muted-foreground">
              {me.scope}
              {me.apps && me.apps.length > 0 && ` · ${me.apps.join(', ')}`}
            </div>
          </div>
          <Button variant="ghost" size="icon" onClick={onSignOut} title="Sign out" aria-label="Sign out">
            <LogOut />
          </Button>
        </div>
      </SidebarFooter>
    </Sidebar>
  )
}
