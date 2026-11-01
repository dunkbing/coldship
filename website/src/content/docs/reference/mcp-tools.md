---
title: MCP tools
description: The tools hangar mcp exposes to AI agents.
---

`hangar mcp` serves these tools over stdio. Each one calls the HTTP API with the agent's token, so scopes and the audit log apply.

| Tool | Arguments | Annotations | Description |
|---|---|---|---|
| `hangar_list_apps` | — | read-only | All apps with their redacted spec |
| `hangar_get_app` | `app` | read-only | Spec, container status, latest deployment |
| `hangar_plan` | `spec` or `delete`, optional `redeploy` | not destructive, idempotent | Dry run. Returns the plan id, changes, `destructive`, `requires_approval`, `next_step` |
| `hangar_get_plan` | `plan_id` | read-only | A plan and its status |
| `hangar_apply` | `plan_id`, `reason` | destructive, idempotent | Apply a reviewed plan. Safe to retry. |
| `hangar_rollback` | `app`, `reason`, optional `to_deployment` | destructive | Redeploy the last good or a specific deployment |
| `hangar_logs` | `app`, optional `tail`, `since` | read-only | Recent logs, marked untrusted |
| `hangar_diagnose` | `app` | read-only | Findings with suggested fixes, plus state and logs |
| `hangar_deployments` | `app` | read-only | Deployment history, with the commit and `failed_in` |
| `hangar_build_log` | `deployment` | read-only | Build log of a Git deployment, marked untrusted |

There is intentionally **no approve tool**. Approving destructive production changes is a human step.

## Results and errors

Successful calls return JSON as structured content (lists are wrapped as `{"items": [...]}`) with a text copy. Failed calls set `isError` and return the [structured error](/reference/errors/) as text, so the agent sees the `code`, `hint` and `suggested_fix`:

```json
{
  "error": {
    "code": "approval_required",
    "message": "plan plan_0a2136c9ac8b makes a destructive change to a production app and has not been approved",
    "hint": "ask a human to review and run: hangar approve plan_0a2136c9ac8b",
    "details": { "plan": "plan_0a2136c9ac8b" }
  }
}
```

## The `spec` argument

`hangar_plan` takes the same shape as [`hangar.yaml`](/reference/spec/), as JSON. Use either `image` or `build`:

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
