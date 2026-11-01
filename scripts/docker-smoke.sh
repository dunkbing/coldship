#!/usr/bin/env bash
# End-to-end check against real Docker: deploys, redeploys, databases,
# snapshots and deletes through the CLI. Needs Docker and bin/coldship
# (make build). Uses apps prefixed "smoke-" and port 7071.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
H="$ROOT/bin/coldship"
WORK=$(mktemp -d)
PORT=7071
export COLDSHIP_URL="http://127.0.0.1:$PORT" COLDSHIP_NO_UPDATE_NOTICE=1

cleanup() {
  [ -n "${SERVER_PID:-}" ] && kill "$SERVER_PID" 2>/dev/null || true
  docker ps -aq --filter label=coldship.app --filter name=coldship-smoke- | xargs -r docker rm -f >/dev/null
  docker rm -f coldship-smoke-squatter >/dev/null 2>&1 || true
  docker volume ls -q --filter name=coldship-smoke- | xargs -r docker volume rm >/dev/null 2>&1 || true
}
trap cleanup EXIT
fail() { echo "FAIL: $*" >&2; echo "--- server log" >&2; tail -50 "$WORK/server.log" >&2; exit 1; }

"$H" server --addr "127.0.0.1:$PORT" --data "$WORK/coldship.db" --proxy=false --startup-grace 2s 2> "$WORK/server.log" &
SERVER_PID=$!
for _ in $(seq 50); do curl -sf "$COLDSHIP_URL/healthz" >/dev/null && break; sleep 0.2; done
export COLDSHIP_TOKEN=$(grep -o 'cst_[0-9a-f]*' "$WORK/server.log" | head -1)
[ -n "$COLDSHIP_TOKEN" ] || fail "no admin token in the server log"

spec() { printf '%s' "$2" > "$WORK/$1.yaml"; echo "$WORK/$1.yaml"; }

echo "==> zero-downtime deploy, then redeploy"
WEB=$(spec web $'name: smoke-web\nimage: nginx:1.27-alpine\nport: 80\n')
"$H" deploy -f "$WEB" --reason smoke >/dev/null || fail "deploy smoke-web"
"$H" deploy -f "$WEB" --reason smoke --redeploy >/dev/null || fail "redeploy smoke-web"
labels=$(docker inspect -f '{{index .Config.Labels "coldship.app"}}|{{index .Config.Labels "coldship.db"}}' coldship-smoke-web)
[ "$labels" = "smoke-web|" ] || fail "app container labels read as '$labels', want 'smoke-web|'"
[ -z "$(docker ps -aq --filter name='^coldship-smoke-web-next$')" ] || fail "-next container left behind"

echo "==> zero-config deploy from flags, then change one env var"
"$H" deploy --name smoke-flags --image nginx:1.27-alpine --port 80 --env SECRET=s3cret --env MODE=a >/dev/null || fail "flag deploy"
"$H" deploy --name smoke-flags --env MODE=b >/dev/null || fail "flag env update"
env=$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' coldship-smoke-flags)
echo "$env" | grep -qx 'SECRET=s3cret' || fail "secret lost by an env-only update"
echo "$env" | grep -qx 'MODE=b' || fail "env update not applied"
"$H" export smoke-flags > "$WORK/exported.yaml" || fail "export"
grep -q 'SECRET: <redacted>' "$WORK/exported.yaml" || fail "export should redact secrets"
"$H" deploy -f "$WORK/exported.yaml" --redeploy >/dev/null || fail "deploy the exported spec"
docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' coldship-smoke-flags | grep -qx 'SECRET=s3cret' ||
  fail "deploying an exported spec must keep redacted secrets"

echo "==> recreate deploy (host_port)"
HP=$(spec hp $'name: smoke-hp\nimage: nginx:1.27-alpine\nport: 80\nhost_port: 18080\n')
"$H" deploy -f "$HP" --reason smoke >/dev/null || fail "deploy smoke-hp"
"$H" deploy -f "$HP" --reason smoke --redeploy >/dev/null || fail "redeploy smoke-hp"
curl -sf http://127.0.0.1:18080/ >/dev/null || fail "smoke-hp not serving on its host port"

echo "==> postgres and redis, snapshots"
DB=$(spec db $'name: smoke-db\nimage: nginx:1.27-alpine\nport: 80\ndatabases:\n  - name: main\n    engine: postgres\n  - name: cache\n    engine: redis\n')
"$H" deploy -f "$DB" --reason smoke >/dev/null || fail "deploy smoke-db"
"$H" db snapshot smoke-db main --reason smoke >/dev/null || fail "postgres snapshot"
"$H" db snapshot smoke-db cache --reason smoke >/dev/null || fail "redis snapshot"
ps -eo args | grep -v grep | grep -q 'REDISCLI_AUTH=' && fail "redis password visible in the process list"

echo "==> a container Coldship doesn't own is left alone"
docker run -d --name coldship-smoke-squatter busybox sleep 300 >/dev/null
SQ=$(spec sq $'name: smoke-squatter\nimage: nginx:1.27-alpine\nport: 80\nhost_port: 18081\n')
if "$H" deploy -f "$SQ" --reason smoke >/dev/null 2>&1; then fail "deploy replaced an unlabeled container"; fi
docker inspect coldship-smoke-squatter >/dev/null 2>&1 || fail "unlabeled container was removed"

echo "==> delete"
for app in smoke-web smoke-flags smoke-hp smoke-db; do
  id=$("$H" plan --delete "$app" --json | python3 -c 'import sys,json;print(json.load(sys.stdin)["id"])')
  "$H" apply "$id" --reason smoke >/dev/null || fail "delete $app"
done
[ -z "$(docker ps -aq --filter label=coldship.app=smoke-db)" ] || fail "smoke-db containers left after delete"
# Two manual snapshots, plus one of each database before the delete.
n=$(find "$WORK/coldship-snapshots/smoke-db" -type f ! -name '.*' | wc -l)
[ "$n" -ge 4 ] || fail "delete should have snapshotted both databases ($n snapshot files)"

echo "docker smoke test passed"
