import { useState, type FormEvent, type ReactNode } from 'react'
import { BellRing, MessageSquare, Plus, Send, Trash2, Webhook } from 'lucide-react'
import { siDiscord, siTelegram } from 'simple-icons'
import { api, type Channel, type ChannelKind, type EventKind } from '../api'
import { useSession } from '../App'
import { useLoad } from '../hooks'
import { CopyBlock, Empty, ErrorBox, Mono, PageHeader, StatusDot, TableSkeleton, Time } from '@/components/common'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { notify } from '@/components/toasts'
import { cn } from '@/lib/utils'

const kinds: { kind: ChannelKind; label: string; how: ReactNode }[] = [
  {
    kind: 'slack',
    label: 'Slack',
    how: (
      <>
        In Slack: <b>Apps → Incoming Webhooks → Add to Slack</b>, pick a channel, and copy the webhook URL.
      </>
    ),
  },
  {
    kind: 'discord',
    label: 'Discord',
    how: (
      <>
        In Discord: channel settings → <b>Integrations → Webhooks → New Webhook → Copy Webhook URL</b>.
      </>
    ),
  },
  {
    kind: 'telegram',
    label: 'Telegram',
    how: (
      <>
        Create a bot with <b>@BotFather</b> and copy its token. Add the bot to your chat and send it a message; the chat id is
        in <Mono className="break-all">https://api.telegram.org/bot&lt;token&gt;/getUpdates</Mono> (groups start with <Mono>-</Mono>).
      </>
    ),
  },
  {
    kind: 'webhook',
    label: 'Webhook',
    how: (
      <>
        Coldship POSTs each event as JSON, signed: <Mono>X-Coldship-Signature</Mono> is <Mono>sha256=</Mono> HMAC-SHA256 of{' '}
        <Mono>X-Coldship-Timestamp + "." + body</Mono> with the secret shown after adding.
      </>
    ),
  },
]

export function ChannelMark({ kind, size = 32 }: { kind: ChannelKind; size?: number }) {
  const box = 'flex flex-none items-center justify-center rounded-lg border bg-card'
  const logo = kind === 'discord' ? siDiscord : kind === 'telegram' ? siTelegram : undefined
  if (logo)
    return (
      <span className={box} style={{ width: size, height: size }} aria-hidden>
        <svg viewBox="0 0 24 24" width={size * 0.55} height={size * 0.55} fill={`#${logo.hex}`}>
          <path d={logo.path} />
        </svg>
      </span>
    )
  const Icon = kind === 'slack' ? MessageSquare : Webhook
  return (
    <span className={cn(box, kind === 'slack' ? 'text-[#4A154B] dark:text-[#E01E5A]' : 'text-muted-foreground')} style={{ width: size, height: size }} aria-hidden>
      <Icon style={{ width: size * 0.5, height: size * 0.5 }} />
    </span>
  )
}

export default function Notifications() {
  const me = useSession()
  const admin = me.scope === 'admin'
  const chans = useLoad(api.channels, [me.org?.id], 15000)
  const events = useLoad(api.channelEvents, [me.org?.id])
  const [adding, setAdding] = useState(false)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState<Error>()
  const [secret, setSecret] = useState('')

  async function act(key: string, f: () => Promise<unknown>, ok?: string) {
    setBusy(key)
    setError(undefined)
    try {
      await f()
      if (ok) notify(ok)
      chans.reload()
    } catch (err) {
      setError(err as Error)
      chans.reload()
    } finally {
      setBusy('')
    }
  }

  const label = (k: string) => events.data?.find((e) => e.kind === k)?.label ?? k

  return (
    <>
      <PageHeader
        title="Notifications"
        description="Get a message when something needs attention: a deploy fails, an app goes down, a change waits for approval, backups fail, the disk fills up."
        actions={
          admin &&
          !adding && (
            <Button onClick={() => setAdding(true)}>
              <BellRing />
              Add channel
            </Button>
          )
        }
      />

      {error && <ErrorBox error={error} />}
      {secret && (
        <Card className="mb-4">
          <CardHeader>
            <CardTitle>
              <h2>Signing secret</h2>
            </CardTitle>
            <CardDescription>Shown once. Your receiver checks each delivery's signature with it.</CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            <CopyBlock text={secret} label="signing secret" />
            <Button variant="outline" size="sm" onClick={() => setSecret('')}>
              I saved it
            </Button>
          </CardContent>
        </Card>
      )}

      {adding && events.data && (
        <AddChannel
          events={events.data}
          busy={busy === 'add'}
          onCancel={() => setAdding(false)}
          onAdd={(r) =>
            act('add', async () => {
              const c = await api.createChannel(r)
              setAdding(false)
              if (c.secret) setSecret(c.secret)
              notify(`Added ${c.name}`, { description: 'Send a test message to check it.' })
            })
          }
        />
      )}

      {chans.error && <ErrorBox error={chans.error} />}
      {chans.loading && !chans.data ? (
        <TableSkeleton cols={4} />
      ) : chans.data?.length === 0 && !adding ? (
        <Empty
          title="No channels yet"
          icon={<BellRing />}
          action={
            admin && (
              <Button onClick={() => setAdding(true)}>
                <Plus /> Add channel
              </Button>
            )
          }
        >
          Add Slack, Discord, Telegram or a webhook, and Coldship tells you when a deploy fails or an app goes down.
          {!admin && ' An admin can add one.'}
        </Empty>
      ) : (
        <div className="space-y-3">
          {chans.data?.map((c) => (
            <ChannelRow
              key={c.id}
              c={c}
              admin={admin}
              busy={busy}
              label={label}
              onTest={() => act('test:' + c.id, () => api.testChannel(c.id), `Test message delivered to ${c.target}.`)}
              onDelete={() => {
                if (window.confirm(`Remove ${c.name}? It stops getting notifications.`))
                  act('del:' + c.id, () => api.deleteChannel(c.id), `Removed ${c.name}.`)
              }}
            />
          ))}
        </div>
      )}
    </>
  )
}

function ChannelRow({
  c,
  admin,
  busy,
  label,
  onTest,
  onDelete,
}: {
  c: Channel
  admin: boolean
  busy: string
  label: (k: string) => string
  onTest: () => void
  onDelete: () => void
}) {
  return (
    <Card className="py-4">
      <CardContent className="flex flex-wrap items-start gap-4">
        <ChannelMark kind={c.kind} />
        <div className="min-w-0 flex-1">
          <div className="font-medium break-all">{c.name}</div>
          <div className="text-sm break-all text-muted-foreground">{c.target}</div>
          <div className="mt-2 flex flex-wrap gap-1.5">
            {c.events.map((e) => (
              <Badge key={e} variant="secondary" className="font-normal" title={e}>
                {label(e)}
              </Badge>
            ))}
          </div>
        </div>
        <div className="flex w-full flex-row flex-wrap items-center justify-between gap-2 text-sm sm:w-auto sm:flex-col sm:items-end">
          {c.last_at ? (
            c.last_error ? (
              <span title={c.last_error}>
                <StatusDot dot="bg-destructive" text="text-destructive" label="Last delivery failed" />
              </span>
            ) : (
              <span className="text-muted-foreground">
                Delivered <Time iso={c.last_at} />
              </span>
            )
          ) : (
            <span className="text-muted-foreground">Nothing sent yet</span>
          )}
          {admin && (
            <div className="flex gap-1">
              <Button variant="outline" size="sm" disabled={!!busy} onClick={onTest}>
                <Send className={busy === 'test:' + c.id ? 'animate-pulse' : undefined} />
                Send test
              </Button>
              <Button variant="ghost" size="sm" disabled={!!busy} onClick={onDelete} aria-label={`Remove ${c.name}`}>
                <Trash2 />
              </Button>
            </div>
          )}
        </div>
        {c.last_error && <p className="w-full text-sm break-words text-destructive">{c.last_error}</p>}
      </CardContent>
    </Card>
  )
}

function AddChannel({
  events,
  busy,
  onAdd,
  onCancel,
}: {
  events: EventKind[]
  busy: boolean
  onAdd: (r: { kind: ChannelKind; name: string; url?: string; bot_token?: string; chat_id?: string; events: string[] }) => void
  onCancel: () => void
}) {
  const [kind, setKind] = useState<ChannelKind>('slack')
  const [name, setName] = useState('')
  const [url, setUrl] = useState('')
  const [token, setToken] = useState('')
  const [chat, setChat] = useState('')
  const [picked, setPicked] = useState<string[]>(() => events.filter((e) => e.default).map((e) => e.kind))
  const info = kinds.find((k) => k.kind === kind)!

  function submit(e: FormEvent) {
    e.preventDefault()
    onAdd(
      kind === 'telegram'
        ? { kind, name, bot_token: token, chat_id: chat, events: picked }
        : { kind, name, url, events: picked },
    )
  }

  return (
    <Card className="mb-4">
      <CardHeader>
        <CardTitle>
          <h2>Add a channel</h2>
        </CardTitle>
      </CardHeader>
      <CardContent>
        <div className="mb-4 grid grid-cols-2 gap-2 sm:grid-cols-4" role="radiogroup" aria-label="Channel kind">
          {kinds.map((k) => (
            <button
              key={k.kind}
              type="button"
              role="radio"
              aria-checked={kind === k.kind}
              onClick={() => setKind(k.kind)}
              className={cn(
                'flex items-center gap-2 rounded-lg border bg-card p-2.5 text-left text-sm font-medium transition-colors hover:bg-accent/40 focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none',
                kind === k.kind && 'border-primary bg-accent/40',
              )}
            >
              <ChannelMark kind={k.kind} size={26} />
              {k.label}
            </button>
          ))}
        </div>
        <p className="mb-4 text-sm text-muted-foreground">{info.how}</p>
        <form onSubmit={submit} className="grid gap-4 sm:grid-cols-2">
          {kind === 'telegram' ? (
            <>
              <div>
                <Label htmlFor="ch-token" className="mb-1.5">
                  Bot token
                </Label>
                <Input id="ch-token" type="password" autoComplete="off" value={token} onChange={(e) => setToken(e.target.value)} required />
              </div>
              <div>
                <Label htmlFor="ch-chat" className="mb-1.5">
                  Chat id
                </Label>
                <Input id="ch-chat" value={chat} onChange={(e) => setChat(e.target.value)} placeholder="-1001234567890" required />
              </div>
            </>
          ) : (
            <div className="sm:col-span-2">
              <Label htmlFor="ch-url" className="mb-1.5">
                {kind === 'webhook' ? 'URL' : 'Webhook URL'}
              </Label>
              <Input
                id="ch-url"
                type="password"
                autoComplete="off"
                value={url}
                onChange={(e) => setUrl(e.target.value)}
                placeholder={
                  kind === 'slack'
                    ? 'https://hooks.slack.com/services/…'
                    : kind === 'discord'
                      ? 'https://discord.com/api/webhooks/…'
                      : 'https://example.com/coldship-events'
                }
                required
              />
            </div>
          )}
          <div className="sm:col-span-2">
            <Label htmlFor="ch-name" className="mb-1.5">
              Name <span className="font-normal text-muted-foreground">(optional)</span>
            </Label>
            <Input id="ch-name" value={name} onChange={(e) => setName(e.target.value)} placeholder={`${info.label}: #ops`} />
          </div>
          <fieldset className="sm:col-span-2">
            <legend className="mb-2 text-sm font-medium">Send a message when</legend>
            <div className="grid gap-2 sm:grid-cols-2">
              {events.map((ev) => (
                <label key={ev.kind} className="flex items-center gap-2 text-sm">
                  <Checkbox
                    checked={picked.includes(ev.kind)}
                    onCheckedChange={(v) => setPicked(v === true ? [...picked, ev.kind] : picked.filter((p) => p !== ev.kind))}
                  />
                  {ev.label}
                </label>
              ))}
            </div>
          </fieldset>
          <div className="flex flex-wrap gap-2 sm:col-span-2">
            <Button type="submit" disabled={busy || picked.length === 0}>
              Add
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
