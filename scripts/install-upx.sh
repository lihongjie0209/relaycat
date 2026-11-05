#!/bin/sh
set -eu

destination=${1:?provide an installation directory}
archive="$destination/upx-5.3.0-amd64_linux.tar.xz"
mkdir -p "$destination"
curl -fsSL 'https://github.com/upx/upx/releases/download/v5.3.0/upx-5.3.0-amd64_linux.tar.xz' -o "$archive"
printf '%s  %s\n' '79cea8a50b6fa102d10c7b79a419cd77dead3cc2d78661de2dc5e5815dc505c1' "$archive" | sha256sum -c -
tar -xJf "$archive" -C "$destination"
