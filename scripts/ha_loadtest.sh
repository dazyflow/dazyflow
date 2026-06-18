#!/usr/bin/env bash
#
# Multi-node HA load test: run TWO real dzd processes against ONE Postgres
# and assert the Phase-2 invariants end-to-end:
#
#   1. No double-fires. A poll trigger scheduled every 1s is fired ~once
#      per second total across the cluster — NOT once per second PER node.
#      Only the advisory-lock leader fires (daemon/leader.go).
#   2. Failover. Kill the leader; a follower acquires the lock and keeps
#      firing within the retry window (daemon/scheduler.go SetLeader path).
#
# It spins up a throwaway Postgres in Docker, seeds a node-less poll graph
# (scripts/ha_loadtest/seed.go — every fire = exactly one kind='graph'
# row), starts both daemons sharing the DB + workspace dir, then counts
# rows in the jobs table over time.
#
# Usage:  scripts/ha_loadtest.sh
# Env:    PG_PORT (default 55432), KEEP=1 to skip teardown for debugging.
set -euo pipefail

cd "$(dirname "$0")/.."

PG_PORT="${PG_PORT:-55432}"
PG_CONTAINER="dzd-ha-loadtest-pg"
PG_DB="dazyflow_test"
DSN="postgres://postgres:test@localhost:${PG_PORT}/${PG_DB}"
GRAPH_ID="ha-poll"
TENANT="loadtest"

WORKDIR="$(mktemp -d /tmp/dzd-ha-XXXXXX)"
# Both nodes share DAZYFLOW_DATA_DIR so the workspace (graphs the
# scheduler reads from) is visible to whichever one wins leadership.
# Sandboxes overlap by design — this mirrors HA production where a
# shared filesystem (NFS / EFS) lives behind every replica.
DATA_DIR="${WORKDIR}/data"
WS_DIR="${DATA_DIR}/workspace"
LOG_A="${WORKDIR}/dzd-a.log"
LOG_B="${WORKDIR}/dzd-b.log"
PID_A=""
PID_B=""

say() { printf '\n=== %s ===\n' "$*"; }
fail() { printf '\nFAIL: %s\n' "$*" >&2; exit 1; }

cleanup() {
	set +e
	[ -n "$PID_A" ] && kill "$PID_A" 2>/dev/null
	[ -n "$PID_B" ] && kill "$PID_B" 2>/dev/null
	wait 2>/dev/null
	if [ "${KEEP:-0}" = "1" ]; then
		echo "KEEP=1 — leaving container ${PG_CONTAINER} and ${WORKDIR}"
		return
	fi
	docker rm -f "$PG_CONTAINER" >/dev/null 2>&1
	rm -rf "$WORKDIR"
}
trap cleanup EXIT

psql_count() {
	docker exec "$PG_CONTAINER" psql -U postgres -d "$PG_DB" -tAc \
		"SELECT count(*) FROM jobs WHERE kind='graph' AND graph_id='${GRAPH_ID}';"
}

leader_of() { # arg: logfile -> prints "yes" if it logged acquiring leadership
	grep -q "acquired scheduler leadership" "$1" && echo yes || echo no
}

# --- 1. Postgres -----------------------------------------------------------
say "starting throwaway Postgres (${PG_CONTAINER} on :${PG_PORT})"
docker rm -f "$PG_CONTAINER" >/dev/null 2>&1 || true
docker run -d --name "$PG_CONTAINER" \
	-e POSTGRES_PASSWORD=test -e POSTGRES_DB="$PG_DB" \
	-p "${PG_PORT}:5432" postgres:16-alpine >/dev/null

echo -n "waiting for postgres"
for _ in $(seq 1 30); do
	if docker exec "$PG_CONTAINER" pg_isready -U postgres >/dev/null 2>&1; then
		echo " up"; break
	fi
	echo -n "."; sleep 1
done
docker exec "$PG_CONTAINER" pg_isready -U postgres >/dev/null 2>&1 || fail "postgres never came up"

# --- 2. Build --------------------------------------------------------------
say "building dzd + seed helper"
go build -o "${WORKDIR}/dzd" ./cmd/dzd
go build -o "${WORKDIR}/seed" ./scripts/ha_loadtest

# --- 3. Seed the poll graph ------------------------------------------------
say "seeding node-less poll graph (every 1s) into shared workspace"
"${WORKDIR}/seed" --base "$WS_DIR" --tenant "$TENANT" --graph "$GRAPH_ID" --interval 1

start_dzd() { # args: name logfile grpc-port
	DAZYFLOW_POSTGRES_DSN="$DSN" \
	DAZYFLOW_DATA_DIR="$DATA_DIR" \
	DAZYFLOW_LISTEN="127.0.0.1:$3" \
	"${WORKDIR}/dzd" >"$2" 2>&1 &
}

# --- 4. Start both nodes ----------------------------------------------------
say "starting node A (:50051) and node B (:50052)"
start_dzd a "$LOG_A" 50051; PID_A=$!
sleep 3   # let A win leadership first
start_dzd b "$LOG_B" 50052; PID_B=$!
sleep 4   # let B settle as follower

kill -0 "$PID_A" 2>/dev/null || fail "node A exited early; see $LOG_A"
kill -0 "$PID_B" 2>/dev/null || fail "node B exited early; see $LOG_B"

# Confirm exactly one leader.
A_LEAD=$(leader_of "$LOG_A"); B_LEAD=$(leader_of "$LOG_B")
echo "node A leader=$A_LEAD  node B leader=$B_LEAD"
if [ "$A_LEAD" = "$B_LEAD" ]; then
	fail "expected exactly one leader, got A=$A_LEAD B=$B_LEAD"
fi
if [ "$A_LEAD" = yes ]; then LEADER_PID=$PID_A; LEADER=A; SURVIVOR_LOG=$LOG_B; else LEADER_PID=$PID_B; LEADER=B; SURVIVOR_LOG=$LOG_A; fi
echo "leader is node $LEADER (pid $LEADER_PID)"

# --- 5. No-double-fire window ----------------------------------------------
WINDOW=10
say "measuring fire rate over ${WINDOW}s (poll interval 1s)"
BEFORE=$(psql_count)
sleep "$WINDOW"
AFTER=$(psql_count)
FIRED=$((AFTER - BEFORE))
echo "fires in ${WINDOW}s: $FIRED (single-leader expectation ≈ ${WINDOW})"

# Two uncoordinated schedulers would roughly double this. Allow generous
# slack for tick alignment / startup, but well below the 2x doubling line.
LOW=$((WINDOW / 2))
HIGH=$((WINDOW * 18 / 10))   # 1.8x
[ "$FIRED" -ge "$LOW" ]  || fail "fired $FIRED < $LOW — leader isn't firing?"
[ "$FIRED" -le "$HIGH" ] || fail "fired $FIRED > $HIGH — looks like BOTH nodes fired (double-fire)"
echo "PASS: no double-fire (within [$LOW, $HIGH])"

# --- 6. Failover ------------------------------------------------------------
say "killing leader (node $LEADER) and watching for failover"
PRE_FAILOVER=$(psql_count)
kill -9 "$LEADER_PID"
[ "$LEADER" = A ] && PID_A="" || PID_B=""

# Lock auto-releases when the dead conn drops; follower retries every 5s.
echo -n "waiting for survivor to acquire leadership"
ACQUIRED=no
for _ in $(seq 1 15); do
	if [ "$(leader_of "$SURVIVOR_LOG")" = yes ]; then ACQUIRED=yes; echo " ok"; break; fi
	echo -n "."; sleep 1
done
[ "$ACQUIRED" = yes ] || fail "survivor never acquired leadership; see $SURVIVOR_LOG"

say "confirming survivor keeps firing"
sleep "$WINDOW"
POST_FAILOVER=$(psql_count)
FAILOVER_FIRED=$((POST_FAILOVER - PRE_FAILOVER))
echo "fires after failover (incl. takeover gap): $FAILOVER_FIRED"
[ "$FAILOVER_FIRED" -ge "$LOW" ] || fail "survivor not firing after failover ($FAILOVER_FIRED)"
echo "PASS: failover — survivor took over and kept firing"

say "ALL CHECKS PASSED"
