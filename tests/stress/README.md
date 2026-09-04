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

## What it does not measure

The HTTP request path, SSE fan-out to browsers, and anything a connector does
over the network. Steps here occupy a worker for `STRESS_STEP_MS` and touch
nothing external — deliberately, so what is measured is dazyflow rather than
somebody's API. The egress limiter is likewise out of scope; see
`DAZYFLOW_EGRESS_RATE_PER_MIN` for why a connector-heavy install may hit a
ceiling long before this rig's.
