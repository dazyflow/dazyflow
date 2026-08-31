#!/usr/bin/env bash
# check-env-example.sh — fail if a DAZYFLOW_* knob the daemon reads is missing
# from .env.example.
#
# .env.example is not a sample; it is the configuration reference. `make env`
# (scripts/sync-env.sh) copies from it, the Dockerfile calls it "the full
# catalogue", and README/DEPLOY send operators to it. A knob that never reaches
# it is a knob that effectively does not exist.
#
# It had drifted to fifteen missing before anyone noticed, and the ones that hurt
# were the ones with a DOCUMENTED SIBLING — DAZYFLOW_TRUSTED_PROXIES absent while
# DAZYFLOW_TRUST_PROXY_HEADERS is in three places, three of six DAZYFLOW_FREE_*
# absent while the README says "the DAZYFLOW_FREE_* knobs". A reader who finds
# five of six concludes the list is complete. Hence a gate rather than a habit.
#
# Scope, and why it is narrow:
#   * DAZYFLOW_* only. DZCTL_*, DZ_TEST_*, FLOWGEN_* and OTEL_* are client,
#     test-harness and exporter-standard variables, not daemon configuration.
#   * Test files are skipped: a t.Setenv in a _test.go is not a deployment knob.
#   * The EXEMPT list below is for the handful that are genuinely not operator
#     configuration. Add to it deliberately, with the reason on the line.
#
# Usage: scripts/check-env-example.sh [env-example-path]
set -euo pipefail

example="${1:-.env.example}"
[ -f "$example" ] || { echo "check-env-example: $example not found" >&2; exit 1; }

# Not deployment configuration; each is read for a different reason.
EXEMPT="
DAZYFLOW_API_KEY        dzctl/dz-mcp client credential, not a daemon knob
DAZYFLOW_URL            dzctl/dz-mcp client target
DAZYFLOW_TENANT         dzctl client scope
DAZYFLOW_WORKSPACE      dzctl client scope
DAZYFLOW_TEST_DB        CI/test harness only
DAZYFLOW_TEST_MYSQL     CI/test harness only
DAZYFLOW_DEV_KEY        make dev only; documented in README's Development section
"

exempt() { echo "$EXEMPT" | awk '{print $1}' | grep -qx "$1"; }

missing=0
# Every way the daemon reads an env var. envStr/envInt/envBool/envDuration are
# cmd/dzd's own helpers; os.Getenv/LookupEnv covers the packages below it.
# git ls-files rather than a recursive grep: web/node_modules contains Go
# sources (flatted ships a Go port beside its JS), and the tracked file list is
# both the correct scope and faster.
used=$(git ls-files '*.go' 2>/dev/null \
	| grep -v '_test\.go$' \
	| xargs grep -hoE '(os\.Getenv|os\.LookupEnv|envStr|envInt|envBool|envDuration|envDefault)\(\s*"DAZYFLOW_[A-Z0-9_]+"' \
	| grep -oE 'DAZYFLOW_[A-Z0-9_]+' | sort -u)

for v in $used; do
	exempt "$v" && continue
	grep -qE "^#?\s*${v}=" "$example" || { echo "  $v"; missing=1; }
done

if [ "$missing" -ne 0 ]; then
	cat >&2 <<'MSG'

check-env-example: the knobs above are read by the daemon and absent from
.env.example. Add each one under a `# ---- Section ----` heading with what it
does and its default, or add it to EXEMPT in this script with a reason.
MSG
	exit 1
fi
echo "check-env-example: every DAZYFLOW_* knob the daemon reads is documented."
