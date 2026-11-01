---
title: Architecture
description: How Coldship is put together.
---

```txt
 Claude Code / agents ──MCP (stdio)──┐
 humans / scripts ────CLI (--json)───┼──► HTTP JSON API ──► engine ──► runtime (Docker)
 humans ──────────────dashboard──────┘     bearer tokens       │
                                                                └──► store (SQLite)
```

Everything ships as one Go binary. The CLI, the MCP server and the React dashboard are all clients of the HTTP API, so there is exactly one place where authentication, guardrails and auditing happen.

## Packages

| Package | Responsibility |
|---|---|
| `cmd/coldship` | CLI entry point (cobra): `server`, `mcp`, and all client commands |
| `internal/spec` | The `coldship.yaml` schema: strict parsing, validation with suggested fixes, redaction |
| `internal/planner` | Diffs current against desired spec and classifies destructive changes |
| `internal/engine` | The control plane: plan, approve, apply, rollback, diagnose |
| `internal/store` | SQLite persistence for tokens, apps, plans, deployments and the audit log |
| `internal/runtime` | `Runtime` interface; the Docker driver (via the `docker` CLI); an in-memory fake for tests |
| `internal/builder` | Git builds: resolves refs, fetches a pinned commit, runs `docker build`; a fake for tests |
| `internal/api` | HTTP routing, authentication and scope checks |
| `internal/client` | Typed Go client for the API |
| `internal/mcpserver` | MCP tools on top of the client |
| `internal/web` | Serves the dashboard, embedded with `go:embed` |
| `internal/apierr` | The structured error type |
| `dashboard/` | React + Vite dashboard source (built into `internal/web/dist`) |
| `website/` | This documentation site (Astro + Starlight) |

## Key decisions

**SQLite for state.** A single-node install needs one file and no extra service. No database server, cache or websocket service runs alongside it. The schema is plain SQL, so moving to Postgres for multi-node is mechanical.

**Plans are stored.** Apply takes a plan id instead of a spec, so what gets applied is exactly what was reviewed. Plans record the deployment they were based on, which is how staleness is detected.

**Failed deploys don't move "current".** An app's spec is its last *successful* deployment. That gives rollback and future plans a stable reference point.

**Docker via the CLI.** Shelling out to `docker` keeps the dependency tree small. The `Runtime` interface keeps it replaceable with the Docker Engine API or another runtime.

**Mutations are serialized per app.** Each app has a lock, so plan and apply checks are race-free, and a long build of one app doesn't block changes to another.

**Builds are pinned and cached.** Refs are resolved to commits at plan time. Images are tagged by commit and build configuration, so a rollback reuses an existing image instead of rebuilding.
