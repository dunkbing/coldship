---
title: Preview environments
description: Throwaway copies of an app at a branch or image, for testing changes before they reach the real app.
---

A preview is a temporary copy of an app, running a different Git ref or image. It's how an agent (or a person, or CI) checks a change for real before it touches the actual app, and it deletes itself when its time is up.

```sh
hangar preview create shop --ref feat/login --ttl 2h --env FEATURE_LOGIN=1 --reason "try the new login"
```

```txt
Created preview shop-feat-login of shop: preview deployed
  https://shop-feat-login.previews.example.com
Expires Sun 14:32 (in 2h0m).
```

Agents use the `hangar_preview_create`, `hangar_preview_list` and `hangar_preview_delete` [MCP tools](/reference/mcp-tools/). The usual loop is: create a preview, check it with `hangar_get_app` and `hangar_diagnose`, iterate, then delete it.

## What a preview is

A preview of `shop` named after `feat/login` is the app **`shop-feat-login`**, built from `shop`'s current spec with these changes:

| | In the preview |
|---|---|
| Source | `--ref` (apps built from Git) or `--image` (apps that run an image). With neither, the parent's ref re-resolved to its latest commit. |
| Stage | `preview`, always. A plan can't turn a preview into a production app. |
| Domains | `<preview name>.<preview domain>`, if the server has a `--preview-domain`. Never the parent's domains. |
| `host_port` | Removed (two apps can't share one) |
| Volumes | **Fresh and empty.** Volumes belong to an app, so a preview never touches its parent's data. |
| Env | Inherited, **except from production apps** (see below). `--env` adds or overrides. |

It's deployed through the normal plan and apply path, so it gets the same audit trail, pinned commits, health checks, zero-downtime updates and deploy logs as any app.

### Previews of production apps don't get production secrets

A production app's env typically holds things like `DATABASE_URL` and payment API keys. A throwaway preview built from an unreviewed branch shouldn't silently get them, and it certainly shouldn't write to the production database. So previews of **production** apps start with **no inherited env**. Pass what the preview needs with `--env`, or opt in with `--inherit-env` (recorded in the audit log). Previews of staging apps inherit env by default.

## Updating and extending

`preview create` is declarative and safe to repeat. Running it again with the same arguments, for example after pushing to the branch, redeploys if anything changed (such as a new commit) and resets the expiry:

```txt
created: false | action: update | changes: build.commit
```

Leaving out an `--env` you passed before removes it, just as removing a line from a spec would.

## Expiry and limits

- **TTL:** each preview lives for `--ttl` (default 24h, max 7 days; the server's `--preview-ttl` and `--preview-max-ttl` set these). Every minute, a background reaper deletes expired previews with their containers and volumes, audited as `hangar` with reason "preview expired". A preview whose first deploy failed is cleaned up too.
- **Limit:** at most 20 previews exist at once (`--max-previews`). Creating one more fails with `preview_limit`, listing the existing previews soonest-expiring first, so you can decide which to delete. Updating an existing preview is never blocked.
- **Deleting early:** `hangar preview delete <name>`, or the **Delete preview** button in the dashboard. This only works on previews. Regular apps are deleted through a reviewed plan.

## Preview URLs

Give the server a domain for previews, and point a wildcard DNS record at it:

```sh
hangar server --preview-domain previews.example.com   # DNS: *.previews.example.com → this server
```

Each preview then gets `https://<name>.previews.example.com` with its own certificate. Without a preview domain, previews still run, and you can check them with `diagnose` and `logs`, or reach them from other apps by name on Hangar's network.

Let's Encrypt allows 50 new certificates per registered domain per week. If you create many short-lived previews, use a domain dedicated to previews.

## In the dashboard

Previews have their own section on the apps page, showing their parent, source and time left. A preview's page has a banner with the expiry and a **Delete preview** button.
