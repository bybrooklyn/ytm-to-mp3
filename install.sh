#!/bin/sh
# Install the latest ytm release for Linux or macOS.
#   curl -fsSL https://raw.githubusercontent.com/bybrooklyn/ytm-to-mp3/main/install.sh | sh
set -eu
repo="bybrooklyn/ytm-to-mp3"
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in linux|darwin) ;; *) echo "unsupported OS: $os (Windows: use install.ps1)" >&2; exit 1 ;; esac
case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) echo "unsupported CPU: $(uname -m)" >&2; exit 1 ;; esac

tag=$(curl -fsSL "https://api.github.com/repos/$repo/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)
[ -n "$tag" ] || { echo "could not find the latest release" >&2; exit 1; }
ver=${tag#v}
file="ytm_${ver}_${os}_${arch}.tar.gz"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT

echo "Downloading ytm $tag ($os/$arch)..."
curl -fsSL -o "$tmp/$file" "https://github.com/$repo/releases/download/$tag/$file"
curl -fsSL -o "$tmp/checksums.txt" "https://github.com/$repo/releases/download/$tag/checksums.txt"
want=$(grep " $file\$" "$tmp/checksums.txt" | cut -d' ' -f1)
if command -v sha256sum >/dev/null; then got=$(sha256sum "$tmp/$file" | cut -d' ' -f1); else got=$(shasum -a 256 "$tmp/$file" | cut -d' ' -f1); fi
[ -n "$want" ] && [ "$want" = "$got" ] || { echo "checksum mismatch" >&2; exit 1; }

tar -xzf "$tmp/$file" -C "$tmp" ytm
dest="${YTM_INSTALL_DIR:-$HOME/.local/bin}"
mkdir -p "$dest"
install -m 0755 "$tmp/ytm" "$dest/ytm"
echo "Installed to $dest/ytm"
case ":$PATH:" in *":$dest:"*) ;; *) echo "Add $dest to your PATH to run 'ytm'." ;; esac
