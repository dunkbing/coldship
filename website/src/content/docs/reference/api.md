---
title: HTTP API
description: Endpoints, authentication and request shapes for the Coldship HTTP API.
---

All endpoints are under `/v1`, take and return JSON, and need `Authorization: Bearer <token>`. Errors use the [structured error format](/reference/errors/) with a matching HTTP status.

The dashboard authenticates with a session cookie instead. It gets one from `POST /v1/sessions`, and must send `X-Coldship-Request: 1` on requests that change state. See [Dashboard sessions](/concepts/guardrails/#dashboard-sessions).

## Endpoints

| Method | Path | Scope | Description |
|---|---|---|---|
| `GET` | `/healthz` | none | Liveness: returns `ok` |
| `POST` | `/v1/sessions` | none | Body `{token}` or `{email, password}`, with the `X-Coldship-Request: 1` header. Sets the `coldship_session` cookie and returns the same as `whoami`. Five wrong passwords in 15 minutes block an email or address for the rest of the window. |
| `DELETE` | `/v1/sessions` | session | Sign out: deletes the session and clears the cookie |
| `GET` | `/v1/whoami` | read | `{name, scope, apps, org, user, orgs, server_admin}`: the caller, the organization it acts in and, for a signed-in person, their account and organizations |
| `GET` | `/v1/apps` | read | List apps |
| `GET` | `/v1/apps/{name}` | read | App with `runtime` status and `latest_deployment` |
| `GET` | `/v1/apps/{name}/deployments?limit=20` | read | Deployments, newest first (max 200) |
| `GET` | `/v1/apps/{name}/logs?tail=100&since=10m` | read | `{lines, untrusted: true, note}` (tail max 5000) |
| `GET` | `/v1/apps/{name}/diagnose` | read | Findings, runtime status, recent logs |
| `POST` | `/v1/apps/{name}/rollback` | deploy | Body `{reason, to_deployment?}` |
| `GET` | `/v1/deployments/{id}/log` | read | Deploy log: `{lines, commit, untrusted: true, note}` |
| `GET` | `/v1/system` | read | Version and update status: `{version, latest, update_available, auto_upgrade, upgrade_state, ...}` |
| `GET` | `/v1/system/secrets` | admin | Encryption status: key id, source, counts. Never key material |
| `POST` | `/v1/system/secrets/rotate` | admin | Rotate the secret key |
| `POST` | `/v1/system/check` | admin | Check for a new version now |
| `POST` | `/v1/system/upgrade` | admin | Start an upgrade: waits for deployments, installs, restarts |
| `POST` | `/hooks/git/{app}` | webhook signature | Git push webhooks. Authenticated only by the app's secret; see [Git webhooks](/guides/webhooks/) |
| `GET` | `/v1/apps/{name}/webhook` | read | Webhook URL, settings and recent deliveries |
| `POST` | `/v1/apps/{name}/webhook` | admin | Enable, or with `{rotate: true}` replace, the secret; returns it once |
| `DELETE` | `/v1/apps/{name}/webhook` | admin | Disable the webhook |
| `GET` | `/v1/apps/{name}/databases` | read | Databases with status and latest snapshot |
| `GET` | `/v1/apps/{name}/snapshots?database=` | read | Snapshots, newest first |
| `POST` | `/v1/apps/{name}/databases/{db}/snapshots` | deploy | Snapshot now: `{reason?}` |
| `GET` | `/v1/apps/{name}/jobs` | read | The app's [jobs](/guides/jobs/), each with `next`, `running` (a run id) and `last` |
| `POST` | `/v1/apps/{name}/jobs/{job}/run` | deploy | Run a job now. Returns the run, `status: running` |
| `GET` | `/v1/apps/{name}/job-runs?job=` | read | Recent runs, newest first, without output |
| `GET` | `/v1/apps/{name}/job-runs/{id}` | read | One run with its output (`log`, the last 64 KB; untrusted) |
| `POST` | `/v1/apps/{name}/databases/{db}/import` | admin | Copy an off-site backup in as a snapshot, to restore with a plan: `{backup}` |
| `POST` | `/v1/data/{name}/restore` | admin | Restore a data service: `{snapshot, reason}` or `{backup, reason}` (an off-site backup's key) |
| `GET` | `/v1/backups` | read | The org's [off-site backup](/guides/backups/) bucket (never its secret key), last sync and error, copied and waiting snapshots |
| `PUT` | `/v1/backups` | admin | Set the bucket: `{endpoint, bucket, access_key, secret_key, region?, prefix?, path_style?, keep?}`. Checked before saving; an empty `secret_key` keeps the saved one |
| `POST` | `/v1/backups/test` | admin | Check a bucket without saving it (same body) |
| `POST` | `/v1/backups/sync` | admin | Copy snapshots now |
| `GET` | `/v1/backups/list?database=&app=` | read | Backups in the bucket, newest first: all, a data service's (`database`), or an app database's (`app` and `database`) |
| `DELETE` | `/v1/backups` | admin | Stop copying; the bucket's contents stay |
| `GET` | `/v1/channels` | read | [Notification](/guides/notifications/) channels: kind, where they go (never URLs or tokens), events, last delivery |
| `GET` | `/v1/channels/events` | read | The events channels can get |
| `POST` | `/v1/channels` | admin | Add a channel: `{kind, name?, url?, bot_token?, chat_id?, events?}`. A webhook's signing `secret` is returned once |
| `PATCH` | `/v1/channels/{id}` | admin | Change its name, events or target (empty fields keep theirs) |
| `DELETE` | `/v1/channels/{id}` | admin | Remove it |
| `POST` | `/v1/channels/{id}/test` | admin | Send a test message; fails with `channel_unreachable` if it isn't delivered |
| `POST` | `/v1/build/detect` | deploy | `{app}` or `{spec}`: check out the commit and report how it would be built, without building |
| `POST` | `/v1/previews` | deploy | Create or update a preview: `{app, ref?, image?, name?, ttl?, env?, inherit_env?, reason?}` |
| `GET` | `/v1/previews` | read | List previews |
| `DELETE` | `/v1/previews/{name}?reason=...` | deploy | Delete a preview |
| `POST` | `/v1/plans` | deploy | Body `{spec}`, `{patch}`, `{delete: "<app>"}` or `{restore: {app, database, snapshot}}`, optionally `redeploy: true`. A `patch` (`{name, image?, build?, port?, env?, unset_env?, add_domains?, remove_domains?, add_databases?, add_volumes?, stage?, ...}`) changes only what it names and creates the app if needed. In a `spec`, an env value of `<redacted>` keeps the current value. |
| `GET` | `/v1/plans?status=pending&limit=50` | read | List plans. `status` is `pending`, `applied` or `expired` |
| `GET` | `/v1/plans/{id}` | read | One plan |
| `POST` | `/v1/plans/{id}/apply` | deploy | Body `{reason}`. Idempotent. |
| `POST` | `/v1/plans/{id}/approve` | admin | Approve a plan |
| `GET` | `/v1/audit?limit=50` | read | Audit entries, newest first (max 1000) |
| `POST` | `/v1/tokens` | admin | Body `{name, scope, apps?}`. `apps` limits the token to those apps (names or `*` patterns) and their previews. Returns `{name, scope, apps, token}`. |
| `GET` | `/v1/tokens` | admin | Token names, scopes and creation times; never their values |
| `DELETE` | `/v1/tokens/{name}?reason=...` | admin | Revoke a token and end its dashboard sessions |
| `GET` | `/v1/org` | read | The caller's organization, with counts of apps, data services, members and tokens |
| `PATCH` | `/v1/org` | admin | Rename: `{name}` |
| `DELETE` | `/v1/org` | admin (person) | Delete an organization with no apps or data services |
| `GET` | `/v1/orgs` | read | Your organizations, with your role in each |
| `POST` | `/v1/orgs` | default-org admin (person) | Create an organization: `{name}`. You become its admin and switch to it |
| `POST` | `/v1/session/org` | person | Switch your session to another organization: `{org}` |
| `GET` | `/v1/org/members` | read | Members and their roles |
| `PATCH` | `/v1/org/members/{id}` | admin | Change a role: `{role}` |
| `DELETE` | `/v1/org/members/{id}` | admin, or yourself | Remove a member, or leave |
| `POST` | `/v1/org/members/{id}/reset` | default-org admin | A single-use password reset link (24 hours) |
| `GET` / `POST` | `/v1/org/invites` | admin | List pending invites; create one with `{role}`, which returns its `url` once |
| `DELETE` | `/v1/org/invites/{id}` | admin | Revoke an invite |
| `POST` | `/v1/links/lookup`, `/v1/links/accept` | none | Read and use an invite or reset link: `{secret, email?, name?, password?}` |
| `POST` | `/v1/account` | admin token | Create a person's account (an admin) from a token session |
| `PATCH` | `/v1/account`, `POST` `/v1/account/password` | person | Change your name; change your password (`{current, new}`) |
| `POST` | `/v1/apps/{name}/move` | admin of both | Move an app to another organization: `{org}` |
| `GET` | `/v1/agents` | read | `{presets, agents}`: the agents you can connect, and the org's connected ones (key names only, never values) |
| `PUT` | `/v1/agents/{name}` | admin | Connect or change an agent: `{kind, command?, env?, scope?, apps?, triggers?, alert_fix?}`. `kind` is `claude`, `codex`, `gemini` or `custom`; an empty `env` value keeps the saved one; `scope` is `read` or `deploy`; `triggers` are the [alerts](/guides/agents-in-dashboard/#responding-to-alerts) that start it (`app.down`, `deploy.failed`, `job.failed`, `backup.failing`, `disk.low`), and `alert_fix` gives those sessions its role instead of viewer. Left out, both keep their saved values |
| `DELETE` | `/v1/agents/{name}` | admin | Remove an agent and end its sessions |
| `POST` | `/v1/agents/{name}/sessions` | deploy | Start a session: `{prompt?}`; it becomes `ready` in the background, then gets `prompt` as its first message |
| `GET` | `/v1/agent-sessions`, `/v1/agent-sessions/{id}` | read | The org's last 100 sessions (with `title` and `resumable`), and one with its transcript, `modes`, `config_options`, `commands` and `usage` |
| `GET` | `/v1/agent-sessions/{id}/stream` | read | Server-sent events, as JSON: a `snapshot`, then `item`, `status` and `session` (settings, title, usage) changes |
| `POST` | `/v1/agent-sessions/{id}/settings` | deploy | Change a setting the agent offers: `{mode}`, or `{option, value}` (like `{"option": "model", "value": "..."}`) |
| `POST` | `/v1/agent-sessions/{id}/resume` | deploy | Start an ended session again; the agent reloads its conversation if it supports that |
| `DELETE` | `/v1/agent-sessions/{id}/transcript` | deploy | Delete an ended session's transcript |
| `POST` | `/v1/agent-sessions/{id}/prompt` | deploy | Send a message: `{text}` |
| `POST` | `/v1/agent-sessions/{id}/permissions/{perm}` | deploy | Answer the agent's request: `{option}` |
| `POST` | `/v1/agent-sessions/{id}/cancel`, `DELETE` `/v1/agent-sessions/{id}` | deploy | Stop the current turn; end the session |

Everything acts in the caller's organization: apps, data services, plans, deployments, tokens and audit entries of other organizations are reported as not found. Server-wide endpoints (`/v1/system/*` other than `GET /v1/system`) need an admin of the default organization.

Request bodies reject unknown fields and trailing data, are limited to 1 MiB, and must arrive within 30 seconds.

## Example

```sh
curl -s -H "Authorization: Bearer $COLDSHIP_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"spec":{"name":"web","image":"nginx:1.27-alpine","port":80,"host_port":8080}}' \
  http://127.0.0.1:7070/v1/plans
```

```json
{
  "id": "plan_692da65cccbb",
  "app": "web",
  "action": "create",
  "changes": [{ "path": "app", "op": "create", "to": "web" }],
  "destructive": false,
  "downtime": false,
  "requires_approval": false,
  "status": "pending",
  "created_by": "claude",
  "expires_at": "2026-11-01T06:39:18Z",
  "next_step": "Review the changes, then apply plan plan_692da65cccbb with a reason."
}
```

Any path outside `/v1/` and `/healthz` serves the [dashboard](/guides/dashboard/).

## Go client

`internal/client` is a typed Go client for this API, used by the CLI and the MCP server.
