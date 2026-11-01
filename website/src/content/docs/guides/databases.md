---
title: Managed databases
description: Postgres and Redis declared in the app's spec, with credentials handled for you and snapshots before anything destructive.
---

Declare the databases an app needs in its spec:

```yaml title="coldship.yaml"
name: shop
image: ghcr.io/acme/shop:1.4.2
port: 8080
databases:
  - name: main
    engine: postgres        # postgres 14–17, or redis 6–7
    version: "17"           # major version (default: newest); minor updates come with the image
  - name: cache
    engine: redis           # injected as REDIS_URL
```

On apply, Coldship starts each database and waits until it accepts connections, then deploys the app with the connection URL in its env:

| Engine | Injected as (default) | URL |
|---|---|---|
| `postgres` | `DATABASE_URL` | `postgres://app:<password>@shop-db-main:5432/app?sslmode=disable` |
| `redis` | `REDIS_URL` | `redis://default:<password>@shop-db-cache:6379/0` |

Set `env:` on a database to use another variable name. Two databases of the same engine need different names; validation suggests one, e.g. `REPORTS_URL`.

## Credentials stay on the server

Coldship generates a random password for each database when it's created and stores it on the server. The password only ever goes to the database container and into the app's env. It isn't in the spec, plans, API responses, the CLI, MCP results, deploy logs or the dashboard. Credentials stay the same across deploys.

Databases run on Coldship's internal Docker network, with no published ports. Only apps on that network can reach them.

## Snapshots

Coldship snapshots databases automatically:

- **before anything destructive:** removing a database from the spec, deleting the app, or restoring a snapshot (which saves the current data first)
- **daily** (`--db-snapshot-interval`, default 24h; `0` turns it off)
- **on demand:** `coldship db snapshot shop main --reason "before migration"`, the `coldship_db_snapshot` MCP tool, or **Snapshot now** in the dashboard

Postgres snapshots are `pg_dump` custom-format archives; Redis snapshots are RDB files. Each is stored with a SHA-256 checksum under `--snapshot-dir` (default `coldship-snapshots/` next to the state file). Coldship keeps the newest 20 per database (`--snapshot-keep`). Previews are throwaway, so they're not snapshotted.

**If a snapshot fails, nothing is deleted.** Deleting an app stops with `snapshot_failed` and leaves everything running. A database removed from the spec is kept while the rest of the deploy goes ahead, and the failure is logged and audited.

```sh
coldship db list shop
coldship db snapshots shop [--database main]
```

## Restoring

A restore is a destructive plan, like any other:

```sh
coldship db restore shop main snap_6797f97b295e --reason "undo the bad migration"
```

```txt
Plan plan_95a5c8970ea4 for app "shop" (restore)
  ⟲ databases.main: snap_6797f97b295e  [DESTRUCTIVE: replaces all data in main with snapshot snap_6797f97b295e from 2026-11-01T07:47:44Z (the current data is snapshotted first)]
```

- For **production** apps the plan needs a human to approve it, just like other destructive changes. The command stops with exit code 3 and prints the approve and apply commands.
- Before restoring, Coldship checks the snapshot file against its checksum, and refuses a file that has changed (`snapshot_corrupt`).
- The current data is snapshotted first. The result names that snapshot, so a restore can itself be undone.

Agents restore through the normal tools: `coldship_plan` with `restore: {app, database, snapshot}`, then `coldship_apply`.

## Changing and removing databases

- **Removing** a database from the spec is destructive: the plan says so, production needs approval, and the database is snapshotted, then deleted with its volume after the new app version is live.
- **Changing the engine or major version** of an existing database is refused (`database_version_change`) for now, because it needs a data migration. To move data, add a new database and restore a snapshot into it.

## Previews

A [preview](/guides/previews/) of an app with databases gets its **own, empty** databases. It never connects to the parent's data, and its databases are deleted along with it.

## Diagnose

If a database container isn't running, `coldship diagnose` reports `database_down`, since the app can't reach it.
