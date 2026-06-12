#!/usr/bin/env bash
# End-to-end AP-invoice demo. One graph receives webhook POSTs that
# represent "new invoice received", looks the invoice up via a mock API
# with a secret-injected Authorization header, branches on amount, and
# either auto-approves (low value) or pages the CFO (high value). Every
# invoice is archived to the workspace sandbox regardless.
#
# What's exercised:
#   - webhook trigger with per-graph secret
#   - http_request with secret://-injected Authorization headers
#   - branch with field-path + numeric condition
#   - multi-output fan (fetch feeds both classify AND archive)
#   - per-tenant sandbox + quota
#   - audit: the saved graph JSON retains secret:// references
set -euo pipefail

cd "$(dirname "$0")"
ROOT="$(cd ../.. && pwd)"

SANDBOX_BASE=$(mktemp -d)
HZD_LOG=/tmp/ap-hzd.log
BE_LOG=/tmp/ap-be.log

# Ephemeral Postgres for the encrypted secret store (secret://). Own
# container + port + strong password, so the demo is self-contained and
# never touches a local/production database.
PG_CONTAINER=ap-demo-pg
PG_PORT=55432
PG_PASS=ap-demo-not-a-real-password
PG_DSN="postgres://hazyflow:${PG_PASS}@localhost:${PG_PORT}/hazyflow?sslmode=disable"

cleanup() {
    kill "${HZD_PID:-}" "${BE_PID:-}" 2>/dev/null || true
    wait 2>/dev/null || true
    docker rm -f "$PG_CONTAINER" >/dev/null 2>&1 || true
    rm -rf "$SANDBOX_BASE" /tmp/ap-hzd /tmp/ap-hzctl /tmp/ap-backend
    rm -f /tmp/ap-low.json /tmp/ap-high.json "$HZD_LOG" "$BE_LOG"
}
trap cleanup EXIT

# --- 1. build --------------------------------------------------------------
echo "[1/7] building binaries"
(cd "$ROOT" && go build -o /tmp/ap-hzd ./cmd/hzd)
(cd "$ROOT" && go build -o /tmp/ap-hzctl ./cmd/hzctl)
go build -o /tmp/ap-backend ./mock-backend

# --- 2. start mock backend + ephemeral Postgres ---------------------------
echo "[2/7] starting mock backend on :60500"
/tmp/ap-backend --listen=:60500 > "$BE_LOG" 2>&1 &
BE_PID=$!
sleep 0.2

echo "      starting ephemeral Postgres (container $PG_CONTAINER on :$PG_PORT)"
docker rm -f "$PG_CONTAINER" >/dev/null 2>&1 || true
docker run -d --rm --name "$PG_CONTAINER" \
    -e POSTGRES_USER=hazyflow -e POSTGRES_PASSWORD="$PG_PASS" -e POSTGRES_DB=hazyflow \
    -p "$PG_PORT":5432 postgres:16-alpine >/dev/null
# The official postgres image restarts once after initdb, so pg_isready
# can report "up" during init and then reset the next connection. Probe
# with a real query (over the in-container socket) so we only proceed once
# the server truly accepts connections — hzd Fatal-exits on a failed
# connect, with no retry of its own.
echo -n "      waiting for Postgres"
pg_ready=0
for _ in $(seq 1 60); do
    if docker exec "$PG_CONTAINER" psql -U hazyflow -d hazyflow -c 'select 1' >/dev/null 2>&1; then
        pg_ready=1; break
    fi
    echo -n "."; sleep 0.5
done
if [[ "$pg_ready" -ne 1 ]]; then echo " FAILED"; echo "ERROR: Postgres did not become ready" >&2; exit 1; fi
echo " ready"

# --- 3. start hzd -----------------------------------------------------------
# /trigger/ paths land on the same HTTP listener as the API; we don't run
# a separate webhook port anymore. All hzd config goes via HAZYFLOW_*.
# Secrets live in the encrypted secret store (secret://), which needs a
# master key + Postgres (the ephemeral container above). The key is
# generated per-run; secrets are seeded over the API in step 3b.
echo "[3/7] starting hzd (workers=2, http=:18080, sandbox=$SANDBOX_BASE)"
MASTER_KEY=$(head -c32 /dev/urandom | base64)
HAZYFLOW_LISTEN=":50099" \
HAZYFLOW_HTTP=":18080" \
HAZYFLOW_DEV_KEY=1 \
HAZYFLOW_DATA_DIR="$SANDBOX_BASE" \
HAZYFLOW_MASTER_KEY="$MASTER_KEY" \
HAZYFLOW_POSTGRES_DSN="$PG_DSN" \
HAZYFLOW_ALLOW_PRIVATE_EGRESS=1 \
/tmp/ap-hzd \
    > "$HZD_LOG" 2>&1 &
HZD_PID=$!
# Postgres-backed boot (connect + migrate) can take 10-20s on a cold
# container, so wait generously for the HTTP listener before proceeding.
booted=0
for _ in $(seq 1 60); do
    if grep -q "listening on \[::\]:18080" "$HZD_LOG" 2>/dev/null; then booted=1; break; fi
    if ! kill -0 "$HZD_PID" 2>/dev/null; then break; fi  # hzd died — stop waiting
    sleep 0.5
done
if [[ "$booted" -ne 1 ]]; then
    echo "ERROR: hzd did not come up; log follows:" >&2
    sed 's/^/      /' "$HZD_LOG" >&2
    exit 1
fi
grep -E "listening" "$HZD_LOG" | sed 's/^/      /' || true
TOKEN=$(grep -oE 'hzk_[a-z0-9_]+' "$HZD_LOG" | head -1)

# --- 3b. seed the demo secrets into the encrypted store -------------------
echo "[3b/7] seeding secrets (secret://INVOICE_API_KEY, SLACK_TOKEN, APPROVAL_API_KEY)"
seed_secret() {
    curl -fsS -X PUT \
        -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
        -d "{\"value\":\"$2\"}" \
        "http://localhost:18080/api/v1/secrets/$1" > /dev/null
}
seed_secret INVOICE_API_KEY  "Bearer invoice-svc-key-abc"
seed_secret SLACK_TOKEN      "Bearer slack-bot-token-def"
seed_secret APPROVAL_API_KEY "Bearer approval-system-key-ghi"

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
ls -1 "$SANDBOX_BASE/sandbox/dev/default/archive/" 2>&1 | sed 's/^/    /'
echo
echo "--- archive/invoice-42.json contents ---"
cat "$SANDBOX_BASE/sandbox/dev/default/archive/invoice-42.json" | sed 's/^/    /'
echo
echo "--- archive/invoice-big-99.json contents ---"
cat "$SANDBOX_BASE/sandbox/dev/default/archive/invoice-big-99.json" | sed 's/^/    /'

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
    "test -f $SANDBOX_BASE/sandbox/dev/default/archive/invoice-42.json && test -f $SANDBOX_BASE/sandbox/dev/default/archive/invoice-big-99.json"
assert "mock backend saw correct API key for /invoices" \
    "! grep -q 'invoices: bad Authorization' $BE_LOG"
assert "no SSRF or auth refusals in backend log" \
    "! grep -q 'unauthorized' $BE_LOG"
assert "graph JSON still references secret:// (resolved secrets not leaked)" \
    "HZCTL_TOKEN=$TOKEN /tmp/ap-hzctl --server=localhost:50099 graph load process-invoice-low 2>&1 | grep -q 'secret://INVOICE_API_KEY'"

if [[ $errors -eq 0 ]]; then
    echo
    echo "[ok] all assertions passed"
else
    echo
    echo "[!!] $errors assertion(s) failed"
    exit 1
fi
