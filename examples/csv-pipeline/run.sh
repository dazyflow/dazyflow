#!/usr/bin/env bash
# End-to-end demo: dzd + remote transformer + dzctl driving a 3-node graph.
#
# What this proves:
#   - A third-party gRPC module slots into Dazyflow via --remote
#   - Data flows: native file_read → remote csv_uppercase → native file_write
#   - The sandbox holds: file_read reads from $DATA_DIR/dev/default,
#     file_write produces output there
set -euo pipefail

cd "$(dirname "$0")"
ROOT="$(cd ../.. && pwd)"

# dzd runs on Postgres — there's no in-memory mode — so the demo needs a DB.
# In CI the build manifest stands one up and exports DAZYFLOW_TEST_DB; locally,
# `make pg` starts the bundled database on the same default DSN. Honour an
# explicit DAZYFLOW_POSTGRES_DSN if the caller set one.
: "${DAZYFLOW_POSTGRES_DSN:=${DAZYFLOW_TEST_DB:-postgres://dazyflow:dazyflow@localhost:5432/dazyflow_test?sslmode=disable}}"
export DAZYFLOW_POSTGRES_DSN

DATA_DIR=$(mktemp -d)
trap 'rm -rf "$DATA_DIR" /tmp/dzd-demo /tmp/dzctl-demo /tmp/csv-xform-demo' EXIT

echo "[1/6] building binaries"
(cd "$ROOT" && go build -o /tmp/dzd-demo ./cmd/dzd)
(cd "$ROOT" && go build -o /tmp/dzctl-demo ./cmd/dzctl)
go build -o /tmp/csv-xform-demo ./transformer

echo "[2/6] seeding input.csv"
# dzd's sandbox lives under $DATA_DIR/sandbox/<tenant>/<workspace>/
mkdir -p "$DATA_DIR/sandbox/dev/default"
cat > "$DATA_DIR/sandbox/dev/default/input.csv" <<EOF
name,role
alice,engineer
bob,manager
charlie,intern
EOF
echo "    input.csv ($(wc -c < "$DATA_DIR/sandbox/dev/default/input.csv") bytes)"

# wait_for polls a bash /dev/tcp probe until the port accepts or it times out.
wait_for() { # host port label
    for _ in $(seq 1 100); do
        (exec 3<>"/dev/tcp/$1/$2") 2>/dev/null && { exec 3>&- 3<&-; return 0; }
        sleep 0.1
    done
    echo "[!!]  timed out waiting for $3 ($1:$2)"; return 1
}

echo "[3/6] starting csv_uppercase on :60001"
/tmp/csv-xform-demo --listen=127.0.0.1:60001 > /tmp/csv-xform.log 2>&1 &
XFORM_PID=$!
trap 'kill $XFORM_PID 2>/dev/null || true; kill $DZD_PID 2>/dev/null || true; rm -rf "$DATA_DIR" /tmp/dzd-demo /tmp/dzctl-demo /tmp/csv-xform-demo' EXIT
wait_for 127.0.0.1 60001 csv_uppercase

echo "[4/6] starting dzd with the remote registered"
# DAZYFLOW_DEV=1 downgrades the insecure-defaults guard (default DB password,
# empty master key) to warnings so the demo runs without provisioning real
# secrets; DEV_KEY=1 mints the dev token we grep out below.
DAZYFLOW_LISTEN=":50099" \
DAZYFLOW_DEV=1 \
DAZYFLOW_DEV_KEY=1 \
DAZYFLOW_DATA_DIR="$DATA_DIR" \
DAZYFLOW_REMOTE_MODULES="csv_uppercase=127.0.0.1:60001" \
/tmp/dzd-demo > /tmp/dzd.log 2>&1 &
DZD_PID=$!
# Dump dzd's log if it never binds — otherwise a startup failure (e.g. an
# unreachable DB) shows only as an opaque timeout.
wait_for 127.0.0.1 50099 dzd || { echo "    --- dzd log ---"; sed 's/^/    /' /tmp/dzd.log; exit 1; }
# The dev token is minted during boot; poll the log until it appears.
TOKEN=""
for _ in $(seq 1 100); do
    TOKEN=$(grep -oE 'dzk_[a-z0-9_]+' /tmp/dzd.log | head -1)
    [ -n "$TOKEN" ] && break
    sleep 0.1
done
echo "    dev token: ${TOKEN:0:20}..."
grep -E "registered remote|listening" /tmp/dzd.log | sed 's/^/    /'

echo "[5/6] submitting + running the graph"
DZCTL_TOKEN="$TOKEN" /tmp/dzctl-demo --server=localhost:50099 graph save pipeline.json > /dev/null
DZCTL_TOKEN="$TOKEN" /tmp/dzctl-demo --server=localhost:50099 graph run csv-uppercase 2>&1 | sed 's/^/    /'

echo "[6/6] verifying output"
if [[ -f "$DATA_DIR/sandbox/dev/default/output.csv" ]]; then
    echo "    output.csv exists"
    echo "    --- output.csv ---"
    sed 's/^/    /' "$DATA_DIR/sandbox/dev/default/output.csv"
    echo "    ------------------"
    EXPECT="NAME,ROLE
ALICE,ENGINEER
BOB,MANAGER
CHARLIE,INTERN"
    if [[ "$(cat "$DATA_DIR/sandbox/dev/default/output.csv")" == "$EXPECT" ]]; then
        echo "[ok]  pipeline produced the expected uppercase CSV"
    else
        echo "[!!]  contents differ from expected"
        exit 1
    fi
else
    echo "[!!]  output.csv was not produced"
    echo "    --- dzd log tail ---"
    tail -20 /tmp/dzd.log
    echo "    --- transformer log tail ---"
    tail -20 /tmp/csv-xform.log
    exit 1
fi
