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
| `unauthenticated` | 401 | Missing or unknown token, or an expired session |
| `csrf_check_failed` | 403 | Cookie-authenticated request that changes state, without the `X-Hangar-Request` header |
| `insufficient_scope` | 403 | The token's scope is too low for this action |
| `approval_required` | 403 | Destructive production change. A human must run `hangar approve <plan>`. CLI exit code 3. |
| `app_not_found` / `plan_not_found` / `deployment_not_found` | 404 | Check the name or id |
| `route_not_found` | 404 | Unknown API path |
| `plan_stale` | 409 | The app changed since planning. Plan again. CLI exit code 4. |
| `plan_expired` | 409 | Plans live 30 minutes. Plan again. CLI exit code 4. |
| `plan_superseded` | 409 | A newer push replaced this webhook plan; the message names the new one. CLI exit code 4. |
| `no_rollback_target` | 409 | There is no earlier successful deployment |
| `token_exists` | 409 | A token with that name exists |
| `image_pull_failed` | 422 | Check the image name, tag and registry access. The running app is untouched. |
| `git_unreachable` | 422 | The repository can't be read. Check the URL and network. |
| `git_auth_failed` | 422 | The repository needs credentials: `--git-credentials` or an SSH key |
| `git_ref_not_found` | 422 | At plan time. `details.available_refs` lists real branches and tags. |
| `git_commit_not_found` | 422 | The pinned commit is gone (force-pushed?). Plan again. |
| `git_clone_failed` | 500 | Setting up the checkout failed |
| `dockerfile_not_found` | 422 | `builder: dockerfile` (or a named Dockerfile) is set, but the file doesn't exist. The suggested fix names one found elsewhere in the repository. |
| `build_detect_failed` | 422 | No Dockerfile, and Hangar couldn't work out how to build or start the project. The suggested fix shows the `build.auto` fields to set. |
| `build_failed` | 422 | `docker build` failed. Read the build log. The running app is untouched (`failed_in: build`). |
| `build_timeout` | 504 | The build exceeded the server's `--build-timeout` |
| `health_check_failed` | 502 | The new container never became healthy. With zero-downtime deploys the previous version keeps serving (`details.previous_version_running`). |
| `database_version_change` | 409 | Changing a database's engine or major version needs a migration Hangar doesn't do yet |
| `database_start_failed`, `database_not_ready` | 502 | A database couldn't start or accept connections. The running app is untouched; see the deploy log. |
| `snapshot_failed` | 502 | A snapshot failed. Nothing was deleted. |
| `snapshot_not_found`, `snapshot_missing`, `snapshot_corrupt` | 404/500 | The snapshot doesn't exist, its file is gone, or it doesn't match its checksum. Nothing is restored. |
| `restore_failed` | 502 | The restore itself failed. The data from before is saved as a snapshot. |
| `bad_signature` | 401 | A webhook request's signature or token doesn't match the app's secret |
| `webhook_not_enabled` | 404 | No webhook is enabled for the app |
| `webhook_exists` | 409 | The webhook is already enabled; pass `rotate` to replace the secret |
| `payload_too_large` | 413 | Webhook payloads are limited to 5 MB |
| `preview_limit` | 409 | All preview slots are used. `details.previews` lists them, soonest-expiring first. |
| `not_a_preview` | 400 | Preview commands only delete previews; delete regular apps with a plan |
| `name_in_use` | 409 | A regular app already has the preview's name; pass another `--name` |
| `domain_in_use` | 409 | Another app already has this domain (`details.app`) |
| `proxy_disabled` | 400 | The app has domains but the server runs with `--proxy=false` |
| `proxy_unavailable`, `proxy_reload_failed` | 502 | The proxy container couldn't start or reload its config |
| `updates_disabled`, `no_update_available`, `upgrade_in_progress` | 409 | From `/v1/system/upgrade` |
| `update_check_failed` | 502 | The release manifest couldn't be fetched, or its signature is invalid |
| `container_exited` | 502 | The new container didn't stay up. Call `diagnose`, then fix or roll back. |
| `container_start_failed` | 502 | Docker refused to start the container |
| `host_port_in_use` | 502 | Another process uses `host_port`. Pick another. |
| `container_remove_failed`, `volume_remove_failed` | 502 | Docker failed to remove a resource |
| `deploy_failed` | 502 | Another runtime failure during apply |
| `runtime_unavailable`, `logs_unavailable` | 502 | Hangar couldn't talk to Docker |
| `server_unreachable` | 503 | (CLI and MCP) The server isn't running at `HANGAR_URL` |
| `secrets_unreadable` | 500 | A stored secret doesn't decrypt with the configured key (wrong key, corrupted, or copied from another row) |
| `key_from_environment` | 409 | Keys from `HANGAR_SECRET_KEY` can't be rotated in place; see [rotation](/guides/secrets/#status-and-rotation) |
| `internal` | 500 | A bug. Details are in the server log. |
