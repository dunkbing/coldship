---
title: Introduction
description: What Hangar is, who it is for, and how it differs from other self-hosted platforms.
---

Hangar runs containerized apps on your own server. What sets it apart is who it is designed for: **AI agents are first-class users**, and every interface, error message and safety rail is shaped around how agents actually behave.

## The core idea

An agent working on your codebase should be able to ship and operate what it builds: deploy a change, notice it broke, find out why, and roll back. Most platforms make that either impossible (the UI is the only interface) or dangerous (the API will happily delete a production database).

Hangar gives agents a small set of operations that are safe to call repeatedly, and keeps humans in the loop for the changes that matter:

1. The agent edits a declarative [spec](/reference/spec/) and asks for a **plan**. A plan is a dry run that returns a diff with each change marked destructive or not.
2. The agent **applies** the plan by its id, with a reason. If anything changed in the meantime, the plan is stale and the agent has to re-plan.
3. If the plan is destructive and touches a production app, it waits for a **human approval**.
4. If the deploy fails, the agent calls **diagnose**, then fixes the spec or **rolls back**.
5. Everything lands in an **audit log** under the agent's own token name.

## Interfaces

| Interface | For | How |
|---|---|---|
| MCP server | Claude Code, Cursor and other MCP clients | `hangar mcp` (stdio) |
| CLI | Humans, scripts and agents with a shell | `hangar ...`, with `--json` for machine-readable output |
| HTTP API | Integrations | JSON over HTTP with bearer tokens |
| Dashboard | Humans | Served by `hangar server` at `/` |

All four go through the same HTTP API, so the same permissions, guardrails and audit log apply to each of them.

## Status

Hangar is an early starter project. It deploys container images to a single Docker host. Building from Git, domains with TLS, preview environments and multi-node support are on the [roadmap](/project/roadmap/).
