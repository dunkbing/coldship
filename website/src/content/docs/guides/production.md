---
title: Running in production
description: What to do before exposing a Coldship server beyond localhost.
---

Coldship is an early project. Before relying on it, go through this list.

## Network exposure

The server binds to `127.0.0.1:7070` by default and speaks plain HTTP. To reach it from elsewhere, put it behind a reverse proxy that terminates TLS (Caddy, nginx or Traefik), or reach it over a private network such as WireGuard or Tailscale. Don't expose port 7070 directly.

Have the proxy send `X-Forwarded-Proto: https`, so the dashboard's session cookie gets the `Secure` flag.

For [Git webhooks](/guides/webhooks/#exposing-the-endpoint), expose only the `/hooks/git/` path publicly, never the whole API.

## Tokens

- Store the bootstrap admin token in a password manager. It is shown only once.
- Give every agent and CI job its own `deploy` token, so the audit log tells them apart.
- Never give an agent an `admin` token. That would let it approve its own destructive plans.

## State and backups

All platform state (apps, plans, deployments, tokens, audit log) is in one SQLite file, set with `--data` (default `./coldship.db`). Back it up with SQLite's online backup, which is safe while the server runs:

```sh
sqlite3 coldship.db ".backup 'coldship-backup.db'"
```

App data lives in Docker volumes named `coldship-<app>-<volume>`. Back those up separately. [Managed databases](/guides/databases/) are snapshotted daily into `--snapshot-dir`; copy that directory off the server too, since a disk failure takes local snapshots with it.

## Git builds

- Private repositories: use `--git-credentials` (HTTPS) or an SSH key for the server user. See [Build from Git](/guides/build-from-git/#private-repositories).
- Builds use disk space for checkouts (`--build-dir`, default the system temp directory) and images. Prune old `coldship-build/*` images now and then; ones you might roll back to are rebuilt automatically if missing.
- Only build repositories you trust. A Dockerfile runs code on your build host.

## Secrets

Stored secrets are [encrypted](/guides/secrets/) with a key that is created on first start as `coldship.key` next to the database. For production:

- Keep the key off the server's data disk with `--secret-key-file`, or provide it with `COLDSHIP_SECRET_KEY` from a secret manager.
- Back up the key, and back it up separately from `coldship.db`. Without it, the stored secrets can't be recovered.
- Delete backups of `coldship.db` taken before upgrading to encryption: they contain plaintext.

## Running as a service

The installer sets Coldship up as a systemd service:

```sh
curl -fsSL https://raw.githubusercontent.com/dunkbing/coldship/main/install.sh | sh -s -- --service
```

It keeps state in `/var/lib/coldship`, starts on boot, and prints the admin token once. Running it again updates the binary and keeps your data and token. Manage it with `sudo systemctl status|restart|stop coldship` and read its log with `sudo journalctl -u coldship`.

The service runs as **root**. It needs Docker, and access to Docker is equivalent to root on the host anyway, so a separate user in the `docker` group adds no real isolation. Running as root also lets self-upgrades replace the binary. Your own user only talks to the server over HTTP with a token, so it doesn't need Docker access.

The installed unit, if you'd rather write it yourself:

```ini title="/etc/systemd/system/coldship.service"
[Unit]
Description=Coldship
After=network-online.target docker.service
Wants=network-online.target docker.service

[Service]
ExecStart=/usr/local/bin/coldship server --data /var/lib/coldship/coldship.db --admin-token-file /var/lib/coldship/admin-token
WorkingDirectory=/var/lib/coldship
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

`--admin-token-file` writes the first admin token to that file (mode 0600) instead of the log. Read it once, store it, and delete the file.
