---
title: HTTP API
description: Endpoints, authentication and request shapes for the Hangar HTTP API.
---

All endpoints are under `/v1`, take and return JSON, and need `Authorization: Bearer <token>`. Errors use the [structured error format](/reference/errors/) with a matching HTTP status.

The dashboard authenticates with a session cookie instead. It gets one from `POST /v1/sessions`, and must send `X-Hangar-Request: 1` on requests that change state. See [Dashboard sessions](/concepts/guardrails/#dashboard-sessions).

## Endpoints

| Method | Path | Scope | Description |
|---|---|---|---|
| `GET` | `/healthz` | none | Liveness: returns `ok` |
| `POST` | `/v1/sessions` | none | Body `{token}`. Sets the `hangar_session` cookie and returns `{name, scope}`. |
| `DELETE` | `/v1/sessions` | session | Sign out: deletes the session and clears the cookie |
| `GET` | `/v1/whoami` | read | `{name, scope}` of the calling token |
| `GET` | `/v1/apps` | read | List apps |
| `GET` | `/v1/apps/{name}` | read | App with `runtime` status and `latest_deployment` |
| `GET` | `/v1/apps/{name}/deployments?limit=20` | read | Deployments, newest first (max 200) |
| `GET` | `/v1/apps/{name}/logs?tail=100&since=10m` | read | `{lines, untrusted: true, note}` (tail max 5000) |
| `GET` | `/v1/apps/{name}/diagnose` | read | Findings, runtime status, recent logs |
| `POST` | `/v1/apps/{name}/rollback` | deploy | Body `{reason, to_deployment?}` |
| `GET` | `/v1/deployments/{id}/build-log` | read | `{lines, commit, untrusted: true, note}` |
| `POST` | `/v1/plans` | deploy | Body `{spec}`, `{delete: "<app>"}`, optionally `redeploy: true` |
| `GET` | `/v1/plans?status=pending&limit=50` | read | List plans. `status` is `pending`, `applied` or `expired` |
| `GET` | `/v1/plans/{id}` | read | One plan |
| `POST` | `/v1/plans/{id}/apply` | deploy | Body `{reason}`. Idempotent. |
| `POST` | `/v1/plans/{id}/approve` | admin | Approve a plan |
| `GET` | `/v1/audit?limit=50` | read | Audit entries, newest first (max 1000) |
| `POST` | `/v1/tokens` | admin | Body `{name, scope}`. Returns `{name, scope, token}`. |

Request bodies reject unknown fields and are limited to 1 MiB.

## Example

```sh
curl -s -H "Authorization: Bearer $HANGAR_TOKEN" \
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
