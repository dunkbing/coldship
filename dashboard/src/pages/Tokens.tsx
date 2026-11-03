import { useState, type FormEvent } from 'react'
import { KeyRound, Plus, Trash2 } from 'lucide-react'
import { api, type Scope, type Token } from '../api'
import { useSession } from '../App'
import { useLoad } from '../hooks'
import { CopyBlock, Empty, ErrorBox, Mono, PageHeader, TableSkeleton, Time, ToneBadge } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { notify } from '@/components/toasts'
import { RoleSelect, roleLabel, roles } from './Team'

export default function Tokens() {
  const me = useSession()
  const tokens = useLoad(api.tokens, [me.org?.id])
  const [creating, setCreating] = useState(false)
  const [revoking, setRevoking] = useState<Token>()
  const [error, setError] = useState<Error>()

  return (
    <>
      <PageHeader
        title="Tokens"
        description="API tokens for the CLI, CI and AI agents. Each acts in this organization only."
        actions={
          <Button onClick={() => setCreating(true)}>
            <Plus /> New token
          </Button>
        }
      />
      {(tokens.error || error) && <ErrorBox error={(tokens.error || error)!} />}
      {tokens.loading && !tokens.data && <TableSkeleton cols={4} />}
      {tokens.data?.length === 0 && (
        <Empty title="No tokens" icon={<KeyRound />}>
          Create one for the CLI (<Mono>coldship login</Mono>), a CI pipeline or an AI agent.
        </Empty>
      )}
      {tokens.data && tokens.data.length > 0 && (
        <Card className="gap-0 py-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="pl-4">Name</TableHead>
                <TableHead>Role</TableHead>
                <TableHead>Apps</TableHead>
                <TableHead className="hidden sm:table-cell">Created</TableHead>
                <TableHead className="w-12" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {tokens.data.map((t) => (
                <TableRow key={t.name}>
                  <TableCell className="pl-4 font-medium whitespace-normal">
                    {t.name}
                    {t.name === me.name && !me.user && <span className="block font-normal text-muted-foreground sm:inline"> (this session)</span>}
                  </TableCell>
                  <TableCell>
                    <ToneBadge t={t.scope === 'admin' ? 'info' : 'muted'}>{roleLabel(t.scope)}</ToneBadge>
                  </TableCell>
                  <TableCell className="text-muted-foreground">{t.apps?.length ? <Mono>{t.apps.join(', ')}</Mono> : 'All'}</TableCell>
                  <TableCell className="hidden text-muted-foreground sm:table-cell">
                    <Time iso={t.created_at} />
                  </TableCell>
                  <TableCell className="pr-4 text-right">
                    <Button variant="ghost" size="icon" aria-label={`Revoke ${t.name}`} onClick={() => setRevoking(t)}>
                      <Trash2 />
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}

      <CreateToken
        open={creating}
        onClose={() => {
          setCreating(false)
          tokens.reload()
        }}
      />

      <Dialog open={!!revoking} onOpenChange={(o) => !o && setRevoking(undefined)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Revoke {revoking?.name}?</DialogTitle>
            <DialogDescription>Anything using it (the CLI, CI, an agent) stops working at once, and its dashboard sessions are signed out.</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setRevoking(undefined)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={() => {
                const t = revoking!
                setRevoking(undefined)
                setError(undefined)
                api.revokeToken(t.name).then(
                  () => (t.name === me.name && !me.user ? window.location.assign('/') : (notify(`Revoked ${t.name}`), tokens.reload())),
                  setError,
                )
              }}
            >
              Revoke
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

function CreateToken({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [name, setName] = useState('')
  const [scope, setScope] = useState<Scope>('deploy')
  const [apps, setApps] = useState('')
  const [token, setToken] = useState<string>()
  const [error, setError] = useState<Error>()
  const [busy, setBusy] = useState(false)

  function close() {
    setName('')
    setApps('')
    setScope('deploy')
    setToken(undefined)
    setError(undefined)
    onClose()
  }

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(undefined)
    try {
      const patterns = apps.split(/[\s,]+/).filter(Boolean)
      const t = await api.createToken(name.trim(), scope, patterns.length ? patterns : undefined)
      setToken(t.token)
    } catch (err) {
      setError(err as Error)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !o && close()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{token ? 'Token created' : 'New token'}</DialogTitle>
          <DialogDescription>
            {token ? "Copy it now: it isn't shown again." : 'Give it a name that says where it is used, like ci or claude-laptop.'}
          </DialogDescription>
        </DialogHeader>
        {token ? (
          <>
            <CopyBlock text={token} label="token" />
            <p className="text-sm text-muted-foreground">
              For the CLI: <Mono>coldship login {window.location.origin}</Mono>, then paste it.
            </p>
            <DialogFooter>
              <Button onClick={close}>Done</Button>
            </DialogFooter>
          </>
        ) : (
          <form className="grid gap-4" onSubmit={submit}>
            <div className="grid gap-2">
              <Label htmlFor="tokname">Name</Label>
              <Input id="tokname" value={name} onChange={(e) => setName(e.target.value)} placeholder="ci" autoFocus />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="tokrole">Role</Label>
              <RoleSelect id="tokrole" value={scope} onChange={setScope} />
              <p className="text-xs text-muted-foreground">{roles.find((r) => r.value === scope)?.description}</p>
            </div>
            {scope !== 'admin' && (
              <div className="grid gap-2">
                <Label htmlFor="tokapps">Apps it may use</Label>
                <Input id="tokapps" value={apps} onChange={(e) => setApps(e.target.value)} placeholder="every app" className="font-mono" />
                <p className="text-xs text-muted-foreground">
                  Names or patterns like <Mono>agent-*</Mono>, separated by commas. It sees nothing else.
                </p>
              </div>
            )}
            {error && <ErrorBox error={error} />}
            <DialogFooter>
              <Button type="button" variant="outline" onClick={close}>
                Cancel
              </Button>
              <Button type="submit" disabled={busy || !name.trim()}>
                Create token
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}
