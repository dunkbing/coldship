---
title: CLI
description: Every coldship command, its flags and its exit codes.
---

The `coldship` binary is the server, the CLI and the MCP server.

## Global flags

| Flag | Env | Default | Description |
|---|---|---|---|
| `--url` | `COLDSHIP_URL` | saved by `coldship login`, else `http://127.0.0.1:7070` | Server URL |
| `--token` | `COLDSHIP_TOKEN` | saved by `coldship login` | API token |

## Signing in

| Command | Description |
|---|---|
| `coldship login [server-url] [--token T]` | Check a token and save it with the server URL in `~/.config/coldship/config.json` (mode 0600), so other commands need no env vars. Asks for the token if `--token` isn't given. The installer does this for you on the server. |
| `coldship logout` | Forget the saved URL and token |
| `--json` | | off | Machine-readable output on stdout; errors as JSON on stderr |

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success |
| `1` | Any other error |
| `3` | `approval_required` |
| `4` | `plan_stale` or `plan_expired` |

## Server

### `coldship server`

Runs the API server, the Docker runtime and the dashboard.

| Flag | Env | Default | |
|---|---|---|---|
| `--addr` | `COLDSHIP_ADDR` | `127.0.0.1:7070` | Listen address |
| `--data` | `COLDSHIP_DATA` | `coldship.db` | SQLite state file |
| `--admin-token-file` | | | On first start, write the admin token to this file (0600) instead of printing it. The installer uses it. |
| `--startup-grace` | | `3s` | How long a new container must stay up for the deploy to count as successful |
| `--build-timeout` | | `20m` | Maximum time for one Git build |
| `--git-credentials` | `COLDSHIP_GIT_CREDENTIALS` | | git-credential-store file for private HTTPS repositories |
| `--build-dir` | `COLDSHIP_BUILD_DIR` | system temp dir | Where temporary build checkouts go |
| `--proxy` | | `true` | Run the Caddy proxy for app domains |
| `--preview-domain` | `COLDSHIP_PREVIEW_DOMAIN` | | Previews get `https://<name>.<domain>` (needs wildcard DNS) |
| `--preview-ttl` / `--preview-max-ttl` | | `24h` / `168h` | Default and maximum preview lifetime |
| `--max-previews` | | `20` | Most previews at once |
| `--secret-key-file` | `COLDSHIP_SECRET_KEY_FILE` | `coldship.key` next to `--data` | Key that encrypts stored secrets. `COLDSHIP_SECRET_KEY` (base64 32 bytes) supplies the key directly instead. |
| `--public-url` | `COLDSHIP_PUBLIC_URL` | | How Git hosts reach this server, for webhook URLs |
| `--snapshot-dir` | `COLDSHIP_SNAPSHOT_DIR` | `coldship-snapshots` next to `--data` | Where database snapshots are stored |
| `--snapshot-keep` | | `20` | Snapshots kept per database, counted separately for manual and automatic ones (so manual snapshots never push out automatic ones) |
| `--db-snapshot-interval` | | `24h` | Scheduled snapshots of every database (`0` disables) |
| `--proxy-http-port` / `--proxy-https-port` | | `80` / `443` | Host ports for the proxy |
| `--acme-email` | `COLDSHIP_ACME_EMAIL` | | Email for Let's Encrypt notices |
| `--update-url` | `COLDSHIP_UPDATE_URL` | built in (release binaries) | Release manifest to check for new versions |
| `--auto-upgrade` | | `true` | Install new minor/patch versions automatically when no deployment is running |
| `--update-interval` | | `6h` | How often to check for updates |

On first start, with no tokens in the database, it creates an `admin` token and prints it once (or writes it to `--admin-token-file`). It exits with a hint if Docker isn't installed, running, or usable by this user.

### `coldship mcp`

Runs an MCP server on stdio. Uses `COLDSHIP_URL` and `COLDSHIP_TOKEN`. See [MCP tools](/reference/mcp-tools/).

## Changing apps

| Command | Description |
|---|---|
| `coldship deploy [flags]` | Deploy without a config file. Inside a Git repository with no `--name`, deploys that repository (current branch) as an app named after the folder. Flags: `--name`, `--image` or `--git`/`--ref`, `--port` (detected for Git builds), `--host-port`, `--domain`/`--remove-domain`, `--env K=V`/`--unset-env K`, `--db postgres[:name]`, `--volume name:/path`, `--memory`, `--production`, `--redeploy`, `--reason`. Each flag changes only what it names; the rest of the app, secrets included, stays. Stops with exit 3 if approval is needed. |
| `coldship delete <app> [--yes]` | Delete an app with its volumes and databases (databases are snapshotted first). Shows what goes and asks you to type the app's name. Production apps need approval. |
| `coldship deploy -f coldship.yaml [--redeploy]` | Deploy a spec file describing the whole app. Used by default when `./coldship.yaml` exists and no app flags are given. |
| `coldship export <app>` | The app's spec as YAML. Secrets show as `<redacted>`, which deploying the file keeps as they are. |
| `coldship plan [-f coldship.yaml] [--redeploy]` | Dry run of a spec. Defaults to `./coldship.yaml`. |
| `coldship plan --delete <app>` | Dry run of deleting an app |
| `coldship apply <plan-id> --reason "..."` | Apply a reviewed plan |
| `coldship approve <plan-id>` | Approve a plan (admin token) |
| `coldship rollback <app> --reason "..." [--to <deployment-id>]` | Redeploy the last good or a specific deployment |

## Inspecting

| Command | Description |
|---|---|
| `coldship apps` | List apps |
| `coldship status <app>` | Spec, container state and latest deployment |
| `coldship deployments <app> [--limit 20]` | Deployment history, newest first |
| `coldship logs <app> [--tail 100] [--since 10m]` | Container logs |
| `coldship diagnose <app>` | Findings and suggested fixes |
| `coldship deploy-log <deployment-id>` | What happened during a deployment: build, start, health checks (untrusted output) |
| `coldship version` | CLI and server versions, and whether an update is available |

## Secrets

| Command | Description |
|---|---|
| `coldship secrets status` | Key id, key source, and how many stored values are encrypted (admin) |
| `coldship secrets rotate-key` | Re-encrypt all stored secrets with a new key, then drop the old one (admin) |
| `coldship secrets decrypt --data coldship.db --yes` | Offline, server stopped: write secrets back as plaintext, for downgrading to a version without encryption |

## Builds

| Command | Description |
|---|---|
| `coldship build detect [-f coldship.yaml \| --app A] [--dockerfile]` | Show how the app would be built (its Dockerfile, or what Coldship detects and generates) without building |

## Webhooks

| Command | Description |
|---|---|
| `coldship webhook enable <app> [--rotate]` | Enable the app's Git webhook and print its URL and secret, shown once (admin) |
| `coldship webhook status <app>` | Webhook settings and recent deliveries |
| `coldship webhook disable <app>` | Turn the webhook off (admin) |

## Databases

| Command | Description |
|---|---|
| `coldship db list <app>` | Databases with status, injected env var and latest snapshot |
| `coldship db snapshot <app> <database> [--reason "..."]` | Snapshot now |
| `coldship db snapshots <app> [--database D]` | Snapshots, newest first |
| `coldship db restore <app> <database> <snapshot> --reason "..."` | Plan and apply a restore. Stops with exit 3 if approval is needed. |

## Previews

| Command | Description |
|---|---|
| `coldship preview create <app> [--ref R \| --image I] [--name N] [--ttl 2h] [--env K=V] [--inherit-env \| --no-inherit-env] [--reason "..."]` | Create or update a preview. Safe to repeat. |
| `coldship preview list` | Previews, soonest-expiring first |
| `coldship preview delete <preview> [--reason "..."]` | Delete a preview and its volumes |

## Upgrades

| Command | Description |
|---|---|
| `coldship upgrade` | Upgrade to the latest release: the server at `COLDSHIP_URL` if one is running (admin token; waits for its restart), then this CLI binary (with sudo if needed). `--check` only checks; `--no-wait` doesn't wait for the server; `--cli-only` skips the server. |
| `coldship uninstall [--keep-data] [--yes]` | Remove Coldship from this machine: the service, every app, database and proxy container, their volumes and Coldship's state (unless `--keep-data`), images built from Git, the network, your CLI login, and the binary. Lists everything and asks you to type `uninstall` first. Asks for sudo if needed. Leaves Docker itself, and volumes Coldship didn't create, alone. |
| `coldship release manifest --version X os/arch=path ...` | Write the release manifest, `latest.json` (maintainers; `make release` runs it) |

After other commands, the CLI prints a one-line note on stderr when a new version is available or the CLI is older than the server. It checks at most once an hour, and never prints with `--json`, when stderr isn't a terminal, or with `COLDSHIP_NO_UPDATE_NOTICE=1`.

## Audit

| Command | Description |
|---|---|
| `coldship audit [--limit 50]` | Audit log |

## Tokens

| Command | Description |
|---|---|
| `coldship token create --name <name> [--scope deploy]` | Create a token (admin). Scopes: `read`, `deploy`, `admin`. The token is shown once. |
| `coldship token list` | Token names, scopes and creation times, never their values (admin) |
| `coldship token revoke <name> [--reason "..."]` | Revoke a token and sign out its dashboard sessions (admin). The last admin token can't be revoked. |
