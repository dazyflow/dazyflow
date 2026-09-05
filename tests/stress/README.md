<!--
SPDX-FileCopyrightText: 2026 Angels' Ware
SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Stress rig

Answers "what are the limits we are playing with" with numbers from your
hardware instead of an estimate from someone else's.

It drives the real queue, the real dispatcher, the real workers and the real
event bus against a real Postgres, with several simulated replicas contending
for the same work — which is the part unit tests and single-process benchmarks
cannot show you.

## Run it

```sh
make test-db                     # or point STRESS_DSN at any scratch database
STRESS_DSN='postgres://…/dazyflow_stress?sslmode=disable' \
  go test ./tests/stress/ -run TestStressQueue -v -timeout 30m
```

**It truncates `jobs` and `bus_events` in that database.** Give it a scratch
one, never the database an install is using.

## What to turn

| Variable | Default | What it means |
|---|---:|---|
| `STRESS_REPLICAS` | 3 | Independent dzd processes, each with its own connection pool |
| `STRESS_WORKERS` | 8 | Workers per replica — the per-process ceiling on concurrent steps |
| `STRESS_CONNS` | 20 | Pool size per replica |
| `STRESS_RATE` | 20 | Runs submitted per second |
| `STRESS_SECONDS` | 60 | How long to sustain it |
| `STRESS_NODES` | 8 | Steps per run |
| `STRESS_STEP_MS` | 50 | How long a step occupies its worker |
| `STRESS_TENANTS` | 50 | Distinct orgs the load is spread across |
| `STRESS_HOG_RUNS` | 0 | Runs one extra org bursts at the start, each `STRESS_HOG_WIDTH` (64) independent steps — the fairness scenario |
| `STRESS_BURST_SPACING` | store default | Queue burst spacing, e.g. `0` for plain FIFO, to A/B fairness |

## Reading the result

- **Achieved vs offered** — if achieved falls below offered, the fleet is the
  constraint, and the queue-latency line says by how much.
- **Queue latency** is `dazyflow_jobs_oldest_queued_seconds` in production: the
  age of the oldest step still waiting for a worker. Climbing means add workers
  or replicas.
- **Pool waits** rising means the pool is short for the worker count, which
  costs latency on everything else sharing it (the HTTP path most visibly)
  before it costs throughput.
- **Bytes per run** times your runs per day times your retention window is the
  storage question, which is usually the first real ceiling.

## What it found here

On a laptop with Postgres in a Docker container at stock settings — so these are
the *shape* of the answer, not your numbers:

Doubling the workers (3x8 to 3x16) and doubling the replicas (to 6x8) each
changed nothing: 281, 284 and 270 steps/s, with the commit rate flat at 3,758/s
throughout. **The database was the ceiling, not the fleet** — which is the
question this rig exists to answer, because from inside one process it looks
like a worker-count problem.

That pointed at the round trips a step costs, and acting on it moved the
saturation point from ~285 to ~390 steps/s:

| Offered | Before | After |
|---:|---:|---:|
| 320 steps/s | 284 (89%) | **320 (100%)** |
| 480 steps/s | 258 (54%) | **397 (83%)** |
| 640 steps/s | 289 (45%) | **383 (60%)** |

Two lessons worth keeping, because the first one is counter-intuitive:

- **Removing reads did almost nothing.** Skipping a whole-run read on every step
  took SELECTs from 5.2 to 4.2 per step and moved throughput by 1%. Reads do not
  fsync; commits do. Count transactions, not statements.
- **The event bus was a third of execution throughput.** Every event was its own
  commit. Batching them into one statement per flush window took the bus from
  2.15 statements per step to 0.40, and that is where the 37% came from.

`STRESS_BUS=memory` takes the bus off the database entirely and gives the
ceiling to aim at — 402 steps/s here, against 397 achieved, so there is little
left in that direction.

The next cut was the commit count itself. A step's completion and the enqueue of
its successors were two transactions; they are now one statement, which also
returns the run's status so the cancel check no longer costs a read of its own.
Per step that is 8.4 → 5.1 statements and 3.9 → 2.8 write transactions; at 48
workers offered 1,000 steps/s the fleet went from 614 to ~660 while the commit
rate *fell* from ~5,000/s to ~3,300/s. What a step costs now, from the trace:

| Per step | Statement |
|---:|---|
| 1.1 | claim (`UPDATE … FOR UPDATE SKIP LOCKED`) |
| 1.0 | complete + enqueue successors + read run status (one CTE) |
| 0.3 | graph record and root for a new run, amortised |
| 0.2–0.4 | event bus flush, amortised |
| 2.0 | point reads: predecessor results, and the run record once per replica |
| 0.1 | completion check, once per run |

The remaining gap to the fleet's theoretical rate is per-statement latency under
load rather than the number of commits: with 48 clients on one machine each of
the ~5 round trips a step still makes waits its turn.

## Is the queue fair?

`STRESS_HOG_RUNS=100` has one extra org dump 100 runs of 64 independent steps
(6,400 steps) on the queue at the start while 50 others trickle in at 480
steps/s, and reports how long *everyone else* waited. A chain would not do as a
hog — only one of its steps is ever queued — which is why the hog's flow is
wide.

| Queue order | Everyone else, worst | Everyone else, mean | The hog, worst |
|---|---:|---:|---:|
| first-come-first-served (`STRESS_BURST_SPACING=0`) | 5.86s | 1.71s | 5.86s |
| burst spacing 100ms (default) | **0.12s** | **0.02s** | 32.17s |

The hog pays for its own burst and nobody else notices it. Throughput at the
ceiling was the same either way (640 vs 622 steps/s, inside the noise), because
the fairness is decided at enqueue — one index probe for the org's queue tail —
and the claim stays a single index scan.

Two designs that looked right and measured wrong, kept here so they are not
tried again: deciding fairness *at claim time* (serve the org with the fewest
running steps) made every worker compute the same order and herd onto the same
few rows — 81ms per claim under load and throughput down 70%; and a 10ms
spacing degraded to plain FIFO under load, because an enqueue takes longer than
10ms then, and several enqueues in flight for one org read the same tail, so
the tail never outran the clock.

## Should you tune Postgres?

Mostly no — measured, not assumed. With 48 workers offered 990 steps/s on a
stock Postgres 16 in Docker:

| Setting | Steps/s |
|---|---:|
| stock (`shared_buffers` 128MB, `wal_buffers` 4MB, `synchronous_commit` on) | 614 |
| `commit_delay = 2000` (group commit, durable) | 615 |
| `shared_buffers` 1GB + `wal_buffers` 64MB | 613 |
| `synchronous_commit = off` | **661** |
| all of the above together | 661 |

The disk does ~1,240 fdatasyncs/s (`pg_test_fsync`), yet the run commits 5,000
times a second — Postgres already shares ~4 commits per flush. Sampling
`pg_stat_activity` mid-run shows why the knobs do not bite: at most **one**
backend is ever in `IO:WALSync` (waiting for disk), while up to 26 sit in
`LWLock:WALWrite` — queued for the lock that serialises WAL insertion. That is
contention from many small transactions, and no durability or buffer setting
removes it. The rest sit in `Client:ClientRead`: idle, waiting for a worker to
send the next statement.

So the ceiling is statements per step, on both sides at once — each write
transaction contends for the WAL lock, and each round trip stalls the worker
that issued it. That is what the merged completion write above acts on.
`synchronous_commit = off` is the one setting worth knowing
about, and it buys ~7% by dropping the last few hundred milliseconds of commits
on a Postgres crash (not a client crash); a step could re-run. Decide that
deliberately or leave it on.

**Two things to check before drawing conclusions from your own numbers**, both
of which produced convincing wrong answers here first: `go test` caches results
when nothing Go can see has changed, and a Postgres setting is invisible to it —
always run with `-count=1` (`make stress` does). And the `submitted` line must
read ~100% of target; if the rig's own submitter is the limit, "achieved" is
measured against load that was never offered.

## What it does not measure

The HTTP request path, SSE fan-out to browsers, and anything a connector does
over the network. Steps here occupy a worker for `STRESS_STEP_MS` and touch
nothing external — deliberately, so what is measured is dazyflow rather than
somebody's API. The egress limiter is likewise out of scope; see
`DAZYFLOW_EGRESS_RATE_PER_MIN` for why a connector-heavy install may hit a
ceiling long before this rig's.
