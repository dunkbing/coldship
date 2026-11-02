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

## Responding to alerts

An agent can start on its own when something goes wrong. In its settings, under **Start on its own when**, pick the alerts it answers:

| Alert | |
|---|---|
| An app goes down | `app.down` |
| A deploy or build fails | `deploy.failed` |
| Off-site backups fail | `backup.failing` |
| The server's disk is almost full | `disk.low` (default organization only) |

When one fires, Coldship starts a session and asks the agent to look into it with its tools (status, logs, deploy logs, metrics) and finish with a short report. That report goes to the organization's [notification channels](/guides/notifications/) as `agent.report`, with a link to the session, so you can read what it did and carry on the conversation. Channels pick their events when they're added, so a channel added before this existed needs adding again with **An agent reports on an alert**.

- **It only looks, unless you say otherwise.** Alert sessions are viewers. Turn on **Let it fix things** (for a deployer agent) to let it roll back or restart; destructive production changes still wait for an admin.
- **Agents limited to some apps** only hear about those apps, and not about server-wide alerts like the disk.
- **Nobody may be watching.** If the agent asks permission for a tool, `agent.waiting` tells your channels; unanswered after 15 minutes, the request is declined and the turn ends, so it reports what it has. Alert sessions end after 15 minutes without messages.
- **Allow its tools ahead of time.** Agents like Claude Code ask before each tool they haven't been allowed. Start one session yourself and choose **Always allow** for the coldship tools it uses; that's kept in its home volume, so alert sessions don't stop to ask.
- **No storms.** Each agent takes an alert at most once per app every 30 minutes, at most 2 alert sessions run at once (leaving room for yours), and an agent's own deploys never start agents.
- **Alert text is data.** Error messages and build output can be written by anyone who can push code, so Coldship hands them to the agent fenced off and marked as data, not instructions.

Sessions started by an alert show a bell in the session list, and the audit log names the alert (`alert:app.down`) as who started it.

## How it's contained

- Each session runs the agent in its own container (`node:22-bookworm-slim`, 2 GB memory limit), on Docker's default network: it reaches the AI provider, but not Coldship's network, where apps and databases talk to each other. Ports published on the server are reachable from it, as from anywhere else.
- It reaches Coldship only through a Unix socket in a directory of its own, the only host path mounted in. Nothing else of the server's file system is visible, and Coldship offers it no file or terminal access.
- It acts with a token made for the session, with the agent's role and app limits, which is revoked when the session ends (and any left by a crash are removed at the next start). Everything it does is in the audit log under that token's name.
- Its home directory (`/root`, with npm's cache) is a volume, `coldship-agent-<org>-<agent>`, so later sessions start faster. `coldship uninstall` removes it (unless `--keep-data`).

Agents' own built-in tools (like Claude Code's shell) run inside that container, not on the server.
