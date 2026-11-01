---
title: App spec (hangar.yaml)
description: Every field of the declarative app spec.
---

An app is described by one YAML file, usually `hangar.yaml`. Parsing is strict: an unknown field (such as a typo like `imgae`) is an error, never silently ignored.

```yaml
name: api
stage: production
image: ghcr.io/acme/api:1.4.2
port: 8080
host_port: 8080
memory: 512m
env:
  DATABASE_URL: postgres://api@db/api
  LOG_LEVEL: info
volumes:
  - name: uploads
    path: /srv/uploads
```

## Fields

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | string | yes | 1–40 characters: lowercase letters, digits and dashes, starting with a letter. The app's identity; changing it creates a different app. |
| `stage` | string | no | `production`, `staging` (default) or `preview`. Destructive changes to `production` apps need [approval](/concepts/guardrails/). |
| `image` | string | yes | Container image reference. It is pulled if not present locally. |
| `port` | int | no | Port the app listens on inside the container. Used to publish `host_port` and to detect port mismatches. |
| `host_port` | int | no | Publish `port` on this host port. Requires `port`. |
| `memory` | string | no | Memory limit, e.g. `256m` or `1g`. Unlimited if unset. |
| `env` | map | no | Environment variables. Keys match `[A-Za-z_][A-Za-z0-9_]*`; values are single-line. Values are write-only and redacted in all output. |
| `volumes` | list | no | Persistent Docker volumes, each `{name, path}`. The name follows the same rules as app names, and the path must be absolute. Removing a volume from the spec **deletes it and its data**. |

## Validation errors

Validation errors name the field and, where they can, suggest a fix:

```json
{
  "error": {
    "code": "invalid_spec",
    "message": "host_port is set but port is not; Hangar needs to know which container port to publish",
    "field": "port",
    "suggested_fix": { "port": 8080 }
  }
}
```

## On the host

| Thing | Name |
|---|---|
| Container | `hangar-<name>` |
| Volume | `hangar-<name>-<volume>` |
| Labels | `hangar.app=<name>`, `hangar.deployment=<id>` |

Containers run with `--restart unless-stopped`.
