# Security

Coldship controls Docker on the machine it runs on, so security issues matter. Thank you for reporting them responsibly.

## Reporting a vulnerability

**Please don't open a public issue.** Report privately through GitHub instead:
[Security → Report a vulnerability](https://github.com/dunkbing/coldship/security/advisories/new).

Include what you can: the version (`coldship --version`), what an attacker needs (a deploy token, a malicious Git repository, network access to the webhook endpoint, …), steps to reproduce, and the impact.

You'll get a response within a few days. Fixes ship as a new release, which Coldship servers install automatically for patch versions. Reporters are credited unless they prefer otherwise.

## Supported versions

Only the latest release gets security fixes.

## Scope and threat model

These are in scope:

- A token holder doing more than its scope allows (`read` < `deploy` < `admin`), for example a deploy-scope agent approving its own destructive production changes or reading secret values
- Escaping from an app, a Git build or a webhook onto the host, or injecting into Docker, Git or proxy configuration
- Reading stored secrets without the key, or secrets leaking into API responses, logs or the dashboard
- Webhook requests accepted without a valid signature
- Cross-site attacks on the dashboard

By design, these are not vulnerabilities:

- A deploy-scope token can run any container image, and a Dockerfile runs code during its build. So a deploy token can read the env (including secrets) of every app it can deploy, by deploying an image that prints it. Treat deploy tokens like deploy access, and revoke one you no longer trust with `coldship token revoke`. Tokens scoped to single apps are planned.
- Anyone with root, or with access to the Docker socket on the host, can read app env values (`docker inspect`). Encryption at rest protects the state file, not a compromised host.
- The default key file next to the database protects leaked copies of the database file only. See [Secrets encryption](website/src/content/docs/guides/secrets.md).
