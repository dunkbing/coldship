---
title: Agents in the dashboard
description: Run Claude Code, Codex or any ACP agent on the server and work with it from the dashboard, with permission prompts and Coldship's guardrails.
---

The **Agents** page runs an AI agent on your server and lets you chat with it in the dashboard: "why is web failing?", "deploy my repo with a Postgres database", "is the disk filling up?". It works through Coldship's own tools, asks before it acts, and production changes still wait for an admin.

To use an agent on your own machine instead, see [Connect an AI agent](/guides/ai-agents/).

## Connecting an agent

Admins click **Connect agent** and pick one:

| Agent | Needs | Runs |
|---|---|---|
| **Claude Code** | `ANTHROPIC_API_KEY` | `@agentclientprotocol/claude-agent-acp` |
| **Codex** | `OPENAI_API_KEY` or `CODEX_API_KEY` | `@agentclientprotocol/codex-acp` |
| **Gemini CLI** (experimental) | `GEMINI_API_KEY` | `@google/gemini-cli --acp` |
| **Custom** | your command and variables | anything that speaks the [Agent Client Protocol](https://agentclientprotocol.com) on stdio |

Package versions are pinned. Keys are stored encrypted, like every secret in Coldship, and passed only to the agent's container. Each organization connects its own agents.

Give the agent a **role**, deployer or viewer, and optionally limit it to some apps (`agent-*`). It never gets more than the role of the person who starts a session, and it's never an admin: approving production changes stays with people.

## Sessions

**New session** starts the agent; the first start downloads it, which takes a minute. Then write to it. As it works you see its messages, its plan, and each tool call with input and output.

When the agent wants to run a tool, a card asks you to **Allow** or **Reject** it. Anyone in the organization who can deploy may answer. Choices like "Always allow" are remembered by the agent in its home volume, so they carry over to later sessions, whoever starts them. **Stop** interrupts the current turn; **End session** stops the agent.

Sessions end after 30 minutes without a message, and at most 4 run at once on the server (across all organizations). Transcripts are kept in memory, so they're gone after a restart.

## How it's contained

- Each session runs the agent in its own container (`node:22-bookworm-slim`, 2 GB memory limit), on Docker's default network: it reaches the AI provider, but not Coldship's network, where apps and databases talk to each other. Ports published on the server are reachable from it, as from anywhere else.
- It reaches Coldship only through a Unix socket in a directory of its own, the only host path mounted in. Nothing else of the server's file system is visible, and Coldship offers it no file or terminal access.
- It acts with a token made for the session, with the agent's role and app limits, which is revoked when the session ends (and any left by a crash are removed at the next start). Everything it does is in the audit log under that token's name.
- Its home directory (`/root`, with npm's cache) is a volume, `coldship-agent-<org>-<agent>`, so later sessions start faster. `coldship uninstall` removes it (unless `--keep-data`).

Agents' own built-in tools (like Claude Code's shell) run inside that container, not on the server.
