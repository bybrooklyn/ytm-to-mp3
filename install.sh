#!/bin/sh
# Install the latest ytm release for Linux or macOS.
#   curl -fsSL https://raw.githubusercontent.com/bybrooklyn/ytm-to-mp3/main/install.sh | sh
# Pin a version:  YTM_VERSION=v1.2.3 sh install.sh
# Custom dir:     YTM_INSTALL_DIR=/usr/local/bin sh install.sh
#
# Integrity: this script verifies the archive against checksums.txt from the
# same release. Releases also ship checksums.txt.sig and checksums.txt.pem
# (cosign keyless, signed by the release workflow). To verify manually:
#   cosign verify-blob checksums.txt \
#     --signature checksums.txt.sig --certificate checksums.txt.pem \
#     --certificate-identity-regexp '^https://github.com/bybrooklyn/ytm-to-mp3/\.github/workflows/release\.yml@refs/tags/v' \
#     --certificate-oidc-issuer https://token.actions.githubusercontent.com
# then: sha256sum -c --ignore-missing checksums.txt
set -eu
repo="bybrooklyn/ytm-to-mp3"
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in linux|darwin) ;; *) echo "unsupported OS: $os (Windows: use install.ps1)" >&2; exit 1 ;; esac
case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) echo "unsupported CPU: $(uname -m)" >&2; exit 1 ;; esac

if command -v curl >/dev/null 2>&1; then
  dl() { curl --proto '=https' --tlsv1.2 -fsSL -o "$2" "$1"; }
  headers() { curl --proto '=https' --tlsv1.2 -sI "$1"; }
elif command -v wget >/dev/null 2>&1; then
  # GNU wget has --https-only; busybox wget does not (and only gets https URLs here).
  wopt=""
  if wget --help 2>&1 | grep -q -- '--https-only'; then wopt="--https-only"; fi
  dl() { wget -q $wopt -O "$2" "$1"; }
  headers() { wget -q $wopt --spider -S --max-redirect=0 "$1" 2>&1 || true; }
else
  echo "error: need curl or wget to download ytm; install one and retry" >&2
  exit 1
fi

tag=${YTM_VERSION:-}
if [ -z "$tag" ]; then
  # Resolve via the /releases/latest redirect (no API, no rate limit).
  tag=$(headers "https://github.com/$repo/releases/latest" | tr -d '\r' | sed -n 's/^[Ll]ocation:.*\/tag\/\([^/ ]*\)$/\1/p' | tail -n1)
  [ -n "$tag" ] || { echo "error: could not determine the latest release (check your network, or set YTM_VERSION=vX.Y.Z)" >&2; exit 1; }
fi
if ! printf '%s\n' "$tag" | grep -Eq '^v?[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$'; then
  echo "error: invalid version '$tag' (expected something like v1.2.3)" >&2
  exit 1
fi
case "$tag" in v*) ;; *) tag="v$tag" ;; esac
ver=${tag#v}
file="ytm_${ver}_${os}_${arch}.tar.gz"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT

echo "Downloading ytm $tag ($os/$arch)..."
dl "https://github.com/$repo/releases/download/$tag/$file" "$tmp/$file" || { echo "error: download failed for $tag ($file); does that release exist?" >&2; exit 1; }
dl "https://github.com/$repo/releases/download/$tag/checksums.txt" "$tmp/checksums.txt" || { echo "error: could not download checksums.txt for $tag" >&2; exit 1; }
want=$(grep " $file\$" "$tmp/checksums.txt" | cut -d' ' -f1)
if command -v sha256sum >/dev/null 2>&1; then got=$(sha256sum "$tmp/$file" | cut -d' ' -f1); else got=$(shasum -a 256 "$tmp/$file" | cut -d' ' -f1); fi
[ -n "$want" ] && [ "$want" = "$got" ] || { echo "error: checksum mismatch, refusing to install" >&2; exit 1; }

tar -xzf "$tmp/$file" -C "$tmp" ytm
dest="${YTM_INSTALL_DIR:-$HOME/.local/bin}"
mkdir -p "$dest"
install -m 0755 "$tmp/ytm" "$dest/ytm"
echo "Installed to $dest/ytm"
case ":$PATH:" in
  *":$dest:"*) ;;
  *)
    case "${SHELL:-}" in
      */zsh) rc="~/.zshrc" ;;
      */bash) rc="~/.bashrc" ;;
      */fish) rc="~/.config/fish/config.fish (use: fish_add_path $dest)" ;;
      *) rc="~/.profile" ;;
    esac
    echo "$dest is not on your PATH. Add this to $rc:"
    echo "  export PATH=\"$dest:\$PATH\""
    ;;
esac
echo
echo "Next: run  ytm setup"
