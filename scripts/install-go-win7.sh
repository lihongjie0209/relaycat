#!/usr/bin/env bash
set -euo pipefail

readonly version="1.26.7"
readonly release_url="https://github.com/XTLS/go-win7/releases/download/patched-${version}"
readonly sdk_sha256="3ec8549eab338720c1a891f37c6d2467e7205997554bf649527d083f9973b82d"
readonly legacy_sha256="43b0ce97dff850fa0804dc86aa1b978c51ec3a4a18f87f1660f04c06779b9118"

if [[ $# -ne 1 || -z "$1" ]]; then
  echo "usage: $0 DESTINATION" >&2
  exit 2
fi

destination="$1"
sdk_archive="${destination}-sdk.zip"
legacy_archive="${destination}-source-legacy.zip"

mkdir -p "$destination"
curl --fail --location --retry 3 --output "$sdk_archive" \
  "${release_url}/go-for-win7-linux-amd64.zip"
echo "${sdk_sha256}  ${sdk_archive}" | sha256sum --check
unzip -qo "$sdk_archive" -d "$destination"

# source-legacy adds the LoadLibrary fallback required by Windows 7 systems
# without KB2533623, as well as the legacy socket fallback.
curl --fail --location --retry 3 --output "$legacy_archive" \
  "${release_url}/source-legacy.zip"
echo "${legacy_sha256}  ${legacy_archive}" | sha256sum --check
unzip -qo "$legacy_archive" 'src/*' -d "$destination"

if ! grep -q 'var useLoadLibraryEx bool' "$destination/src/runtime/os_windows.go"; then
  echo "XTLS source-legacy runtime patch was not applied" >&2
  exit 1
fi
if ! grep -q 'func sysSocket' "$destination/src/net/sock_windows.go"; then
  echo "XTLS source-legacy socket patch was not applied" >&2
  exit 1
fi

actual_version=$("${destination}/bin/go" version)
case "$actual_version" in
  *"go${version}"*) ;;
  *)
    echo "unexpected patched Go version: ${actual_version}" >&2
    exit 1
    ;;
esac

echo "$actual_version"
