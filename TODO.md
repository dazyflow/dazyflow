# TODO — drop catalog gaps

Assessment of the built-in drop catalog (~90 registered drops) and where to
invest next. See the "drops/" tree; standard-library buckets are
flow / transform / value / net / io / secrets / trigger.

## Current coverage (strong)

- **Flow control:** branch, switch, if, compare, not, contains, merge,
  for_each, delay, await_approval, subgraph, limit
- **Row/data transforms:** parse_json, map_rows, compute_rows, route_rows,
  sort_rows, group_aggregate, join_rows, split_rows, dedupe_rows,
  unwrap_results, render_text/table/template
- **Values / IO / net:** text, number, json, url, phone, http_request,
  webhook_send, webhook_input, http_download, http_upload,
  file_read/write/picker, secret_set, built-in KV store
- **Triggers:** cron, poll, webhook, google_form, manual

This is on par with n8n's core node set, so "more apps" is not the only lever.

## Missing standard primitives (do these first — pure, no OAuth/connection)

1. **Date/time** — ~~no `date_format` / `date_math` / `now` drop~~. **DONE:**
   `date` drop (`drops/datetime/`) — parse/now + offset + timezone + format.
2. **Code / expression drop** — ~~no generic value-level expression node~~.
   **DONE:** `expression` drop (`drops/transform/`) — a CEL formula over the
   input value (`input`, `now`), the value-level sibling of compute_rows. (For
   arbitrary OS commands there's still the Shell drop.)
3. **Format converters** — **DONE:** `parse_json` (pre-existing), `parse_csv` /
   `build_csv` and `parse_xml` (`drops/transform/`), and `base64` + `hash`
   (HMAC-capable, `drops/encoding/`).
4. **String / regex helper** — **DONE:** `regex` drop (`drops/transform/`) —
   extract / replace / split / match, with capture groups as columns.

All four missing primitives are now shipped. Also added: **phone** drop
(`drops/value/`) — validate/normalize a number to E.164 with a region default,
the SMS-input sibling of `url` (backed by libphonenumber). Lower priority
remaining: uuid / random, an explicit `filter` (route_rows mostly covers).

## App connector gaps (breadth — ongoing, prioritize by demand)

Already covered: Slack, Discord, ntfy, MQTT, Gmail+SMTP, Calendar,
Drive/Sheets/Excel, Notion, GitHub/git, Postgres/MySQL/SQLite, Stripe,
Claude/OpenAI, weather, Home Assistant, SMHI, **Fortnox**, **46elks**.

Notable absences:
- **Microsoft 365** (Teams, Outlook, OneDrive) — biggest gap for business users
- **PM / trackers** (Jira, Linear, Asana, Trello, Airtable)
- **Telegram** (cheap, popular, fits the prosumer / self-hosted lean)
- Cloud storage (S3 / Dropbox / Box), CRM (HubSpot / Salesforce), support (Zendesk)
- ~~RSS~~ — DONE (`rss` drop; pairs with the Interval trigger)

## Nordic go-to-market (current strategy)

Target market is now **Sweden-first, then the Nordics** — so connector
breadth is ranked by Nordic-local demand, not generic popularity. The moat is
the local services the global players (Zapier / Make / n8n) don't build.

Shipped this session:
- **Fortnox** (`drops/fortnox/`) — Sweden's dominant SMB accounting: create
  customer, create invoice, list invoices (paid-invoice poll), customer picker.
  First OAuth connector needing `client_secret_basic` — added to the daemon's
  OAuth registry (see `daemon/oauth.go` `postTokenForm`).
- **46elks** (`drops/elks/`) — Swedish/Nordic SMS; static-key (no daemon
  change), Twilio-shaped.
- **phone** value drop — E.164 normalize/validate (feeds the SMS drops), with a
  live country-flag hint on the card for international input.

Next Nordic candidates (ranked, all deferred): Signicat/BankID (eID —
OAuth + session polling), Klarna (payments — static key, rich domain),
nShift (shipping), Roaring (org-number enrichment), Visma (all-Nordics
accounting). **Telegram** still fits the European lean above M365 / CRMs.

## Recommendation

Missing primitives are shipped. Connectors are now demand-led breadth work,
ranked by the **Sweden-first / Nordic** strategy above: prefer static-key
connectors (no daemon change — 46elks was ~½ day) for momentum, and pay the
OAuth/token tax only where the market needs it (Fortnox, later Signicat).

## Task list

- [x] **date** drop — parse/now + timezone + offset + format (`drops/datetime/`)
- [x] **parse_csv** / **build_csv** drops (`drops/transform/`)
- [x] **expression** drop — CEL formula over a value (`drops/transform/`)
- [x] **base64** + **hash** (HMAC) drops (`drops/encoding/`)
- [x] **parse_xml** drop (`drops/transform/`)
- [x] **regex** drop — extract/replace/split/match (`drops/transform/`)
- [x] **rss** drop — RSS/Atom reader, Interval-paired, cursor dedupe on by default (`drops/rss/`)
- [x] **phone** drop — E.164 validate/normalize + region default, card flag hint (`drops/value/`)
- [x] daemon **`client_secret_basic`** OAuth support (`daemon/oauth.go`) — Fortnox needs it
- [x] **Fortnox** connector — customer/invoice + poll, OAuth (`drops/fortnox/`)
- [x] **46elks** connector — SMS, static-key (`drops/elks/`)

### Nordic connectors (next, ranked)
- [ ] Signicat / BankID — eID auth & signing (OAuth + session polling)
- [ ] Klarna — payments (static key; thin first slice)
- [ ] nShift — Nordic multi-carrier shipping
- [ ] Roaring — org-number → company/credit enrichment
- [ ] Visma — all-Nordics accounting (broadens beyond Fortnox/SE)

### Other breadth (demand-led)
- [ ] Telegram connector (fits the European lean)
- [ ] Microsoft 365 (Teams / Outlook / OneDrive)
- [ ] PM trackers (Jira / Linear / Airtable)
