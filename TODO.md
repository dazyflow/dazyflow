# Catalogue gaps

Working backlog from the 2026-09-10 audit of the built-in step catalogue
(188 drops registered: 64 built-in, 37 platform-generic, 87 app integrations).
The question behind it: can a Zapier / n8n / Dify user bring their flows here?

Ranked by how often the gap blocks a port. Take them one at a time.

---

## 1. A code step that needs no runner  — status: DONE (2026-09-10)

**Blocks:** Zapier "Code by Zapier", n8n "Code", Dify "Code". The single most
common non-trivial step on all three platforms.

Today the only escape hatches are `run_on_runner` (needs a machine the user
hosts) and `shell`, which is unregistered unless the operator sets
`DAZYFLOW_ENABLE_SHELL` — it is a full RCE primitive on a multi-tenant
deployment, so it stays off. `expression` (CEL, `internal/celexpr`) covers
one-line value math and nothing else: no statements, no loops, no multi-output.

**Shipped:** the `code` drop (drops/code) on goja, chosen so a pasted Zapier or
n8n snippet has a chance of running as written. The sandbox is internal/jsvm:
a bare ES global object — no network, no files, no imports — plus a console
that writes to the run log. Bounded by a whole-step deadline (5s default, 30s
ceiling) and an 8 MB result cap; NOT bounded on heap, which is documented at
the top of internal/jsvm and is why the default timeout stays short.

Two shapes: once over the whole input, or once per row (`row`, `index`), where
returning nothing drops the row — filter and transform in one step.

## 2. Pagination, or a bounded loop  — status: PARTLY DONE (2026-09-10)

**Blocks:** most n8n API workflows, Dify's Loop node, any "keep fetching until
the cursor runs out" pattern.

`core/topsort.go` rejects cycles, so the engine is a strict DAG. `for_each`
iterates a list that must already exist. `http_request` has no cursor handling —
only `github_list_issues`, `gmail_search_messages` and `notion_query_database`
paginate, each privately.

**Shipped:** the cursor mode on `http_request` (drops/net/http_paginate.go) —
`link`, `body` and `page` shapes, items joined across pages, and the bounds a
loop against someone else's server needs (page ceiling, one time budget and one
size budget for the whole run, per-hop egress checks, self-reference guard).

**Still open:** the general "repeat until". Paging is the case that blocked most
ports, but retry-with-backoff and poll-until-ready still have no expression. If
that turns out to matter, `repeat_until` (a condition-driven sibling of
`for_each`, still one node to the engine) is the next cheapest step; back-edges
in the engine remain the expensive last resort.

## 3. Extract from HTML by selector  — status: DONE (2026-09-10)

**Blocks:** every scraping port. `web_watch` notices a page changed and `regex`
can claw at markup, but there is no CSS-selector extractor. n8n's HTML node is
a staple.

**Shipped:** `parse_html` ("Read a web page", drops/transform/parse_html.go) on
goquery. `selector@attribute` field specs, an optional row selector that turns
one record into one row per match, and base-URL resolution for links and
images. Selectors are compiled up front so a typo names the field instead of
silently matching nothing.

## 4. Digest, and delay-until  — status: DONE (2026-09-10)

**Blocks:** Zapier's "Digest by Zapier" and "Delay Until", which have no
analogue here. `delay` takes only `ms`. Collections + a cron flow can fake a
digest, but it is four steps and a schema the user has to invent.

**Shipped:** `digest_add` + `digest_take` (drops/db/digest.go), backed by the
Collections store under a `digest_` prefix; taking reads and empties in one
transaction, and an empty digest fires its own output so a quiet night sends
nothing. `delay` gained `until` + `tz` on the reltime grammar, deferring the
same way a duration does.

## 5. Lookup table, and no-code text/number formatting  — status: DONE (2026-09-10)

**Blocks:** Zapier Formatter ports, and it is an ergonomics hole for the
audience this product is aimed at. Uppercase, trim, truncate, currency, "map
this value to that one" — all reachable today only by writing CEL in
`expression`. Capability present, shape wrong.

**Shipped:** `lookup` and `format_text` (drops/transform). Lookup maps through
a table of pairs, case-insensitive by default, with a fallback or a separate
No-match output. Change text covers case, tidy, shorten, default, literal
find-and-replace and split.

**Also shipped:** `format_number` ("Change a number") — write it out, round
(nearest/up/down), as money, as a percentage, in the flow's language or the
step's own. Separators are hand-rolled rather than taken from x/text so the
grouping stays an ordinary space: an invisible NBSP breaks CSV columns and
string comparisons downstream and nothing on screen says why.

## 6. `switch` matches on equality only  — status: DONE (2026-09-10)

**Blocks:** Zapier Paths, whose branches carry full conditions. A path meaning
"amount > 100 AND status = paid" needs a chain of `if`s here. `if`/`compare`
have the operators; `switch` and `route_rows` do not.

**Shipped:** a `filter` (row-condition) alternative to `equals` on each Switch
case, compiled by internal/rowcel like every other condition in the tree. The
payload binds to `row`; a non-object binds as `row.value`. Existing cases are
untouched.

**Note:** `route_rows` already routed rows by condition — it was only the
value-level Switch that could not. Nothing left open here.

## 7. Collections cannot delete  — status: DONE (2026-09-10)

`builtin_store_append` (upsert via `unique_by`), `builtin_store_find`,
`builtin_store_query` (SELECT only). State can be written and read, never
cleaned up — so "mark processed, then forget" has no ending.

**Shipped:** `builtin_store_delete` (drops/db/builtin_store_delete.go). Same
visual row conditions as Find, deleting by rowid inside one transaction so a
filter that errors halfway changes nothing; emptying a collection whole needs
the explicit `all` switch.

## 8. RAG: embed / upsert / retrieve  — status: DONE (2026-09-10)

**Blocks:** the whole knowledge half of Dify — Knowledge Retrieval, vector
stores, agents with tools. Only worth doing if Dify-shaped use cases are a
target; say so before building.

**Shipped:** `knowledge_add` + `knowledge_search` (drops/knowledge), on an
`llm.Embedder` added to the existing provider registry (ChatGPT, Gemini,
Ollama; Anthropic has no embeddings API). Vectors are float32, normalised on
write, in their own SQLite file beside the Collections one; search reads and
scores the whole base, capped at 20k passages. The embedding provider lives on
a "Knowledge" connection rather than in the step, which is what keeps this two
drops instead of six — see the note in item 9.

**Still open:** an agent with tools (Dify's Agent node, n8n's AI Agent). That is
a different shape from a step — a loop with tool calls — and the DAG says no to
the loop. Deliberately not attempted.

## 9. Collapse the AI provider × task matrix  — status: open

20 drops: 4 providers (ChatGPT, Claude, Gemini, Ollama) × 5 tasks (ask,
classify, extract, summarize, draft_reply), identical in shape. Five drops with
the provider as a param would say the same thing.

Not just tidiness: the flow generator's prompt is already ~14.5k tokens and
`search_drops` returns the wrong step; 20 near-identical entries are exactly
what poisons that retrieval. Migration for existing flows is the hard half.

## 10. `expression` points at a step most users cannot see  — status: DONE (2026-09-10)

Its description ends "For running real OS commands or scripts, use the Shell
step instead" — but `shell` is unregistered without `DAZYFLOW_ENABLE_SHELL`.
**Shipped:** it now points at Code for more than one line, and at Run on your
machine for work needing a network or a library. Nothing in the catalogue names
the opt-in shell step any more.
