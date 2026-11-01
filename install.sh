#!/bin/sh
# Install Coldship from GitHub Releases.
#
#   curl -fsSL https://raw.githubusercontent.com/dunkbing/coldship/main/install.sh | sh
#
# COLDSHIP_VERSION=0.2.0 installs a specific version; COLDSHIP_INSTALL_DIR
# changes where it goes (default /usr/local/bin).
set -eu

repo="dunkbing/coldship"
dir="${COLDSHIP_INSTALL_DIR:-/usr/local/bin}"

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
  sudo mv "$tmp/$asset" "$dir/coldship"
fi
echo "Installed $("$dir/coldship" --version)"
