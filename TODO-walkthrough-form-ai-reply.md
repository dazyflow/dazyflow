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

- [ ] **(from P0-1) "Live" chip can mislead when unpublished.** A trigger flow with a configured
      interval shows "Live" before it's published; the scheduler only runs *published* flows.
      → Have the status reflect published state, or add a "needs publish" sub-state.
- [ ] **(from P0-3) Postgres still has no "Connect" affordance.** SQLite is now the easy default,
      but for users who *do* have Postgres, the raw `dsn` field is still developer-facing.
      → Give Postgres a connection (conn.postgres.dsn via ConnectionFields) so it joins the
      Apps "Connect" flow + needs-setup chip we built.
- [ ] **(from P0-4) Approval→notify is guided but not automated.** The wiring is now well
      explained, but still manual.
      → A one-click "request approval, notify me on ntfy" that auto-wires
      `pending_url` → ntfy `click` and the Approved port → the send step.

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
- [ ] **ntfy receiving is unexplained pub/sub.** The user invents a topic in the flow, but must
      separately subscribe to that exact topic in the ntfy app to ever receive anything. No
      discovery, no "subscribe" link/QR.
      → Surface the topic + a one-tap subscribe (deep link / QR) and a "send test notification".
- [ ] **DB row shape is implicit.** `rows` wants `[{col: val}]`. Wiring `responses` dumps *all*
      questions as columns; there's no "choose which fields → which columns" step, and
      `create_table` makes everything TEXT.
      → A simple column mapper for the insert step.
- [ ] **Renaming a form question silently breaks downstream references** (responses keyed by
      title, not stable ID).
      → Warn on unknown/renamed field references; prefer stable keys.

## P2 — Polish (confusing but not blocking)

- [ ] **"Leave interval blank to check only when you press Run"** phrasing is confusing — say
      "Run automatically every N minutes" vs "only when I press Run".
- [ ] **Email `subject` silently defaults to "(no subject)"** — prompt for it (it's the reply's
      subject, e.g., "Re: your submission").
- [ ] **ntfy truncates >4 KiB silently** — warn when wiring a long body.
- [ ] **Reference picker** lazy-loads with a blank list (reads as broken), has no search, and
      labels form fields under `trigger.body.*`.
- [ ] **`await_approval` has no visible "awaiting" state** in run history, and the approver is
      "whoever clicks the link" (no role/email targeting) — set expectations in-UI.
- [ ] **AI errors are opaque** ("llm_http_error: rate limit") — give actionable text
      ("ChatGPT is rate-limited; try again shortly").
- [ ] **A wired input greys out the field** with no visible current value — show what's flowing in.

## Cross-cutting / wins to keep

- [x] AI steps (Claude/ChatGPT) now have a one-click **Connect** affordance (node chip,
      inspector, banner, gate) — replicate that connection pattern for **Postgres** and unify ntfy.
- [ ] After all P0/P1: re-run the persona walkthrough end-to-end and confirm **zero** `${…}`
      typing and **zero** dead-ends.

---

### How to re-run this audit
Play the persona: build the target graph from an empty flow, connecting apps only via UI
affordances, never typing tokens or DSNs. Note every spot you'd be stuck or guess wrong;
fold new findings in here by severity. Ship when the final checkbox passes.
