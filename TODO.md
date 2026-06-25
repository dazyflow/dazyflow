# Web UI/UX — professional polish TODO

Prioritized gaps found by walking the running app. Strong already: node
inspector, Ctrl+K step palette, run detail (timeline/log/retry), a11y labels.

## 1. Flows overview cards (landing page — weakest, first impression) — DONE
- [x] Consistent card layout (equal height via pinned footer; clamped desc)
- [x] Operational signal per card: last run time + outcome dot, recent-runs
      sparkline (RunSparkline). Next-scheduled already shown via schedule chip.
- [x] Search / sort (recent/name/status) / status filter on the flows list
- [~] One-line summary fallback: shows last-run line always; trigger-text
      fallback skipped (needs per-flow graph fetch — not worth N requests)
- [ ] Per-card actions menu (duplicate / rename / delete) — BACKEND-BLOCKED:
      no flow delete/rename/duplicate API exists in client or daemon

## 2. Canvas minimap + auto-layout — DONE
- [x] React Flow MiniMap (themed, pannable, node swatches tinted by run status)
- [x] "Tidy" button — layered left-to-right auto-arrange by dependency depth,
      preserves vertical order, re-fits view

## 3. Runs filtering for debugging at scale — DONE
- [x] Text search (run id / flow name) — client-side over loaded rows + no-match state
- [x] Per-flow filter — server-side via listRuns (paginates correctly)
- [x] Result count ("N+ runs"); Load-more pagination already existed
- [~] Timestamps: kept ABSOLUTE in runs table per user's hybrid decision
      (relative stays on flow cards w/ absolute on hover). Convention honored.
- [ ] Date-range filter — BACKEND-BLOCKED: runs API has no date params;
      client-only over loaded rows would be misleading. Skipped.

## 4. Global command bar / search — DONE
- [x] Global ⌘K command bar (CommandPalette) — jump to any page or flow,
      grouped "Go to" + "Flows", keyboard nav, reuses step-palette styling.
      ⌘K opens it everywhere EXCEPT the editor (step palette keeps ⌘K there),
      per the agreed keybinding split.

## 5. Aggregate home / dashboard — DONE
- [x] /overview Dashboard: 4 stat cards (runs today, success rate, needs
      attention, approvals waiting) + "needs attention" failed-runs list +
      recent activity + flow inventory footer. Nav entry "Overview" (Gauge),
      added to command bar, and made the landing for returning users
      (new users still get /welcome onboarding). All derived client-side.

## 6. Header polish — PARTIAL (scoped to highest-value, well-bounded piece)
- [x] Help button in header + "?" key → keyboard-shortcuts reference modal
      (ShortcutsModal), General + Flow editor sections, platform-aware mod key.
- [~] Notifications / activity — the pending-approvals badge on the Approvals
      nav already covers the main "something needs you" case; a full
      notifications feed is a separate, larger build. Deferred.
- [ ] Breadcrumbs — DEFERRED: the IA is flat (sidebar + page title); breadcrumbs
      add little. Low fit.
- [ ] Undo/redo in editor — DEFERRED: needs a real graph history stack
      (snapshot/coalesce, autosave interplay). Substantial feature, not polish.
      History (version snapshots) already exists for coarse rollback.

## 7. UX
- [ ] Go through app and trigger all kinds of errors you can come up with, and then ensure that the error messages are non-techie friendly so they can solve issues without a coder
- [ ] Add UI to set a subdomain, such as "klahr.dazyflow.app"

---

# Platform / iPaaS hardening TODO

Derived from the "what does an iPaaS competing with Zapier have to solve"
analysis, **recalibrated for the hosted multi-tenant model** (NOT self-hosted).
Hosting means a SHARED polling fleet, SHARED egress IPs, and CROSS-TENANT
rate-limit contention — so the polling and rate-limit items below are real
severity, not theoretical. Already strong: typed ports + explicit
`${upstream...}` mapping (no doomed "universal schema" attempt), per-drop
egress allowlist + SSRF guard, job-ID idempotency key for outbound writes.

## 7. Per-external-API rate limiting & 429 handling — DONE
- [x] Per-(tenant, external host) token-bucket limiter (`drops/net/ratelimit.go`)
      applied to EVERY outbound call: net.Do, http_request, and webhook_send all
      Acquire a slot before dialing. Tunable via DAZYFLOW_EGRESS_RATE_PER_MIN /
      _BURST / _CONCURRENCY (cmd/dzd); safe non-zero defaults, =0 disables.
- [x] Honor `429`/`503` + `Retry-After` / `RateLimit-Reset` (delta + epoch) +
      `RateLimit-Remaining: 0` — ObserveEgressResponse sets a per-host cooldown
      AND feeds Retry-After to the engine backoff via a ctx RetryHint
      (core/retryhint.go); maybeScheduleRetry takes max(backoff, Retry-After).
- [x] Bound fan-out by RATE — a fanned step issues one Acquire per item, so the
      token bucket drip-paces it; the count cap (maxAutoFanItems) stays as the
      hard ceiling.
- [x] Per-(tenant,host) concurrency cap bounds simultaneous in-flight calls so
      one tenant's burst can't get the shared egress IP throttled for everyone.

## 8. Polling at scale — DONE
- [x] Poll jitter / spread: deterministic per-(tenant,ws,flow,node) jitter
      (hash-based, ≤ interval/4, capped 60s) pulls each poll's FIRST fire
      EARLIER within its interval at enrollment (scheduler.go), so a mass
      enrollment doesn't align thousands of flows onto the same tick. Stable
      across rescans + leader failover; never adds latency.
- [x] Conditional-request caching: opt-in `cache_key` on http_request stores
      the server's ETag/Last-Modified (drops/net/httpcache.go) and sends
      If-None-Match / If-Modified-Since on GET/HEAD. A 304 short-circuits to a
      bodyless result (status 304) downstream can branch on — a no-new-data
      poll costs ~0. Validators persist per (tenant,flow,node,cache_key).
- [x] Adaptive poll backoff: fetcher nodes (gform, homeassistant_state_changed,
      conditional http_request) write a per-flow "found data?" marker
      (pollstate package); the scheduler folds it into an empty-streak and
      widens the effective interval (2×/4×/8× after a 3-fire grace, capped 8×
      and at the 1-year ceiling), snapping back to base the moment data
      returns. Graph-scoped so a downstream fetcher can speak for the run.

## 9. Idempotency for APIs without an idempotency header — DONE
- [x] Engine-side dedupe: a new `Manifest.DedupeWrites` flag + `core.WriteDedupeStore`
      interface (in-memory TTL/bounded impl `engine.NewMemoryWriteDedupe`, wired
      onto the shared engine in cmd/dzd). buildAndExecute records a node's
      successful result keyed by its job ID; an expired-lease reclaim / crash
      re-running the SAME job ID returns the recorded result instead of re-firing.
      Opted in: twilio_send_sms, gmail_send_email, discord_send_message,
      sheets_append_row, homeassistant_call_service.
- [x] Contract = AT-LEAST-ONCE for all five (documented on the flag + interface):
      Put happens AFTER the write succeeds, so a crash in the tiny window
      between the API returning and the result being recorded can still
      re-fire; recording before would risk at-most-once (silently dropping a
      message), the worse failure for these connectors. Scope note: in-memory
      store is single-node (one shared engine); multi-node cross-process
      reclaim needs a shared store behind the same interface (follow-up).

## 10. Connector breadth & contract quality — ONGOING (the real content moat)
- [ ] Track connector coverage vs. demand; the typed-port stance is sound, so
      this is a grind on count + output-shape quality, not an architecture risk.
      (Still ongoing — adding connectors is the perpetual content work.)
- [x] Output-shape contract tests (drops/output_contract_test.go), the OUTPUT
      twin of examples_contract: for ALL 108 built-ins at once — (a) ports are
      well-formed (unique non-empty ids, a Label, non-empty MIMEs) and (b) a
      drop never EMITS an output port it didn't declare (caught delay's
      hand-rolled `pass` pin; the universal PassPort is allowed). Runs every
      drop against the adversarial corpus and asserts on each StatusOK result.

## 11. Plain-English failure UX — DONE
- [x] Map raw transport errors → human cause + action (web/src/lib/explainRunError.ts,
      shown in RunDetail's failure banner + per-node expander). Already covered
      429/timeout/auth-expired/network/404/permission/secret/oauth; ADDED a
      transient 5xx headline (502/503/504/bad gateway/gateway timeout →
      explain.serviceUnavailable, "their side, try again shortly"). +tests.
- [x] Auto-retry vs needs-you, distinctly: the node view now exposes
      `will_retry`/`retry_at` (from a requeued record's AvailableAt+Attempt);
      RunDetail shows an "Auto-retrying — next attempt in Ns" pill on a node
      between attempts, and the terminal-failure banner says "Failed after N
      attempts" + "This step won't retry on its own — fix it, then Retry from
      failure." (A still-retrying node leaves the run running, not failed, so
      the two states never collide.)
