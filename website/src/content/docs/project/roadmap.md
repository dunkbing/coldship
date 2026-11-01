---
title: Roadmap
description: What is planned next for Hangar.
---

Hangar today deploys container images, or builds them from Git repositories with a Dockerfile, on a single Docker host, with domains, automatic HTTPS, zero-downtime deploys, preview environments, managed Postgres and Redis, Git webhooks, and self-upgrades. Planned next, roughly in order:

- **Builds without a Dockerfile.** Detect and build a repository Nixpacks/Buildpacks-style, explaining the detected build so an agent can correct it.
- **Seeded previews.** Start a preview's volumes from a sanitized snapshot of the parent's data instead of empty.
- **Database upgrades.** Major version changes by dump and restore, and off-server snapshot storage (S3-compatible).
- **Policies.** Spend and instance caps per token, and per-app permissions.
- **Notifications.** Approval requests to Slack or a webhook, and event webhooks that can wake an agent when a deploy fails.
- **Multi-node.** A separate node agent, with Postgres as the state store.
- **Secrets at rest.** Encrypt env values in the state store.
