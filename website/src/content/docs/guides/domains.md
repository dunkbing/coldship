---
title: Domains and zero downtime
description: Serve apps on their own domains with automatic HTTPS, and deploy new versions without dropping requests.
---

## Domains with automatic HTTPS

Give an app one or more hostnames and the port it listens on:

```yaml title="hangar.yaml"
name: shop
image: ghcr.io/acme/shop:1.4.2
port: 8080
domains:
  - shop.example.com
  - www.shop.example.com
health:
  path: /healthz      # optional: must answer 2xx/3xx before traffic switches
  timeout: 60s
```

Point the domains' DNS at the server and deploy. Hangar's proxy gets a certificate from Let's Encrypt the first time each hostname is requested, renews it, and redirects HTTP to HTTPS. Once deployed, `hangar status shop` (and the dashboard) lists the app's URLs.

- **Each domain belongs to one app.** Planning a domain another app already has fails with `domain_in_use`, naming that app.
- **`*.localhost` names** get certificates from the proxy's own local CA, which is handy for trying things out.
- **No public ports needed.** Apps with domains don't need a `host_port`; the proxy reaches them over Hangar's internal Docker network.
- **Apps can reach each other by name** on that network, e.g. `http://api:3000` from another app.

### How it works

`hangar server` runs [Caddy](https://caddyserver.com) as a managed container, `hangar-proxy`, publishing ports 80 and 443. Hangar writes Caddy's whole configuration from its own state and reloads it gracefully, so in-flight requests finish. Caddy's admin API is never exposed outside the container. Certificates live in the `hangar-proxy-data` volume.

| Flag | Default | |
|---|---|---|
| `--proxy` | `true` | Set to `false` to run without the proxy (domains are then refused) |
| `--proxy-http-port` / `--proxy-https-port` | `80` / `443` | Host ports, if something else already uses 80/443 |
| `--acme-email` | | Email for Let's Encrypt certificate notices |

If the proxy can't start (say, port 80 is taken), the server still runs, and `diagnose` reports `proxy_down` for apps with domains.

## Zero-downtime deploys

When an app has no `host_port` and no volumes, an update never interrupts traffic:

1. The new version starts **next to** the old one.
2. Hangar waits for it to be healthy: `health.path` answers 2xx/3xx, or without a path, the port accepts connections.
3. The proxy switches to the new version.
4. The old version gets 10 seconds to finish its requests, then stops.

**If the new version never becomes healthy, nothing changes for users.** The new container is removed, the old one keeps serving, and the deploy fails with `health_check_failed` and `previous_version_running: true`. Its output is captured in the deploy log first, so `diagnose` can still explain the failure:

```txt
App web: healthy=true state=running restarts=0
  [warning] new_version_unhealthy: latest deployment dep_3cc9f95c5aa5 never became healthy (health_check_failed), so traffic stayed on the previous version: ...
  [warning] port_mismatch: the app seems to listen on port 3000 but the spec says port 80
      suggested fix: {"port":3000}
```

### When there is downtime

Two kinds of app can't run two versions side by side, so they use **stop and replace**, and their plans say `downtime: true`:

- apps with a **`host_port`**, because two containers can't bind the same host port. Use `domains` instead.
- apps with **volumes**, because two copies of something like a database must never write to the same data.

`hangar status` and the dashboard show which strategy each app uses, and every deployment records it.
