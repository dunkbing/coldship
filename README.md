# Coldship

A self-hosted app platform designed for **AI agents as first-class users**.
Agents work through MCP or a JSON CLI. Humans keep control through scoped tokens, approvals, an audit log,
and a web dashboard embedded in the binary.

📖 Full documentation lives in [`website/`](website/) (Astro + Starlight): `make website-dev` to browse it locally.

## Agent-native by design

| Principle | How |
|---|---|
| Declarative | One `coldship.yaml` per app. Unknown fields are errors, so a typo never becomes a silent no-op. |
| Plan, then apply | `plan` is a dry run that returns a diff marked destructive or not. `apply` takes a plan id, so an agent can only apply what it reviewed. |
| Safe retries | Re-applying an applied plan returns the original result. Plans go stale if the app changed, and expire after 30 minutes. |
| Errors for LLMs | Every error has a stable `code`, a `hint`, and often a `suggested_fix` (for example `{"port": 3000}`). |
| Guardrails | Tokens have scopes (`read` < `deploy` < `admin`). A destructive change to a `production` app needs a human `approve`, and agents get no approve tool. |
| One-call debugging | `diagnose` returns container state, latest deploy, logs, and likely causes such as crash loops, OOM, port mismatches, or missing env. |
| Rollback | `rollback` redeploys the last good deployment. A failed deploy never replaces the "current" spec. |
| Domains + HTTPS | `domains: [...]` routes through a managed Caddy proxy with automatic certificates. |
| Zero-downtime deploys | New version starts next to the old, must pass its health check, then traffic switches. If it fails, the old one keeps serving. |
| Preview environments | `coldship preview create web --ref feat/x --ttl 2h`: a throwaway copy with fresh volumes and no production secrets by default, deleted automatically. |
| Git webhooks | `on_push: {action: plan\|deploy, previews: true}`: signed pushes from GitHub, GitLab or Gitea plan or deploy the branch tip, and branches get previews. |
| Managed databases | `databases: [{name: main, engine: postgres}]`: credentials generated and injected as `DATABASE_URL`, never shown; snapshots daily and before anything destructive; restores are approved plans. |
| Secrets at rest | Env values, database passwords, webhook secrets and deploy logs are AES-256-GCM encrypted in the state file; `coldship secrets rotate-key`. Back up `coldship.key` separately. |
| Self-upgrades | Signed releases (Ed25519 + SHA-256), installed automatically when idle; "new version available" in the dashboard and CLI. |
| Build from Git | `build: {git, ref}` instead of `image`; no Dockerfile needed for Node, Bun, Python, Go and static sites (`coldship build detect` explains the generated one). Plans pin the commit, failed builds never touch the running app, built images are reused on rollback. |
| No secret leaks | Env values are redacted in all output. The runtime gets them through a 0600 env file, never argv. |
| Prompt-injection hygiene | Logs are labeled untrusted data in every response and tool description. |
| Accountability | Every mutation is audited with the actor (token name) and a required `reason`. |

## Architecture

```
 Claude Code / agents ──MCP(stdio)──┐
 humans / scripts ───CLI (--json)───┼──HTTP JSON API──► engine ──► runtime (Docker)
                                    │   (bearer token)    │
                                    │                     └──► store (SQLite: apps, plans,
                                    │                               deployments, tokens, audit)
```

A single `coldship` binary contains the server, the CLI, and the MCP server. The CLI and MCP server are both
clients of the HTTP API, so every action goes through the same auth, guardrails, and audit.

```
cmd/coldship/          CLI entry point (cobra): server, mcp, plan, apply, deploy, approve, rollback, ...
internal/spec/       coldship.yaml schema, strict parsing, validation with suggested fixes, redaction
internal/planner/    current-vs-desired diff, destructive classification
internal/engine/     plan / approve / apply / rollback / diagnose (the control plane)
internal/store/      SQLite persistence
internal/runtime/    Runtime interface, Docker driver (via docker CLI), in-memory fake for tests
internal/builder/    Git builds: resolve ref -> commit, fetch, docker build (with symlink/protocol guards)
internal/api/        HTTP API + auth/scopes
internal/client/     Go API client (used by the CLI and MCP)
internal/mcpserver/  MCP tools for agents
internal/apierr/     structured errors
internal/web/        embeds and serves the dashboard build (go:embed)
dashboard/           React + Vite dashboard (bun)
website/             documentation site (Astro + Starlight, bun)
scripts/             screenshots.sh: regenerates the docs screenshots
```

## Quick start

Requires Go 1.26+ (older Go downloads it automatically), [Bun](https://bun.sh), and Docker.

```sh
make build                     # builds the dashboard, embeds it, builds bin/coldship
./bin/coldship server            # prints a one-time admin token on first start
                               # dashboard: http://127.0.0.1:7070
export COLDSHIP_TOKEN=cst_...    # the admin token

./bin/coldship plan   -f examples/hello/coldship.yaml
./bin/coldship deploy -f examples/hello/coldship.yaml --reason "first deploy"
./bin/coldship diagnose hello
./bin/coldship audit
```

### Give an agent access

Create a `deploy`-scoped token for the agent. It can then plan, apply, and roll back, but never approve.

```sh
./bin/coldship token create --name claude --scope deploy
claude mcp add coldship -e COLDSHIP_URL=http://127.0.0.1:7070 -e COLDSHIP_TOKEN=cst_... -- /path/to/bin/coldship mcp
```

MCP tools: `coldship_list_apps`, `coldship_get_app`, `coldship_plan`, `coldship_get_plan`, `coldship_apply`,
`coldship_rollback`, `coldship_logs`, `coldship_diagnose`, `coldship_deployments`.

### CLI contract for scripts and agents

- `--json` prints machine-readable output on stdout. Errors go to stderr as `{"error": {...}}`.
- Exit codes: `0` ok, `1` error, `3` approval required, `4` plan stale or expired.

## Spec

```yaml
name: web              # lowercase, digits, dashes
stage: staging         # production | staging | preview
image: nginx:1.27-alpine
port: 80               # container port
host_port: 8080        # optional: publish on the host
memory: 256m           # optional limit
env:
  API_KEY: secret      # write-only, redacted everywhere
volumes:
  - name: data         # removing a volume from the spec deletes its data (destructive)
    path: /data
```

Or build from Git instead of `image` (with your Dockerfile, or one Coldship generates for Node, Bun, Python, Go and static sites):

```yaml
build:
  git: https://github.com/acme/web.git   # https, http or ssh; no credentials in the URL
  ref: main                               # resolved to a commit at plan time
  context: .
  dockerfile: Dockerfile
```

Private repos: `coldship server --git-credentials <git-credential-store file>`, or SSH keys of the server user.

## Roadmap

- [x] Build from a Git repo with a Dockerfile
- [x] Builds without a Dockerfile (Node, Bun, Python, Go, static)
- [x] Git webhooks: plan, deploy or preview on push
- [x] Reverse proxy + automatic TLS (managed Caddy) with domains in the spec
- [x] Zero-downtime deploys (start new container, health check, then swap)
- [x] Signed self-upgrades
- [x] Ephemeral preview environments with a TTL
- [x] Managed databases (Postgres/Redis) with snapshots taken before destructive ops
- [ ] Policies: spend/instance caps per token, per-app permissions
- [ ] Approval notifications (Slack/webhook) and event webhooks to wake agents on failures
- [ ] Multi-node: a separate node agent; Postgres as the state store
- [x] Secrets encrypted at rest (AES-256-GCM, rotatable key)

## Development

| Target | |
|---|---|
| `make build` | Dashboard + Go binary |
| `make test` | Go tests |
| `make website-dev` / `make website` | Docs site dev server / static build (set `SITE_URL` for the sitemap) |
| `make release VERSION=x.y.z UPDATE_PUBLIC_KEY=... UPDATE_URL=...` | Signed release in `dist/` (see the Upgrading guide) |
| `make screenshots` | Regenerate docs screenshots (Docker; uses the Playwright image) |

Use `bun add` to add JS dependencies rather than editing `package.json` by hand.
