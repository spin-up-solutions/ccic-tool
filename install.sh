#!/bin/sh
#
# ccic installer.
#
#   curl -fsSL https://raw.githubusercontent.com/spin-up-solutions/ccic-tool/main/install.sh | sh
#
# Environment:
#   CCIC_VERSION       tag to install (default: latest release)
#   CCIC_INSTALL_DIR   where to put the binary (default: first writable of
#                      /usr/local/bin, $HOME/.local/bin — sudo is used for
#                      /usr/local/bin only if it is not already writable)
set -eu

REPO="spin-up-solutions/ccic-tool"
BIN="ccic"

info() { printf '\033[36mccic:\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[31mccic: %s\033[0m\n' "$*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required but not installed"; }
need curl
need tar

# ---- platform ---------------------------------------------------------------
os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$arch" in
  x86_64|amd64)  arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) die "unsupported architecture: $arch" ;;
esac
case "$os" in
  darwin|linux) ;;
  *) die "unsupported OS: $os (ccic builds for macOS and Linux)" ;;
esac

# ---- version ----------------------------------------------------------------
version="${CCIC_VERSION:-}"
if [ -z "$version" ]; then
  info "looking up the latest release"
  version="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | sed -n 's/.*"tag_name" *: *"\([^"]*\)".*/\1/p' | head -1)"
  [ -n "$version" ] || die "could not determine the latest release of $REPO"
fi
info "installing $version for ${os}/${arch}"

# ---- download and verify ----------------------------------------------------
asset="${BIN}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$version"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

curl -fsSL "$base/$asset"        -o "$tmp/$asset"        || die "could not download $asset"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt" || die "could not download checksums.txt"

expected="$(grep " $asset\$" "$tmp/checksums.txt" | awk '{print $1}')"
[ -n "$expected" ] || die "no checksum published for $asset — refusing to install"

if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$tmp/$asset" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  actual="$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')"
else
  die "need sha256sum or shasum to verify the download"
fi
[ "$actual" = "$expected" ] || die "checksum mismatch for $asset — refusing to install"

tar -xzf "$tmp/$asset" -C "$tmp" "$BIN" || die "could not extract $BIN from $asset"
chmod 0755 "$tmp/$BIN"

# ---- install ----------------------------------------------------------------
install_to() {
  dir="$1"
  [ -d "$dir" ] || mkdir -p "$dir" 2>/dev/null || return 1
  if [ -w "$dir" ]; then
    mv "$tmp/$BIN" "$dir/$BIN"
  elif command -v sudo >/dev/null 2>&1; then
    info "$dir needs elevated permissions"
    sudo mv "$tmp/$BIN" "$dir/$BIN" || return 1
  else
    return 1
  fi
  target="$dir/$BIN"
}

target=""
if [ -n "${CCIC_INSTALL_DIR:-}" ]; then
  install_to "$CCIC_INSTALL_DIR" || die "could not install into $CCIC_INSTALL_DIR"
else
  install_to /usr/local/bin || install_to "$HOME/.local/bin" \
    || die "could not find a writable install directory — set CCIC_INSTALL_DIR"
fi

printf '\033[32mccic:\033[0m installed %s to %s\n' "$version" "$target" >&2

case ":$PATH:" in
  *":$(dirname "$target"):"*) ;;
  *) info "note: $(dirname "$target") is not on your PATH" ;;
esac

"$target" --version
info "next: cd into a project and run 'ccic init'"
