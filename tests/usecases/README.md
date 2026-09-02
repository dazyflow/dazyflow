<!--
SPDX-FileCopyrightText: 2026 Angels' Ware
SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Scenarios — thirty-five things people ask an automation tool for

Jobs a non-technical buyer turns up with, written the way they'd say them out
loud rather than in Dazyflow vocabulary. Each one has an acceptance test ("it
works when…") and a verdict backed by a real graph. Ten openers first, then the
twenty-five they ask for next.

This is the *inbound ask* list. Its sibling,
[tests/scenarios/README.md](../scenarios/README.md), covers recurring internal
jobs a small company already does by hand. The two
overlap by design — where they do, this file links across instead of repeating.

## How the verdicts were reached

Every scenario was built as an actual graph in this directory and put
through the same authoring gate the product applies when a flow is saved
(`core.ValidateGraphFull`: unknown steps, missing ports, unsatisfied required
inputs, MIME and cardinality mismatches, placeholder and security lint), plus a
param-level check the gate doesn't do — unknown settings, missing required
settings, and each setting's declared type and enum.

Composing isn't running, so the formulas inside the graphs — the CEL filters,
the grouping, the column shaping, the parts a non-technical user cannot debug —
were additionally *executed* against sample data. That layer is
`tests/usecases/formulas_test.go`; both suites run with
`go test ./tests/usecases/`.

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
that is down, a carrier that is down. The outage tests carry through to recovery: the same work must
succeed on the next run once the service is back, which is the half that
proves nothing was silently written off.

### Driving it without a key

`FLOWGEN_MANUAL_DIR=… go test ./daemon -run TestFlowGenManual` runs the same
loop with a person — or another agent — playing the model. Each turn writes the
exact prompt the generator would have sent and waits for a reply file, so the
conversation can be walked one turn at a time and resumed. It needs no vendor
key, and it is the way to *read* what the model is working from: the system
prompt, the catalog rows, and what `describe_drop` actually returns.

Note the limit of it as a *score*: whoever plays the model here knows the corpus, so validity and app coverage
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
several that needed a workaround nobody would find. The gaps and the defects
behind them are in the changelog.

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
table into the mail body. The totals step accepts the short
`{"revenue": "sum"}` form a person would guess.

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
as one row; a formula works too. Ships as
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
folder, and appends a row with the vendor, amount and due date that AI read
**off the PDF itself**. The step's `first` output is a file ref that wires
straight into Upload to Drive *and* into the AI step's Files input; `files`
carries them all. Ships as the `invoices-to-drive` template. Previously
impossible: Dazyflow could send attachments but not read them.

Worth knowing what changed here, because it was wrong for a while: this flow
used to hand the AI step the EMAIL BODY, which on a real invoice mail says
"please see attached". The numbers it logged were whatever the model could
infer from a sender and a subject line. Now the document goes in, and because
the model reads rendered pages rather than a text layer, a scanned invoice
works as well as a generated one.

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
[tests/scenarios §6](../scenarios/README.md) is the same flow off a sheet.

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
[tests/scenarios §9](../scenarios/README.md) still does for price watching.
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
into ntfy's *Link to open*, so the notification is tappable.
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
left-join-plus-null-test this replaced validated clean and silently wrote
nothing.

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
impossible — Sheets could be read and appended to, never changed.

### 13. "Tell me when someone cancels, and why"

**It works when:** a cancelled subscription posts to the team, logs the churn
with whatever reason came with it, and sends the customer a courteous goodbye.

**Verdict: Supported** — needed a customer lookup. `13-subscription-cancelled.json`.
Stripe events name the customer by `cus_…` id, never by email, and Stripe's
search cannot look up by id — so there was no way to write to the person who
just left. The new Get customer step closes that.

### 14. "Check that what we were paid matches what we invoiced"

**It works when:** a weekly run lines up Stripe's payments against the invoice
sheet and reports the mismatches — paid but not invoiced, invoiced but not
paid, and wrong amounts.

**Verdict: Supported.** `14-reconcile-payments.json` full-outer-joins the two
sides on the invoice number and labels each row with what's wrong, so all three
kinds of mismatch come out of one pass and a clean week sends a table that says
so. The classifier is one formula, pinned by a test that walks through the
null-vs-missing trap a left join sets.

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
has a wall-clock cap (30 minutes by default), so Delay is for minutes.

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
needs a nested address object built per order — a loop-body step receiving
structured data. The number goes
through the Phone step first, so a mistyped one fails with "not a dialable
number" instead of a cryptic carrier rejection.

## People and internal operations

### 21. "Set a new starter up on their first day"

**It works when:** one form submission sends the welcome email, books the intro
meeting in the calendar, opens their onboarding checklist, and tells the team.

**Verdict: Supported** — needed wireable calendar fields. `21-new-starter-onboarding.json`
computes the meeting's title, start, end and attendee from the form and wires
them into Create event, which previously took only typed values.
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
Slack's reply-in-thread, which was previously a typed setting only. Trimming
the title uses the CEL string helpers.

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
backup would have overwritten the last.

### 27. "Clean up my mailing list"

**It works when:** addresses are normalised, phone numbers checked, duplicates
removed, and the tidy list written back — with a count of what was dropped.

**Verdict: Supported.** `27-clean-the-mailing-list.json` lower-cases and trims
addresses, drops anything that isn't one, removes duplicates keeping the newest,
and reports the count from Remove duplicates' own "dropped" output. The
normalising is one formula over the CEL string helpers.

### 28. "Send the bookkeeper last month's numbers"

**It works when:** a monthly run queries the database, writes a spreadsheet the
accountant can open, and emails it as an attachment.

**Verdict: Supported.** `28-monthly-accounts-export.json` — three steps: query,
write the workbook into the run's scratch space, attach it. Attachments are a
variadic input, so the file wires straight in.

### 29. "Send each customer their own personalised statement"

**It works when:** one run turns a table into one message per customer, each
containing only their own rows, laid out properly rather than as a data dump.

**Verdict: Supported** — structured handover into a loop body is what this
needs.
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
so this could not be asked before.

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
is `now` → `now+1h`, which only became sayable with relative calendar windows.
Two conditions are combined with an AND step so it acts only
when there's a booking *and* the heating is currently off; a day with no
bookings does nothing at all.

### 33. "Tell me the moment our website goes down — and when it's back"

**It works when:** a check every few minutes alerts on the first failure, stays
quiet while it's still down, and says so when it recovers.

**Verdict: Supported** — needed a new step. `33-site-up-or-down.json` uses the
new Is it up? step, which fires "Went down" on the check where it breaks and
"Came back" when it recovers, and nothing in between, so an outage pages you
once rather than twelve times an hour. It also takes a phrase the page must
contain, which catches the server answering 200 with an error page.

### 34. "Announce it once, post it everywhere"

**It works when:** one submission goes out to Slack, Discord, the mailing list
and the phone push in a single run, and a failure on one doesn't block the rest.

**Verdict: Supported** — the second half needed a new setting.
`34-announce-everywhere.json` fans one wording out to four channels, each marked
non-critical, so Discord being down still leaves the Slack post, the email and
the push counted as done. Until now a step at the end of a branch always failed
the whole run, because the tolerate-this-failure setting lived on connections
and a last step has none.

### 35. "Our other system can call you — turn that into a text message"

**It works when:** another system POSTs to a private webhook URL and the person
named in the payload gets an SMS, with the request rejected if it isn't signed.

**Verdict: Supported.** `35-webhook-to-sms.json`. The webhook's accepted tokens
are a stored secret rather than a literal in the flow, callers without one are
turned away by the endpoint, and every message is logged with the time so
there's a record of what the other system asked for.


### 36. "Do all that, but my mail isn't Gmail"

Same ask as 6, from someone whose mail lives on Fastmail, mailbox.org, Migadu,
or a mail server they run themselves. Until now the answer was no: reading mail
meant Gmail, and Gmail's read scope is one Google gates behind an app review
that a self-hoster cannot realistically pass.

**It works when:** invoices arriving at any IMAP account are found, the attached
PDF is filed in Drive, a row is logged with what AI read off the email, and the
mail is marked read so the next poll leaves it alone.

**Verdict: Supported** — needed a new app. `36-invoices-from-my-own-mail-server.json`
runs the whole of case 6 against a plain IMAP mailbox: Search emails finds the
invoice mail, Download attachments takes the PDF, the AI step reads **the PDF**
(with the email body alongside it as context) to pull the vendor and amount,
and Mark as read closes it off. The match records are the same shape Gmail's
search emits, so the graph is case 6 with the four Gmail steps swapped for
their Mailbox equivalents — no rewiring.

Worth knowing what this does *not* cover: Microsoft 365 has disabled password
logins for IMAP, so an M365 mailbox needs OAuth that the Mailbox connection
doesn't do yet.
