import { useState, type FormEvent } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { Loader2 } from 'lucide-react'
import { api, type DataEngine } from '../api'
import { cn } from '@/lib/utils'
import { ErrorBox, Mono, PageHeader } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { DataMark, dataKinds, isStorage } from '../components/data'

export default function NewData() {
  const nav = useNavigate()
  const [params] = useSearchParams()
  const initial = (params.get('kind') as DataEngine) || 'postgres'
  const [kind, setKind] = useState<DataEngine>(dataKinds[initial] ? initial : 'postgres')
  const [name, setName] = useState('')
  const [version, setVersion] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<Error>()
  const k = dataKinds[kind]
  const ver = version || k.versions[k.versions.length - 1]

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(undefined)
    try {
      const d = await api.createData(kind, name.trim(), ver)
      nav(`/data/${d.name}`)
    } catch (err) {
      setError(err as Error)
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <PageHeader
        title="Add a database or storage"
        description="It runs on this server, reachable only by your apps. Connect apps to it after it's created."
      />

      <Card className="max-w-3xl">
        <CardContent>
          <form className="space-y-5" onSubmit={submit}>
            <div className="grid gap-2 md:grid-cols-2" role="radiogroup" aria-label="Kind">
              {(Object.keys(dataKinds) as DataEngine[]).map((key) => {
                const selected = kind === key
                return (
                  <button
                    type="button"
                    role="radio"
                    aria-checked={selected}
                    key={key}
                    className={cn(
                      'flex items-start gap-3 rounded-lg border p-3 text-left transition-colors outline-none hover:bg-muted/50 focus-visible:ring-3 focus-visible:ring-ring/50',
                      selected && 'border-primary bg-accent/50 ring-1 ring-primary hover:bg-accent/50',
                    )}
                    onClick={() => {
                      setKind(key)
                      setVersion('')
                    }}
                  >
                    <DataMark engine={key} />
                    <span className="min-w-0">
                      <span className="block font-medium">{dataKinds[key].label}</span>
                      <span className="block text-xs text-muted-foreground">{dataKinds[key].blurb}</span>
                    </span>
                  </button>
                )
              })}
            </div>

            <div className="flex flex-col gap-4 sm:flex-row">
              <div className="flex-1 space-y-1.5">
                <Label htmlFor="data-name">Name</Label>
                <Input
                  id="data-name"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder={isStorage(kind) ? 'uploads' : 'maindb'}
                  pattern="[a-z][a-z0-9-]{0,39}"
                  title="lowercase letters, digits and dashes, starting with a letter"
                  required
                  autoFocus
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="data-version">Version</Label>
                <Select value={ver} onValueChange={setVersion}>
                  <SelectTrigger id="data-version" className="w-full sm:w-32">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {[...k.versions].reverse().map((v) => (
                      <SelectItem key={v} value={v}>
                        {v}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            </div>

            <p className="text-sm text-muted-foreground">
              Apps you connect get <Mono className="text-foreground">{k.gives}</Mono>.{' '}
              {isStorage(kind) ? 'Snapshots of S3 storage aren’t supported yet.' : 'It’s snapshotted daily and before anything destructive.'}
            </p>

            {error && <ErrorBox error={error} />}
            <div className="flex flex-wrap items-center gap-2">
              <Button type="submit" disabled={busy || !name.trim()}>
                {busy && <Loader2 className="animate-spin" />}
                {busy ? `Creating ${k.label}… (up to a minute)` : `Create ${k.label}`}
              </Button>
              <Button variant="ghost" asChild>
                <Link to="/data">Cancel</Link>
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>
    </>
  )
}
