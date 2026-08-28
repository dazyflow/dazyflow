<!--
SPDX-FileCopyrightText: 2026 Angels' Ware
SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Scenarios — thirty-five things people ask an automation tool for

Jobs a non-technical buyer turns up with, written the way they'd say them out
loud rather than in Dazyflow vocabulary. Each one has an acceptance test ("it
works when…") and a verdict backed by a real graph. Ten openers first, then the
twenty-five they ask for next.

This is the *inbound ask* list. Its sibling, [tests/scenarios.md](tests/scenarios.md),
covers recurring internal jobs a small company already does by hand. The two
overlap by design — where they do, this file links across instead of repeating.

## How the verdicts were reached

Every scenario was built as an actual graph under `tests/usecases/` and put
through the same authoring gate the product applies when a flow is saved
(`core.ValidateGraphFull`: unknown steps, missing ports, unsatisfied required
inputs, MIME and cardinality mismatches, placeholder and security lint), plus a
param-level check the gate doesn't do — unknown settings, missing required
settings, and each setting's declared type and enum.

Composing isn't running, so the formulas inside the graphs — the CEL filters,
the grouping, the column shaping, the parts a non-technical user cannot debug —
were additionally *executed* against sample data. That layer is
`tests/usecases/formulas_test.go`, and it is where the two silent-wrong-answer
bugs below were caught. Both suites run with `go test ./tests/usecases/`.

Each new or changed step also has its own tests next to it in `drops/`.
Not covered: live Gmail/Sheets/Slack/Stripe calls against real accounts. Where a
verdict depends on the runtime behaviour of a connector, it says so.

## The asks are also an eval for the flow generator

Everything above answers "can this be built?" — by someone who knows the
catalog and writes graph JSON. The product's actual promise is narrower and
harder: describe the flow in a sentence and the AI drafts it. This corpus tests
that too, for free, because each ask is already paired with a known-good graph:

    FLOWGEN_EVAL_KEY=<provider key> make flowgen-eval

That feeds every scenario's own words — the quoted heading plus its "It works
when" sentence, nothing else — to the same generator the editor calls, and
scores each draft three ways:

- **Valid** — does it pass the gate the save path applies? A draft that doesn't
  is one the user cannot run.
- **Trigger** — did it choose the same *kind* of start (a schedule, an inbound
  call, an app event)? Not the same cron expression to the minute.
- **Apps reached** — of the outside services the reference answer needs, how
  many does the draft touch? Judged by service, not by step, because picking a
  different Gmail step is fine and not realising Gmail is involved is not.

Read all three together: the three are deliberately separate because **a flow
that does nothing passes the gate**. The scripted-model test in
`daemon/flowgen_eval_test.go` pins exactly that case — an unrelated draft scores
Valid but 0% apps — so a rising "valid" number can never be mistaken for the
generator getting better at the job.

Each run writes `flowgen-eval.md` (the table), `flowgen-eval.json` (the scores)
and every generated draft, so a bad answer can be read rather than guessed at.
Live runs cost money and aren't deterministic, so the suite reports rather than
fails; set `FLOWGEN_EVAL_MIN_VALID=80` to enforce a floor once there's a
baseline worth defending. `FLOWGEN_EVAL_ONLY=12,29,33` runs a subset.

### The scenarios are also run, not just validated

Composition checking proves a flow could be built. It cannot prove the engine
runs it. `tests/journey/usecases_run_test.go` takes graphs from
`tests/usecases/` as they are, saves them through the real API, publishes,
fires, and waits — with every outside service mocked by `fakesaas_test.go` —
then asserts on **what the world received**: the emails sent, the rows written,
the invoices raised, the notifications and their tap targets.

Eleven shapes run, chosen for their mechanics rather than their subject:

| Shape | Scenario | What only a run can prove |
| --- | --- | --- |
| Structured loop handover | 29 | each customer's own lines reach their own template |
| Read → act → write back | 12 | the stamp lands on the row it came from |
| Collecting loop results | 30 | only the unanswered threads reach the digest |
| Transition-only firing | 33 | one page on the break, one on recovery, nothing between |
| Tolerating a dead channel | 34 | the other three still went out |
| Pausing for a person | 22 | the run waits, and the link reaches the approver |
| Rejecting | 22 | nothing is booked, and the requester is told |
| AI judgement routing | 17 | the enquiry reaches the sheet, not the verdict |
| Dedupe across runs | 2 | the same mailbox posts once, not every poll |
| Remind once, never twice | 15 | only the contract inside the window, and only once |
| Three services in one loop body | 20 | book → tracking → cleaned number → text → mark, per order |
| Surviving an outage | 12, 20 | nothing is marked done that never happened |

The mocked services are **stateful** — the spreadsheet remembers what was
written to it — because that is the only way to test the promise eleven
scenarios make: that nothing happens twice. Every applicable test runs its flow
a second time and asserts the second run does nothing.

Three of them inject faults — a dead notification channel, an invoicing API
that is down, a carrier that is down — which is where findings **U** and **V**
came from. The outage tests carry through to recovery: the same work must
succeed on the next run once the service is back, which is the half that
proves nothing was silently written off.

### Driving it without a key

`FLOWGEN_MANUAL_DIR=… go test ./daemon -run TestFlowGenManual` runs the same
loop with a person — or another agent — playing the model. Each turn writes the
exact prompt the generator would have sent and waits for a reply file, so the
conversation can be walked one turn at a time and resumed. It needs no vendor
key, and it is the way to *read* what the model is working from: the system
prompt, the catalog rows, and what `describe_drop` actually returns.

That is what turned up finding **T** below. Note the limit of it as a *score*:
whoever plays the model here knows the corpus, so validity and app coverage
measured this way flatter the result. What it is good for is the other
direction — reading the instructions the generator gives and finding the ones
that are wrong.

Two parts of it run in ordinary CI with no model and no key:
`TestFlowGenScenariosHarness` checks the corpus still parses, every ask still
has a reference graph, and the scorer agrees a reference answers its own ask —
so this document and `tests/usecases/` can't drift apart unnoticed.
`TestFlowGenScenariosScripted` drives the whole eval against a scripted model,
so the live path keeps working between runs.

All thirty-five are supported. Nine of them weren't when first written — filing
emailed attachments, marking a spreadsheet row done, emailing whoever just
cancelled, asking whether anyone had replied, watching whether a site is up, and
several that needed a workaround nobody would find. "What had to change" below
records every gap and defect behind them, since the fixes are the interesting
part of the answer.

---

## 1. "Put my contact form on the web and drop every reply in a spreadsheet"

Lead capture. They want a link to paste in an email signature, and every
submission landing as a row — plus a heads-up so nobody has to watch the sheet.

**It works when:** a public form link exists without touching HTML, each
submission appends a row to their Google Sheet, and a ping lands in Slack.

**Verdict: Supported.** `tests/usecases/01-web-form-to-sheet.json`. The
`webhook_input` step hosts the form itself (`public_form` + `form_fields`), and
its body wires straight into `sheets_append_row` — a single JSON object is
accepted as one row. This one also ships as the `form-to-sheet` template, so a
newcomer forks it rather than building it.

## 2. "Tell me in Slack when an important email arrives"

Inbox triage. Not every email — the ones from a customer domain, or with
"invoice" in the subject.

**It works when:** the mailbox is polled, only matching mail is picked up, each
new one posts a readable one-liner, and nothing is posted twice.

**Verdict: Supported.** `tests/usecases/02-important-email-to-slack.json`.
Gmail's own search syntax is the filter, and `only_new` is what keeps a
published polling flow from re-posting the same mail every five minutes — the
single most important setting in this flow, and easy to miss. Ships as the
`email-to-slack` template.

## 3. "Email me a summary of my spreadsheet every Monday morning"

The recurring report. Numbers live in a sheet; the ask is a digest, not a link.

**It works when:** it runs on a schedule in their timezone, reads the sheet,
totals the rows, and sends a formatted table by email.

**Verdict: Supported.** `tests/usecases/03-weekly-sheet-digest-email.json`.
Cron with an IANA timezone, `compute_rows` for the "last 7 days" window (CEL has
`now` in scope), `group_aggregate` for the totals, `render_table` for an HTML
table into the mail body. The totals step used to reject the short
`{"revenue": "sum"}` form a person would guess — finding **F**, now accepted.

## 4. "When someone pays, thank them and log the sale"

The order pipeline. A payment arrives; nobody should have to retype it.

**It works when:** a Stripe payment starts the flow, the customer gets a
thank-you, the team channel gets a notification, and a row lands in a table —
without duplicates when Stripe retries the webhook.

**Verdict: Supported.** `tests/usecases/04-payment-thanks-and-log.json`. The
trigger gives typed pins (`customer_email`, `amount_display`) plus the raw
payment object. Duplicate protection is `builtin_store_append`'s `unique_by`.
One rough edge remains by design: composing one sentence out of two pins needs a
formatting step (`render_template`), because every message input takes a single
wire. The log row is shaped with `map_rows`, which also accepts a single object
as one row; a formula would work too now that finding **A** is fixed. Ships as
the `payment-to-thanks-and-log` template.

## 5. "Read my incoming email and sort it out for me"

AI triage. Sort support mail into buckets, draft replies for the easy ones,
escalate anything urgent to a phone.

**It works when:** each new email is classified into their own categories, the
route differs per category, a draft reply is produced for routine ones, and
urgent ones fire an SMS.

**Verdict: Supported.** `tests/usecases/05-ai-email-triage.json`. `for_each`
over the search results, `claude_classify` with the user's own categories,
`switch` on the category, and `${item.…}` to reach the email's fields inside
the loop. Categories are `{name, description}` objects and switch cases are
`{slot, equals}` objects — both shapes are easy to get wrong and both are now
pinned by the test. Ships as the `ai-email-triage` template.

## 6. "File the invoices people email me into Drive"

Document filing. Attachments arrive by mail and someone drags them into a
folder by hand every week.

**It works when:** matching emails are found, the attached PDF is saved to a
named Drive folder, and a row with sender/date/link is appended to a sheet.

**Verdict: Supported** (was a gap; closed by the new
`gmail_get_attachments` step). `tests/usecases/06-file-invoice-attachments.json`
finds the invoice mail, saves the attached PDF, uploads it to a named Drive
folder, and appends a row with the vendor, amount and due date that AI read off
the email. The step's `first` output is a file ref that wires straight into
Upload to Drive; `files` carries them all. Ships as the `invoices-to-drive`
template. Previously impossible: Dazyflow could send attachments but not read
them — see finding **B**.

## 7. "Text my customers the day before their appointment"

Reminders off a calendar. Clinics, salons, tradespeople.

**It works when:** it runs daily, reads tomorrow's bookings, and sends one
message per booking with the time filled in.

**Verdict: Supported** (was partial; closed by relative calendar windows and a
typeable Regex text). `tests/usecases/07-appointment-reminders.json` runs at
17:00, asks the calendar for `tomorrow` → `tomorrow+1d` in the salon's own
timezone, keeps the bookings whose notes carry a phone number, pulls that number
out, normalises it to E.164 with the Phone step (which rejects a number that
isn't dialable rather than letting Twilio fail later), and texts each customer —
then logs the event ids so a re-run can't double-remind.

The phone number comes from the booking's own notes, because that is where a
calendar keeps it; if the bookings live in a sheet with a phone column instead,
[tests/scenarios.md §6](tests/scenarios.md) is the same flow off a sheet. What
had to change to make this buildable is findings **C** and **H**.

## 8. "Watch this page/feed and tell me when something new shows up"

Monitoring. A competitor's blog, a tender site, a news feed, a price.

**It works when:** the source is polled, only genuinely new items are reported,
and the alert lands in Slack or on a phone with a link.

**Verdict: Supported** (pages were partial; closed by the new `web_watch`
step). `tests/usecases/08-watch-feed-and-alert.json` polls an RSS/Atom feed
whose `dedupe` setting does the "only new" work for free.
`tests/usecases/08b-watch-page-and-alert.json` watches an arbitrary page: one
step, which fires its `on_change` pin only when the page's visible words
changed, leaving the alert dormant on a quiet check. Before, that meant four
steps and a hand-rolled collection to diff against, as
[tests/scenarios.md §9](tests/scenarios.md) still does for price watching.
Ships as the `watch-a-page` template — the only one in the gallery besides the
demo that needs no connected account at all.

## 9. "Nothing goes out until I approve it"

Human in the loop. Refunds, discounts, invoices — anything that costs money.

**It works when:** the flow pauses, an approver gets a link, and the action runs
on approve or is skipped with a note on reject.

**Verdict: Supported.** `tests/usecases/09-approve-before-refund.json`. A public
form takes the request, `await_approval` parks the run and emits the link, the
notification carries it, and `approved`/`rejected` fan out to the refund and to
a "declined" note. `stripe_create_refund` reuses one idempotency key across
retries, so a flaky run can't refund twice. The approval link now wires directly
into ntfy's *Link to open*, so the notification is tappable — finding **E**.
Ships as the `approve-before-refund` template.

## 10. "Keep these two systems in step"

Sync. Rows from a sheet into a database or Notion, run repeatedly, without
creating the same record twice.

**It works when:** it runs on a schedule, only new or changed rows are written,
re-running doesn't duplicate, and column names can be mapped.

**Verdict: Supported.** `tests/usecases/10-keep-two-systems-in-step.json`. For a
database this is one step: `postgres_upsert_rows` with `conflict_columns` is
idempotent by construction. For a target without upsert (Notion), the flow uses
the new `kind: "anti"` join against a `notion_synced` collection — "the rows
with no match on the right" — writes only those, and records what it wrote. The
left-join-plus-null-test that this replaced is finding **D**: it validated clean
and silently wrote nothing.

---

# Round two: twenty-five more

The ten above are the classic openers. These twenty-five are what the same buyer
asks for next — once the first flow works, the questions get specific to how
their business actually runs. Same rules: plain-language ask, an acceptance
test, and a verdict backed by a graph in `tests/usecases/`. Where an ask is best
served by more than one flow, it says so and why.

## Money

### 11. "Chase people whose payment bounced"

**It works when:** a failed Stripe payment emails the customer a fix-it link,
tells finance, and records the attempt so nobody chases twice.

**Verdict: Supported.** `11-failed-payment-dunning.json`. The trigger's payment
object drives two templates — the customer's letter, quoting the bank's own
reason, and the finance channel's one-liner — and the attempt is recorded in a
Collection keyed on the payment id, so Stripe retrying its webhook updates the
same row instead of chasing twice.

### 12. "When a job is marked done, send the invoice"

**It works when:** flipping a row to Done in the jobs sheet raises a real
invoice for that customer with the right amount, and marks the row invoiced.

**Verdict: Supported** — needed the new write-back step. `12-job-done-invoice.json`
reads the Jobs tab with row numbers on, keeps the rows that are Done and not yet
invoiced, raises a Fortnox invoice per row, and writes `invoiced_on` back **into
those rows**, so tomorrow's run skips them. Marking a row was previously
impossible — Sheets could be read and appended to, never changed; see finding
**K**.

### 13. "Tell me when someone cancels, and why"

**It works when:** a cancelled subscription posts to the team, logs the churn
with whatever reason came with it, and sends the customer a courteous goodbye.

**Verdict: Supported** — needed a customer lookup. `13-subscription-cancelled.json`.
Stripe events name the customer by `cus_…` id, never by email, and Stripe's
search cannot look up by id — so there was no way to write to the person who
just left. The new Get customer step closes that; see finding **L**.

### 14. "Check that what we were paid matches what we invoiced"

**It works when:** a weekly run lines up Stripe's payments against the invoice
sheet and reports the mismatches — paid but not invoiced, invoiced but not
paid, and wrong amounts.

**Verdict: Supported.** `14-reconcile-payments.json` full-outer-joins the two
sides on the invoice number and labels each row with what's wrong, so all three
kinds of mismatch come out of one pass and a clean week sends a table that says
so. The classifier is one formula, and it is pinned by a test — the null-vs-
missing trap from finding **D** is exactly what it walks through.

### 15. "Warn me before a contract renews"

**It works when:** every contract whose end date is 30 days out produces one
reminder to the account owner, and only one.

**Verdict: Supported.** `15-renewal-reminders.json`. The window is a formula
against `now`, and "only one" is the write-back again: `reminded_on` goes into
the row, so the next morning's run passes over it.

## Customers and sales

### 16. "Look up a new lead's company for me"

**It works when:** a lead arriving with a company registration number comes back
enriched — legal name, status, address — into the CRM row, with the team
notified.

**Verdict: Supported.** `16-enrich-new-lead.json`. Worth knowing for any
"combine two things" flow: a formula step reads one wired value, so the form and
the company lookup are brought together with a Merge step first, and the formula
then shapes one record out of `input[0]` and `input[1]`.

### 17. "Stop the junk from my contact form reaching us"

**It works when:** AI judges each submission, obvious spam is dropped silently,
and real enquiries reach the inbox and the sheet as before.

**Verdict: Supported.** `17-contact-form-spam-filter.json`. The shape to copy:
Classify decides, Compare turns the answer into yes/no, and Branch routes the
**original submission** — so what reaches the sheet is the person's actual
message, not the classifier's verdict.

### 18. "Ask for a review a few days after they buy"

**It works when:** a purchase schedules a follow-up, the ask goes out days
later rather than immediately, and nobody is asked twice.

**Verdict: Supported, as two flows.** `18a-purchase-log.json` records each
purchase; `18b-ask-for-a-review.json` runs every morning and asks whoever bought
three days ago, then stamps them asked. Deliberately not a Delay step: a step
has a wall-clock cap (30 minutes by default), so Delay is for minutes — see
finding **M**.

### 19. "Read our feedback and tell me who's unhappy"

**It works when:** each response is scored for sentiment, the unhappy ones are
escalated to a person with the text attached, and the rest are just filed.

**Verdict: Supported.** `19-feedback-sentiment.json` — same Classify → Compare →
Branch shape as 17, with everything filed either way so the good news is
counted too.

### 20. "Text the customer their tracking link when it ships"

**It works when:** marking an order shipped books the shipment, and the customer
gets the tracking link by SMS without anyone copying it.

**Verdict: Supported.** `20-ship-and-text-tracking.json`. The shipment booking
needs a nested address object built per order — which is a loop-body step
receiving structured data, the thing finding **I** had to fix. The number goes
through the Phone step first, so a mistyped one fails with "not a dialable
number" instead of a cryptic carrier rejection.

## People and internal operations

### 21. "Set a new starter up on their first day"

**It works when:** one form submission sends the welcome email, books the intro
meeting in the calendar, opens their onboarding checklist, and tells the team.

**Verdict: Supported** — needed wireable calendar fields. `21-new-starter-onboarding.json`
computes the meeting's title, start, end and attendee from the form and wires
them into Create event, which until now took only typed values (finding **J**).
The checklist is one formula returning four rows.

### 22. "Handle time-off requests"

**It works when:** a request form pauses for the manager, an approved request
lands in the shared calendar and the team channel, and a rejected one tells the
person why.

**Verdict: Supported.** `22-time-off-request.json`. The approval link goes
straight into the notification's tap target, and the approved payload — the
original request — feeds the calendar entry, so the dates come from what the
person actually asked for. Plain dates make it an all-day event, which is what
time off is.

### 23. "Run our standup for us"

**It works when:** the prompt goes out every weekday morning, answers land in
one place, and a digest of who said what is posted at a set time.

**Verdict: Supported, as three flows** — `23a` asks at 09:00, `23b` is the form
that collects the answers, `23c` posts the digest at 16:00. Three triggers means
three flows: a schedule fires the whole flow it belongs to, so two schedules in
one flow would run both halves twice a day. Worth knowing when planning, not a
limitation to work around.

### 24. "Turn '@bot make a ticket' in Slack into an actual ticket"

**It works when:** mentioning the bot in a channel opens a GitHub issue (or a
Notion page) with the message as the body, and replies in the thread with the
link.

**Verdict: Supported** — the thread reply needed a new wire. `24-slack-mention-to-ticket.json`
strips the @-mention, trims a title out of what's left, opens the issue, and
answers **under the original message** by wiring the trigger's timestamp into
Slack's reply-in-thread, which was previously a typed setting only (finding
**J**). Trimming the title uses the string helpers from finding **N**.

### 25. "If nobody picks up an alert, escalate it"

**It works when:** an alert notifies the first person, waits, and — if nobody
has acknowledged it — notifies the next one up.

**Verdict: Supported, as two flows.** `25a-alert-intake.json` takes the alert on
a token-protected webhook, records it, and pages the primary with an
acknowledge link — the run then simply waits at the approval step, which is
what "unacknowledged" means. `25b-escalate-unacked.json` sweeps every five
minutes for anything unacknowledged for ten, pages the backup, and marks it
escalated so the backup isn't paged twice.

## Documents and data hygiene

### 26. "Back up my spreadsheet every week"

**It works when:** a dated copy lands in a Drive folder every Sunday night, and
the run says which file it wrote.

**Verdict: Supported** — the dated name needed a wire. `26-weekly-sheet-backup.json`
exports the sheet, builds `Bokforing-YYYY-MM-DD.pdf` from the Date step, and
uploads under that name; Drive's file name was a typed setting only, so every
backup would have overwritten the last (finding **J**).

### 27. "Clean up my mailing list"

**It works when:** addresses are normalised, phone numbers checked, duplicates
removed, and the tidy list written back — with a count of what was dropped.

**Verdict: Supported.** `27-clean-the-mailing-list.json` lower-cases and trims
addresses, drops anything that isn't one, removes duplicates keeping the newest,
and reports the count from Remove duplicates' own "dropped" output. The
normalising is one formula, which needed the string helpers (finding **N**).

### 28. "Send the bookkeeper last month's numbers"

**It works when:** a monthly run queries the database, writes a spreadsheet the
accountant can open, and emails it as an attachment.

**Verdict: Supported.** `28-monthly-accounts-export.json` — three steps: query,
write the workbook into the run's scratch space, attach it. Attachments are a
variadic input, so the file wires straight in.

### 29. "Send each customer their own personalised statement"

**It works when:** one run turns a table into one message per customer, each
containing only their own rows, laid out properly rather than as a data dump.

**Verdict: Supported** — this is the one finding **I** was really about.
`29-personal-statements.json` groups the charges by customer, collecting each
one's lines into a list, then loops: every customer's own group goes into their
own template as `${item.}`, which now arrives as a real object with a real list
inside it, so the template can walk the lines. Before, that setting received the
JSON as text and the loop could only ever send scalars.

### 30. "Remind me about emails nobody answered"

**It works when:** anything I sent that hasn't had a reply in three days comes
back to me as a list, once.

**Verdict: Supported** — needed a new step. `30-chase-unanswered-email.json`
searches sent mail from three to fourteen days ago and asks the new Read
conversation step, per thread, whether anyone has answered. Gmail has no
"unanswered" search operator and the mail steps only ever saw single messages,
so this could not be asked before; see finding **O**.

## Alerts and the physical world

### 31. "Warn the crew if tomorrow's weather is bad"

**It works when:** an evening check of tomorrow's forecast for the job site
sends a warning only when it's below freezing or the rain is heavy.

**Verdict: Supported.** `31-weather-warning.json`. Tomorrow's row is picked by
comparing the forecast's own date against `now` plus a day, and a quiet evening
sends nothing because the formatting step's "when there's nothing" text is
empty — an empty message is how a flow says "no news".

### 32. "Have the heating on before the first booking"

**It works when:** the flow reads the day's first appointment and turns the
heating on an hour before it, and does nothing on a day with no bookings.

**Verdict: Supported.** `32-heating-before-first-booking.json` checks every
quarter of an hour whether anything starts within the hour — the calendar window
is `now` → `now+1h`, which only became sayable with relative windows (finding
**C**, round one). Two conditions are combined with an AND step so it acts only
when there's a booking *and* the heating is currently off; a day with no
bookings does nothing at all.

### 33. "Tell me the moment our website goes down — and when it's back"

**It works when:** a check every few minutes alerts on the first failure, stays
quiet while it's still down, and says so when it recovers.

**Verdict: Supported** — needed a new step. `33-site-up-or-down.json` uses the
new Is it up? step, which fires "Went down" on the check where it breaks and
"Came back" when it recovers, and nothing in between, so an outage pages you
once rather than twelve times an hour. It also takes a phrase the page must
contain, which catches the server answering 200 with an error page. See finding
**P**.

### 34. "Announce it once, post it everywhere"

**It works when:** one submission goes out to Slack, Discord, the mailing list
and the phone push in a single run, and a failure on one doesn't block the rest.

**Verdict: Supported** — the second half needed a new setting.
`34-announce-everywhere.json` fans one wording out to four channels, each marked
non-critical, so Discord being down still leaves the Slack post, the email and
the push counted as done. Until now a step at the end of a branch always failed
the whole run, because the tolerate-this-failure setting lived on connections
and a last step has none; see finding **Q**.

### 35. "Our other system can call you — turn that into a text message"

**It works when:** another system POSTs to a private webhook URL and the person
named in the payload gets an SMS, with the request rejected if it isn't signed.

**Verdict: Supported.** `35-webhook-to-sms.json`. The webhook's accepted tokens
are a stored secret rather than a literal in the flow, callers without one are
turned away by the endpoint, and every message is logged with the time so
there's a record of what the other system asked for.

---

## What had to change

Working through the thirty-five turned up three asks that couldn't be built at
all, several more that needed a workaround nobody would find, and a set of
defects behind them. All are closed; each entry says what was wrong, what
shipped, and where the guard lives. **A–H** came out of the first ten,
**I–Q** out of the twenty-five.

### A. Formulas couldn't emit rows or objects — *fixed*

A CEL result that was a map came back as `map[any]any`, which `encoding/json`
refuses and every row consumer rejected:

```
expression  expr: input.map(r, {'email': r.email})
  → build_csv / dedupe_rows / map_rows / sort_rows / render_table
      bad_input: row 0: expected object, got map[interface {}]interface {}
json.Marshal(output) → json: unsupported type: map[interface {}]interface {}
```

The same for a single object (`{'a': input.id}`) and for a `compute_rows`
column whose formula returned a map; scalars and lists of scalars were fine.
"Build a row with a formula" is the obvious move in use cases 4, 7 and 10, and
it validated clean and then failed at run time.

`unwrapCEL` (`drops/transform/compute_rows.go`) handed composites to
`ConvertToNative(any)` and returned whatever came back. It now normalises them
recursively, which covers both callers. Pinned by
`TestExpression_ObjectResultIsJSONShaped`,
`TestExpression_RowListResultIsJSONShaped` and
`TestComputeRows_ObjectValuedColumnIsJSONShaped`.

### B. Incoming email attachments were invisible — *fixed*

`gmail_get_message` exposed `date`, `from`, `subject`, `body` and nothing else:
no attachment port, no attachment id, no `raw` mode. Dazyflow could *send*
attachments (`gmail_send_email`, `email_send`, both variadic) and `drive_upload`
accepted any file — the only missing link was getting the bytes out of a
received message, which blocked use case 6 outright.

New step **`gmail_get_attachments`** (Gmail — Download attachments): `first` is
a file ref that wires straight into a filing step, `files` lists them all with
name/type/size/path, and "Only these types" takes just the PDFs. Inline
signature images are skipped (no filename), files land in the run's scratch area
or a named workspace folder, and sender-controlled filenames are sanitised —
`TestGetAttachments_HostileFilename` pins that `../../etc/passwd` can't escape.

### C. Google Calendar's window couldn't be relative or wired — *fixed*

`time_min`/`time_max` took RFC3339 only, with no input ports, so no flow could
say "tomorrow" — the standard reason anyone reads a calendar on a schedule. The
window had to be left wide open and the rows filtered afterwards.

Both ends now accept `now`, `today`, `tomorrow`, `yesterday`, `+3d`, `-2h30m`,
`tomorrow+9h` or an absolute timestamp, take a `tz` for the day boundaries, and
can be wired from an upstream step. The grammar is `drops/internal/reltime`,
shared with the Date step's offset parser so `3d` means one thing everywhere.
`TestListEvents_RelativeWindow` pins that a nightly run gets exactly the next
local day.

### D. After a left join, `has()` was the wrong test — *fixed*

`join_rows` fills unmatched right-hand columns with `null`, not absence. So
`!has(row.synced_at)` was false for exactly the rows that hadn't matched, and
the intuitive "only the ones I haven't synced yet" filter dropped **every** row.
No error, no warning — the sync just never wrote anything.

`kind: "anti"` now answers the question directly: the left rows with no match on
the right, carrying only their own columns, so there is no null to misread.
`TestNotYetSyncedFilter` checks the anti join *and* keeps the old trap pinned,
so if `has()` ever starts working the note here gets revisited rather than
quietly rotting.

### E. Param-only fields couldn't take a wire — *fixed*

ntfy's *Link to open* was the sharp example: the step's own help text said to
wire an approval step's link into it, but it had no input port, so the link had
to be spliced into the message body by an extra formatting step. It now takes a
wire, like Title and Message (`TestNtfy_ClickInputOverridesParam`).

### F. Two row steps, two shapes for "how" — *fixed*

`sort_rows` takes a friendly string (`by: "revenue,-created_at"`);
`group_aggregate` demanded nested objects and rejected the
`{"revenue": "sum"}` a person would guess with `expected object, got string`.
The short form — the op alone, with the output name doubling as the source
column — is now accepted alongside the long one.

### G. Half the asks had no template — *fixed*

The gallery shipped five, covering use cases 1, 2 and most of 3. Six more now
ship, each forked from a graph in `tests/usecases/`:
`payment-to-thanks-and-log`, `ai-email-triage`, `approve-before-refund`,
`invoices-to-drive`, `watch-a-page` and `site-up-or-down`. The last two need
only an ntfy topic, so besides the demo they are the templates a newcomer can
run with no account connected at all. The other twenty-nine graphs under
`tests/usecases/` are template-ready if the gallery wants them: a title, a
use-case line and an index entry each.

### H. A loop body couldn't start with a piece of text — *fixed*

A For each body is the nodes reachable from its `body` pin, and the current
item reaches them as `${item.…}` **in their own settings**. The Text step is a
value source with no pass pin, so it can't be in a body — which meant there was
no way to run a Regex over a field of the current item: Regex took its text only
from a wired input, and there was nothing upstream to wire. Regex now accepts
the text as a setting (`text: "${item.description}"`), with a wired input still
winning. That is what makes use case 7's phone-number extraction one step
instead of impossible.

### I. A loop couldn't hand a step anything but text — *fixed*

A For each body has no upstream node to wire from: its steps see the current
item through `${item.…}` in their own settings. Those settings were resolved as
text, so a step needing structure — a shipment's address object, an email
template's merge data, a list of invoice lines — received JSON *as a string* and
could not read it. "One X per row" was therefore only buildable when X needed
nothing but scalars.

A setting whose whole value is one `${item.…}` reference now keeps the value's
real shape, exactly as `${resource.…}` already did; inline references inside a
sentence, and scalars, are unchanged. That is what makes use case 29 (each
customer's own statement) and 20 (a nested shipment) work.
`engine/loopitem_test.go:TestItemWholeValue_KeepsStructure` and
`TestResolveParams_StructuredItemValue` pin it.

### J. More fields that couldn't take a wire — *fixed*

Finding **E** was one instance of a pattern, and the twenty-five found four more
where the value obviously comes from whatever started the flow:

- **Create event's start, end, description, location and attendees.** A booking
  made from a form or a row varies by all of them; they were typed settings
  only, so a calendar entry could never carry the dates someone had just
  submitted. Start and end also accept a relative value now ("tomorrow+9h"),
  while an absolute plain date still means an all-day event.
- **Slack's reply-in-thread.** Replying under the message that started the flow
  needs the trigger's timestamp — the step's own code comment had already
  anticipated it.
- **Drive's file name.** A weekly backup names its file after the date it ran;
  as a typed setting, every backup overwrote the last.
- **Read range's row numbers** (see **K**), which the write-back depends on.

### K. A spreadsheet could be read and added to, never changed — *fixed*

There was no way to mark a row done. Every "handle the new rows" flow therefore
had to keep a private ledger of what it had already processed and filter against
it — workable for a database, but nonsense to a person looking at a Status
column that never fills in. Three of the twenty-five (12, 15, 20) ask for
exactly this.

Read range can now include each row's **real position in the sheet**, and the
new **Update cells** step writes values back into those rows — a column the
sheet doesn't have yet is added with its header. Together they close the loop:
read what's outstanding, act, mark it done, and skip it next time.

### L. Stripe events name a customer you can't write to — *fixed*

Every payment and subscription event carries a `cus_…` id, never an email, and
Stripe's search API cannot look up by id — so "email the person who just
cancelled" had no path. The new **Get customer** step turns the id into an email
and a name.

### M. Delay is for minutes, not days — *documented*

Steps have a wall-clock cap (30 minutes by default, adjustable per step), so a
Delay is the right tool for an escalation ladder and the wrong one for "follow
up in three days". The durable shape is a record plus a scheduled sweep, which
is how 18 and 25 are built. Not a defect, but the reason two of the twenty-five
are two flows rather than one.

### N. Formulas had no string functions — *fixed*

No substring, no split, no trim, no upper/lower-case. "The first ten characters
of the date", "tidy up these addresses", "shorten this into a title" — all
unsayable, each needing an extra step or simply impossible. The formula
environment now includes the standard string helpers (`substring`, `split`,
`join`, `replace`, `trim`, `lowerAscii`, `upperAscii`, `indexOf`, `charAt`), in
both the row formulas and the Expression step, so the grammar is learned once.
Three of the twenty-five needed them.

### O. Email steps could only see one message at a time — *fixed*

"Has anyone answered?" is a question about a conversation, and every mail step
worked on single messages. Gmail has no "unanswered" search operator either, so
use case 30 had no path at all. The new **Read conversation** step returns a
thread's messages and answers the actual question: Replied is No while the
newest message in the thread is still one of yours.

### P. Nothing could watch whether a site was up — *fixed*

Round one's page watcher compares *content*, and treats a bad response as a
failure — so it can't answer "is it up?", and a run that fails is not a run that
tells you why. The new **Is it up?** step reports up or down as a result, and
fires only on the transitions: once when it breaks, once when it recovers,
nothing in between. A site already down on the first check does fire, because
that is news.

### Q. A step at the end of a branch always failed the whole run — *fixed*

The tolerate-this-failure policies live on connections, so a terminal step — a
notification, the last write — had nowhere to hang one, and its failure always
marked the run failed. In a fan-out that is plainly wrong: Discord being down
is no reason for the Slack post and the email not to count. Steps can now be
marked **non-critical** individually, which is what use case 34 needs.
`daemon/dispatch_classify_test.go:TestFailurePropagates_ContinueOnError` pins
the rule, including that it holds for a terminal step.

### S. Comparing a count against a number failed — *fixed*

Steps report counts, status codes and spreadsheet cells as **text**, so wiring a
count pin into "is greater than 0" — which is what use case 32 does to ask
"is there a booking?" — failed with `non-numeric operand in <,> comparison:
string vs float64`. Nothing about that message tells the person who wired it
what to do, and there was nothing to do: no step converts text to a number.
Compare now reads numeric text as the number it plainly is, on either side.
Text that genuinely isn't a number still fails, rather than silently counting
as zero.

### R. Two checks were stricter than the product — *fixed*

Both surfaced while building these, and both would have blocked correct flows:
the template guard rejected a required setting that was satisfied by a *wire*
(the product's own "fill it in, or connect it" model), and the schema check
rejected a `${…}` reference in a field whose declared type isn't text — which is
every structured setting a loop body fills in. Fixed in `tests/scenarios/
templates_test.go` and `tests/usecases/usecases_test.go`.

### T. The generator's own instructions couldn't build a loop — *fixed*

Found by walking the generator by hand. The catalog rows are generated from the
manifests, so they stay accurate; the guidance above them is hand-written, and
it had gone stale in the worst possible place. It said:

> wire for_each.body into the per-item step's input

which is the footgun this repo already documents — the body pin is a *control*
pin, and pointing it at a typed input injects the whole row where a string was
expected. Worse, the guidance never mentioned `${item.…}` at all, so nothing
told the model how a step inside a loop reads the current item. The syntax
appeared in the whole 53 KB prompt exactly twice, both times incidentally,
inside two steps' example params.

A model could recover from that only by choosing to call `describe_drop` on
`for_each`, whose examples do explain it well. Twelve of the thirty-five
scenarios use a loop, so a third of the corpus rested on the model taking an
optional detour. The guidance now states the mechanism: what the body pin is,
that body steps read `${item.field}` from their own params, and that a param
whose whole value is one reference keeps the value's real type (finding **I**).
`TestFlowGenPromptTeachesLoopBodies` pins each of those facts, since this is the
part of the prompt that goes stale as steps gain abilities.

### U. A failed step could mark its work as done — *fixed*

Found by breaking the invoicing API mid-scenario. Use case 12 read the finished
jobs, invoiced each one in a loop, then stamped the sheet — and the stamp ran
off the loop's *completion*, not its success. With the invoicing service down:

- every invoice failed,
- the run reported **succeeded**,
- both jobs were stamped `invoiced_on` anyway,
- so the retry skipped them, and **the customer is never billed**.

Silent, financial, and unrecoverable without someone noticing by hand. Two
things were wrong, and both are fixed:

- **The marking has moved inside the loop.** Each row is stamped by its own
  iteration, after its own invoice succeeded, so a partial outage leaves
  exactly the rows that worked marked and the rest still workable. Use cases 15
  and 20 had the same shape and got the same treatment.
- **A loop where every item failed now fails.** "Carry on past a bad row" is
  the right default for one bad row among many; reporting success when nothing
  at all worked is an outage dressed up as a result. A partial failure still
  continues and surfaces on the `errors` port, unchanged.

`TestUseCase12_AnOutageMustNotMarkTheJobsDone` covers the whole arc, including
that the same jobs invoice cleanly once the service is back.

### V. The approval link never reached anyone — *fixed*

The await-approval step's own description says to put it *before* the step that
notifies a person, and to wire its link into that notification. That could not
work: the dispatcher treated a parked step as having produced nothing, so the
notify step waited for the approval — the notification about the thing needing
approval only went out *after* it had been approved.

Nobody would ever have been told there was something to approve. It affects
every approval flow (use cases 9, 22 and 25) and the shipped
`approve-before-refund` template. The existing test suite missed it because it
read the link straight out of the run record, which is not how a flow uses it.

A parked step's **emitted** ports are now live immediately, while the ports
that only arrive with the decision (`approved`, `rejected`) keep their branches
waiting — not skipped. Parking advances the run so those dependents are
considered, and because dispatch is keyed on each node's stable record id,
re-dispatching them on resume is a no-op rather than a second notification.

### W. The journey harness disabled the very dedupe it should test — *fixed*

The test stack never wired the per-node state stores the daemon gives the drops
(`cursor.*`, the HTTP-cache pair, poll state). Every run therefore looked like a
first run: "only new since last run" would have re-emitted the whole mailbox, a
feed would re-fire every item, an up/down watch would alert on every check —
and no test could have caught it, because the harness had quietly removed the
mechanism. Wired now, for every journey.

### X. The race suite couldn't finish — *fixed*

`make test` runs `go test -race ./...`, and the daemon package — the HTTP API,
the dispatcher, the worker and their end-to-end tests — takes about 13 minutes
under the race detector, past Go's default 10-minute per-package ceiling. The
suite failed on a timeout rather than on a test, and the report named whichever
test happened to be running when the alarm went off, which is a confusing way
to find out. `make test`, `make ci` and the CI manifest now allow 30 minutes.
The ceiling is per package, so this is headroom for the slowest one rather than
a licence for a slow suite. Verified clean: the whole suite under `-race`, and
the scenario runs repeated with `-count=3`.

---

## Scoreboard

Thirty-five asks, all supported. "New step" marks one that needed a capability
that did not exist.

| # | Ask | Verdict |
| --- | --- | --- |
| 1 | Contact form → spreadsheet + ping | Supported (template) |
| 2 | Important email → Slack | Supported (template) |
| 3 | Weekly spreadsheet digest by email | Supported |
| 4 | Payment → thank-you, ping, sales log | Supported (template) |
| 5 | AI reads, sorts and drafts replies to email | Supported (template) |
| 6 | File emailed invoice attachments | Supported (template) — new step |
| 7 | Remind customers before an appointment | Supported |
| 8 | Watch a source, alert on anything new | Supported (template) — new step for pages |
| 9 | Approve before anything goes out | Supported (template) |
| 10 | Keep two systems in step | Supported |
| 11 | Chase a bounced payment | Supported |
| 12 | Job marked Done → invoice it, mark the row | Supported — new step |
| 13 | Someone cancelled → log it, say goodbye | Supported — new step |
| 14 | Do payments match invoices? | Supported |
| 15 | Warn before a contract renews | Supported |
| 16 | Enrich a new lead with company data | Supported |
| 17 | Keep spam off the contact form | Supported |
| 18 | Ask for a review a few days later | Supported (two flows) |
| 19 | Spot the unhappy feedback | Supported |
| 20 | Ship it and text the tracking link | Supported |
| 21 | Set a new starter up | Supported |
| 22 | Time-off requests with approval | Supported |
| 23 | Run the standup | Supported (three flows) |
| 24 | Slack mention → a real ticket | Supported |
| 25 | Escalate an alert nobody picked up | Supported (two flows) |
| 26 | Weekly backup to Drive | Supported |
| 27 | Clean the mailing list | Supported |
| 28 | Monthly numbers to the bookkeeper | Supported |
| 29 | A personalised statement per customer | Supported |
| 30 | Emails nobody answered | Supported — new step |
| 31 | Warn the crew about tomorrow's weather | Supported |
| 32 | Heating on before the first booking | Supported |
| 33 | Website down — and back up | Supported — new step |
| 34 | Announce once, post everywhere | Supported |
| 35 | Another system calls us → a text message | Supported |
