#!/bin/sh
# Install Coldship:
#
#   curl -fsSL https://raw.githubusercontent.com/dunkbing/coldship/main/install.sh | sh
#
# On Linux this installs the server as a system service (installing Docker
# first if needed) and logs the CLI in. On macOS it installs only the CLI.
#
# Options, as environment variables:
#   COLDSHIP_CLI_ONLY=1       install only the coldship command, no server
#   COLDSHIP_VERSION=0.2.0    install a specific version
#   COLDSHIP_INSTALL_DIR=...  where the binary goes (default /usr/local/bin)
set -eu

repo="dunkbing/coldship"
dir="${COLDSHIP_INSTALL_DIR:-/usr/local/bin}"
data=/var/lib/coldship
url=http://127.0.0.1:7070

if [ -t 1 ]; then
  bold=$(printf '\033[1m'); green=$(printf '\033[32m'); yellow=$(printf '\033[33m'); dim=$(printf '\033[2m'); reset=$(printf '\033[0m')
else
  bold=; green=; yellow=; dim=; reset=
fi
step() { printf '%s==>%s %s\n' "$bold" "$reset" "$1"; }
ok() { printf '  %s✓%s %s\n' "$green" "$reset" "$1"; }
note() { printf '  %s!%s %s\n' "$yellow" "$reset" "$1"; }
die() { printf '\n%scoldship: %s%s\n' "$bold" "$1" "$reset" >&2; exit 1; }

# Root runs commands directly; everyone else through sudo.
as_root() {
  if [ "$(id -u)" = 0 ]; then "$@"; else sudo "$@"; fi
}

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) die "unsupported OS $(uname -s)" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) die "unsupported CPU $(uname -m)" ;;
esac

mode=server
if [ "${COLDSHIP_CLI_ONLY:-}" = 1 ] || [ "$os" != linux ]; then
  mode=cli
elif ! command -v systemctl >/dev/null 2>&1; then
  mode=cli
  note "no systemd here, so only the CLI is installed (run the server yourself with: coldship server)"
fi

printf '\n%sInstalling Coldship%s %s(%s/%s, %s)%s\n\n' "$bold" "$reset" "$dim" "$os" "$arch" "$( [ $mode = server ] && echo "server and CLI" || echo "CLI only")" "$reset"

if [ "$mode" = server ] && [ "$(id -u)" != 0 ]; then
  command -v sudo >/dev/null 2>&1 || die "installing the server needs root: run this as root, or install sudo"
  step "Asking for sudo (to install Docker and the service)"
  # sudo reads the password from the terminal, even when this script is piped.
  sudo -v || die "sudo is needed to install the server"
  ok "sudo ok"
fi

# --- Docker ---

docker_answers() { as_root docker version >/dev/null 2>&1; }

install_docker() {
  id=""
  [ -r /etc/os-release ] && id=$(. /etc/os-release && echo "${ID:-}")
  case "$id" in
    ol | rhel | rocky | almalinux | centos)
      # get.docker.com doesn't support these; Docker's RHEL-family repo does.
      as_root dnf -y -q install dnf-plugins-core
      as_root dnf config-manager --add-repo https://download.docker.com/linux/centos/docker-ce.repo
      as_root dnf -y -q install docker-ce docker-ce-cli containerd.io docker-buildx-plugin
      ;;
    *)
      curl -fsSL https://get.docker.com | as_root sh
      ;;
  esac
}

if [ "$mode" = server ]; then
  step "Docker"
  if ! command -v docker >/dev/null 2>&1; then
    printf '  installing Docker Engine (this takes a minute)...\n'
    install_docker >/tmp/coldship-docker-install.log 2>&1 ||
      die "Docker couldn't be installed automatically (log: /tmp/coldship-docker-install.log). Install it (https://docs.docker.com/engine/install/), then run this again."
    ok "Docker installed"
  fi
  if ! docker_answers; then
    as_root systemctl enable --now docker >/dev/null 2>&1 || true
    i=0
    while ! docker_answers && [ $i -lt 30 ]; do sleep 1; i=$((i + 1)); done
    docker_answers || die "Docker is installed but won't start. Check: sudo systemctl status docker"
  fi
  ok "Docker $(as_root docker version --format '{{.Server.Version}}' 2>/dev/null) is running"
fi

# --- the coldship binary ---

if [ -n "${COLDSHIP_DOWNLOAD_BASE:-}" ]; then
  base="$COLDSHIP_DOWNLOAD_BASE"   # for testing a release before publishing
elif [ -n "${COLDSHIP_VERSION:-}" ]; then
  base="https://github.com/$repo/releases/download/v${COLDSHIP_VERSION#v}"
else
  base="https://github.com/$repo/releases/latest/download"
fi
asset="coldship-$os-$arch"

step "Coldship"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/$asset" "$base/$asset" || die "download failed: $base/$asset"
curl -fsSL -o "$tmp/SHA256SUMS" "$base/SHA256SUMS" || die "download failed: $base/SHA256SUMS"
(
  cd "$tmp"
  grep " $asset\$" SHA256SUMS > expected || exit 1
  if command -v sha256sum >/dev/null; then sha256sum -c expected >/dev/null; else shasum -a 256 -c expected >/dev/null; fi
) || die "checksum mismatch, not installing"
chmod +x "$tmp/$asset"
# Copy rather than move: a moved file keeps its /tmp SELinux label, and
# systemd may not run it (status 203/EXEC on Oracle Linux, RHEL, Fedora).
if [ -w "$dir" ]; then
  install -m 0755 "$tmp/$asset" "$dir/coldship"
else
  as_root mkdir -p "$dir"
  as_root install -m 0755 "$tmp/$asset" "$dir/coldship"
fi
relabel() {
  if command -v restorecon >/dev/null 2>&1; then as_root restorecon "$@" 2>/dev/null || true; fi
}
relabel "$dir/coldship"
version=$("$dir/coldship" --version | awk '{print $NF}')
ok "coldship $version installed to $dir/coldship"

if [ "$mode" = cli ]; then
  printf '\n%sDone.%s Connect to your server with: %scoldship login https://your-server%s\n\n' "$bold" "$reset" "$bold" "$reset"
  exit 0
fi

# --- the service ---

step "Service"
# A Coldship started by hand (coldship server in a terminal) would hold the
# port the service needs.
if ! as_root systemctl is-active --quiet coldship && curl -fsS "$url/healthz" >/dev/null 2>&1; then
  die "something already listens on 127.0.0.1:7070. If it's \`coldship server\` running in a terminal, stop it (Ctrl-C), then run this again."
fi
token_file="$data/admin-token"
fresh=no
as_root test -e "$data/coldship.db" || fresh=yes
as_root mkdir -p "$data"
as_root chmod 700 "$data"
cat > "$tmp/coldship.service" <<EOF
[Unit]
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
EOF
as_root install -m 0644 "$tmp/coldship.service" /etc/systemd/system/coldship.service
relabel /etc/systemd/system/coldship.service
as_root systemctl daemon-reload
as_root systemctl enable coldship >/dev/null 2>&1
as_root systemctl restart coldship
i=0
until as_root systemctl is-active --quiet coldship && curl -fsS "$url/healthz" >/dev/null 2>&1; do
  i=$((i + 1))
  if [ $i -ge 60 ]; then
    printf '\n  The service did not start. Its log:\n\n' >&2
    as_root journalctl -u coldship -n 15 --no-pager >&2 || true
    die "the service didn't start (full log: sudo journalctl -u coldship)"
  fi
  sleep 0.5
done
ok "running, and starts on boot (sudo systemctl status coldship)"

# --- log the CLI in ---

token=""
if [ "$fresh" = yes ] && as_root test -f "$token_file"; then
  token=$(as_root cat "$token_file")
  as_root rm -f "$token_file"
  step "CLI"
  # Log in as the person installing, also when the script runs under sudo.
  if [ "$(id -u)" = 0 ] && [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != root ]; then
    sudo -u "$SUDO_USER" -H "$dir/coldship" login "$url" --token "$token" >/dev/null
  else
    "$dir/coldship" login "$url" --token "$token" >/dev/null
  fi
  ok "logged in: coldship commands work right away"
fi

printf '\n%s%sColdship is ready.%s\n\n' "$bold" "$green" "$reset"
printf '  Deploy something:  %scoldship deploy --name hello --image nginx:1.27-alpine --port 80 --host-port 8080%s\n' "$bold" "$reset"
printf '  Dashboard:         %s%s%s %s(on your laptop: ssh -L 7070:127.0.0.1:7070 <this server>)%s\n' "$bold" "$url" "$reset" "$dim" "$reset"
if [ -n "$token" ]; then
  printf '\n  Admin token (shown once; for the dashboard and other machines, keep it in a password manager):\n\n    %s%s%s\n' "$bold" "$token" "$reset"
else
  printf '\n  %sExisting installation updated; your data and admin token are unchanged.%s\n' "$dim" "$reset"
fi
printf '\n  %s!%s Back up %s%s/coldship.key%s (it encrypts your secrets), away from this server.\n\n' "$yellow" "$reset" "$bold" "$data" "$reset"
