---
title: Roadmap
description: What is planned next for Hangar.
---

Hangar today deploys container images, or builds them from Git repositories with a Dockerfile, on a single Docker host. Planned next, roughly in order:

- **Builds without a Dockerfile.** Detect and build a repository Nixpacks/Buildpacks-style, explaining the detected build so an agent can correct it.
- **Git webhooks.** Plan automatically on push, so a human or agent only has to review and apply.
- **Domains and TLS.** An embedded Caddy reverse proxy with automatic certificates, configured by a `domains` field in the spec.
- **Zero-downtime deploys.** Start the new container, health-check it, then switch traffic, instead of stop-and-replace.
- **Preview environments.** `stage: preview` with a `ttl`, so agents can spin up a copy of the stack to test their own changes, and it cleans itself up.
- **Managed databases.** Postgres and Redis add-ons with automatic snapshots before destructive operations.
- **Policies.** Spend and instance caps per token, and per-app permissions.
- **Notifications.** Approval requests to Slack or a webhook, and event webhooks that can wake an agent when a deploy fails.
- **Multi-node.** A separate node agent, with Postgres as the state store.
- **Secrets at rest.** Encrypt env values in the state store.
