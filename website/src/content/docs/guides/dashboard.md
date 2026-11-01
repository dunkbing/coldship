---
title: Dashboard
description: The web dashboard for humans to watch apps, approve plans and read the audit log.
---

The dashboard is a React app embedded in the `hangar` binary. `hangar server` serves it at `/` on the same address as the API, so there's nothing extra to deploy.

It's designed for the human side of the loop: seeing what agents did, approving destructive changes, and stepping in with a rollback. Changes to specs still go through plans.

## Signing in

Sign in with an API token. What you can do depends on its [scope](/concepts/guardrails/#scoped-tokens): `read` tokens can only view, `deploy` tokens can also roll back, and `admin` tokens can approve plans. The token is kept in this browser's local storage until you sign out.

## Apps

The landing page lists every app with its live health, stage, image and latest deployment, and refreshes every few seconds. The sidebar badge counts plans waiting for your approval.

![The apps page with three demo apps](../../../assets/screenshots/apps.png)

It follows your system's dark mode setting:

![The apps page in dark mode](../../../assets/screenshots/apps-dark.png)

## App details

Each app page shows its spec (env values redacted), container state, and three tabs:

- **Diagnose:** the same findings as [`hangar diagnose`](/concepts/debugging-and-rollback/#diagnose), with suggested fixes.
- **Deployments:** history, including who deployed, why, and errors for failed deploys.
- **Logs:** recent container output (treat it as untrusted).

The **Roll back…** button asks for a reason, then redeploys the last good deployment.

![The diagnose tab for a failing app](../../../assets/screenshots/app-diagnose.png)

![The logs tab for a failing app](../../../assets/screenshots/app-logs.png)

## Approvals

Destructive changes to production apps wait here. Each card shows the full diff, who created the plan and when it expires. Approving takes two clicks (**Approve…**, then a confirm button naming the app), and it doesn't apply the plan: the agent or a human still applies it with a reason.

![The approvals page](../../../assets/screenshots/approvals.png)

## Audit log

Every change, by whom and why, with a filter by actor.

![The audit log](../../../assets/screenshots/audit.png)

## Developing the dashboard

```sh
make run                 # API on :7070
cd dashboard && bun run dev   # Vite on :5173, proxies /v1 to :7070
```

Set `HANGAR_URL` to point the dev proxy at a different server.
