---
title: CLI
description: Every hangar command, its flags and its exit codes.
---

The `hangar` binary is the server, the CLI and the MCP server.

## Global flags

| Flag | Env | Default | Description |
|---|---|---|---|
| `--url` | `HANGAR_URL` | `http://127.0.0.1:7070` | Server URL |
| `--token` | `HANGAR_TOKEN` | | API token |
| `--json` | | off | Machine-readable output on stdout; errors as JSON on stderr |

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success |
| `1` | Any other error |
| `3` | `approval_required` |
| `4` | `plan_stale` or `plan_expired` |

## Server

### `hangar server`

Runs the API server, the Docker runtime and the dashboard.

| Flag | Env | Default | |
|---|---|---|---|
| `--addr` | `HANGAR_ADDR` | `127.0.0.1:7070` | Listen address |
| `--data` | `HANGAR_DATA` | `hangar.db` | SQLite state file |
| `--startup-grace` | | `3s` | How long a new container must stay up for the deploy to count as successful |
| `--build-timeout` | | `20m` | Maximum time for one Git build |
| `--git-credentials` | `HANGAR_GIT_CREDENTIALS` | | git-credential-store file for private HTTPS repositories |
| `--build-dir` | `HANGAR_BUILD_DIR` | system temp dir | Where temporary build checkouts go |

On first start, with no tokens in the database, it creates an `admin` token and prints it once.

### `hangar mcp`

Runs an MCP server on stdio. Uses `HANGAR_URL` and `HANGAR_TOKEN`. See [MCP tools](/reference/mcp-tools/).

## Changing apps

| Command | Description |
|---|---|
| `hangar plan [-f hangar.yaml] [--redeploy]` | Dry run of a spec. Defaults to `./hangar.yaml`. |
| `hangar plan --delete <app>` | Dry run of deleting an app |
| `hangar apply <plan-id> --reason "..."` | Apply a reviewed plan |
| `hangar deploy [-f hangar.yaml] --reason "..." [--redeploy]` | Plan and apply in one step. Stops with exit 3 if approval is needed. |
| `hangar approve <plan-id>` | Approve a plan (admin token) |
| `hangar rollback <app> --reason "..." [--to <deployment-id>]` | Redeploy the last good or a specific deployment |

## Inspecting

| Command | Description |
|---|---|
| `hangar apps` | List apps |
| `hangar status <app>` | Spec, container state and latest deployment |
| `hangar deployments <app> [--limit 20]` | Deployment history, newest first |
| `hangar logs <app> [--tail 100] [--since 10m]` | Container logs |
| `hangar diagnose <app>` | Findings and suggested fixes |
| `hangar build-log <deployment-id>` | Build log of a Git deployment (untrusted output) |
| `hangar audit [--limit 50]` | Audit log |

## Tokens

| Command | Description |
|---|---|
| `hangar token create --name <name> [--scope deploy]` | Create a token (admin). Scopes: `read`, `deploy`, `admin`. The token is shown once. |
