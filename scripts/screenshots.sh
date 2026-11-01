#!/usr/bin/env bash
# Regenerates the dashboard screenshots used by the docs website.
#
# Starts a throwaway Hangar server on :7272 with its own state file, seeds demo
# apps on the local Docker daemon, then drives the embedded dashboard with
# Playwright inside the official Playwright Docker image (so no browser
# libraries are needed on the host). Everything is cleaned up afterwards.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$ROOT/website/src/assets/screenshots"
PORT=7272
PW_IMAGE="mcr.microsoft.com/playwright:v1.64.1-noble"
WORK="$(mktemp -d)"
H="$ROOT/bin/hangar"
export HANGAR_URL="http://127.0.0.1:$PORT"

cleanup() {
  [[ -n "${SERVER_PID:-}" ]] && kill "$SERVER_PID" 2>/dev/null || true
  for app in web api worker; do
    docker rm -f "hangar-demo-$app" >/dev/null 2>&1 || true
  done
  docker volume ls -q --filter name=hangar-demo- | xargs -r docker volume rm >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

# Demo apps use a "demo-" prefix so they never collide with real ones.
spec() { # name stage image [extra yaml]
  printf 'name: demo-%s\nstage: %s\nimage: %s\n%s' "$1" "$2" "$3" "${4:-}" > "$WORK/$1.yaml"
  echo "$WORK/$1.yaml"
}

echo "==> building"
make -C "$ROOT" build >/dev/null

echo "==> building demo image that crashes on start"
docker build -q -t hangar-demo/worker:2 - >/dev/null <<'DOCKERFILE'
FROM alpine:3.20
CMD echo "worker v2 starting" && echo "Server listening on port 3000" && echo "fatal: DATABASE_URL is required but not set" && exit 1
DOCKERFILE

echo "==> starting server"
"$H" server --addr "127.0.0.1:$PORT" --data "$WORK/hangar.db" --startup-grace 2s 2> "$WORK/server.log" &
SERVER_PID=$!
for _ in $(seq 50); do curl -sf "$HANGAR_URL/healthz" >/dev/null && break; sleep 0.2; done
ADMIN=$(grep -o 'hgr_[0-9a-f]*' "$WORK/server.log" | head -1)

AGENT=$(HANGAR_TOKEN=$ADMIN "$H" token create --name claude --scope deploy --json | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')
HANGAR_TOKEN=$ADMIN "$H" token create --name ci --scope deploy --json >/dev/null

echo "==> seeding demo apps as the 'claude' agent"
export HANGAR_TOKEN=$AGENT
ENV_WEB=$'port: 80\nhost_port: 18081\nmemory: 128m\nenv:\n  API_BASE_URL: https://api.example.com\n  SESSION_SECRET: s3cret\n'
"$H" deploy -f "$(spec web staging nginx:1.27-alpine "$ENV_WEB")" --reason "Initial deploy of the marketing site" >/dev/null
"$H" deploy -f "$(spec web staging nginx:1.27-alpine "$ENV_WEB  FEATURE_NEW_NAV: \"true\"
")" --reason "Enable the new navigation (ticket WEB-142)" >/dev/null

API_V1=$'port: 80\nhost_port: 18082\nenv:\n  DATABASE_URL: postgres://api@db/api\nvolumes:\n  - name: uploads\n    path: /srv/uploads\n  - name: cache\n    path: /var/cache/api\n'
"$H" deploy -f "$(spec api production nginx:1.27-alpine "$API_V1")" --reason "Launch API v1" >/dev/null

"$H" deploy -f "$(spec worker staging nginx:1.27-alpine $'env:\n  QUEUE: default\n')" --reason "Deploy background worker" >/dev/null
"$H" deploy -f "$(spec worker staging hangar-demo/worker:2 $'port: 8080\nenv:\n  QUEUE: default\n')" --reason "Upgrade worker to v2" >/dev/null 2>&1 || true

# Pending plans: one destructive production change awaiting approval, one routine.
API_V2=$'port: 80\nhost_port: 18082\nenv:\n  DATABASE_URL: postgres://api@db/api\nvolumes:\n  - name: uploads\n    path: /srv/uploads\n'
"$H" plan -f "$(spec api production nginx:1.27-alpine "$API_V2")" >/dev/null
"$H" plan -f "$(spec web staging nginx:1.28-alpine "$ENV_WEB  FEATURE_NEW_NAV: \"true\"
")" >/dev/null

echo "==> capturing screenshots"
mkdir -p "$OUT"
docker run --rm --network host --ipc host \
  -v "$ROOT/dashboard:/work:ro" -v "$OUT:/out" \
  -e HANGAR_URL -e HANGAR_TOKEN="$ADMIN" -e OUT=/out \
  --user "$(id -u):$(id -g)" -e HOME=/tmp \
  "$PW_IMAGE" node /work/scripts/screenshots.mjs

ls -1 "$OUT"
