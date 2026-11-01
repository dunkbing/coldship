---
title: Running in production
description: What to do before exposing a Hangar server beyond localhost.
---

Hangar is an early project. Before relying on it, go through this list.

## Network exposure

The server binds to `127.0.0.1:7070` by default and speaks plain HTTP. To reach it from elsewhere, put it behind a reverse proxy that terminates TLS (Caddy, nginx or Traefik), or reach it over a private network such as WireGuard or Tailscale. Don't expose port 7070 directly.

Have the proxy send `X-Forwarded-Proto: https`, so the dashboard's session cookie gets the `Secure` flag.

For [Git webhooks](/guides/webhooks/#exposing-the-endpoint), expose only the `/hooks/git/` path publicly, never the whole API.

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

Stored secrets are [encrypted](/guides/secrets/) with a key that is created on first start as `hangar.key` next to the database. For production:

- Keep the key off the server's data disk with `--secret-key-file`, or provide it with `HANGAR_SECRET_KEY` from a secret manager.
- Back up the key, and back it up separately from `hangar.db`. Without it, the stored secrets can't be recovered.
- Delete backups of `hangar.db` taken before upgrading to encryption: they contain plaintext.

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
