import { useMemo, useState, type ReactNode } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Bot, Check, Copy, ExternalLink, GitBranch, Lock, Search, SlidersHorizontal, Terminal } from 'lucide-react'
import { siGithub } from 'simple-icons'
import { api, type GitHubRepo } from '@/api'
import { useLoad } from '@/hooks'
import { useSession } from '@/App'
import { Empty, ErrorBox, PageHeader, Time, ToneBadge } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

const INSTALL = 'curl -fsSL https://raw.githubusercontent.com/dunkbing/coldship/main/install.sh | COLDSHIP_CLI_ONLY=1 sh'

function GitHubMark({ className = 'size-4' }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" className={className} aria-hidden>
      <path d={siGithub.path} fill="currentColor" />
    </svg>
  )
}

// CopyBlock shows text to paste, with a copy button.
function CopyBlock({ text, label }: { text: string; label?: string }) {
  const [done, setDone] = useState(false)
  return (
    <div className="relative">
      <pre className="overflow-x-auto rounded-lg border bg-muted p-3 pr-24 font-mono text-[0.8125rem] whitespace-pre-wrap break-words">{text}</pre>
      <Button
        size="sm"
        variant="outline"
        className="absolute top-2 right-2"
        aria-label={label ? `Copy ${label}` : 'Copy'}
        onClick={() => {
          navigator.clipboard?.writeText(text)
          setDone(true)
          setTimeout(() => setDone(false), 1500)
        }}
      >
        {done ? <Check /> : <Copy />}
        {done ? 'Copied' : 'Copy'}
      </Button>
    </div>
  )
}

function Step({ n, title, children }: { n: number; title: string; children: ReactNode }) {
  return (
    <li className="flex gap-3">
      <span className="flex size-6 flex-none items-center justify-center rounded-full bg-accent text-xs font-semibold text-accent-foreground">{n}</span>
      <div className="min-w-0 flex-1 space-y-2">
        <div className="font-medium">{title}</div>
        {children}
      </div>
    </li>
  )
}

export default function Launch() {
  const me = useSession()
  return (
    <>
      <PageHeader title="Launch an app" description="Deploy from GitHub, from your terminal, or let an AI agent do it." />
      <Tabs defaultValue="github" className="gap-4">
        <TabsList className="h-auto flex-wrap">
          <TabsTrigger value="github">
            <GitHubMark /> GitHub
          </TabsTrigger>
          <TabsTrigger value="terminal">
            <Terminal /> Terminal
          </TabsTrigger>
          <TabsTrigger value="agent">
            <Bot /> AI agent
          </TabsTrigger>
          <TabsTrigger value="manual">
            <SlidersHorizontal /> Configure manually
          </TabsTrigger>
        </TabsList>
        <TabsContent value="github">
          <GitHubPanel admin={me.scope === 'admin'} limited={!!me.apps?.length} />
        </TabsContent>
        <TabsContent value="terminal">
          <TerminalPanel />
        </TabsContent>
        <TabsContent value="agent">
          <AgentPanel admin={me.scope === 'admin'} />
        </TabsContent>
        <TabsContent value="manual">
          <Card>
            <CardHeader>
              <CardTitle>Configure manually</CardTitle>
              <CardDescription>Fill in a form: a Git repository or a container image, port, domain, environment and databases.</CardDescription>
            </CardHeader>
            <CardContent>
              <Button asChild>
                <Link to="/apps/new/configure">Open the form</Link>
              </Button>
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>
    </>
  )
}

function GitHubPanel({ admin, limited }: { admin: boolean; limited: boolean }) {
  const status = useLoad(api.github, [])
  const [token, setToken] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<Error>()

  async function run(f: () => Promise<unknown>) {
    setBusy(true)
    setError(undefined)
    try {
      await f()
      status.reload()
    } catch (e) {
      setError(e as Error)
    } finally {
      setBusy(false)
    }
  }

  if (!status.data) return <Skeleton className="h-64 rounded-xl" />
  if (!status.data.connected)
    return (
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <GitHubMark className="size-5" /> Connect GitHub
          </CardTitle>
          <CardDescription>Pick a repository and branch to deploy, including private ones. Coldship stores the token encrypted.</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          {!admin ? (
            <p className="text-sm text-muted-foreground">An admin connects GitHub. Meanwhile, deploy from the terminal or with an AI agent.</p>
          ) : (
            <>
              <ol className="space-y-4">
                <Step n={1} title="Create a fine-grained token on GitHub">
                  <p className="text-sm text-muted-foreground">
                    Give it access to the repositories you'll deploy, with <strong>Contents: read-only</strong> (Metadata is included).
                  </p>
                  <Button asChild variant="outline" size="sm">
                    <a href="https://github.com/settings/personal-access-tokens/new" target="_blank" rel="noreferrer">
                      Create a token on GitHub <ExternalLink />
                    </a>
                  </Button>
                </Step>
                <Step n={2} title="Paste it here">
                  <div className="flex flex-col gap-2 sm:flex-row">
                    <Label htmlFor="ghtoken" className="sr-only">
                      GitHub token
                    </Label>
                    <Input
                      id="ghtoken"
                      type="password"
                      autoComplete="off"
                      placeholder="github_pat_…"
                      value={token}
                      onChange={(e) => setToken(e.target.value)}
                      className="font-mono"
                    />
                    <Button disabled={busy || !token.trim()} onClick={() => run(() => api.connectGitHub(token).then(() => setToken('')))}>
                      Connect
                    </Button>
                  </div>
                </Step>
              </ol>
              {error && <ErrorBox error={error} />}
            </>
          )}
        </CardContent>
      </Card>
    )

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2 text-sm text-muted-foreground">
        <span className="inline-flex items-center gap-1.5">
          <GitHubMark /> Connected as <strong className="text-foreground">{status.data.account}</strong>
        </span>
        {admin && (
          <Button variant="ghost" size="sm" disabled={busy} onClick={() => run(api.disconnectGitHub)}>
            Disconnect
          </Button>
        )}
      </div>
      {error && <ErrorBox error={error} />}
      {limited ? (
        <p className="text-sm text-muted-foreground">Your token is limited to some apps, so it can't browse GitHub. Deploy from the terminal instead.</p>
      ) : (
        <RepoPicker />
      )}
    </div>
  )
}

function RepoPicker() {
  const nav = useNavigate()
  const repos = useLoad(api.githubRepos, [])
  const [q, setQ] = useState('')
  const [picked, setPicked] = useState<GitHubRepo>()
  const branches = useLoad(() => (picked ? api.githubBranches(picked.full_name) : Promise.resolve([] as string[])), [picked?.full_name])
  const [branch, setBranch] = useState('')
  const shown = useMemo(
    () => (repos.data ?? []).filter((r) => r.full_name.toLowerCase().includes(q.trim().toLowerCase())).slice(0, 50),
    [repos.data, q],
  )

  if (repos.error) return <ErrorBox error={repos.error} />
  if (picked)
    return (
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <GitHubMark /> {picked.full_name}
            {picked.private && (
              <ToneBadge t="muted">
                <Lock className="size-3" /> private
              </ToneBadge>
            )}
          </CardTitle>
          {picked.description && <CardDescription>{picked.description}</CardDescription>}
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="max-w-sm space-y-1.5">
            <Label htmlFor="branch">Branch</Label>
            {branches.data ? (
              <Select value={branch || picked.default_branch} onValueChange={setBranch}>
                <SelectTrigger id="branch" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {branches.data.map((b) => (
                    <SelectItem key={b} value={b}>
                      <GitBranch className="size-3.5" /> {b}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            ) : (
              <Skeleton className="h-9 w-full" />
            )}
          </div>
          <div className="flex flex-wrap gap-2">
            <Button
              onClick={() =>
                nav(`/apps/new/configure?git=${encodeURIComponent(picked.clone_url)}&ref=${encodeURIComponent(branch || picked.default_branch)}`)
              }
            >
              Continue
            </Button>
            <Button variant="ghost" onClick={() => setPicked(undefined)}>
              Pick another repository
            </Button>
          </div>
        </CardContent>
      </Card>
    )

  return (
    <Card>
      <CardHeader>
        <CardTitle>Pick a repository</CardTitle>
        <div className="relative mt-2">
          <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden />
          <Input aria-label="Search repositories" placeholder="Search" value={q} onChange={(e) => setQ(e.target.value)} className="pl-9" />
        </div>
      </CardHeader>
      <CardContent className="p-0">
        {!repos.data ? (
          <div className="space-y-2 p-4">
            {Array.from({ length: 5 }, (_, i) => (
              <Skeleton key={i} className="h-10 w-full" />
            ))}
          </div>
        ) : shown.length === 0 ? (
          <div className="p-4">
            <Empty title="No repositories">The token can't see any repository{q && ' matching that'}. Give it access to more on GitHub.</Empty>
          </div>
        ) : (
          <ul className="divide-y border-t">
            {shown.map((r) => (
              <li key={r.full_name}>
                <button
                  className="flex w-full items-center justify-between gap-3 px-6 py-3 text-left hover:bg-muted/60 focus-visible:bg-muted/60 focus-visible:outline-none"
                  onClick={() => {
                    setPicked(r)
                    setBranch('')
                  }}
                >
                  <span className="min-w-0">
                    <span className="flex items-center gap-2 font-medium">
                      {r.full_name}
                      {r.private && <Lock className="size-3.5 text-muted-foreground" aria-label="private" />}
                    </span>
                    {r.description && <span className="block truncate text-sm text-muted-foreground">{r.description}</span>}
                  </span>
                  <span className="flex-none text-xs text-muted-foreground">
                    <Time iso={r.updated_at} />
                  </span>
                </button>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  )
}

function TerminalPanel() {
  const url = window.location.origin
  return (
    <Card>
      <CardHeader>
        <CardTitle>Deploy from your terminal</CardTitle>
        <CardDescription>No config file needed: Coldship works out how to build your project.</CardDescription>
      </CardHeader>
      <CardContent>
        <ol className="space-y-5">
          <Step n={1} title="Install the coldship command (skip this on the server itself)">
            <CopyBlock text={INSTALL} label="install command" />
          </Step>
          <Step n={2} title="Sign in to this server">
            <CopyBlock text={`coldship login ${url}`} label="login command" />
            <p className="text-sm text-muted-foreground">It asks for an API token. The admin token was printed when Coldship was installed.</p>
          </Step>
          <Step n={3} title="Deploy, inside your project's Git repository">
            <CopyBlock text="coldship deploy --domain app.example.com" label="deploy command" />
            <p className="text-sm text-muted-foreground">
              Or deploy an image: <code className="font-mono">coldship deploy --name web --image nginx:1.27-alpine</code>. Add a database with{' '}
              <code className="font-mono">--db postgres</code>.
            </p>
          </Step>
        </ol>
      </CardContent>
    </Card>
  )
}

function agentPrompt(url: string, token: string, patterns: string[]) {
  const limit = patterns.length
    ? `\nYour token only works for apps named ${patterns.join(' or ')}: name new apps to match (for example ${patterns[0].replace('*', 'web')}).\n`
    : ''
  return `You can deploy and manage apps on my Coldship server (a self-hosted app platform).
${limit}
Set up:
1. Install the CLI if it's missing: ${INSTALL}
2. Connect Coldship's MCP server. In Claude Code:
   claude mcp add coldship -e COLDSHIP_URL=${url} -e COLDSHIP_TOKEN=${token} -- coldship mcp
   Other agents: run "coldship mcp" as a stdio MCP server with COLDSHIP_URL and COLDSHIP_TOKEN set.

How to deploy this project:
- Plan first with coldship_plan, using patch: {name, build: {git: <repo URL>, ref: <branch>}} or {name, image: <image>}. Add a domain with add_domains.
- Read the plan's changes, then apply it with coldship_apply and a short reason.
- Need a database or file storage? Create it with coldship_create_data (postgres, mysql, redis or s3) and connect it with patch add_uses.
- If a deploy fails, read coldship_deploy_log and coldship_diagnose, fix the cause, and try again.
- If anything returns approval_required, stop and ask me to approve it in the Coldship dashboard. Never try to work around it.`
}

function AgentPanel({ admin }: { admin: boolean }) {
  const url = window.location.origin
  const [name, setName] = useState('claude')
  const [apps, setApps] = useState('agent-*')
  const [token, setToken] = useState<string>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<Error>()

  async function create() {
    setBusy(true)
    setError(undefined)
    try {
      const patterns = apps.split(/[\s,]+/).filter(Boolean)
      const t = await api.createToken(name.trim(), 'deploy', patterns.length ? patterns : undefined)
      setToken(t.token)
    } catch (e) {
      setError(e as Error)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Let an AI agent deploy it</CardTitle>
        <CardDescription>
          Give your agent (Claude Code, Codex, Cursor…) a token and this prompt. It can deploy and fix things on its own; anything destructive in production waits for your approval.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <ol className="space-y-5">
          <Step n={1} title="Create a token for the agent">
            {!admin ? (
              <p className="text-sm text-muted-foreground">Ask an admin for a deploy token limited to the agent's apps.</p>
            ) : token ? (
              <p className="text-sm text-muted-foreground">
                Created. It's in the prompt below and isn't shown again: copy the prompt now.
              </p>
            ) : (
              <div className="grid gap-3 sm:grid-cols-[1fr_1fr_auto] sm:items-end">
                <div className="space-y-1.5">
                  <Label htmlFor="tname">Token name</Label>
                  <Input id="tname" value={name} onChange={(e) => setName(e.target.value)} />
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="tapps">Apps it may use</Label>
                  <Input id="tapps" value={apps} onChange={(e) => setApps(e.target.value)} placeholder="every app" className="font-mono" />
                </div>
                <Button disabled={busy || !name.trim()} onClick={create}>
                  Create token
                </Button>
                <p className="text-xs text-muted-foreground sm:col-span-3">
                  Patterns like <code className="font-mono">agent-*</code> let it create and change only apps with matching names, and see nothing else. Leave it empty for every app.
                </p>
              </div>
            )}
            {error && <ErrorBox error={error} />}
          </Step>
          <Step n={2} title="Give your agent this prompt">
            <CopyBlock text={agentPrompt(url, token ?? '<TOKEN>', apps.split(/[\s,]+/).filter(Boolean))} label="agent prompt" />
          </Step>
        </ol>
      </CardContent>
    </Card>
  )
}
