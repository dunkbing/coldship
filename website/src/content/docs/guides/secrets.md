---
title: Secrets encryption
description: How Hangar encrypts stored secrets, where the key lives, and how to back it up and rotate it.
---

Hangar encrypts every stored value that can contain a secret, with AES-256-GCM:

| Stored | Why it's secret |
|---|---|
| App specs, plus the plans and deployments that embed them | Env values (API keys, tokens) |
| Database credentials | Passwords of [managed databases](/guides/databases/) |
| Webhook secrets | They authenticate [Git pushes](/guides/webhooks/) |
| Deploy logs | A failing container's output can include secrets |

API tokens and dashboard sessions were never stored at all, only one-way hashes of them.

Each value is encrypted with a fresh random nonce and **bound to the row it belongs to**. Copying an encrypted value into another row (say, one app's webhook secret into another app) makes it fail to decrypt rather than leak. A value that doesn't decrypt is an error (`secrets_unreadable`); it is never treated as plaintext.

## The key

On first start, Hangar creates a key file, `hangar.key`, next to the state database, readable only by its owner (mode 0600), and logs a warning to back it up. Hangar refuses to start with a key file that other users can read, the way ssh does.

| Option | |
|---|---|
| `--secret-key-file /path/hangar.key` (or `HANGAR_SECRET_KEY_FILE`) | Keep the key somewhere other than next to the database |
| `HANGAR_SECRET_KEY` | A base64 32-byte key (`openssl rand -base64 32`), e.g. from a secret manager or systemd credentials. Then no key file is used. |

:::caution[Back up the key separately]
Without the key, the stored secrets can't be recovered: env values, database passwords and webhook secrets would have to be set again. Back the key up, **but not in the same place as the database**. A backup holding both offers no protection.
:::

**What the default location protects against.** With the key file next to `hangar.db`, encryption protects the database file on its own: a copied backup, a leaked snapshot of the file, a support bundle. It doesn't protect against someone who can read the whole server, which has the key as well. For that, keep the key on separate storage, or provide it through `HANGAR_SECRET_KEY` from a secret manager.

**Hangar fails closed.** If the key is missing or wrong, the server refuses to start and says so. It never creates a new key for a database whose secrets are already encrypted, since that would make them unreadable.

## Upgrading to encryption

The first start of a version with encryption encrypts all existing secrets in one transaction. Then it scrubs the old plaintext out of the SQLite files: SQLite would otherwise keep it in freed pages and in the write-ahead log. Freed pages are zeroed from then on. If the scrub fails (for example, the disk is full), the secrets are still encrypted, Hangar logs a warning, and it retries on the next start.

The scrub covers the SQLite files only. Copies elsewhere still hold the plaintext: backups taken before the upgrade, filesystem or VM snapshots, and old blocks on copy-on-write filesystems. Replace or delete them.

:::note[This upgrade changes the on-disk format]
Older versions can't read an encrypted database, so roll back only through [Downgrading](#downgrading). Release it as a new major version, so [automatic upgrades](/guides/upgrades/) don't install it unattended.
:::

## Status and rotation

```sh
hangar secrets status       # key id, key source, how many values are encrypted (admin)
hangar secrets rotate-key   # new key, re-encrypt everything, drop the old key (admin, audited)
```

Rotation is crash-safe:
1. The key file first gets the new key next to the old one.
2. Every secret is re-encrypted in one transaction.
3. Only then is the old key dropped.

If the process dies at any step, the key file on disk still opens the database. Writes that are in progress during a rotation finish first and are re-encrypted with the new key. Before the old key is dropped, Hangar checks that no stored value still uses it; if one does, the key file keeps both keys and it asks you to run the rotation again.

**After rotating:**
- Back up the new key file.
- **Keep the old key with the database backups from before the rotation.** Restoring one of those backups needs the key it was encrypted with. If the keys don't match, the startup error names both key ids.
- Rotation protects what's written from now on. Anyone who already has an old database copy *and* the old key can still read that copy.

A key from `HANGAR_SECRET_KEY` can't be rewritten by Hangar. To rotate one: stop the server, run `hangar secrets decrypt --yes`, set the new key, and start the server again.

## Downgrading

An older Hangar from before encryption can't read an encrypted database. This includes the `hangar.old` binary kept by [self-upgrades](/guides/upgrades/). To go back to such a version:

```sh
# with the server stopped
hangar secrets decrypt --data /var/lib/hangar/hangar.db --yes
```

This writes all secrets back as plaintext. Start the older binary afterwards. A newer version encrypts them again on its next start.

## What isn't covered

- **Database snapshots** (`--snapshot-dir`) hold your apps' own data, and they are not encrypted by Hangar. Store them on encrypted disks, or treat them like database backups.
- **Running containers** receive their env values in plaintext, as any process must. Anyone with Docker access on the host can read them with `docker inspect`. That is the host's root, which no at-rest encryption can stop.
