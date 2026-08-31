# Scheduled automation scenarios

Recurring jobs a small, non-technical company runs by hand today. Dazyflow
should automate each end to end without the operator writing code. Every
scenario names the building blocks it needs and, as the acceptance check, the
outcome that proves it works. A scenario that fails, or needs a step that does
not exist, is a gap to close.

Its sibling [/SCENARIOS.md](../SCENARIOS.md) covers the *inbound* asks a buyer
turns up with; this file covers internal jobs. Where they overlap, they link
across.

## How these are tested

Two layers, cheapest first.

1. **Composition** — `tests/scenarios/scenarios_test.go`. Each scenario has a
   graph under `tests/scenarios/NN-*.json`; every graph goes through
   `core.ValidateWithManifests` against the live native catalog, plus a check
   that each `for_each` step module exists. It proves the scenario composes:
   every module and port exists, the wiring is type-compatible, and required
   inputs are connected. `templates_test.go` applies the same guard to the
   shipped templates in `web/public/templates` — what a newcomer actually forks.
   Neither models the editor's variadic `port[N]` convention, so the tests
   normalize those before validating.

2. **Newcomer journey** — `tests/journey/`. Boots the real stack (the same HTTP
   API the web UI calls, plus a worker) and walks what a newcomer does: sign up,
   find the building blocks, see which accounts to connect, save, validate, fill
   in the blanks, run. A failure reads as "a newcomer would be stuck here."
   Three scenarios run the whole loop with the SaaS endpoints mocked at their
   HTTP base and a raw `token` param standing in for a connected account —
   lead-intake, overdue-invoice, weekly-sales-summary. The rest are exercised
   through save + validate.

No layer makes live Gmail/Sheets/Slack/Notion calls, so a "Proves it works" line
describes the behaviour to assert once real accounts are wired in.

Two runtime caveats neither layer covers: PDF export exists only for Google
Sheets (`sheets_export_pdf`), so a "PDF" deliverable routes through a Sheet; and
a no-input node cannot be sequenced *after* a writer by an edge, so
order-sensitive steps must share a data dependency.

Authoring note: pointing `for_each`'s `item_port` at a typed input injects the
whole row object there and the step rejects it. Feed per-item values through
`${item....}` in `step_params` and leave `item_port` at its default.

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
