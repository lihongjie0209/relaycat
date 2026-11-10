#!/bin/sh
set -eu

repo='lihongjie0209/relaycat'
version=latest
install_dir=
package=auto

usage() {
  cat <<'EOF'
Usage: install.sh [--version vX.Y.Z] [--install-dir DIR] [--compact | --standard]

Install the latest Relaycat release for this Linux system. OpenWrt uses the
compact package by default where one is available. This installs only the
relaycat executable; it does not create or restart a service.
EOF
}

die() {
  printf 'relaycat installer: %s\n' "$*" >&2
  exit 1
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version|--install-dir)
      option=$1
      shift
      [ "$#" -gt 0 ] || die "missing value for $option"
      case "$option" in
        --version) version=$1 ;;
        --install-dir) install_dir=$1 ;;
      esac
      ;;
    --compact) package=compact ;;
    --standard) package=standard ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
  shift
done

[ "$(uname -s)" = Linux ] || die 'this script supports Linux and OpenWrt; use install.ps1 on Windows'
command -v sha256sum >/dev/null 2>&1 || die 'sha256sum is required'
command -v tar >/dev/null 2>&1 || die 'tar is required'

download() {
  url=$1
  destination=$2
  attempt=1
  while [ "$attempt" -le 3 ]; do
    if command -v curl >/dev/null 2>&1; then
      if curl -fsSL "$url" -o "$destination"; then return 0; fi
    elif command -v wget >/dev/null 2>&1; then
      if wget -O "$destination" "$url"; then return 0; fi
    else
      die 'curl or wget is required'
    fi
    rm -f "$destination"
    attempt=$((attempt + 1))
    if [ "$attempt" -le 3 ]; then sleep "$attempt"; fi
  done
  die "download failed: $url"
}

workdir=$(mktemp -d "${TMPDIR:-/tmp}/relaycat-install.XXXXXX") || die 'could not create a temporary directory'
staged=
cleanup() {
  if [ -n "$staged" ]; then rm -f "$staged"; fi
  rm -rf "$workdir"
}
trap cleanup 0
trap 'exit 1' 1 2 3 15

if [ "$version" = latest ]; then
  download "https://api.github.com/repos/$repo/releases/latest" "$workdir/release.json"
  version=$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$workdir/release.json" | head -n 1)
fi
printf '%s\n' "$version" | LC_ALL=C grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+([-+][A-Za-z0-9.-]+)?$' || die "invalid release version: $version"

is_openwrt=false
if [ -f /etc/openwrt_release ]; then is_openwrt=true; fi

case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  mipsel|mipsle) arch=mipsle ;;
  mips)
    if command -v opkg >/dev/null 2>&1 && opkg print-architecture | grep -Eq 'mips(el|le)'; then
      arch=mipsle
    else
      die 'MIPS endianness could not be confirmed; only little-endian MIPS is supported'
    fi
    ;;
  *) die "unsupported CPU architecture: $(uname -m)" ;;
esac

if [ "$package" = auto ]; then
  if [ "$is_openwrt" = true ] && [ "$arch" != amd64 ]; then
    package=compact
  else
    package=standard
  fi
fi
suffix=
if [ "$package" = compact ]; then
  case "$arch" in
    arm64|mipsle) suffix=_upx ;;
    *) die "no compact release is available for linux/$arch" ;;
  esac
fi

if [ -z "$install_dir" ]; then
  if [ "$is_openwrt" = true ]; then
    install_dir=/usr/bin
  elif [ "$(id -u)" -eq 0 ]; then
    install_dir=/usr/local/bin
  else
    [ -n "${HOME:-}" ] || die 'HOME is unset; pass --install-dir'
    install_dir=$HOME/.local/bin
  fi
fi

archive="relaycat_${version#v}_linux_${arch}${suffix}.tar.gz"
base="https://github.com/$repo/releases/download/$version"
download "$base/checksums.txt" "$workdir/checksums.txt"
download "$base/$archive" "$workdir/$archive"

expected=$(awk -v name="$archive" '$2 == name {print $1}' "$workdir/checksums.txt")
printf '%s\n' "$expected" | LC_ALL=C grep -Eq '^[0-9a-fA-F]{64}$' || die "checksum entry missing or ambiguous for $archive"
actual=$(sha256sum "$workdir/$archive" | awk '{print $1}')
[ "$actual" = "$expected" ] || die "SHA-256 mismatch for $archive"

tar -xzf "$workdir/$archive" -C "$workdir" relaycat
[ -f "$workdir/relaycat" ] && [ ! -L "$workdir/relaycat" ] || die 'archive does not contain a regular relaycat executable'
chmod 755 "$workdir/relaycat"
"$workdir/relaycat" version >/dev/null || die 'downloaded executable cannot run on this device'

mkdir -p "$install_dir"
staged=$(mktemp "$install_dir/.relaycat.XXXXXX") || die "cannot write to $install_dir"
cp "$workdir/relaycat" "$staged"
chmod 755 "$staged"
"$staged" version >/dev/null || die 'staged executable cannot run on this device'
mv -f "$staged" "$install_dir/relaycat"
staged=

printf 'Installed %s to %s/relaycat\n' "$version" "$install_dir"
"$install_dir/relaycat" version
case ":${PATH:-}:" in
  *":$install_dir:"*) ;;
  *) printf 'Add %s to PATH to run relaycat by name.\n' "$install_dir" ;;
esac
