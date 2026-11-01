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
| `POST` | `/v1/sessions` | none | Body `{token}`, with the `X-Coldship-Request: 1` header. Sets the `coldship_session` cookie and returns `{name, scope}`. |
| `DELETE` | `/v1/sessions` | session | Sign out: deletes the session and clears the cookie |
| `GET` | `/v1/whoami` | read | `{name, scope}` of the calling token |
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
| `POST` | `/v1/tokens` | admin | Body `{name, scope}`. Returns `{name, scope, token}`. |
| `GET` | `/v1/tokens` | admin | Token names, scopes and creation times; never their values |
| `DELETE` | `/v1/tokens/{name}?reason=...` | admin | Revoke a token and end its dashboard sessions |

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
