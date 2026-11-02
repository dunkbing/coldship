import { useState } from 'react'
import { KeyRound, LogOut, MoreHorizontal, UserPlus, UserX, X } from 'lucide-react'
import { api, type Invite, type Member, type Scope } from '../api'
import { useSession } from '../App'
import { useLoad } from '../hooks'
import { CopyBlock, ErrorBox, PageHeader, TableSkeleton, Time, ToneBadge } from '@/components/common'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { notify } from '@/components/toasts'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

export const roles: { value: Scope; label: string; description: string }[] = [
  { value: 'admin', label: 'Admin', description: 'Everything: approves production changes, manages members and tokens.' },
  { value: 'deploy', label: 'Deployer', description: 'Deploys and changes apps and data; production changes wait for an admin.' },
  { value: 'read', label: 'Viewer', description: 'Sees apps, logs and metrics; changes nothing.' },
]

export function roleLabel(r?: Scope) {
  return roles.find((x) => x.value === r)?.label ?? r ?? ''
}

export function RoleSelect({ id, value, onChange, disabled }: { id?: string; value: Scope; onChange: (r: Scope) => void; disabled?: boolean }) {
  return (
    <Select value={value} onValueChange={(v) => onChange(v as Scope)} disabled={disabled}>
      <SelectTrigger id={id} className="w-32" aria-label="Role">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {roles.map((r) => (
          <SelectItem key={r.value} value={r.value}>
            {r.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

function initials(m: Member) {
  const parts = (m.name || m.email).split(/[\s@.]+/).filter(Boolean)
  return ((parts[0]?.[0] ?? '') + (parts[1]?.[0] ?? '')).toUpperCase()
}

export default function Team() {
  const me = useSession()
  const admin = me.scope === 'admin'
  const members = useLoad(api.members, [me.org?.id])
  const invites = useLoad(() => (admin ? api.invites() : Promise.resolve([] as Invite[])), [me.org?.id, admin])
  const [error, setError] = useState<Error>()
  const [link, setLink] = useState<{ title: string; text: string; url: string }>()
  const [removing, setRemoving] = useState<Member>()

  async function act(fn: () => Promise<unknown>, after?: () => void, ok?: string) {
    setError(undefined)
    try {
      await fn()
      if (ok) notify(ok)
      after?.()
    } catch (e) {
      setError(e as Error)
    }
  }

  return (
    <>
      <PageHeader title="Team" description={`People in ${me.org?.name ?? 'this organization'} and what they can do.`} />
      {error && <ErrorBox error={error} />}
      {members.error && <ErrorBox error={members.error} />}

      {!me.user && (
        <p className="mb-4 rounded-lg border border-dashed p-3 text-sm text-muted-foreground">
          You're signed in with the token <strong className="text-foreground">{me.name}</strong>. People sign in with their email and password; tokens are
          for the CLI and agents.
        </p>
      )}

      <Card className="mb-6 gap-0 py-0">
        {members.loading && !members.data ? (
          <div className="p-4">
            <TableSkeleton rows={3} cols={3} />
          </div>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="pl-4">Member</TableHead>
                <TableHead>Role</TableHead>
                <TableHead className="hidden sm:table-cell">Joined</TableHead>
                <TableHead className="w-12" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {members.data?.map((m) => {
                const self = m.id === me.user?.id
                return (
                  <TableRow key={m.id}>
                    <TableCell className="pl-4">
                      <div className="flex items-center gap-3">
                        <Avatar className="size-8">
                          <AvatarFallback className="bg-accent text-xs text-accent-foreground">{initials(m)}</AvatarFallback>
                        </Avatar>
                        <div className="min-w-0">
                          <div className="truncate font-medium">
                            {m.name} {self && <span className="font-normal text-muted-foreground">(you)</span>}
                          </div>
                          <div className="truncate text-xs text-muted-foreground">{m.email}</div>
                        </div>
                      </div>
                    </TableCell>
                    <TableCell>
                      {admin ? (
                        <RoleSelect value={m.role} onChange={(r) => act(() => api.setRole(m.id, r), members.reload, `${m.name || m.email} is now ${roleLabel(r).toLowerCase()}`)} />
                      ) : (
                        <ToneBadge t={m.role === 'admin' ? 'info' : 'muted'}>{roleLabel(m.role)}</ToneBadge>
                      )}
                    </TableCell>
                    <TableCell className="hidden text-muted-foreground sm:table-cell">
                      <Time iso={m.joined_at} />
                    </TableCell>
                    <TableCell className="pr-4 text-right">
                      {(admin || self) && (
                        <DropdownMenu>
                          <DropdownMenuTrigger asChild>
                            <Button variant="ghost" size="icon" aria-label={`Actions for ${m.email}`}>
                              <MoreHorizontal />
                            </Button>
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end">
                            {me.server_admin && (
                              <DropdownMenuItem
                                onSelect={() =>
                                  act(async () => {
                                    const l = await api.resetLink(m.id)
                                    setLink({
                                      title: `Password reset for ${m.email}`,
                                      text: 'Send this link to them. It works once, within 24 hours, and signs out their other sessions.',
                                      url: l.url!,
                                    })
                                  })
                                }
                              >
                                <KeyRound /> Password reset link
                              </DropdownMenuItem>
                            )}
                            {me.server_admin && <DropdownMenuSeparator />}
                            <DropdownMenuItem variant="destructive" onSelect={() => setRemoving(m)}>
                              {self ? <LogOut /> : <UserX />}
                              {self ? 'Leave organization' : 'Remove from organization'}
                            </DropdownMenuItem>
                          </DropdownMenuContent>
                        </DropdownMenu>
                      )}
                    </TableCell>
                  </TableRow>
                )
              })}
              {members.data?.length === 0 && (
                <TableRow>
                  <TableCell colSpan={4} className="py-8 text-center text-muted-foreground">
                    No members yet: only tokens.
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        )}
      </Card>

      {admin && <Invites invites={invites.data} loading={invites.loading} reload={invites.reload} onError={setError} onLink={setLink} />}

      <Dialog open={!!link} onOpenChange={(o) => !o && setLink(undefined)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{link?.title}</DialogTitle>
            <DialogDescription>{link?.text}</DialogDescription>
          </DialogHeader>
          {link && <CopyBlock text={link.url} label="link" />}
          <p className="text-xs text-muted-foreground">It isn't shown again. Coldship doesn't send email: share it however you like.</p>
          <DialogFooter>
            <Button onClick={() => setLink(undefined)}>Done</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={!!removing} onOpenChange={(o) => !o && setRemoving(undefined)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{removing?.id === me.user?.id ? `Leave ${me.org?.name}?` : `Remove ${removing?.name}?`}</DialogTitle>
            <DialogDescription>
              {removing?.id === me.user?.id
                ? "You'll lose access to its apps and data. An admin can invite you back."
                : `${removing?.email} loses access to ${me.org?.name} at once. Their account stays; an admin can invite them back.`}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setRemoving(undefined)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={() => {
                const m = removing!
                setRemoving(undefined)
                act(
                  () => api.removeMember(m.id),
                  () => (m.id === me.user?.id ? window.location.assign('/') : members.reload()),
                  m.id === me.user?.id ? undefined : `Removed ${m.name || m.email}`,
                )
              }}
            >
              {removing?.id === me.user?.id ? 'Leave' : 'Remove'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

function Invites({
  invites,
  loading,
  reload,
  onError,
  onLink,
}: {
  invites?: Invite[]
  loading: boolean
  reload: () => void
  onError: (e: Error) => void
  onLink: (l: { title: string; text: string; url: string }) => void
}) {
  const me = useSession()
  const [role, setRole] = useState<Scope>('deploy')
  const [busy, setBusy] = useState(false)

  async function create() {
    setBusy(true)
    try {
      const inv = await api.invite(role)
      onLink({
        title: `Invite to ${me.org?.name}`,
        text: `Anyone with this link can join as ${roleLabel(role).toLowerCase()} within 7 days. It works once.`,
        url: inv.url!,
      })
      reload()
    } catch (e) {
      onError(e as Error)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Invite people</CardTitle>
        <CardDescription>Create a link and send it to them. They pick their own password.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-5">
        <div className="flex flex-col gap-3 sm:flex-row sm:items-end">
          <div className="space-y-1.5">
            <Label htmlFor="invrole">Role</Label>
            <RoleSelect id="invrole" value={role} onChange={setRole} />
          </div>
          <p className="flex-1 text-sm text-muted-foreground sm:pb-2">{roles.find((r) => r.value === role)?.description}</p>
          <Button onClick={create} disabled={busy}>
            <UserPlus /> Create invite link
          </Button>
        </div>
        {loading && !invites && <TableSkeleton rows={1} cols={3} />}
        {invites && invites.length > 0 && (
          <div className="space-y-2">
            <h3 className="text-sm font-medium">Pending invites</h3>
            <ul className="divide-y rounded-lg border">
              {invites.map((i) => (
                <li key={i.id} className="flex items-center justify-between gap-3 px-3 py-2 text-sm">
                  <span>
                    {roleLabel(i.role)} <span className="text-muted-foreground">· by {i.created_by} · expires <Time iso={i.expires_at} /></span>
                  </span>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => api.revokeInvite(i.id).then(() => (notify('Invite revoked', { description: 'The link no longer works.' }), reload()), onError)}
                    aria-label="Revoke invite"
                  >
                    <X /> Revoke
                  </Button>
                </li>
              ))}
            </ul>
          </div>
        )}
      </CardContent>
    </Card>
  )
}
