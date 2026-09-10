# Changelog

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning: [SemVer](https://semver.org/spec/v2.0.0.html).

Versions correspond to git tags `X.Y.Z`. The running version is stamped into the
binary at build time and surfaced on `GET /api/v1` (the `build` block) and in the
web UI's account menu. Write under `[Unreleased]`, never under a released
heading; `make patch` (or `minor` / `major`) promotes it and tags.

## [Unreleased]

## [0.41.9] - 2026-09-10

### Added

- **"Is empty" now means empty.** If and Compare could ask whether a value was
  *there*, and the dropdown called that "is empty" — but an empty list is
  there, so a report guarded that way went out with no rows in it, silently.
  Two new tests answer the question that was actually being asked: **is empty**
  and **has something in it**, true of nothing at all, a list or object with no
  entries, and text that is blank or only spaces. A number is never empty — 0
  sales is a measurement — and neither is false. The old pair keeps its exact
  behaviour and is now honestly labelled **is set** / **is not set**, so no
  existing flow changes what it does.

- **Row steps can take the first few.** Twenty steps had a row cap and every
  one of them was a step that *fetches* rows — nothing that shaped them could
  cap at all, so "the top five by score" had no expression. **Sort rows** and
  **Choose & rename columns** now take a **Max rows**, applied after the sort
  and after the filters respectively, which is what turns a sort into a top-N.

- **Knowledge can forget a document.** Adding a source again replaced its
  passages, but nothing could take one out — the page that no longer exists,
  the policy that was withdrawn, the customer who asked to be forgotten.
  **Forget a document** removes one source's passages, or empties a whole base
  behind an explicit switch, which also frees the name to be rebuilt with a
  different embedding model. It needs no connection: forgetting reads no
  embeddings, so it works before Knowledge is set up.

- **Change a number.** Rounding was reachable through a formula; writing a
  number for the person who reads it was not reachable at all. 1,234.50 in
  English is 1 234,50 in Swedish, money goes $1,234.50 in one and 1 234,50 kr
  in the other, and a Swedish customer emailed an English number reads it as a
  mistake. The step follows the flow's own language unless you set Language on
  it — the same rule the Date & time step already uses.

  Four things: write it out with fixed decimals and grouped thousands, round it
  (to the nearest, or always up or down for the answers that have to cover
  something — boxes to order, a price ceiling), as money, or as a percentage.
  Rounding is the one that changes the number, so the number comes out on its
  own port alongside the text: a rounded value can be multiplied without being
  parsed back out of its own formatting.

  The thousands separator is an ordinary space, not the non-breaking one
  typography asks for. An invisible non-ASCII character survives an email and
  then quietly breaks a spreadsheet lookup, a CSV column and every comparison
  downstream, with nothing on screen to explain why. A number that arrives as
  text is read as a number, including the forms people actually write —
  "1 234,50", "1,234.50", "1.234,50".

- **Knowledge: let a flow answer from your own documents.** The retrieval half
  of what Dify calls a knowledge base, and the last thing on the porting list
  with no answer here at all. **Add documents** cuts a document into passages,
  turns each into numbers that capture what it is about, and stores them in a
  knowledge base in the workspace. **Find related** takes a question, finds the
  passages closest to it in meaning, and hands them over already joined and
  ready for a prompt — wire that into ChatGPT or Claude and the answer rests on
  your handbook rather than the model's memory.

  Two steps rather than six, because the embedding provider is part of a
  **Knowledge** connection you make once — ChatGPT, Gemini, or Ollama on your
  own machine — instead of a separate pair of steps per provider. That also
  buys the thing correctness needs: a base records the model that built it and
  refuses a question embedded by another, since numbers from two models are not
  comparable and ranking them would be ranking noise. Connecting embeds a probe
  string, so "Connected" means the key, the model name and the address all work
  together.

  Adding the same source twice replaces its passages rather than duplicating
  them, so a nightly re-read keeps a base current; a document with no source of
  its own is identified by its own fingerprint, so adding it twice stores it
  once. Passages live beside your Collections in the workspace — a plain file,
  backed up with everything else, no database to provision. A search reads and
  scores every passage in the base, which answers a thousand of them in tens of
  milliseconds; past twenty thousand in one base the step says so rather than
  quietly getting slow.

- **Expression points somewhere you can actually go.** Its description ended
  by sending you to the Shell step, which is unregistered unless the operator
  sets `DAZYFLOW_ENABLE_SHELL` — so most readers were pointed at a step they
  could not see. It now names Code for anything longer than a line, and Run on
  your machine for work that needs a network or a library.

- **Switch can route on a condition, not just a value.** Each of its eight
  matches took a value to be equal to, so "amount over 100" needed an If in
  front of it and "paid AND over 100" needed two — which is why a Zapier Path
  could not be brought over as one step. A match now takes either a value to
  look for, as before, or a **Condition** written with the same visual editor
  Find, Split rows and Route rows already show. First match still wins, so the
  strict one goes above the loose one.

  The condition sees the whole incoming value as `row`, which is what the
  editor's own output expects; a plain number or piece of text reads as
  `row.value`, since an editor that speaks in fields has no other way to say
  anything about one. Every existing Switch keeps working — a match with a
  value is exactly what it always was — and the two kinds can sit in the same
  step.

- **Look up.** Turning a value into another value — country code to country
  name, plan to discount, status code to words a person can read — meant an
  eight-case Switch or a chain of formulas, neither of which survives a table of
  twenty rows. **Look up** takes the table as pairs and swaps the value on its
  way through. Capitalisation is ignored unless you ask for it, so a table typed
  by hand matches what an API sends. Anything unlisted takes the fallback, or —
  with no fallback — leaves on a separate **No match** output carrying its own
  value, so a flow can handle the unknown ones instead of pretending they were
  fine. Exactly one of the two outputs fires.

- **Change text.** The small text jobs, without writing a formula: upper case,
  lower case, Title Case and Sentence case (both of which lower the rest first,
  so a name shouted in capitals comes out readable), tidy up the spaces — both
  ends and the runs in the middle, which is the fix for text pasted out of a web
  page — shorten to a length, fill in a blank when the text arrived empty, find
  and replace plainly, and split into pieces. Splitting hands the pieces back
  twice over: as a list, and as rows, so a comma-separated line becomes
  something a loop can walk. All of it was reachable through CEL before; none of
  it was reachable without learning CEL.

- **A digest: pile things up now, send them together later.** The one Zapier
  built-in with no equivalent here. Entries arrive one at a time over hours and
  have to leave together, which no single flow can hold — so it is two steps in
  two flows sharing a collection. **Add to a digest** puts a row, a record or a
  line of text aside and emits nothing but a count, because the point is that
  the flow does not act now. **Take the digest**, after a Schedule trigger,
  hands over everything and empties the pile in the same transaction, so an
  entry cannot go out twice and a failure leaves the pile intact for next time.

  An empty digest fires a separate **Nothing there** output rather than an
  empty list, so a quiet night sends no email unless you deliberately wire that
  side up. Entries live in an ordinary collection called `digest_<name>` —
  browsable in-app while they wait, and prefixed so naming a digest after a
  collection you already keep cannot empty that collection.

- **Collections can delete.** Rows could be saved and read but never tidied, so
  anything a flow remembered piled up for good. **Delete rows** takes the same
  visual conditions the Find step uses. Emptying a collection whole is
  deliberately harder: with no condition the step refuses unless **Delete every
  row** is also on. The collection itself survives, so the flow that fills it
  keeps working.

- **Delay can wait until a moment, not just for a while.** `Wait until` takes
  the same time words the calendar steps do — `tomorrow`, `tomorrow+9h` for
  tomorrow morning, `now+2h`, `+3d`, or a timestamp the Date & time step handed
  you — with a timezone deciding which day `tomorrow` means. A moment already
  past carries straight on rather than failing, so a flow that ran late still
  finishes. The wait defers exactly as a duration does: a flow parked until
  Monday hands its worker back and costs nothing while it waits.

- **Read a web page.** A page could be fetched and watched for changes, but
  nothing could say "the price is in `.product-price`". Extracting anything
  from HTML meant a regular expression against markup — which works right up
  until the page adds a space.

  **Read a web page** takes the HTML and a list of CSS selectors, the same ones
  a browser's inspector hands you. Each value is written `selector@attribute`,
  both halves optional: `.price` for text, `a@href` for a link's address,
  `@data-id` for an attribute of the element itself, `@html` for its inner
  markup. Name no row selector and you get one record read from the whole page;
  name the thing that repeats — `.product`, `table tbody tr` — and you get one
  row per match with every field read inside it, which is the shape Choose &
  rename columns, Write CSV, Sheets and the database steps already take. Set
  the page address and a relative link comes out whole.

  A selector that cannot be read fails the step naming the field it belongs to,
  rather than quietly matching nothing and filling a column with blanks — which
  is what the underlying library does on its own, and is indistinguishable from
  a page that changed shape.

- **Web request can fetch every page.** An API that answers in pages had no
  answer here at all: the graph is a DAG, so "fetch, and fetch again while
  there is a next page" has nowhere to live as a wire, and the three steps that
  do paginate (GitHub issues, Gmail search, Notion queries) each did it
  privately, out of reach of every other API.

  **Fetch every page** puts the loop inside the step. Pick how the API says
  where the next page is — a `Link` header, a field in the response body (a
  cursor or a whole address), or a page number that climbs until a page comes
  back empty — and point **Where the items are** at the list on each page. What
  comes out of Response is every page's items joined into one list, ready for
  the row steps.

  Every bound a loop that talks to someone else's server needs is in place: a
  page ceiling (ten by default, a hundred at most), the step's time limit and
  response-size limit spent across all the pages together rather than granted
  afresh for each one, a fresh private-address and allowlist check on every hop
  — the next address comes from the server, so it is as untrusted as any other
  field in the response — and an early stop when an API offers itself as its
  own next page. An unpaginated call is untouched.

- **A Code step.** Between a one-line formula and a machine you host there was
  nothing: `Expression` computes a single value, `Run on your machine` needs a
  runner, and the `shell` drop is rightly switched off on a shared deployment.
  So the most common step on every other automation platform — Zapier's Code,
  n8n's Code, Dify's Code — had no answer here, and any flow built around one
  could not be brought over.

  **Code** runs JavaScript inside Dazyflow. The value wired into `in` arrives as
  `input`, whatever you `return` leaves on `out`, and `console.log` writes to
  the run log while the flow runs. It runs once over the whole input, or once
  per row with the row as `row` and its position as `index` — and in per-row
  mode returning nothing drops the row, so one step filters and transforms at
  once.

  It has no way out of the process: no network, no files, no imports. That is
  the point — call APIs with **Web request**, read files with the file steps,
  then wire the result in. A script is held to five seconds by default (thirty
  at most) and to 8 MB of returned data; past either, the step fails instead of
  holding a worker. Work that genuinely needs a library or a network still
  belongs on **Run on your machine**.

### Fixed

- **Three steps that could not be found by the words for them.** Web request
  had just learned to fetch every page of an API and the catalogue never
  learned to say so — "get every page from the api" ranked it seventh, behind
  the page watcher and a Notion step. "Clean up old rows" ranked Collections'
  delete sixth, behind Sort rows, whose id carries the word. And "scrape the
  price off a product page" found the page *watcher* before Read a web page.
  All three now rank first, and neither the watcher nor Sort rows lost the
  queries that are genuinely theirs.

- **Searching the catalogue for a short word finds what it means.** A search
  for "ai" answered with `await_approval`, `contains` and `email` — steps that
  merely contain those two letters — and none of the AI steps; "db" answered
  with the CSV and JSON transforms, whose descriptions mention "a DB query" in
  passing, and none of the databases. Ranking scored a bare substring of a
  step's id above an exact tag match, three to one. A word properly inside the
  id still counts for full marks, so "mail" still finds the mailbox steps and
  "sheet" still finds Google Sheets; an incidental run of letters now counts
  for less than a step that actually says the word. The SQL steps also learned
  that people write "db". This is the flow generator's and MCP's search — the
  editor's own step palette has always searched separately, and was unaffected.

- **Wires to a folded drop no longer go missing when a flow opens.** The editor
  asks for the flow and the drop catalogue at the same time, so a card can be
  drawn before the catalogue answers — holding stand-in pins for a moment
  instead of the ports its wires were drawn to. An open card corrects itself
  when the real ports land, because the card changes size and the canvas
  re-reads its pins; a folded card is the same size either way — icon, name,
  button — so it kept the stand-ins, and every connector line into or out of it
  stayed invisible until the drop was dragged or unfolded. Flows whose steps
  carry names of your own were hit hardest: a renamed card does not change width
  when its catalogue entry arrives, so nothing prompted a second look. The
  editor now re-reads the pins of every card that was drawn ahead of its entry.

## [0.41.8] - 2026-09-09

### Added

- **Watch a page can send headers.** The step fetched with no headers at all,
  which quietly ruled out the case it is best at: watching a shop's or a
  service's JSON API rather than the page wrapped around it. An API that wants
  an `Authorization` key, or just an `Accept`, was unreachable — leaving a
  ten-step scrape-and-diff as the only way to follow one number.

  It now takes the same **Headers** field as the Web request step, with the same
  `${secret.NAME}` resolution, so the key stays in the secret store. Watching a
  price is four steps: Interval → Watch a page (word-comparison off, a pattern
  picking the price out of the JSON) → If (is less than) → a notification. The
  step's `On change` pin leaves the whole chain dormant on a quiet check, and
  the first check — which has nothing to compare against — cannot reach the
  comparison at all.

- **Template: "Watch a price → tell me when it drops".** The four steps above,
  shipped in the gallery under Notifications. It reads the shop's product API
  rather than scraping the page around it, so a redesign or a moving review
  count cannot set it off, and it tells you only when the new price is *lower* —
  a rise is recorded silently. Fill in the product's JSON address and an ntfy
  topic. The README's gallery count was stale at thirteen; it is seventeen.

## [0.41.7] - 2026-09-09

### Added

- **A skipped step says why on the card.** A skip used to be invisible on the
  canvas: no chip, no dimming, a card identical to one the run had not reached
  yet — which is exactly the moment someone asks why nothing happened. Skipped
  cards now dim and carry a chip naming the reason: *Didn't fire* for a trigger
  the run did not enter through, *Not reached* for a step whose branch was
  skipped, failed, or routed elsewhere. A switched-off step keeps its own
  **Off** chip rather than gaining a second one saying the same thing.

  The run detail's timeline says the same thing: the row carries the reason
  beside the status, and opening it explains the skip in a sentence rather than
  the "no result recorded" it used to fall through to. That page has no **Off**
  chip of its own, so it explains a switched-off step too.

  The reason travels as a stable code (`core.SkipCode*`) on the run stream's
  node frame and on the step's record, with the copy per language shared by both
  views. It is a field of its own rather than a `JobError`, because everything
  that reads `Result.Error` — failure notifications, the run detail's red chip —
  would present a skip as a failure. A record written before the codes existed,
  or one carrying a code this build has no copy for, still gets labelled.

- **Fire one trigger from its own card.** A Slack- or GitHub-triggered flow
  could not be tested in the editor at all: the toolbar only offers "Send test
  event" for the Webhook/Form/Request family, and plain Run makes
  `slack_on_mention` execute standalone, which fails with `no_trigger_data` by
  design — the error told you to go post a real mention in Slack. Trigger cards
  now carry **Test fire**, which runs the flow as if that trigger had gone off,
  seeded with the payload the provider actually posts (an Events API envelope, a
  push, a newly opened pull request) and editable before firing.

  `POST /test-trigger` takes an optional `?node=<id>` naming the one trigger to
  seed. That is what a two-trigger flow needs: seeding the whole webhook family
  at once left `${trigger.*}` resolving to whichever trigger came first in graph
  order. Without the parameter the endpoint behaves exactly as before.

  The seeds are built by the same code the live delivery handlers use
  (`slackMentionSeed`, `githubPushSeed`, `githubPRSeed`, now shared via
  `daemon/triggerseed.go`), and a test asserts every port a manifest declares
  gets a value — a test fire that produced a different payload shape than
  production would be worse than no test. Payloads that a real delivery would
  not have produced are refused rather than fired: a Slack event of another
  type, a mention outside the step's channel filter, a pull request whose action
  is not `opened`. Schedule triggers get no button, since they derive their own
  fire moment and plain Run already exercises them.

### Fixed

- **A drop card can no longer grow wider than the canvas.** A card was sized by
  its content with nothing to stop it: a step named after a pasted URL, a long
  URL in a parameter, or an enum label written as a sentence each stretched the
  card to the width of one unbroken line — several screens across at working
  zoom, hiding whatever it overlapped. Cards now cap at 320px, which clears the
  widest card the catalogue produces on its own; past it a parameter value
  ellipsises, a long port label wraps, and a step name wraps to two lines and
  then ellipsises with the whole name on hover. A parameter's text box resizes
  vertically only, since dragging one sideways used to widen the card with it.

- **A trigger that did not fire no longer runs, or fails the flow.** Every root
  step with no incoming wire is dispatched, and only a *seeded* one is held
  back, so a flow with two triggers ran both: a Slack delivery also executed the
  Webhook step, which fails by design with `no_trigger_data` — and that failed
  the whole run even though the branch that did fire was fine. A scheduled flow
  carrying a Webhook step failed the same way on **every** fire, and in the
  other direction a webhook delivery executed the schedule step, stamping the
  steps after it with a fire moment nothing had scheduled.

  A trigger that did not fire is now skipped, the same path a switched-off step
  takes, so its branch goes dormant and the run still reaches a terminal state.
  The test is what each trigger needs in order to have fired, never which one
  finished first: an inbound trigger (webhook, form, request, provider event)
  carries data only when a delivery seeds it, and a seeded step is already
  terminal before anything is dispatched — so one that reaches a worker was not
  the way in, and is skipped as soon as anything else could have been. A
  schedule trigger derives its own moment, so it is skipped only on a run a
  delivery started. With neither present nothing is skipped, which is what keeps
  the `no_trigger_data` message reaching an author who pressed Run on a webhook
  or Slack flow.

- **Tidy no longer moves a locked step.** A locked card promises on its face
  that it "won't move", and dragging it was blocked — but Tidy repositioned
  every node on the canvas, locked ones included, which is the one action where
  an author had deliberately said otherwise. Locked steps now keep their
  position: they still hold the column their wiring gives them, so the rest of
  the flow still reads left to right through them, and any column whose band
  runs into a locked card stacks below it instead of on top of it. Align,
  distribute, and dragging a comment frame over a locked card leave it alone
  for the same reason (the frame carries everything else it encloses).

## [0.41.6] - 2026-09-09

### Fixed

- **Swedish search vocabulary now serves both searches instead of one.** The
  Swedish → English table lived in `web/src/lib/dropSearch.ts`, so it reached
  the editor's step palette and nothing else: a word added so a person could
  find a step by typing it did nothing for the same person asking the AI to
  build the flow, because the server-side search behind `search_drops` and the
  MCP `list_drops` tool had never heard of it. The table moved to Go
  (`internal/svsearch`), `make sv-aliases` generates the TypeScript from it, and
  `make catalogs-check` fails when the two drift. Swedish retrieval went from
  43% top-five to 93% on thirty asks; English is unchanged at 75% first-hit and
  98% top-five.

  The two sides keep different *policy* on purpose. The palette expands every
  token, because "fakt" should reach "faktura" while someone is still typing.
  The server expands only a token the catalogue cannot answer literally, which
  is what makes "adding Swedish never reorders an English result" true instead
  of merely intended — weighting alone did not, since English words
  prefix-match Swedish keys: "check" reached "checksumma" and put `hash` in the
  results for "check every five minutes", and "summarise" reached "summa" and
  put `group_aggregate` first for "summarise it". Two words of Swedish filler
  could also score thirty-one steps, because "för för" folds to "forfor", the
  ending stripper cuts "or", and the remaining "forf" prefix-matches
  "förfrågan". A guard now checks every alias value is a word some step
  actually carries, which found one that was not (`uppgift → task`), and
  `samling` — what the Swedish UI calls Collections — was missing entirely.

- **A Swedish request met an English catalogue in silence.** The step catalogue
  is authored in English by design — it is the contract the HTTP API, the MCP
  tools and the AI generator are all grounded on, and only the human UI
  localises. But nothing said so: the word "language" appeared eleven times in
  the generator's prompt, every one of them a `language(string)` param on an AI
  step. The prompt now states that the catalogue is English, to search it with
  English keywords whatever language the request is in, and to write what the
  user reads in their own. Worse than the silence, a Swedish query used to
  return confidently wrong steps: filler words matched inside English step ids,
  so "min" ("my") hit ge-min-i and "en" ("a") hit builtin_store_app-en-d and
  caldav_create_ev-en-t, and four unrelated Swedish asks came back with the same
  irrelevant block. A bare substring of an id now needs five characters, and
  Swedish filler is ignored outright, so those asks return nothing rather than
  nonsense. Measured on 30 Swedish asks: 43% top-five against 98% for English,
  and every Swedish ask naming a brand — Fortnox, Klarna, nShift, SMHI — reaches
  its step. Closing the rest means giving this side the Swedish alias table the
  editor's step palette already has (`web/src/lib/dropSearch.ts`).

- **The AI flow generator was never told how to read a value.** Its prompt
  taught `${item.…}` (the for-each form) seventeen times and never named the
  other two: the word "upstream" appeared once, to say a loop body has none,
  and `${trigger.body.…}` appeared once inside a single catalogue example's
  params. Those two are the only way to feed several scalar params from one
  structured output — the shape behind every "form → calendar", "form → SMS"
  and "email → sheet" flow — so the generator could only find them by accident,
  or by tripping the structured-into-text check, which is a warning and so
  never came back through the repair loop. The prompt now has a REFERENCES
  section, and says to prefer an edge when a whole port feeds a whole input.
  The MCP surface already documented both through its `flow_references` tool;
  this brings the in-app generator up to it.

- **Steps carried the wrong words to find them by.** A retrieval benchmark of
  83 plain-language asks (`daemon/flowgen_retrieval_test.go`) put the catalogue
  at 49% first-hit and 74% top-five. Searching now ignores the words an ask is
  made of rather than about ("the" is a substring of "weather", which dragged
  the forecast steps into eight unrelated asks), withholds the exact-name
  jackpot from a word that merely appears in a sentence (`text` won "text me",
  `email` won "when a new email arrives", `number` won "pull the invoice number
  out"), accumulates across every matching tag instead of stopping at the
  first, and stems a tag so "week" reaches "weekly". Alongside that, about
  twenty steps gained the words people actually use — British spellings
  (summarise, categorise), "spreadsheet" on the Sheets steps that lacked it
  though a sibling had it, feedback/survey/contact/page on the Form trigger,
  local/offline/own-machine on Ollama, duplicate on the upsert steps — and
  `stripe_get_customer` lost an `email` tag it should never have had, since it
  looks a customer up by Stripe id and was outranking the step that searches by
  email. 75% first-hit, 98% top-five, with both floors now enforced.

- **Searching the step catalogue missed the step you asked for.** Every word of
  a query had to appear, so any phrase collapsed — "web form" found only
  `http_upload` and "public form" found nothing, though `form_input` carries
  both words between its id and its tags. Words matched as substrings, so
  "form" hit "format" and "transform" and ranked `base64` and `build_csv` above
  the Form trigger. Any word may now match, a step accounting for more of the
  query ranks higher, and prose fields match whole words. Words under three
  characters are ignored: "to" and "my" match half the catalogue and say
  nothing about intent. The AI flow generator had a second, weaker copy of this
  search that also threw the ranking away by sorting its results
  alphabetically; it now shares the one implementation and keeps the order.

- **The four form templates handed out a form link that 404'd.** `form-to-sheet`,
  `form-to-sms`, `form-to-collection` and `approve-before-refund` declared their
  trigger as a Webhook step with a `public_form` param — the opt-in from before
  the Form step existed. Nothing has read that param since, and the hosted form
  is served only for `form_input`, so every one of them published a flow whose
  public link served no form. They now use the Form step. A guard checks the
  shipped templates, because a graph wired this way validates against the
  catalogue and so passed every composition test.

### Changed

- **The hosted form and the template gallery got a visual pass.** The public
  form page was a plain 480px column on its own palette; it is now a single
  elevated card that uses the app's own colours in both themes, with a real
  type hierarchy, focus rings, 16px inputs (anything smaller makes mobile
  Safari zoom on focus), a full-width button under 480px, and a confirmation
  aligned to the heading instead of centred under it. Gallery cards gained
  elevation, a larger app tile that top-aligns with the first line of the
  title, and a hairline footer that puts the apps on the left and the action on
  the right, so the buttons line up across the grid. Card titles were styled by
  a `.template-card-head h2` rule while the markup renders an `h3`, so they had
  been carrying browser defaults.

- **Opening a flow no longer replays the last run over it.** The editor
  remembered the last run you started per flow and re-attached to it on open,
  so a flow you came back to greeted you with green borders on every step and a
  finished run's outputs in the inspector — a canvas claiming steps had just
  run when nothing had. Run statuses now appear only when the run is actually
  yours to look at: following the `Edit` link from the runs list, a run's own
  page or an approval, or a run still in flight when you reload (found through
  the edit lock, so mid-run progress keeps arriving). Opening a flow to work on
  it shows the flow.

## [0.41.5] - 2026-09-08

### Changed

- **The "it worked" message moved into the toolbar and now fades by itself.**
  It was a panel pinned to the top-centre of the canvas that stayed until you
  dismissed it or pressed Run again — which is the one spot a flow always
  occupies, since a flow reads from its top-left, so announcing success covered
  the trigger and the first steps. The outcome now appears beside the button
  that started the run and fades after a few seconds, holding while the pointer
  or keyboard is on it so the link can't vanish mid-click. On a short bar the
  sentence yields and the tick plus "See the full run" carry it. The folded-away
  JSON of the last step's output is gone with the panel: the canvas already
  answers that better, in place, through the data face and the output pins'
  hover-peek.

### Fixed

- **The published banner no longer draws a stray line under its own text.**
  "Published — the live version matches your draft." sits in the editor's
  banner strip, which already closes itself off from the canvas with a 1px
  border; the row inside it painted a second one, a stack-padding above the
  first, so the message read as underlined.

## [0.41.4] - 2026-09-08

### Added

- **Steps fold down.** Minimize on a step's header collapses its card to the
  icon and the name, with one pin a side standing in for all of them; hovering
  a folded card reveals Maximize, and on a touch screen — where there is no
  hover to reveal it with — the button is simply always there. Both are also on
  the step's right-click menu. Folding is saved with the flow rather than in one
  browser, because a long flow is readable only if the boring middle is folded
  and that has to hold for whoever opens it next. Wires are untouched: every pin
  stays mounted under its own id and they merely stack at one point, so a folded
  card cannot lose a connection.

- **Steps can be locked.** The pin button in the inspector's header makes a
  step's fields read-only and stops its card being dragged — a guard against
  the slip of nudging a card mid-review or typing into the wrong field. A locked
  step carries a "Locked" chip on the canvas, so a field that won't take a
  keystroke reads as a state someone chose rather than as a bug. It runs exactly
  as before: this is an editor guard, not a permission, and the API still writes
  to a locked node. Its pins also stay wireable — locking guards the step's
  values, not the diagram.

### Fixed

- **Undo now covers a step's own flags.** Marking a step "can't fail the run"
  was not undoable: the editor's undo observer watched breakpoints and
  switched-off steps but not `continue_on_error`, so toggling it recorded no
  snapshot — and then the NEXT unrelated edit recorded a document that already
  carried the flag, so a single Ctrl+Z quietly reverted both. Applying a
  snapshot had the mirror of the same gap and never restored the flag. One
  helper now hydrates every per-node flag, on every path that replaces the
  document, so adding a flag can't half-wire itself again. The same hole meant
  a flow whose load 404'd left the previous flow's flag sets in place — and
  since node ids repeat between flows, the next save could write a stale flag
  onto an unrelated step.

- **Run logs are no longer deleted out from under a run that is still going.**
  Retention asked of each LINE "is this older than the window?", but a run
  parked on an approval, or waiting out a `delay`, writes its first lines on day
  one and finishes weeks later — so past the window the start of its log was
  deleted while it was still being written. The window is now measured from the
  RUN's finish, like the run history it accompanies: a finished run's log goes
  whole, and a run that has not finished is never touched. Same defect the run
  history had in 0.41.2, in the table beside it.

- **A workspace dzd could not read no longer loses its schedules.** The hourly
  reconcile lists every workspace's flows, then deletes projection rows for
  flows it did not see. A workspace whose listing FAILED contributed no flows,
  so that silence read as "every flow here was deleted" and the whole
  workspace's schedules went — its scheduled flows stopping until a later pass
  rebuilt them. The prune is now scoped to the workspaces the pass actually
  read; the ones that failed are left alone, and a genuinely deleted flow is
  still pruned as before.

- **A workspace whose directory has vanished no longer reports "no flows".**
  `ListGraphs` resolved no HEAD for a missing working tree exactly as it does
  for a repo with no commits, so an unmounted volume read as an empty
  workspace — and anything deleting on absence, the schedule reconcile
  included, acted on it. It now returns an error; an empty workspace still
  reports no flows, and the in-memory backend is unaffected.

### Changed

- **Money pins say which unit they mean.** Every `Amount (smallest unit)` pin
  and field across Stripe and Klarna now reads `Amount (cents/öre)`. "Smallest
  unit" is only meaningful if you already knew currencies have subunits and
  that the API wants them; someone who read it as kronor and typed `50` refunded
  50 öre. The worked example stays in the field help, and the friendlier
  `Amount (display)` pin beside it is unchanged.

- **Ticketmaster · Search events had three pins nobody could tell apart.**
  `Events found` was the list, `Count` was how many came back on this page, and
  `Total found` was how many exist in total. They are now `Events`,
  `On this page` and `Total matches`.

- **Database steps stop speaking SQL.** `Upsert rows` is now
  `Add or update rows`, and `Insert rows` (Postgres, MySQL, SQLite) and Google
  Sheets' `Append rows` are all `Add rows` — one name for one idea, in words
  that don't assume the reader has written a query.

- **Route rows and Switch stop naming their own plumbing.** The eight pins read
  `Routing slot 1…8` and `Case 1…8`; they are now `Route 1…8` and `Match 1…8`,
  and both steps' catch-all pin says `Everything else` instead of `Default`.
  Their fields followed — `Slot`, `Equals` and `Key field` are now `Path to send
  it down`, `Value to look for` and `What to match on`, and Route rows' three
  fields had no titles at all, so the editor labelled them `routes` and
  `default_slot`. Each `slot` field now spells out which pin it feeds ("rows_1"
  is the pin labelled Route 1), which the old `One of rows_1..rows_8` left you
  to work out. Both descriptions were written for someone reading the params
  rather than the canvas — Route rows' even shipped its own roadmap note
  ("fixed for V1; semantic naming via variadic ports is a future enhancement")
  — and are rewritten around first-match-wins, which is the actual trap.

- **Combine two lists is no longer a SQL join wearing a friendly name.** Its
  pins were `Left rows` and `Right rows`; they are now `First list` and
  `Second list`, and its dropdown, its field help and its description follow.
  Its three fields also had no titles at all, so the editor showed the raw
  parameter keys `on`, `kind` and `right_suffix` — they are now `Match on`,
  `What to keep` and `Suffix for repeated column names`. Port ids are unchanged,
  so existing flows keep their wiring.

### Developer

- Two failure-email throttle tests seeded a prior failure at `now - 10min`
  against an hourly TUMBLING window, so they failed for the first ten minutes
  of every hour — about one CI run in six, the 0.41.2 release run among them.
  The seeds are clamped into the current window.

- The README's editor screenshot is a published dark-theme flow instead of a
  draft with two unconnected accounts and a "not published" banner.

## [0.41.3] - 2026-09-07

### Developer

- The changelog is one line per change, down from 8,000 lines of prose.
- A test helper re-registered its module on a second `-count` pass, panicking the daemon suite.

## [0.41.2] - 2026-09-07

### Fixed

- **Run history no longer deletes the steps of a run that is still going.** The
  retention sweep asked of each ROW "is this terminal and older than the
  window?", which is true of the succeeded steps of a run that has not finished
  — one parked on an approval, or waiting out a `delay` (which accepts up to a
  year). Past the window those steps were deleted out from under the live run.

  What that cost was not only history. The completion check reads a missing
  step record as "not done yet", so the run could never finish and the
  orphan reaper could never close it: it sat in the Runs list as running for
  ever. A step whose predecessor had been pruned failed with a bare
  `predecessor "x": not found`. And a second bug lived in the same query —
  step records finish BEFORE the run record that owns them, so a cutoff
  landing between the two stripped the steps off an already-completed run and
  left the run listed with nothing in it.

  Retention is now scoped to the run: a run's history is deleted whole, N days
  after the RUN finished, and a run that has not finished is never touched. The
  window is therefore measured from the end of processing rather than the
  beginning. Because an unfinished run is now never swept, a run that can never
  finish is closed by the reaper instead (see the abandoned-run entry below).

- **A form submission refused by a plan limit is kept instead of thrown away.**
  Someone filling in a hosted form while the organisation was over its monthly
  run allowance, suspended, or running a flow that no longer validates got
  "this form is closed" — and what they typed became one line in the daemon
  log. No run, no marker, no email. The owner had no way to learn the form had
  stopped collecting, and the visitor was not coming back.

  The submission is now kept as a failed run carrying the data itself, so it
  shows in the Runs list with the reason, the run page shows what was
  submitted, and **Retry** processes it once the cause is fixed. The owner is
  emailed. A flow keeps up to 20 refused deliveries an hour; past that one run
  records how many more were refused and NOT kept, so a gap in the record is
  stated rather than left to be inferred.

  Inbound webhooks are handled the same way, and the status code now says which
  happened: a 4xx when the delivery is stored (do not resend — we hold it) and
  503 when it is not (please retry). It used to be a blanket 500, so a sender
  like Stripe burned its retry budget and then dropped the event for good.

- **A poll trigger that cannot read its own position stops instead of guessing.**
  Every "only new since last run" step keeps a watermark. Reading it returned
  the empty string on *failure* as well as on "nothing stored yet", and every
  step reads the latter as a first run — so one transient hiccup in the secret
  store made a poller re-baseline to whatever was in front of it, silently
  marking as handled everything that had arrived since the last poll, emitting
  nothing, and reporting success. The mail, feed items or files in that window
  were skipped permanently.

  Such a step now fails, which loses nothing: the stored position is untouched,
  so the next poll resumes from it. Two related holes closed with it. On a
  deployment with no `DAZYFLOW_MASTER_KEY` there is nowhere to keep a position
  at all, and those steps concluded "first run" on every run, for ever, emitting
  nothing while the runs looked green — they now say so plainly. And a failure
  to write the FIRST position is no longer ignored: nothing was emitted, so the
  next run baselines too, and a write that kept failing parked the flow on its
  first run permanently.

- **Gmail stops losing an email whose fetch failed.** A search returns id stubs
  that are expanded one call each; an expansion that failed left that entry
  undateable, so it could not be emitted — and the watermark still advanced
  past it as soon as any newer email in the same page expanded cleanly. One
  unlucky API call, one email silently never processed, on a green run.

  Any unread email now holds the watermark where it is, so it comes back around
  next poll; the emails that did read are emitted again with it, which is the
  same duplicates-over-silent-drops trade this step already documents for a
  failed cursor write. A page where NOTHING could be fetched fails outright,
  because an outage and a quiet poll are indistinguishable downstream.

- **A burst bigger than "Max emails" is delayed rather than truncated.** Gmail
  and Mailbox searches return the NEWEST matches up to the cap. With "only new"
  on, a poll therefore emitted the newest 50 of 200 new emails and advanced the
  watermark to the newest of them, putting the other 150 permanently behind it.

  The poll now asks the mail server for the backlog rather than for the newest
  mail, and works through it from the OLDEST end, in arrival order, so the
  remainder stays in front of the watermark for the next poll. A backlog too
  deep to scan in one go refuses rather than draining part of it, leaving the
  whole thing waiting.

- **A run that fails while nobody is looking now reports itself.** Failure
  notification was a goroutine armed when the run was submitted, subscribed to
  that run, bounded to an hour. Every shape of that lost the notifications most
  worth having: a restart or deploy killed every watcher, so anything in flight
  at that moment failed silently for ever; a run recovered from an expired lease
  by another replica had no watcher on the replica that finished it; runs the
  orphan reaper closes had none either; and the one-hour ceiling meant every
  approval, every long delay and every retry backoff outlived its own watcher.

  Notification is now a sweep over the runs the store says are owed one, which
  no process can forget, and a startup pass sends anything the previous process
  was mid-way through. A send that fails is retried a bounded number of times
  instead of being dropped — a mail host having a bad minute used to mean the
  notification was simply gone. `DAZYFLOW_NOTIFY_INTERVAL` and
  `DAZYFLOW_NOTIFY_LOOKBACK` tune it; the lookback is what stops the first
  sweep after a weekend of downtime being a mailstorm.

  Three consequences fall out. A run the platform stops on its wall-clock
  timeout is reported: it ends as *cancelled*, which the old watcher ignored
  entirely, so the one failure the platform itself causes was the one nobody was
  told about — and the Runs list only said "cancelled", which reads as though
  somebody meant it. A submission whose work could not be queued is reported,
  where before it was failed before its notifier was even armed. And a run
  started through the API or `dzctl` is reported (see Changed).

- **A flow whose schedule has quietly died leaves a record.** Every failure
  path in the scheduler's fire was one line in the daemon log and a return: a
  workspace that would not open, a published-revision lookup that failed, a
  published flow that would not decode, a submission refused for a reason the
  owner has to act on. A flow whose published revision no longer decodes was
  dead on every tick, for ever, and from inside the app looked exactly like a
  flow that was running fine.

  These now write a failed run naming the cause, which puts them in the Runs
  list and hands them to the notification sweep. Deliberate states still leave
  nothing behind — unpublishing a flow and pausing it are how you turn a
  schedule off. Fires the scheduler owed and did not make (a stalled tick loop,
  a takeover from a dead leader) are recorded with the count.

- **Running out of the monthly run allowance sends an email.** The signals were
  an in-app Usage banner and a coalesced marker in the Runs list, both of which
  need somebody to be looking at the app — which is what the users of an
  automation product are precisely not doing. Flows stopped, everything looked
  calm, and the way people found out was from a customer. One email per
  organisation per day now says the flows have stopped, that nothing was
  deleted, and that they start again on their own at the monthly reset.

- **A run that can never finish is closed instead of sitting there for ever.**
  Now that an unfinished run is never pruned, one that cannot progress would be
  immortal: in the Runs list as running for ever, holding one of the
  organisation's simultaneous-run slots for ever, and notifying nobody, because
  it never reaches a terminal state. After enough of them a capped organisation
  admits every new run as pending and starts none.

  The reaper now fails such a run with a reason. The test is "nothing is
  pending", not "old": a run with any queued, running or awaiting step is
  waiting for something real — an approval parked for three weeks, a delay
  counting down 90 days — and is never touched however ancient it looks.
  `DAZYFLOW_ABANDON_RUNS_AFTER` tunes the window, which exists only so a run
  mid-transition is not mistaken for an abandoned one.

- **A repeating failure stops going quiet after the first email.** The throttle
  asked "has any other run of this flow failed in the last hour?", which a
  broken flow always answers yes to — so an outage sent one email at its start
  and then nothing, however long it lasted. A flow broken for a week produced a
  single email, sent a week ago, with the Runs list as the documented fallback.
  The window now tumbles, so a continuing outage is reported once per window,
  and each message says how many runs failed in the window before it.

- **Smaller ones.** A run refused before anything was written no longer costs a
  run out of the monthly allowance. A form or webhook submission is no longer
  abandoned half-written when the sender hangs up mid-request, which could
  leave a run stuck running with no work in it. A Mailbox search whose server
  returns fewer messages than it matched no longer advances its position past
  the ones that did not arrive.

### Changed

- **A flow started from the app, the API or `dzctl` now emails on failure.** The
  email was suppressed for any run marked as started by a person, on the
  reasoning that they were watching the canvas turn red. But the same endpoints
  serve `dzctl`, the MCP server and anybody's own cron, and nothing distinguishes
  those callers once a submission arrives — so the flag meant "tell nobody" for
  exactly the unattended runs that need telling. Suppression is gone; the
  per-flow throttle covers the case it was for, so iterating on a broken flow
  costs one email per window, not one per attempt.

- **For each fails when most of the items fail.** Carrying on past a bad row is
  the point of the step and has not changed, but the step reported SUCCESS for
  any mix short of total failure — so 99 failures out of 100 was a green run,
  no notification, and a following step recording the work as done. Past half,
  the step now fails. The rows that succeeded are still on `results` and the
  failures still on `errors`, so nothing is thrown away; what changes is that
  somebody is told. The failure count now goes to the run log either way.

- **The first Google Forms check after publishing no longer processes the whole
  back catalogue.** Every other watcher records where it is up to and fires
  nothing on its first check; this one treated every existing response as new,
  so publishing a flow against a form with 500 responses put all 500 through
  the step that acts on each one. It now baselines like its siblings. This also
  removed a way to lose the lot: emitting a backlog and then failing to record
  it meant re-emitting the same backlog on every check, for ever.

- **"Max emails" on the two mail searches takes the oldest waiting mail when
  "only new" is on.** An ad-hoc search still returns the newest matches, which
  is what someone asking a big mailbox for 50 results wants. A poll needs the
  other end — see the burst entry above.

## [0.41.1] - 2026-09-07

### Changed

- The publish confirmation was rewritten.
- The "it worked" panel stops taking a bite out of the canvas, and stops opening with a wall of JSON.

## [0.41.0] - 2026-09-07

### Removed

- A restart no longer publishes your drafts.
- A step's settings have one shape each.
- The row-shaping steps no longer look for a `headers` input.
- The runner queue's pre-tags columns are gone.
- Excel's `autosize` and `freezeRow` params are gone.

### Changed

- The editor's messages moved out of the canvas and into the toolbar.

### Fixed

- Two messages stop pointing at a menu that isn't there.
- A wire from a step's Headers output sticks.

## [0.40.0] - 2026-09-07

### Fixed

- The approval link in the email now opens a page you can decide on.

### Changed

- BREAKING: an API key can no longer approve an approval step unless the step allows it.
- A webhook key can now travel in the address, and is no longer mandatory.
- The Request step got the same two doors.

## [0.39.0] - 2026-09-07

### Added

- Spotify: connect an account and read the artists it follows, paged with a cursor.
- Ticketmaster: search live events, plus a When an event is announced trigger.

## [0.38.0] - 2026-09-06

### Changed

- BREAKING: the hosted form is its own step.

### Added

- A flow can answer its caller.

## [0.37.3] - 2026-09-06

### Performance

- Starting a run queues all its ready steps in one write.
- Saving a flow spent a fifth of its time backtracking a regex.
- Editing a step no longer re-renders every wire on the canvas.
- Dragging a step no longer re-renders every card on the canvas.
- Reading one manifest no longer materializes the whole catalog.
- Approving a parked run resumes it immediately.
- A run now starts when it is submitted, not on the next poll.
- `GET /api/v1/me` builds its response as a struct, not a map.
- Starting any dazyflow binary no longer waits on a bcrypt derivation.
- Reading a workspace's flows no longer holds the workspace mutex through JSON decoding.

## [0.37.2] - 2026-09-06

### Performance

- Listing a workspace's flows no longer decodes every step's configuration.
- The schedules list loaded every flow one at a time.
- The sidebar asked before the answer could be right.
- The sidebar's approvals badge fetched the whole inbox to render a number.
- The run viewer's timeline stopped re-parsing the flow, and over-reading columns, on every poll.
- An English reader no longer downloads the Swedish translation of the whole product.
- The run list stopped fetching every run's whole flow to render seven columns.
- The flow list reads the workspace in one pass instead of one flow at a time.
- Catalog endpoints no longer recompress, and a browser that already holds the catalog transfers nothing.
- Built assets are cached for a year instead of being revalidated on every page load.
- Authenticating a request went from ~485µs to ~2.6µs, and the request path now compresses.
- The API compresses its responses.
- The drop catalog was serialized twice into one response.
- Another 11% through the same database, and a step now costs 4.0 statements instead of 5.1.
- A step reads its predecessors in one query.
- Submitting a run no longer reads a record to learn what it just wrote.
- The drop catalog is derived once, not per request.
- The web app loads a third of what it used to.

## [0.37.1] - 2026-09-05

### Performance

- A third more work through the same database: the execution path saturated at ~285 steps/sec, now ~390.
- The completion check no longer runs on every step.
- A step's completion and the successors it releases are one write.
- The queue is fair between orgs.
- One run cache per process rather than per worker (`WorkerConfig.Runs`): one read per replica, not per worker.

### Added

- A load rig for the execution path (`make stress`, `tests/stress/`).

## [0.37.0] - 2026-09-04

### Performance

- A replica no longer reads the whole fleet's run events.

## [0.36.0] - 2026-09-04

### Changed

- An apex deep link now forwards to the org's own subdomain.
- Multi-replica deployments no longer need sticky sessions.

### Fixed

- The Google sign-in binding cookie was never cleared on an org subdomain, and was widened on every deployment.
- Connecting an app over OAuth failed on an org subdomain.
- Tidy was reported missing a third time — so it is back in the toolbar, with its name on it.
- Erasing an org left its flows on disk in the synthesized git mirror.

## [0.35.0] - 2026-09-04

### Added

- Flows can live in Postgres instead of git, which is what makes flow authoring safe on more than one `dzd`.
- Internally, `workspace.Store` is now a façade over a backend interface.

### Changed

- A stock `dzd` now runs eight steps at once instead of two.
- `DAZYFLOW_PG_MAX_CONNS` follows the worker count when unset, at `max(20, workers + 12)`.
- `DAZYFLOW_MAX_CONCURRENT_JOBS` is reachable.
- The scheduler no longer reads every flow in the install to find out what to fire.

### Performance

- Memory no longer grows with the number of tenants.
- A workspace's lock is now held per directory rather than per `Store`.
- Five sweeps that ran on a timer were scanning whole tables to do it.

### Fixed

- Tidy vanished on flows with fewer than two steps.
- A flow could hold a connection that made every save fail, with nothing on screen to fix.

## [0.34.0] - 2026-09-03

### Added

- A run's result is a table when the run produced a table.
- Files a run wrote are listed on the run page, with a download.
- Collections can be published as a login-free read-only page.

### Fixed

- A `workspace://` path wrote the file into a directory called `workspace:`.

### Changed

- Collections pages past the first thousand rows.

## [0.33.1] - 2026-09-03

### Changed

- Show all data sits in the data panel's top-right corner.

### Fixed

- A wire dragged onto a Text step made the flow unsaveable, with nothing on the canvas to undo.
- A refused save retried itself every 1.5 seconds, forever.

## [0.33.0] - 2026-09-03

### Fixed

- Five webhook tests raced the clock and went red on CI.

## [0.32.1] - 2026-09-03

### Added

- A step's data opens in a dialog for reading.

### Changed

- A card now expands instead of folding over.
- A card names the fields a step emits rather than sampling its rows.

### Fixed

- A step's data table did not draw on a wide card.
- Opening a card's data no longer resizes the card.

## [0.32.0] - 2026-09-03

### Added

- A step can say what it produces before it has ever run.
- A step's last output survives a reload.
- A step card now folds open to show what the step produces.

### Fixed

- Swedish went missing across the step catalogue, and nothing said so.
- A connection field's placeholder was never translated.

### Changed

- `make drop-catalog` records every string a drop puts on a screen, not just its description and pin labels.

## [0.31.2] - 2026-09-03

### Changed

- `golang.org/x/crypto` bumped to v0.56.0 for GO-2026-6355 (DoS on a deadlocked SSH channel).

## [0.31.1] - 2026-09-02

### Security

- An Approval step could aim the deployment's own mail server at hundreds of thousands of strangers.
- The question an Approval step asks, and the error a failed run reports, had no length limit.
- A long flow name stopped its own emails from being sent at all.
- A flow's comment boxes and its trigger list weren't weighed, so a one-step flow could be a gigabyte.
- A hosted form's field names had no length limit, so one link answered an unauthenticated request with 60 MB.
- A step supplied by a runner could raise its own connection limit.
- A flow could still trigger itself forever, through the Webhook step or its own failure webhook.
- A flow could trigger itself forever through its own hosted form.
- A Wait step held one of the daemon's execution slots for its whole duration.
- The graph size ceiling did not weigh identifiers.
- The trigger cap counted the trigger list, not the trigger steps.
- A hosted form rendered as many fields as the flow declared.
- One saved flow could fire itself two thousand times a minute.
- The fan-in rule was skipped for exactly the steps it could not see.
- Nothing weighed a graph: a graph's payload is capped at 16 MiB, and routing knots are capped too.
- A flow ID was a path and a git ref name, validated nowhere.
- The value ceiling was blind to a list of step results, so the doubling bomb still worked.
- A subgraph hop reset the trigger-chain counter.

### Fixed

- A breakpoint left behind runs that never ended.
- Wiring rules that nothing enforced.
- Editor metadata was uncapped.
- One flow could take the whole daemon down.
- A flow could trigger itself forever.
- Connections the editor called invalid were still saved and run.
- A large flow cost minutes of CPU per run.
- "Wait 292 years" completed instantly.
- Tidy could not be reached when you needed it.
- The editor toolbar hid its overflow.

## [0.31.0] - 2026-09-02

### Added

- AI steps can read a file, not just text about one.
- The invoice flows now read the invoice.
- Two templates for apps that had none.
- Calendars can be amended and cancelled, not only created.
- All-day events on `caldav_create_event`.
- YAML, and a way out for JSON and XML.
- Work on PDF files themselves: the new PDF app.

### Changed

- `golang.org/x/image` bumped to v0.45.0 for GO-2026-6222, a `vp8l.Decode` vulnerability.
- Icons added for the PDF, YAML and calendar steps, which fell back to their category's glyph.

## [0.30.0] - 2026-09-02

### Added

- Move files over SFTP: the new SFTP app.
- Calendars that aren't Google's: the new Calendar app.

### Changed

- A guard on the write-dedupe flags (`drops/writepolicy_test.go`).
- `folder-open` added to the web icon registry, so List files draws its own glyph.

### Fixed

- The Mailbox app is called Mailbox in Swedish too.

## [0.29.0] - 2026-09-02

### Added

- Read email over IMAP: the new Mailbox app.
- Read one email over IMAP, and take its files.
- `imap_mark_seen` closes off a handled email.
- A second AI-triage template, for a mailbox that isn't Gmail (`ai-email-triage-imap`), plus use case 36.

### Changed

- `mail-check` added to the web icon registry, so Mark as read draws its own glyph.
- Attachment-saving rules moved to `drops/internal/mailfiles`, shared by the Gmail and IMAP steps.
- Shared helpers replace per-connector copies.

### Removed

- Dead code kept alive only by its own tests (`HasConfiguredSchedulerTrigger`, `webapi.ApplyRefresh`, `githubDo`).

## [0.28.8] - 2026-09-01

### Added

- Recent decisions on the Approvals page.

### Fixed

- A mail-server login that was never presented reported "OK".

## [0.28.7] - 2026-09-01

### Fixed

- A passing e2e assertion reported itself as failed, and it read like the MCP integration was broken.

## [0.28.6] - 2026-09-01

### Fixed

- Stripe's app page said "Connect Stripe" twice.
- The flow editor stopped warning about unconnected apps when the secret store is off.
- A Postgres connection failure at boot named the wrong subsystem and none of the fix.
- `npm ci` could not install the web dependencies at all, so CI was red and the Docker build failed.
- `engine/jobstore`'s Postgres conformance suite failed at random.
- A flaky editor autosave test failed only under parallel workers.
- `docker compose up -d` could not work on a fresh clone.
- A failed sign-in blamed the password when the real cause was `DAZYFLOW_WEB_ORIGIN`.

### Changed

- The web UI is published on `localhost:8642` instead of `localhost:8080`.
- `make check` and `make ci` now say when they skipped a gated test suite, and `make test-db` wires one up.
- The bundled Postgres publishes on `127.0.0.1:5442` instead of `5432`, and the port is configurable.
- README: "Running it for real" listed two required values; it is three — the DSN needs `sslmode=require`.

## [0.28.5] - 2026-09-01

### Fixed

- Re-running a webhook- or form-triggered run started it with no data.

## [0.28.4] - 2026-09-01

### Fixed

- `daemon/mailer.go` was not gofmt-clean, so `fmt-check` failed CI on 0.28.3.

## [0.28.3] - 2026-09-01

### Fixed

- The Email (SMTP) step's mail arrived twice.
- An address in both To and CC was mailed twice.

## [0.28.2] - 2026-09-01

### Changed

- A pushed version tag is now the release, and the only thing that deploys.

### Fixed

- `tests/e2e/ap-invoice/run.sh` waited on a fixed `sleep 0.5` after firing each webhook.

## [0.28.1] - 2026-09-01

### Changed

- `examples/` is now `tests/e2e/`.
- `SCENARIOS.md` and `tests/scenarios.md` moved next to the graphs that back them.
- `.nvmrc` moved to `web/.nvmrc`.
- README rewritten to lead with trying it, not operating it.
- CI image builds are cached and no longer a serial tail.
- The Go job is sharded and the end-to-end scripts have their own job.
- bcrypt no longer runs at production cost under `go test`.
- `.env.example` documents fifteen knobs it had never mentioned, including `DAZYFLOW_TRUSTED_PROXIES`.
- Guide cross-links now resolve on the repository host as well as in the docs SPA.
- `make check` matches what CI enforces, and says what it does not cover.
- The web test files are type-checked.
- A failed deploy trigger now says what to check.

### Fixed

- A race in `TestRunStatus_ParksAndResumes`.
- Outbound pacing leaked between tests in `drops/net` and `drops/geo`.
- The five `TestAllDrops_*` contract sprays run their per-drop subtests in parallel.
- Nine dangling links into `docs/decisions/`, a directory that was never created.
- Nothing checked Markdown links outside `docs/guide/`.
- `docs/DEPLOY.md` told operators `make docs-dev` runs VitePress.
- `cmd/docsgen` escaped `{{` as `&#123;&#123;` for a renderer that is gone.
- Two of the three shipped examples could not run, and CI only ran the third.
- `go build ./...` walked into `web/node_modules`.
- A leaked socket per HTTP error in the runner agent.
- Nine Go files were not gofmt-clean.

### Removed

- Seven Markdown files, ~2,600 lines.

### Added

- Issue templates for bug reports and connector requests; `config.yml` routes vulnerabilities privately.
- An index for `docs/guide/`, carrying the intended reading order a file list loses.
- Tests for `cmd/docsgen`, which renders the public step catalog from the drop manifests.
- CI runs on push and pull request.
- Four new gates: `make fmt-check`, `make env-check`, `npm audit`, and a guide cross-link check.
- ESLint, limited to the React hooks rules.
- `CONTRIBUTING.md` and a pull-request template.

## [0.28.0] - 2026-08-31

### Security

- There is now a documented way to report a vulnerability, and it no longer dead-ends.

### Added

- Dependabot keeps Go modules, npm packages, Actions and Docker base images current.

### Changed

- CI moved to GitHub Actions, and release images now come from GitHub Container Registry.
- Production overlay: `REGISTRY_HOST`, `REGISTRY_USER` and `REGISTRY_PASSWORD_HASH` are gone; set `GHCR_OWNER`.

## [0.27.10] - 2026-08-31

### Fixed

- On a single-host deploy, every action failed the moment you signed in.
- "See a flow run" reported "Not signed in." to people who were signed in.
- Connect on an app the server has no credentials for threw you out of Dazyflow.
- `/signin` and `/signup` answered "page not found" to anyone with a session.
- Collections showed timestamps as raw UTC instants.
- The sign-in and sign-up screens sent their logo to `dazyflow.app`.
- A template needing two apps said "which isn't set up".

### Changed

- The Apps page leads with the apps people recognise.
- An app's steps read as descriptions, not identifiers.
- "Built-in" describes itself in words.
- A hosted deployment no longer tells you to go and find the server administrator.
- The hosted-form templates label their trigger "Form".
- The verify-email banner says what you lose by ignoring it.
- The signed-out screens say "Dazyflow".

## [0.27.9] - 2026-08-31

### Fixed

- A Swedish reader met one English paragraph in the middle of a Swedish card.
- The docs guards in the frontend suite had been searching an empty directory in CI.

## [0.27.8] - 2026-08-30

### Added

- Collections stamp every row with when it was saved.

## [0.27.7] - 2026-08-30

### Added

- A guide page for teams, roles and approvals.
- Approval cards show what is being approved.

### Fixed

- A hosted form filled its collection alphabetically.
- A 403's real message was replaced with advice to ask an admin.
- The verification banner claimed an email had been sent, and its resend failed silently.
- Inviting anyone was impossible when the deployment's mail was broken.
- Every organization got one seat more than its plan allowed.
- Outstanding invitations didn't hold a seat.
- Two people accepting at once could both take the last seat.
- The upgrade pitch contradicted the plan table beside it.

## [0.27.6] - 2026-08-30

### Fixed

- `${upstream.…}` and `${trigger.…}` only worked one hop from their source.
- The approval email carried the template instead of the question.

## [0.27.5] - 2026-08-30

### Fixed

- An agent can now see whether the customer read their reply.
- A support thread could go quiet with nobody told.
- System notes in a support thread were English in a Swedish UI.
- The support chat read your own email address back at you.

## [0.27.4] - 2026-08-30

### Fixed

- Tidy parked loose cards in the first column.
- Closing a support ticket asks first.
- "Contact support" on the editor's failure banner left the product.
- Editor banners covered the flow instead of sitting beside it.
- Half the pins on a card read English to a Swedish reader.
- A Swedish warning ended in an English word.
- `${trigger.…}` was offered everywhere and resolved nowhere.

## [0.27.3] - 2026-08-30

### Fixed

- Both sides of a branch could run.

## [0.27.2] - 2026-08-30

### Fixed

- Step icons could render unstyled, and the Inspector's did.

## [0.27.1] - 2026-08-30

### Added

- A guard that the UI actually uses `enumNames`.
- A guard against `.test()` on a global regex.
- One `DropIcon` component draws every step icon.

### Fixed

- A number and its unit now have a space between them.
- Self-coloured icons were invisible in the step search.
- Node cards showed raw `${…}` syntax instead of reference chips.
- `&lt;` and `&gt;` were showing up on screen.
- Read JSON refused to hand you a single field.
- A dropdown's value on a node card read as its identifier, not its label.

### Changed

- Retention no longer deletes approvals.

## [0.27.0] - 2026-08-29

### Added

- An Email step: a typed source field that emits an address only after checking it parses.
- A guard on the passthrough pin.

### Changed

- The If step now says the same thing in all three places.
- Site check and Watch a web page say what the pass-through pin does.

### Fixed

- "Send test event" was unreachable on every webhook flow built in the last year.
- The editor's live flow-watch reconnects.
- Phone's Swedish field help came back.

## [0.26.5] - 2026-08-29

### Added

- A running script now says what it is doing.

## [0.26.4] - 2026-08-29

### Added

- A published release asks to be deployed.

## [0.26.3] - 2026-08-29

### Changed

- `make upgrade` no longer buries its own output.

### Fixed

- Release images now carry the commit they were built from.

## [0.26.2] - 2026-08-29

### Added

- `scripts/deploy.sh` — the runner-driven deploy entry point, now version-controlled.

## [0.26.1] - 2026-08-29

### Fixed

- The registry password hash has to be escaped in `.env`, and nothing said so.

## [0.26.0] - 2026-08-29

### Changed

- Production deploys pull prebuilt images instead of compiling on the box.

### Fixed

- `make upgrade` could not report a failure.

## [0.25.0] - 2026-08-29

### Fixed

- The overview's four stat tiles now open the runs they counted.

## [0.24.1] - 2026-08-29

### Changed

- The AI steps ask the vendor which models you can use, instead of offering a list compiled into the release.

## [0.24.0] - 2026-08-28

### Added

- Gemini joins Claude, ChatGPT and Ollama as an AI provider.

### Fixed

- Signing in no longer lands on "page not found".
- Belonging to a second organisation no longer reports a permission error on every page.

## [0.23.0] - 2026-08-28

### Added

- Ollama joins Claude and ChatGPT as an AI provider.

### Changed

- The chrome around AI stopped naming two vendors.

## [0.22.2] - 2026-08-28

### Changed

- The welcome screen was reordered around what the reader actually needs.

### Fixed

- The sign-in page no longer greets everyone with a notice about a missing page.

## [0.22.1] - 2026-08-28

### Added

- A render error shows a page instead of blanking one.
- Every signed-out screen says what it is.
- The docs site has chrome of its own.

### Changed

- Copyright is held by Angels' Ware.
- The docs render light instead of pinned dark.

### Fixed

- A dead link inside the app says so.
- A bad link while signed out no longer looks like a session expiry.
- The docs' brand mark no longer leads to "Page not found".

## [0.22.0] - 2026-08-28

### Added

- Steps can be imported from an OpenAPI spec.
- Re-importing a spec says what it would take away, and asks first.
- A service with no public address can be called through a runner.
- The docs site reads like a document.

### Fixed

- A generated catalog page no longer opens with its generator's comment.
- The overlay port is named rather than described.
- The authentication picker says which header it means.

## [0.21.0] - 2026-08-27

### Added

- A web API can describe itself, and the description reaches the Apps page.
- The template gallery reads in the reader's language.

### Fixed

- An uncurated app is named the way it was typed.

### Security

- A registration token can no longer take over an existing runner.

## [0.20.0] - 2026-08-27

### Added

- Web API steps wear your service's own logo.

## [0.19.1] - 2026-08-27

### Fixed

- Web API steps are captioned by their names, not their ids.
- A web API's service address is no longer settable from its Apps connection.
- Deleting a web API now says what will actually break, like MCP servers do.
- MCP steps are no longer labelled "Built-in".

## [0.19.0] - 2026-08-27

### Added

- Web APIs — describe your own service and get steps out of it.

### Changed

- Tenant step-source rules moved to `daemon/stepsources.go`, shared by MCP servers and web APIs.

### Fixed

- Deleting an MCP server now says what will actually break.
- A disconnected MCP server no longer makes flows look broken.

## [0.18.1] - 2026-08-27

### Added

- MCP steps use the tool's own display title.
- A server's handshake note is shown to the admin who added it.
- MCP tools bring their own icons.

## [0.18.0] - 2026-08-27

### Added

- A 46elks template: "Web form → text me".
- Every icon a step declares now actually renders.
- A guard keeps the two sides in step.
- Shipped templates now have their CEL expressions compiled by CI.
- The guide now teaches the product, not just its vocabulary.

### Fixed

- A brand-new account's Runs page no longer blames a filter nobody set.
- The pre-run gate's button now names where it goes, and goes somewhere useful.
- The hosted form now speaks the flow's language.
- A failed form submission no longer throws away what the visitor typed.
- In-page documentation links now actually jump.

### Changed

- MCP servers are named, not slugged.
- An MCP server can be renamed.

## [0.17.0] - 2026-08-27

### Added

- Each org can now add its own MCP servers, in Admin → MCP servers.
- An MCP tool's arguments are now ports, so they can be wired.

## [0.16.7] - 2026-08-27

### Fixed

- A flow's language, and its failure notification, actually save now.

## [0.16.6] - 2026-08-27

### Fixed

- A flow's language, and its failure notification, survive the next save.

## [0.16.5] - 2026-08-27

### Fixed

- A dropdown whose own "empty" choice wouldn't stick.

## [0.16.4] - 2026-08-27

### Fixed

- 0.16.3 didn't build: a duplicate `"Language"` entry in the Swedish vocabulary failed `tsc`.

## [0.16.3] - 2026-08-27

### Added

- The platform's own emails speak Swedish.
- Flows write dates in your language.

### Changed

- An email's plain-text half is generated from the same content as its HTML.

### Fixed

- A step card no longer shows `${…}` reference syntax.

## [0.16.2] - 2026-08-26

### Added

- Weekday as a format: a date comes out as "Thursday", or "Thu" for the short form.

### Removed

- "Move to weekday" is gone from the Date & time step.

## [0.16.1] - 2026-08-26

### Added

- "Move to weekday" on the Date & time step.
- A searchable timezone picker.

### Changed

- The two time formats are a matched pair.

### Fixed

- A dropdown no longer misreports a value it doesn't list.

## [0.16.0] - 2026-08-26

### Changed

- The Date & time step's format field is one you can actually type.

### Added

- "At time of day" on the Date & time step.

### Fixed

- The inspect button no longer hides on small screens.
- "Publish changes" is clickable again, and publishing is reachable on a phone.

## [0.15.9] - 2026-08-26

### Fixed

- The Email app's From address takes a name, not just an address.

## [0.15.8] - 2026-08-26

### Changed

- Dropdowns read as words, in your own language.

### Added

- The Regex step can rewrite several different words in one pass.

### Fixed

- A run's ports are named the way the canvas names them.
- A run now shows what each step received, not just what it produced.
- A renamed step is called that everywhere, not just on its card.
- Renaming a step now sticks.
- A column's custom name is kept when you click away from the box.

## [0.15.7] - 2026-08-26

### Fixed

- "Run it once and I'll know your columns" now happens.

### Changed

- A table's columns and their headings are one list again.

## [0.15.6] - 2026-08-26

### Fixed

- Renaming a table column was not reachable from the editor.

## [0.15.5] - 2026-08-26

### Added

- You can download your own data.
- Collections sorts by column, and the download follows.
- A table can be named; the name renders as the table's `<caption>`.
- Sort rows has a direction toggle.
- A comment can be coloured, instead of every note on every canvas being violet.

### Fixed

- Collections shouted your column names back at you.
- Renaming a column in Make a table emptied it.
- The editor asked you to publish changes it then reported as no changes.

## [0.15.4] - 2026-08-26

### Added

- The editor warns when a script's language contradicts what will run it.

## [0.15.3] - 2026-08-26

### Fixed

- The JSON node's editor on a card had its caret offset from the text.

### Changed

- A Text step that holds code says so on the canvas.

## [0.15.2] - 2026-08-26

### Added

- The Text step can hold code.

### Changed

- A released changelog section can no longer be edited by accident.
- Removing an environment variable asks first.

## [0.15.1] - 2026-08-26

### Fixed

- Adding an environment variable was close to impossible.
- The caret in the script box was still off after 0.15.0.

## [0.15.0] - 2026-08-25

### Fixed

- The caret sat off the text in the runner step's script box.

### Added

- A connection can say what happens when the step before it fails.
- A step can be marked as one whose failure doesn't fail the run.
- A script's exit code can be a flow signal instead of a failure.
- Run on your machine can pass environment variables, a credential among them, to the script.

### Changed

- A run you start yourself no longer emails you when it fails.
- A flow that keeps failing now emails once an hour, not once per run.

## [0.14.0] - 2026-08-25

### Changed

- Online machines lead when several match.
- Run on your machine says where with tags, and only tags.
- Tags are assigned on a machine's own settings page.
- `--tags` is the flag; `--labels` still works.

## [0.13.0] - 2026-08-25

### Added

- Labels can be assigned from Admin → Runners.

### Fixed

- Wide tables could not be scrolled on a phone.

## [0.12.0] - 2026-08-25

### Added

- The Run on your machine step now asks which machine, and what should run the script.

## [0.11.0] - 2026-08-25

### Added

- Flows can run scripts on machines you own.

### Changed

- A remote module declares every step it serves, and belongs to one organization.
- The Approvals inbox opens the run.
- Icon sizes, spacing and stacking order come from scales.
- The Apps page no longer ends each step with a JSON dump of its params schema.
- Two English strings had two Swedish translations each.

### Fixed

- Fourteen dialogs now close on Escape, and every dialog announces itself as one.
- A confirm dialog with more than a sentence in it now renders the markup it was given.
- Loading placeholders are announced to screen readers.

### Developer

- The web app is arranged by feature, and the flow editor has been broken up.
- Five new guards on the design system, each one written after the drift it now prevents.
- Duplicated interface text is on shared keys, and the locale bundles are checked structurally.
- The runner agent and its installer are tested, in CI.

## [0.10.3] - 2026-08-24

### Changed

- A row on the Runs page opens the run.

## [0.10.2] - 2026-08-24

### Fixed

- Swedish relative times now read as Swedish.

## [0.10.1] - 2026-08-24

### Changed

- The run page introduces itself with when it ran, not 24 characters of hex.
- Relative times are spelled out: "5 minutes ago", not "5m ago".
- The step inspector no longer approves.

### Added

- "Waiting for approval" is a run status.

### Fixed

- Duplicate approval emails — found and fixed.

## [0.10.0] - 2026-08-23

### Added

- Approve or reject from the run page.

### Fixed

- The approval email's fallback link led somewhere you couldn't approve.

## [0.9.1] - 2026-08-23

### Fixed

- The "approval needed" email never sent unless approval links were configured.
- `make env` no longer reports correct production settings as orphans.
- `ACME_EMAIL` is documented; Caddy reads it, so it had never reached `.env.example`.
- Approving something already decided no longer reads as a fault.
- Approval mail now logs one line per notification.

## [0.9.0] - 2026-08-22

### Added

- A flow waiting on a person now emails the people who can decide.
- Approve or reject straight from the canvas.
- Step and field help is reachable on a tablet.

### Changed

- The Swedish translation reads like Swedish software.
- Plain language across every surface a non-technical person reads.
- Implementation words are out of copy a business user reads.

### Removed

- The "notify me on ntfy with the approval link" button.

### Fixed

- Approvals taken from an email link were never recorded in the audit trail.
- The Swedish UI documented a broken secret reference.
- Engine vocabulary was showing on an admin page.
- Two full-width buttons were centred when they should have been left-aligned.
- Swedish gender agreement, and an email header where an account should be.
- Two English typos: the file drop zones said "Step" where they mean "Drop".

### Developer

- The Swedish drop catalogue can no longer rot silently.

## [0.8.0] - 2026-08-21

### Added

- Mirror your flows to your own git.
- ⌘K reaches settings and administration.

### Fixed

- The git page was unfindable for what most people want it for.
- nShift and Roaring had blank app pages.

## [0.7.4] - 2026-08-21

### Fixed

- The Apps index showed a green dot for a broken connection.

## [0.7.3] - 2026-08-21

### Fixed

- The Apps page waited for a run to notice a dead connection.

## [0.7.2] - 2026-08-21

### Fixed

- The fix-it link in the run timeline still went to the Apps index.
- Two undefined CSS variables broke the build.

## [0.7.1] - 2026-08-21

### Added

- Reconnect, per account: each connected account gets its own row, state and Reconnect button.

### Fixed

- A dead OAuth grant showed as connected.
- A failed run's fix-it link went to the Apps index.

## [0.7.0] - 2026-08-21

### Added

- The scenario corpus is now RUN, not just validated (`tests/journey/usecases_run_test.go`).
- `sheets_update_cells` (Google Sheets — Update cells), plus row numbers on Read range.
- `gmail_get_thread` (Gmail — Read conversation).
- `site_check` (Is it up?) fires on the transitions only: once when a site breaks, once when it recovers.
- `stripe_get_customer` (Stripe — Get customer).
- String helpers in formulas: `substring`, `split`, `join`, `replace`, `trim`, `indexOf` and more.
- Steps can be marked non-critical (`continue_on_error` on a node).
- More fields take a wire: Create event's times and description, Slack's thread reply, Drive's file name.
- A generator eval built on the scenario corpus (`make flowgen-eval`).

### Fixed

- The approval link never reached anyone.
- A loop where every item failed now fails.
- `make test` could not finish.
- The flow generator's instructions couldn't build a loop.
- Compare reads numeric text as a number.
- A loop can hand a step structured data.

## [0.6.0] - 2026-08-20

### Added

- Attachments can be read off incoming email.
- `web_watch` (Watch a page — tell me when it changes).
- Five templates: Stripe payment, AI inbox triage, refund approval, invoices to Drive, page watch.
- `join_rows` gained `kind: "anti"` — the left rows with no match on the right.
- Relative time windows on Google Calendar.
- The Regex step's text can be typed on the step.

### Fixed

- Formulas can produce rows and objects again.
- `group_aggregate` accepts the short form.
- ntfy's "Link to open" takes a wire.
- Emailed links now open in the right org.

### Changed

- Shared the location connectors' coordinate helpers.
- Split the HTTP gateway by concern.

## [0.5.0] - 2026-08-20

### Added

- Undo/redo in the flow editor, with shortcuts and toolbar buttons that stay visible when disabled.

### Changed

- "Drop" is now "step" (Swedish: *steg*) everywhere a person can read it.
- `make patch` / `minor` / `major` promote the changelog themselves.
- Detail pages share one `BackLink` component instead of five hand-rolled copies.

### Security

- A store read failure could no longer be mistaken for "this flow doesn't exist".
- Google sign-in is bound to the browser that started it.
- Org Google OAuth client secrets are encrypted at rest.
- Secret redaction no longer leaks when one secret contains another.
- The `DAZYFLOW_DEV_KEY` dev admin token is refused when the deployment doesn't look local.
- bcrypt cost raised from 10 to 12, with an opportunistic re-hash on successful login.
- The audit trail no longer accepts forged entries.
- A Content-Security-Policy on the app surface; `shell` and `git` resolve their workdir through `os.Root`.

### Fixed

- Postgres and the in-memory job store now agree on `core.ErrConflict` for a duplicate enqueue.
- Error-to-HTTP-status mapping uses typed sentinels instead of substrings of user-facing messages.
- 27 steps with non-idempotent external writes opt into engine-side write dedupe, up from 10.
- MCP idempotency keys are hashed over canonical JSON.
- `on_error` is validated when a graph is saved, rather than accepted and then silently ignored.
- Panics in drop-spawned goroutines are recovered instead of taking down the daemon.
- 31 steps declared no `meta` output port while emitting one, so the editor could not wire it.
- Bad `DAZYFLOW_*` values are reported rather than swallowed, and the listener's drain is awaited on exit.

## [0.4.0] - 2026-08-19

Everything below shipped between 0.2.0 and 0.4.0. The 0.3.0, 0.3.1 and 0.3.2
tags were same-day interim cuts that carried no entries of their own, and are
folded in here.

### Added

- Support dashboard (Phase 3): ticket assignment, ownership and status filters, server-side stat tiles.
- Support tickets and in-app chat (Phase 2): file a ticket about a flow; agents work a cross-tenant queue.
- "Contact support" is a real link in the flow editor and, prefilled with diagnostics, on run failures.
- Fortnox connector (`drops/fortnox/`) — create customer, create invoice, list invoices, customer picker.
- 46elks connector (`drops/elks/`) — send SMS via the Swedish/Nordic 46elks API.
- Klarna connector (`drops/klarna/`) — get order, capture, refund.
- nShift connector (`drops/nshift/`) — book a shipment, get its status, cancel an unprinted draft.
- Roaring connector (`drops/roaring/`) — company overview and company search.
- Phone value drop (`drops/value/`) — validate and normalize to E.164, emitting country, number and type.
- OAuth `client_secret_basic` in the daemon's OAuth registry, per provider via `TokenAuthStyle: "basic"`.
- Runs date-range filter: `since`/`until` on the runs endpoints, and a From/To picker on the Runs page.

### Changed

- Twilio, Discord, MQTT and Stripe use a first-class service connection instead of loose secret references.
- Realigned the OpenAPI definition of `GET /api/v1/me/runs` with the parameters it actually accepts.

### Security

- Bumped `go-git/v5` to v5.19.2, clearing GO-2026-6214 and GO-2026-6213.
- Secret references are no longer resolvable out of flow data.
- Stored secrets and wrapped DEKs are bound to their row with AES-GCM additional authenticated data.
- `Vary: Origin` is now sent on every response, not only when the request's Origin matched.
- The auth/webhook rate limiter now reclaims per-IP buckets that were left DEPLETED.
- Run quota is now enforced atomically.
- Stripe webhook events are recorded as processed only after the plan change is applied.
- The sign-in page validates `return_to` before navigating, closing an open redirect.
- The public `/form/` endpoints are rate-limited and cap their request body, matching the `/trigger/` surface.
- Workspace file download now refuses the internal `.scratch` tree, like the other file operations.
- A panic while processing a node or fanning out a trigger no longer crashes the whole daemon.
- `X-Frame-Options: DENY` and `Referrer-Policy` on the app surface; the `/form/` surface is exempt.
- Unauthenticated, DB-touching public endpoints are now per-IP rate-limited.

### Fixed

- The web UI no longer reports its version as `vdev` on a production deploy.
- `make upgrade` no longer tears down a production stack.
- The sign-in form's submit button is no longer permanently disabled after a session expires.
- A graph run can no longer strand permanently when a worker shuts down.
- The scheduler no longer holds its mutex across the poll-outcome marker read, a store round-trip.
- `Engine.Run` keeps the results of a failed node's SIBLINGS.
- Graph/node `timeout_seconds` is clamped against int64 overflow, which had disabled the timeout.
- Per-item write dedupe no longer aliases across an auto-fanned node; the TOTP counter increments atomically.
- Run-list pagination orders by `(enqueued_at, id)`, plus a `jobs(tenant, status)` index.
- A `ListJobsForGraph` scope check no longer admits records with an empty tenant.
- The Postgres event bus no longer drops an event whose row committed out of `BIGSERIAL` order.
- The Postgres pool reserves headroom for the event-bus listener and the leader lock.

## [0.2.0] - 2026-06-27

### Added

- Flow duplicate: copies a flow under a fresh ID as a disabled draft, with a per-card action.
- Licensed under AGPL-3.0-or-later; added `LICENSE` and a README license section.
- SPDX license headers across all Go and TypeScript source files.

### Changed

- Write dedupe is now Postgres-backed in multi-node deployments.
- Consolidated 167 scattered coverage test files into their per-subject `_test.go` files.
- Decluttered the repository root: reference docs into `docs/`, the `Caddyfile` into `deploy/`.

### Removed

- Stale planning docs `GDPR_FIXES.md` and `manual.md`, and the dangling links to them.
- Orphaned root `package-lock.json` stub (no root `package.json` exists).
- The dev-only `cmd/email-preview` generator and its artifact, unreferenced by build, CI and docs.
- The `scripts/ha_loadtest` HA load-test harness, never wired into CI or the Makefile.

## [0.1.0] - 2026-06-08

Initial release.

### Added

- Flow engine: graph flows with branching, fan-out, subgraphs, per-node retries, and persisted runs.
- Connectors: HTTP, Postgres, Slack, Gmail, GitHub, Git, Notion, Sheets, Excel, shell, transforms.
- AI steps: Claude-backed generation and transformation inside a flow.
- Triggers: inbound webhooks and timezone-aware cron schedules.
- Web UI: visual flow builder and run viewer, light and dark.
- MCP server exposing the connector catalog to an LLM agent.
- Control plane: gRPC API with the `dzctl` CLI, plus an OpenAPI-documented REST surface.
- Auth and multi-tenancy: orgs, RBAC, TOTP, invitations, a platform super-admin.
- Secrets: master-key-encrypted storage for connector credentials.
- Deployment: a Docker Compose stack that refuses to start on insecure defaults.
- Versioning: build-time version metadata, surfaced on `GET /api/v1`, plus release targets.
