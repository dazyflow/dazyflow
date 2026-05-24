#!/usr/bin/env bash
# End-to-end demo: hzd + remote transformer + hzctl driving a 3-node graph.
#
# What this proves:
#   - A third-party gRPC module slots into Hazy Flow via --remote
#   - Data flows: native file_read → remote csv_uppercase → native file_write
#   - The sandbox holds: file_read reads from $SANDBOX_BASE/dev/default,
#     file_write produces output there
set -euo pipefail

cd "$(dirname "$0")"
ROOT="$(cd ../.. && pwd)"

SANDBOX_BASE=$(mktemp -d)
trap 'rm -rf "$SANDBOX_BASE" /tmp/hzd-demo /tmp/hzctl-demo /tmp/csv-xform-demo' EXIT

echo "[1/6] building binaries"
(cd "$ROOT" && go build -o /tmp/hzd-demo ./cmd/hzd)
(cd "$ROOT" && go build -o /tmp/hzctl-demo ./cmd/hzctl)
go build -o /tmp/csv-xform-demo ./transformer

echo "[2/6] seeding input.csv"
mkdir -p "$SANDBOX_BASE/dev/default"
cat > "$SANDBOX_BASE/dev/default/input.csv" <<EOF
name,role
alice,engineer
bob,manager
charlie,intern
EOF
echo "    input.csv ($(wc -c < "$SANDBOX_BASE/dev/default/input.csv") bytes)"

echo "[3/6] starting csv_uppercase on :60001"
/tmp/csv-xform-demo --listen=127.0.0.1:60001 > /tmp/csv-xform.log 2>&1 &
XFORM_PID=$!
trap 'kill $XFORM_PID 2>/dev/null || true; kill $HZD_PID 2>/dev/null || true; rm -rf "$SANDBOX_BASE" /tmp/hzd-demo /tmp/hzctl-demo /tmp/csv-xform-demo' EXIT
sleep 0.3

echo "[4/6] starting hzd with the remote registered"
/tmp/hzd-demo --listen=:50099 --workers=2 \
              --sandbox-base="$SANDBOX_BASE" \
              --remote="csv_uppercase=127.0.0.1:60001" \
              > /tmp/hzd.log 2>&1 &
HZD_PID=$!
sleep 0.5
TOKEN=$(grep -oE 'hzk_[a-z0-9_]+' /tmp/hzd.log | head -1)
echo "    dev token: ${TOKEN:0:20}..."
grep -E "registered remote|listening" /tmp/hzd.log | sed 's/^/    /'

echo "[5/6] submitting + running the graph"
HZCTL_TOKEN="$TOKEN" /tmp/hzctl-demo --server=localhost:50099 graph save pipeline.json > /dev/null
HZCTL_TOKEN="$TOKEN" /tmp/hzctl-demo --server=localhost:50099 graph run csv-uppercase 2>&1 | sed 's/^/    /'

echo "[6/6] verifying output"
if [[ -f "$SANDBOX_BASE/dev/default/output.csv" ]]; then
    echo "    output.csv exists"
    echo "    --- output.csv ---"
    sed 's/^/    /' "$SANDBOX_BASE/dev/default/output.csv"
    echo "    ------------------"
    EXPECT="NAME,ROLE
ALICE,ENGINEER
BOB,MANAGER
CHARLIE,INTERN"
    if [[ "$(cat "$SANDBOX_BASE/dev/default/output.csv")" == "$EXPECT" ]]; then
        echo "[ok]  pipeline produced the expected uppercase CSV"
    else
        echo "[!!]  contents differ from expected"
        exit 1
    fi
else
    echo "[!!]  output.csv was not produced"
    echo "    --- hzd log tail ---"
    tail -20 /tmp/hzd.log
    echo "    --- transformer log tail ---"
    tail -20 /tmp/csv-xform.log
    exit 1
fi
