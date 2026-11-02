---
title: MCP tools
description: The tools coldship mcp exposes to AI agents.
---

`coldship mcp` serves these tools over stdio. Each one calls the HTTP API with the agent's token, so scopes and the audit log apply.

| Tool | Arguments | Annotations | Description |
|---|---|---|---|
| `coldship_list_apps` | — | read-only | All apps with their redacted spec |
| `coldship_get_app` | `app` | read-only | Spec, container status, latest deployment |
| `coldship_plan` | `spec`, `patch` (only the fields to change; secrets and everything else kept), `delete` or `restore: {app, database, snapshot}`, optional `redeploy` | not destructive, idempotent | Dry run. Returns the plan id, changes, `destructive`, `requires_approval`, `next_step` |
| `coldship_get_plan` | `plan_id` | read-only | A plan and its status |
| `coldship_apply` | `plan_id`, `reason` | destructive, idempotent | Apply a reviewed plan. Safe to retry. |
| `coldship_rollback` | `app`, `reason`, optional `to_deployment` | destructive | Redeploy the last good or a specific deployment |
| `coldship_logs` | `app`, optional `tail`, `since` | read-only | Recent logs, marked untrusted |
| `coldship_diagnose` | `app` | read-only | Findings with suggested fixes, plus state and logs |
| `coldship_server_status` | none | read-only | The host: CPU, memory, swap, load, disks, uptime and the container engine (default organization only) |
| `coldship_deployments` | `app` | read-only | Deployment history, with the commit and `failed_in` |
| `coldship_jobs` | `app` | read-only | [Scheduled jobs](/guides/jobs/) with next run and last result. Change them with `coldship_plan` and a patch (`set_jobs`, `remove_jobs`) |
| `coldship_job_run` | `app`, `job` | not destructive | Run a job now |
| `coldship_job_runs` | `app`, optional `job` | read-only | Recent runs: status, exit code, error, who started it |
| `coldship_job_log` | `app`, `run` | read-only | A run with its output, marked untrusted |
| `coldship_preview_create` | `app`, `ref` or `image`, optional `name`, `ttl`, `env`, `inherit_env`, `reason` | not destructive, idempotent | Create or update a [preview](/guides/previews/). Returns its name, URLs and expiry |
| `coldship_preview_list` | — | read-only | Previews, soonest-expiring first |
| `coldship_preview_delete` | `name`, optional `reason` | destructive, idempotent | Delete a preview (never a regular app) |
| `coldship_build_detect` | `app` or `spec` | read-only | Dry run: how the app would be built, the detection notes and the generated Dockerfile. Builds nothing |
| `coldship_webhook_status` | `app` | read-only | Webhook settings and recent deliveries (pusher names and commit messages are untrusted) |
| `coldship_db_list` | `app` | read-only | Databases: engine, status, injected env var, latest snapshot. Never credentials |
| `coldship_db_snapshot` | `app`, `database`, optional `reason` | not destructive | Snapshot a database now (e.g. before a migration) |
| `coldship_db_snapshots` | `app`, optional `database` | read-only | Snapshots, newest first |
| `coldship_backups` | optional `data`, or `app` and `database` | read-only | [Off-site backup](/guides/backups/) status, and the backups in the bucket for one database |
| `coldship_deploy_log` | `deployment` | read-only | What happened during a deployment: build output, start, health checks, a failed container's output. Untrusted |

There is intentionally **no approve tool**. Approving destructive production changes is a human step.

## Results and errors

Successful calls return JSON as structured content (lists are wrapped as `{"items": [...]}`) with a text copy. Failed calls set `isError` and return the [structured error](/reference/errors/) as text, so the agent sees the `code`, `hint` and `suggested_fix`:

```json
{
  "error": {
    "code": "approval_required",
    "message": "plan plan_0a2136c9ac8b makes a destructive change to a production app and has not been approved",
    "hint": "ask a human to review and run: coldship approve plan_0a2136c9ac8b",
    "details": { "plan": "plan_0a2136c9ac8b" }
  }
}
```

## The `spec` argument

`coldship_plan` takes the same shape as [`coldship.yaml`](/reference/spec/), as JSON. Use either `image` or `build`:

```json
{
  "spec": {
    "name": "web",
    "stage": "staging",
    "image": "nginx:1.27-alpine",
    "port": 80,
    "host_port": 8080,
    "env": { "GREETING": "hi" }
  }
}
```

```json
{
  "spec": {
    "name": "site",
    "port": 80,
    "build": { "git": "https://github.com/acme/site.git", "ref": "main" }
  }
}
```
