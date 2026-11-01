---
title: Contributing
description: Build, test and develop Coldship, its dashboard and these docs.
---

## Prerequisites

- Go 1.26+
- [Bun](https://bun.sh) for the dashboard and website. Add packages with `bun add`, not by editing `package.json`.
- Docker, for running apps and regenerating screenshots

## Make targets

| Target | What it does |
|---|---|
| `make build` | Builds the dashboard into `internal/web/dist`, then the Go binary into `bin/coldship` |
| `make go` | Builds only the Go binary (uses whatever dashboard build is present) |
| `make test` | Go tests |
| `make run` | Build and start the server |
| `make website` | Build this docs site into `website/dist`. Set `SITE_URL` to the public address to generate the sitemap. |
| `make website-dev` | Docs dev server with live reload |
| `make screenshots` | Regenerate the dashboard screenshots used in these docs |

## Tests

```sh
make test
```

Engine tests run against `runtime.Fake`, an in-memory runtime, so they need no Docker. `internal/api` has an end-to-end test through HTTP, covering scopes, approvals and the audit log.

## Screenshots

The docs screenshots are generated, not hand-made:

```sh
make screenshots
```

`scripts/screenshots.sh`:

1. builds the binary and starts a throwaway server on port 7272 with its own state file
2. seeds demo apps (`demo-web`, `demo-api`, `demo-worker`, and `demo-site` built from a local Git repository served over HTTP) on your local Docker as an agent called `claude`, including a failing deploy, a failing build and a plan waiting for approval
3. runs `dashboard/scripts/screenshots.mjs` with Playwright inside the official `mcr.microsoft.com/playwright` Docker image, so no browser libraries are needed on the host
4. writes PNGs to `website/src/assets/screenshots/` and removes the demo containers and volumes

Re-run it whenever the dashboard changes, and commit the updated images.
