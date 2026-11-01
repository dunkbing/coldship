---
title: Upgrading Coldship
description: How Coldship finds, verifies and installs new versions of itself, and how to publish releases.
---

Coldship keeps itself up to date. The server checks for a new release every 6 hours and, by default, installs minor and patch releases automatically once no deployment is running.

## Seeing that a new version is available

- **Dashboard:** a banner shows the new version and its release notes, with **Upgrade now** for admins.
- **CLI:** after a command, a one-line note on stderr:

  ```txt
  ⓘ A new Coldship version is available: 0.3.0 (the server runs 0.2.0). It will be installed automatically when no deployment is running.
  ```

  It's checked at most once an hour. It never appears with `--json`, when stderr isn't a terminal (scripts, agents), or with `COLDSHIP_NO_UPDATE_NOTICE=1`.
- **`coldship version`** shows the CLI and server versions and the update status.

## Upgrading

```sh
coldship upgrade          # install the latest release now (admin)
coldship upgrade --check  # just check now
```

The server:

1. waits for running deployments to finish
2. downloads the binary for its platform
3. verifies it
4. replaces itself, keeping the previous binary as `coldship.old`
5. restarts in place, with the same process ID (systemd and other supervisors see no exit)

The CLI waits until the server reports the new version.

**Automatic upgrades** do the same on their own, for minor and patch releases. **Major versions are never installed automatically**: the banner and the CLI say so, and you run `coldship upgrade` after reading the release notes. Turn automatic upgrades off with `coldship server --auto-upgrade=false`.

A CLI on another machine updates itself with `coldship self-update`. It fetches the release manifest from the server and verifies it with its own built-in key.

## How releases are verified

An upgrade replaces the binary that controls your Docker host, so nothing is trusted on faith:

- **Signed manifests.** Releases are described by a manifest signed with an Ed25519 key. The public key is built into the binary. If the signature doesn't verify, the manifest is ignored.
- **Checked downloads.** Each binary's SHA-256 is in the signed manifest. A mismatch is never installed.
- **A test run first.** The new binary must run and report the expected version (`coldship --version`) before it replaces the old one.
- **No key, no updates.** A binary built without a signing key never checks for or installs updates.

To roll back by hand, stop the server, move `coldship.old` back over `coldship`, and start it again. Rolling back to a version from **before secrets encryption** needs one more step first: `coldship secrets decrypt --yes` (see [Downgrading](/guides/secrets/#downgrading)). Otherwise the older binary can't read the database.

## Publishing a release (maintainers)

Releases are published to GitHub Releases by the `Release` workflow. To cut one, push a tag:

```sh
git tag -a v0.2.0 -m "Zero-downtime deploys and automatic HTTPS"   # the message is the release note servers show
git push origin v0.2.0
```

The workflow:

1. runs the tests
2. builds the dashboard and binaries for Linux and macOS (amd64 and arm64)
3. writes `SHA256SUMS` and a `latest.json` manifest signed with the release key
4. **verifies the release before publishing**: an older Coldship build must install it through the real `self-update` path (signature, checksum, `--version`)
5. publishes the GitHub release with generated notes

Tags with a suffix, like `v0.2.0-rc.1`, become pre-releases. Servers never install them automatically, because `releases/latest` skips pre-releases.

**The signing key.** The private key is the `COLDSHIP_RELEASE_KEY` Actions secret. The public key is committed in `release/coldship-release.pub` and built into every binary. Keep an offline backup of the private key: if it's lost, existing installations can't verify new releases. If it leaks, someone could sign releases your servers would install.

To build a release locally (for testing): `make release VERSION=0.2.0 RELEASE_KEY=path/to/key`, which writes `dist/`.
