import type { ReactNode } from 'react'
import {
  Bell,
  Bot,
  Boxes,
  Database,
  DatabaseBackup,
  Gauge,
  KeyRound,
  LayoutDashboard,
  ScrollText,
  Settings,
  ShieldCheck,
  UserRound,
  Users,
} from 'lucide-react'
import type { WhoAmI } from './api'

export interface NavPage {
  to: string
  label: string
  icon: ReactNode
  keywords?: string[]
}

export interface NavGroup {
  label: string
  pages: NavPage[]
}

// navGroups is every page the person may open, for the sidebar and the
// command palette alike, so neither offers a page the other hides.
export function navGroups(me: WhoAmI): NavGroup[] {
  const everyApp = !me.apps?.length // tokens limited to some apps see less
  const defaultOrg = me.org?.id === 'default'
  const groups: NavGroup[] = [
    {
      label: 'Deploy',
      pages: [
        { to: '/', label: 'Overview', icon: <LayoutDashboard />, keywords: ['home', 'dashboard', 'health', 'status'] },
        { to: '/apps', label: 'Apps', icon: <Boxes />, keywords: ['services', 'deployments'] },
        { to: '/data', label: 'Data & Storage', icon: <Database />, keywords: ['databases', 'postgres', 'mysql', 'redis', 's3', 'buckets'] },
        ...(everyApp ? [{ to: '/backups', label: 'Backups', icon: <DatabaseBackup />, keywords: ['off-site', 'snapshots', 'restore'] }] : []),
      ],
    },
    {
      label: 'Operate',
      pages: [
        { to: '/approvals', label: 'Approvals', icon: <ShieldCheck />, keywords: ['plans', 'review'] },
        ...(everyApp ? [{ to: '/agents', label: 'Agents', icon: <Bot />, keywords: ['ai', 'claude', 'codex', 'gemini', 'sessions'] }] : []),
        { to: '/audit', label: 'Audit log', icon: <ScrollText />, keywords: ['history', 'activity'] },
        ...(defaultOrg && everyApp ? [{ to: '/server', label: 'Server', icon: <Gauge />, keywords: ['host', 'cpu', 'memory', 'disk', 'upgrade'] }] : []),
      ],
    },
    {
      label: 'Organization',
      pages: [
        { to: '/org/team', label: 'Team', icon: <Users />, keywords: ['members', 'invite', 'people'] },
        ...(me.scope === 'admin' ? [{ to: '/org/tokens', label: 'Tokens', icon: <KeyRound />, keywords: ['api', 'keys', 'access'] }] : []),
        ...(everyApp ? [{ to: '/org/notifications', label: 'Notifications', icon: <Bell />, keywords: ['slack', 'discord', 'telegram', 'webhook', 'alerts'] }] : []),
        { to: '/org/settings', label: 'Settings', icon: <Settings />, keywords: ['organization', 'github', 'rename'] },
      ],
    },
  ]
  return groups
}

// The account page lives in the user menu, not the sidebar.
export const accountPage: NavPage = { to: '/account', label: 'Account', icon: <UserRound />, keywords: ['password', 'profile', 'name'] }
