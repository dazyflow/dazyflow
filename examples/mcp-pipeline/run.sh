#!/usr/bin/env bash
# Live MCP integration demo: dzd spawns a real MCP stdio server, lists
# its tools, and a graph calls one of them. The "server" is a tiny Go
# binary in ./server but the protocol is identical to what
# @modelcontextprotocol/server-* npm packages speak — drop in any of
# those by passing it to --mcp.
set -euo pipefail

cd "$(dirname "$0")"
ROOT="$(cd ../.. && pwd)"

SANDBOX_BASE=$(mktemp -d)
DZD_LOG=/tmp/mcp-dzd.log

cleanup() {
    kill "${DZD_PID:-}" 2>/dev/null || true
    wait 2>/dev/null || true
    rm -rf "$SANDBOX_BASE" /tmp/mcp-dzd /tmp/mcp-dzctl /tmp/mcp-server
    rm -f /tmp/mcp-pipeline.json "$DZD_LOG"
}
trap cleanup EXIT

echo "[1/5] building binaries"
(cd "$ROOT" && go build -o /tmp/mcp-dzd ./cmd/dzd)
(cd "$ROOT" && go build -o /tmp/mcp-dzctl ./cmd/dzctl)
go build -o /tmp/mcp-server ./server

echo "[2/5] starting dzd with MCP server registered"
# --mcp=name=command [args]; semicolon-separated across servers
DAZYFLOW_LISTEN=":50099" \
DAZYFLOW_DEV_KEY=1 \
DAZYFLOW_DATA_DIR="$SANDBOX_BASE" \
DAZYFLOW_MCP_SERVERS="ap-demo=/tmp/mcp-server" \
/tmp/mcp-dzd > "$DZD_LOG" 2>&1 &
DZD_PID=$!
sleep 0.5
TOKEN=$(grep -oE 'dzk_[a-z0-9_]+' "$DZD_LOG" | head -1)
grep -E "registered MCP|listening" "$DZD_LOG" | sed 's/^/    /'

echo "[3/5] verifying MCP tools appear as modules"
DZCTL_TOKEN=$TOKEN /tmp/mcp-dzctl --server=localhost:50099 module list 2>&1 \
    | grep "^mcp:" | sed 's/^/    /'

echo "[4/5] running the graph"
DZCTL_TOKEN=$TOKEN /tmp/mcp-dzctl --server=localhost:50099 graph save pipeline.json > /dev/null
DZCTL_TOKEN=$TOKEN /tmp/mcp-dzctl --server=localhost:50099 graph run mcp-lookup-and-route 2>&1 | tail -5

echo "[5/5] inspecting outcome"
echo "    archives written under $SANDBOX_BASE/dev/default/users/:"
ls "$SANDBOX_BASE/dev/default/users/" | sed 's/^/      /'

if [[ -f "$SANDBOX_BASE/dev/default/users/premium.json" ]]; then
    echo "    premium.json content:"
    sed 's/^/      /' "$SANDBOX_BASE/dev/default/users/premium.json"
fi

echo
echo "    job records:"
DZCTL_TOKEN=$TOKEN /tmp/mcp-dzctl --server=localhost:50099 job list mcp-lookup-and-route 2>&1 | sed 's/^/      /'

echo
echo "--- assertions ---"
errors=0
assert() {
    if eval "$2"; then echo "  [ok] $1"; else echo "  [!!] $1"; errors=$((errors + 1)); fi
}
assert "dzd registered the MCP server" \
    "grep -q 'registered MCP server' $DZD_LOG"
assert "tools appear as mcp:ap-demo:* modules" \
    "DZCTL_TOKEN=$TOKEN /tmp/mcp-dzctl --server=localhost:50099 module list 2>&1 | grep -q 'mcp:ap-demo:lookup_user'"
assert "graph succeeded end-to-end" \
    "DZCTL_TOKEN=$TOKEN /tmp/mcp-dzctl --server=localhost:50099 job list mcp-lookup-and-route 2>&1 | grep -E '^[a-f0-9]+ +succeeded'"
assert "MCP tool result reached the branch and routed to premium" \
    "test -f $SANDBOX_BASE/dev/default/users/premium.json"
assert "regular path was skipped (branch correctly forked)" \
    "DZCTL_TOKEN=$TOKEN /tmp/mcp-dzctl --server=localhost:50099 job list mcp-lookup-and-route 2>&1 | grep save_regular | grep -q skipped"

if [[ $errors -eq 0 ]]; then
    echo
    echo "[ok] all assertions passed — Dazyflow ↔ MCP integration live"
else
    echo
    echo "[!!] $errors assertion(s) failed"
    exit 1
fi
