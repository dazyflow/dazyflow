#!/usr/bin/env bash
# End-to-end AP-invoice demo. One graph receives webhook POSTs that
# represent "new invoice received", looks the invoice up via a mock API
# with a secret-injected Authorization header, branches on amount, and
# either auto-approves (low value) or pages the CFO (high value). Every
# invoice is archived to the workspace sandbox regardless.
#
# What's exercised:
#   - webhook trigger with per-graph secret
#   - http_request with env://-injected Authorization headers
#   - branch with field-path + numeric condition
#   - multi-output fan (fetch feeds both classify AND archive)
#   - per-tenant sandbox + quota
#   - audit: the saved graph JSON retains env:// references
set -euo pipefail

cd "$(dirname "$0")"
ROOT="$(cd ../.. && pwd)"

SANDBOX_BASE=$(mktemp -d)
HZD_LOG=/tmp/ap-hzd.log
BE_LOG=/tmp/ap-be.log

cleanup() {
    kill "${HZD_PID:-}" "${BE_PID:-}" 2>/dev/null || true
    wait 2>/dev/null || true
    rm -rf "$SANDBOX_BASE" /tmp/ap-hzd /tmp/ap-hzctl /tmp/ap-backend
    rm -f /tmp/ap-low.json /tmp/ap-high.json "$HZD_LOG" "$BE_LOG"
}
trap cleanup EXIT

# --- 1. build --------------------------------------------------------------
echo "[1/7] building binaries"
(cd "$ROOT" && go build -o /tmp/ap-hzd ./cmd/hzd)
(cd "$ROOT" && go build -o /tmp/ap-hzctl ./cmd/hzctl)
go build -o /tmp/ap-backend ./mock-backend

# --- 2. start mock backend ------------------------------------------------
echo "[2/7] starting mock backend on :60500"
/tmp/ap-backend --listen=:60500 > "$BE_LOG" 2>&1 &
BE_PID=$!
sleep 0.2

# --- 3. start hzd with secrets in env -------------------------------------
# /trigger/ paths land on the same HTTP listener as the API; we don't run
# a separate webhook port anymore. All hzd config goes via HAZYFLOW_*.
echo "[3/7] starting hzd (workers=2, http=:18080, sandbox=$SANDBOX_BASE)"
INVOICE_API_KEY="Bearer invoice-svc-key-abc" \
SLACK_TOKEN="Bearer slack-bot-token-def" \
APPROVAL_API_KEY="Bearer approval-system-key-ghi" \
HAZYFLOW_LISTEN=":50099" \
HAZYFLOW_HTTP=":18080" \
HAZYFLOW_DEV_KEY=1 \
HAZYFLOW_DATA_DIR="$SANDBOX_BASE" \
/tmp/ap-hzd \
    > "$HZD_LOG" 2>&1 &
HZD_PID=$!
sleep 0.4
TOKEN=$(grep -oE 'hzk_[a-z0-9_]+' "$HZD_LOG" | head -1)
grep -E "listening" "$HZD_LOG" | sed 's/^/      /'

# --- 4. save the two pipeline graphs --------------------------------------
echo "[4/7] saving graphs"
HZCTL_TOKEN=$TOKEN /tmp/ap-hzctl --server=localhost:50099 graph save pipeline-low.json > /dev/null
HZCTL_TOKEN=$TOKEN /tmp/ap-hzctl --server=localhost:50099 graph save pipeline-high.json > /dev/null

# --- 5. fire the low-value invoice via webhook ----------------------------
echo "[5/7] webhook → process-invoice-low (\$250 amount → auto-approve path)"
LOW_JOB=$(curl -s -X POST -H "Authorization: Bearer webhook-secret" \
    http://127.0.0.1:18080/trigger/dev/default/process-invoice-low | grep -oE '[a-f0-9]{20,}')
echo "      → job $LOW_JOB"
sleep 0.5

# --- 6. fire the high-value invoice ---------------------------------------
echo "[6/7] webhook → process-invoice-high (\$12,500 amount → CFO path)"
HIGH_JOB=$(curl -s -X POST -H "Authorization: Bearer webhook-secret" \
    http://127.0.0.1:18080/trigger/dev/default/process-invoice-high | grep -oE '[a-f0-9]{20,}')
echo "      → job $HIGH_JOB"
sleep 0.5

# --- 7. verify ------------------------------------------------------------
echo "[7/7] verifying outcomes"
echo
echo "--- Mock backend log (what each service actually received) ---"
sed 's/^/    /' "$BE_LOG"
echo

echo "--- low-invoice ($LOW_JOB) node trail ---"
HZCTL_TOKEN=$TOKEN /tmp/ap-hzctl --server=localhost:50099 job list process-invoice-low 2>&1 | sed 's/^/    /'
echo
echo "--- high-invoice ($HIGH_JOB) node trail ---"
HZCTL_TOKEN=$TOKEN /tmp/ap-hzctl --server=localhost:50099 job list process-invoice-high 2>&1 | sed 's/^/    /'

echo
echo "--- archived invoices on disk ---"
ls -1 "$SANDBOX_BASE/dev/default/archive/" 2>&1 | sed 's/^/    /'
echo
echo "--- archive/invoice-42.json contents ---"
cat "$SANDBOX_BASE/dev/default/archive/invoice-42.json" | sed 's/^/    /'
echo
echo "--- archive/invoice-big-99.json contents ---"
cat "$SANDBOX_BASE/dev/default/archive/invoice-big-99.json" | sed 's/^/    /'

echo
echo "--- audit: saved graph still references secrets symbolically ---"
HZCTL_TOKEN=$TOKEN /tmp/ap-hzctl --server=localhost:50099 graph load process-invoice-low 2>&1 \
    | grep -E '"Authorization"|"url"' | head -10 | sed 's/^/    /'

# --- assertions -----------------------------------------------------------
echo
echo "--- assertions ---"
errors=0
assert() {
    if eval "$2"; then
        echo "  [ok] $1"
    else
        echo "  [!!] $1"
        errors=$((errors + 1))
    fi
}
assert "low-invoice graph succeeded" \
    "HZCTL_TOKEN=$TOKEN /tmp/ap-hzctl --server=localhost:50099 job status $LOW_JOB 2>&1 | grep -q 'status:    succeeded'"
assert "high-invoice graph succeeded" \
    "HZCTL_TOKEN=$TOKEN /tmp/ap-hzctl --server=localhost:50099 job status $HIGH_JOB 2>&1 | grep -q 'status:    succeeded'"
assert "low invoice routed to auto_approve" \
    "HZCTL_TOKEN=$TOKEN /tmp/ap-hzctl --server=localhost:50099 job list process-invoice-low 2>&1 | grep auto_approve | grep -q succeeded"
assert "low invoice's notify_cfo was skipped" \
    "HZCTL_TOKEN=$TOKEN /tmp/ap-hzctl --server=localhost:50099 job list process-invoice-low 2>&1 | grep notify_cfo | grep -q skipped"
assert "high invoice routed to notify_cfo" \
    "HZCTL_TOKEN=$TOKEN /tmp/ap-hzctl --server=localhost:50099 job list process-invoice-high 2>&1 | grep notify_cfo | grep -q succeeded"
assert "high invoice's auto_approve was skipped" \
    "HZCTL_TOKEN=$TOKEN /tmp/ap-hzctl --server=localhost:50099 job list process-invoice-high 2>&1 | grep auto_approve | grep -q skipped"
assert "both invoices archived" \
    "test -f $SANDBOX_BASE/dev/default/archive/invoice-42.json && test -f $SANDBOX_BASE/dev/default/archive/invoice-big-99.json"
assert "mock backend saw correct API key for /invoices" \
    "! grep -q 'invoices: bad Authorization' $BE_LOG"
assert "no SSRF or auth refusals in backend log" \
    "! grep -q 'unauthorized' $BE_LOG"
assert "graph JSON still references env:// (resolved secrets not leaked)" \
    "HZCTL_TOKEN=$TOKEN /tmp/ap-hzctl --server=localhost:50099 graph load process-invoice-low 2>&1 | grep -q 'env://INVOICE_API_KEY'"

if [[ $errors -eq 0 ]]; then
    echo
    echo "[ok] all assertions passed"
else
    echo
    echo "[!!] $errors assertion(s) failed"
    exit 1
fi
