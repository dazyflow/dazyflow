#!/usr/bin/env bash
# govulncheck.sh — run govulncheck and fail on any symbol-level finding that is
# not on the reviewed ALLOW list below. Arguments pass straight through, e.g.
#   scripts/govulncheck.sh ./...
#   scripts/govulncheck.sh -mode binary /tmp/vulnbin/dzd
#
# Why a wrapper: govulncheck has no suppression mechanism, and the Go vuln DB
# sometimes lists an advisory with no fixed version even after the fix has
# shipped. Without a wrapper, CI would stay red until someone upstream corrected
# the DB entry.
#
# Each entry pins an advisory to one EXACT module@version. That way a bump
# forces a fresh look instead of silently carrying the exemption forward. Every
# entry needs a reason that someone else can verify, and should be removed once
# the DB records a fixed version.
#
# GV overrides the binary (CI installs a pinned govulncheck into GOPATH/bin).
set -euo pipefail

ALLOW=(
  # excelize v2.11.0 already contains the fix (qax-os/excelize 93f0b3ca, #2331):
  # xlsxC.getValueFrom rejects `xlsxSI < 0` (cell.go:626). The DB entry has no
  # fixed version, so every release is flagged.
  "GO-2026-6452 github.com/xuri/excelize/v2@v2.11.0"
)

GV="${GV:-$(go env GOPATH)/bin/govulncheck}"
out="$(mktemp)"
trap 'rm -f "$out"' EXIT

# Exit status is ignored here: with -format json, govulncheck exits 0 on
# findings and non-zero only on a scan error, which `set -e` catches below.
"$GV" -format json "$@" >"$out"

# Symbol-level findings only (trace[0].function set). Module and package
# findings are code we never call, matching govulncheck's own text-mode gate.
mapfile -t found < <(jq -r '
  select(.finding and .finding.trace[0].function)
  | .finding as $f | $f.trace[0]
  | "\($f.osv) \(.module)@\(.version)"' "$out" | sort -u)

is_allowed() {
  local a
  for a in "${ALLOW[@]}"; do [[ "$a" == "$1" ]] && return 0; done
  return 1
}

fail=0
for f in "${found[@]}"; do
  if is_allowed "$f"; then
    echo "allowed (see scripts/govulncheck.sh): $f"
  else
    echo "VULNERABLE: $f  https://pkg.go.dev/vuln/${f%% *}"
    jq -r --arg id "${f%% *}" '
      select(.finding.osv == $id and .finding.trace[0].function)
      | .finding.trace | .[0].module as $m | first(.[] | select(.module != $m))
      | "    called from \(.position.filename // .package):\(.position.line // "?") \(.function)"' "$out" | sort -u
    fail=1
  fi
done

[[ ${#found[@]} -eq 0 ]] && echo "govulncheck: no symbol-level findings"
exit "$fail"
