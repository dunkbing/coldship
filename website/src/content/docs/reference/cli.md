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
| `--proxy` | | `true` | Run the Caddy proxy for app domains |
| `--preview-domain` | `HANGAR_PREVIEW_DOMAIN` | | Previews get `https://<name>.<domain>` (needs wildcard DNS) |
| `--preview-ttl` / `--preview-max-ttl` | | `24h` / `168h` | Default and maximum preview lifetime |
| `--max-previews` | | `20` | Most previews at once |
| `--proxy-http-port` / `--proxy-https-port` | | `80` / `443` | Host ports for the proxy |
| `--acme-email` | `HANGAR_ACME_EMAIL` | | Email for Let's Encrypt notices |
| `--update-url` | `HANGAR_UPDATE_URL` | built in | Signed release manifest to check |
| `--update-public-key` | `HANGAR_UPDATE_PUBLIC_KEY` | built in | Key release manifests must be signed with |
| `--auto-upgrade` | | `true` | Install new minor/patch versions automatically when no deployment is running |
| `--update-interval` | | `6h` | How often to check for updates |

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
| `hangar deploy-log <deployment-id>` | What happened during a deployment: build, start, health checks (untrusted output) |
| `hangar version` | CLI and server versions, and whether an update is available |

## Previews

| Command | Description |
|---|---|
| `hangar preview create <app> [--ref R \| --image I] [--name N] [--ttl 2h] [--env K=V] [--inherit-env \| --no-inherit-env] [--reason "..."]` | Create or update a preview. Safe to repeat. |
| `hangar preview list` | Previews, soonest-expiring first |
| `hangar preview delete <preview> [--reason "..."]` | Delete a preview and its volumes |

## Upgrades

| Command | Description |
|---|---|
| `hangar upgrade` | Upgrade the server to the latest signed release (admin). Waits for the restart. `--check` checks now; `--no-wait` returns at once. |
| `hangar self-update` | Update this hangar binary (for CLIs on other machines). Verifies the release with the built-in key. |
| `hangar release keygen` | Create the Ed25519 key pair that signs releases (maintainers) |
| `hangar release manifest --version X os/arch=path ...` | Write a signed release manifest (maintainers; `make release` runs it) |

After other commands, the CLI prints a one-line note on stderr when a new version is available or the CLI is older than the server. It checks at most once an hour, and never prints with `--json`, when stderr isn't a terminal, or with `HANGAR_NO_UPDATE_NOTICE=1`.
| `hangar audit [--limit 50]` | Audit log |

## Tokens

| Command | Description |
|---|---|
| `hangar token create --name <name> [--scope deploy]` | Create a token (admin). Scopes: `read`, `deploy`, `admin`. The token is shown once. |
