# Walkthrough TODO — "Form → AI reply → DB → notify → approve → send"

A re-runnable friction audit. **Persona:** a non-technical user building this flow:

> When a **Google Form** gets a new response → **AI drafts a reply** → write the reply
> to a **database** → **ntfy** notification that it's been handled → **approve** sending →
> **email the reply** to the responder.

Re-run the persona walkthrough after each fix pass and tick items off. Done when a
non-techie can build and run this end-to-end without typing a `${…}` token, reading
docs, or getting silently stuck.

## Target graph (the "happy path" we're optimizing for)

```
Google Form (trigger)
  └─ responses ─▶ AI · Draft reply ──▶ DB · Insert row ──▶ Await approval
                     (Message)            (rows)                │
                                                    pending_url ─┴─▶ ntfy (message + click=link)
                                                       approved ─────▶ Email · Send (to responder)
```

---

## P0 — Blockers — PASS 1 FIXED ✅ (verified live 2026-06-15)

- [x] **Triggers don't fire on "Run" — only on "Publish", with no guidance.**
      → Run button tooltip on any trigger flow now reads *"Runs once now, to test. To run
      automatically on new responses, click Publish."* The Google Form trigger's own description
      now says the same. *(Smaller follow-up below: the "Live" status chip can read Live even when
      a configured trigger isn't published — a separate, subtler nudge.)*
- [x] **Responder's email is not in the form response.**
      → `google_form_trigger` now emits a clean **`email`** field (the respondent's address) when
      the form collects emails, surfaced in the field-name hints and documented. The user maps
      `email` directly instead of adding a question + `responses[0].…` index syntax. (Tests:
      `TestRespondentEmail_SurfacedWhenPresent/_OmittedWhenAbsent`.) *Note: requires the Form's
      own "Collect email addresses" setting — outside our control, but now surfaced cleanly.*
- [x] **Picking the database is a techie cliff.**
      → **SQLite** is now the zero-setup default for "save to a database": reframed summary
      ("no setup"), `save`/`store` search tags, and **default `path`/`table`** so it works with no
      config. Postgres's `dsn` now explains itself and points non-techies to SQLite.
      *(Follow-up: give Postgres a real "Connect" affordance like the AI/Gmail apps — P1 below.)*
- [x] **The approval link / notification ordering is counter-intuitive.**
      → `await_approval`'s output is relabeled **"Approval link"**, its description spells out the
      ordering in plain language ("put this BEFORE your notify step; it hands you a link to send;
      they tap it to approve, then the flow continues"), and it ships a worked example for this
      exact ntfy scenario. ntfy's "Link to open" now tips you to wire the Approval link there.
      *(Follow-up: a one-click "request approval via ntfy" auto-wire — P1 below.)*

## P1 — Major friction (doable but painful / error-prone)

- [x] **(from P0-1) "Live" chip + require-published.** — FIXED (the "require published" path was
      chosen). The scheduler now only runs PUBLISHED flows: `rescan` skips enrolling never-published
      flows and `fireGraph` gates on `PublishedCommit` (no more HEAD fallback) — `daemon/scheduler.go`.
      The status chip gained a **`needs_publish`** state (upload-cloud icon): a flow with a configured
      SCHEDULER trigger (cron/poll/google-form) that isn't published reads **"Needs publish"** instead
      of a misleading "Live", in both the editor and the flow list. Webhooks/events are intentionally
      excluded (they're not "the scheduler"). `core/flowstatus.go` `FlowRunStatusPublished`,
      `web/src/flowStatus.ts`, `FlowStatusChip.tsx`, server `ListFlowSummaries`. Verified live (chip in
      list + editor). (Tests: `TestScheduler_UnpublishedFlowIsNotEnrolled`; scheduler firing tests now
      publish first.)
- [x] **(from P0-3) Postgres now has a "Connect" affordance.** — FIXED. The three `postgres_*` drops
      dropped `RequiresConnections(PG_DSN)` + the developer-facing `dsn` param in favour of a
      `ConnectionFields` `dsn` (secret, required), mirroring ntfy/Claude. The unified node chip,
      inspector "Connect Postgres" banner, pre-run gate, and Apps connection card now all light up
      automatically (`injectConnectionDefaults` fills the unset `dsn` from `conn.postgres.dsn` at run
      time). Backward-compatible: old flows with a typed `dsn` still work when no connection is set.
      Verified live (8/8 checks: manifest contract + node chip + inspector + Apps card).
- [x] **(from P0-4) Approval→notify one-click auto-wire.** — FIXED. The await_approval inspector now
      has a **"Notify me on ntfy with the approval link"** button that creates an ntfy step and wires
      it: edge `pending_url → ntfy.message` (orders execution + shows the link) and param
      `ntfy.click = ${upstream.<approval>.pending_url}` (the whole notification is tappable; `click` is
      a param not a port, so it can't be an edge). `web/src/pages/FlowEditor.tsx` `addApprovalNtfy` +
      `web/src/components/Inspector.tsx`. Verified live (button → ntfy node + persisted click ref +
      edge). *The remaining wire — Approved port → send step — stays a manual drag (flow-specific, and
      already explained on await_approval); the button's hint spells it out.*

- [x] **One field out of a response needs developer syntax.** — PASS 2 FIXED.
      The `{}` reference picker already produced field-level tokens (e.g. "New responses → first
      → Email" inserting `${…responses[0].Email}`) — the gap was usability. Added a **search box**
      ("Search fields… e.g. email") with **Enter-to-insert-first-match**, so a non-techie types
      "email" + Enter instead of scrolling a long flat list or hand-typing `[0].field`.
      *(Follow-up: render field entries as a labelled sub-group per node, not just searchable.)*
- [x] **Batch vs single mismatch.** — PASS 3 FIXED (cardinality-metadata route).
      Added **`Port.List`** cardinality and a central `MarkListPorts` pass (applied next to
      WithPassthrough) that tags conventionally-named list ports (`rows`/`responses`/`messages`/
      `issues`/`events`/`items`/`results`/…) on every drop — no per-port edits. The editor now
      shows an amber **"wrap in For each" badge** on any node where a *list* output is wired into a
      *non-list* (one-at-a-time) input, ignoring the pass pin and loop-body nodes. `for_each` also
      ships a worked "once per response" example. Verified live: Form `responses` → Claude `Text`
      shows the badge; `responses` → `for_each.items` does not. (Tests: `MarkListPorts`.)
- [x] **ntfy receiving is unexplained pub/sub.** — FIXED (discovery). The ntfy node inspector now
      has a **"Receive these on your phone"** section with a **subscribe link** (`https://ntfy.sh/<topic>`,
      reusing the Triggers `CodeField` copy/Open row) and a how-to hint ("subscribe to this exact
      topic in the free ntfy app; use Run this step to send a test; custom-server note"). Blank topic
      prompts you to set one. Verified live (5/5). *(Follow-up: a scannable QR needs a frontend dep —
      `qrcode.react` — held pending a dep-add decision; the link covers desktop + mobile-browser.)*
- [x] **DB row shape — column mapper.** — FIXED. Added an optional **`field_mapping`** param
      ({incoming field: column name}) to all six row-writers (sqlite/postgres/mysql × insert/upsert),
      applied centrally in `parseRowsInput` (drops/db/rowwrite.go) so it's whitelist+rename in one:
      only listed fields are written, under the given column names (blank = drop). The table is shaped
      from the OUTPUT columns and upsert `conflict_columns` refer to the mapped names. Renders as a
      free key/value editor (SchemaForm DictField). For heavier reshaping the description points at
      `map_rows`. (Tests: `TestApplyFieldMapping`, `TestParseRowsInput_FieldMapping`.)
- [~] **Renaming a form question silently breaks references** (responses keyed by title, not stable
      ID). — PARTIALLY FIXED. New `dangling_reference` lint (core/lint.go) warns at save time when a
      node references `${upstream.<id>.…}` for an `<id>` that's no longer a step (deleted/renamed
      NODE) — pure-graph, conservative (never flags a valid ref). (Tests:
      `TestLintGraph_DanglingReferenceFlagged`, `…_ValidReferenceNotFlagged`.) *Field-level renames
      (form question "Email" → "Contact email", node still exists) need the trigger's LIVE field list
      at lint time (Forms API), which the pure pass can't reach — needs a decision on threading
      live-field fetches into lint vs. surfacing in the reference picker.*

## NEW — found re-running the walkthrough (PASS 4, 2026-06-15)

- [x] **P1 — Per-field `description` copy never reaches the UI (cross-cutting).** — FIXED. `SchemaForm`'s
      `FieldLabel` (`web/src/components/SchemaForm.tsx`) rendered only `schema.title` + the first
      `schema.examples` entry — it referenced `schema.description` nowhere, so **every
      "fixed by field-description copy" item was invisible** (P2 email `subject`; P0-3 Postgres `dsn`
      self-explanation; P1 DB `field_mapping`→`map_rows`). Fix: each field whose schema has a
      `description` now gets a small hover/focus **(i) tooltip next to its label** — reusing the
      `inspector-info` affordance already on the drop header (`.sf-label-group` keeps it tight to the
      label; the `{ }` ref button stays pinned right). Chosen over an inline `.desc` line specifically
      to keep the panel low-clutter — no permanent text under any input. Verified live: the Email
      Subject (i) tooltip now carries the full *"…Leave blank and it sends as '(no subject)'…"* copy
      (3 field icons render in the Email inspector). This lights up P0-3 / P1-DB / P2-subject together.

- [x] **P2 — "save" ranks the KV store above the database.** — FIXED. Added a general
      `Manifest.SearchBoost` ranking nudge (applied to fuzzy/tag matches only, never overtaking an
      exact id/label hit; floored at 1 so a negative value down-ranks without dropping the match) in
      `daemon/search.go`. SQLite **Insert rows** gets `SearchBoost: 25` and **Upsert rows** `10`, so the
      canonical save-to-a-database default now leads "save"/"database" over the no-setup KV store (which
      tied at score 70 and won only on the alphabetical tiebreak). Chose boosting SQLite over down-ranking
      Built-in store so the KV store still ranks well for "store"/"kv"/"no-setup". (Tests:
      `TestSearch_SearchBoostBreaksTie` — tie-break, negative floor, and exact-beats-boosted-fuzzy.)

## P2 — Polish (confusing but not blocking)

- [x] **"Leave interval blank to check only when you press Run"** phrasing is confusing. — FIXED.
      Both the poll (`Interval`) and cron trigger param descriptions now lead with the automatic case and
      contrast it with manual: *"Set this to run the flow automatically every N minutes/hours/days … Leave
      it empty and the flow never runs on its own; it runs only when you press Run."*
      (`drops/trigger/poll_trigger.go`, `drops/trigger/cron_trigger.go`).
- [x] **Email `subject` silently defaults to "(no subject)"** — FIXED (copy + now surfaced). The
      `subject` param on both `email_send` (SMTP) and `gmail_send_email` carries the text *"The email's
      subject line — e.g. 'Re: your submission'. Leave blank and it sends as '(no subject)'."* PASS 4
      found the copy was written but **never rendered** (re-opened, then fixed by the cross-cutting
      `FieldLabel` (i)-tooltip change in the NEW section above). Verified live: the Subject field's (i)
      tooltip now shows the full text. *(Follow-up: a true required-subject prompt/validation if
      blank-sends are undesirable.)*
- [x] **ntfy truncates >4 KiB silently** — FIXED. The body was already truncated at the ~4 KiB ntfy
      cap, but silently. Now it warns loudly: a progress message ("message was N bytes; … shortened —
      wire a summary instead of a full document") and `truncated` / `original_bytes` flags in the result
      `meta` (`drops/notify/ntfy.go`). (Test: `TestNtfy_TruncatesLongMessage` extended to assert both.)
- [x] **Reference picker** lazy-loads with a blank list, has no search, labels form fields under
      `trigger.body.*`. — FIXED. The picker now shows an explicit **Loading…** state while fetching (no
      more blank-reads-as-broken) and an **empty** message when there's genuinely nothing; it has a
      **search box** with autofocus + Enter-to-insert-first-match; and trigger/form fields are
      **humanized** (`email` → "Email", `first_name` → "First Name") instead of raw dotted keys
      (`SchemaForm.tsx` `ReferenceMenu` / `describe`).
- [x] **`await_approval` has no visible "awaiting" state** in run history, approver expectations
      unset. — FIXED. Two parts: (1) the `.status-dot` spans in the run-history list + run header were
      rendered with NO css at all (invisible), so a parked run looked finished — added per-status colours
      with the awaiting state getting a pulsing amber ring, plus an explicit **"Awaiting approval"** text
      tag on awaiting rows (`app.css`, `RunHistory.tsx`, i18n). (2) Approver expectations are now stated
      in-UI: the `await_approval` description and the `prompt` field's (i)-tooltip both say *"anyone who
      has the link can approve or reject — no per-person targeting … the flow records who clicked on the
      Approver output"* (`drops/flow/await_approval.go`).
- [x] **AI errors are opaque** ("llm_http_error: rate limit") — FIXED. Added a shared
      `llmtask.HTTPError(codePrefix, label, status, detail)` that maps non-2xx LLM statuses to actionable,
      named text — 401/403 "check the API key…", 429 "ChatGPT is rate-limited; wait a moment…", 400/422
      "rejected the request — usually the model name or an over-long prompt", 404 "couldn't find that
      model", 5xx "temporary problem — try again". Both Claude and ChatGPT route through it (stable codes
      preserved: `*_rate_limited`, `*_api`). Also: a request timeout now reads "the AI request timed out
      after Ns — raise timeout_ms or shorten the input" instead of a raw context error
      (`drops/internal/llmtask/llmtask.go`, `drops/claude`, `drops/openai`). (Test:
      `TestHTTPError_ActionableByStatus`.)
- [x] **A wired input greys out the field** with no visible current value — FIXED. A wired non-picker
      param now renders a read-only `WiredField` note naming the source ("Comes from New responses ·
      Email") instead of an editable-looking box whose value is silently ignored. The source label is
      traced from the incoming edge via `wiredSourcesByNode` (reusing `tokenLabels`) and threaded
      FlowEditor → Inspector → SchemaForm → SchemaField. Resource pickers keep their existing richer
      disabled note (`SchemaForm.tsx`, `Inspector.tsx`, `FlowEditor.tsx`, i18n `wiredInput`/`wiredInputNamed`).

## Cross-cutting / wins to keep

- [x] AI steps (Claude/ChatGPT) have a one-click **Connect** affordance (node chip, inspector,
      banner, gate). **Postgres now replicates it** (see P1 above); ntfy still to unify.
- [~] After all P0/P1: re-run the persona walkthrough end-to-end and confirm **zero** `${…}`
      typing and **zero** dead-ends. — **RE-RUN PASS 4 (2026-06-15, live, headless):** built the
      full graph (Form → Draft reply → SQLite → Await approval → ntfy → Email) entirely through UI
      affordances, **typing zero `${…}` tokens and zero DSNs**, 6 nodes on canvas. All P0/P1
      affordances confirmed present and working live: palette add-by-search, AI Connect chip,
      `await_approval` "Approval link" label, the one-click **"Notify me on ntfy"** auto-wire
      (4→5 nodes), ntfy "Receive these on your phone" subscribe section, SQLite no-config defaults +
      Column mapping `+Add`. **Not yet zero-friction** — two findings folded in above (NEW section):
      field-`description` copy never renders (P1), and "save" ranks the KV store above SQLite (P2).
      Re-run once the `FieldLabel` renderer fix lands. *Note: the Google-Form respondent `email`
      field (P0-2) can only be confirmed against a live OAuth'd form — out of reach in the headless
      harness; verify manually.*
      **PASS 5 (2026-06-15): the PASS-4 blockers are now resolved** — the `FieldLabel` (i)-tooltip
      renders field descriptions (NEW section), and the whole P2 batch landed (AI-error copy, ntfy
      truncation warning, interval phrasing, "save"→SQLite ranking via `Manifest.SearchBoost`, wired-input
      "comes from" note, run-history status dots + "Awaiting approval" tag, humanized reference-picker
      fields). Stays `[~]` pending a fresh end-to-end persona re-run; full **zero-dead-ends** still can't
      be *certified* while the Google-Form field-rename gap (item above) needs a live Forms-API decision.

---

### How to re-run this audit
Play the persona: build the target graph from an empty flow, connecting apps only via UI
affordances, never typing tokens or DSNs. Note every spot you'd be stuck or guess wrong;
fold new findings in here by severity. Ship when the final checkbox passes.
