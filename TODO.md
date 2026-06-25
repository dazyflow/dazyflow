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

---

# Platform / iPaaS hardening TODO

Derived from the "what does an iPaaS competing with Zapier have to solve"
analysis, **recalibrated for the hosted multi-tenant model** (NOT self-hosted).
Hosting means a SHARED polling fleet, SHARED egress IPs, and CROSS-TENANT
rate-limit contention — so the polling and rate-limit items below are real
severity, not theoretical. Already strong: typed ports + explicit
`${upstream...}` mapping (no doomed "universal schema" attempt), per-drop
egress allowlist + SSRF guard, job-ID idempotency key for outbound writes.

## 7. Per-external-API rate limiting & 429 handling — HIGH (shared egress)
- [ ] Per-(tenant, external host) token-bucket limiter in the engine, applied
      to outbound calls in `drops/net/do.go` — today only `daemon/ratelimit.go`
      (auth IP buckets) and a jobstore fairness cap exist; nothing paces
      third-party calls.
- [ ] Honor `429` + `Retry-After` / `RateLimit-*` headers on the connector
      retry path — grep shows ZERO handling today; a retried call just re-hits
      the ceiling. Feed Retry-After into the engine's backoff.
- [ ] Bound fan-out by RATE, not just count — `maxAutoFanItems = 1000`
      (engine/autofan.go) is a blunt count cap; a 1000-item for_each still
      bursts a 100/min API. Queue/drip per the limiter above.
- [ ] Shared-IP reputation risk: per-host concurrency caps so one tenant's
      burst can't get the platform's egress IP blacklisted for everyone.

## 8. Polling at scale — HIGH (shared fleet, was a non-issue when self-hosted)
- [ ] Poll jitter / spread: interval-anchored poll triggers (core/graph.go:192)
      will align thousands of tenants onto the same tick → thundering herd on
      both the scheduler and target APIs. Add per-flow jitter to the enrollment.
- [ ] Conditional-request caching (ETag / If-Modified-Since / cursor) so a poll
      that finds no new data costs ~0 against the target's rate budget — the
      core cost driver the analysis flags.
- [ ] Adaptive poll backoff: widen interval for consistently-empty pollers,
      tighten for active ones, to cut wasted calls across the fleet.

## 9. Idempotency for APIs without an idempotency header — MEDIUM
- [ ] Engine-side dedupe (store + check job/step key) for connectors whose
      upstream API has NO idempotency key — currently documented-but-unguarded
      in twilio_send_sms, gmail_send_email, discord_send_message,
      sheets_append_row, homeassistant_call_service. A retry there CAN double-fire.
- [ ] Decide+document the at-least-once vs at-most-once contract per such drop
      so the failure UI (#11) can tell the user which guarantee they have.

## 10. Connector breadth & contract quality — ONGOING (the real content moat)
- [ ] Track connector coverage vs. demand; the typed-port stance is sound, so
      this is a grind on count + output-shape quality, not an architecture risk.
- [ ] Per-connector contract tests for output schema stability (examples_contract
      harness already exists in drops/) extended to new connectors.

## 11. Plain-English failure UX — MEDIUM (trust)
- [ ] Map raw transport errors (502 / timeout / 429 / auth-expired) to a
      human cause + suggested action in the run viewer, not the cryptic status.
      Run detail/timeline/retry UI already exists to hang this off.
- [ ] Surface "will auto-retry in Ns" vs "needs you" distinctly, tied to the
      retry policy and the #9 idempotency guarantee.
