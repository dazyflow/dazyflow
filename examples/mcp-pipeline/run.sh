#!/usr/bin/env bash
# Live MCP integration demo: hzd spawns a real MCP stdio server, lists
# its tools, and a graph calls one of them. The "server" is a tiny Go
# binary in ./server but the protocol is identical to what
# @modelcontextprotocol/server-* npm packages speak — drop in any of
# those by passing it to --mcp.
set -euo pipefail

cd "$(dirname "$0")"
ROOT="$(cd ../.. && pwd)"

SANDBOX_BASE=$(mktemp -d)
HZD_LOG=/tmp/mcp-hzd.log

cleanup() {
    kill "${HZD_PID:-}" 2>/dev/null || true
    wait 2>/dev/null || true
    rm -rf "$SANDBOX_BASE" /tmp/mcp-hzd /tmp/mcp-hzctl /tmp/mcp-server
    rm -f /tmp/mcp-pipeline.json "$HZD_LOG"
}
trap cleanup EXIT

echo "[1/5] building binaries"
(cd "$ROOT" && go build -o /tmp/mcp-hzd ./cmd/hzd)
(cd "$ROOT" && go build -o /tmp/mcp-hzctl ./cmd/hzctl)
go build -o /tmp/mcp-server ./server

echo "[2/5] starting hzd with MCP server registered"
# --mcp=name=command [args]; semicolon-separated across servers
HAZYFLOW_LISTEN=":50099" \
HAZYFLOW_DEV_KEY=1 \
HAZYFLOW_DATA_DIR="$SANDBOX_BASE" \
HAZYFLOW_MCP_SERVERS="ap-demo=/tmp/mcp-server" \
/tmp/mcp-hzd > "$HZD_LOG" 2>&1 &
HZD_PID=$!
sleep 0.5
TOKEN=$(grep -oE 'hzk_[a-z0-9_]+' "$HZD_LOG" | head -1)
grep -E "registered MCP|listening" "$HZD_LOG" | sed 's/^/    /'

echo "[3/5] verifying MCP tools appear as modules"
HZCTL_TOKEN=$TOKEN /tmp/mcp-hzctl --server=localhost:50099 module list 2>&1 \
    | grep "^mcp:" | sed 's/^/    /'

echo "[4/5] running the graph"
HZCTL_TOKEN=$TOKEN /tmp/mcp-hzctl --server=localhost:50099 graph save pipeline.json > /dev/null
HZCTL_TOKEN=$TOKEN /tmp/mcp-hzctl --server=localhost:50099 graph run mcp-lookup-and-route 2>&1 | tail -5

echo "[5/5] inspecting outcome"
echo "    archives written under $SANDBOX_BASE/dev/default/users/:"
ls "$SANDBOX_BASE/dev/default/users/" | sed 's/^/      /'

if [[ -f "$SANDBOX_BASE/dev/default/users/premium.json" ]]; then
    echo "    premium.json content:"
    sed 's/^/      /' "$SANDBOX_BASE/dev/default/users/premium.json"
fi

echo
echo "    job records:"
HZCTL_TOKEN=$TOKEN /tmp/mcp-hzctl --server=localhost:50099 job list mcp-lookup-and-route 2>&1 | sed 's/^/      /'

echo
echo "--- assertions ---"
errors=0
assert() {
    if eval "$2"; then echo "  [ok] $1"; else echo "  [!!] $1"; errors=$((errors + 1)); fi
}
assert "hzd registered the MCP server" \
    "grep -q 'registered MCP server' $HZD_LOG"
assert "tools appear as mcp:ap-demo:* modules" \
    "HZCTL_TOKEN=$TOKEN /tmp/mcp-hzctl --server=localhost:50099 module list 2>&1 | grep -q 'mcp:ap-demo:lookup_user'"
assert "graph succeeded end-to-end" \
    "HZCTL_TOKEN=$TOKEN /tmp/mcp-hzctl --server=localhost:50099 job list mcp-lookup-and-route 2>&1 | grep -E '^[a-f0-9]+ +succeeded'"
assert "MCP tool result reached the branch and routed to premium" \
    "test -f $SANDBOX_BASE/dev/default/users/premium.json"
assert "regular path was skipped (branch correctly forked)" \
    "HZCTL_TOKEN=$TOKEN /tmp/mcp-hzctl --server=localhost:50099 job list mcp-lookup-and-route 2>&1 | grep save_regular | grep -q skipped"

if [[ $errors -eq 0 ]]; then
    echo
    echo "[ok] all assertions passed — Hazyflow ↔ MCP integration live"
else
    echo
    echo "[!!] $errors assertion(s) failed"
    exit 1
fi
