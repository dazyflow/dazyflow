#!/usr/bin/env bash
# update-sheetjs.sh — refresh the vendored SheetJS build and regenerate the
# Excel drops.
#
# SheetJS is vendored (not fetched at build time) so the daemon build stays
# hermetic and offline. This is the ONLY supported way to update it: it pins an
# exact version + sha256, fetches the standalone full build from the SheetJS
# CDN, verifies the hash, then regenerates excel_read.ts / excel_write.ts (and
# manifests.json) via `go generate ./officialdrops`.
#
# To move to a new SheetJS release, bump VERSION + SHA256 below first (get the
# hash from `curl -sL <URL> | sha256sum`), then run this.

set -euo pipefail

VERSION="0.20.3"
SHA256="cc015130aa8521e7f088f88898eba949ccdcbfb38df0bd129b44b7273c3a6f41"
URL="https://cdn.sheetjs.com/xlsx-${VERSION}/package/dist/xlsx.full.min.js"

cd "$(dirname "$0")"   # officialdrops/excelsrc
dest="xlsx.full.min.js"
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

echo "fetching SheetJS ${VERSION}"
echo "  ${URL}"
curl -fsSL --max-time 60 "$URL" -o "$tmp"

got="$(sha256sum "$tmp" | cut -d' ' -f1)"
if [ "$got" != "$SHA256" ]; then
	echo "sha256 mismatch — refusing to vendor an unexpected build:" >&2
	echo "  expected $SHA256" >&2
	echo "  got      $got" >&2
	echo "If you are intentionally bumping the version, update VERSION + SHA256 in this script." >&2
	exit 1
fi

mv "$tmp" "$dest"
echo "vendored ${dest} (sha256 verified)"

echo "regenerating Excel drops + manifests…"
( cd ../.. && go generate ./officialdrops )
echo "done. Review and commit: excelsrc/${dest}, ../excel_read.ts, ../excel_write.ts, ../manifests.json"
