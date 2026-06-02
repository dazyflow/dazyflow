#!/usr/bin/env bash
# End-to-end demo: hzd + remote transformer + hzctl driving a 3-node graph.
#
# What this proves:
#   - A third-party gRPC module slots into Hazyflow via --remote
#   - Data flows: native file_read → remote csv_uppercase → native file_write
#   - The sandbox holds: file_read reads from $DATA_DIR/dev/default,
#     file_write produces output there
set -euo pipefail

cd "$(dirname "$0")"
ROOT="$(cd ../.. && pwd)"

DATA_DIR=$(mktemp -d)
trap 'rm -rf "$DATA_DIR" /tmp/hzd-demo /tmp/hzctl-demo /tmp/csv-xform-demo' EXIT

echo "[1/6] building binaries"
(cd "$ROOT" && go build -o /tmp/hzd-demo ./cmd/hzd)
(cd "$ROOT" && go build -o /tmp/hzctl-demo ./cmd/hzctl)
go build -o /tmp/csv-xform-demo ./transformer

echo "[2/6] seeding input.csv"
# hzd's sandbox lives under $DATA_DIR/sandbox/<tenant>/<workspace>/
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
trap 'kill $XFORM_PID 2>/dev/null || true; kill $HZD_PID 2>/dev/null || true; rm -rf "$DATA_DIR" /tmp/hzd-demo /tmp/hzctl-demo /tmp/csv-xform-demo' EXIT
wait_for 127.0.0.1 60001 csv_uppercase

echo "[4/6] starting hzd with the remote registered"
# Running hzd from a bare `go build` (not the Docker image) means drophost.mjs
# isn't beside the binary; point hzd at the in-repo copy so the scripted-drop
# runtime (used by the official drops) initializes instead of fatally exiting.
HAZYFLOW_LISTEN=":50099" \
HAZYFLOW_DEV_KEY=1 \
HAZYFLOW_DATA_DIR="$DATA_DIR" \
HAZYFLOW_NODE_DROPHOST="$ROOT/engine/containerdrop/nodehost/drophost.mjs" \
HAZYFLOW_REMOTE_MODULES="csv_uppercase=127.0.0.1:60001" \
/tmp/hzd-demo > /tmp/hzd.log 2>&1 &
HZD_PID=$!
wait_for 127.0.0.1 50099 hzd
# The dev token is minted during boot; poll the log until it appears.
TOKEN=""
for _ in $(seq 1 100); do
    TOKEN=$(grep -oE 'hzk_[a-z0-9_]+' /tmp/hzd.log | head -1)
    [ -n "$TOKEN" ] && break
    sleep 0.1
done
echo "    dev token: ${TOKEN:0:20}..."
grep -E "registered remote|listening" /tmp/hzd.log | sed 's/^/    /'

echo "[5/6] submitting + running the graph"
HZCTL_TOKEN="$TOKEN" /tmp/hzctl-demo --server=localhost:50099 graph save pipeline.json > /dev/null
HZCTL_TOKEN="$TOKEN" /tmp/hzctl-demo --server=localhost:50099 graph run csv-uppercase 2>&1 | sed 's/^/    /'

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
    echo "    --- hzd log tail ---"
    tail -20 /tmp/hzd.log
    echo "    --- transformer log tail ---"
    tail -20 /tmp/csv-xform.log
    exit 1
fi
