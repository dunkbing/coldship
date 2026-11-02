import { useEffect, useState, type FormEvent } from 'react'
import { Link } from 'react-router-dom'
import { CloudUpload, DatabaseBackup, RefreshCw } from 'lucide-react'
import { api, type BackupConfig, type BackupStatus, type DataEngine, type OffsiteBackup } from '../api'
import { useSession } from '../App'
import { useLoad } from '../hooks'
import { Empty, ErrorBox, Mono, PageHeader, PageSkeleton, StatusDot, TableSkeleton, Time } from '@/components/common'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { DataMark } from '../components/data'
import { bytes } from './MetricsTab'

// Endpoint examples for common providers.
const providers = [
  { name: 'AWS S3', endpoint: 'https://s3.us-east-1.amazonaws.com', region: 'us-east-1' },
  { name: 'Cloudflare R2', endpoint: 'https://<account-id>.r2.cloudflarestorage.com', region: 'auto' },
  { name: 'Backblaze B2', endpoint: 'https://s3.us-west-004.backblazeb2.com', region: 'us-west-004' },
  { name: 'Wasabi', endpoint: 'https://s3.wasabisys.com', region: 'us-east-1' },
]

function emptyConfig(st?: BackupStatus): BackupConfig {
  return {
    endpoint: st?.endpoint ?? '',
    region: st?.region ?? '',
    bucket: st?.bucket ?? '',
    prefix: st?.configured ? (st.prefix ?? '') : 'coldship',
    access_key: st?.access_key ?? '',
    secret_key: '',
    path_style: st?.path_style ?? false,
    keep: st?.keep ?? 30,
  }
}

export function SyncState({ st }: { st: BackupStatus }) {
  if (!st.configured) return <StatusDot dot="bg-muted-foreground" text="text-muted-foreground" label="Off" />
  if (st.last_error) return <StatusDot dot="bg-destructive" text="text-destructive" label="Failing" />
  if (st.pending > 0) return <StatusDot dot="bg-warn" text="text-warn" label={`${st.pending} waiting`} />
  return <StatusDot dot="bg-ok" text="text-ok" label="Up to date" />
}

export default function Backups() {
  const me = useSession()
  const admin = me.scope === 'admin'
  const status = useLoad(api.backupStatus, [me.org?.id], 10000)
  const list = useLoad(() => (status.data?.configured ? api.backups() : Promise.resolve([])), [status.data?.configured, status.data?.uploaded])
  const data = useLoad(api.data, [me.org?.id])
  const [editing, setEditing] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<Error>()
  const [message, setMessage] = useState('')

  async function act(f: () => Promise<unknown>, done?: string) {
    setBusy(true)
    setError(undefined)
    setMessage('')
    try {
      await f()
      if (done) setMessage(done)
      status.reload()
      list.reload()
    } catch (err) {
      setError(err as Error)
    } finally {
      setBusy(false)
    }
  }

  if (!status.data) return status.error ? <ErrorBox error={status.error} /> : <PageSkeleton kind="form" />
  const st = status.data
  const services = new Map((data.data ?? []).filter((d) => !d.app).map((d) => [d.name, d]))

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
        title="Backups"
        description={
          <>
            Every database is snapshotted {st.interval ? `every ${humanInterval(st.interval)}` : 'on demand'} and before anything
            destructive. Snapshots live on this server; with a bucket set, each one is also copied off it, so you can restore even
            if the server is lost.
          </>
        }
      />

      {error && <ErrorBox error={error} />}
      {message && (
        <Alert className="my-3 border-transparent bg-info-soft text-info" role="status">
          <AlertDescription className="text-info">{message}</AlertDescription>
        </Alert>
      )}

      {!st.configured && !editing ? (
        <Empty title="Off-site backups are off" icon={<CloudUpload />}>
          <p>Copy every snapshot to an S3-compatible bucket: AWS S3, Cloudflare R2, Backblaze B2, Wasabi, or your own MinIO.</p>
          {admin ? (
            <Button className="mt-4" onClick={() => setEditing(true)}>
              Set a bucket
            </Button>
          ) : (
            <p className="mt-2">An admin can set one up.</p>
          )}
        </Empty>
      ) : editing ? (
        <DestinationForm
          st={st}
          busy={busy}
          onCancel={() => setEditing(false)}
          onSave={(cfg) =>
            act(async () => {
              await api.saveBackup(cfg)
              setEditing(false)
            }, 'Saved. Snapshots are being copied to the bucket, starting with the ones already on this server.')
          }
          onTest={(cfg) => act(() => api.testBackup(cfg), 'The bucket works: Coldship wrote, listed and deleted a test file.')}
        />
      ) : (
        <Card>
          <CardHeader>
            <CardTitle>
              <h2 className="flex items-center gap-2">
                <DatabaseBackup className="size-4 text-muted-foreground" aria-hidden />
                <Mono className="text-sm">
                  {st.bucket}
                  {st.prefix ? `/${st.prefix}` : ''}
                </Mono>
              </h2>
            </CardTitle>
            <CardDescription className="break-all">{st.endpoint}</CardDescription>
            {admin && (
              <CardAction className="flex gap-2">
                <Button variant="outline" size="sm" disabled={busy} onClick={() => act(() => api.syncBackups(), 'Synced.')}>
                  <RefreshCw className={busy ? 'animate-spin' : undefined} aria-hidden />
                  Sync now
                </Button>
                <Button variant="ghost" size="sm" onClick={() => setEditing(true)}>
                  Edit
                </Button>
              </CardAction>
            )}
          </CardHeader>
          <CardContent>
            <dl className="grid gap-4 text-sm sm:grid-cols-4">
              <div>
                <dt className="text-muted-foreground">Status</dt>
                <dd className="mt-1">
                  <SyncState st={st} />
                </dd>
              </div>
              <div>
                <dt className="text-muted-foreground">Last sync</dt>
                <dd className="mt-1">{st.last_sync ? <Time iso={st.last_sync} /> : 'not yet'}</dd>
              </div>
              <div>
                <dt className="text-muted-foreground">Copied</dt>
                <dd className="mt-1">{st.uploaded} snapshots</dd>
              </div>
              <div>
                <dt className="text-muted-foreground">Keeping</dt>
                <dd className="mt-1">{st.keep} per database</dd>
              </div>
            </dl>
            {st.last_error && (
              <Alert variant="destructive" className="mt-4">
                <AlertDescription className="break-words">{st.last_error}</AlertDescription>
              </Alert>
            )}
            {admin && (
              <div className="mt-4 border-t pt-4">
                <Button
                  variant="ghost"
                  size="sm"
                  className="text-destructive hover:text-destructive"
                  disabled={busy}
                  onClick={() => {
                    if (window.confirm('Stop copying snapshots off-site? Backups already in the bucket are kept.'))
                      act(() => api.deleteBackup(), 'Off-site backups are off. Nothing in the bucket was deleted.')
                  }}
                >
                  Turn off
                </Button>
              </div>
            )}
          </CardContent>
        </Card>
      )}

      {st.configured && (
        <section className="mt-8">
          <h2 className="mb-3 text-base font-semibold">In the bucket</h2>
          {list.error && <ErrorBox error={list.error} />}
          {list.loading && !list.data ? (
            <TableSkeleton cols={5} />
          ) : !list.data?.length ? (
            <p className="text-sm text-muted-foreground">Nothing yet. New snapshots are copied within a minute.</p>
          ) : (
            <Card className="py-0">
              <BackupTable
                items={list.data}
                busy={busy}
                canRestore={(b) => admin && !b.app && services.get(b.database)?.engine === b.engine}
                onRestore={(b, reason) =>
                  act(async () => setMessage((await api.restoreDataBackup(b.database, b.key, reason)).message))
                }
                showDatabase
              />
            </Card>
          )}
          <p className="mt-3 text-sm text-muted-foreground">
            On a new server, set the same bucket, create a data service with the same name and engine, then restore. Apps' own
            databases: <Mono>coldship db import &lt;app&gt; &lt;db&gt; --backup &lt;key&gt;</Mono>, then <Mono>coldship db restore</Mono>.
          </p>
        </section>
      )}
    </>
  )
}

function humanInterval(go: string): string {
  if (go === '24h0m0s') return 'day'
  if (go === '1h0m0s') return 'hour'
  return go.replace(/0s$/, '').replace(/0m$/, '')
}

export function BackupTable({
  items,
  busy,
  canRestore,
  onRestore,
  showDatabase,
}: {
  items: OffsiteBackup[]
  busy: boolean
  canRestore: (b: OffsiteBackup) => boolean
  onRestore: (b: OffsiteBackup, reason: string) => void
  showDatabase?: boolean
}) {
  return (
    <Table>
      <TableHeader>
        <TableRow>
          {showDatabase && <TableHead>Database</TableHead>}
          <TableHead>Taken</TableHead>
          <TableHead>Size</TableHead>
          <TableHead>Reason</TableHead>
          <TableHead>
            <span className="sr-only">Actions</span>
          </TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {items.map((b) => (
          <TableRow key={b.key}>
            {showDatabase && (
              <TableCell>
                <span className="inline-flex items-center gap-2 font-medium">
                  <DataMark engine={b.engine as DataEngine} size={20} />
                  {b.app ? `${b.app}/${b.database}` : b.database}
                </span>
                <div className="text-xs text-muted-foreground">
                  {b.engine} {b.version}
                </div>
              </TableCell>
            )}
            <TableCell>
              <Time iso={b.created_at} />
            </TableCell>
            <TableCell>{bytes(b.size)}</TableCell>
            <TableCell className="text-muted-foreground">{b.reason}</TableCell>
            <TableCell className="text-right">
              {canRestore(b) && (
                <Button
                  variant="ghost"
                  size="sm"
                  disabled={busy}
                  onClick={() => {
                    const reason = window.prompt(
                      `Restore ${b.database} from the off-site backup of ${new Date(b.created_at).toLocaleString()}? All current data is replaced (it's snapshotted first). Why?`,
                    )
                    if (reason?.trim()) onRestore(b, reason)
                  }}
                >
                  Restore
                </Button>
              )}
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  )
}

function DestinationForm({
  st,
  busy,
  onSave,
  onTest,
  onCancel,
}: {
  st: BackupStatus
  busy: boolean
  onSave: (cfg: BackupConfig) => void
  onTest: (cfg: BackupConfig) => void
  onCancel: () => void
}) {
  const [cfg, setCfg] = useState<BackupConfig>(() => emptyConfig(st))
  useEffect(() => setCfg(emptyConfig(st)), [st.configured]) // eslint-disable-line react-hooks/exhaustive-deps
  const set = (k: keyof BackupConfig) => (e: React.ChangeEvent<HTMLInputElement>) =>
    setCfg({ ...cfg, [k]: k === 'keep' ? Number(e.target.value) : e.target.value })

  function submit(e: FormEvent) {
    e.preventDefault()
    onSave(cfg)
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2>Bucket</h2>
        </CardTitle>
        <CardDescription>
          Any S3-compatible storage. Coldship checks it can write there before saving. Use a bucket with encryption turned on;
          the keys only need to list, read, write and delete objects in it.
          {st.public_only && ' This organization can use public HTTPS endpoints only.'}
        </CardDescription>
      </CardHeader>
      <CardContent>
        <div className="mb-4 flex flex-wrap gap-2">
          {providers.map((p) => (
            <Button
              key={p.name}
              type="button"
              variant="outline"
              size="sm"
              onClick={() => setCfg({ ...cfg, endpoint: p.endpoint, region: p.region, path_style: false })}
            >
              {p.name}
            </Button>
          ))}
        </div>
        <form onSubmit={submit} className="grid gap-4 sm:grid-cols-2">
          <Field id="endpoint" label="Endpoint" className="sm:col-span-2">
            <Input id="endpoint" value={cfg.endpoint} onChange={set('endpoint')} placeholder="https://s3.us-east-1.amazonaws.com" required />
          </Field>
          <Field id="bucket" label="Bucket">
            <Input id="bucket" value={cfg.bucket} onChange={set('bucket')} placeholder="my-backups" required />
          </Field>
          <Field id="region" label="Region" hint="if the provider needs one">
            <Input id="region" value={cfg.region} onChange={set('region')} placeholder="us-east-1" />
          </Field>
          <Field id="access_key" label="Access key ID">
            <Input id="access_key" value={cfg.access_key} onChange={set('access_key')} autoComplete="off" required />
          </Field>
          <Field id="secret_key" label="Secret key" hint={st.secret_key_set ? 'leave empty to keep the saved one' : undefined}>
            <Input
              id="secret_key"
              type="password"
              value={cfg.secret_key}
              onChange={set('secret_key')}
              autoComplete="new-password"
              required={!st.secret_key_set}
            />
          </Field>
          <Field id="prefix" label="Folder" hint="inside the bucket">
            <Input id="prefix" value={cfg.prefix} onChange={set('prefix')} placeholder="coldship" />
          </Field>
          <Field id="keep" label="Keep" hint="backups per database">
            <Input id="keep" type="number" min={1} max={1000} value={cfg.keep} onChange={set('keep')} />
          </Field>
          <div className="flex items-start gap-3 sm:col-span-2">
            <Checkbox id="path_style" checked={cfg.path_style} onCheckedChange={(v) => setCfg({ ...cfg, path_style: v === true })} />
            <div>
              <Label htmlFor="path_style">Path-style URLs</Label>
              <p className="text-sm text-muted-foreground">For MinIO, RustFS, SeaweedFS and other self-hosted S3.</p>
            </div>
          </div>
          <div className="flex flex-wrap gap-2 sm:col-span-2">
            <Button type="submit" disabled={busy}>
              Save
            </Button>
            <Button type="button" variant="outline" disabled={busy} onClick={() => onTest(cfg)}>
              Test
            </Button>
            <Button type="button" variant="ghost" onClick={onCancel}>
              Cancel
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  )
}

function Field({ id, label, hint, className, children }: { id: string; label: string; hint?: string; className?: string; children: React.ReactNode }) {
  return (
    <div className={className}>
      <Label htmlFor={id} className="mb-1.5">
        {label}
        {hint && <span className="font-normal text-muted-foreground"> ({hint})</span>}
      </Label>
      {children}
    </div>
  )
}
