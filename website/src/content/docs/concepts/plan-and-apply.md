---
title: Plan and apply
description: How every change in Hangar goes from a dry-run plan to a deployment.
---

Every change in Hangar, including creates, updates, deletes and rollbacks, goes through two steps. This is the main thing that makes it safe to hand to an agent.

## Plan

A plan compares the app's **current spec** (from the last successful deployment) with the **desired spec** you send, and records the difference.

```sh
hangar plan -f hangar.yaml          # create or update
hangar plan --delete web            # delete an app
hangar plan -f hangar.yaml --redeploy   # recreate the container even if nothing changed
```

A plan contains:

| Field | Meaning |
|---|---|
| `id` | What you pass to apply, e.g. `plan_0a2136c9ac8b` |
| `action` | `create`, `update`, `delete`, `redeploy` or `noop` |
| `changes` | One entry per changed field, e.g. `{"path": "image", "op": "update", "from": "nginx:1.27", "to": "nginx:1.28"}` |
| `destructive` | True if any change destroys data or the app |
| `downtime` | True if the container is recreated |
| `requires_approval` | True if a human must approve before apply (see [guardrails](/concepts/guardrails/)) |
| `next_step` | A plain-language instruction for the agent |
| `expires_at` | Plans expire 30 minutes after creation |

Env values never appear in a plan. Changed env keys are listed with the note "value changed (redacted)".

### What counts as destructive

| Change | Destructive? |
|---|---|
| Deleting an app | Yes, always |
| Removing a volume from the spec | Yes: the volume and its data are deleted |
| Image, port, memory, env changes | No (but the container restarts) |
| Adding a volume | No |

## Apply

```sh
hangar apply plan_0a2136c9ac8b --reason "upgrade nginx for CVE-2026-1234"
```

Apply checks, in order:

1. **A reason is given.** Otherwise `reason_required`.
2. **The plan exists and hasn't expired.** Otherwise `plan_not_found` or `plan_expired`.
3. **Approval, if required.** Otherwise `approval_required`.
4. **The plan isn't stale.** If the app was deployed or deleted after the plan was made, you get `plan_stale`. Make a new plan and review it again.

Then Hangar pulls the image (before touching the running container), replaces the container, and waits for a startup grace period (3 seconds by default). A deploy only succeeds if the container is still running, with no restarts, at the end of that period.

### Apply is idempotent

Applying a plan that was already applied returns the original result and does nothing else. If a network error leaves an agent unsure whether its apply went through, it can safely call apply again.

### Failed deploys

If the new container doesn't stay up, the deployment is marked `failed` and the error tells you what to do next:

```json
{
  "error": {
    "code": "container_exited",
    "message": "container did not stay up after start (state=restarting, exit_code=1, restarts=4, oom_killed=false)",
    "hint": "call diagnose for this app to see logs and likely causes",
    "suggested_fix": { "tool": "rollback", "app": "worker" },
    "details": { "deployment": "dep_832572d5f155", "plan": "plan_d00e21371ef9" }
  }
}
```

A failed deploy never replaces the app's current spec. The last good deployment stays the reference point for future plans and for [rollback](/concepts/debugging-and-rollback/).
