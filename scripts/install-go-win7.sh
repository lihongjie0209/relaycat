#!/usr/bin/env bash
set -euo pipefail

readonly version="1.26.5"
readonly archive_sha256="aaec2901eb40be5068edce46267c4a287f2ac38cbfd9bbcb76f0320df7168e2d"
readonly archive_url="https://github.com/XTLS/go-win7/releases/download/patched-${version}/go-for-win7-linux-amd64.zip"

if [[ $# -ne 1 || -z "$1" ]]; then
  echo "usage: $0 DESTINATION" >&2
  exit 2
fi

destination="$1"
archive="${destination}.zip"

mkdir -p "$destination"
curl --fail --location --retry 3 --output "$archive" "$archive_url"
echo "${archive_sha256}  ${archive}" | sha256sum --check
unzip -qo "$archive" -d "$destination"

actual_version=$("${destination}/bin/go" version)
case "$actual_version" in
  *"go${version}"*) ;;
  *)
    echo "unexpected patched Go version: ${actual_version}" >&2
    exit 1
    ;;
esac

echo "$actual_version"
