#!/usr/bin/env bash
# Regenerates the dashboard screenshots used by the docs website.
#
# Starts a throwaway Coldship server on :7272 with its own state file, seeds demo
# apps on the local Docker daemon, then drives the embedded dashboard with
# Playwright inside the official Playwright Docker image (so no browser
# libraries are needed on the host). Everything is cleaned up afterwards.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$ROOT/website/src/assets/screenshots"
PORT=7272
GIT_PORT=7273
PW_IMAGE="mcr.microsoft.com/playwright:v1.64.1-noble"
WORK="$(mktemp -d)"
H="$ROOT/bin/coldship"
export COLDSHIP_URL="http://127.0.0.1:$PORT"

cleanup() {
  [[ -n "${SERVER_PID:-}" ]] && kill "$SERVER_PID" 2>/dev/null || true
  [[ -n "${GIT_PID:-}" ]] && kill "$GIT_PID" 2>/dev/null || true
  docker images --format '{{.Repository}}:{{.Tag}}' | grep '^coldship-build/demo-' | xargs -r docker rmi -f >/dev/null 2>&1 || true
  docker rm -f coldship-proxy >/dev/null 2>&1 || true
  docker network rm coldship >/dev/null 2>&1 || true
  for app in web api worker site; do
    docker rm -f "coldship-demo-$app" >/dev/null 2>&1 || true
  done
  docker volume ls -q --filter name=coldship-demo- | xargs -r docker volume rm >/dev/null 2>&1 || true
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
docker build -q -t coldship-demo/worker:2 - >/dev/null <<'DOCKERFILE'
FROM alpine:3.20
CMD echo "worker v2 starting" && echo "Server listening on port 3000" && echo "fatal: DATABASE_URL is required but not set" && exit 1
DOCKERFILE

echo "==> creating a demo Git repository, served over HTTP"
REPO="$WORK/repo"
g() { git -C "$REPO" -c user.name="Demo Dev" -c user.email=dev@example.com "$@"; }
publish() { g push -q origin main; git -C "$WORK/git/site.git" update-server-info; }
git init -q -b main "$REPO"
printf '<h1>Acme</h1>\n' > "$REPO/index.html"
printf 'FROM nginx:1.27-alpine\nCOPY index.html /usr/share/nginx/html/index.html\n' > "$REPO/Dockerfile"
g add -A && g commit -q -m "Initial site"
mkdir -p "$WORK/git" && git clone -q --bare "$REPO" "$WORK/git/site.git" && git -C "$WORK/git/site.git" update-server-info
g remote add origin "$WORK/git/site.git"
# (cd + exec rather than --directory, which needs Python 3.7+)
(cd "$WORK/git" && exec python3 -m http.server "$GIT_PORT" --bind 127.0.0.1) >/dev/null 2>&1 &
GIT_PID=$!
wait_for() { for _ in $(seq 50); do curl -sf "$1" >/dev/null && return 0; sleep 0.2; done; echo "timed out waiting for $1" >&2; exit 1; }
wait_for "http://127.0.0.1:$GIT_PORT/site.git/HEAD"

echo "==> starting server"
"$H" server --addr "127.0.0.1:$PORT" --data "$WORK/coldship.db" --proxy=false --startup-grace 2s 2> "$WORK/server.log" &
SERVER_PID=$!
wait_for "$COLDSHIP_URL/healthz"
ADMIN=$(grep -o 'cst_[0-9a-f]*' "$WORK/server.log" | head -1)

AGENT=$(COLDSHIP_TOKEN=$ADMIN "$H" token create --name claude --scope deploy --json | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')
COLDSHIP_TOKEN=$ADMIN "$H" token create --name ci --scope deploy --json >/dev/null

echo "==> seeding demo apps as the 'claude' agent"
export COLDSHIP_TOKEN=$AGENT
ENV_WEB=$'port: 80\nhost_port: 18081\nmemory: 128m\nenv:\n  API_BASE_URL: https://api.example.com\n  SESSION_SECRET: s3cret\n'
"$H" deploy -f "$(spec web staging nginx:1.27-alpine "$ENV_WEB")" --reason "Initial deploy of the marketing site" >/dev/null
"$H" deploy -f "$(spec web staging nginx:1.27-alpine "$ENV_WEB  FEATURE_NEW_NAV: \"true\"
")" --reason "Enable the new navigation (ticket WEB-142)" >/dev/null

API_V1=$'port: 80\nhost_port: 18082\nenv:\n  DATABASE_URL: postgres://api@db/api\nvolumes:\n  - name: uploads\n    path: /srv/uploads\n  - name: cache\n    path: /var/cache/api\n'
"$H" deploy -f "$(spec api production nginx:1.27-alpine "$API_V1")" --reason "Launch API v1" >/dev/null

"$H" deploy -f "$(spec worker staging nginx:1.27-alpine $'env:\n  QUEUE: default\n')" --reason "Deploy background worker" >/dev/null
"$H" deploy -f "$(spec worker staging coldship-demo/worker:2 $'port: 8080\nenv:\n  QUEUE: default\n')" --reason "Upgrade worker to v2" >/dev/null 2>&1 || true

SITE=$'port: 80\nhost_port: 18083\nbuild:\n  git: http://127.0.0.1:'"$GIT_PORT"$'/site.git\n  ref: main\n  args:\n    SITE_ENV: staging\n'
printf 'name: demo-site\nstage: staging\n%s' "$SITE" > "$WORK/site.yaml"
"$H" deploy -f "$WORK/site.yaml" --reason "Deploy the site from Git" >/dev/null
printf '<h1>Acme</h1><a href="/pricing">Pricing</a>\n' > "$REPO/index.html"
g commit -q -am "Add pricing link" && publish
"$H" deploy -f "$WORK/site.yaml" --reason "Ship pricing link" >/dev/null
printf 'FROM node:22-alpine AS assets\nWORKDIR /src\nCOPY . .\nRUN echo "building assets" && npm run build\nFROM nginx:1.27-alpine\nCOPY --from=assets /src/dist /usr/share/nginx/html\n' > "$REPO/Dockerfile"
g commit -q -am "Build assets with npm" && publish
"$H" deploy -f "$WORK/site.yaml" --reason "Switch to an npm asset build" >/dev/null 2>&1 || true

# Pending plans: one destructive production change awaiting approval, one routine.
API_V2=$'port: 80\nhost_port: 18082\nenv:\n  DATABASE_URL: postgres://api@db/api\nvolumes:\n  - name: uploads\n    path: /srv/uploads\n'
"$H" plan -f "$(spec api production nginx:1.27-alpine "$API_V2")" >/dev/null
"$H" plan -f "$(spec web staging nginx:1.28-alpine "$ENV_WEB  FEATURE_NEW_NAV: \"true\"
")" >/dev/null

echo "==> capturing screenshots"
mkdir -p "$OUT"
docker run --rm --network host --ipc host \
  -v "$ROOT/dashboard:/work:ro" -v "$OUT:/out" \
  -e COLDSHIP_URL -e COLDSHIP_TOKEN="$ADMIN" -e OUT=/out \
  --user "$(id -u):$(id -g)" -e HOME=/tmp \
  "$PW_IMAGE" node /work/scripts/screenshots.mjs

ls -1 "$OUT"
