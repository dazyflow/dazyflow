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

Measured on an i3-4030U, four cores, 2026-09-05:

| | Before | After |
|---|---:|---:|
| `ManifestsForTenant` | 745µs, 492KB, 1332 allocs | **106µs, 111KB, 187 allocs** |
| `SubmitValidation` | 820µs, 515KB, 1413 allocs | **176µs, 134KB, 268 allocs** |

Absolute numbers are hardware-specific; the ratio is the point. `ValidateRuntime`
itself was never the cost — the snapshot in front of it was.
