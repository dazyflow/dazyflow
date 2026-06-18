# Scheduled automation scenarios

A reference set of recurring jobs that a small to mid sized company, staffed by
non-technical people, runs every day or week by hand today. Dazyflow should be
able to automate each one end to end without the operator writing code.

We keep this list so we can test against it: every scenario names the building
blocks it needs and the outcome that proves it works. When a scenario fails or
needs a node we do not have, that is a gap to close. Treat the "Proves it works"
line as the acceptance check.

## How these are tested

There are three layers, from cheapest to most lifelike.

1. **Composition** (`tests/scenarios/scenarios_test.go`,
   `tests/scenarios/templates_test.go`). Each scenario has a graph under
   `tests/scenarios/NN-*.json`. This loads every graph and runs it through
   `core.ValidateWithManifests` against the live native catalog, plus a check
   that each `for_each` step module exists. It proves the scenario *composes*:
   every module and port exists, the wiring is type-compatible, and required
   inputs are connected. `templates_test.go` applies the same guard to the
   shipped templates in `web/public/templates` (what a newcomer actually forks),
   which caught two real defects: both Gmail templates used a stale `step` param
   instead of `step_module` (so the `for_each` failed at run time, now fixed) and
   feed `for_each.results` (a list of Result wrappers) into a `compute_rows` that
   reads them as raw message rows (a shape mismatch that needs a results->rows
   flattening step; those two are skipped with that note until redesigned). It
   also surfaced that `ValidateWithManifests` doesn't model the editor's variadic
   `port[N]` convention, so the test normalizes those before validating.

2. **Newcomer journey** (`tests/journey/`). Boots the real stack (the same HTTP
   API the web UI calls, plus a worker that executes flows) and walks the steps
   a non-technical newcomer takes: sign up, find the building blocks in the
   catalog, see which accounts to connect, save the flow, let the app validate
   it, fill in the blanks, and run it. A failure reads as "a newcomer would be
   stuck here." Three scenarios run the whole loop for real, with the SaaS
   endpoints mocked at their HTTP base and a raw `token` param standing in for a
   connected account:
   - lead-intake (no account needed): webhook in, lead stored.
   - overdue-invoice (Sheets + Gmail): reads the sheet, computes days overdue,
     emails exactly the one unpaid+overdue client, logs back to the sheet.
   - weekly-sales-summary (Sheets + AI + Slack): filters to last week, totals by
     salesperson, has the AI write the recap, posts it to Slack.
   The remaining scenarios are exercised through save + validate.

3. **Lived walkthrough** (`tests/journey/WALKTHROUGH.md` + `screenshots/`). A
   narrated, screenshot-backed pass driving the actual web UI as a clueless
   user, recording real friction. Headline: the app is genuinely
   non-technical-friendly; the friction is self-host *setup* (register OAuth apps
   for Gmail/Slack/Notion, set `DAZYFLOW_WEB_ORIGIN`), not the product. The
   fastest path to first success is a no-setup template (contact form -> stored
   list), which works with zero configuration.

The composition layer does not make live Gmail/Sheets/Slack/Notion calls, so the
"Proves it works" lines describe the end-to-end behaviour to assert once real
accounts are wired in.

First run of the composition layer found two gaps, since closed:

- **`parse_json`** (new transform) turns JSON text, an AI step's output or an
  HTTP response, into rows + headers, tolerating Markdown code fences and
  surrounding prose. Without it, scenarios 3 and 9 had no way to feed extracted
  data into a tabular drop.
- **`notion_create_page` gained a `content` input** so upstream text (an AI
  summary) can become the page body. Before, the node took only static params
  and could not receive a value computed earlier in the flow (scenario 10).

Running the connected scenarios for real (layer 2) surfaced one more, since
closed, plus a scenario-authoring lesson:

- **The CEL env had no `now`.** Time-window filters ("overdue", "last week",
  "due tomorrow") are the backbone of these scenarios, but `compute_rows`,
  `route_rows`, and `split_rows` only exposed `row`, so any filter referencing
  the current time failed to compile at run time. `now` (a timestamp, bound at
  eval) is now in scope for all three. This is core to the non-technical
  promise: most recurring jobs are "do X to the rows from the last day/week".
- **`for_each` `item_port` footgun.** Pointing `item_port` at a typed input
  (e.g. a Gmail step's string `body`) injects the whole row object there and the
  step rejects it. Feed per-item values through `${item....}` in `step_params`
  instead, and leave `item_port` at its default. (Caught when overdue-invoice
  sent zero emails despite routing correctly.)

Two runtime caveats the composition check does not cover, noted where relevant:
PDF export exists only for Google Sheets (`sheets_export_pdf`), so a "PDF"
deliverable routes through a Sheet; and a no-input node (like `sheets_export_pdf`)
cannot be sequenced *after* a writer by an edge, so order-sensitive steps should
share a data dependency.

The blocks referenced map to real integrations in this repo: schedule and
webhook triggers; Gmail (search, get, send); Google Sheets (read, append,
export PDF); Excel (read, write); Slack (send, list); Notion (create page,
query database); MySQL and Postgres (query, insert, upsert); HTTP request,
download and upload; the AI (Claude) step; row transforms (map, filter/route,
group and aggregate, join, sort, dedupe, compute); flow control (branch,
for each, merge, sleep, human approval); and notify (email, ntfy, webhook).

---

## 1. Daily overdue invoice chaser

- **Persona:** Office manager at a 12 person agency.
- **Runs:** Every weekday at 09:00.
- **Today, by hand:** Opens the receivables spreadsheet, eyeballs which invoices
  are past due, and copies and pastes a reminder email to each client.
- **Flow:** Schedule trigger, read the invoice sheet, compute days overdue,
  route to the rows that are unpaid and past due, for each one send a reminder
  email, then append a "reminded on" timestamp back to the sheet.
- **Blocks:** schedule, sheets read, compute rows, route rows, for each, gmail
  send, sheets append.
- **Proves it works:** Only past due unpaid rows trigger an email, each email is
  addressed to the right client with the right amount, and the sheet records
  that the reminder went out so nobody is chased twice.

## 2. Weekly sales summary to Slack

- **Persona:** Founder who wants the numbers without opening a dashboard.
- **Runs:** Every Monday at 08:00.
- **Today, by hand:** Someone pivots last week's orders and pastes a recap into
  the team channel.
- **Flow:** Schedule trigger, read the orders sheet (or query the orders table),
  filter to last week, group and aggregate by product and by salesperson, sort
  by revenue, format the totals, post to a Slack channel.
- **Blocks:** schedule, sheets read or postgres/mysql query, route rows, group
  and aggregate, sort rows, slack send.
- **Proves it works:** A single message lands in the channel every Monday with
  correct weekly totals, top products, and per person figures that match the
  source data.

## 3. Daily inbox receipt and expense capture

- **Persona:** Bookkeeper drowning in forwarded receipts.
- **Runs:** Every day at 18:00.
- **Today, by hand:** Reads each receipt email, types the vendor, date and
  amount into a spreadsheet, and files the PDF.
- **Flow:** Schedule trigger, search Gmail for receipts and invoices since the
  last run, fetch each message, let the AI step pull vendor, date, total and
  category as a JSON array, parse that into rows, and append them to the expense
  sheet. (Filing the PDF attachment via http download/upload is a natural
  extension.)
- **Blocks:** schedule, gmail search, for each (gmail get), ai (Claude),
  parse_json, sheets append.
- **Proves it works:** Each receipt becomes one clean expense row with the right
  fields extracted, the attachment is filed, and already processed messages are
  not captured twice.

## 4. Weekly low stock reorder with approval

- **Persona:** Shop owner managing inventory in a spreadsheet.
- **Runs:** Every Friday at 16:00.
- **Today, by hand:** Scans stock levels, decides what to reorder, and emails the
  supplier.
- **Flow:** Schedule trigger, read the inventory sheet or table, compute which
  items are below their reorder threshold, build a reorder list, pause for a
  human approval link, and on approval email the purchase order to the supplier.
- **Blocks:** schedule, sheets read or db query, compute rows, route rows, human
  approval, gmail send (or notify email).
- **Proves it works:** Only items under threshold appear on the order, nothing is
  sent until a person clicks approve, and rejecting the approval sends nothing.

## 5. Monthly recurring invoice generation

- **Persona:** Solo consultant billing the same retainer clients each month.
- **Runs:** First business day of the month.
- **Today, by hand:** Duplicates last month's invoice template, edits the client
  name and amount, exports a PDF, and emails it.
- **Flow:** Schedule trigger, read the recurring clients sheet, and for each
  client run a per-client subgraph that fills the invoice template, exports it
  to PDF, and emails the PDF. (The multi-step-per-client work lives in a child
  graph driven by `for_each` + `subgraph`; PDF export routes through a Google
  Sheet via `sheets_export_pdf`.)
- **Blocks:** schedule, sheets read, for each, subgraph, sheets export PDF,
  gmail send.
- **Proves it works:** Each client receives one correctly numbered invoice PDF
  with their own line items and total, and a copy is filed for the records.

## 6. Daily appointment reminders

- **Persona:** Receptionist at a clinic or salon.
- **Runs:** Every day at 17:00 for the next day's bookings.
- **Today, by hand:** Looks at tomorrow's calendar and messages each client.
- **Flow:** Schedule trigger, read the bookings sheet, filter to appointments
  dated tomorrow, for each one send a reminder by email and a push notification,
  and append the booking to a "reminded" log so reruns can skip it.
- **Blocks:** schedule, sheets read, route rows, for each, notify email, ntfy,
  sheets append.
- **Proves it works:** Only tomorrow's bookings generate a reminder, each goes to
  the correct client, and same day reruns read the log and do not double remind.

## 7. Weekly new lead digest into the CRM

- **Persona:** Sales lead who never wants a web form submission to slip.
- **Runs:** Collected continuously by webhook, digested every Monday at 09:00.
- **Today, by hand:** Copies form entries from email into Notion and pings the
  team.
- **Flow:** Webhook trigger captures each form submission and stores it; a
  schedule trigger then dedupes the week's leads, creates a Notion page per new
  lead in the CRM database, and posts a count and the names to Slack.
- **Blocks:** webhook input, builtin store or db insert, schedule, dedupe rows,
  for each, notion create page, slack send.
- **Proves it works:** Every submitted lead is captured even between runs,
  duplicates are collapsed, each new lead exists as a Notion page, and the
  Monday recap matches the count of new pages.

## 8. Weekly timesheet roll-up for payroll

- **Persona:** Operations manager preparing hours for the bookkeeper.
- **Runs:** Every Friday at 17:00.
- **Today, by hand:** Collects each person's hours, sums them, and emails a
  summary.
- **Flow:** Schedule trigger, read the timesheet sheet, group and aggregate hours
  by employee, join against the rate or role table, compute pay totals, write a
  summary workbook, and email it to the bookkeeper. (A PDF deliverable would
  route the summary through a Google Sheet and `sheets_export_pdf`.)
- **Blocks:** schedule, sheets read, group and aggregate, join rows, compute
  rows, excel write, gmail send.
- **Proves it works:** Per employee totals add up to the source hours, rates join
  correctly, and the bookkeeper receives one summary covering the week.

## 9. Daily competitor or supplier price watch

- **Persona:** Category manager keeping an eye on a handful of product pages.
- **Runs:** Every morning at 07:00.
- **Today, by hand:** Visits each page, notes the price, and flags changes.
- **Flow:** Schedule trigger, make an HTTP request to the product page, let the
  AI step pull the current price as JSON, parse it into rows, join against the
  last stored price, compute whether it changed, route to the changed rows, and
  alert Slack only when it moved, recording today's price for next time.
- **Blocks:** schedule, http request, ai (Claude), parse_json, builtin store
  query and append, join rows, compute rows, route rows, for each (slack send).
- **Proves it works:** Prices are fetched for every URL, the stored baseline
  updates each run, and an alert fires only when a price actually changes.

## 10. Weekly feedback and review digest

- **Persona:** Customer success owner who wants the gist, not 200 rows.
- **Runs:** Every Monday at 10:00.
- **Today, by hand:** Reads through the week's survey responses or reviews and
  writes a short summary.
- **Flow:** Schedule trigger, read the responses sheet for the last week, feed
  them to the AI step to summarize themes and overall sentiment and surface the
  sharpest quotes, save the write up into a Notion page via its `content` input,
  and post the same summary to Slack.
- **Blocks:** schedule, sheets read, route rows, ai (Claude), notion create page
  (content input), slack send.
- **Proves it works:** The digest covers only the last week's responses, the
  summary reflects the actual content with representative quotes, and it is
  filed in Notion with a Slack pointer every Monday.
