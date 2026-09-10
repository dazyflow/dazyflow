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

## 9. Collapse the AI provider × task matrix  — status: WON'T DO (2026-09-10)

20 drops: 4 providers (ChatGPT, Claude, Gemini, Ollama) × 5 tasks (ask,
classify, extract, summarize, draft_reply), identical in shape. Five drops with
the provider as a param would say the same thing.

**Decided against, on three counts.**

*The logos are the feature.* Someone with a ChatGPT account scans the palette
for the ChatGPT mark; a `provider` dropdown inside one generic "Ask AI" step
hides the exact thing they are looking for. Zapier and n8n keep per-vendor
nodes for the same reason. Every drop carries its provider's Icon, Color and
BrandLogo, and the Apps page's connection cards are per-provider too.

*The measured cost is small.* The matrix is 20 of 199 catalogue rows and 6,833
of 58,157 characters — 11.7% of the flow generator's prompt, ~1,700 tokens.
Collapsing to five saves ~1,300, or 9% of the prompt.

*Retrieval is not actually harmed.* "summarize", "classify", "extract fields"
and "draft a reply" each rank the right task first with the four providers
clustered at the top, which is the correct answer — the model then picks by
which account is connected. The retrieval complaint that motivated this item
was a different bug (below).

**It is also blocked, if anyone revisits it.** The 20 come from four
`llmtask.Config` values, so the code duplication is small — only the catalogue
is 20 wide. Collapsing needs a step whose CONNECTION is chosen by a param, and
`engine/secrets.go` injects from the manifest's static `Integration`, so one
`ask` drop with a provider dropdown could never be handed the right key. Item 8
sidestepped that by giving Knowledge a connection of its own; item 9 cannot.
Making injection dynamic means letting an author's param choose which stored
secret is injected — only ever behind a manifest-declared allowlist, and think
about `base_url` overrides first.

---

## 9b. Short queries lose to incidental substrings — status: DONE (2026-09-10)

Found while measuring item 9. A search for "ai" returns `await_approval`,
`contains` and `email`, and none of the AI steps:

    email           150   id contains "ai" (+100), label contains "ai" (+50)
    await_approval  150   aw(ai)t
    contains        150   cont(ai)ns
    claude           55   exact tag match on "ai"

`matchScore` (daemon/search.go) awards a raw `strings.Contains` on id and
label, so an incidental substring beats an exact tag three to one. The same
function already fixed this class for prose — its own comment: as a substring,
"form" hit "format", which ranked build_csv above form_input — by switching to
`containsWord`; the id and label paths were left raw.

Not a straight swap: `containsWord("google_sheets_read", "sheet")` is false,
and losing that hit would be its own regression, so the id path likely wants
the three tiers `tagScore` already uses (exact / word / stem) rather than one
`Contains`. It is a scored system with tests, so it deserves its own pass.

Half of this was already known and guarded: `TestRetrieval_SwedishNoiseFindsNothing`
catches the same trap for Swedish filler ("min" inside ge-min-i, "en" inside
builtin_store_app-en-d), but only for two-word queries — its comment says a
bare one-word search is a deliberate act, so single words were left out on
purpose. "ai" is what that exclusion cost: a one-word query that means
something, answered by three steps that merely contain the letters.

**Shipped.** `matchScore` now tiers the id and label the way `tagScore`
already tiered its tags: a delimited word in the id keeps +100, a bare
substring drops to +30 (label 50 → 15), which puts it below an exact tag
match's 55. The substring tier stays because it is load-bearing — "mail"
reaches gmail/imap and "sheet" reaches google_sheets_read only as a substring,
neither delimited nor a prefix — and the guard asserts both still work.

The 83-ask benchmark is unchanged by it: hit@1 74% (62/83), hit@5 98% (82/83),
Swedish 93%, identical before and after.

**"db" was a second, unrelated cause.** The SQL steps carried the tag
`database` but never `db`, while three transform DESCRIPTIONS mention "a DB
query" in passing and scored +20 each on it — so the abbreviation found the
wrong half of the catalogue. Nine SQL drops gained a `db` tag; the descriptions
were left alone, since "a DB query" is the right prose.

Pinned by `TestRetrieval_ShortQueriesMeanWhatTheySay`, which asserts the first
hit for a short query CARRIES the word as a category or tag rather than naming
today's ids.

**Not touched:** the prefix tier still answers "js" with `json` (id prefix,
+250) ahead of `code` (exact tag, 55). That one is defensible — it is what
makes as-you-type prefix matching work — and `code` is still rank 2.

## 10. `expression` points at a step most users cannot see  — status: DONE (2026-09-10)

Its description ends "For running real OS commands or scripts, use the Shell
step instead" — but `shell` is unregistered without `DAZYFLOW_ENABLE_SHELL`.
**Shipped:** it now points at Code for more than one line, and at Run on your
machine for work needing a network or a library. Nothing in the catalogue names
the opt-in shell step any more.

---

# Second round (2026-09-10)

Audited again after the ten steps landed — this time asking what breaks when
someone builds with what is there, rather than what a competitor has. Six
findings, all fixed.

## 11. "is empty" did not mean empty — status: DONE (2026-09-10)

`evaluate("not_exists")` is `a == nil`, and the enumNames ALREADY called it
"is empty". So an author guarding a weekly report with the operator labelled
"is empty" got the report anyway, because `[]` is not nil. A wrong answer, not
a missing one.

Measured before: `op=exists` took `then` for an empty list and `else` only for
nil. Nothing in the catalogue answered "only if there are any rows" or "skip
when the list is empty" — reachable only as CEL (`size(input) > 0`).

**Shipped:** `is_empty` / `not_empty` on both `if` and `compare`, via
`isEmptyValue` — nil, empty list/object (by reflection, since rows arrive as
several concrete slice types), blank or whitespace-only text. A number and a
boolean are never empty. `exists`/`not_exists` keep their behaviour exactly and
are relabelled "is set" / "is not set", so saved flows are untouched and the
misleading label is gone.

## 12. Nothing could take the first N rows — status: DONE (2026-09-10)

Twenty steps had a `limit`; every one was a source (list/query/search). Not one
row-shaping step could cap: build_csv, compute_rows, dedupe_rows,
group_aggregate, map_rows, sort_rows. So "top five by score" was `sort_rows` →
nothing, and the probe agreed ("top five by score" → compute_rows).

**Shipped:** `limit` on `sort_rows` (after the sort — that is what makes a
top-N) and on `map_rows` (after the filters). Canonical param name, matching
the twenty that already had one.

## 13. Three steps unfindable by their own words — status: DONE (2026-09-10)

Measured ranks before → after: "get every page from the api" 7 → 1
(`http_request`), "clean up old rows" 6 → 1 (`builtin_store_delete`), "scrape
the price off a product page" 2 → 1 (`parse_html`). Fixed in tags and Summary
only — `Summary` is not in the translated catalogue, so retrieval vocabulary
costs no Swedish. Checked that `web_watch` still wins "watch a web page for
changes" and `sort_rows` still wins "sort the rows".

The other twelve new steps already ranked first on a plain-language ask.

## 14. Knowledge could not forget a document — status: DONE (2026-09-10)

`deadcode -test` found `forgetSource` unreachable: I wrote it and never wired
it, so a base could be added to and searched but never cleaned — item 7's
defect in a new store.

**Shipped:** `knowledge_forget` ("Forget a document"), plus `forgetBase` for
emptying one behind an explicit switch, which also drops the `bases` row so the
name is free to be rebuilt under a different embedding model. It declares NO
connection fields — forgetting reads no embeddings — making it the only step in
the integration that works before Knowledge is connected. `deadcode -test` is
clean across the new packages now.
