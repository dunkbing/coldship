import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api } from '../api'
import { useLoad } from '../hooks'
import { Check, Copy as CopyIcon } from 'lucide-react'
import { ErrorBox, Mono, PageHeader, PageSkeleton, Time } from '@/components/common'
import { Skeleton } from '@/components/ui/skeleton'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { DataMark, dataKinds, envSummary, isStorage } from '../components/data'
import { useSession } from '../App'
import { notify } from '@/components/toasts'
import { BackupTable } from './Backups'

function Copy({ text }: { text: string }) {
  const [done, setDone] = useState(false)
  return (
    <Button
      type="button"
      variant="ghost"
      size="sm"
      className="shrink-0"
      onClick={() => {
        navigator.clipboard?.writeText(text)
        setDone(true)
        setTimeout(() => setDone(false), 1500)
      }}
    >
      {done ? <Check className="text-ok" aria-hidden /> : <CopyIcon aria-hidden />}
      {done ? 'Copied' : 'Copy'}
    </Button>
  )
}

// A line of code that scrolls inside its box, with a copy button.
function CopyLine({ code, copy }: { code: string; copy: string }) {
  return (
    <div className="flex items-center gap-2 rounded-lg border bg-muted/50 py-1 pr-1 pl-3">
      <code className="min-w-0 flex-1 overflow-x-auto py-1 font-mono text-xs whitespace-nowrap">{code}</code>
      <Copy text={copy} />
    </div>
  )
}

const linkClass = 'text-primary hover:underline'

export default function DataDetail() {
  const { name = '' } = useParams()
  const me = useSession()
  const nav = useNavigate()
  const admin = me.scope === 'admin'
  const { data: d, error, reload } = useLoad(() => api.dataOne(name), [name], 5000)
  const snaps = useLoad(() => api.dataSnapshots(name), [name])
  const backup = useLoad(api.backupStatus, [me.org?.id])
  const offsite = useLoad(
    () => (backup.data?.configured && !me.apps?.length ? api.backups(name) : Promise.resolve(undefined)),
    [name, backup.data?.configured, backup.data?.uploaded],
  )
  const [conn, setConn] = useState<Record<string, string>>()
  const [busy, setBusy] = useState(false)
  const [actionError, setActionError] = useState<Error>()
  const [message, setMessage] = useState('')
  const [confirmDelete, setConfirmDelete] = useState('')
  const [deleting, setDeleting] = useState(false)

  async function act(f: () => Promise<unknown>, ok?: string) {
    setBusy(true)
    setActionError(undefined)
    setMessage('')
    try {
      await f()
      if (ok) notify(ok)
      reload()
      snaps.reload()
      backup.reload()
    } catch (err) {
      setActionError(err as Error)
    } finally {
      setBusy(false)
    }
  }

  if (error) return <ErrorBox error={error} />
  if (!d) return <PageSkeleton kind="detail" />
  const kind = dataKinds[d.engine]
  const connectCmd = `coldship deploy --name <app> --use ${d.name}`

  return (
    <>
      <PageHeader
        crumbs={
          <>
            <Link className="hover:text-foreground hover:underline" to="/data">
              Data &amp; Storage
            </Link>{' '}
            /
          </>
        }
        title={
          <>
            <DataMark engine={d.engine} />
            <span className="break-all">{d.name}</span>
          </>
        }
        description={
          <>
            {kind?.label} {d.version} · {d.status.running ? 'running' : d.status.state || 'not running'} · host{' '}
            <Mono className="text-foreground">{d.host}</Mono>
          </>
        }
      />

      {actionError && <ErrorBox error={actionError} />}
      {message && (
        <Alert className="my-3 border-transparent bg-info-soft text-info" role="status">
          <AlertDescription className="text-info">{message}</AlertDescription>
        </Alert>
      )}

      <div className="grid gap-4 md:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>
              <h2>Connect an app</h2>
            </CardTitle>
            <CardDescription>
              The app gets <Mono className="text-foreground">{envSummary(d.env)}</Mono> on its next deploy.
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            <CopyLine code={connectCmd} copy={connectCmd} />
            <p className="text-sm text-muted-foreground">
              Used by:{' '}
              {d.used_by.length === 0
                ? 'no apps yet'
                : d.used_by.map((a, i) => (
                    <span key={a}>
                      {i > 0 && ', '}
                      <Link className={linkClass} to={`/apps/${a}`}>
                        {a}
                      </Link>
                    </span>
                  ))}
            </p>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>
              <h2>Connection details</h2>
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            {!admin ? (
              <p className="text-sm text-muted-foreground">Passwords are shown to admin tokens only.</p>
            ) : !conn ? (
              <>
                <p className="text-sm text-muted-foreground">
                  For connecting with your own tools from Coldship's network. Revealing is recorded in the audit log.
                </p>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={busy}
                  onClick={() => act(async () => setConn(await api.dataConnection(d.name)))}
                >
                  Show connection details
                </Button>
              </>
            ) : (
              <div className="space-y-2">
                {Object.entries(conn)
                  .sort()
                  .map(([k, v]) => (
                    <CopyLine key={k} code={`${k}=${v}`} copy={v} />
                  ))}
              </div>
            )}
          </CardContent>
        </Card>
      </div>

      <Card className="mt-4">
        <CardHeader>
          <CardTitle>
            <h2>Snapshots</h2>
          </CardTitle>
          {!isStorage(d.engine) && (
            <CardAction>
              <Button
                variant="outline"
                size="sm"
                disabled={busy}
                onClick={() => act(() => api.snapshotData(d.name), 'Snapshot saved.')}
              >
                Snapshot now
              </Button>
            </CardAction>
          )}
        </CardHeader>
        <CardContent>
          {isStorage(d.engine) ? (
            <p className="text-sm text-muted-foreground">
              Snapshots of S3 storage aren't supported yet. Back up the bucket with an S3 tool such as rclone.
            </p>
          ) : !snaps.data || snaps.data.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              None yet. Snapshots are taken daily and before anything destructive
              {backup.data?.configured ? ', and copied to the backup bucket.' : '.'}
            </p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Snapshot</TableHead>
                  <TableHead>Taken</TableHead>
                  <TableHead>Size</TableHead>
                  <TableHead>Reason</TableHead>
                  {backup.data?.configured && <TableHead>Off-site</TableHead>}
                  {admin && (
                    <TableHead>
                      <span className="sr-only">Actions</span>
                    </TableHead>
                  )}
                </TableRow>
              </TableHeader>
              <TableBody>
                {snaps.data.map((s) => (
                  <TableRow key={s.id}>
                    <TableCell>
                      <Mono className="text-xs">{s.id}</Mono>
                    </TableCell>
                    <TableCell>
                      <Time iso={s.created_at} />
                    </TableCell>
                    <TableCell>{(s.size / 1024).toFixed(1)} KB</TableCell>
                    <TableCell className="text-muted-foreground">{s.reason}</TableCell>
                    {backup.data?.configured && (
                      <TableCell>
                        {s.remote_key ? (
                          <span className="inline-flex items-center gap-1 text-ok">
                            <Check className="size-3.5" aria-hidden />
                            Copied
                          </span>
                        ) : s.remote_error ? (
                          <span className="text-destructive" title={s.remote_error}>
                            Failed, retrying
                          </span>
                        ) : (
                          <span className="text-muted-foreground">Waiting</span>
                        )}
                      </TableCell>
                    )}
                    {admin && (
                      <TableCell className="text-right">
                        <Button
                          variant="ghost"
                          size="sm"
                          disabled={busy}
                          onClick={() => {
                            const reason = window.prompt(
                              `Restore ${d.name} from ${s.id}? All current data is replaced (it's snapshotted first). Why?`,
                            )
                            if (reason?.trim()) act(async () => setMessage((await api.restoreData(d.name, s.id, reason)).message))
                          }}
                        >
                          Restore
                        </Button>
                      </TableCell>
                    )}
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {!isStorage(d.engine) && !me.apps?.length && (
        <Card className="mt-4">
          <CardHeader>
            <CardTitle>
              <h2>Off-site backups</h2>
            </CardTitle>
            <CardDescription>
              {backup.data?.configured ? (
                <>
                  Copies in <Mono className="text-foreground">{backup.data.bucket}</Mono>, kept even if this server is lost.
                </>
              ) : (
                <>
                  Off. Snapshots stay on this server only.{' '}
                  <Link className={linkClass} to="/backups">
                    Set a bucket
                  </Link>
                </>
              )}
            </CardDescription>
          </CardHeader>
          {backup.data?.configured && (
            <CardContent>
              {offsite.error ? (
                <ErrorBox error={offsite.error} />
              ) : !offsite.data ? (
                <Skeleton className="h-16 w-full rounded-lg" />
              ) : offsite.data.length === 0 ? (
                <p className="text-sm text-muted-foreground">None yet. New snapshots are copied within a minute.</p>
              ) : (
                <BackupTable
                  items={offsite.data}
                  busy={busy}
                  canRestore={(b) => admin && b.engine === d.engine}
                  onRestore={(b, reason) =>
                    act(async () => setMessage((await api.restoreDataBackup(d.name, b.key, reason)).message))
                  }
                />
              )}
            </CardContent>
          )}
        </Card>
      )}

      {admin && (
        <Card className="mt-4 ring-destructive/40">
          <CardHeader>
            <CardTitle>
              <h2 className="text-destructive">Delete</h2>
            </CardTitle>
          </CardHeader>
          <CardContent>
            {d.used_by.length > 0 ? (
              <p className="text-sm text-muted-foreground">Disconnect {d.used_by.join(', ')} first (remove it from each app's uses).</p>
            ) : !deleting ? (
              <Button variant="destructive" size="sm" onClick={() => setDeleting(true)}>
                Delete {d.name}
              </Button>
            ) : (
              <div className="flex flex-col gap-3 sm:flex-row sm:items-end">
                <div className="flex-1 space-y-1.5">
                  <Label htmlFor="confirm-delete" className="block font-normal leading-normal">
                    Type <Mono>{d.name}</Mono> to delete it and all its data{!isStorage(d.engine) && ' (a snapshot is kept)'}
                  </Label>
                  <Input
                    id="confirm-delete"
                    value={confirmDelete}
                    onChange={(e) => setConfirmDelete(e.target.value)}
                    autoFocus
                  />
                </div>
                <div className="flex flex-wrap items-center gap-2">
                  <Button
                    variant="destructive"
                    size="sm"
                    disabled={busy || confirmDelete !== d.name}
                    onClick={() =>
                      act(async () => {
                        await api.deleteData(d.name)
                        notify(`Deleted ${d.name}`)
                        nav('/data')
                      })
                    }
                  >
                    Delete
                  </Button>
                  <Button variant="ghost" size="sm" onClick={() => setDeleting(false)}>
                    Cancel
                  </Button>
                </div>
              </div>
            )}
          </CardContent>
        </Card>
      )}
    </>
  )
}
