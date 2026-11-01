---
title: Running in production
description: What to do before exposing a Hangar server beyond localhost.
---

Hangar is an early project. Before relying on it, go through this list.

## Network exposure

The server binds to `127.0.0.1:7070` by default and speaks plain HTTP. To reach it from elsewhere, put it behind a reverse proxy that terminates TLS (Caddy, nginx or Traefik), or reach it over a private network such as WireGuard or Tailscale. Don't expose port 7070 directly.

Have the proxy send `X-Forwarded-Proto: https`, so the dashboard's session cookie gets the `Secure` flag.

## Tokens

- Store the bootstrap admin token in a password manager. It is shown only once.
- Give every agent and CI job its own `deploy` token, so the audit log tells them apart.
- Never give an agent an `admin` token. That would let it approve its own destructive plans.

## State and backups

All platform state (apps, plans, deployments, tokens, audit log) is in one SQLite file, set with `--data` (default `./hangar.db`). Back it up with SQLite's online backup, which is safe while the server runs:

```sh
sqlite3 hangar.db ".backup 'hangar-backup.db'"
```

App data lives in Docker volumes named `hangar-<app>-<volume>`. Back those up separately. [Managed databases](/guides/databases/) are snapshotted daily into `--snapshot-dir`; copy that directory off the server too, since a disk failure takes local snapshots with it.

## Git builds

- Private repositories: use `--git-credentials` (HTTPS) or an SSH key for the server user. See [Build from Git](/guides/build-from-git/#private-repositories).
- Builds use disk space for checkouts (`--build-dir`, default the system temp directory) and images. Prune old `hangar-build/*` images now and then; ones you might roll back to are rebuilt automatically if missing.
- Only build repositories you trust. A Dockerfile runs code on your build host.

## Secrets

Env values are redacted in every output, but they are stored **unencrypted** in the SQLite file. Protect the file accordingly (`chmod 600`, encrypted disk). Encryption at rest is on the [roadmap](/project/roadmap/).

## Running as a service

```ini title="/etc/systemd/system/hangar.service"
[Unit]
Description=Hangar
After=docker.service
Requires=docker.service

[Service]
ExecStart=/usr/local/bin/hangar server --addr 127.0.0.1:7070 --data /var/lib/hangar/hangar.db
Restart=on-failure
User=hangar
Group=docker

[Install]
WantedBy=multi-user.target
```
