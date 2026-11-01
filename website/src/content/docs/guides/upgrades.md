---
title: Upgrading Hangar
description: How Hangar finds, verifies and installs new versions of itself, and how to publish releases.
---

Hangar keeps itself up to date. The server checks for a new release every 6 hours and, by default, installs minor and patch releases automatically once no deployment is running.

## Seeing that a new version is available

- **Dashboard:** a banner shows the new version and its release notes, with **Upgrade now** for admins.
- **CLI:** after a command, a one-line note on stderr:

  ```txt
  ⓘ A new Hangar version is available: 0.3.0 (the server runs 0.2.0). It will be installed automatically when no deployment is running.
  ```

  It's checked at most once an hour. It never appears with `--json`, when stderr isn't a terminal (scripts, agents), or with `HANGAR_NO_UPDATE_NOTICE=1`.
- **`hangar version`** shows the CLI and server versions and the update status.

## Upgrading

```sh
hangar upgrade          # install the latest release now (admin)
hangar upgrade --check  # just check now
```

The server:

1. waits for running deployments to finish
2. downloads the binary for its platform
3. verifies it
4. replaces itself, keeping the previous binary as `hangar.old`
5. restarts in place, with the same process ID (systemd and other supervisors see no exit)

The CLI waits until the server reports the new version.

**Automatic upgrades** do the same on their own, for minor and patch releases. **Major versions are never installed automatically**: the banner and the CLI say so, and you run `hangar upgrade` after reading the release notes. Turn automatic upgrades off with `hangar server --auto-upgrade=false`.

A CLI on another machine updates itself with `hangar self-update`. It fetches the release manifest from the server and verifies it with its own built-in key.

## How releases are verified

An upgrade replaces the binary that controls your Docker host, so nothing is trusted on faith:

- **Signed manifests.** Releases are described by a manifest signed with an Ed25519 key. The public key is built into the binary. If the signature doesn't verify, the manifest is ignored.
- **Checked downloads.** Each binary's SHA-256 is in the signed manifest. A mismatch is never installed.
- **A test run first.** The new binary must run and report the expected version (`hangar --version`) before it replaces the old one.
- **No key, no updates.** A binary built without a signing key never checks for or installs updates.

To roll back by hand, stop the server, move `hangar.old` back over `hangar`, and start it again. Rolling back to a version from **before secrets encryption** needs one more step first: `hangar secrets decrypt --yes` (see [Downgrading](/guides/secrets/#downgrading)). Otherwise the older binary can't read the database.

## Publishing a release (maintainers)

```sh
# once: create the signing key (keep hangar-release.key secret)
./bin/hangar release keygen

# each release: build, sign, upload dist/ as-is
make release VERSION=0.3.0 \
  UPDATE_PUBLIC_KEY=<public key from keygen> \
  UPDATE_URL=https://releases.example.com/hangar/latest.json \
  NOTES="Zero-downtime deploys and automatic HTTPS"
```

`make release` cross-compiles `dist/hangar-linux-amd64` and `dist/hangar-linux-arm64` with the version, public key and update URL built in, and writes `dist/latest.json` signed with `hangar-release.key`. Upload the whole `dist/` directory to the update URL's location. The manifest refers to the binaries by relative paths.
