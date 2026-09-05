<!--
SPDX-FileCopyrightText: 2026 Angels' Ware
SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Benchmarks

CPU-side counterpart to [the stress rig](../stress/README.md). The rig answers
"how many steps per second, and what is the constraint"; these answer "what does
one call cost", for the calls a person waits behind.

They live here rather than beside the code they measure because the realistic
input is the whole drop catalog, and `engine` cannot import `drops` — the drops
import `engine`.

```sh
go test ./tests/perf/ -run XXX -bench . -benchtime 3000x -count=6
```

Compare two revisions with [benchstat](https://pkg.go.dev/golang.org/x/perf/cmd/benchstat):

```sh
go test ./tests/perf/ -run XXX -bench . -count=6 > new.txt
git stash && go test ./tests/perf/ -run XXX -bench . -count=6 > old.txt; git stash pop
go run golang.org/x/perf/cmd/benchstat@latest old.txt new.txt
```

## What is here

| Benchmark | What it stands in for |
|---|---|
| `ManifestsForTenant` | The catalog snapshot every validation, save, submit and palette request takes |
| `ValidateRuntime` | The wiring gate a 20-step flow passes on save and on submit |
| `SubmitValidation` | The pair, as one submit actually pays for it |
| `AuthenticateSession` | The auth chain every authenticated HTTP and gRPC request goes through, against a real Postgres |

`AuthenticateSession*` need `DAZYFLOW_TEST_DB` and skip without it. They come in
four shapes on purpose, because the interesting number is a difference:
`NoGate` is the session lookup alone, plain is the full chain, and `Cached` is
the full chain with the moderation memo on. Sizing anything from the plain
number alone would blame the session store for the lockout reads in front of
it.

Measured on an i3-4030U, four cores, 2026-09-05:

| | Before | After |
|---|---:|---:|
| `ManifestsForTenant` | 745µs, 492KB, 1332 allocs | **106µs, 111KB, 187 allocs** |
| `SubmitValidation` | 820µs, 515KB, 1413 allocs | **176µs, 134KB, 268 allocs** |

Absolute numbers are hardware-specific; the ratio is the point. `ValidateRuntime`
itself was never the cost — the snapshot in front of it was.

## The auth chain (2026-09-05)

The same lesson one layer up. Session validation was already memoized
(`CachingSessionStore`), so it looked handled — but the platform-admin lockout
gate in front of it was not, and it is *two* uncached primary-key reads per
request, one of which decodes four JSON columns to reach a single boolean:

| Per authenticated request | Before | After |
|---|---:|---:|
| `AuthenticateSession` (chain, serial) | 485µs, 4.7KB, 65 allocs | **2.6µs, 213B, 3 allocs** |
| `AuthenticateSessionParallel` | 258µs | **1.6µs** |
| `AuthenticateSessionNoGate` (session lookup only) | 1.4µs | 1.4µs |

The third row is the whole diagnosis: the part everyone had already optimized
was 1.4µs of a 485µs chain. `ModerationGate.CacheTTL` shares
`DAZYFLOW_SESSION_CACHE_TTL`, because it buys the same trade in the same place —
and the suspend/unsuspend handlers invalidate locally, so only *other* replicas
lag, by the same window sessions already lag by.

End to end against a real dzd and a local Postgres, `GET /api/v1/me`:

| Concurrency | Cache off | Cache on |
|---|---|---|
| 8 | 8.06ms mean, 952 rps | **6.70ms, 1136 rps** |
| 24 | 23.34ms mean, 980 rps | **18.65ms, 1214 rps** |

That is the floor for the win, not the ceiling: the database here is on
localhost. Every millisecond of real network distance to Postgres was being paid
twice per request.

## The response path (2026-09-05)

`daemon` carries the HTTP benchmarks (`httpbench_test.go`) because they need the
gateway's unexported wiring. They mount the routes **once**, the way
`ServeListener` does — `ServeForTest` remounts every route per call (~7.8ms), so
benchmarking through it measures the router, not the handler.

`GET /api/v1/drops` is the flow editor's palette and the API's largest body:

| | Before | After |
|---|---:|---:|
| Server time (body discarded) | 11.2ms | **7.95ms** (-29%, p=0.002, n=6) |
| Bytes on the wire | 1,040,322 | **267,472** (-74%) |

Two independent changes. The catalog was serialized **twice** into one response
— once as `drops`, once as the legacy `modules` alias — so it is now encoded
once and the same bytes are written under both keys; the wire bytes are
unchanged and a test asserts that byte-for-byte. And nothing in front of dzd
necessarily compresses, so it compresses itself.

Routing the single encoding back through a `json.RawMessage` was **slower** than
the double encode (13.4ms): v2 re-validates and reformats raw bytes. Measured,
not reasoned — as was the compression level, where `BestSpeed` gives up 42KB of
ratio (267KB vs 225KB) to halve the CPU, which is the right end of that curve
for a server.

## The response cache (2026-09-05)

Where the request above goes once compression is on. The palette body's cost
splits like this, measured stage by stage on the real built-in catalog:

| Stage | Cost | Share |
|---|---:|---:|
| `listDrops` (tenant map, switches, model overlay) | 0.15ms | 1% |
| `searchManifests` (sort) | 0.63ms | 4% |
| JSON encode (520 KB value) | 3.56ms | 24% |
| gzip of the 1.04 MB body | **10.69ms** | **71%** |

Compression dominates, and it recompresses bytes that are almost always the
ones compressed a moment earlier — so the compressed form is kept, keyed by a
fingerprint of the body itself. Hashing is what makes that affordable:

| Hash over the 1 MB body | Throughput | Cost |
|---|---:|---:|
| CRC32 Castagnoli (SSE4.2) | 12,802 MB/s | **0.08ms** |
| FNV-1a 64 | 470 MB/s | 2.2ms |
| SHA-256 | 207 MB/s | 5.0ms |

Results, `daemon/httpbench_test.go` plus a live A/B against a real dzd and
Postgres at 24 concurrent clients:

| | Before | After |
|---|---:|---:|
| `BenchmarkListDropsGzip` | 15.6ms | **5.0ms** |
| `GET /api/v1/drops` | 127 rps, 190.3ms mean | **366 rps, 65.9ms** |
| `GET /api/v1/catalog/drops` | 194 rps, 124.3ms mean | **369 rps, 65.3ms** |
| Revalidated request (`If-None-Match`) | 267,472 bytes | **0 bytes (304)** |

**Content-addressing is the design, not an implementation detail.** The catalog
varies by tenant, by platform drop switches and by the models a tenant's
credential can call; keying on the finished bytes means none of that has to be
tracked, and two tenants share an entry only when their catalogs are identical.
A generation counter across those three subsystems, and slice-identity of the
schema bytes, were both considered and rejected: they trade a
guaranteed-correct key for one that fails silently and tenant-visibly.

What is left is the encode, and **30% of it is v2 re-validating and reformatting
the `json.RawMessage` schema blobs the daemon produced itself** — the same
behaviour that made the `RawMessage` route slower than double encoding above.
That is a stdlib characteristic, not a call site to fix.

Two traps worth keeping. A 32-bit CRC alone is a weak validator, so the tag is
CRC plus body length. And the uncompressed path now pays the 0.08ms
fingerprint for nothing — it is kept because every browser sends
`Accept-Encoding: gzip`, so that path is only reached by hand-rolled clients.
