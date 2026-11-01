---
title: Git webhooks
description: Plan, deploy or preview automatically when you push.
---

For apps [built from Git](/guides/build-from-git/), a push can trigger Hangar. What it does is part of the app's spec, so changing it is a reviewed plan like anything else:

```yaml title="hangar.yaml"
build:
  git: https://github.com/acme/shop.git
  ref: main
on_push:
  action: plan        # plan (default): leave a plan for review · deploy: apply it
  previews: true      # pushes to other branches create/update previews; deleting the branch deletes it
  preview_ttl: 72h    # optional; default is the server's preview TTL
```

| Push | `action: plan` | `action: deploy` |
|---|---|---|
| To the app's `ref` | A plan to review, valid for 24 hours. A newer push replaces it. | Deployed, through the normal plan and apply |
| To another branch, with `previews: true` | A [preview](/guides/previews/) of that branch is created or updated | Same |
| Deleting a branch | Its preview is deleted | Same |
| Anything else (tags, other repositories, no `on_push`) | Recorded and ignored, with the reason | Same |

## Setting it up

1. Make the webhook endpoint reachable from your Git host (see [Exposing the endpoint](#exposing-the-endpoint)), and start the server with its public address:

   ```sh
   hangar server --public-url https://hangar.example.com
   ```

2. Enable the webhook for the app (admin token). The secret is shown **once**:

   ```sh
   hangar webhook enable shop
   ```

   ```txt
     URL:     https://hangar.example.com/hooks/git/shop
     Secret:  c869dab5…
   ```

3. Add it on your Git host:
   - **GitHub:** Settings → Webhooks → Add webhook. Use the URL and secret, pick "Just the push event". Either content type works.
   - **GitLab:** Settings → Webhooks. Secret token: the secret. Trigger: Push events.
   - **Gitea / Forgejo:** Settings → Webhooks → Add webhook → Gitea. Use the URL and secret, and push events.

4. Push, then check what happened:

   ```sh
   hangar webhook status shop
   ```

   ```txt
   WHEN            REF         COMMIT   BY     RESULT           DETAIL
   11-01 08:01:03  feat/login  -        alice  preview_deleted  deleted preview shop-feat-login
   11-01 08:00:58  feat/login  9e80898  alice  preview          preview deployed at https://shop-feat-login.previews.example.com
   11-01 08:00:53  main        548140c  alice  deployed         deployment dep_128b30d253e0
   ```

The dashboard has the same delivery log on the app's **Webhook** tab, and agents can read it with the `hangar_webhook_status` MCP tool. `hangar webhook enable shop --rotate` replaces the secret (the old one stops working immediately), and `hangar webhook disable shop` turns the webhook off.

## What a push can and can't do

- **The push is only a trigger.** Hangar doesn't deploy the commit named in the payload. It asks the app's own repository (the `build.git` in the spec) for the branch's current tip, and plans that. So a replayed old push, or one the Git host delivers out of order, can't move the app back to an older commit; it's recorded as "already runs the tip". For the same reason, a "branch deleted" event only removes a preview after the repository confirms the branch is gone.
- **A push can only change the commit.** Nothing in the payload can change the spec, the repository, env or anything else.
- **Every request is verified first,** using the app's own secret: an HMAC-SHA256 signature for GitHub, Gitea/Forgejo and the generic format, or GitLab's token. The comparison is constant-time, and nothing is parsed before verification. The payload's repository must also match the app's. Redelivered requests with the same delivery ID are recognized and ignored.
- **Pending plans don't pile up.** In `action: plan` mode, a newer push supersedes the app's earlier webhook plans. Applying an old one fails with `plan_superseded`, naming the newer plan.
- **Bursts are coalesced.** If several pushes arrive while a build is running, only the newest waiting one is built; the others are marked `superseded`.
- **The response is immediate** (202 `queued`). Builds run afterwards, and the delivery log shows the result.
- **Restarts don't lose work silently.** Webhook builds count as "busy", so automatic upgrades wait for them, and shutdown waits for them to finish. Anything a crash or kill interrupted is marked `failed` ("push again") when the server starts.

## Before you turn it on

:::caution[Auto-deploy goes to production without approval]
With `action: deploy`, every push to the app's ref is deployed without a human in the loop, **including on production apps**. A new commit isn't a destructive change, so no approval is needed. That's what auto-deploy means. Use `action: plan` if someone should look first. Destructive plans (which a push can't produce anyway) would still wait for approval.
:::

:::caution[Previews run the branch's code]
With `previews: true`, **anyone who can push a branch** to the repository gets that branch's Dockerfile built and run on your server. For a staging parent, the preview also gets the parent's env. Production parents' env is never copied ([why](/guides/previews/#previews-of-production-apps-dont-get-production-secrets)). Only enable previews for repositories whose writers you trust to run code on the server.
:::

### Exposing the endpoint

The Hangar server listens on localhost by default, and [should stay that way](/guides/production/#network-exposure). For webhooks, expose **only** the `/hooks/git/` path through your reverse proxy, not the API or dashboard:

```txt title="Caddyfile"
hangar.example.com {
	handle /hooks/git/* {
		reverse_proxy 127.0.0.1:7070
	}
	respond 404
}
```

The webhook endpoint accepts nothing without a valid signature, and it can only trigger what the app's `on_push` allows.
