---
title: Dashboard
description: The web dashboard for humans to watch apps, approve plans and read the audit log.
---

The dashboard is a React app embedded in the `coldship` binary. `coldship server` serves it at `/` on the same address as the API, so there's nothing extra to deploy.

It's designed for the human side of the loop: seeing what agents did, approving destructive changes, and stepping in with a rollback. Changes to specs still go through plans.

## Signing in

People sign in with their email and password; see [Teams and organizations](/guides/organizations/) for the first account and invites. What you can do depends on your role: viewers only look, deployers change apps, and admins also approve plans and manage the team.

You can also sign in with an API token (the **API token** tab). It's exchanged for a 12-hour [session cookie](/concepts/guardrails/#dashboard-sessions) and isn't stored in the browser. Account sessions last 7 days.

## Overview

The home page shows the organization at a glance and refreshes every 10 seconds:

- **Needs attention:** what needs someone now. That means apps that are failing or not running, failed deploys, scheduled jobs whose last run failed, changes waiting for approval, agents waiting for an answer, and data services that are down. Admins of the default organization also see failing off-site backups, a disk that's 90% full, and a new Coldship release. The list is worked out from how things are now, so an item goes away once it's fixed. Each item links to the page where you can fix it.
- **Apps:** every app with its health, failing ones first.
- **Recent deploys** across the apps, recent **agent sessions**, and, for the default organization, the **server's** memory and disks.

With nothing deployed yet, it shows the first steps instead: deploy an app, add a database, and set up an AI agent.

![The overview, with a failed deploy, a failing job, an approval and an agent waiting](../../../assets/screenshots/overview.png)

It follows your system's dark mode setting:

![The overview in dark mode](../../../assets/screenshots/overview-dark.png)

## Search and commands

Press <kbd>⌘K</kbd> (<kbd>Ctrl K</kbd> on Linux and Windows), or click **Search…** in the sidebar (the search icon on phones), to jump anywhere:

- pages, apps, data services and recent agent sessions;
- an app's tab, by name: `web logs`, `api deployments`;
- actions: a new app or database, switching organization, signing out;
- **Ask an agent:** whatever you typed becomes the first message of a new agent session.

It only offers what your role and token can see: a token limited to some apps finds only those.

![The command palette, searching for web](../../../assets/screenshots/palette.png)

## Apps

The apps page lists every app with its live health, stage, image and latest deployment, and refreshes every few seconds. The sidebar badge counts plans waiting for your approval.

![The apps page with three demo apps](../../../assets/screenshots/apps.png)

![The apps page in dark mode](../../../assets/screenshots/apps-dark.png)

## App details

Each app page shows its spec (env values redacted; for Git apps, the repository, ref, pinned commit and Dockerfile), container state, and three tabs:

- **Diagnose:** the same findings as [`coldship diagnose`](/concepts/debugging-and-rollback/#diagnose), with suggested fixes.
- **Deployments:** history, including who deployed, why, the commit and errors. Failed builds are marked **failed in build**, and each deployment has a **Log** button showing its build output, health checks and, for failures, the new container's output.
- **Logs:** recent container output (treat it as untrusted).

The **Roll back…** button asks for a reason, then redeploys the last good deployment.

![The diagnose tab for an app whose new version failed its health check](../../../assets/screenshots/app-diagnose.png)

![The logs tab](../../../assets/screenshots/app-logs.png)

![Deployments of an app built from Git, with a failed build's log open](../../../assets/screenshots/app-git-deployments.png)

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

Set `COLDSHIP_URL` to point the dev proxy at a different server.
