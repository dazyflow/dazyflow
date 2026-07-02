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
- **Values / IO / net:** text, number, json, http_request, webhook_send,
  webhook_input, http_download, http_upload, file_read/write/picker,
  secret_set, built-in KV store
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

All four missing primitives are now shipped. Lower priority remaining: uuid /
random, an explicit `filter` (route_rows mostly covers).

## App connector gaps (breadth — ongoing, prioritize by demand)

Already covered: Slack, Discord, ntfy, MQTT, Gmail+SMTP, Calendar,
Drive/Sheets/Excel, Notion, GitHub/git, Postgres/MySQL/SQLite, Stripe,
Claude/OpenAI, weather, Home Assistant.

Notable absences:
- **Microsoft 365** (Teams, Outlook, OneDrive) — biggest gap for business users
- **PM / trackers** (Jira, Linear, Asana, Trello, Airtable)
- **Telegram** (cheap, popular, fits the prosumer / self-hosted lean)
- Cloud storage (S3 / Dropbox / Box), CRM (HubSpot / Salesforce), support (Zendesk), RSS

The Home Assistant + MQTT + SMHI mix suggests a prosumer / self-hosted /
European audience — if so, rank **Telegram and RSS** above M365 / CRMs.

## Recommendation

Ship the missing primitives first (small, no auth, multiply the value of every
existing app), then treat connectors as continuous demand-led breadth work.

## Task list

- [x] **date** drop — parse/now + timezone + offset + format (`drops/datetime/`)
- [x] **parse_csv** / **build_csv** drops (`drops/transform/`)
- [x] **expression** drop — CEL formula over a value (`drops/transform/`)
- [x] **base64** + **hash** (HMAC) drops (`drops/encoding/`)
- [x] **parse_xml** drop (`drops/transform/`)
- [x] **regex** drop — extract/replace/split/match (`drops/transform/`)
- [ ] regex extract/replace/split drop
- [ ] Telegram connector
- [ ] RSS trigger
- [ ] Microsoft 365 (Teams / Outlook / OneDrive)
- [ ] PM trackers (Jira / Linear / Airtable)
