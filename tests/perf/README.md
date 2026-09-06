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

Two more live beside the code they measure, because they need a real store
rather than the catalog: `daemon/httpbench_test.go` carries the request-path
benchmarks (they need the gateway's unexported wiring) and
`engine/jobstore/listbench_test.go` and `workspace/flowlistbench_test.go` carry
the polled reads below.

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

## The polled reads (2026-09-05)

The passes above all measured a request a person waits for once. These measure
the ones a browser repeats: the run views poll every two seconds while a run is
live, and the sidebar's flow list loads on every page.

Both were the same defect, and neither was visible in a request count or a
statement count — only in what each statement dragged back. A run record pins
the flow JSON it ran at submit, tens of kilobytes, TOASTed and compressed in
Postgres. The run-list read fetched and decompressed all of it for every row,
to render an id, a status, three timestamps and an error code.

`GET /api/v1/me/runs` through the real handler stack over a real Postgres, with
a 40 KB flow (`daemon/httpbench_test.go`, n=6):

| | Before | After |
|---|---:|---:|
| Page of 20 (the default) | 12.48ms | **5.41ms** (-57%, p=0.002) |
| Page of 200 (a busy workspace) | 71.05ms | **9.11ms** (-87%, p=0.002) |
| Allocated per request, page of 200 | 6.33 MB | **282 KB** (-96%) |

At the store (`engine/jobstore/listbench_test.go`), the shape is the point —
**the narrow read is flat in flow size where the old one is linear**:

| Page of 200 | 12-step flow (4.8 KB) | 100-step flow (40 KB) |
|---|---:|---:|
| Full records | 20.5ms, 1.60 MB | 107.6ms, 10.38 MB |
| Summaries | **4.06ms, 166 KB** | **3.88ms, 166 KB** |

Three neighbours had the same shape. The run-detail header is polled beside the
list and shows nothing the payload could answer (1597µs → **586µs**, -63%); the
per-submit admission check materialized up to 200 whole records to produce one
integer (3.36ms → **0.36ms**, 268 KB → 1.2 KB); and the promoter sweep, which
runs every couple of seconds on every replica, read a page of 200 whole records
to collect the set of tenant names in them.

The flow list was the same question one layer over: it loaded each flow whole
and then looked up its published pointer separately. Fifty flows, 30 steps each
(`workspace/flowlistbench_test.go`, n=6):

| | Before | After |
|---|---:|---:|
| Postgres graph store | 53.36ms | **15.89ms** (-70%, p=0.002) |
| git store | 29.48ms | **22.79ms** (-23%, p=0.002) |
| Allocations (git, 50 flows) | 56.9k | **33.8k** (-41%) |

On Postgres that is **151 round trips reduced to 1** — head revision, content
and published pointer were a query each, per flow. On git it re-read
`.git/HEAD` and re-decoded the same commit and tree once per flow. What is left
on both is decoding the flows themselves, which the list genuinely needs: a
flow's trigger nodes are what decide whether it shows as live.

**Both are contracts, not call-site fixes.** `core.RunSummaryReader` and
`workspace.Store.ListAtHead` are implemented by both backends of their
respective stores and pinned by the existing conformance suites, which require
the narrow read to agree — row for row — with the full read it replaces. A
projection that quietly disagreed about which runs exist would show a different
list depending on which backend an install runs.

One measurement note. The run-list benchmark needs a real Postgres and skips
without `DAZYFLOW_TEST_DB`: over the in-memory store both paths read the same
objects, so the projection is free there **by construction** and the cost it
removes — transferring and detoasting a JSONB column — does not exist. A
benchmark that ran green on the memory store would have measured nothing.

## The run viewer's timeline (2026-09-05)

The endpoint beside the header above — `GET /api/v1/me/runs/{id}/nodes`, the
step timeline, re-asked for every two seconds while a run is live. Over a real
Postgres, a 40 KB flow and a hundred steps (`daemon/httpbench_test.go`, n=6):

| | Before | After |
|---|---:|---:|
| `ListRunNodes` | 5.722ms | **3.370ms** (-41%, p=0.002) |
| Bytes allocated | 878 KiB | **440 KiB** (-50%) |
| Allocations | 6,417 | **3,164** (-51%) |

Two independent causes, and the profile is what separated them. Of the
handler's time, 34% was `fillRunNodeInputs` — which reconstructs what each
step received, needs the flow the run pinned at submit, and was fetching that
record and decoding the whole flow JSON **on every poll**, for a document that
cannot change for the life of the run. The workers have shared one parsed copy
per process since the execution-path work (`RunCache`, keyed by run and
interned by payload bytes so runs of one flow share a parse); the read path now
shares the same cache. That alone is -28%, and a Postgres round trip per poll.
The lesson generalizes: a cache built for the write path is worth checking
against the read path that asks the same question.

The rest was the over-fetch of the run list one layer down. The node read
returned twenty columns and decoded two JSON documents per step so the view
could render eight fields; nine of the columns are ids the caller already holds
or queue bookkeeping no view shows, and the `job` document was decoded whole to
reach one member of it, which Postgres can project instead (`job -> 'input'`).

`core.NodeRunReader` is the contract, implemented by both backends and pinned
by the conformance suite the same way `core.RunSummaryReader` is: the narrow
read must agree, step for step, with the full read it replaces. The comparator
is hand-written, because `core.NodeRun` carries pointers and maps and `==`
would compare them by identity — the trap that made the run-summary conformance
pass on Memory and fail on Postgres with two values that printed identically.

## The web bundle, per language (2026-09-05)

Not a Go benchmark — measured off `npx vite build` output, gzipped at level 9,
summing the entry chunk and everything `dist/index.html` preloads.

| | Before | After |
|---|---:|---:|
| First paint, gzipped | 272.2 KB | **228.2 KB** (-16%) |
| JavaScript to parse | 739 KB | **606 KB** (-18%) |
| Editor / apps / runs, extra | +91.6 KB | **0** for an English reader |

Both UI catalogues (~45 KB gzipped each) sat in an eagerly loaded chunk, and
the Swedish drop vocabulary — the translation of every step's label, subtitle,
description, port, field and enum, 91.6 KB gzipped — sat in the chunk
`lib/dropText.ts` pulled in, which the editor, the apps pages and the run views
all import. So every visitor downloaded and parsed every language.

Only the fallback catalogue is bundled now; the rest are code-split per
language, and `i18n.setLanguage` fetches a language's catalogue **and** its drop
vocabulary before changing to it, so no screen paints raw message keys. A
Swedish reader transfers the same bytes as before in two more requests, both
immutably cached after the first load. To re-measure:

```sh
cd web && npx vite build
cd dist/assets && for f in <entry and preloads named in ../index.html>; do
  printf '%s %s\n' "$(gzip -9 -c "$f" | wc -c)" "$f"
done
```

## The badge that fetched an inbox (2026-09-05)

The same over-fetch class as the polled reads above, one level up: not a read
that drags back a column nobody renders, but a read that returns a whole
**list** where the caller renders its `.length`.

The approvals badge in the sidebar is the most repeated authenticated request
the product makes — every signed-in tab, every 30 seconds, and again on each
navigation. It was served by `GET /api/v1/approvals/pending`, the inbox's own
listing, which carries for every parked step the prompt, the canonical approve
URL and the **value the flow stashed on it** — the refund, the submission, the
draft reply the inbox exists to show you. Up to 200 of them, each JSON-marshalled
once by `approvalContextPreview` just to measure it against the 4 KB cap, then
again into the response. Nothing on it reached the badge except the count.

`GET /api/v1/approvals/pending/count`, over a real Postgres, contexts sized just
under the preview cap:

| Parked approvals | List | Count |
|---|---:|---:|
| 25 | 2.80ms, 841 KB, 1734 allocs | **0.350ms, 10.6 KB, 68 allocs** |
| 200 | 19.1ms, 6.67 MB, 13170 allocs | **0.875ms, 11.2 KB, 68 allocs** |

**The shape is the point, as it was for the polled reads.** The list is linear
in what is parked (2.80 → 19.1ms, 6.8x); the count is nearly flat, and its
allocations are *exactly* flat — 68 either way — because no row is ever
materialized. So the old number gets worse as a workspace's approval queue
grows, which is precisely when it is being polled hardest.

Live A/B against a real `dzd` and Postgres, 200 parked, 24 concurrent, 3 reps
(spread under 1%):

| | List | Count |
|---|---:|---:|
| Throughput | 146.5 rps | **1702 rps** (11.6x) |
| Mean latency | 165.0ms | **14.1ms** (-91%) |
| p95 | 244ms | **23.2ms** |
| Wire bytes, gzipped | 719 B | **39 B** |

`core.NodeRunReader` gained `CountNodeRecords`, the same optional-extension
idiom as `RunSummaryReader.CountGraphRuns`, with `core.CountNodeRecords` as the
paging fallback so a store without it stays correct. Both backends implement
it and the conformance suite pins the count to the **length of the list it
counts**, filter for filter — that equality is the safety argument, because a
count that selected differently from its list would render a badge disagreeing
with the page it links to, and would do it on only one backend.

Two details are load-bearing rather than incidental:

- **The 200 ceiling is carried through as a ceiling, not dropped.** The list was
  capped at 200, so an uncapped count would make the badge claim a number the
  inbox does not show. Both stores clip, and a test asserts `min(parked, 200)`
  at 260 parked — the agreement check alone would be satisfied by both reads
  being broken the same way.
- **The filter already lived in the store** (`jsonb_exists(result->'output',
  'pending_url')`, which is what tells an await_approval node from a parked
  subgraph caller). That is what makes the count possible at all: a predicate
  applied in Go would have needed the records it was trying not to read.

Two more callers of the same shape went with it. The dashboard's "approvals
waiting" tile fetched the whole inbox for one number; and the support badge,
for an agent, counted a page of full ticket rows — where `QueueSummary` already
existed, is cached with a 5s TTL, and is *uncapped*, so the badge got cheaper
and more correct at once (a queue longer than the store's page limit had been
under-reporting itself). A requester's own tickets are few enough that the list
is the count, and that one was left alone.

## What a page actually waits on (2026-09-06)

Found by driving the real app headless (playwright-core against the
`chromium_headless_shell` build, per the notes in CONTRIBUTING) with every
`/api/` request recorded, then re-measuring each endpoint directly against a
live `dzd` — because the dev server's proxy and React's StrictMode both distort
the browser-side numbers. **StrictMode double-invokes effects in development,
so identical repeated URLs are an artifact; two requests to DIFFERENT URLs are
real.** That distinction is what separated the findings below from noise.

Ranked by server cost at 8 concurrent (30 flows of 25 steps, 1,500 runs), two
endpoints stood out and everything else was under 22ms:

| Endpoint | Mean | Bytes returned |
|---|---:|---:|
| `GET /me/schedules` | **148.0ms** | 42 B |
| `GET /me/flows` | **82.8ms** | 348 B |
| `GET /drops` | 21.9ms | 267 KB |
| `GET /me/runs?limit=200` | 17.2ms | 3.6 KB |
| everything else | 0.7–13ms | — |

### One flow at a time, a third time

`ListSchedules` still loaded every flow with a `Load` per id — the same loop
`workspace.Store.ListAtHead` was built to replace, with two callers migrated
and this one missed. Live, 8 concurrent: **148.0 → 82.1ms (-45%)**, throughput
**54.2 → 98.3 rps**. It needs each flow's whole graph (a schedule is a trigger
NODE inside one), so it takes the batch read with an empty env — no pointer is
involved, a schedule is read off the flow's current content.

### The workspace lock is the real ceiling — measured, not yet fixed

Both endpoints then sit at ~82ms, which is not a coincidence: they pay the same
serialized workspace read. Scaling concurrency against `/me/schedules` shows it
exactly — **throughput is pinned at ~98 rps from c=1 to c=16 while mean latency
scales linearly** (12.0 → 20.6 → 41.3 → 81.1 → 160.3ms). That is queueing
behind one lock, not work.

The lock is correct and must stay: `gitBackend.mu` is a full `sync.Mutex` **on
purpose**, because go-git's object LRU is mutated *during reads*, so two
readers race. So the fix cannot be an RWMutex — it has to be needing the repo
less.

Where the serialized time goes, measured on 30 flows x 25 steps:

| | Time | Allocations |
|---|---:|---:|
| `Store.Head()` | 0.09ms | 98 |
| `Store.ListAtHead("")` | 14.5ms | 17,163 |

A CPU profile puts **JSON decode at 78% of `listAtHead`** and the git tree walk
at the rest. Per flow, three decode shapes:

| Decode | Time | Bytes | Allocs |
|---|---:|---:|---:|
| Full `core.Graph` | 298µs | 29,375 | 514 |
| Params omitted entirely | 66µs | 2,332 | 13 |
| **Params as `json.RawMessage`, decoded only for trigger modules** | **107µs** | 8,910 | 38 |

So the flow list can be **~2.8x cheaper** with no cache, no invalidation and no
shared state — the cost is `map[string]any` params on ordinary steps, which no
list caller reads. The third row is the one to build: `classifyTriggers` (via
`FlowRunStatusPublished`) genuinely needs `cron`, `interval_seconds`,
`public_form` and the webhook secrets, but only on trigger nodes — one or two
out of twenty-five.

**Built** — see "The flow header projection" below.

REJECTED while looking, so they are not retried:
- **Memoizing the flow list on HEAD alone.** `DropSuggestions` already memoizes
  per HEAD and is right to, because it reads content only. `ListFlowSummaries`
  also reports `Published`, which comes from an env TAG — and publishing moves
  a tag WITHOUT moving HEAD, so a HEAD-keyed memo serves a stale "needs
  publish" badge. Any cache here must read the env pointers fresh; only the
  decode may be keyed on HEAD.
- **Serving `/me/schedules` from the `flow_schedules` projection table.** It
  holds cron/tz/interval per flow, but not node_id, flow name, icon, or the
  disabled flags — and disabled triggers are not enrolled at all, while the UI
  must still show them.

### Asking before the answer can be right

The sidebar fired its flow list and its approvals badge on every page against
an **unresolved org**: `activeWorkspace` falls back to the default the moment a
token exists, before `whoami` lands, so each effect ran once for the wrong
scope and again for the real one. The first answer was always discarded — and
the flow list is the most expensive read on the page. Both now wait for `me`.

Measured by re-driving the app, API calls per page:

| Page | Before | After |
|---|---:|---:|
| Dashboard | 22 | **18** |
| Flow list | 19 | **15** |
| Run list | 18 | **14** |
| Run detail | 22 | **18** |
| Approvals | 20 | **16** |
| Editor | 14 | **10** |

Four fewer per page (two, discounting StrictMode's doubling), and the unscoped
`?tenant=` flow-list read now happens once at boot instead of on every
navigation.

## The flow header projection (2026-09-06)

The change the section above measured and left for later. Four reads list a
workspace's flows — the sidebar's list, the schedules list, the drop-suggestion
miner and the visibility filter — and every one of them decoded each flow
whole, building a `map[string]any` for the params of every ordinary step. None
of them reads those. A twenty-five-step flow has one or two trigger steps and
twenty-odd ordinary ones, and the ordinary ones are the whole cost.

`core.UnmarshalGraphHeader` keeps params as raw bytes and decodes them only
where `core.IsTriggerModule` says a list caller will read them. That set is not
a new list to maintain: it already exists, and an existing test
(`TestEventTriggerModulesMatchCatalog`) keeps it in lockstep with the catalog.
It is also exactly right — the params the four callers read are the cron
expression and timezone, the poll interval, the webhook's secret and
public-form flag, and the per-node `disabled` switch, all of which live on
trigger steps.

`workspace.Store.ListHeadersAtHead` is the read; both backends share one
`decodeFlow` so the two cannot decode differently.

Store level, 30 flows x 25 steps on the git backend:

| | Full `ListAtHead` | `ListHeadersAtHead` |
|---|---:|---:|
| Time | 15.9ms | **8.8ms** (-45%) |
| Allocations | 17,164 | **2,914** (5.9x fewer) |

Live A/B against a real `dzd`, 30 flows, 8 concurrent, 2 reps each:

| | Before | After |
|---|---:|---:|
| `GET /me/flows` | 93.2 rps, 86.6ms mean | **132.1 rps, 60.9ms** |
| `GET /me/schedules` | 91.1 rps, 88.6ms mean | **126.7 rps, 63.5ms** |

And the ceiling itself moved, which is the point — `/me/flows` across
concurrency:

| | c=1 | c=4 | c=16 |
|---|---:|---:|---:|
| Before | 11.71ms / 85.5 rps | 43.94ms / 91.5 rps | 172.62ms / 94.8 rps |
| After | **8.46ms / 118.5 rps** | **31.33ms / 128.2 rps** | **123.54ms / 131.5 rps** |

Throughput is still flat across concurrency, because the workspace mutex is
still there and still correct. What changed is what a reader holds it FOR, so
the flat line sits 39% higher.

### Why this is safe

The risk was never the speed, it was that these graphs reach
`AuthorizeGraphView`, whose Visibility and Owner decide who may see a flow. So
the projection is pinned from three directions:

- `TestGraphHeaderMatchesFull` decodes a fixture both ways and requires the
  header to equal the full decode with ordinary params dropped — nothing else
  may differ — and separately that `FlowRunStatusOf` agrees between them.
- `TestGraphHeaderFixtureCoversEveryField` reflects over `Graph` and `Node` and
  fails if the fixture leaves any field zero, because a field nobody sets would
  pass the equality test by being absent from both sides. Add a field to either
  type and this test tells you to cover it.
- The workspace conformance suite runs `ListHeadersAtHead` against **both
  backends** and requires the same flows, the same env pointers, and the same
  graphs modulo ordinary params — plus a spot check that the cron step keeps
  its params, that an ordinary step does not, and that Owner and Visibility
  survive.

Verified end to end as well: `/me/flows`, `/me/schedules` and
`/me/flows/suggestions` return **byte-identical** responses before and after,
against a workspace seeded with real cron triggers and a mixed-module flow so
the schedules list and the adjacency miner both had real output to produce
(4,010 / 10,172 / 112 bytes).

`FlowHeader` is a distinct type from `FlowAtHead` rather than a flag on it, so
the elision is visible where the value is used: an elided step has
`Params == nil`, indistinguishable from one that genuinely has none, so a
header is for LISTING flows and never for running, editing or validating one.
`Load` and `ListAtHead` remain the full reads.
