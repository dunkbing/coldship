# Coldship

**A self-hosted app platform that AI agents can run for you, with you in control.**

Deploy apps to your own server, from a container image or straight from Git. Your coding agent
(Claude Code or any MCP client) can deploy, debug and roll back on its own. Anything destructive,
like deleting production data, waits for your approval.

- **One command to deploy:** no config files to learn; every change is still recorded in an audit log
- **Your approval for risky changes:** destructive production changes need a human
- **Deploy from Git:** with or without a Dockerfile (Node, Bun, Python, Go, static sites)
- **Domains and HTTPS:** automatic certificates, zero-downtime deploys
- **Preview environments** for branches, deleted automatically
- **Managed Postgres and Redis** with automatic snapshots
- **Built-in dashboard**, audit log, encrypted secrets, and self-updates

## Install

On a Linux server with [Docker](https://docs.docker.com/engine/install/):

```sh
curl -fsSL https://raw.githubusercontent.com/dunkbing/coldship/main/install.sh | sh
```

That's it. It installs Docker if needed, runs Coldship as a service that starts on boot, and logs
your CLI in, so `coldship` commands work right away. It prints your **admin token** once: keep it in
a password manager. Coldship keeps itself up to date after that. Back up
`/var/lib/coldship/coldship.key` (it encrypts your stored secrets) away from the server.

The dashboard is at http://127.0.0.1:7070 on the server (from your laptop: `ssh -L 7070:127.0.0.1:7070 your-server`).

On macOS (or with `COLDSHIP_CLI_ONLY=1`) the same command installs just the CLI; connect it with
`coldship login https://your-server`. To build from source, you need
[Go](https://go.dev/dl/) and [Bun](https://bun.sh): `git clone https://github.com/dunkbing/coldship.git && cd coldship && make build`.

## Deploy an app

No config file needed. Inside a Git repository:

```sh
coldship deploy --domain web.example.com   # point the domain's DNS at your server
```

Coldship names the app after the folder, builds your current branch (with or without a Dockerfile)
and serves it with HTTPS. Or deploy an image:

```sh
coldship deploy --name hello --image nginx:1.27-alpine --port 80 --domain hello.example.com
```

Change things the same way. Each flag changes only what it names:

```sh
coldship deploy --name hello --env API_KEY=... --db postgres
```

## Let your agent use it

Create a token for your agent. It can deploy and debug, but it can't approve risky changes:

```sh
coldship token create --name claude --scope deploy
```

Add Coldship to Claude Code:

```sh
claude mcp add coldship -e COLDSHIP_URL=http://127.0.0.1:7070 -e COLDSHIP_TOKEN=cst_... -- coldship mcp
```

Then ask it something like: *"Deploy this repo to web.example.com with a Postgres database, and if it fails, figure out why and fix it."*

## Everyday commands

```sh
coldship apps                       # list apps
coldship diagnose web               # what's wrong, and how to fix it
coldship logs web                   # recent logs
coldship rollback web --reason ...  # back to the last good version
coldship approve <plan-id>          # approve a risky change
coldship export web > coldship.yaml # the app as a config file, if you want one in Git
```

## Documentation

The full guides live in [`website/`](website/src/content/docs/). To browse them locally: `make website-dev`.

## Development

```sh
make build   # dashboard + binary
make test    # Go tests
```

See the [contributing guide](website/src/content/docs/project/contributing.md) and
[architecture](website/src/content/docs/project/architecture.md).
