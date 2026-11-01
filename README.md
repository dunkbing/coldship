# Hangar

A self-hosted app platform designed for **AI agents as first-class users**.
Agents work through MCP or a JSON CLI. Humans keep control through scoped tokens, approvals, and an audit log.

## Agent-native by design

| Principle | How |
|---|---|
| Declarative | One `hangar.yaml` per app. Unknown fields are errors, so a typo never becomes a silent no-op. |
| Plan, then apply | `plan` is a dry run that returns a diff marked destructive or not. `apply` takes a plan id, so an agent can only apply what it reviewed. |
| Safe retries | Re-applying an applied plan returns the original result. Plans go stale if the app changed, and expire after 30 minutes. |
| Errors for LLMs | Every error has a stable `code`, a `hint`, and often a `suggested_fix` (for example `{"port": 3000}`). |
| Guardrails | Tokens have scopes (`read` < `deploy` < `admin`). A destructive change to a `production` app needs a human `approve`, and agents get no approve tool. |
| One-call debugging | `diagnose` returns container state, latest deploy, logs, and likely causes such as crash loops, OOM, port mismatches, or missing env. |
| Rollback | `rollback` redeploys the last good deployment. A failed deploy never replaces the "current" spec. |
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

A single `hangar` binary contains the server, the CLI, and the MCP server. The CLI and MCP server are both
clients of the HTTP API, so every action goes through the same auth, guardrails, and audit.

```
cmd/hangar/          CLI entry point (cobra): server, mcp, plan, apply, deploy, approve, rollback, ...
internal/spec/       hangar.yaml schema, strict parsing, validation with suggested fixes, redaction
internal/planner/    current-vs-desired diff, destructive classification
internal/engine/     plan / approve / apply / rollback / diagnose (the control plane)
internal/store/      SQLite persistence
internal/runtime/    Runtime interface, Docker driver (via docker CLI), in-memory fake for tests
internal/api/        HTTP API + auth/scopes
internal/client/     Go API client (used by the CLI and MCP)
internal/mcpserver/  MCP tools for agents
internal/apierr/     structured errors
```

## Quick start

Requires Go 1.26+ (older Go downloads it automatically) and Docker.

```sh
make build
./bin/hangar server            # prints a one-time admin token on first start
export HANGAR_TOKEN=hgr_...    # the admin token

./bin/hangar plan   -f examples/hello/hangar.yaml
./bin/hangar deploy -f examples/hello/hangar.yaml --reason "first deploy"
./bin/hangar diagnose hello
./bin/hangar audit
```

### Give an agent access

Create a `deploy`-scoped token for the agent. It can then plan, apply, and roll back, but never approve.

```sh
./bin/hangar token create --name claude --scope deploy
claude mcp add hangar -e HANGAR_URL=http://127.0.0.1:7070 -e HANGAR_TOKEN=hgr_... -- /path/to/bin/hangar mcp
```

MCP tools: `hangar_list_apps`, `hangar_get_app`, `hangar_plan`, `hangar_get_plan`, `hangar_apply`,
`hangar_rollback`, `hangar_logs`, `hangar_diagnose`, `hangar_deployments`.

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

## Roadmap

- [ ] Build from a Git repo (Nixpacks/Buildpacks-style detection) in addition to images
- [ ] Reverse proxy + automatic TLS (embedded Caddy) with domains in the spec
- [ ] Zero-downtime deploys (start new container, health check, then swap)
- [ ] Ephemeral preview environments with a TTL (`stage: preview`, `ttl: 2h`)
- [ ] Managed databases (Postgres/Redis) with snapshots taken before destructive ops
- [ ] Policies: spend/instance caps per token, per-app permissions
- [ ] Approval notifications (Slack/webhook) and event webhooks to wake agents on failures
- [ ] Multi-node: a separate node agent; Postgres as the state store
- [ ] Secrets encrypted at rest
- [ ] Read-only web dashboard of agent activity
