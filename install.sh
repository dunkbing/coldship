#!/bin/sh
# Install Coldship from GitHub Releases.
#
#   On a server (installs the CLI and runs Coldship as a system service):
#     curl -fsSL https://raw.githubusercontent.com/dunkbing/coldship/main/install.sh | sh -s -- --service
#
#   On a laptop (just the CLI):
#     curl -fsSL https://raw.githubusercontent.com/dunkbing/coldship/main/install.sh | sh
#
# COLDSHIP_VERSION=0.2.0 installs a specific version; COLDSHIP_INSTALL_DIR
# changes where it goes (default /usr/local/bin).
set -eu

repo="dunkbing/coldship"
dir="${COLDSHIP_INSTALL_DIR:-/usr/local/bin}"
service=no
for arg in "$@"; do
  case "$arg" in
    --service) service=yes ;;
    *) echo "coldship: unknown option $arg (use --service to run Coldship as a system service)" >&2; exit 1 ;;
  esac
done

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) echo "coldship: unsupported OS $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "coldship: unsupported CPU $(uname -m)" >&2; exit 1 ;;
esac

# Root runs commands directly; everyone else through sudo.
as_root() {
  if [ "$(id -u)" = 0 ]; then "$@"; else sudo "$@"; fi
}

if [ "$service" = yes ]; then
  if [ "$os" != linux ] || ! command -v systemctl >/dev/null 2>&1; then
    echo "coldship: --service needs Linux with systemd" >&2; exit 1
  fi
  # The service runs as root, so only Docker itself has to be there.
  if ! command -v docker >/dev/null 2>&1; then
    echo "coldship: Docker isn't installed. Install it, then run this again:" >&2
    echo "  curl -fsSL https://get.docker.com | sudo sh" >&2
    exit 1
  fi
  if ! as_root docker version >/dev/null 2>&1; then
    echo "coldship: Docker is installed but not running. Start it, then run this again:" >&2
    echo "  sudo systemctl enable --now docker" >&2
    exit 1
  fi
fi

if [ -n "${COLDSHIP_DOWNLOAD_BASE:-}" ]; then
  base="$COLDSHIP_DOWNLOAD_BASE"   # for testing a release before publishing
elif [ -n "${COLDSHIP_VERSION:-}" ]; then
  base="https://github.com/$repo/releases/download/v${COLDSHIP_VERSION#v}"
else
  base="https://github.com/$repo/releases/latest/download"
fi
asset="coldship-$os-$arch"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
echo "Downloading $asset..."
curl -fsSL -o "$tmp/$asset" "$base/$asset"
curl -fsSL -o "$tmp/SHA256SUMS" "$base/SHA256SUMS"

(
  cd "$tmp"
  grep " $asset\$" SHA256SUMS > expected || { echo "coldship: $asset is not in SHA256SUMS" >&2; exit 1; }
  if command -v sha256sum >/dev/null; then sha256sum -c expected >/dev/null; else shasum -a 256 -c expected >/dev/null; fi
) || { echo "coldship: checksum mismatch, not installing" >&2; exit 1; }
chmod +x "$tmp/$asset"

if [ -w "$dir" ]; then
  mv "$tmp/$asset" "$dir/coldship"
else
  echo "Installing to $dir (needs sudo)..."
  as_root mv "$tmp/$asset" "$dir/coldship"
fi
echo "Installed $("$dir/coldship" --version)"

if [ "$service" = no ]; then
  # Running the server by hand needs a user that can use Docker.
  if [ "$os" = linux ] && command -v docker >/dev/null 2>&1 && ! docker version >/dev/null 2>&1; then
    echo
    echo "Note: this user can't use Docker, which \`coldship server\` needs. Either:"
    echo "  - run Coldship as a service instead:  curl -fsSL https://raw.githubusercontent.com/$repo/main/install.sh | sh -s -- --service"
    echo "  - or add yourself to the docker group: sudo usermod -aG docker \$USER, then log in again"
  fi
  exit 0
fi

# --- system service ---
data=/var/lib/coldship
token_file="$data/admin-token"
as_root mkdir -p "$data"
as_root chmod 700 "$data"
fresh=no
[ -e "$data/coldship.db" ] || fresh=yes

unit="[Unit]
Description=Coldship
After=network-online.target docker.service
Wants=network-online.target docker.service

[Service]
ExecStart=$dir/coldship server --data $data/coldship.db --admin-token-file $token_file
WorkingDirectory=$data
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
"
# A Coldship started by hand (coldship server in a terminal) would hold the
# port the service needs.
if ! as_root systemctl is-active --quiet coldship && curl -fsS http://127.0.0.1:7070/healthz >/dev/null 2>&1; then
  echo "coldship: something already listens on 127.0.0.1:7070. If it's \`coldship server\` running in a terminal, stop it (Ctrl-C), then run this again." >&2
  exit 1
fi

printf '%s' "$unit" > "$tmp/coldship.service"
as_root mv "$tmp/coldship.service" /etc/systemd/system/coldship.service
as_root chmod 644 /etc/systemd/system/coldship.service
as_root systemctl daemon-reload
as_root systemctl enable coldship >/dev/null 2>&1
as_root systemctl restart coldship

echo "Starting the Coldship service..."
ok=no
for _ in $(seq 60); do
  if as_root systemctl is-active --quiet coldship && curl -fsS http://127.0.0.1:7070/healthz >/dev/null 2>&1; then ok=yes; break; fi
  sleep 0.5
done
if [ "$ok" = no ]; then
  echo "coldship: the service didn't start. See: sudo journalctl -u coldship -n 50" >&2
  exit 1
fi

echo
echo "Coldship is running as a service (sudo systemctl status coldship) and starts on boot."
echo "Data: $data. Back up $data/coldship.key separately from the database."
if [ "$fresh" = yes ] && as_root test -f "$token_file"; then
  token="$(as_root cat "$token_file")"
  as_root rm -f "$token_file"
  echo
  echo "Your admin token (shown once, store it safely):"
  echo
  echo "  $token"
  echo
  echo "Use it with the CLI:"
  echo "  export COLDSHIP_TOKEN=$token"
  echo "  coldship deploy --name hello --image nginx:1.27-alpine --port 80 --host-port 8080"
else
  echo "Existing installation: use the admin token you saved before."
fi
echo
echo "Dashboard: http://127.0.0.1:7070 (on your laptop: ssh -L 7070:127.0.0.1:7070 <this server>)"
