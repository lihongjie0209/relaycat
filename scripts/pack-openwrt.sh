#!/bin/sh
set -eu

binary=${1:?provide the OpenWrt binary path}
upx --best --lzma "$binary"
upx -t "$binary"
size=$(wc -c < "$binary")
if [ "$size" -gt 5242880 ]; then
  printf 'packed OpenWrt binary exceeds 5 MiB: %s bytes\n' "$size" >&2
  exit 1
fi
