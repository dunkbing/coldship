---
title: Roadmap
description: What is planned next for Hangar.
---

Hangar today deploys container images, or builds them from Git repositories with a Dockerfile, on a single Docker host, with domains, automatic HTTPS, zero-downtime deploys and self-upgrades. Planned next, roughly in order:

- **Builds without a Dockerfile.** Detect and build a repository Nixpacks/Buildpacks-style, explaining the detected build so an agent can correct it.
- **Git webhooks.** Plan automatically on push, so a human or agent only has to review and apply.
- **Preview environments.** `stage: preview` with a `ttl`, so agents can spin up a copy of the stack to test their own changes, and it cleans itself up.
- **Managed databases.** Postgres and Redis add-ons with automatic snapshots before destructive operations.
- **Policies.** Spend and instance caps per token, and per-app permissions.
- **Notifications.** Approval requests to Slack or a webhook, and event webhooks that can wake an agent when a deploy fails.
- **Multi-node.** A separate node agent, with Postgres as the state store.
- **Secrets at rest.** Encrypt env values in the state store.
