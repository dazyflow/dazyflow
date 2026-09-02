# Adversarial (chaos) flow suite

Flows built to crash, hang, recurse or exhaust the daemon, run against the
real stack (`Service` + `Worker` + engine + native drops) with a hard
deadline on every case, so a hang fails instead of wedging the suite.

Opt-in, and deliberately outside the default `go test ./...`: several cases
are timing-sensitive (they assert on wall-clock and log volume) and would
flake under CI contention, and one runs a deliberate memory bomb. The
guarantees themselves are covered by ordinary unit tests in the packages
that own them — see **Where each fix is guarded** below — so CI protects the
fixes and this suite is the thing you re-run when hunting for new holes.

```sh
DAZYFLOW_CHAOS=1 go test ./tests/chaos/ -v -timeout 30m
# The doubling bomb at full size (2 GiB of payload) against the shipped ceiling:
DAZYFLOW_CHAOS=1 DAZYFLOW_CHAOS_OOM=1 go test ./tests/chaos/ -run TestOOM
```

A failing test here is a finding, not a flake. Everything currently
passes.

## What it covers

| Test | The attack | What now stops it |
|---|---|---|
| `TestOOM_DoublingTemplateBomb` | Each step's text names its predecessor twice, doubling the payload per hop — 22 steps killed the process with `fatal error: out of memory` in the template resolver (a runtime throw no recover catches, so every tenant's runs died with it) | `core.MaxValueBytes` bounds one value at both ends — template expansion and the node result — so the compounding step fails with `value_too_large` |
| `TestPassPin_StateAmplification` | 4 MiB threaded through 50 steps stored 209 MiB of run state, ~4 GiB at the node ceiling: every step stores its own copy | `core.MaxRunStateBytes` bounds the run; the step that crosses it fails with `run_state_too_large` |
| `TestIllegalWiring_IsRefusedNotRun` | 200 wires into a single-value input (ran and reported **success**, silently keeping one), duplicate wires, a typo'd `on_error` (silently abort), a MIME-incompatible wire, an edge to a port that doesn't exist | `core.ValidateRuntime` on both the save and submit paths, and the canvas refuses the second wire while dragging |
| `TestUnknownModule_FailsAtTheStep` | A module the catalog doesn't have — deliberately still accepted, because a tenant's runner and MCP drops live outside the default palette | It fails at the step, cleanly, instead of hanging the run |
| `TestDenseGraph_DispatchStaysUsable`, `TestEdgeBomb_IsRejected` | 400 no-op steps with 79,800 wires took 2m43s of CPU (≈50 min extrapolated to the 1000-node ceiling); 500k wires between two steps saved in 1.6s | Per-run wiring index + per-pass record cache + the worker's graph cache (same flow: 5s), and a 5000-connection ceiling puts the shape out of reach |
| `TestWideFanIn_DoesNotFloodTheLog` | One "waiting: predecessor …" line per dependent per completion — thousands of lines a second on a wide flow | The trace is behind `DAZYFLOW_DEBUG_DISPATCH` |
| `TestTriggerLoop_IsBroken` | A published webhook flow whose HTTP step calls its own trigger URL ran forever (~5 runs/s, climbing): each iteration is a fresh top-level run, invisible to the subgraph depth cap and fan-out budget | A self-directed call carries `X-Dazyflow-Trigger-Depth`; the endpoint refuses past `core.MaxTriggerChainDepth` |
| `TestWebhookSendLoop_IsBroken`, `TestFailureNotifyLoop_IsBroken`, `TestSelfDirected_RecognizesEquivalentURLs`, `TestAliasedSelfCall_LoopIsBroken` | The self-triggering flow through the doors the breaker didn't cover. The depth stamp was written by ONE drop, and decided "our own origin" by string-comparing `scheme://host`: the **Webhook** drop — whose whole purpose is POSTing to a URL — carried nothing (65 → 85 runs from one anonymous submission); `failure_notify.webhook` pointed at the flow's own form was a loop with **no step in it at all**, 155 → 652 runs over five seconds (~120/s sustained), since `fireFailureNotification` builds its own request and the throttle beside it covers the email channels only; and `https://flows.example:443/…` was "not us" when the base URL said `https://flows.example`, as was a trailing root dot, or the address the daemon answers on inside its container | The stamp moved into the shared client (`triggerDepthTransport` in `buildClient`), which every outbound call dials through — the Webhook drop, an upload or download with a method, a connection-configured sender, and the daemon's own failure webhook — and it SETS the header rather than filling it in, so a step can't hold the chain at zero from its own `headers` param. It compares a normalized origin (default port per scheme, trailing root dot, loopback aliases) against every origin that reaches us: `dzd` now registers its listen address alongside `DAZYFLOW_PUBLIC_BASE_URL`. The notifier has no run context, so it reads the failed run's `TriggerDepth` from its record |
| `TestDelay_RejectsAbsurdDurations` | `ms` past ~9.2e12 overflowed the duration and the step reported success instantly — "wait" became "don't wait" | Refused above a year, pointing at the Schedule trigger |
| `TestSubgraphRecursion_IsBounded` | A flow calling itself, two flows calling each other, a wide self-recursive fan-out | Held already: 8 levels of nesting, 1024 descendant runs per tree |
| `TestLoopBodyMisuse_Terminates` | A loop body wired back into the main flow, two loops sharing a body step, a parking or child-graph step inside a body | Held already: each fails cleanly rather than hanging |
| `TestParkedApprovals_DoNotStarveWorkers` | 20 runs parked on approvals, then an ordinary flow | Held already: a parked step holds no worker slot |
| `TestMergeChain_HitsTheValueCeiling` | The doubling bomb rebuilt out of Merge steps, no templates: 15 hops turned a 1 KiB seed into 18 MB of stored state, 581 MB and 1.7 GiB RSS at 20, a gigabyte at 21 steps. Both ceilings measured it as **16 bytes** — `core.ApproxValueSize` charged a struct one word without walking it, and `merge` emits `[]core.Ref` (so does `for_each`'s `results`) | `core.refSize` walks a Ref for its strings and its inline payload, and the reflect arm walks a struct's exported fields, so the step that crosses the limit fails with `value_too_large` |
| `TestTriggerChainDepth_SurvivesASubgraphHop` | `A --subgraph--> B --HTTP--> A's own trigger URL`, forever: the child run started at depth 0, so the counter never climbed, and each webhook run was a fresh top-level tree the lineage walk and fan-out budget couldn't connect to the last | `SubmitChild` reads the parent run's `TriggerDepth` and stamps it on the child |
| `TestDynamicPortsStep_FanInIsBounded` | 200 wires into one input port of a `subgraph` step — `validateManifests` skipped every port check on a `DynamicPorts` module, fan-in included, and `AssembleInput` kept the last wire | Fan-in needs no port list, so it now applies to a dynamic port too; the canvas holds an undeclared pin on such a drop to one wire |
| `TestVariadicFanIn_IsBounded` | 400 wires into `merge.items`; no variadic port in the catalog declares a `Max`, so only the 5000-connection cap bounded it | `core.DefaultMaxVariadicFanIn` (64) applies to any variadic port that declares no max of its own, on both the server and the canvas |
| `TestEditorMetadata_IsCapped` | A 2-node graph carrying 20,000 edge waypoints and 50,000 frames: the node and connection caps don't see editor metadata, which rides in every run record and is re-parsed each dispatch pass | `core.MaxEdgeWaypoints` and `core.MaxGraphFrames`; the size ceilings are also checked before the graph is walked, so an oversized graph is refused without being validated |
| `TestCatalogLessModule_StillObeysFanIn`, `TestCatalogLessModule_AssemblesOneValuePerPort` | 300 wires into one input of `runner.mystery_step`, two into an `mcp.` tool: stored, run, and reduced to whichever value was walked last — port rules are read off a manifest, and a module outside this daemon's catalog has none, so the one rule that needs no port list was skipped for exactly the steps the canvas can't see | Fan-in needs no port list, so it applies to a catalog-less module too, on the server and on the canvas. A port NAME we don't recognize is still the drop's business — one wire per port is all the data model carries |
| `TestTriggerArray_IsCapped` | 2000 identical `* * * * *` triggers on one flow → 2000 scheduler entries → 2000 runs a minute (50,000 validated clean in a 1.7 MB graph): nothing capped `len(Triggers)`, and entries were keyed by position in the array so duplicates each got their own fire | `core.MaxGraphTriggers` (32), and the scheduler keys an entry by the schedule itself, so identical triggers collapse into one |
| `TestBreakpointInPublishedFlow_DoesNotParkTriggeredRuns` | A published flow with a breakpoint and a cron trigger: every unattended fire parked and stayed non-terminal for good — nothing reaps a paused run (the reaper reads its un-dispatched dependents as outstanding work), and `runningGraphRuns` counts them, so a capped tenant lost its concurrency slots permanently | A breakpoint holds only a run somebody started and is watching (`JobRecord.Manual`); step mode, which is set per run through the Step API, is unaffected |
| `TestGraphBytes_AreCapped` | 501 nodes / 500 wires / 1000 frames carrying 64 KiB of params, label and env each: a **156 MiB** flow inside every count ceiling, 208 MiB of run records. The ceilings all COUNT things; nothing weighed the graph, and the only backstop was the 200 MiB request cap that exists for file uploads | `core.MaxGraphBytes` (16 MiB), measured with the bounded `ApproxGraphBytes` walk, so weighing a hostile graph costs the budget rather than the graph |
| `TestWaypointTotal_IsCapped` | 500 wires x 256 waypoints = 128,000 waypoints, 3.3 MiB of run records against 466 KiB for the same flow bare — and 1.28M waypoints (~21 MiB per run record) reachable at the connection ceiling, because the waypoint cap was per wire | `core.MaxGraphWaypoints` (20,000) alongside the per-wire cap |
| `TestIdentifierBytes_AreCapped`, `TestModuleNameBytes_AreCapped`, `TestPortNameBytes_AreCapped` | `TestGraphBytes_AreCapped` rebuilt out of IDENTIFIERS: 100 nodes with 256 KiB node IDs (26 MB), one node with a 32 MiB module name (34 MB), 200 wires naming 128 KiB ports between two catalog-less steps (52 MB) — measured as 500, 0 and 0 bytes and all saved clean. `ApproxGraphBytes` skipped node IDs and module names as "already bounded by the node and connection ceilings", which bound the COUNT, not the LENGTH: nothing validates a node ID (`ValidGraphID` covers the flow id only) and a catalog-less module gets no port rules at all | The walk charges every string the caller supplies — node IDs, module names, both endpoints and both ports of every wire, and the graph-level strings it also missed (`Language`, `Owner`, `Version`, `FailureNotify`) — and still stops at the budget |
| `TestTriggerNodeArray_IsCapped`, `TestPollTriggerNodes_AreCapped`, `TestTriggerArrayAndSteps_ShareOneCap` | `TestTriggerArray_IsCapped` closed with the STEP instead of the trigger: 200 identical `* * * * *` Schedule steps were 200 scheduler entries and 111 runs in a clock minute, 200 Poll steps at `interval_seconds: 1` were 200 entries and 112 runs in ten clock seconds. `MaxGraphTriggers` counted `len(Triggers)` only, and the collapse-identical-schedules key is graph-level — a step's entry is keyed by node ID, as it must be since each carries its own cursor | Trigger STEPS count against `core.MaxGraphTriggers` too, and against the SAME budget as the array, so splitting the flood across the two buys nothing. Which steps are triggers is a manifest property, so the rule sits in `validateManifests` — reached by every save and submit through `ValidateRuntime` |
| `TestHostedForm_FieldCountIsCapped` | 100,000 `form_fields` on a `webhook_input` step: one **unauthenticated** GET of the hosted form returned 9 MB in 1.6s, 10x what the graph stores, and ~180 MB at the 16 MiB graph budget. A submission was capped (`maxFormFields`, 50); the render was not | The declared list is cut to `core.MaxHostedFormFields` where it is read, so the page can never render more inputs than a submission could carry, and the trigger lint tells the author at save time |
| `TestFormLoop_IsBroken` | `TestTriggerLoop_IsBroken` through the other door: a flow whose HTTP step POSTs to its own `/form/` URL ran forever (65 → 85 runs over five seconds, climbing) from ONE anonymous submission — `/trigger` wants the flow's secret, `/form` wants the link. `handleForm` submitted at `TriggerDepth` 0 unconditionally, so the counter never climbed | The form endpoint reads `core.TriggerDepthHeader` and submits with it, exactly as the webhook listener does; `seedRun` then refuses past `core.MaxTriggerChainDepth` |
| `TestParallelWaits_DoNotStarveWorkers`, `TestQueuedWaits_DoNotMonopolizeThePool` | `Worker.Run` is a serial claim → process loop and `DAZYFLOW_WORKER_COUNT` defaults to 2, so a deployment executes two nodes at a time across every tenant — and `executeDelay` slept on the goroutine that claimed it. Two parallel Wait steps in ONE flow delayed an unrelated one-step flow by 6.5s; 60 queued 2s Waits left it unfinished after 45s | A step with nothing to do until a known time returns `core.StatusDeferred`; the worker requeues it at that horizon (`Claim`'s `AvailableAt`) and takes other work. The wait is anchored on `core.NodeEnqueuedAt`, which survives the requeue, so the re-execution finds no time left and finishes. A timeout the author DECLARED still binds — a wait that cannot fit inside it is refused at once |
| `TestGraphID_IsValidated` | Flow IDs `..`, `.`, `a/../../escape`, `with space`, 300 characters: nothing validated an ID between the API and `graphs/<id>.json` / the `graphs/<id>/published` tag, so one saved a flow outside `graphs/` that could never be loaded, published or deleted, and a 300-character ID worked in memory and failed with `ENAMETOOLONG` on disk | `core.ValidGraphID`, enforced at the save gate and again in the workspace store — the choke point every writer reaches the repository through |

| `TestFrameIDBytes_AreCapped`, `TestTriggerTypeBytes_AreCapped`, `TestOversizedGraph_RunRecordCost` | `TestIdentifierBytes_AreCapped` rebuilt out of the graph's two repeated SUB-RECORDS. The size walk charged a frame's title and colour but not its **ID**, and a trigger's cron, tz, secret and form fields but not its **type** — so 1000 frames with 1 MiB IDs was a **1.0 GiB** graph and 32 triggers with 4 MiB types a 128 MB one, both measured as **ten bytes** and saved clean. Nothing validates a frame ID (frames are editor-only boxes the engine ignores) and the scheduler ignores a type it doesn't recognize, so neither field is bounded anywhere else; `MaxGraphFrames` and `MaxGraphTriggers` bound how MANY sub-records a graph carries, not how big one is. 200 MiB of frame IDs submitted clean and cost **280 MiB of run records** for a single one-step run — 173,000x the same flow bare, on every fire | `ApproxGraphBytes` charges the frame ID and the trigger type, so "every string the caller supplies" now means the repeated sub-records too |
| `TestHostedForm_FieldNameLengthIsCapped` | `TestHostedForm_FieldCountIsCapped` closed with the other half of the field list. A field NAME has no natural length and the page emits each one four times (`for=`, the label text, `id=`, `name=`), so the amplifier came back INSIDE the count cap: 50 declared fields named 300 KB each is a ~14 MiB flow, inside the graph budget, that answered one **unauthenticated** GET with **60 MB in 0.6s** — repeatable by anyone holding the link | `core.MaxHostedFormFieldLen` (128) and `MaxHostedFormTitleLen` (200), applied where the count cap is. An over-long name is DROPPED, not truncated — a truncated name still renders and still posts, under a key the owner never typed, and two names sharing a prefix would collide — and the trigger lint names the step at save time |
| `TestManifestDeclaredFanIn_IsClamped` | `core.DefaultMaxVariadicFanIn` (64) applies only to a port declaring no `Max` of its own, so the drop DECLARING the port chose its own ceiling — and a manifest is not always ours: a remote runner's arrives over gRPC and its max is taken verbatim (`engine.portFromPB` does no clamping), as does an MCP host's. A port declaring `max: 1000000` put fan-in back exactly where it was before the default existed, bounded only by the 5000-connection cap, for precisely the steps outside the default palette | `core.MaxVariadicFanIn` (1024) is the ceiling no manifest can raise, applied in `validateManifests` where every manifest source converges, and mirrored on the canvas |

| `TestApprovalFanOut_IsBounded`, `TestApprovalFanOut_LeavesRealFlowsAlone` | An Approval step's recipient list is a comma-separated param, so its only ceiling was the graph byte budget it is charged against — **~650,000 addresses** — and the notifier sends **one message per address** in a serial loop, twice per approval (on park, then on the decision). Two things make it worse than a long list: the mail goes out through the **operator's** transactional mailer, not a connected account the author had to authorize, so any tenant can aim the deployment's own sending domain at arbitrary addresses; and the loop runs synchronously on the worker goroutine that parked the run (`OnNodeAwaiting`), so at a realistic SMTP round trip one parked run holds one of the two default worker slots for hours. 2000 addresses took 7.0s at a 2 ms round trip. Capping the step alone bounded the wrong unit — the same flood came back split across **steps**, since parallel gates all park in the same run: 40 gates with a full list each sent **2000 messages from one run**, with 50,000 in reach at the node ceiling, and nothing throttles approval mail the way `FailureEmailWindow` throttles failure mail | `core.MaxApprovalRecipients` (50) applied where the list is read, so both notifiers get the same list, and `core.MaxGraphApprovalRecipients` (200) bounds the whole run in `core.Validate` — the same shape as trigger steps and the Triggers array sharing one budget. A switched-off gate never parks, so it doesn't count; an ordinary flow (3 gates, 5 people each) is untouched |

| `TestApprovalPrompt_IsClippedForMail` | Capping the recipient COUNT bounds how many messages go out, not how big one is. The approval mail carries the step's prompt, which is read off the RUN RESULT rather than the graph — so the graph byte budget never touched it and its only ceiling was `core.MaxValueBytes` (64 MiB). Rendered into an HTML body AND a plain-text body and sent once per recipient, a 4 MiB prompt put **43.7 MB on the wire for five approvers** (10.4x the prompt), with 200 recipients allowed per run. A failing run's error message is the same shape, and rides to a third-party webhook as well as to mail | `core.ClipNotificationText` (4000 bytes) bounds a free-form string the daemon embeds in a notification, applied to the approval prompt and to the failure payload where it is built — so the mail and the webhook share the ceiling. Nothing a reader needs is lost: the mail's purpose is the link, and the Approvals inbox and run page show the full text |

| `TestFlowName_DoesNotBreakNotificationDelivery` | The flow's display NAME is the mail Subject and the node id the fact line beside it, both author-supplied and bounded only by the graph byte budget. A header is bounded far harder than a body — RFC 5321 caps a line at 1000 octets and a server that sees a longer one drops the connection — so the failure is not a big email but **no email**: a 2 MiB flow name sent nothing at all, so the approvers were never told the run was waiting and it could never be unblocked. Failure mail broke the same way, silently, on the same name | `core.ClipNotificationLabel` (200 bytes) on the name and step id where a notification is built, leaving plenty of room under the line limit for the header name and encoding |

Held under attack, and now pinned by `TestWiringsThatMustStayRefused`,
`TestFallbackEdge_WhenSourceSucceeds`, `TestAbsurdTimeouts` and
`TestDeepNesting_SurvivesTheRunPath`: 200 wires into a `pass` pin, a wire into
a trigger's output or its (absent) pass pin, a cycle laundered through a loop
body pin or built entirely from fallback edges, 65 wires into a variadic input
with no declared max, a fallback edge whose source succeeded (the dependent is
skipped, the run finishes), negative and `math.MinInt` timeouts on a node and
on a graph, deeply nested settings (including one holding a template
reference), and 9000-deep JSON parsed at run time — while a setting nested
past what the size walk covers is refused at the gate.

Also verified and unchanged: cycles are always refused (including behind a
disabled step and through a loop-body pin), and `for_each` clamps concurrency
to 64 and items to `limits.MaxRows`.

## Where each fix is guarded in CI

- `core/valuesize_test.go` — the graph-byte walk charges identifiers (node IDs,
  module names, edge ports) and the graph-level strings, and still stops at the
  budget.
- `core/validate_wiring_test.go` — trigger STEPS share the trigger cap with the
  Triggers array, and an ordinary step is not a trigger.
- `daemon/form_test.go` — the hosted form carries the trigger-chain depth and
  refuses past the cap, and its declared field list is capped at render.
- `drops/flow/delay_test.go` — a long wait defers instead of sleeping, resumes
  from the enqueue anchor, and still honours a declared per-node timeout.
- `daemon/worker_test.go` — a deferred node is requeued with a horizon and the
  worker slot goes back to the pool while the flow waits.
- `core/valuesize_test.go` — the value ceiling and the size walk (budget and
  depth bounded, so measuring a hostile value stays cheap).
- `core/validateruntime_test.go` — which wiring rules apply on the run path,
  which stay editor-only, and what a dynamic-port step is exempt from.
- `engine/valuelimit_test.go` — template expansion and node-result ceilings.
- `daemon/limits_test.go` — the connection ceiling and the trigger-chain cap.
- `daemon/dispatchindex_test.go` — one store read per predecessor per pass,
  deduplicated dependents, per-run topology reuse, bounded caches.
- `daemon/runstate_test.go` — the per-run state budget.
- `daemon/webhook_test.go` — the trigger endpoint's depth refusal (429).
- `drops/net/selforigin_test.go` — the depth header reaches our own origin
  and nowhere else, through the shared client rather than one drop: every
  spelling of our origin, every configured origin, the Webhook drop, and a
  hand-set header that must neither win nor leak.
- `daemon/failure_notify_loop_test.go` — the failure webhook carries the
  failed run's trigger depth to our own form, and nothing to a third party.
- `cmd/dzd/config_test.go` — the gateway's listen address counts as one of
  our origins.
- `drops/flow/delay_test.go` — absurd and overflowing waits.
- `web/src/lib/ports.test.ts` — the canvas's fan-in rule, the default variadic
  ceiling, and dynamic-port pins.
- `core/valuesize_ref_test.go` — a Ref is charged for what it carries, the walk
  still stops at the budget, and a struct's exported fields are walked.
- `core/validate_wiring_test.go` — duplicate wires (and the bounded report),
  the default variadic ceiling, dynamic-port fan-in, and the metadata caps.
- `daemon/limits_test.go` — a subgraph child inherits its parent's trigger
  depth.
- `core/validate_wiring_test.go` — fan-in on a catalog-less module, and the
  trigger / total-waypoint / graph-byte ceilings (including that a setting
  nested past the size walk's depth counts as over budget).
- `core/graphid_test.go` and `workspace/store_test.go` — which flow IDs are
  usable, and that the store refuses the rest whichever writer reaches it.
- `daemon/scheduler_internal_test.go` — identical schedules on one flow collapse
  to a single scheduler entry.
- `daemon/breakpoint_e2e_test.go` and `daemon/misc_test.go` — a breakpoint holds
  a watched run and lets an unattended one through.
- `web/src/lib/ports.test.ts` — the canvas holds a pin on a drop with no
  manifest to one wire.
- `core/valuesize_test.go` — the graph-byte walk charges a frame's ID and a
  trigger's type alongside the strings it already weighed.
- `daemon/form_test.go` — an over-long declared field name is dropped at render
  and the good fields around it survive.
- `core/validate_wiring_test.go` — a manifest-declared variadic max raises the
  ceiling above the default but not past `core.MaxVariadicFanIn`.
- `web/src/lib/ports.test.ts` — the canvas clamps a declared max at the same
  absolute ceiling.
- `daemon/approval_notify_test.go` — the approver list is capped where it is
  read, keeping the addresses the author listed first.
- `core/validate_wiring_test.go` — approval steps share one per-run recipient
  budget, a disabled gate doesn't count, and an ordinary flow still validates.
- `core/valuesize_test.go` — a notification string is clipped on a rune
  boundary, says it was cut, and leaves a real prompt alone; a label stays well
  inside the mail line limit.
