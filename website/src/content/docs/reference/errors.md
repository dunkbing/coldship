---
title: Errors
description: The structured error format and every error code.
---

Every error from every interface has the same shape:

```json
{
  "error": {
    "code": "plan_stale",
    "message": "app web changed since plan plan_5f0d0e29eadf was made",
    "field": "optional: the spec field at fault",
    "hint": "create a new plan; the app was deployed or deleted by someone else in the meantime",
    "suggested_fix": "optional: a concrete value or action",
    "details": { "optional": "ids such as plan and deployment" }
  }
}
```

`code` is stable and safe to branch on. `message` and `hint` are written for people and LLMs to read, and may change.

## Codes

| Code | HTTP | Meaning and what to do |
|---|---|---|
| `invalid_spec` | 400 | The spec failed parsing or validation. See `field` and `suggested_fix`. |
| `spec_unreadable` | 400 | The CLI couldn't read the spec file |
| `invalid_request` | 400 | Malformed body or query parameter |
| `reason_required` | 400 | Apply and rollback need a non-empty `reason` |
| `invalid_scope` | 400 | Token scope must be `read`, `deploy` or `admin` |
| `invalid_token_name` | 400 | Token names are lowercase letters, digits and dashes |
| `invalid_rollback_target` | 400 | `to_deployment` isn't a successful deployment of this app |
| `unauthenticated` | 401 | Missing or unknown token |
| `insufficient_scope` | 403 | The token's scope is too low for this action |
| `approval_required` | 403 | Destructive production change. A human must run `hangar approve <plan>`. CLI exit code 3. |
| `app_not_found` / `plan_not_found` | 404 | Check the name or id |
| `route_not_found` | 404 | Unknown API path |
| `plan_stale` | 409 | The app changed since planning. Plan again. CLI exit code 4. |
| `plan_expired` | 409 | Plans live 30 minutes. Plan again. CLI exit code 4. |
| `no_rollback_target` | 409 | There is no earlier successful deployment |
| `token_exists` | 409 | A token with that name exists |
| `image_pull_failed` | 422 | Check the image name, tag and registry access. The running app is untouched. |
| `container_exited` | 502 | The new container didn't stay up. Call `diagnose`, then fix or roll back. |
| `container_start_failed` | 502 | Docker refused to start the container |
| `host_port_in_use` | 502 | Another process uses `host_port`. Pick another. |
| `container_remove_failed`, `volume_remove_failed` | 502 | Docker failed to remove a resource |
| `deploy_failed` | 502 | Another runtime failure during apply |
| `runtime_unavailable`, `logs_unavailable` | 502 | Hangar couldn't talk to Docker |
| `server_unreachable` | 503 | (CLI and MCP) The server isn't running at `HANGAR_URL` |
| `internal` | 500 | A bug. Details are in the server log. |
