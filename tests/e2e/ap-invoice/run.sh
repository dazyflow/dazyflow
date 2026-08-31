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
ROOT="$(cd ../../.. && pwd)"

SANDBOX_BASE=$(mktemp -d)
DZD_LOG=/tmp/ap-dzd.log
BE_LOG=/tmp/ap-be.log

# Ephemeral Postgres for the encrypted secret store (secret://). Own
# container + port + strong password, so the demo is self-contained and
# never touches a local/production database.
PG_CONTAINER=ap-demo-pg
PG_PORT=55432
PG_PASS=ap-demo-not-a-real-password
PG_DSN="postgres://dazyflow:${PG_PASS}@localhost:${PG_PORT}/dazyflow?sslmode=disable"

cleanup() {
    kill "${DZD_PID:-}" "${BE_PID:-}" 2>/dev/null || true
    wait 2>/dev/null || true
    docker rm -f "$PG_CONTAINER" >/dev/null 2>&1 || true
    rm -rf "$SANDBOX_BASE" /tmp/ap-dzd /tmp/ap-dzctl /tmp/ap-backend
    rm -f /tmp/ap-low.json /tmp/ap-high.json "$DZD_LOG" "$BE_LOG"
}
trap cleanup EXIT

# --- 1. build --------------------------------------------------------------
echo "[1/7] building binaries"
(cd "$ROOT" && go build -o /tmp/ap-dzd ./cmd/dzd)
(cd "$ROOT" && go build -o /tmp/ap-dzctl ./cmd/dzctl)
go build -o /tmp/ap-backend ./mock-backend

# --- 2. start mock backend + ephemeral Postgres ---------------------------
echo "[2/7] starting mock backend on :60500"
/tmp/ap-backend --listen=:60500 > "$BE_LOG" 2>&1 &
BE_PID=$!
sleep 0.2

echo "      starting ephemeral Postgres (container $PG_CONTAINER on :$PG_PORT)"
docker rm -f "$PG_CONTAINER" >/dev/null 2>&1 || true
docker run -d --rm --name "$PG_CONTAINER" \
    -e POSTGRES_USER=dazyflow -e POSTGRES_PASSWORD="$PG_PASS" -e POSTGRES_DB=dazyflow \
    -p "$PG_PORT":5432 postgres:16-alpine >/dev/null
# The official postgres image restarts once after initdb, so pg_isready
# can report "up" during init and then reset the next connection. Probe
# with a real query (over the in-container socket) so we only proceed once
# the server truly accepts connections — dzd Fatal-exits on a failed
# connect, with no retry of its own.
echo -n "      waiting for Postgres"
pg_ready=0
for _ in $(seq 1 60); do
    if docker exec "$PG_CONTAINER" psql -U dazyflow -d dazyflow -c 'select 1' >/dev/null 2>&1; then
        pg_ready=1; break
    fi
    echo -n "."; sleep 0.5
done
if [[ "$pg_ready" -ne 1 ]]; then echo " FAILED"; echo "ERROR: Postgres did not become ready" >&2; exit 1; fi
echo " ready"

# --- 3. start dzd -----------------------------------------------------------
# /trigger/ paths land on the same HTTP listener as the API; we don't run
# a separate webhook port anymore. All dzd config goes via DAZYFLOW_*.
# Secrets live in the encrypted secret store (secret://), which needs a
# master key + Postgres (the ephemeral container above). The key is
# generated per-run; secrets are seeded over the API in step 3b.
echo "[3/7] starting dzd (workers=2, http=:18080, sandbox=$SANDBOX_BASE)"
MASTER_KEY=$(head -c32 /dev/urandom | base64)
# DAZYFLOW_DEV=1 downgrades the fail-closed config guard to warnings. This
# demo already provisions the two things that guard is usually protecting — a
# strong DB password and a real random master key, both generated above — so
# the only check it actually trips is the TLS one: the ephemeral Postgres is a
# throwaway loopback container with no certificate, and giving it one to run a
# demo would be theatre. Without this the script dies at boot with
# "DAZYFLOW_POSTGRES_DSN does not enforce TLS", which is what it did.
DAZYFLOW_LISTEN=":50099" \
DAZYFLOW_HTTP=":18080" \
DAZYFLOW_DEV=1 \
DAZYFLOW_DEV_KEY=1 \
DAZYFLOW_DATA_DIR="$SANDBOX_BASE" \
DAZYFLOW_MASTER_KEY="$MASTER_KEY" \
DAZYFLOW_POSTGRES_DSN="$PG_DSN" \
DAZYFLOW_ALLOW_PRIVATE_EGRESS=1 \
/tmp/ap-dzd \
    > "$DZD_LOG" 2>&1 &
DZD_PID=$!
# Postgres-backed boot (connect + migrate) can take 10-20s on a cold
# container, so wait generously for the HTTP listener before proceeding.
booted=0
for _ in $(seq 1 60); do
    if grep -q "listening on \[::\]:18080" "$DZD_LOG" 2>/dev/null; then booted=1; break; fi
    if ! kill -0 "$DZD_PID" 2>/dev/null; then break; fi  # dzd died — stop waiting
    sleep 0.5
done
if [[ "$booted" -ne 1 ]]; then
    echo "ERROR: dzd did not come up; log follows:" >&2
    sed 's/^/      /' "$DZD_LOG" >&2
    exit 1
fi
grep -E "listening" "$DZD_LOG" | sed 's/^/      /' || true
TOKEN=$(grep -oE 'dzk_[a-z0-9_]+' "$DZD_LOG" | head -1)

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
echo "[4/7] saving and publishing graphs"
DZCTL_TOKEN=$TOKEN /tmp/ap-dzctl --server=localhost:50099 graph save pipeline-low.json > /dev/null
DZCTL_TOKEN=$TOKEN /tmp/ap-dzctl --server=localhost:50099 graph save pipeline-high.json > /dev/null

# A webhook fires the PUBLISHED revision, never the draft (see
# daemon/webhook.go: store.LoadPublished). Saving alone leaves both flows
# unpublished, so every POST below came back 401 "unknown endpoint or invalid
# secret" — the deliberately generic pre-auth error, which gives no hint that
# publishing is the missing step. That is what this script was doing.
#
# Publishing is an HTTP-only operation: there is no publish RPC in
# api/proto/control.proto and no `dzctl graph publish`, so this goes to the
# same :18080 gateway the webhooks below use, with the dev token as bearer.
# An empty body publishes HEAD. `flow_id` is the full tenant/workspace/id
# triple (splitFlowID in daemon/me_routes.go), and it is ONE path segment:
# the route is `{flow_id}`, which Go's mux matches against a single segment,
# so the slashes must be percent-encoded. Same as the web client, which sends
# encodeURIComponent(`${tenant}/${workspace}/${id}`).
for flow in process-invoice-low process-invoice-high; do
    code=$(curl -s -o /dev/null -w '%{http_code}' -X POST \
        -H "Authorization: Bearer $TOKEN" \
        "http://127.0.0.1:18080/api/v1/me/flows/dev%2Fmain%2F$flow/publish")
    [ "$code" = "200" ] || { echo "ERROR: publishing $flow returned HTTP $code"; exit 1; }
    echo "      published $flow"
done

# wait_for_job polls a run until it reaches a terminal state.
#
# This replaced `sleep 0.5`. The webhook returns as soon as the run is
# ENQUEUED, not when it finishes, so half a second was a bet on the whole graph
# — HTTP fetch, branch, file_write — completing that fast. It held on a warm
# machine and lost on CI, where the failure surfaced as
# "ls: cannot access .../archive/: No such file or directory" from the
# diagnostic below rather than as a named assertion.
wait_for_job() { # job-id label
    for _ in $(seq 1 300); do   # 30s
        if DZCTL_TOKEN=$TOKEN /tmp/ap-dzctl --server=localhost:50099 job status "$1" 2>&1 \
            | grep -qE 'status:[[:space:]]+(succeeded|failed)'; then
            return 0
        fi
        sleep 0.1
    done
    echo "[!!]  timed out waiting for $2 ($1) to reach a terminal state"
    return 1
}

# --- 5. fire the low-value invoice via webhook ----------------------------
echo "[5/7] webhook → process-invoice-low (\$250 amount → auto-approve path)"
LOW_JOB=$(curl -s -X POST -H "Authorization: Bearer webhook-secret" \
    http://127.0.0.1:18080/trigger/dev/main/process-invoice-low | grep -oE '[a-f0-9]{20,}')
echo "      → job $LOW_JOB"
wait_for_job "$LOW_JOB" process-invoice-low || true

# --- 6. fire the high-value invoice ---------------------------------------
echo "[6/7] webhook → process-invoice-high (\$12,500 amount → CFO path)"
HIGH_JOB=$(curl -s -X POST -H "Authorization: Bearer webhook-secret" \
    http://127.0.0.1:18080/trigger/dev/main/process-invoice-high | grep -oE '[a-f0-9]{20,}')
echo "      → job $HIGH_JOB"
wait_for_job "$HIGH_JOB" process-invoice-high || true

# --- 7. verify ------------------------------------------------------------
echo "[7/7] verifying outcomes"
echo
echo "--- Mock backend log (what each service actually received) ---"
sed 's/^/    /' "$BE_LOG"
echo

echo "--- low-invoice ($LOW_JOB) node trail ---"
DZCTL_TOKEN=$TOKEN /tmp/ap-dzctl --server=localhost:50099 job list process-invoice-low 2>&1 | sed 's/^/    /'
echo
echo "--- high-invoice ($HIGH_JOB) node trail ---"
DZCTL_TOKEN=$TOKEN /tmp/ap-dzctl --server=localhost:50099 job list process-invoice-high 2>&1 | sed 's/^/    /'

echo
# `|| true` on each: these are diagnostics, and under `set -euo pipefail` a
# missing file made the script die HERE, at a debug line, instead of reaching
# the assertions below that would have named which invoice never archived.
echo "--- archived invoices on disk ---"
ls -1 "$SANDBOX_BASE/sandbox/dev/main/archive/" 2>&1 | sed 's/^/    /' || true
echo
echo "--- archive/invoice-42.json contents ---"
sed 's/^/    /' "$SANDBOX_BASE/sandbox/dev/main/archive/invoice-42.json" || true
echo
echo "--- archive/invoice-big-99.json contents ---"
sed 's/^/    /' "$SANDBOX_BASE/sandbox/dev/main/archive/invoice-big-99.json" || true

echo
echo "--- audit: saved graph still references secrets symbolically ---"
DZCTL_TOKEN=$TOKEN /tmp/ap-dzctl --server=localhost:50099 graph load process-invoice-low 2>&1 \
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
    "DZCTL_TOKEN=$TOKEN /tmp/ap-dzctl --server=localhost:50099 job status $LOW_JOB 2>&1 | grep -q 'status:    succeeded'"
assert "high-invoice graph succeeded" \
    "DZCTL_TOKEN=$TOKEN /tmp/ap-dzctl --server=localhost:50099 job status $HIGH_JOB 2>&1 | grep -q 'status:    succeeded'"
assert "low invoice routed to auto_approve" \
    "DZCTL_TOKEN=$TOKEN /tmp/ap-dzctl --server=localhost:50099 job list process-invoice-low 2>&1 | grep auto_approve | grep -q succeeded"
assert "low invoice's notify_cfo was skipped" \
    "DZCTL_TOKEN=$TOKEN /tmp/ap-dzctl --server=localhost:50099 job list process-invoice-low 2>&1 | grep notify_cfo | grep -q skipped"
assert "high invoice routed to notify_cfo" \
    "DZCTL_TOKEN=$TOKEN /tmp/ap-dzctl --server=localhost:50099 job list process-invoice-high 2>&1 | grep notify_cfo | grep -q succeeded"
assert "high invoice's auto_approve was skipped" \
    "DZCTL_TOKEN=$TOKEN /tmp/ap-dzctl --server=localhost:50099 job list process-invoice-high 2>&1 | grep auto_approve | grep -q skipped"
assert "both invoices archived" \
    "test -f $SANDBOX_BASE/sandbox/dev/main/archive/invoice-42.json && test -f $SANDBOX_BASE/sandbox/dev/main/archive/invoice-big-99.json"
assert "mock backend saw correct API key for /invoices" \
    "! grep -q 'invoices: bad Authorization' $BE_LOG"
assert "no SSRF or auth refusals in backend log" \
    "! grep -q 'unauthorized' $BE_LOG"
assert "graph JSON still references secret:// (resolved secrets not leaked)" \
    "DZCTL_TOKEN=$TOKEN /tmp/ap-dzctl --server=localhost:50099 graph load process-invoice-low 2>&1 | grep -q 'secret://INVOICE_API_KEY'"

if [[ $errors -eq 0 ]]; then
    echo
    echo "[ok] all assertions passed"
else
    echo
    echo "[!!] $errors assertion(s) failed"
    exit 1
fi
