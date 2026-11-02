---
title: Roadmap
description: What is planned next for Coldship.
---

Coldship today deploys container images, or builds them from Git repositories (with or without a Dockerfile), on a single Docker host, with domains, automatic HTTPS, zero-downtime deploys, preview environments, managed Postgres and Redis, Git webhooks, and self-upgrades. Planned next, roughly in order:

- **More build languages.** Ruby, PHP, Rust, Java and Elixir detection for builds without a Dockerfile.
- **Seeded previews.** Start a preview's volumes from a sanitized snapshot of the parent's data instead of empty.
- **Database upgrades.** Major version changes by dump and restore.
- **Policies.** Spend and instance caps per token, and per-app permissions.
- **Notifications.** Approval requests to Slack or a webhook, and event webhooks that can wake an agent when a deploy fails.
- **Multi-node.** A separate node agent, with Postgres as the state store.
- **Encrypted backups.** Encrypt off-site backups with a passphrase you keep, on top of the bucket's encryption.
