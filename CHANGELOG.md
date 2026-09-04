# Changelog

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning: [SemVer](https://semver.org/spec/v2.0.0.html).

Versions correspond to git tags `X.Y.Z`. The running version is stamped into the
binary at build time and surfaced on `GET /api/v1` (the `build` block) and in the
web UI's account menu. Write under `[Unreleased]`, never under a released
heading; `make patch` (or `minor` / `major`) promotes it and tags.

## [Unreleased]

### Performance

- **Memory no longer grows with the number of tenants.** Every workspace a
  process ever touched stayed open for its lifetime, each holding a go-git
  repository — so one sweep over an install left one repository resident per
  tenant, permanently. `AutoFSWorkspaces` now evicts least-recently-used, and
  `WorkspaceEnumerator.All` yields one store at a time (an `iter.Seq2`, so the
  `for key, store := range enum.All()` call sites are unchanged) instead of
  returning them all in a map, so a sweep can't defeat the cap. Measured on a
  full read of every flow, 10 per tenant:

  | Tenants | Before | After (cap 512) |
  |---:|---:|---:|
  | 1,000 | 224 MB | 141 MB |
  | 3,000 | 462 MB | 143 MB |

  Tripling the tenants moves the capped figure by 2 MB. That is the point: the
  resident cost follows `DAZYFLOW_MAX_OPEN_WORKSPACES` (default 512, 0 to
  disable), not the size of the install. Eviction is off when graphs are in
  memory rather than on disk, where dropping a store would discard the only
  copy of that tenant's flows.

  Part of the drop is separate and applies whatever the cap: go-git sizes a
  repository's object cache for a source tree at **96 MiB each**, and a
  workspace holding a few dozen small JSON documents is given 2 MiB.

- **A workspace's lock is now held per directory rather than per `Store`.**
  Caching stores and evicting them means two `Store` values for one working
  tree can be alive at once, and they share a single `.git/index` — two
  concurrent worktree writes corrupt the repository, which is reproducible in a
  test (`commit: EOF`) the moment the lock is per-`Store`. The mutex is keyed by
  absolute directory for the life of the process, so the overlap is harmless.
  This makes the eviction above safe; it does **not** make a workspace safe
  across two `dzd` processes on shared storage, which is a different problem.


- **Five sweeps that ran on a timer were scanning whole tables to do it.** Each
  runs on a fixed schedule regardless of how much work it has, so the cost grew
  with the install while the frequency stayed put. Measured on a 2M-row `jobs`
  table (~220k runs) plus 1M run-log, 500k audit and 500k bus-event rows:

  | Sweep | Before | After |
  |---|---:|---:|
  | Concurrency promoter, every 2s **per replica** | 52 ms | 0.4 ms |
  | Orphaned-run reaper, every 60s **per replica** | 14 ms | 0.5 ms |
  | Job retention, hourly, nothing to delete | 668 ms | 0.1 ms |
  | Run-log retention, hourly, nothing to delete | 190 ms | 0.02 ms |
  | Audit retention, hourly, nothing to delete | 131 ms | 0.05 ms |
  | Bus-event sweep, every 5 min | 1053 ms | 62 ms |

  The two graph-run sweeps don't filter by tenant, so `jobs_tenant_status_idx`
  could only serve them by scanning all of itself and filtering;
  `jobs_graph_status_idx` is partial on `kind = 'graph'` (one row in nine) and
  ordered by `enqueued_at`, so it also removes the sort. The three retention
  sweeps had no index on the column they filter by at all — worst in the case
  that ends every pass, proving there is nothing left to delete, which is a full
  scan. `jobs_prune_idx` mirrors the existing `runner_tasks_prune_idx`: partial
  on terminal status, so a row enters it once when it finishes and is never
  updated in it again. `audit_events_prune_idx` is partial on the same
  `action <> 'approval'` exemption the sweep applies, so approval events —
  which are kept indefinitely — are never indexed.

  Cost is one extra index write per row as it goes terminal: a few percent on
  the completion `UPDATE`, nothing measurable on `INSERT`.

  **Upgrading a large install:** these are created at boot like every other
  index here, and a plain `CREATE INDEX` holds a write lock while it builds —
  roughly a second per million rows for `jobs_prune_idx` in the measurement
  above. If your `jobs` table is big enough for that to matter, build them
  first, without the lock, and boot will find them already there:

  ```sql
  CREATE INDEX CONCURRENTLY IF NOT EXISTS jobs_graph_status_idx
      ON jobs (status, enqueued_at DESC) WHERE kind = 'graph';
  CREATE INDEX CONCURRENTLY IF NOT EXISTS jobs_prune_idx
      ON jobs (finished_at) WHERE status IN ('succeeded','failed','cancelled','skipped');
  CREATE INDEX CONCURRENTLY IF NOT EXISTS bus_events_created_idx ON bus_events (created_at);
  CREATE INDEX CONCURRENTLY IF NOT EXISTS run_logs_ts_idx ON run_logs (ts);
  CREATE INDEX CONCURRENTLY IF NOT EXISTS audit_events_prune_idx
      ON audit_events (ts) WHERE action <> 'approval';
  ```

### Changed

- **The scheduler no longer reads every flow in the install to find out what to
  fire.** Its rescan derived the enrollment set by walking every tenant's git
  workspace and loading every flow, every 30 seconds, holding each workspace's
  mutex against the editor while it went. That is O(all flows in the install)
  per pass: measured at 1,000 tenants × 10 flows it took 3.7s and left 224 MB
  resident, and it scales linearly, so around 100,000 flows a pass no longer
  fits inside its own 30s ticker — the scheduler saturates a core and fires
  every cron late, because rescan and the fire tick share a goroutine.

  What a flow asks for is now derived once, when it changes, into a
  `flow_schedules` table, and a rescan is one query. Publishing, unpublishing,
  pausing, resuming, editing a cadence and deleting a flow each re-derive that
  one flow's rows. The same measurement is **87ms**, and the parsed cron
  schedule of an unchanged entry is carried across passes rather than re-parsed.

  Nothing about *when* a flow fires changes: the cadence still comes from the
  draft (so a timing edit takes effect on save) while the revision that runs is
  still the published one, and an entry keeps its next-fire time and poll
  backoff across a rescan exactly as before.

  `DAZYFLOW_SCHEDULE_RECONCILE_INTERVAL` (default `1h`, `0` disables) rebuilds
  the table from the workspaces on the scheduler leader. It is the backstop for
  what the write paths can't cover — the first boot after upgrade, when the
  table starts empty; a projection write that failed after its commit landed; a
  flow deleted while this dzd was down. It skips flows whose rows already match,
  so a steady-state pass writes nothing. Deployments without Postgres keep the
  old walk.

### Fixed

- **Tidy vanished on flows with fewer than two steps.** It greyed out below
  two nodes, deliberately, "so it stays discoverable" — but React Flow dims
  its own controls through `fill-opacity`, which does nothing to an outline
  glyph, so this button carried an `opacity: 0.4` instead. Forty percent on a
  1px stroked icon over the pale canvas grid does not read as unavailable, it
  reads as absent, and it disappeared precisely on the new flows whose author
  had not found it yet. The control is never disabled now; `autoLayout`
  already did nothing under two nodes, so there was never anything to guard
  against. (Second report on this button — the first moved it out of the
  scrolling toolbar into the pinned canvas cluster; both are now under test.)

- **A flow could hold a connection that made every save fail, with nothing on
  screen to fix.** An edge pointing at a port its step hasn't got draws
  nothing (React Flow renders no wire for a handle that isn't there), carries
  nothing, and is refused by the daemon — `edge 0: node "text_1" (text) has no
  input port "in"` — so every autosave failed and moving a step was enough to
  trigger one. `spawnPort` closed the path that created these, but left the
  flows that already had one permanently stuck. The editor now prunes such
  edges once it knows the drops' ports, says which step was involved, and
  leaves the flow dirty so the repair is saved. The prune mirrors
  `core.Validate`'s own exemptions — an uncatalogued step, a disabled step, or
  one whose ports come from its params is never judged — so it cannot remove
  an edge the daemon would have accepted. Two paths that could still create
  one are closed as well: a wire into a step that declares no inputs is now
  refused on the canvas, and paste checks ports instead of trusting the
  clipboard.

## [0.34.0] - 2026-09-03

### Added

- **A run's result is a table when the run produced a table.** The run-detail
  Result panel pretty-printed everything as JSON, so a flow ending in "Group
  and count" or "Save rows" — the shape a manually-run flow most often
  produces — asked the reader to parse braces to find a number. Rows now
  render as a real table, in the column order the producing step emitted
  (`core.Ref.Headers`), and both shapes get a Copy and a Download: rows leave
  as CSV, text as `.txt` or `.json`. A long text result folds behind a
  show-everything toggle rather than being cut at the 600-character banner
  cap.
- **Files a run wrote are listed on the run page, with a download.** A flow
  whose last step is "Save as file", "Write Excel" or "Merge PDFs" has no
  value to show — the answer is a file, and the only trace of it was a path
  string folded inside a step's output port, with no way to tell which entry
  on the Files page this run had produced. Steps' output refs are now
  collected into a Files panel. No new storage: an artifact is a workspace
  file, served by the endpoint that already serves them. A `scratch://` path
  is listed as temporary rather than offered as a download the server would
  refuse.
- **Collections can be published as a login-free read-only page.** A flow
  that only runs by hand still has to show its answer to somebody, and that
  somebody is usually the person who asked for the data rather than anyone
  with an account — previously a screenshot or a seat. A collection can now
  be shared as `/board/<token>`: one regenerable token per collection, the
  same credential model as the hosted forms and the TV overview, with search,
  paging and CSV on the page. Minting takes `graph:edit` (a read-only viewer
  cannot publish data they were only trusted to read), is audited, and is
  listed per workspace so a member can see which collections are public. The
  page carries the collection's rows unredacted — that being the point — so
  the dialog says so before the link exists. The row-delete handle is
  stripped from the public payload, and the response is `no-store`.

### Fixed

- **A `workspace://` path wrote the file into a directory called
  `workspace:`.** Only `scratch://` was ever a real scheme; the redundant
  `workspace://` spelling was never resolved, so the path cleaned to
  `workspace:/reports/summary.json`, the step reported success, and the file
  landed somewhere the author had not asked for — visible afterwards only as a
  junk folder on the Files page. `drops/excel` stripped the prefix, so the same
  path worked there and misfired in every other file step. It is now stripped
  once, in the shared sandbox resolver, and the eight step examples that
  carried it use the canonical bare relative path the editor's file picker
  writes.

### Changed

- **Collections pages past the first thousand rows.** The endpoint caps a page
  at 1000 rows and the footer said "first 1000 loaded", but no control reached
  row 1001 — so a collection a flow had been appending to for months was in
  the store and off the screen. The page now pages at the server's own cap,
  reports the row range it is showing, and says that search, sort and CSV
  cover the loaded page.

## [0.33.1] - 2026-09-03

### Changed

- **Show all data sits in the data panel's top-right corner.** It used to
  trail the port tabs inside the panel's header, which wraps — so on a step
  with several output ports it dropped onto a second row, and it was in a
  different place on every card. As a bare faint glyph on the code surface it
  also carried no more weight than the panel's own chrome, so it did not read
  as something you could press. It is pinned now, and rests as a bordered chip
  the way every other icon control in the app does.

### Fixed

- **A wire dragged onto a Text step made the flow unsaveable, with nothing on
  the canvas to undo.** Dragging off a step's output onto empty canvas opens
  the palette and wires up whatever you pick. Text and Number are value
  sources — a literal you type on the step, with no inputs at all — but the
  palette offered them anyway and then wired to an invented `in` port. Nothing
  was drawn, because the canvas draws no wire to a port a step does not have,
  so there was no wire to delete; meanwhile every save came back `invalid
  graph: edge 0: node "text_1" (text) has no input port "in"`. The palette no
  longer offers a step that cannot take the wire, and a step spawned with
  nowhere to land is placed unwired rather than attached to a port that does
  not exist.

- **A refused save retried itself every 1.5 seconds, forever.** The editor
  keeps the graph marked unsaved after a failed write and re-armed its timer
  as soon as the attempt finished, so a graph the daemon refuses was sent
  again on a loop. Each attempt cleared the error banner and then set it
  again, and that banner takes room on the page — so the canvas jumped up and
  down about once a second while nothing was being fixed. Autosave now stands
  down after a refusal until the flow changes. **Save** is never blocked, so
  you can still ask for the write yourself.

## [0.33.0] - 2026-09-03

### Fixed

- **Five webhook tests raced the clock and went red on CI.** The
  events endpoint answers Stripe *before* fanning the event out to the flows
  that subscribe to it — deliberately, because Stripe retries a delivery it
  considers slow — so the tests had nothing to synchronise on and polled the
  job store for two seconds before giving up. Two seconds of wall clock is a
  coin flip under `-race` on a loaded runner, not an assertion. The handler
  now exposes a completion signal the way it already exposes an injectable
  clock, and the tests wait on the work instead: they finish in ~0.03s rather
  than up to 2s, and the remaining timeout is a hang guard, so a slow runner
  costs latency rather than a failure. The asynchronous path is still what is
  under test — nothing was made synchronous to make it pass. The GitHub
  events handler had the identical two-second poll in its own two dispatch
  tests, so it carries the same signal; both handlers now read the same way.


## [0.32.1] - 2026-09-03

### Added

- **A step's data opens in a dialog for reading.** **Show all data** in the
  face header opens what a step emitted at reading size: every column, up to
  500 rows, cells at 400 characters instead of the card's 28, column headers
  pinned while the rows scroll, and a tab per output port. It is a centred
  panel on a desktop and fullscreen on a phone — the shape the Ctrl+K palette
  already takes. An `Example` renders there too, badged as one, which is why
  this is a dialog and not a second browser window: a window pointed at a run
  route has no run to show for a step that has never run, and would be blocked
  by default besides.

  The card keeps the glance, because **Data view** (`v`) opens every card at
  once to see where a value stopped moving through a flow, and no dialog can
  do that. This is the per-port escalation behind it.

### Changed

- **A card now expands instead of folding over.** The header — icon, name,
  chips and chevron — stays where it is, and the data panel opens below it, so
  the header captions its own data by sitting above it rather than travelling
  down to it. With every card open at once under **Data view**, a header that
  stays put is also what lets you tell which card you are reading.

- **A card names the fields a step emits rather than sampling its rows.** The
  face used to render the same table the dialog does, at three rows and four
  columns with every cell cut at 28 characters. In a 300px card that could not
  be read, and it hid the thing the canvas is actually for: choosing what to
  wire next is a question about field NAMES, and names are short enough that
  all of them fit on a line where a row of truncated values did not. A step
  now reads `author, content, id, link +3 more`, with its row count and port
  type beneath — and a value that is short enough to be its own summary, like
  a trigger's timestamp, is simply shown. One line per card also scans across
  a whole flow under **Data view** in a way three cramped rows never did. The
  values themselves are a click away in the dialog.

### Fixed

- **A step's data table did not draw on a wide card.** The fold rotated the
  header about its own bottom edge, which needed a `perspective` 3D context
  around the data panel — and past roughly 700px of card width Chrome stopped
  rasterising the table inside it. The rows were in the DOM, correctly laid
  out, fully opaque and correctly coloured, and simply never painted: the card
  showed its **From the last run** badge and its row count above an empty box.
  Expanding downward needs no transform at all, so the whole class of bug goes
  with it.

- **Opening a card's data no longer resizes the card.** A seven-column table
  dragged the card from its 200px minimum to nearly 800px and took every wire
  endpoint with it, so pressing `v` reflowed the whole graph. Flex and grid
  items default to `min-width: auto` — "never narrower than my content" — so
  the table's width propagated out through every wrapper, and the panel's
  `overflow: auto` never engaged, because the card simply grew to fit it. The
  well is now excluded from the card's intrinsic width, so a card measures the
  same width open as shut whatever its data.

## [0.32.0] - 2026-09-03

### Added

- **A step can say what it produces before it has ever run.** Building a flow
  meant running it to find out what came out of each step, so wiring the next
  one was guesswork until the first real run — and "just run it" is not free
  when the step sends an email. An output port can now carry `Example`, a
  worked example of what it *carries*, which the editor renders on the card's
  data face with a dashed **Example** badge so it can never be read as a real
  value. A real value always supersedes it, and no row count is shown beside
  an example, because "2 items" next to synthetic data reads as a fact about
  the last run.

  242 of the catalogue's 413 output ports now carry one: every plain-text
  port, every yes/no port, and every record the steps build themselves —
  Gmail and IMAP messages and attachments, Stripe's payment events and
  `Details`, Fortnox, Klarna, nShift, Roaring, GitHub, Drive, Sheets,
  Calendar, Notion, Slack, RSS, PDF, the weather and geocoding steps, the AI
  summarize/classify/reply steps, and the value parsers.

  The remaining ports carry none, deliberately, in three groups. A port whose
  shape comes from the INPUT rather than the step — the row transforms, the
  parsers, a query's columns, a webhook's payload — would otherwise describe
  one caller's data as if it were the step's own. A control-flow pin
  (if/then, approved/rejected) carries a signal, not a value. And a raw
  vendor passthrough (`Full response`, `Customer`, `Full event`) is named by
  the vendor's API, not by our code, so an example could not be verified
  against this repo — and a wrong field name is exactly the harm the feature
  exists to prevent. File ports are a fourth, technical gap: the data face
  renders an example as an inline value, so a file name would read as text.

  Every declared example is pinned to its port's declared kind by
  `drops/output_example_contract_test.go`, so it cannot promise a shape the
  port does not carry.


- **A step's last output survives a reload.** The card data faces read
  per-node outputs, and those lived only in the editor's memory for the length
  of a session — reload, and every face went back to "no data yet", so a flow
  you had already run was as opaque as one you hadn't. `GET
  /api/v1/me/flows/{flow_id}/samples` answers "what did each of this flow's
  steps last produce?" and the editor seeds itself from it on open. Nothing
  new is stored: a node record already holds what its node produced and the
  run viewer already serves those values to the same people, so this reads the
  same rows keyed by flow instead of by run. It merges newest-first across
  runs rather than reading only the newest, which matters because sampling one
  step runs its upstream chain alone — so the newest run often covers a
  fraction of the graph while older runs hold the rest. Live values from a run
  in progress always win over the historical ones. The port hover-peek reads
  the same map and gains the same persistence. Bounded by run retention: once
  a run's records are pruned its values go too, and the card says "no data
  yet" again.


- **A step card now folds open to show what the step produces.** Knowing what
  comes out of a step meant running the flow and reading the run page, so
  wiring the next step was guesswork until the first real run. The card's
  header now folds down about its own bottom edge and uncovers a data panel:
  a table for a list of records, a key–value list for one record, the first
  lines of text, a file's name — whichever the port carries — with a tab per
  output port so a router's two branches are read separately. The step's name
  travels down with the fold and captions its own data. Ports stay on their
  rows throughout, so no wire moves sideways. **Data view** in the toolbar (or
  `v`) folds every card at once, which is how you trace one value through the
  whole flow; a card's own chevron overrides it for that card. The panel shows
  values from the latest run in this editor session — a step that has not run
  says so rather than showing an empty box.

### Fixed

- **Swedish went missing across the step catalogue, and nothing said so.**
  Every drop string is looked up by its English text and falls back to that
  English when there is no match, so a missing or stale translation renders
  perfectly good English instead of failing — 207 of them were. Whole families
  added since the last translation pass (Calendar, Mailbox, SFTP, PDF, and the
  JSON/XML/YAML writers) had untranslated field labels, help text and
  dropdowns; the connection cards for Calendar, Mailbox, SFTP and Ollama asked
  for their server and credentials in English; and rewording the catalogue's
  own help from "wire" to "connect" orphaned 38 existing translations in one
  go, each of them silently reverting a Swedish reader to English. Seven
  strings had been filed in the dropdown map when they were help text and
  titles, where no lookup would ever reach them, and a dead weekday dropdown
  left seven translations behind. All of it is now translated, including the app names
  that were generic English rather than brands — Mailbox (IMAP), Calendar
  (CalDAV), Email (SMTP), Collections, Outgoing webhooks — wherever they name
  an app: the Apps pages, the palette's app chip, the "needs setup" list and
  the template gallery's blocked-app notice.

- **A connection field's placeholder was never translated.** The label above
  the input and the help under it went through the vocabulary; the example
  inside the input did not, so "usually your email address" and "only if your
  instance is behind a proxy" stayed English — and four translations written
  for them had sat unused since they were added.

### Changed

- **`make drop-catalog` now records every string a drop puts on a screen**, not
  just its description and its pin labels: the step name, the action line, the
  app, every params_schema title, help paragraph and dropdown option, the
  connection card, and the "keeps state" copy. The web build's coverage guard
  reads that catalogue, so what it does not record is what nobody notices —
  adding a drop, adding a param, or rewording either now fails the guard until
  the Swedish is written or refreshed, and a translation whose English is gone
  fails it too.

## [0.31.2] - 2026-09-03

### Changed

- **`golang.org/x/crypto` bumped to v0.56.0** for GO-2026-6355, a denial of
  service on a deadlocked established SSH channel. `sftputil.Dial` calls
  `ssh.NewClientConn`, so the SFTP drop reached the vulnerable symbol and CI's
  `govulncheck` scan went red.

## [0.31.1] - 2026-09-02

### Security

- **An Approval step could aim the deployment's own mail server at hundreds of
  thousands of strangers.** The people an Approval step notifies are typed into
  one field as a list, and nothing limited how long that list was — about
  650,000 addresses fit in a flow. Each one gets its own message, sent one
  after another, twice per approval: once when the run pauses and once when
  someone decides. That mail goes out through the operator's own mail server
  rather than an account the flow's author connected, so anyone who can save a
  flow could send from the deployment's address to anywhere. Sending them also
  happens on the worker that paused the run, so a long list held one of the two
  default workers for hours while the rest of the flows waited. A step now
  notifies at most 50 people, and one flow at most 200 across all of its
  Approval steps — splitting the list across several steps buys nothing,
  because they all pause in the same run. The editor says so when you save.

- **The question an Approval step asks, and the error a failed run reports,
  had no length limit in the messages carrying them.** Both are produced while
  the flow runs rather than typed into the canvas, so the limit on how much a
  flow may carry never applied to them — only the much larger limit on a single
  value. Each is rendered into both halves of an email and sent once per
  recipient, so a four-megabyte question pushed forty-four megabytes through
  the mail server for five approvers; the failure text also travels to whatever
  webhook a flow names. Both are now cut to a readable length in the message
  itself. Nothing is lost: the email exists to carry the link, and the
  Approvals inbox and the run page still show the whole thing.

- **A long flow name stopped its own emails from being sent at all.** The
  flow's name becomes the subject line of the approval and failure emails, and
  nothing limited its length. A subject far past what the mail protocol allows
  on one line makes the receiving server hang up, so the message was never
  delivered — which meant a flow waiting on an approval sat there with nobody
  told it was waiting and no way for anyone to release it, and failure alerts
  went missing the same way. Neither failure was visible anywhere. The name and
  the step name in these messages are now shortened to fit.

- **A flow's comment boxes and its trigger list weren't weighed, so a
  one-step flow could be a gigabyte.** The ceiling on how much a flow may
  carry counts every free-form string in it — settings, labels, notes, the
  names of steps and connections. It missed one field on each of the two
  records a flow repeats: the internal id of a canvas comment box, and a
  trigger's type. Nothing else bounds either (the engine ignores comment
  boxes entirely, and the scheduler skips a trigger type it doesn't
  recognize), and the caps beside them limit how *many* boxes and triggers a
  flow has, not how big one is. A flow with a thousand comment boxes was
  1.0 GiB and measured as ten bytes; it saved, published, and cost 280 MiB
  of run history for a single one-step run — every time it fired. Both
  fields are now weighed with the rest.

- **A hosted form's field names had no length limit, so one link answered
  an unauthenticated request with 60 MB.** The hosted form is the only page
  in the product a stranger can open without a credential, and its field
  count was capped after an earlier report. The length of a field *name*
  was not, and the page prints each name four times — so fifty fields named
  three hundred thousand characters each fit inside the flow size limit and
  amplified fourfold on every visit, repeatable by anyone with the link. A
  field name is now capped at 128 characters and the heading at 200; a
  longer name isn't shown, and the editor says so when you save.

- **A step supplied by a runner could raise its own connection limit.** How
  many connections one input accepts defaults to 64, unless the step
  declares its own maximum — and a step registered by a self-hosted runner
  or an MCP host declares that over the wire, where it was taken as given.
  One declaring a maximum of a million restored the unlimited fan-in the
  default exists to prevent, for exactly the steps the canvas can't see.
  A declared maximum is now honoured only up to 1024.

- **A flow could still trigger itself forever — through the Webhook step, its
  own failure webhook, or a second spelling of its own address.** The breaker
  that stops a self-triggering flow is a depth header stamped on a call that
  comes back to us, and it was written by one drop out of the several that can
  post to a URL of the author's choosing. Built with the **Webhook** step —
  whose entire purpose is POSTing to a URL — the same loop ran again from one
  anonymous form submission. Pointed at its own form, a flow's *failure*
  webhook was a loop with no step in it at all, at around a hundred and twenty
  runs a second, because the daemon posts that one itself and the flood
  throttle beside it covers only the email channels. And "is this us?" was a
  string comparison against the configured public URL, so writing out the
  default port, adding the DNS root dot, or using the address the daemon
  answers on inside its container all read as a third party. The stamp now
  lives in the HTTP client every outbound call shares — including the failure
  webhook, and including each hop of a redirect — it is set rather than filled
  in, so a step cannot hold the chain at zero from its own headers, and the
  comparison normalizes the origin and covers every address the daemon is
  reachable at.

- **A flow could trigger itself forever through its own hosted form.** The
  self-triggering webhook loop was closed with a trigger-chain depth header
  that the `/trigger` endpoint refuses past — but the hosted form submitted at
  depth 0 unconditionally, so a flow whose HTTP step posted to its own
  `/form/` URL ran without limit, and that is the door that needs no secret at
  all: one anonymous submission, runs climbing for as long as the daemon was
  up. The form endpoint now carries the depth like every other trigger path.

- **A Wait step held one of the daemon's execution slots for its whole
  duration.** A worker claims and runs one node at a time, and the pool
  defaults to two across every tenant, so two parallel Wait steps in a single
  flow stopped everyone's runs for as long as their author typed — sixty
  queued two-second waits left an unrelated one-step flow unfinished after
  forty-five seconds. A wait now hands its slot back and asks to be picked up
  again at its deadline, so the flow waits without occupying anything. A
  timeout set on the step still applies: a wait that cannot fit inside it is
  refused instead of running past it.

- **The graph size ceiling did not weigh identifiers.** It measured settings,
  labels, notes and frame titles, and skipped node IDs and module names as
  already bounded by the node and connection ceilings — which bound how MANY
  there are, not how long each one is. Nothing validates a node ID (only the
  flow ID is checked), and a step outside the catalog has no port rules at
  all, so the same oversized flow the ceiling exists to refuse came back with
  its payload moved into the names: a hundred steps carrying 256 KiB IDs is
  26 MB that measured as 500 bytes, held up only by the 200 MiB request cap
  meant for file uploads. Every string a caller supplies is now charged.

- **The trigger cap counted the trigger list, not the trigger steps.** A
  schedule lives on the step now, and the scheduler gives each step its own
  entry — it has to, since each keeps its own cursor — so pasting two hundred
  identical Schedule steps produced two hundred entries and a hundred runs a
  minute, bounded only by the step ceiling of a thousand. Trigger steps and
  declared triggers now share the same cap of 32.

- **A hosted form rendered as many fields as the flow declared.** A
  submission was capped at fifty fields, but the page was not: a form
  declaring a hundred thousand answered a single unauthenticated GET with
  9 MB and over a second of CPU, from a flow well inside every other limit.
  The page now shows at most the fifty a submission could carry, and the
  editor says so while the form is being built.

- **One saved flow could fire itself two thousand times a minute.** Nothing
  capped how many triggers a flow declares, and the scheduler keyed each
  entry by its position in the trigger array — so pasting the same
  `* * * * *` schedule 2000 times produced 2000 scheduler entries and 2000
  runs a minute, from a graph of 1.7 MB that validated clean. A flow now
  declares at most 32 triggers, and identical schedules collapse into one
  entry.

- **The fan-in rule was skipped for exactly the steps it could not see.**
  Port rules come from a module's manifest, and a module outside this
  instance's catalog has none — that is deliberate, since a tenant's runner
  and MCP steps are registered elsewhere. But fan-in needs no port list: the
  engine assembles one value per port wherever the step runs, so 300 wires
  into one input of a runner step were stored, run, and reduced to whichever
  value happened to be read last, with nothing reported. Fan-in now applies
  to such a step on both the server and the canvas. A port NAME we have no
  description for is still accepted — that part is the step's own business.

- **Nothing weighed a graph.** Every ceiling counted things — nodes, wires,
  frames, waypoints per wire — so a flow inside all of them still reached
  156 MB of step settings, labels and frame titles, and wrote 208 MB of run
  records, bounded only by the request cap that exists for file uploads. A
  graph's own payload is now capped at 16 MiB, and a connection's routing
  knots are capped in total as well as per wire (the per-wire cap alone left
  1.28M of them reachable, about 21 MB in every run record).

- **A flow ID was a path and a git ref name, validated nowhere.** It becomes
  `graphs/<id>.json` in the workspace repository and a component of the tag
  that marks a published revision, and no gate checked it: `a/../../escape`
  saved a flow outside `graphs/` that could then never be loaded, published
  or deleted; `.` and `..` saved one under a nonsense name; an ID with a
  space saved but could never be published, because a git ref may not contain
  one; and a 300-character ID worked in memory and failed on disk. IDs are
  now letters, digits, `-`, `_` and `.`, up to 128 characters, checked at the
  save gate and again in the store every writer goes through.

- **The value ceiling was blind to a list of step results, so the doubling
  bomb still worked.** Both guards that enforce it — the per-value check and
  the per-run state budget — measure with `core.ApproxValueSize`, which
  charged a *struct* one word without walking its fields. Merge emits a list
  of Refs (so does For each's `results`), so an 18 MB value measured as 16
  bytes and sailed past a 1 MiB ceiling. Wiring each Merge into the next
  doubled the payload per hop with no templates involved: 21 ordinary steps
  stored a gigabyte, and the out-of-memory throw that follows is not a panic
  — no recover catches it, so one tenant's flow ends every other tenant's
  runs. A Ref is now charged for its strings and whatever it carries inline,
  and a struct's exported fields are walked rather than assumed small.

- **A subgraph hop reset the trigger-chain counter.** The cap that stops a
  flow triggering itself lives on the run record, and a child run was created
  without it — so `A → Reusable flow B → B calls A's trigger URL` cycled
  forever, each hop setting the counter back to zero and each new webhook run
  being a fresh top-level tree that neither the nesting cap nor the fan-out
  budget could connect to the last one. A child now inherits its parent's
  depth.

### Fixed

- **A breakpoint left behind runs that never ended.** Breakpoints live in the
  saved flow, and the dispatcher honoured them on every run — including the
  ones a schedule or webhook starts with nobody watching. A paused run is
  never reaped (its un-dispatched dependents read as work still outstanding)
  and counts against the workspace's concurrency, so a published flow with a
  breakpoint quietly accumulated runs no one would ever continue, and could
  take every concurrency slot for good. A breakpoint now holds only a run you
  started and are watching; stepping through a run is unaffected.

- **Wiring rules that nothing enforced.** A Reusable flow step was exempt from
  *every* port check, fan-in included, because its real ports come from its
  own settings — so 200 wires could land on one of its inputs and 199 values
  were silently dropped. Fan-in needs no port list, so it now applies there
  too. A variadic input with no declared maximum was bounded only by the
  5000-connection graph cap; it now takes 64 unless its port says otherwise.
  The same wire drawn twice is refused wherever it lands, including on a
  variadic pin, where duplicates used to accumulate freely. The canvas
  applies all three while you drag, so a wire that won't save won't draw.

- **Editor metadata was uncapped.** A connection's routing knots and a flow's
  comment boxes ride in the graph JSON that every run record carries and the
  worker re-parses on each dispatch pass, but neither was bounded by the node
  or connection caps — a two-step flow could carry tens of thousands of each.
  Both are now capped, and the size ceilings are checked before a graph is
  walked, so an oversized one is refused without being validated first.

- **One flow could take the whole daemon down.** Nothing capped the SIZE of a
  value a step emits. A step whose text names its predecessor twice doubles
  the payload, so a chain of about twenty such steps — a flow well inside
  every existing limit — turned a kilobyte into gigabytes and killed the
  process with a runtime out-of-memory throw. That is not a panic: no recover
  catches it, so one tenant's flow ended every other tenant's runs too.

  A single value is now bounded at both ends — when a template expands into
  it and when a step emits it — and the step that crosses the ceiling fails
  with `value_too_large` naming the placeholder that did it. The run as a
  whole is bounded too: every step stores its own copy of what it emitted and
  the pass pin threads a payload down the chain, so one large value became
  payload × steps of stored run state; past that budget the run stops with
  `run_state_too_large`. Both are configurable — `DAZYFLOW_MAX_VALUE_BYTES`
  (64 MiB) and `DAZYFLOW_MAX_RUN_STATE_BYTES` (1 GiB). Row *count* was already
  capped; this caps bytes.

- **A flow could trigger itself forever.** A published webhook flow whose HTTP
  step calls its own trigger URL ran until someone noticed: each iteration is
  a fresh top-level run, so the subgraph nesting cap and the fan-out budget —
  which follow parent links inside one run tree — never saw it. The same holds
  for two flows firing each other.

  A step calling one of *our own* URLs now carries how deep the trigger chain
  already is, and the trigger endpoint refuses past eight, answering 429. The
  header goes only to this instance's own public origin
  (`DAZYFLOW_PUBLIC_BASE_URL`), so nothing about a run's topology reaches a
  third party.

### Fixed

- **Connections the editor called invalid were still saved and run.** The
  port-level rules — a single-value input takes one wire, a variadic one
  respects its max, an edge names ports that exist and whose types meet, an
  `on_error` is one of the four policies — ran in the editor and in the
  in-process engine, but the daemon's save and run paths checked only the
  structural ones. So a flow with two hundred wires into one input saved,
  ran, and reported **success**: the engine keeps whichever wire it read last,
  and the author is never told which value arrived. A typo'd `on_error`
  silently degraded to abort, dropping the failure handling that was asked
  for.

  Both paths now apply the same wiring gate. Two authoring rules stay out of
  it deliberately: an unconnected required input (a work-in-progress state the
  step reports better itself at run time) and a module missing from the
  catalog (a tenant's runner and MCP drops live outside the default palette).
  A step whose ports come from its params — a Reusable flow's input/output map
  — is exempt from the port rules, which also silences a false "port does not
  exist" the editor had been showing on those.

  On the canvas the second wire into a single-value input no longer sticks,
  the same way a type-incompatible wire already didn't.

- **A large flow cost minutes of CPU per run.** Readiness was re-derived by
  scanning every edge in the graph and re-reading a record per edge — for
  every dependent, after every step finished — while each step re-parsed the
  whole graph payload. Four hundred steps that do nothing took 2m43s, and
  against Postgres each of those re-reads is a query. A run's wiring is now
  indexed once and each predecessor read once per pass, and a worker keeps
  the parsed graph: the same flow takes 5s, and the dispatcher no longer
  writes a log line per dependent per completion (a wide fan-in buried the log
  in thousands of "waiting" lines; `DAZYFLOW_DEBUG_DISPATCH=1` brings them
  back).

  Connections are also capped now — 5000 per flow. The node ceiling never
  bounded the work, because cost scales with wires: two steps can carry half a
  million of them.

- **"Wait 292 years" completed instantly.** `Wait` multiplied milliseconds
  into nanoseconds without an overflow check, so past ~9.2e12 the duration
  wrapped and the step reported success without waiting at all. Anything over
  a year is now refused with a message pointing at the Schedule trigger.

- **Tidy could not be reached when you needed it.** The editor's toolbar has a
  scrolling left half so the pinned actions — Run, Publish — never slide off
  the right edge. Tidy lived in that scrolling half, and the region collapses
  to whatever width the pinned cluster leaves it: on a laptop with the
  Inspector open there was often nothing left, so Tidy sat off-screen behind a
  deliberately hidden scrollbar with no keyboard shortcut to fall back on. It
  also hid itself entirely below two nodes, which made its absence read as
  random rather than as a rule.

  Tidy now sits in the canvas controls at the bottom-left, beside zoom and
  fit-view. That cluster is pinned to the canvas and cannot scroll away, and a
  whole-graph layout action belongs with the other viewport tools anyway. It
  stays mounted at every size and is merely disabled below two nodes, so the
  feature is visible before it is usable.

- **The editor toolbar hid its overflow.** The same scrolling region strands
  Add step, Add note, History and the saved indicator, and its scrollbar is
  hidden on purpose — so the tools out of view were reachable only by guessing
  that the bar swipes sideways. It now shows an arrow on whichever side still
  has controls off-screen, and the edge fade follows the same measurement
  instead of being painted on unconditionally, which had been dimming the last
  24px of a bar that fit.

## [0.31.0] - 2026-09-02

### Added

- **AI steps can read a file, not just text about one.** Ask, Summarize,
  Extract fields and Classify gained a variadic **Files** input: connect a PDF
  or an image — an emailed invoice, a scanned receipt, a screenshot — and the
  model reads the document itself. Because it reads rendered pages rather than
  a text layer, a **scan** works as well as a text PDF.

  This closes the hole under our own headline use cases. Cases 6 and 36 saved
  an invoice PDF and then ran Extract on the *email body* — which for a real
  invoice says "please see attached". The AP demo's own README listed OCR as
  faked. Nothing in the catalogue could read a PDF.

  One shared implementation, four providers, four different wire formats:
  Anthropic takes a `document` block, OpenAI a `file` part with a data: URI,
  Gemini `inline_data`, and Ollama only images on the message. Each is asserted
  against the body we actually send, because a wrong block type fails at the
  vendor with an error about our request.

  A provider that cannot carry a file **refuses** rather than dropping it.
  Ollama gets images but not PDFs, and says so naming the file and what to use
  instead. The alternative — a model answering confidently about a document it
  never received, and a flow filing that answer as fact — is the expensive
  failure this feature could have had.

  The file's type is decided by sniffing its bytes, not its extension: a `.pdf`
  that is really a PNG is sent as an image instead of being rejected by the
  vendor as a malformed document.

- **The invoice flows now read the invoice.** Use cases 6 and 36 and the
  `invoices-to-drive` template wire the attached PDF into the AI step's Files
  input. They used to hand it the EMAIL BODY, which on a real invoice mail
  says "please see attached" — so the vendor and amount they logged were
  whatever a model could infer from a sender and a subject line. Case 6's
  README verdict said "read off the email" and now says what it does. This is
  the payoff of the Files input, which until now nothing used.

- **Two templates for apps that had none.** `sftp-feed-to-sheet` picks up the
  CSV a bank or supplier drops overnight and appends its rows to a sheet, each
  file handled exactly once. `invoices-into-one-pdf` combines a period's
  invoice attachments into a single document and emails it to the accountant.
  Templates are how anyone finds a step, and 30 of 40 apps still have none —
  these are the two whose steps shipped most recently with no way to discover
  them.

- **Calendars can be amended and cancelled, not only created.**
  `caldav_update_event` moves or renames an event, `caldav_delete_event` and
  `gcal_delete_event` remove one. Cancelling was the missing half of every
  booking flow — use case 22's withdrawn time-off request had nowhere to go.

  Update is read-modify-write, not replace: a blank field means "leave it
  alone". A step that wrote only the fields an author filled in would silently
  drop the guest list, the description and the recurrence rule off any event it
  touched. Moving the start without a new end keeps the event's LENGTH, because
  "push it back an hour" must not quietly shorten a two-hour meeting. SEQUENCE
  is bumped so attendees' clients pick the change up.

  Deleting something already gone is not an error — the calendar is in the
  state the flow asked for, and failing would break the second run of a
  cancellation flow. `meta.removed` says which of the two happened.

- **All-day events on `caldav_create_event`.** The behaviour difference from
  the Google step that I documented rather than fixed last time: a plain date
  produced a midnight-to-01:00 timed event. There's now an explicit
  "All-day event" switch writing a date-valued `DTSTART`, which is what
  iCalendar means by all-day. Explicit rather than inferred from the input's
  shape, because "tomorrow" is a date and almost never means all day.

- **YAML, and a way out for JSON and XML.** `parse_yaml` reads YAML into the
  same rows + headers shape Read JSON produces — config files, Kubernetes
  manifests, docker-compose, an HTTP response — with the same `path`
  semantics. `build_json`, `build_yaml` and `build_xml` serialise rows back
  out, which nothing but CSV could do before: an API body, a config file to
  commit, an XML import for an older system.

  Reading YAML cost no new dependency — `go.yaml.in/yaml/v3` was already
  direct — and reuses Read JSON's own `digPath` and `rowsFromValue`, because
  the two formats describe the same value model.

  Two places where YAML genuinely isn't JSON, both handled: a stream may hold
  several `---` documents (one row each, which is how a manifest bundle is
  shaped), and YAML permits non-string mapping keys, which decode to a shape
  no row consumer here can read and are now rendered as text rather than
  arriving empty. yaml.v3 refuses an alias bomb on its own, which matters
  because this text can come from an `http_request` — asserted, so a library
  change that removes that protection is caught here.

  Write YAML encodes rows as YAML nodes rather than Go maps, because yaml.v3
  sorts a map's keys: a generated config file that alphabetises itself, or
  reorders every line between runs, is unusable in a repo. Write JSON turns
  Go's HTML escaping **off** — `&` as `\u0026` is valid JSON but arrives
  mangled to anything reading the raw body. Write XML **refuses** a column
  name that isn't a legal element name rather than sanitising it: renaming it
  silently would put the wrong tag in someone's import file, and the failure
  would surface at the far end as a schema mismatch.

- **Work on PDF files themselves: the new PDF app.** `pdf_merge` joins several
  into one (the natural end of a filing flow — one file for the accountant
  instead of forty), `pdf_split` cuts one into a file per page or every N
  pages, and `pdf_info` reads the page count, title, dates and whether it's
  password-protected, so a flow can branch before doing work.

  Local, in-process, pure Go via pdfcpu — no external binary and no browser
  engine. It costs **+3.3 MB** on the daemon binary, 45.9 MB to 49.2 MB,
  measured by building with and without the app rather than probed in
  isolation (an isolated probe under-reported it tenfold, because taking the
  address of a few API functions doesn't link what real use does). Every step
  checks the `%PDF` header before parsing, because pdfcpu's own error for a
  text file wired in is "xref table: no header version available", which reads
  like a corrupt document rather than the truth.

  Not text extraction, deliberately: pdfcpu doesn't do it and the pure-Go
  alternatives fall over on a real invoice. Reading what a PDF *says* goes
  through an AI step's Files input.

### Changed

- **`golang.org/x/image` bumped to v0.45.0** for GO-2026-6222, a
  `vp8l.Decode` vulnerability. It arrived as a transitive dependency of pdfcpu
  (`drops/pdf` → pdfcpu → `x/image/webp`) and broke CI's `govulncheck -mode
  binary` scan of `dzd`. Worth knowing that the SOURCE scan passed and only
  the binary scan caught it: the two do different reachability analysis, so
  `govulncheck ./...` alone is not the gate.

- **`combine`, `file-search`, `scissors`, `file-code`, `calendar-x` and
  `calendar-clock` added to the web icon registry**, so the PDF, YAML and
  calendar steps draw their own glyphs rather than falling back to their
  category's.

## [0.30.0] - 2026-09-02

### Added

- **Move files over SFTP: the new SFTP app.** `sftp_list_files`,
  `sftp_download_file` and `sftp_upload_file` reach a bank's drop box, a
  supplier's feed, or a server you run yourself — an address and an account
  rather than an app to register. This is where a lot of corporate
  integration still actually happens, and until now Upload to Drive was the
  only place a flow could put a file.

  Host-key verification has no default and cannot be turned off. Paste the
  server's `SHA256:…` fingerprint (or a known_hosts line); until you do, the
  connection deliberately fails — quoting the fingerprint the server actually
  offered, so the fix is a copy and paste rather than a hunt for
  `ssh-keyscan`. Accepting any key would allow a silent man-in-the-middle,
  and the server credentials are what it would collect.

  'Only new since last run' on List files tracks the newest modified time
  **plus the names sharing that second**. A bare timestamp loses files: SFTP
  reports whole seconds, a feed drops a batch inside one, and a poll landing
  mid-batch would record that second and skip every straggler — silently.

  Known limit: one server per connection. Someone with a bank drop box *and*
  a supplier feed needs two, which this shape doesn't give them yet.

- **Calendars that aren't Google's: the new Calendar app.**
  `caldav_list_events` and `caldav_create_event` speak CalDAV, which Fastmail,
  mailbox.org, iCloud, Nextcloud and a Radicale box of your own all support.
  The reminder and booking use cases (7, 21, 22, 32) were Google-only until
  now; the events come out in the same shape `gcal_list_events` emits, so a
  flow moves between the two by swapping the step.

  The connection address can be a discovery root, a principal, or one
  calendar's own path — providers publish all three and nobody can be expected
  to know which they were handed, so the client walks from whatever it is given
  down to the calendar collections underneath. An account with several
  calendars is not silently narrowed to one: the error names them so the right
  one can be copied into the field, and Test connection lists them.

  Relative time windows work exactly as they do on the Google step —
  `"tomorrow"` to `"tomorrow+1d"` for tomorrow's bookings, resolved in the
  timezone you set.

### Changed

- **A guard on the write-dedupe flags** (`drops/writepolicy_test.go`).
  `DedupeWrites` and `Idempotent` are hand-set bools that decide whether the
  engine may replay a step or must replay its recorded result; get them wrong
  on a step that writes outward and a lease reclaim sends a second email or
  books a second slot, with every existing test still green. The 28 drops that
  opt in are now pinned by name, and a manifest declaring both flags at once —
  a contradiction — fails. `engine/writededupe_test.go` covered the mechanism;
  nothing covered which drops asked for it.

- **`folder-open` added to the web icon registry**, so List files draws its own
  glyph rather than falling back to its category's.

### Fixed

- **The Mailbox app is called Mailbox in Swedish too.** Its step cards read
  "Brevlåda" while the Apps page and every mention in the prose said Mailbox,
  so one app appeared under two names. The name is now left as it is, like
  Gmail, Slack and Fortnox; where the Swedish text needed the ordinary noun it
  says "mejlkonto" instead.

## [0.29.0] - 2026-09-02

### Added

- **Read email over IMAP: the new Mailbox app.** `imap_search_messages` finds
  mail in a folder of any account that speaks IMAP — Fastmail, mailbox.org,
  Migadu, iCloud, your own Dovecot, or Gmail with an app password — and emits
  the same record shape as Gmail's Search emails, so a For each over the
  matches and `${item.id}` carry over unchanged. Configure the account once on
  the Mailbox page (server, port, security, login, folder) and test it before
  saving.

  This exists because the read side of Gmail is gated on `gmail.readonly`, a
  Google *restricted* scope: it grants freely on a Workspace-internal OAuth
  app, but an external app needs a security assessment, and the practical
  escape — Testing mode — expires refresh tokens after seven days. IMAP needs a
  password and nothing else. Email (SMTP) still can only send; SMTP has no
  command to list or search a mailbox at all.

  'Only new since last run' tracks the folder's own UIDVALIDITY and UID rather
  than a timestamp, so a published poll acts on each email exactly once, and a
  folder that gets recreated re-baselines instead of replaying itself. Searches
  use EXAMINE and BODY.PEEK, so they never mark mail read.

- **Read one email over IMAP, and take its files.** `imap_get_message` returns
  one message as Date / From / Subject / Body — the same four pins as Gmail's
  Read email, and the body in full rather than the shortened form a search
  carries. `imap_get_attachments` saves the files off a message, skipping
  inline signature images, with the same `only` filter and
  first/files/count outputs as its Gmail counterpart.

  Both work from BODYSTRUCTURE: the server describes the MIME tree, and only
  the parts actually wanted cross the wire — so reading the body of a mail with
  a 20 MB PDF hanging off it costs a few hundred bytes, and an oversized set of
  attachments is refused before anything is downloaded. Reads use BODY.PEEK
  throughout, so neither step marks mail read. Content-transfer-encoding and
  charset are both decoded, which is what turns a quoted-printable ISO-8859-1
  Nordic invoice into words instead of `Fakturan =E4r betald`.

- **`imap_mark_seen` closes off a handled email.** Put it at the end of a
  triage or filing flow and the mail stops showing as unread, so a human sees a
  tidy inbox and an "Unread only" search won't pick it up again. Genuinely
  idempotent — read is read — so unlike the send steps it lets the engine retry
  freely and needs no write-dedupe.

  A STORE against a message that no longer exists is not an error in IMAP: the
  server accepts the command and does nothing. So this step asks for the
  updated flags back rather than using .SILENT, and reports a deleted email as
  not found instead of claiming success.

- **A second AI-triage template, for a mailbox that isn't Gmail**
  (`ai-email-triage-imap`), plus use case 36. The template is the Gmail one
  with a single node swapped — same edges, same downstream steps, because the
  IMAP search emits the same record shape. Use case 36 runs the whole of case 6
  against a plain IMAP mailbox: find the invoice mail, take the PDF, read the
  body for AI to extract from, mark it done.

  Not yet: threading or IDLE. Microsoft 365 needs OAuth for IMAP and so is out
  of reach for now.

### Changed

- **`mail-check` added to the web icon registry**, so the new Mark as read step
  draws its own glyph rather than falling back to its category's.

- **Attachment-saving rules moved into `drops/internal/mailfiles`**, shared by
  `gmail_get_attachments` and the new `imap_get_attachments` instead of copied:
  which parts to keep, the size cap, and the filename sanitiser — which is a
  security boundary (the name comes from the sender), so a second hand-copied
  version of it would be a second thing to get wrong.

- **Shared helpers replace per-connector copies.** One `drops/cursor` package
  now backs the poll watermarks of Gmail, RSS, Google Forms and Home Assistant
  (the daemon wires it once); OpenAI and Ollama read the chat-completion
  envelope through `drops/internal/chatcompletion`; request-body, status-check,
  progress and error-result helpers live in `drops/internal/params`; header
  derivation and cell rendering in `drops/internal/rows`; secret-name
  validation and sandbox-escape detection in `core`.

### Removed

- Dead code kept alive only by its own tests: `HasConfiguredSchedulerTrigger`
  (Go and web), `webapi.ApplyRefresh`, `githubDo`, the `SetHTTPBase` hooks in
  the 46elks and Fortnox connectors, and a dozen small wrappers. Test-only
  helpers moved into `_test.go` files. Unused web exports un-exported.

## [0.28.8] - 2026-09-01

### Added

- **Recent decisions on the Approvals page.** Beneath the inbox, the settled
  approvals: the question, the value that was decided on, who decided, which
  way, when, and the comment they left — newest decision first, each row
  linking to its run.

  The inbox can only ever show what is outstanding; a card leaves it the instant
  someone acts. So the page could not answer either of the two questions people
  came back with — has this already been handled, and what did we decide the
  last time one of these came round — and the record existed only as an audit
  row and a resumed node's output.

  A decided approval is a SUCCEEDED node-record, rejections included: the step
  ran to completion and routed the value out its `rejected` port. Which port
  carries the value is the decision, and `pending_url` — emitted at the pause
  and carried across the resume — is what marks the record as an approval at
  all. Both new store filters exist for that: `HasOutputPort` finds approvals
  without a module column to filter on, and `NewestByFinished` orders by when
  someone decided rather than when the flow got there. The second is not
  cosmetic — an approval parked three weeks and approved this morning is the
  newest decision and one of the oldest records, and under a `LIMIT`, ordering
  by the wrong column doesn't reorder the page, it drops the row off it. A
  partial index on the same predicate keeps both queries off the full history
  of every succeeded step the workspace has ever run.

  Cancellations are in the list too, as their own outcome. Cancel a run parked
  at an approval and the request is called off without anyone deciding it —
  previously it just left the inbox and appeared nowhere, which reads like
  somebody handled it. This needed a fix underneath: cancelling **overwrote** a
  node's result with a bare error, and for a parked step that result was the
  only record of what the request had been for — the prompt, the value, the
  approval link, all erased. Cancel now keeps what a step had already
  published and stamps the error alongside it, so a cancelled approval can
  still say what it was for (on the run page as much as in this list). Nothing
  downstream can see the preserved ports: `classifyEdge` blocks every edge out
  of a cancelled record whatever it holds. Runs cancelled before this change
  carry no such record and stay absent rather than appearing half-rendered.

  New endpoint: `GET /api/v1/approvals/decided` (same `?tenant=`/`?workspace=`
  scoping as the pending list, plus `?limit=`, default 50). Decided and
  cancelled are two indexed queries merged by outcome time — taking the newest
  `limit` of each and keeping the newest `limit` of the union is exactly the
  newest `limit` of the whole.

### Fixed

- **A mail-server login that was never presented reported "OK".** The Email
  app's "Test connection" went green, and the Email step reported a successful
  send, for a connection whose credentials the server had never been asked to
  rule on.

  Two separate holes, both from treating "no username" as "this server needs no
  login". Three tenant-facing paths had independently grown the same
  `if username != "" { PlainAuth(...) }` (the Email drop, the connect-time
  verifier, the email-template test send), so a connection carrying a password
  but no username — a password field filled in while the username was left
  blank, the two being separate fields on the same card — threw the password
  away and opened an unauthenticated session. Any
  relay that accepts unauthenticated mail then delivered it, and the step
  reported OK. `smtputil.Auth` is now the one place that decides this: no login
  at all stays valid (an internal relay), half a login is an error naming the
  field that is missing.

  The second hole was in verification itself. `smtputil.Verify` ran
  EHLO/STARTTLS/AUTH and quit, so with no login configured it asserted nothing
  about authorization — a server that refuses every unauthenticated submission
  had still answered "ok" to everything we asked it. Verify now offers the
  configured sender in a `MAIL FROM` and abandons the transaction with `RSET`
  (no recipient, no `DATA`, nothing delivered), which is the command every big
  provider uses to say no — Microsoft 365's `5.7.57`, Gmail's and Exim's `530`.
  A failing login, an unauthenticated session, and a From the server won't let
  this login use now all fail the check with the server's own words.

  Also: when connection security is `starttls`, the server doesn't offer
  STARTTLS, and a login is configured, the dial now fails with what actually
  happened and the two real fixes instead of net/smtp's bare "unencrypted
  connection". Loopback stays exempt (a local mail bridge), and an
  unauthenticated relay on the default `starttls` setting keeps working exactly
  as before — only credentials are held back from a cleartext hop.

  The operator's own Mailer takes its login from `DAZYFLOW_SMTP_URL`, where a
  username with no password is documented (it doubles as the sender for an
  unauthenticated relay), so that stays legal; the mirror-image typo
  `smtp://:password@host` now fails at parse time instead of silently sending
  unauthenticated.

## [0.28.7] - 2026-09-01

### Fixed

- **A passing e2e assertion reported itself as failed, and it read like the MCP
  integration was broken.** `tests/e2e/mcp-pipeline/run.sh` failed CI on
  `[!!] tools appear as mcp:ap-demo:* modules` while every assertion around it
  passed — including the three that only hold if that exact tool resolved, ran,
  and routed. Nothing was wrong with MCP registration.

  The assertion is `dzctl module list 2>&1 | grep -q 'mcp:ap-demo:lookup_user'`
  under `set -euo pipefail`. `grep -q` exits the instant it matches, and the
  match sits at byte 7074 of a 13.5KB listing, so dzctl is typically still
  writing when the reader goes away: it takes `SIGPIPE`, and `pipefail` promotes
  141 to the pipeline's status. `PIPESTATUS` was `141 0` — producer killed,
  grep matched. Whether dzctl flushed the remaining ~6.4KB into the 64KB pipe
  buffer before grep's exit is a race, which is why it reproduced 2 times in 10
  locally and passed on every earlier run.

  Fixed in the `assert` helper rather than at the one call site: it now clears
  `pipefail` around the condition and restores it after, so the grep's exit
  status — which *is* the assertion — decides the result. A producer that
  genuinely fails still fails the assertion, because each one redirects `2>&1`
  into the stream grep reads, leaving nothing to match; verified against both a
  pattern that is absent from the catalog and a dzctl pointed at a dead port.
  `tests/e2e/ap-invoice/run.sh` carried the same latent bug across all seven of
  its `grep -q` assertions and got the same fix. The two-stage assertions
  (`… | grep name | grep -q state`) were never exposed: the middle grep reads to
  EOF, so the producer never sees a closed pipe.

## [0.28.6] - 2026-09-01

### Fixed

- **Stripe's app page said "Connect Stripe" twice.** Stripe is the one
  integration that ships two credentials — the API key as `connection_fields`
  on the action drops, and `STRIPE_WEBHOOK_SECRET` as a requirement on the
  triggers — so `/apps/stripe` correctly renders two connection cards. Both
  titled themselves `Connect {{name}}` off the *app* name, leaving nothing to
  tell them apart. When a page carries more than one card, each now names the
  credential it holds — "Connect Stripe — Secret API key" and "Connect Stripe —
  Webhook signing secret". Single-connection apps keep the plain app name, and
  so does a multi-field card (SMTP's host/user/pass has no single name).

  Both names go through `connectionText`, so Swedish reads "Anslut Stripe —
  Hemlig API-nyckel" / "… — Webhook-signeringshemlighet". That also fixes a
  pre-existing gap: `SecretCard` rendered its field label untranslated even in
  the card body, so a Swedish user saw one English label under a Swedish
  heading.

- **The flow editor stopped warning about unconnected apps when the secret
  store is off.** With `DAZYFLOW_MASTER_KEY` empty — the quick start's default —
  `/api/v1/secrets` is disabled, so the editor holds `secrets === null` and
  every credential check reads "can't tell, don't block". OAuth and
  `${secret.NAME}` each have an admin-blocked parallel that still warns in that
  state; the `conn.<slug>.<key>` shape (Stripe, Claude, SMTP) had none, so
  dropping one of those steps on the canvas produced no pre-run banner at all.
  Added `unavailableConnectionApps` alongside the existing two. A field typed
  straight into the node still counts as satisfied, and the per-node badge keeps
  its deliberate "don't nag when we can't tell" silence.

- **A Postgres connection failure at boot named the wrong subsystem and none of
  the fix.** `pgxpool.NewWithConfig` does not dial — the pool is lazy — so the
  first connection error surfaced inside whichever store constructor happened to
  run first, as a raw driver string:

  ```
  postgres key store: failed to connect to `user=dazyflow database=dazyflow`:
  failed SASL auth: FATAL: password authentication failed for user "dazyflow"
  ```

  repeated every second forever, while `DAZYFLOW_DEV` cheerfully warned that the
  DSN still used the default password — which it did, and which was correct. The
  real cause is that `POSTGRES_PASSWORD` applies only when the `pgdata` volume is
  first created, and Compose names volumes after the project, which defaults to
  the **directory name**. So a fresh clone into another directory called
  `dazyflow` adopts the previous install's volume and is refused by a password
  set months earlier, in a `.env` that no longer exists.

  `dzd` now dials once, up front, and translates the two failures this stack
  actually produces — `28P01` (bad password) and `3D000` (missing database) —
  into the volume-lifecycle explanation and the command that resolves it. The
  README carries the same note.

### Fixed

- **`npm ci` could not install the web dependencies at all, so CI was red and
  the Docker build failed.** The npm-packages bump moved `typescript` to
  `^7.0.2` while leaving `typescript-eslint` at `^8`, whose peer range is
  `>=4.8.4 <6.1.0` — and no `typescript-eslint` release supports TypeScript 7
  yet. npm refuses the graph outright:

  ```
  npm error ERESOLVE could not resolve
  npm error While resolving: typescript-eslint@8.68.0
  npm error Found: typescript@7.0.2
  ```

  Both the *Web unit tests and build* and *Docs site* CI jobs died on `npm ci`,
  and `docker compose up -d` failed the same way at `RUN npm ci`. It was
  invisible locally because an existing `web/node_modules` still held the older
  TypeScript, so `tsc`, the tests and the build all ran against a tree npm
  could no longer reproduce.

  TypeScript is pinned to `^6.0.3` — the newest release that satisfies every
  peer in the graph. Reinstalling then surfaced two genuine breakages from the
  React 19 types that the stale tree had hidden: the dropped no-argument
  `useRef` overload (`docs/CodeBlock.tsx`) and the removed global `JSX`
  namespace (`ErrorBoundary.test.tsx`). Worth revisiting TypeScript 7 once
  `typescript-eslint` ships support for it.

- **`engine/jobstore`'s Postgres conformance suite failed at random.** It
  `TRUNCATE`s `jobs` before each subtest, and `daemon`'s pgstores suite
  truncates the same table in the same shared `DAZYFLOW_TEST_DB` — with
  `go test ./...` running the two packages' binaries concurrently, daemon's
  wipe could land between an enqueue here and the read that followed it. The
  symptom was `park = false, <nil>; want true, nil` in `SetGraphRunParked`: an
  update that matched zero rows because the row had been deleted underneath it.
  It never reproduced with the package run on its own. The package now derives
  a database of its own from `DAZYFLOW_TEST_DB`, which is safe because
  `OpenPostgres` applies its schema itself.

- **A flaky editor autosave test failed only under parallel workers.** The save
  assertions used testing-library's `waitFor`, which polls on real time, while
  the tests run on fake timers with `shouldAdvanceTime` — so the 3s autosave
  debounce needs ~3s of wall time to fire, three times `waitFor`'s 1s default.
  It normally passed because the explicit `advanceTimersByTimeAsync(3000)` got
  there first; when parallel workers contend for the CPU the initial render
  lands after that advance, the debounce starts late, and the wait gave up
  before the save. All eight sites now use `vi.waitFor`, which drives the fake
  clock as it polls, so the result no longer depends on machine load.

- **`docker compose up -d` could not work on a fresh clone.** The base
  `docker-compose.yml` started the bundled Postgres with `ssl=on` pointing at
  `/certs/server.crt`, and mounted `./certs`. But `certs/` is gitignored — it is
  per-host key material — so a clone has no cert to serve, Postgres exited with
  `FATAL: could not load server certificate file`, and `dzd` never got past its
  `depends_on: service_healthy` wait. The README's three-command quick start
  failed at the third command for everyone: `dependency failed to start:
  container dazyflow-postgres-1 is unhealthy`.

  It worked on machines that happened to have a `docker-compose.override.yml`
  turning `ssl=on` back off — also gitignored, so it existed only where someone
  had already written one, which is exactly the population that could not
  reproduce the bug.

  `ssl=on` and the `./certs` mount moved to `docker-compose.prod.yml`, next to
  the cert-generation steps in docs/DEPLOY.md. The base file now leaves the
  image default (SSL off), which is what its own default DSN (`sslmode=disable`)
  already expected, so a clean checkout boots on `cp .env.example .env &&
  docker compose up -d` with nothing else. No override file needed; a durable
  deployment merges the overlay and switches the DSN to `sslmode=require`.

- **A failed sign-in blamed the password when the real cause was
  `DAZYFLOW_WEB_ORIGIN`.** Change `DAZYFLOW_PORT` and the browser loads the UI
  from an origin the daemon doesn't allow, so every sign-in POST is refused as
  CSRF (`csrf_origin`) whatever is typed. `explainApiError` checked the sign-in
  surface before the structured code map, so that 403 rendered as *"That email
  or password isn't right"* — sending a self-hoster hunting through credentials
  for a problem that lives in `.env`.

  Deployment-configuration codes now resolve ahead of any surface-specific
  reading of the status, and sign-in gets its own message naming the variable
  and the port requirement. `.env.example` cross-references `DAZYFLOW_PORT` and
  `DAZYFLOW_WEB_ORIGIN` in both directions.

### Changed

- **The web UI is published on `localhost:8642` instead of `localhost:8080`.**
  8080 is the most contended port on a developer machine — Syncthing, Tomcat,
  Jenkins and every ad-hoc `serve -p 8080` want it — and when it is taken the
  quick start dies at `docker compose up -d` with `failed to bind host port
  127.0.0.1:8080/tcp: address already in use`, a message that says nothing
  about what to change. 8642 is unassigned by IANA, absent from browsers'
  blocked-port lists, and easy to remember (descending evens).

  Only the **published** port moved. The daemon still listens on `:8080`
  inside the container and on a host install (`DAZYFLOW_HTTP` is unchanged),
  so the compose healthcheck, `deploy/Caddyfile`, the k8s `containerPort` and
  any existing reverse-proxy config keep working untouched. `DAZYFLOW_PORT`
  and `DAZYFLOW_WEB_ORIGIN` default to 8642 in step, `make dev` serves there,
  the k8s port-forward example uses `8642:8080`, and `dz-mcp` defaults
  `DAZYFLOW_URL` to it.

  **Upgrading an existing install:** nothing breaks — an `.env` that already
  pins `DAZYFLOW_PORT` still wins. If you relied on the old default, either
  set `DAZYFLOW_PORT=8080` and `DAZYFLOW_WEB_ORIGIN=http://localhost:8080`
  explicitly, or update the bookmark and any proxy pointing at the host port.

- **`make check` and `make ci` now say when they skipped a gated test suite,
  and `make test-db` wires one up.** The Postgres-gated Go tests (~128 cases
  across `auth`, `daemon`, `daemon/support`, `engine/jobstore`) and the
  MySQL-backed `drops/db` tests (18) skip *silently* without their DSN env
  vars. CI sets all three and has both service containers, so `ci` — described
  as "the full CI mirror" — was quietly a much weaker gate locally: a green run
  covered about 146 fewer cases than CI. Both targets now print what they
  skipped, and `make test-db` starts the bundled Postgres, creates
  `dazyflow_test` and prints the exports to paste.

- **The bundled Postgres is published on `127.0.0.1:5442` instead of
  `127.0.0.1:5432`, and the port is now configurable.** It was hardcoded, so a
  developer who already had a Postgres on the standard port could not start the
  stack at all — `Bind for 127.0.0.1:5432 failed: port is already allocated`
  aborts every service, not just the database. New `DAZYFLOW_PG_PORT` and
  `DAZYFLOW_PG_BIND_ADDR` control it, and `make dev` rewrites its DSN to follow
  whatever they say.

  As with the HTTP port, only the **host** side moved: the container still
  listens on 5432 and the daemon still reaches it as `postgres:5432` over the
  compose network, so nothing inside the stack changes.

  **Upgrading:** `psql`, `pg_dump` and any backup script pointing at
  `localhost:5432` need either the new port or `DAZYFLOW_PG_PORT=5432` in
  `.env` to keep the old one. CI's own Postgres service container is
  unaffected.

- **README:** "Running it for real" listed two required values; it is three —
  the DSN must also say `sslmode=require`, which needs a Postgres cert. That
  step, previously only in docs/DEPLOY.md, is now in the README with the
  commands. Also: the quick start pointed at a **Templates** nav item that no
  longer exists (it folded into **New flow → From a template**), and there is a
  note for the `database "dazyflow" does not exist` state a half-initialised
  `pgdata` volume leaves behind after a failed first boot.

## [0.28.5] - 2026-09-01

### Fixed

- **Re-running a webhook- or form-triggered run started it with no data.**
  Replay on the run page submitted the FLOW again (the same call the editor's
  Run button makes), and a flow that starts from an inbound delivery begins at a
  step whose data arrived with that request — nothing re-derives it, so the
  re-run died immediately on `no_trigger_data`: *"nothing was sent to this
  flow"*. The delivery was never lost, though: the trigger path pre-completes
  the trigger step with the request's body and headers, so the original run's
  node record still holds the exact payload.

  Replay now goes through the run's own endpoint, `POST
  /api/v1/me/runs/{run_id}/replay`, which re-seeds every live inbound-event
  trigger step (webhook, hosted form, and the provider events — Slack mention,
  GitHub push, Stripe payment, Home Assistant state change) with what that step
  received the first time, then runs every other step from scratch as before.
  The flow definition used is the current one, so "I fixed the flow, put that
  payload through it again" works.

  Three cases are refused up front instead of burning a run that is certain to
  do nothing useful, each with its own message: a run that never received a
  delivery (it was started by hand — press Run, or use Test event in the
  editor), a delivery whose trigger step has since been replaced, and a trigger
  step that is now turned off (seeding bypasses the worker, so a paused step
  must not be fed).

  Also on the run page: a refused or failed Replay/Retry/Stop no longer
  replaces the whole page with "Run not found" — the message appears under the
  buttons with the run still on screen.

## [0.28.4] - 2026-09-01

### Fixed

- **`daemon/mailer.go` was not gofmt-clean, so `fmt-check` failed CI on 0.28.3.**
  Lifting `newMessageID` out to `internal/smtputil` left a blank line at the end
  of the file. Formatting only; no behaviour change.

## [0.28.3] - 2026-09-01

### Fixed

- **The Email (SMTP) step's mail arrived twice.** Its messages carried no
  `Date` and no `Message-ID`. Both are required by RFC 5322, and the second is
  what every downstream MTA uses to collapse a re-queued submission into one
  delivery — so any transient resend along the way (a timeout on the `250`,
  greylisting, a relay re-queue) landed as a second copy with nothing anywhere
  able to recognise it as the same message. Relays that mint their own fresh ID
  per attempt made the two copies look like two different mails to every later
  hop and to the recipient's client.

  The step itself only ever sent once — the node executes once per run (verified
  end-to-end through the worker and dispatcher against a scripted SMTP server),
  and `RetryNever` plus `DedupeWrites` already covered the retry and
  expired-lease paths. The duplication happened downstream, and the missing
  header is what let it through.

  `daemon`'s own transactional Mailer already stamped both headers for exactly
  this reason, and the email-template test send inherited them; the tenant-facing
  drop was the one raw-SMTP path that never got the fix. `newMessageID` has moved
  to `internal/smtputil` as `NewMessageID` (alongside `DateHeader`) so all three
  senders share it, the way they already share `SplitSender` — and it now takes
  the display-name form too, since a `>` left in the domain is a malformed header
  on relays that validate one. The Gmail step still omits both headers on
  purpose: Google's API stamps them server-side.

- **An address in both To and CC was mailed twice.** The Email step concatenated
  To, CC and BCC into the SMTP envelope without de-duplicating, so a server that
  doesn't collapse repeated `RCPT TO` itself delivered a copy per mention.
  Recipients are now de-duplicated on the parsed address (case-insensitively,
  `Ada <a@x.test>` and `a@x.test` being one person), keeping the first mention
  and its spelling. The address headers are untouched — they still show who was
  visibly addressed in which field.

## [0.28.2] - 2026-09-01

### Changed

- **A pushed version tag is now the release, and the only thing that deploys.**
  CI gained `push: tags: ["[0-9]+.[0-9]+.[0-9]+"]`; that run publishes to ghcr
  and calls the deploy webhook. A push to master, a pull request and a manual
  dispatch cannot reach production — dispatching with `publish=true` re-publishes
  images and stops, so re-cutting an image is never one wrong checkbox away from
  shipping it. The `deploy` input is gone rather than left as a control that no
  longer controls anything. Pre-release tags do not match the pattern, so they
  neither move `:latest` nor deploy, matching what `LATEST_TAG` already did.
  Tag refs are also exempted from `cancel-in-progress`, alongside dispatches.

### Fixed

- **`tests/e2e/ap-invoice/run.sh` waited on a fixed `sleep 0.5` after firing each
  webhook.** The trigger returns as soon as the run is ENQUEUED, so half a second
  was a bet that the whole graph — HTTP fetch, branch, file_write — finished in
  that time. It held locally and lost on CI, where it surfaced as
  `ls: cannot access .../archive/: No such file or directory`. It now polls each
  run to a terminal state. The diagnostics below it also gained `|| true`: under
  `set -euo pipefail` the missing directory killed the script AT A DEBUG LINE,
  before the assertions that would have named which invoice failed to archive.

## [0.28.1] - 2026-09-01

### Changed

- **`examples/` is now `tests/e2e/`.** The three pipelines were never a demo
  gallery anyone browsed — they are the only thing driving dzd end-to-end
  through its own CLI and HTTP surfaces, `engine/mcp` builds
  `mcp-pipeline/server` as its test fixture, and CI runs all three run.sh
  scripts. Sitting at the top level as `examples/` is what let two of them rot
  unnoticed. Their READMEs still read as worked examples; nothing about what
  they exercise changed.

- **Root-level `SCENARIOS.md` and `tests/scenarios.md` now sit with the graphs
  that back them**, as `tests/usecases/README.md` and
  `tests/scenarios/README.md`. Each doc's verdicts are backed by the `NN-*.json`
  graphs in the directory it now lives in, and the two were siblings filed in
  different places. `daemon/flowgen_eval_test.go` derives the corpus path from
  `referenceDir` rather than naming it twice.

- **`.nvmrc` moved to `web/.nvmrc`.** Node exists in this repo only for the web
  app; at the root it implied the whole repository was a Node project. CI reads
  it via `node-version-file`, and `nvm` still resolves it from `web/`.

- **README rewritten to lead with trying it, not operating it.** The old page
  opened with the fail-closed guard: five careful edits to a 598-line `.env`
  before anything ran, with the `DAZYFLOW_DEV=1` shortcut buried six paragraphs
  down as a warning. It now opens with a three-command trial on that shortcut
  and the seeded `test@example.com` sign-in, then a "what you can build" section
  naming the templates and linking the scenario corpus, and only then the
  production setup — which is unchanged and still says the same things. Also
  states the Nordic/EU connector angle, which the page never mentioned. A
  screenshot of the canvas is still missing and marked TODO in the file.

- **CI image builds are cached and no longer a serial tail.** The `images` job
  gated on `needs: [test, web, docs, security]`, so a full from-scratch Go build
  plus two Vite builds ran *after* everything else finished — and neither
  Dockerfile's `--mount=type=cache` survived between runs, because a BuildKit
  cache mount is local to the daemon and a hosted runner starts empty. `images`
  now has no `needs` and builds in parallel through buildx with the GitHub
  Actions cache backend (separate `dzd` / `docs` scopes so the two images stop
  evicting each other, `mode=max` to keep the npm-ci and go-build stages).
  Publishing moved to a new `publish` job that keeps the old gate — nothing
  reaches ghcr.io until the suites are green — and rebuilds from that cache
  before pushing. Stamping rules live only in `images` now and reach `publish`
  through job outputs. Tag behaviour is unchanged: `:VERSION` always, `:latest`
  only off a tag.

- **The Go job is sharded and the end-to-end scripts have their own job.** One
  `test` job ran gofmt, env and link checks, a build, the whole race suite, vet,
  three end-to-end shell pipelines and four more gates in sequence. It is now
  three jobs that run in parallel: `gates` (the checks that answer in seconds,
  plus the single `go build` and `go vet`), `test` (a four-way matrix over the
  race suite — daemon, drops, engine+tests, and the complement, which is
  dominated by `auth` at ~350s of bcrypt), and `e2e` (the three run.sh
  pipelines, which need only Postgres and a Go toolchain). The `rest` shard is
  expressed as the complement of the named ones so a new top-level package is
  covered automatically rather than silently going unrun; the four shards were
  verified to partition `go list ./...` exactly. `fail-fast: false` keeps one
  broken shard from hiding the others. `publish` now gates on all of them.

- **bcrypt no longer runs at production cost under `go test`.** `auth` spent
  347s and `daemon` 858s of every CI run on key derivation — 76% of the whole
  race suite — because `PasswordHashCost = 12` is a quarter-second per hash by
  design and the sign-in, signup, TOTP and password-reset suites mint and verify
  thousands of them. New `activeHashCost` returns `bcrypt.MinCost + 1` when
  `testing.Testing()` reports a test binary and `PasswordHashCost` otherwise;
  recovery codes get the same treatment against their own `bcrypt.DefaultCost`.
  Because `testing.Testing()` is fixed by the linker when `go test` builds the
  binary, a released `dzd` cannot be talked down to the cheap cost at runtime —
  verified by hashing from a normal build and reading the cost back. Measured:
  **auth 347s → 19s, daemon 858s → 82s.**

  `timingDummyHash` now mints at the same cost as a real hash instead of
  `bcrypt.DefaultCost`. It exists to make an unknown-account compare cost what a
  real one costs, and at cost 10 against real hashes at 12 it was only paying a
  quarter of it.

### Fixed

- **A race in `TestRunStatus_ParksAndResumes`.** It waited for the RUN to report
  `awaiting` — which happens the moment the FIRST of two gates parks — and then
  read both NODE records immediately, so the second gate was still `running`
  when it was asserted on. It now polls each gate. Latent for as long as it has
  existed; the bcrypt change above made the suite fast enough to start losing
  the race.

- **Outbound pacing leaked between tests in `drops/net` and `drops/geo`.** A
  stubbed 429/503 with no `Retry-After` parks its host on the limiter's
  `fallbackCooldown` (5s), and because the limiter is process-wide the next test
  to reach 127.0.0.1 waited it out. `TestSiteCheck_FiresOnTransitionsOnly` spent
  10s asleep; `TestPhoton_ForwardSuccess` and `TestGeoHTTPFailure_StatusCodes`
  5s each. Both packages now disable pacing in `TestMain` — the limiter's own
  behaviour is still covered, because every test in `ratelimit_test.go` drives
  an isolated `newTestLimiter` rather than the global.

  `TestCov_SetEgressRateLimitTunes` was the other half of it. It saved and
  restored `egressLimit`, but `SetEgressRateLimit` mutates the limiter in place,
  so restoring the pointer restored nothing and its closing retune to 600/min
  stayed in force for every test declared after it. It now re-disables pacing in
  its cleanup. Measured: **drops/geo 11.1s → 1.1s, drops/net 15.8s → 4.8s**, and
  the drops tree 151.7s → 124.7s.

- **The five `TestAllDrops_*` contract sprays run their per-drop subtests in
  parallel.** 163 drops each, run one at a time, and several spend their run
  asleep against the 1500ms watchdog budget rather than on CPU — `delay` cost
  1.5s in three separate tests. The precondition was isolation, so that came
  first: the shared `ws`/`scratch` temp dirs are now per-subtest, and
  `jobWithValue` seeds tenant/graph/node from the drop id instead of every job
  carrying the same `fuzz-tenant`/`fuzz-graph`/`fuzz-node`. That identity was
  harmless while the subtests were serial and would not have been once they
  overlap — any drop keying state by it (a watch marker, a dedupe cursor, a poll
  marker) would have had two subtests writing one key. Subtest counts are
  unchanged at 163 per test, so no coverage moved. Measured: **the drops package
  28.0s → 17.3s standalone**, stable over five race runs with no data races.

### Removed

- **Seven Markdown files, ~2,600 lines.** `REVIEW.md` and
  `tests/journey/WALKTHROUGH.md` (plus 2.9 MB of screenshots) were one-off
  session artifacts; `TODO.md` was a backlog that belongs in issues;
  `docs/support-tickets-design.md` and `docs/own-service-steps-design.md`
  described features that shipped, so the code and its tests are the record now;
  `docs/mcp-on-runners-design.md` and `engine/artifactstore/DESIGN.md` described
  features that were never built. Comments in fifteen files that pointed at them
  now stand on their own.

### Fixed

- **Nine dangling links into `docs/decisions/`, a directory that was never
  created.** The commit that restructured `TODO.md` deleted 249 lines of
  retrospective reasoning instead of moving them; a later entry in this section
  claimed the records had been restored, and they had not. CI has been red on
  `make links-check` since that check landed. The dangling pointers are gone
  from `CONTRIBUTING.md`, the pull-request template, `core/flowstatus.go`,
  `daemon/timeout.go`, `scripts/check-links.sh` and `.github/workflows/ci.yml`;
  the vocabulary rule the one required-reading link carried (`step` in prose,
  `drop` in code) is stated directly in `CONTRIBUTING.md`.

- **Nothing checked Markdown links outside `docs/guide/`.** `make links-check`
  (`scripts/check-links.sh`) now walks every relative link in every tracked
  `.md` file, in `make check` and in CI. It reads `--cached --others
  --exclude-standard`, not just the index — a brand-new document is exactly the
  one whose links have never been checked — and blanks fenced blocks and code
  spans first, so a file that legitimately *shows* Markdown is not a false
  positive.

- **`docs/DEPLOY.md` told operators `make docs-dev` runs VitePress.** It runs the
  Vite dev server; VitePress is not in the tree. The third stale reference to it,
  after `.dockerignore` and `deploy/Caddyfile`.

- **`cmd/docsgen` escaped `{{` as `&#123;&#123;` for a renderer that is gone.** A
  Vue interpolation guard from the VitePress era, applied to 24 strings of
  Go-template prose. The `<` escape stays: `&lt;place>` is also correct in a
  renderer that passes HTML through.

- **Two of the three shipped examples could not run, and CI only ran the third.**
  `examples/mcp-pipeline/run.sh` and `examples/ap-invoice/run.sh` had been
  failing at boot on requirements that landed after they were written — a missing
  `DAZYFLOW_POSTGRES_DSN`, a `sleep` where a Postgres-backed boot needs a poll, a
  missing `DAZYFLOW_DEV=1`, a stale sandbox path, a missing publish step, and a
  `flow_id` that must be the percent-encoded `tenant/workspace/id` triple. Both
  now pass every assertion, and CI runs all three examples.

- **`go build ./...` walked into `web/node_modules`.** `flatted` ships a Go port
  beside its JavaScript, so build, vet, test and `gofmt -l .` descended into a
  directory this project does not own. `go.mod` now carries an `ignore`
  directive, and the `fmt` / `fmt-check` targets operate on `git ls-files
  '*.go'` since gofmt does not read `go.mod`.

- **A leaked socket per HTTP error in the runner agent.** `urllib`'s `HTTPError`
  *is* the response object; `post()` drained the body but never closed it, so a
  recurring 401 on the poll loop accumulated sockets until GC noticed.

- **Nine Go files were not gofmt-clean.** Struct-tag and map-literal
  misalignment plus one out-of-order import.

### Added

- **Issue templates.** A bug report that asks for the version and how it's being
  run, and a feature/connector request that asks for the job in the reporter's
  own words plus how the service authenticates — the main cost driver.
  `config.yml` routes vulnerabilities to Private Vulnerability Reporting.

- **An index for `docs/guide/`.** `README.md` sends readers to that directory,
  where a repository host shows an alphabetical file list — so "Connect an app"
  came before "Build your first flow" and the intended reading order existed
  only in the SPA's `NAV`. The index carries that order.

- **Tests for `cmd/docsgen`.** It renders the entire public step catalog from the
  drop manifests, so a defect there is wrong documentation for every step at
  once. Covers the text-mangling helpers plus an end-to-end run over the real
  registry asserting deterministic output.

- **CI runs on push and pull request.** The workflow was `workflow_dispatch:`
  only, so every gate it defines fired only when someone pressed a button.
  Publishing and deploying stay dispatch-gated.

- **Four new gates.** `make fmt-check`; `make env-check` (every `DAZYFLOW_*` knob
  the daemon reads must appear in `.env.example`); `npm audit
  --audit-level=high --omit=dev`; and a guide cross-link check in
  `web/src/docs/content.test.ts`.

- **ESLint, limited to the React hooks rules.** Deliberately not the recommended
  set: `tsc --strict` and the nine `check-*.mjs` guards already cover what a
  general ruleset would flag. The hooks rules catch what nothing else can — a
  stale closure is well-typed and silent. Found a `rules-of-hooks` violation (a
  plain async action named `useTemplate`, now `applyTemplate`), a disable comment
  for a plugin that was never configured, and two directives that suppressed
  nothing.

- **`CONTRIBUTING.md` and a pull-request template.** The rules the gates enforce
  existed only in Makefile help text, `web/README.md` and CI comments.

### Changed

- **`.env.example` documents fifteen knobs it had never mentioned**, including
  `DAZYFLOW_TRUSTED_PROXIES`. Without that one `X-Forwarded-For` is never
  honoured, so behind the reverse proxy the README tells you to deploy, every
  request arrives wearing the proxy's address and the whole internet shares one
  rate-limit bucket. Also added: the three `DAZYFLOW_EGRESS_*` knobs, the three
  missing `DAZYFLOW_FREE_*` caps, `DAZYFLOW_SHELL_ENV_ALLOW`,
  `DAZYFLOW_OIDC_ALLOWED_TENANTS`, `DAZYFLOW_SUPPORT_INBOX`,
  `DAZYFLOW_SUPPORT_RETENTION`, `DAZYFLOW_RUNNER_TASK_RETENTION`,
  `DAZYFLOW_MAX_ROWS`, `DAZYFLOW_PROMOTE_INTERVAL` and
  `DAZYFLOW_FAILURE_EMAIL_WINDOW`.

- **Guide cross-links now resolve on the repository host as well as in the docs
  SPA.** All forty were extensionless SPA routes — correct in the SPA, dead on
  GitHub, which is where the README's `docs/guide` pointer lands a reader.
  Siblings are now `./slug.md`; the generated step catalog, which has no markdown
  source in the repo, is a full `https://docs.dazyflow.app/...` URL.

- **`make check` matches what CI enforces**, and says what it does not cover. It
  previously skipped catalogue freshness, formatting and the config catalogue, so
  the pre-push gate could pass on a change CI would reject.

- **The web test files are type-checked.** `tsconfig.json` excludes
  `*.test.ts(x)`, which also meant `tsc -b`, `npm run typecheck` and CI's build
  all skipped 123 test files. A `tsconfig.test.json` covers them and found eleven
  real errors — a mock whose signature had drifted from `api.whoami`, eight
  `never[]` spreads, an unused import and a stale `@ts-expect-error`.

- **A failed deploy trigger now says what to check.** The step ended in `curl
  -f`, whose entire report is `curl: (22) ... error: 401` — and `/trigger`
  answers every pre-authentication failure with an identical 401 on purpose, so
  four causes (wrong key, wrong tenant/workspace/flow, an unpublished flow, a
  webhook step with no secrets) look the same. The call now keeps the body and
  status, prints them, and on a 401 lists the four causes plus the two API calls
  that separate them. Note that `/trigger` authenticates against the
  **published** revision while the editor shows the draft, so a key rotated and
  left unpublished reads as correct in the UI and still 401s. A 403 is past auth
  and carries a machine-readable code, so it is reported verbatim. Both secrets
  are shape-checked before a request is spent — a key arriving as `"abc"` or as
  `DAZYFLOW_WEBHOOK_KEY=abc` is otherwise indistinguishable from one that no
  longer matches; whitespace is trimmed rather than rejected, and neither check
  echoes a value.

## [0.28.0] - 2026-08-31

### Security

- **There is now a documented way to report a vulnerability, and it no longer
  dead-ends.** `docs/SECURITY-SLA.md` told researchers to report privately and
  pointed at `SECURITY.md` for "the operational contact" — but `SECURITY.md` was
  132 lines of master-key runbook with no reporting section and no contact
  anywhere in it, so the documented path led nowhere and the only remaining
  option was the public issue the same paragraph forbids. That mattered little
  while the repository was private and matters a great deal as it goes public,
  since GitHub surfaces `SECURITY.md` as the repository's security policy. It
  now opens with a reporting section pointing at GitHub's Private Vulnerability
  Reporting, says what to include, and states where the acknowledgement and fix
  windows live — alongside a 7-day critical/high commitment that previously had
  no inbox behind it.

### Added

- **Dependabot keeps Go modules, npm packages, GitHub Actions and Docker base
  images current** (`.github/dependabot.yml`), closing the supply-chain gap
  `docs/COMPLIANCE.md` §3 had listed as open — there was no bot to wire up on
  builds.sr.ht. Version bumps are grouped into one pull request per ecosystem on
  a monthly cadence, because the alternative is a weekly drip of
  single-dependency PRs each costing a full CI run to review. Advisory-driven
  security updates are a separate mechanism that ignores that schedule, so the
  monthly cadence does not slow a CVE fix; it needs the repository setting
  enabled, which the compliance gap now records as the remaining operator step.
  Base images are worth covering on their own: a stale `node:22-alpine` carries
  OS-level CVEs that `govulncheck`, which only sees Go code, cannot detect.

### Changed

- **CI moved to GitHub Actions, and release images now come from GitHub
  Container Registry.** The `builds.sr.ht` manifest (`.builds/archlinux.yml`)
  is gone, replaced by `.github/workflows/ci.yml`, which runs the same gates —
  the Go suite under the race detector, the runner-agent unittests, `go vet`,
  the dzctl end-to-end example, the catalogue and changelog checks, the
  frontend vitest suite and build, the docs site build, and both govulncheck
  passes with the SBOM. Postgres and MariaDB are service containers rather than
  servers stood up by hand, so the database-gated tests still run rather than
  silently skipping. The 4 GiB swapfile the old manifest needed for source-mode
  govulncheck is dropped: it existed because the same VM was still hosting both
  database daemons, which is not true of a hosted runner.

  Images publish to `ghcr.io/OWNER/dazyflow-dzd` and `-docs` instead of the
  self-hosted `registry:2` container that used to run beside the app behind
  Caddy at `registry.dazyflow.app`. That removes a TLS front door, a basic-auth
  credential pair, and the `registry garbage-collect` chore that kept a 50 GB
  droplet from filling up; `GITHUB_TOKEN` authenticates the push. CI is
  dispatch-only for now, with publishing and deploying as separate opt-in
  inputs, so a build never ships anything unless it was asked to.

- **Self-hosters on the production overlay: `REGISTRY_HOST`, `REGISTRY_USER`
  and `REGISTRY_PASSWORD_HASH` are gone; set `GHCR_OWNER` instead.** It is the
  GitHub account or organisation that owns the packages, lower-case, since ghcr
  rejects an uppercase path segment. It is deliberately required rather than
  defaulted — an empty value would resolve to `ghcr.io//dazyflow-dzd` and fail
  at pull time with a registry error that says nothing about the real cause. A
  ghcr package is private by default and an anonymous pull of one fails with a
  401 that reads like "not found", so either make both packages public or run
  `docker login ghcr.io` once on the host with a token holding `read:packages`.
  Leaving the old `REGISTRY_*` variables set does nothing except mislead the
  next person to read the file.

  `make upgrade` keys its pull-vs-build decision on the `caddy` service now.
  It used to key on `registry`, and that service no longer exists — a condition
  still naming it would have taken the else branch and set a 1-vCPU droplet
  compiling Go and two Vite builds, which is the exact failure the prebuilt
  image path exists to prevent.

## [0.27.10] - 2026-08-31

### Fixed

- **On a single-host deploy, every action failed the moment you signed in.**
  When `dzd` serves the web bundle itself (`DAZYFLOW_WEB_DIST`, the shape
  `docs/DEPLOY.md` describes), its own public origin is by definition a browser
  origin performing cookie-authenticated writes — but the CSRF allow-list was
  built from `DAZYFLOW_WEB_ORIGIN` alone, so that origin was not on it. The
  failure hid itself well: sign-up and sign-in carry no session cookie yet and
  pass the origin check untouched, so the daemon looked healthy right up to the
  first authenticated POST, which answered 403 with the server's own diagnosis
  rendered verbatim in the UI. `DAZYFLOW_PUBLIC_BASE_URL` is now trusted
  automatically whenever we serve the bundle, and the daemon logs that it added
  it. A separately-hosted UI still has to declare itself via
  `DAZYFLOW_WEB_ORIGIN`; that is the case the allow-list is for.

- **"See a flow run" reported "Not signed in." to people who were signed in.**
  The Welcome screen's primary action deep-links to a template that copies
  itself on arrival. That auto-start waited for the template list and the
  session, but not for the workspace — and the template index is a static file
  that resolves long before the identity bootstrap does. So on a cold load it
  fired with no workspace yet and failed the one check that had no wording of
  its own, telling a brand-new user they were not signed in while their email
  sat in the sidebar. It also consumed the deep link before failing, so
  reloading could not retry. The guard now waits for the workspace, the
  deep link is only spent once the copy actually starts, and "signed in, but
  the workspace is still opening" has its own sentence.

- **Connect on an app the server has no credentials for threw you out of
  Dazyflow.** The Apps page read "this provider is missing from the list"
  as "you have not connected it yet", so it offered a live Connect button that
  navigated the whole tab to an authorize endpoint which answered 404 as a raw
  JSON body: the application simply gone, no way back. The template gallery
  already knew the difference and said so in a sentence; the app page now uses
  the same signal and explains instead of offering.

- **`/signin` and `/signup` answered "page not found" to anyone with a
  session.** Both routes live only in the signed-out tree, so with a session in
  hand they fell through to the authenticated catch-all — including from the
  sign-up form's own "Already have an account? Sign in" link, and from any
  bookmark. They now redirect into the app. Deliberately a redirect and not a
  sign-out: a successful sign-up sets the session while the URL is still
  `/signup`, so tearing it down there would log people out the instant they
  created an account.

- **Collections showed timestamps as raw UTC instants.** A row saved by
  `Collections · Save rows` rendered its `saved_at` as `2026-08-31T07:21:54Z`,
  leaving the reader to do the timezone arithmetic. Instant-shaped values now
  render in the viewer's local time, like every other timestamp in the product.
  Only the display changes: the CSV export still carries the stored value, and
  column headings still show the collection's own keys, because those are names
  people match against their flow and their spreadsheet.

- **The sign-in and sign-up screens sent their logo to `dazyflow.app`.** On a
  self-hosted install the first clickable thing on the first screen left the
  installation entirely. It now points at the deployment's own root, which the
  daemon already auth-gates: the marketing landing where one is served, this
  page where it is not. Those screens also gained Privacy and Terms links —
  it is where someone types a password, and nothing there named the terms that
  govern it.

- **A template needing two apps said "which isn't set up".** The
  admin-blocked notice and its tooltip had no plural form, in either language.

### Changed

- **The Apps page leads with the apps people recognise.** It was sorted purely
  alphabetically, so a Swedish SMS gateway opened the page ahead of Gmail,
  Slack and Google. Everyday apps come first now; everything else keeps its
  alphabetical place behind them. Adding a connector does not require touching
  that list.

- **An app's steps read as descriptions, not identifiers.** Each step printed
  its raw id (`gmail_get_attachments`, …) under its name, on a page written for
  someone deciding whether an app does what they need. The ids moved into the
  "Inputs, outputs and settings" disclosure that was already there, one click
  away for whoever wants to grep for them.

- **"Built-in" describes itself in words.** Its card is the largest on the Apps
  page at 57 steps, and it opened with `split_rows`, `await_approval`, "file
  I/O" and "the transform family". It now says what those things do.

- **A hosted deployment no longer tells you to go and find the server
  administrator.** Templates blocked on an unconfigured app said the app was
  not set up "on this Dazyflow server" and that "whoever runs the server has to
  enable it first" — correct for a self-host, and a dead end on the hosted
  product where there is no such person for the customer to find.

- **The hosted-form templates label their trigger "Form".** *Web form →
  Collection* and its siblings put a step called *Webhook*, with ports named
  *Body* and *Headers*, on the canvas — contradicting both the template's name
  and its own inspector, which already offered "Host a form for me".

- **The verify-email banner says what you lose by ignoring it.** It led with
  invitations, which means nothing to someone who is not inviting anybody, and
  never mentioned that unverified means we cannot reach you about a run that
  needs approving or one that failed overnight.

- **The signed-out screens say "Dazyflow".** They carried the operating
  company's name in the footer, which appears nowhere on the site the visitor
  has just come from. An unfamiliar name on the password screen reads as a
  wrong turn.

## [0.27.9] - 2026-08-31

### Fixed

- **A Swedish reader met one English paragraph in the middle of a Swedish
  card.** 0.27.8 announced that `Collections · Save rows` stamps a `saved_at`
  column, and the step's own English description gained a sentence saying so —
  but its Swedish translation was never rewritten to match, so the card fell
  back to English for that one step. That fallback is deliberate and it is the
  right call for the reader: every drop translation records a fingerprint of
  the English it was made from, and when the English is reworded the
  fingerprint stops matching, so what renders is accurate new English rather
  than fluent-but-wrong Swedish. What it is not is visible — nothing anywhere
  says a translation went stale, which is how ten of them once sat unnoticed.
  The Swedish now describes the `saved_at` stamp too, and its fingerprint
  matches the English again.

- **The docs guards in the frontend suite had been searching an empty
  directory in CI.** The docs site's content tree is generated — guide pages
  copied from `docs/guide/`, the step catalogue emitted from the live drop
  manifests — and git-ignored, but CI ran the frontend tests BEFORE the task
  that produces it. So on a clean checkout the docs-search tests searched
  nothing and failed with "Nothing matches", which reads as a bug in search
  rather than as absent content, and three tests that need the rendered pages
  quietly skipped. CI now generates the tree before running the suite.

## [0.27.8] - 2026-08-30

### Added

- **Collections stamp every row with when it was saved.** `Collections · Save
  rows` writes a `saved_at` column alongside the row's own columns, so "newest
  first" is a sort you can ask for rather than a column you had to remember to
  add yourself — `Collections · Find rows` takes it as its **Sort by**, and
  `Collections · Query with SQL` can `ORDER BY saved_at DESC`. Rename it with
  **Time column**, or clear that field to turn the stamp off; a row that
  already carries a column of that name keeps its own value rather than being
  overwritten. The behaviour landed in 0.27.6 and the step's own description
  caught up in 0.27.7, but neither release said so and the translated step
  catalogue still described the older behaviour — so this is the first place it
  is actually written down.

## [0.27.7] - 2026-08-30

### Added

- **A guide page for teams, roles and approvals.** All three are primary UI —
  Approvals is a top-level nav entry, roles gate everything an invited person
  can do — and none of them were written down: no guide page, and not one of
  the Glossary's thirty entries. A cold walkthrough had to guess, including at
  the non-obvious and rather good fact that a **Viewer can approve**, so signing
  things off never requires handing someone edit access or your secrets.
  `docs/guide/teams-and-approvals` now covers inviting, the three roles, seats,
  and wiring an approval; Approval, Invitation, Member, Organization, Role and
  Seat join the Glossary.

- **Approval cards show what is being approved.** The `Wait for approval` step
  already stashed the value wired into its `Value` port on the awaiting result,
  next to the `pending_url` and `prompt` the inbox was already reading — it just
  never travelled the last hop. An approver saw `(no prompt — step ID:
  await_approval_1)` and two buttons, which is a hard thing to say yes to, and
  the opposite of the promise that Dazyflow shows you what it is about to do.
  The card now renders the submission, the drafted reply, the refund. Big values
  are held back with a pointer to the run rather than shipped down a list
  endpoint that returns up to 200 rows.

### Fixed

- **A hosted form filled its collection alphabetically.** `buildFormSeed` built
  the body value without `Headers`, so the declared field order was dropped at
  the source even though it was sitting at the call site. A row value carries
  its own column order and a writer only falls back to sorting the keys when
  none is present — so a form asking *Your name, Your email, What you like about
  us* produced a table led by the long free-text answer, while the editor had
  been offering the right order as the columns all along. Extras a caller posted
  (`utm_source` and friends) are listed after the declared fields rather than
  dropped, since headers name exactly the columns that get written. The same
  ordering now reaches approval cards.

- **A 403's real message was replaced with advice to ask an admin.** The invite
  gate answers with the one sentence that unblocks you — *"verify your email
  before inviting others"* — and the client overwrote every refusal with a
  generic *"You don't have permission to do that. Ask an admin if you think you
  should."* Told to the owner of the organization, that names a person who does
  not exist. A 403 carrying a clean, remedy-shaped message now shows it;
  refusals phrased as the permission you lack (`organization:admin required`)
  still get the headline, because those are written for whoever wired the API
  call rather than the person reading the screen.

- **The verification banner claimed an email had been sent, and its resend
  failed silently.** Signup reports `verification_email_sent`, which is false
  whenever the deployment's mailer is unconfigured or failing — so the first
  thing a new owner read was a claim about a message that was never sent. The
  banner now states only what it can stand behind, and reports honestly what
  each resend actually did: the endpoint's three answers (sent, already
  verified, and a 502) were collapsed into "it didn't throw, so say it worked",
  which left the one route out of the invite gate looking like it succeeded
  every time it was pressed.

- **Inviting anyone was impossible when the deployment's mail was broken.**
  The gate refused invitation *creation* on unverified accounts, and a
  configured-but-failing mailer (wrong password, expired token, provider
  blocking) leaves verification active while every send fails — so the owner was
  told to confirm an address via an email that could never arrive. What the gate
  protects is the operator's mailer, so that alone is now withheld: the
  invitation is created and its link handed over to send by hand, which is what
  the invite dialog has always told admins they can do. Creating an
  organization still requires a verified address; that limit is about spinning
  up tenants, not about mail.

- **Every organization got one seat more than its plan allowed.** The owner
  holds a seat without a membership row — ownership is implicit in the home
  tenant — and the seat gate counted rows while the People page correctly added
  the owner back. The two disagreed by exactly one, so a three-seat plan seated
  four before anything refused.

- **Outstanding invitations didn't hold a seat.** The gate compared against
  people who had already joined, so at two of three seats every invitation
  passed the check on its own and the refusal landed on whichever invitee
  clicked second, as "ask an admin to upgrade". The admin saw nothing wrong.
  Creating an invitation now counts the promises already made. Accepting one
  deliberately does not: a stale invitation nobody opened must never keep a real
  person out of a seat that is genuinely free.

- **Two people accepting at once could both take the last seat.** Counting seats
  and then inserting are two steps, and under `READ COMMITTED` both
  transactions read the same free seat before either wrote — there is no row to
  conflict on, because the anomaly is about a row that does not exist yet.
  Seating is now one atomic step: a transaction and a per-tenant advisory lock,
  released on rollback as well as commit, and taken on the tenant so unrelated
  organizations still write in parallel. Updating someone already seated is
  never refused — they occupy a seat already, so a role change must not fail
  because the org is full.

- **The upgrade pitch contradicted the plan table beside it.** "Pro removes the
  monthly run cap and unlocks scheduled & polling triggers" was static copy
  shown whenever an upgrade was possible, while the comparison table below reads
  its Yes/No from the resolved entitlement. On any deployment that does not gate
  polling — every self-host — the pitch claimed to unlock something the table
  correctly showed as already available. It now matches what the reader's own
  plan actually gates.

## [0.27.6] - 2026-08-30

### Fixed

- **`${upstream.…}` and `${trigger.…}` only worked one hop from their source.**
  Template resolution reused the map of DIRECT predecessors that input assembly
  runs on, and inherited a restriction that makes no sense for it. An input port
  is fed by an edge or by nothing; a reference names a node in the run. In
  `webhook → if → email`, the email step could not see the webhook at all:

  - `${upstream.webhook_input_1.body}` failed the node outright.
  - `${trigger.body.version}` was **left in the text and sent**, so a customer
    received a literal `${trigger.body.version}` with nothing failing, logging
    or warning.

  Referenced results are now looked up in the run. Only the nodes a step's own
  params actually name are fetched, so the common case — no cross-references —
  costs nothing and a flow cannot become a scan of its own run by growing. They
  go into the same map the inputs came from, which is safe because every reader
  is a keyed lookup: `AssembleInput` walks edges and looks up `prior[edge.From]`,
  so an entry for a node with no edge here is never consulted (the run inspector
  already relies on this, passing every record in the run to that function).

  An unresolvable `${trigger.…}` is now an error rather than a shrug. The
  original reasoning — "an unknown scheme is not an error" — conflated two
  things: `SubstituteString` leaves an unknown SCHEME alone so arbitrary `${…}`
  text in JSON and shell survives, but `trigger` is a known scheme with an
  owner, so failing to resolve it is a broken reference, exactly as it has
  always been for `upstream`. A manual Run of a webhook flow now stops with "no
  trigger fired in this run — use Send test event" instead of mailing the
  template to a person.

  The bug survived its own test suite because every test put the trigger
  directly in the predecessor map. There is now an end-to-end case that submits
  a graph and reads what the node produced — the unit tests around the new
  helpers all pass with the worker's call to them deleted, which is the same
  gap that produced the bug.


- **The approval email carried the template instead of the question.** It read
  `prompt` from the stored graph node — the text as authored, `${…}` and all —
  while the engine resolves templates into the job before the step runs. The
  `await_approval` step already emits the resolved question on its `prompt`
  output, which is why the Approvals inbox showed it correctly and only the
  email was wrong: one value, two readers, one of them reading the unresolved
  copy. The email now takes it from the parked result, with no fallback to the
  node's params — the port is omitted only when the prompt is empty, so a
  fallback could do nothing but resurrect a template.

## [0.27.5] - 2026-08-30

### Fixed

- **An agent can now see whether the customer read their reply.** "Read by
  customer 14:32" sits under the newest support message in the queue view, or
  "Not read yet" when the customer last opened the ticket before it arrived.
  Answers the question that otherwise costs a follow-up ticket.

  On the newest support message only. The receipt is per THREAD rather than per
  message — it records when the ticket was last opened — so every older reply
  has been read too, but tagging them all turns a signal into wallpaper, and the
  last one is what decides what the agent does next.

  A ticket with no receipt at all shows nothing rather than "Not read yet". No
  receipt means the ticket predates read tracking or was filed through the API,
  and stating the stronger claim there would be a confident guess. No badge
  means no information, not bad news.

  Agent side only, on purpose. The mirror — telling a customer support opened
  their ticket and said nothing — is a stopwatch on the desk rather than an
  answer to a question, so the customer's view keeps stripping that timestamp
  and offers no indicator either. There is deliberately no "delivered" tick:
  the message is in the database or the POST failed, so a delivered indicator
  would be lit the instant a bubble appeared, which is how people learn to
  ignore ticks generally.

- **A support thread could go quiet with nobody told.** Mail went out on every
  reply, which covers the case where someone is paying attention. It did nothing
  for the case where nobody is: a mail missed, filtered, or opened and
  forgotten, after which the customer assumes support is working on it and
  support assumes the customer went away.

  A ticket message left unread past `DAZYFLOW_SUPPORT_NUDGE_AFTER` (default
  24h, `0` to disable) now earns its side a reminder — the customer who never
  opened the answer, or the queue when nobody has looked at a question.

  Read-aware, not just age-based. Opening a thread records a receipt, so
  somebody who has read a message and not replied yet is never chased; that is a
  staffing question, not a notification one. Age is the floor underneath it, so
  a side that has never opened the ticket at all still gets told. Read receipts
  are recorded by an explicit call rather than as a side effect of the GET: the
  thread polls while open and other surfaces prefetch, and "we fetched it" is
  not "a person looked at it".

  Sent once per waiting period. A newer message from the other side starts a new
  one; nothing else does. Terminal tickets are skipped, the sweep is
  leader-gated so a multi-node install sends one reminder rather than one per
  daemon, and there is no startup pass — a restart is not evidence that anyone
  stopped reading, and firing a batch on every deploy is how a restart loop
  becomes a mail storm.

  The rule was wrong on its first draft in a way worth recording: it asked "is
  there a message from the other side you have not read", which reminded support
  about a question they had already answered. Answering is proof of reading, so
  the thread's most recent message decides who is waiting — which also disposes
  of the case that looked like it needed its own rule, replies crossing in
  flight.

- **System notes in a support thread were English in a Swedish UI.** "The
  customer closed this ticket." sat between translated messages, and nothing
  could fix it client-side: the daemon composes those notes as prose and stores
  the sentence, so the only way to translate one was to match on its English
  text.

  Notes now carry a `system_code` alongside the prose. The web renders the
  translation for a code it knows and falls back to `body` otherwise — which
  covers rows written before the field existed, and a daemon newer than the UI.
  The English stays the floor, never the ceiling, and an API reader or an email
  digest still gets a readable sentence.

  One code per sentence, no parameters: "marked resolved" and "marked closed"
  are separate codes rather than one code plus a status argument. Interpolating
  a status label into a sentence is where translations break — Swedish inflects
  around the insertion point — and a flat code is greppable from both sides of
  the stack.

  The customer's own close and reopen also stop talking about them in the third
  person: their thread now reads "You closed this ticket", the same complaint as
  the email address above.

  Two guards keep it fixed. On the Go side an AST walk fails the build if a
  handler writes a system note through the generic append helper, which would
  compile, store perfectly good English, and be untranslatable exactly as
  before. On the web side the code list is read out of `core/ticket.go` rather
  than restated, so a note added in Go fails the test until its copy is written.

- **The support chat read your own email address back at you.** Every message
  you sent was headed with your address rather than "You" — on your own screen,
  above your own words, on every bubble.

  `support.fromYou` was already in the code and already translated in both
  locales; it just sat behind `m.author || t("support.fromYou")`, a fallback
  for a missing author that the server never sends. So the label existed, was
  correct, and had never once rendered.

  It is now chosen by identity rather than by which side of the thread the
  bubble sits on. Those come apart on the agent side, where every support reply
  is on "your" side but only some are yours: labelling a colleague's message
  "You" would misattribute their words. The customer's view is unchanged in the
  one respect that matters there — a support reply still reads "Support", never
  the individual agent.

## [0.27.4] - 2026-08-30

### Fixed

- **Tidy parked loose cards in the first column.** A card with no incoming
  wires — a Text value, an Email address, anything feeding a step from the side
  — landed at the far left however late the step it fed was, with its wire
  dragged across the whole canvas.

  Column assignment was longest-path-from-the-roots alone. Every node starts at
  column 0 and only TARGETS are ever pushed right, so anything with no incoming
  edge simply stayed where it started. It looked like a root; it was a card
  belonging beside the fifth step.

  Tidy now runs a second pass that slides each node right until it sits one
  column left of its nearest consumer. Nearest, not furthest: a card feeding
  both step 1 and step 5 belongs beside step 1, and taking the max would drag
  it past a node it has to precede. A consumer is always at least one column
  right of its producer, so the slide can only move a node right — the first
  pass's left-to-right ordering survives untouched, and every edge still points
  rightward.

  Triggers are exempt. The entry point reads as where the flow begins, so it
  anchors the left edge even when its only wire runs somewhere late.

  The assignment moved to `lib/autoLayout` on its way out of a `setNodes`
  callback, which is what made it testable: ten cases now cover the reported
  shape, the nearest-consumer rule, trigger anchoring, and that a cycle
  terminates rather than spinning.


- **Closing a support ticket asks first.** "Close ticket" sits in a row with
  Claim and Release, directly above the composer, and fired on the first click
  — the requester telling support to stop working, with no step in between.

  It now opens the app's confirm dialog, which also carries the fact that makes
  the decision easy and that nothing on the page reveals: replying reopens the
  ticket. There is no Reopen button to infer that from, so a user who closed by
  accident had no reason to think the thread was recoverable. Not styled as a
  destructive confirm — it is the requester's own tidy-up, and reading as an
  alarm would be its own kind of wrong.


- **"Contact support" on the editor's failure banner left the product.** It
  rendered the operator-configured mailto or URL and nothing else — so the user
  retyped an error the app was displaying two lines above, and support opened a
  mail with no flow, no run and no diagnostics. The surface that fixes all
  three already existed: the run-detail failure banner files a ticket with a
  redacted bundle attached. The editor just never reached it.

  The same words now open that dialog when the deployment has tickets on,
  carrying this flow and — only when the banner is about a failed RUN, since it
  also reports save, permission and config errors — the run that failed. The
  operator's contact stays as the fallback when tickets are off, and when
  neither is configured nothing renders, so no dead affordance appears.

  It arrives populated: subject from the flow, and the message box holding the
  error the user is looking at, editable so their own account of what they were
  doing goes in the same ticket. `ReportProblemModal` grew a `defaultMessage`
  for that.

  Seven tests cover the join rather than the parts — which is where this class
  of bug lives, since every piece of it typechecks while pointing at the wrong
  flow, a stale run, or nothing. One of them was written after the browser
  showed the dialog opening with Send greyed out: the subject prefill needed
  `name || id`, because a flow that was never renamed has only an id.


- **Editor banners covered the flow instead of sitting beside it.** The error,
  the lint warning and the "run finished" toast were an absolute overlay pinned
  at `top: 60px; left: 12px`, up to 700px wide — which is exactly where a
  flow's first nodes are. Several at once buried the trigger and the step after
  it under an opaque block, and only one of the five (the connection hint, at
  six seconds) ever went away on its own; the rest waited to be dismissed, for
  an edit, or to be replaced.

  Being click-through did not help, because the problem was never that you
  couldn't reach the canvas — it was that you couldn't see it.

  The stack is now a docked strip between the toolbar and the canvas, so it
  takes its height out of the flow area and can never overlap a node. It
  collapses to nothing when there is nothing to say (`:empty`), scrolls rather
  than growing past 40vh when several banners land at once, and keeps clear of
  the inspector panel and the mobile Inspect button — both of which float above
  it — with padding instead of the insets an absolute box used.

  The click-through machinery went with it: every banner had to opt its own
  buttons back into `pointer-events`, and `check-overlay-taps.mjs` existed
  solely to catch the ones that forgot. Its own instructions said to delete it
  if the overlay ever stopped being `pointer-events: none`, so it is deleted —
  it caught this change on the way past, which is a fair last act for a guard.


- **Half the pins on a card read English to a Swedish reader.** The Email step
  showed Adress, `Local part`, `Domain`, `Display name`, Detaljer — two
  languages alternating down one card. Google Calendar was worse in a quieter
  way, labelling the same thing "Plats" in the Inspector and `Location` on its
  own pin.

  Every drop-vocabulary lookup falls back to the English it was handed, which
  is right for the reader and invisible to the maintainer: a pin with no
  `SV_PORTS` entry renders perfectly good English and nothing fails. Twenty-
  eight labels had drifted through that gap, eleven of them plain omissions —
  and six of those eleven were words the repo had ALREADY translated, sitting
  in `SV_FIELD_TITLES` under the same English string.

  All eleven are translated now: Attendees, Blocks, Display name, Domain, End,
  Link to open, Local part, Location, Name in Drive, Reply in thread, Script.

  The gap is closed rather than patched. `make drop-catalog` now emits each
  drop's pin labels alongside its description, and a coverage guard fails on
  any label that is neither translated nor listed as reading the same in
  Swedish. That allowlist is seventeen entries — file formats, loanwords, the
  bare A/B operands of a comparison — which is the number that made this guard
  worth having: the note previously declining it estimated the allowlist would
  run as long as the map itself, and it is a fourteenth of it.


- **A Swedish warning ended in an English word.** "Obligatoriskt värde saknas:
  Email" sat directly under a pin reading "E-post"; "Obligatorisk indata är
  inte kopplad: Value" under one reading "Värde". The node card's two config
  warnings interpolated the manifest's raw English `title` / `label` into a
  translated sentence, so the app appeared to disagree with itself about what a
  thing is called. Both now resolve the name through the same function that
  names it everywhere else.


- **`${trigger.…}` was offered everywhere and resolved nowhere.** The `{}`
  reference menu lists `${trigger.body.<field>}`, a lint message suggests it,
  the canvas draws it as a chip reading "Form → email", and two handler
  comments state that it works. No substituter implemented it — the engine
  resolved `item`, `upstream`, `resource` and the secret schemes, and nothing
  else.

  `SubstituteString` leaves an unknown scheme ALONE by design, so that JSON and
  shell snippets survive resolution untouched. The result was silent: a flow
  built by following the reference menu's own suggestion mailed out a literal
  `${trigger.body.version}` where the version should have been. Nothing failed,
  nothing logged.

  There is now a `trigger` substituter, delegating to the upstream resolver so
  the two schemes cannot drift apart on path syntax or stringification. WHICH
  node it means is decided by the run rather than by a rule: the trigger that
  fired is the one holding a result, so a flow carrying both a webhook and a
  schedule resolves against whichever actually started this run. With no
  trigger result — a manual Run — the placeholder is left as written, matching
  every other scheme.

  It survived this long because the tests naming the scheme covered the menu
  OFFERING the token and the webhook seed's SHAPE. Neither ran a value through
  it. The round trip is covered now, including that the `trigger` and
  `upstream` spellings of one value agree.

## [0.27.3] - 2026-08-30

### Fixed

- **Both sides of a branch could run.** An If emits on `then` OR `else`, never
  both — but dispatch classified the unemitted port as *dormant*: "does not
  contribute, does not block". So any OTHER live wire into the untaken step was
  enough to enqueue it, and both branches ran.

  The shape is the one people actually build: an If that mails one thing on Yes
  and another on No, with both send steps taking their recipient from a single
  Email step off to the side. That address wire is live for both branches, so
  it alone made the untaken side runnable — and someone got both emails.

  A port the predecessor declined to emit on is now `edgeNotRouted` — a routing
  ANSWER, "not down here", not an absence of one — and it skips the dependent
  even when another wire is live, because routing is exclusive. Its skip reason
  says which port declined rather than the generic "all incoming edges
  dormant", so the run log names the router instead of describing a state.

  This is the same hole `NoPassthrough` closes for the pass pin, reached
  through a data port instead: a value that bypasses the routing decision and
  fires the branch nobody chose. Worth knowing they are two faces of one
  problem — an exclusive choice is only exclusive if every path out of the
  router respects it.

## [0.27.2] - 2026-08-30

### Fixed

- **Step icons could render unstyled, and the Inspector's did.** Every icon
  modifier is a COMPOUND rule — `.dz-node .icon.brand-logo`,
  `.quick-palette-row .icon.branded` — so `.icon` is a required partner, not
  decoration. `DropIcon` built its class list by concatenating a prop, and the
  Inspector passes its own box class, so that element carried `brand-logo` and
  `branded` with no `.icon` beside them: modifier classes matching nothing.

  `icon` is now written literally in each template, with the surface's own
  class beside it.

  The reason this shipped is worth recording. `check-css-classes` reads a
  template literal by stripping its `${…}` interpolations and keeping the
  static text, so a partner class hidden behind an interpolation is invisible
  to it — and it reported the modifier as orphaned, correctly. The first fix
  hoisted the concatenation into a variable, which moved the partner behind a
  *different* interpolation and changed nothing. Only writing `icon` inside
  each template satisfies both the browser and the guard, and there is now a
  comment saying so, because the natural tidy-up is to fold it back out.

## [0.27.1] - 2026-08-30

### Added

- **A guard that the UI actually uses `enumNames`.** The Go side has insisted
  for a while that every enum carries display names; nothing checked that any
  screen rendered them. `enumNames` is now read in exactly one module, and
  `lib/dropText` exports the two ways a dropdown value may reach a screen —
  `enumOptionLabel` when walking `schema.enum` to build a list, and
  `enumValueLabel` for a value already stored. A test walks the source and
  fails on any other file touching the field.

  The mapping had been open-coded in **eight** places. Writing the guard found
  three more than the audit that prompted it, including one that skipped
  `enumLabel` entirely — a dropdown on the node card printing its English
  labels in every language, which no amount of Swedish translation would have
  fixed because the lookup was never reached.

  Both helpers take the schema rather than the names array, so no caller has a
  reason to touch `.enumNames` at all; the rule is structural now instead of
  remembered. A value the schema no longer lists still renders as itself, and
  an enum whose values ARE the names people know (HTTP methods, currency
  codes) still falls through to the value, matching `rawValueEnums` on the Go
  side.

- **A guard against `.test()` on a global regex.** It is always a bug: a `/g`
  regex carries `lastIndex`, `test()` advances it, so the same question asked
  twice gives different answers and any later scan of that regex starts
  mid-string. That is what hid the reference chips.

  The rule is deliberately narrow rather than a general ban on shared regex
  state. `test()` wants a boolean and gains nothing from statefulness, so
  there are no legitimate exceptions to carve out — while an `exec()` loop
  that resets at entry, and `matchAll()`, which clones rather than mutates,
  are both fine and both still in use here.

  It catches all three shapes: a binding from a literal, one from
  `new RegExp(…, "g")`, and an inline `/…/g.test(x)`. An audit alongside it
  found no other instance — the five remaining global regexes are either
  function-local, `matchAll`-only, or disciplined `exec` loops.

- **One `DropIcon` component draws every step icon.** The canvas node, the
  Ctrl+K palette, the Apps cards, the config checklist and the Inspector header
  each hand-rolled the same three-way branch — brand logo, self-coloured glyph,
  tinted glyph — and had drifted apart in every way they could. The canvas and
  palette painted a saturated gradient behind a near-black glyph while the
  Inspector used a soft tint behind a coloured one, so the same step looked
  like two different things depending on which half of the screen you read.
  The checklist rendered branded glyphs at one size and tiled ones at another,
  in the same list.

  They now share one component, one tint, and one answer to what a branded
  glyph looks like. Two visible consequences, both deliberate: the Inspector no
  longer tints self-coloured marks (Git's orange, the model logos), matching
  what the other four surfaces always did and the reason the card gave for it;
  and its tint moved from 14% to the shared 22%.


### Fixed

- **A number and its unit now have a space between them.** Durations read
  "94ms", "4.6s", "3.5m". A space is required there — SI says so for unit
  symbols, and Swedish writing rules say so too, which settles it for a UI
  shipping in both: this was not a house style to pick, it was wrong in one of
  our two languages. It is now "94 ms", and the space is non-breaking, so a
  wrapping cell cannot leave the number on one line and the unit on the next.

  **"min", not "m".** `m` is the symbol for metre, so a three-and-a-half minute
  run rendered as "3.5m" — three and a half metres. Familiar from developer
  tooling ("3m29s"), and ambiguous the moment it is read as a unit rather than
  as jargon.

  `formatBytes` in the same file already wrote "1.0 KiB" with a space, so the
  two halves of one module disagreed; both now use the same separator. The
  retry countdown, the workspace timeout and the plan-limits timeout carried
  their own unspaced copies and were fixed with it. CSS durations
  (`--enter-delay: 0.2s`) are untouched — a space there is a syntax error, not
  a typo.

- **Self-coloured icons were invisible in the step search.** Ollama, Git and
  ntfy render as their own marks with no backdrop, taking their colour from
  whatever contains them. Three surfaces set that colour; the Ctrl+K palette
  did not — and it also set `color: var(--accent-ink)` (#140d30) on the icon
  box, so the glyph was drawn near-black on a dark panel. Nothing was missing
  from the row; the icon was painted in the background colour.

  `DropIcon` now sets that colour itself rather than leaving it to whichever
  stylesheet rule happens to match, so a surface cannot forget it.

- **Node cards showed raw `${…}` syntax instead of reference chips.** On every
  card, for every reference — a trigger field, an upstream port, a secret, one
  embedded in a sentence. The chip container rendered; it just had nothing in
  it, with the raw token showing through beside the × that clears it.

  Two helpers shared one global regex. A `/g` regex carries `lastIndex`, and
  `test()` leaves it at the end of the match it just found. Every display
  surface asks `hasToken()` first and tokenizes only if it says yes — so by
  the time the tokenizer ran, the scan started PAST the only token in the
  value and found none. `hasToken` reset `lastIndex` before testing, which is
  what made the bug look impossible: the reset was there, on the wrong side of
  the call that mattered.

  The pattern is now held as source, with a stateless (non-global) regex for
  the test and a fresh instance per tokenize, so there is no shared state left
  to carry.

  The existing tests all passed against this. Each called the tokenizer on a
  fresh value with `lastIndex` still at zero — the one order the real code
  never uses. The new tests call `hasToken` first, which is the only way to
  see it.

- **`&lt;` and `&gt;` were showing up on screen.** The bearer-key help read
  "Callers send `Authorization: Bearer &lt;a key&gt;`" — in English and in
  Swedish, since the escape was copied into both. HTML entities do not decode
  on either rendering path: a `<Trans>` string is parsed for its component
  slots, not un-escaped, and a plain `t()` string is inserted as text.

  The escape was the careful-looking move that made it worse. A literal
  `<a key>` inside a Trans string really would be eaten as a tag, so escaping
  it swapped a placeholder that vanished for one that was visibly broken. The
  answer was to stop needing the brackets: a sibling string a few keys away
  already wrote `Authorization: Bearer …`, and that is now what both say.

  A guard walks every translation file and fails on any HTML entity, naming
  the two ways out — reword, or interpolate the value in, which is not parsed
  as markup either way.

- **Read JSON refused to hand you a single field.** Its `path` is a dot-path,
  and pointing it at one value — `version` in `{"version":"0.27.0"}` — dug
  correctly and then failed the whole step with `not_tabular`, because rows
  were built from the result unconditionally and a bare string is not a table.
  The step has a `Value` output for exactly this, and it was unreachable: rows
  are built first, so the failure came before anything could be emitted.

  A path is an explicit instruction to dig to one place, and what sits there is
  very often a scalar — a version, an id, a count. That now yields an empty
  `Rows` and a populated `Value` instead of an error.

  Only with a path. Without one, "the JSON you handed me is not row-shaped" is
  a genuine mistake — an AI step that answered in prose, an API that returned a
  bare string — and it still fails rather than handing back zero rows for
  someone to discover three steps later. Asking for a scalar is a choice; being
  given one is a surprise. A path matching nothing also still fails, before
  rows are considered, so a typo cannot pass as a field that held nothing.

  The `path` help now says which shapes give you rows and which give you a
  value, rather than implying any path works.

- **A dropdown's value on a node card read as its identifier, not its label.**
  The card printed the stored param value while the Inspector rendered the
  same field through `enumNames`, so the two disagreed about the field you
  were looking at — and the canvas, which people read first, was the one
  showing API vocabulary. On the If step a node said `not_equals` while the
  panel beside it said "does not equal".

  Every read-only enum literal on a card now goes through the same lookup the
  language chip a few lines above already used. A value the schema no longer
  lists still renders as itself rather than blanking, and a `${...}` reference
  keeps its own token rendering — it is never an enum member.

### Changed

- **Retention no longer deletes approvals.** The audit sweep pruned every
  action alike on the tenant's retention window — 90 days on Pro, 7 on Free —
  which put an expiry on the one entry nobody wants expired. An approval is the
  record of who authorised something, and that is characteristically asked
  about long after the window closes: during an incident review, or when
  someone asks who signed off on the release that broke it. At 90 days the
  authorisation for a production deploy is gone within a quarter; on Free,
  within a week.

  Approvals are now exempt from the sweep. Routine chatter — key reads, config
  edits, sign-ins — still ages out as before, which is what retention is
  actually for. Keeping approvals costs little: one is a deliberate human act,
  so the volume is bounded by how often people click rather than by traffic.

  It does not weaken erasure either. A user erasure pseudonymises the actor on
  these rows rather than deleting them, so the authorisation survives while the
  person stops being identifiable — which is what both rules want.

## [0.27.0] - 2026-08-29

### Added

- **An Email step.** A typed source field beside URL and Phone: type an address
  inline or connect a string in, and it emits the address on `out` — but only
  after checking it parses. A malformed address fails the step where it was
  entered, instead of surfacing much later as a bounce or a cryptic SMTP
  rejection, which is the same argument the Phone step makes for numbers.

  It also splits the address, so acting on its parts stops being string
  surgery: `local`, `domain`, and the display name from
  `Ada Lovelace <ada@acme.com>` on its own pin (with the bare address still on
  `out`, ready for a Send email step). Routing everyone from one company down
  their own branch is now `domain` into an If step.

  **Validation is `net/mail.ParseAddress`, not a regular expression.** RFC 5322
  admits quoted local parts, escapes and comments, so a short email regex is
  wrong in both directions at once — it rejects addresses that work and accepts
  ones that don't. The stdlib parser is also already what `internal/smtputil`
  and the notify steps use to decide whether an address is sendable, and a
  check that disagreed with the step that actually sends the mail would be
  worse than no check.

  Two deliberate limits. The domain is lower-cased and the local part is not:
  domains are case-insensitive by spec, local parts are the receiving server's
  business, and folding them is how a working address becomes a bounce. And
  there is no MX lookup — deliverability is a network call with its own failure
  modes, and "the domain resolves" is not the same claim as "this is an
  address". A dotless domain (`ada@acme`) is refused with a message that says
  which of the two it is: legal in the RFC for an intranet host, and in a flow
  about to send mail essentially always a typo.

- **A guard on the passthrough pin.** `NoPassthrough` is a hand-set bool, and
  forgetting it on a router is a correctness bug, not a cosmetic one: `pass` is
  re-emitted whenever the node succeeds, regardless of which port the payload
  took, so anything wired to a router's pass pin fires on every branch at once
  — a hole straight through the routing the author drew. Nothing checked, and a
  new routing drop that forgot the flag would have passed every existing test
  while quietly running both branches.

  The guard is deliberately two partial checks rather than one complete one,
  because "these output ports are mutually exclusive" is not derivable from a
  manifest. It pins the fifteen drops that opt out today, so a refactor cannot
  quietly drop the flag; and it trips on any NEW drop whose output ports look
  exclusive (`then`/`else`, `matched`/`unmatched`, …) while keeping the pin.
  `split_rows` is the standing exception, with its reason recorded: its
  matched/unmatched halves are two outputs emitted on every run, not two
  branches, so the pin is right there.

  Both checks were verified by breaking them — removing the flag from If, and
  hiding split_rows' exemption — rather than by watching them pass.

### Changed

- **The If step now says the same thing in all three places.** Its pins read
  **Value** and **Compare to**, but its operator list still said "A equals B"
  and the literal field was titled just "B" — three vocabularies for two
  things, so nothing on the form told you which side was which.

  The operators are now plain predicates on the value — "equals", "contains",
  "is one of", "is within range" — and the literal field takes the name of the
  pin it stands in for, "Compare to". The form reads left to right as
  *Value · contains · urgent*. The range bounds and the field path follow the
  same vocabulary, and the step's description and examples were reworded off
  A/B.

  Compare is deliberately untouched: it labels its own pins A and B, so "A
  equals B" is coherent there. If was the step that renamed its pins to plain
  language and left the operator list behind. The A/B labels stay in the
  Swedish file for Compare's sake, alongside the new set.

  The Contains preset had a milder version of the same thing — pins reading
  Text and Substring, help text referring to A and B — and now matches its own
  pins too.

  Only labels changed. The port IDs and param names are still `A`, `B` and
  `field`, because a saved graph stores edges by port ID and params by name; a
  rename there would silently drop the values in every flow already using the
  step.

- **Site check and Watch a web page say what the pass-through pin does.** Both
  steps are built around firing rarely — "nothing fires while the state is
  unchanged", "steps connected to 'On change' stay dormant" — and the
  pass-through pin is the one pin that does not behave that way: it carries its
  value on every successful check, changed or not. Someone reading either
  description and wiring from `pass` would reasonably have expected the
  conditional behaviour the rest of the step promises, and nothing on the
  canvas distinguishes the two kinds of port. Each description now says so, and
  points at the port that does mean "only when it changed".

### Fixed

- **"Send test event" was unreachable on every webhook flow built in the last
  year.** The control that replaces Run on a webhook-triggered flow — it fires
  the flow with a synthetic payload through the same seed-building path a real
  `/trigger` hit uses — was gated on a graph-level webhook entry in
  `g.triggers`. Trigger config moved onto the nodes when the Triggers menu was
  removed, so a flow built since then has `triggers: null` and its webhook
  secret on the `webhook_input` node. The condition could not be true, and the
  button was never drawn.

  Nothing failed. The endpoint worked, the dialog worked, the affordance
  existed in the code — the only symptom was someone hunting the UI for a
  button that was never rendered, with no way to tell it apart from not knowing
  where to look. The sibling check forty lines below had been updated for
  node-based config; this one was missed.

  It is now keyed off the node. A secret gates the PUBLIC `/trigger` endpoint,
  while test-trigger runs under the caller's own token, so an unconfigured
  webhook node is still worth firing a sample at. The sample's `form_fields`
  now come from the node's params too, falling back to the graph-level trigger
  for flows predating the move.

  The regression test fixture deliberately has NO `triggers` array: one
  carrying a legacy trigger passes against the broken code, which is how this
  survived as long as it did.

- **The editor's live flow-watch reconnects.** It was one-shot: `watchFlow`
  resolves when its SSE body ends, nothing retried, and the effect resubscribed
  only on a flow or auth change. So any drop — a laptop sleeping, a network
  change, a phone backgrounding the tab — left that window permanently deaf
  while its canvas went on looking live. The 25s server ping defends against
  idle proxy timeouts and none of those.

  Every path that ends the stream now schedules a retry, with backoff from one
  second to thirty; a received frame resets it, so a long-healthy stream that
  drops retries promptly rather than inheriting an old delay. A tab becoming
  visible, or the browser coming back online, retries at once instead of
  sitting out the wait.

  Each reconnect also re-reads the flow. The frame carries only
  `{flow_id, commit, author, autosave}` and no graph, so edits that landed
  while the stream was down were never delivered to anyone and nothing will
  resend them — a reconnect without that fetch would come back and still show
  a stale canvas.

  This does not make edits appear instantly in a second window, and two other
  things still gate that by design: nothing publishes until the 1.5s autosave
  debounce elapses, and a window holding unsaved edits deliberately ignores
  incoming changes rather than clobbering them.

- **Phone's Swedish field help came back.** Its translation is keyed on the
  English string, and the English had been reworded from "or wire a string"
  to "or connect a string" without the key following. The lookup missed and
  fell back to English — the natural-key design degrading safely rather than
  showing stale Swedish, but silently, and the translation had simply stopped
  applying. This is the failure that generated catalogue plus the coverage
  guard exist to catch for descriptions; field strings have no such guard.

## [0.26.5] - 2026-08-29

### Added

- **A running script now says what it is doing.** The "Run on your machine"
  step showed nothing until the command exited, so a deploy — or any script
  taking more than a moment — was a blank line for its whole duration, and
  indistinguishable from a stuck one.

  Almost all of the machinery for this already existed: the agent's heartbeat
  endpoint has always accepted an optional message, the daemon has always
  stored it on the task row, and the dispatcher has always polled that row and
  forwarded a changed value to the step's progress channel. The one missing
  piece was the agent, which posted an empty body and captured the command's
  output with `subprocess.run(capture_output=True)` — which hands the output
  over only once the command has exited, too late to tell anyone anything.

  The agent now runs the command under `Popen` and drains its pipes as they
  fill, tracking the most recent non-empty line and sending it with each
  heartbeat. Draining incrementally also stops a chatty command blocking on a
  full pipe. The heartbeat drops from 30s to 10s, which is now the rate at
  which progress appears rather than only how often the claim is held; beats
  happen only while a task is running, so an idle machine is unaffected.

  Progress is a side channel, never the output. The full stdout and stderr
  still travel in the result, so a dropped beat costs a status line and no
  data. A line is sent only when it changes, matching what the daemon does
  with it — an empty message leaves the stored line standing, so an unchanged
  line would be pure noise.

## [0.26.4] - 2026-08-29

### Added

- **A published release asks to be deployed.** After CI pushes a release's
  images it POSTs to the flow's webhook trigger, so a tagged push carries all
  the way through to production instead of stopping at "the images exist
  somewhere".

  Deliberately a `curl` at the end of the publish step rather than a
  builds.sr.ht `triggers:` entry. That block POSTs with no way to set headers,
  and the trigger endpoint reads the bearer key only from `Authorization` —
  there is no query-parameter fallback — so it could not authenticate. It would
  also fire on every green build rather than only on releases. Hanging the call
  off the publish path instead means it inherits that path's two gates for
  free: branch builds and credential-less builds never reach it.

  A failed trigger fails the build. A release that published and then quietly
  did not deploy is precisely the invisible half-success this setup exists to
  eliminate.

## [0.26.3] - 2026-08-29

### Changed

- **`make upgrade` no longer buries its own output.** Make echoes recipe lines,
  comments included, so the target's explanatory blocks were reprinted on every
  deploy — and since a deploy is normally driven by a flow step, they filled the
  run log, leaving the three lines that say what happened at the bottom of about
  fifteen that do not. The commentary moved above the target, where it still
  documents the recipe without being executed. A deploy now echoes one line.

### Fixed

- **Release images now carry the commit they were built from.** CI built them
  with no `--build-arg`, so the Dockerfile's `ARG COMMIT=unknown` default stood
  and every published image reported `commit: unknown` — 0.26.2 shipped that
  way. The version was right only because `ARG VERSION=` falls back to the
  committed `./VERSION`; `COMMIT` has no such fallback, and a build you cannot
  trace to a commit is one you cannot bisect against.

  `VERSION` is still passed as the tag, which is deliberately EMPTY on a branch
  build so the `./VERSION` fallback keeps working — a non-empty placeholder
  there is what once made every non-Makefile build call itself `dev`.

## [0.26.2] - 2026-08-29

### Added

- **`scripts/deploy.sh`** — the entry point for a runner-driven deploy, now
  version-controlled instead of living as an untracked file on the deployment
  host. It resolves the checkout from its own location, so it needs no
  site-specific path, and runs `PROD=1 make upgrade` under `set -e`.

  It is tracked because the untracked version drifted, invisibly, through an
  entire architecture change: it still opened with `git pull`, which fails on
  the detached HEAD every successful deploy leaves behind, and which nothing
  reported because the script had no `set -e`. A file nobody reviews is a file
  that keeps its bugs. Name this script in a runner's allow-list to stop that
  runner being able to execute anything else.

## [0.26.1] - 2026-08-29

### Fixed

- **The registry password hash has to be escaped in `.env`, and nothing said
  so.** Compose interpolates `.env`, so a bcrypt hash written literally —
  `$2a$14$xwcE...` — has each `$`-segment read as a variable reference and
  replaced with a blank string. Caddy receives a six-character remnant and
  answers every push with `401`, while `.env` still reads correctly to a human
  and Compose's warning names a variable nobody wrote. Every `$` must be
  doubled: `$$2a$$14$$xwcE...`.

  `.env.example` and the infra README now say so, and both give the check that
  distinguishes the two cases, since the file cannot be trusted on sight:
  `docker compose ... exec caddy sh -c 'echo ${#REGISTRY_PASSWORD_HASH}'` —
  60 is a hash, 6 is the remnant.

## [0.26.0] - 2026-08-29

### Changed

- **Production deploys pull prebuilt images instead of compiling on the box.**
  `make upgrade` ran `docker compose up -d --build` on the production host,
  which meant a Go build plus two Vite builds — the app and the docs site — on
  an `s-1vcpu-2gb` droplet. That does not fit. The build starved the machine
  (kswapd pegged at 47% CPU, TLS handshakes timing out), so the site was
  effectively down for as long as it ran, and it could not finish inside the
  ten-minute timeout of the "Run on your machine" step that triggered it — so
  it was killed partway, every time, leaving the previous release running.

  CI already built both images to prove the Dockerfiles still worked; on a tag
  build it now also publishes them to a private registry running on the droplet
  itself. The two directions are deliberately asymmetric: CI pushes over
  `registry.dazyflow.app`, which Caddy fronts with TLS and basic auth, while
  the host pulls over `127.0.0.1:5000` — loopback, which Docker exempts from
  its HTTPS requirement, so the pull path needs no certificate and no
  credential. Nothing is compiled on the production host any more; a deploy is
  a pull and a restart.

  Self-hosting is unaffected. With no `registry` service in the file set —
  which is every deployment that does not use the production overlay —
  `make upgrade` builds from source exactly as before.

  Set `REGISTRY_HOST`, `REGISTRY_USER` and `REGISTRY_PASSWORD_HASH` in `.env`
  to enable the push endpoint. Leaving them unset disables the route rather
  than opening it: the site address falls back to a name that resolves nowhere
  and the credential to a bcrypt hash of a random string, so a box that takes
  the new Caddyfile before its `.env` keeps serving the app. `registry` is now
  a reserved subdomain, so no org can claim it.

### Fixed

- **`make upgrade` could not report a failure.** Its recipe was a single
  backslash-continued line joined with `;`, which make runs as one shell — so
  make only ever saw the exit status of the last command, a trailing `if` that
  returns 0 unconditionally. A failed build printed a successful upgrade, left
  the tree checked out at the new tag, and left the box serving the previous
  release. 0.25.0 "deployed" exactly this way onto images that were fifteen
  hours old. The recipe now runs under `set -e`: the first failing step stops
  it, and the deploy exits non-zero so the caller sees red.

## [0.25.0] - 2026-08-29

### Fixed

- **The overview's four stat tiles now open the runs they counted.** Each tile
  is a claim about a subset of runs — how many ran today, how many succeeded,
  how many failed — but clicking one dropped you on the unfiltered run list.
  The number you had just read was nowhere on the page you landed on, which
  reads as the tile being wrong rather than the link being lazy. Only "Needs
  attention" carried its filter (`?status=failed`).

  Runs today now lands on today's date range, Success rate on the succeeded
  runs, and Approvals waiting keeps going to the approvals page. The runs page
  learned `?since=` and `?until=` (local `YYYY-MM-DD` calendar days, the same
  values its date inputs take) alongside the `?status=` it already honoured, so
  a date-scoped view is a link anyone can share, not just something the tile
  can produce. A param that isn't a calendar date is ignored rather than
  half-applied.

## [0.24.1] - 2026-08-29

### Changed

- **The AI steps ask the vendor which models you can use, instead of offering a
  list compiled into the release.** A model picker built from a list baked into
  the binary is wrong in two directions, and only one of them was anticipated.
  It cannot offer a model published after the release — expected, and the price
  of a picker. It also goes on offering one the vendor has since withdrawn,
  which is the worse half: it fails inside somebody's flow at run time rather
  than at the moment they chose.

  Gemini did exactly that. `gemini-2.5-pro` was closed to new keys while
  staying in Google's catalogue, so a step that had been offering it for weeks
  began answering `NOT_FOUND` — and the list a release ships can never be
  right about that, because availability follows the credential rather than the
  build. Every provider that has a catalogue endpoint is now asked, per
  connection, and the answer replaces what the picker offers.

  Ollama gains the most, having had no picker at all: the model was free text
  because the catalogue is whatever the operator pulled, so a typo was a 404 at
  run time and the default named a model many machines do not have. It now
  lists exactly what that server can run. Where the compiled-in default is not
  something your credential can call, the first model that can stands in — so
  the steps nobody has configured stop being the ones that break.

  The palette never waits for any of this. The editor renders from the list it
  already had and the vendor is asked behind the request, so the answer is
  right from the second render on; a vendor that is slow, down or not connected
  costs nothing and shows nothing. It applies wherever steps are read, not just
  in the editor — an agent reading `describe_drop` over MCP sees the same live
  catalogue, because a picker that is honest in the UI and stale over MCP would
  only move the failure somewhere quieter.

  Two things are deliberately NOT filtered out of what the vendor returns.
  Models that cannot do a text step's job at all — image, audio, transcription,
  robotics — are dropped, because they would only ever fail. Everything else
  stays, including previews and the ones that look like an odd fit for a flow:
  hiding a model the vendor is offering is the same failure as offering one it
  has withdrawn, just in the other direction.

## [0.24.0] - 2026-08-28

### Added

- **Gemini joins Claude, ChatGPT and Ollama as an AI provider.** The same five
  steps — Ask, Summarize, Extract fields, Classify, Draft reply — now run on
  Google's models. Connect it on the Apps page with an API key; the key rides a
  header rather than the query string, so it stays out of proxy access logs.

  The model is a picker (Gemini 2.5 Pro, Flash and Flash-Lite) rather than free
  text, matching Claude and ChatGPT: Google publishes a catalogue, so a typo
  should be impossible rather than a 404 at run time. The cost is that a model
  released after this list was written cannot be selected until the list grows.
  Flash is the default, because the five task steps are short, high-volume
  calls — summarize a row, classify an email — which is what Flash is priced
  and tuned for. An author who wants Pro picks it on the step.

  Gemini is the only one of the four that does not speak the OpenAI chat shape,
  so a little more is translated behind the step than for the others: the model
  travels in the URL rather than the body, the system prompt is a separate
  instruction rather than a turn, and a forced tool call comes back as part of
  the reply rather than a field beside it. None of that surfaces in the step —
  Extract fields and Classify behave as they do on any other provider.

### Fixed

- **Signing in no longer lands on "page not found".** The sign-in form
  navigated somewhere only when the URL carried an invitation or a `return_to`;
  a plain sign-in navigated nowhere and relied on the router happening to be on
  a path the signed-in app also serves. From `/` that worked, because the root
  has a signed-in route. From `/signin` — a bookmark, the link in an
  invitation, anywhere the signed-out catch-all had already sent someone — it
  did not: the signed-in route tree has no `/signin`, so the moment the
  password was accepted the catch-all announced that the page did not exist.

  Every success path now decides explicitly where it lands: the invitation it
  came from, the page a `return_to` asked for, or the root. It replaces the
  form in history rather than stacking on it, so Back does not return to a
  sign-in page that would strand the reader the same way. The second-factor
  step shared the gap and shares the fix, and the check that stops a crafted
  `return_to` bouncing a freshly authenticated user off-origin now covers the
  password path too, which it never did.

- **Belonging to a second organisation no longer reports a permission error on
  every page.** The app remembers which organisation this browser last worked
  in, and the session is scoped to one organisation at a time. Signing in
  scopes the session to your own organisation — so for anyone who had been
  invited into a second one and last used *that*, the two disagreed from the
  first render. Every request then named one organisation while the session
  was bound to the other, and the server correctly refused: the flow list, the
  runs, the apps, all reporting that you lack permission and to ask an
  administrator. No amount of granting helps, because permissions were never
  the problem.

  Sign-in now reconciles the two before anything is drawn, the same way
  switching organisation from the picker always has. Where the remembered
  organisation is one you can still act in, the session is re-scoped to it and
  you land where you left off. Where it isn't — an invitation withdrawn,
  an organisation suspended — it falls back to your own and forgets the stale
  one, so the next visit starts clean rather than retrying a door that is
  closed.

## [0.23.0] - 2026-08-28

### Added

- **Ollama joins Claude and ChatGPT as an AI provider.** The same five steps —
  Ask, Summarize, Extract fields, Classify, Draft reply — now run against a
  model on hardware you control, so a self-hosted workspace no longer has to
  hold an account with a cloud model vendor to use the AI steps at all.

  Connect it on the Apps page with a Server URL (default
  `http://localhost:11434`). The API key field is optional and only wanted when
  your instance sits behind an authenticating proxy — Ollama itself has no
  keys, and the network boundary is the authentication. The connection test
  asks what actually fails for a local runtime: whether the daemon can reach
  the server, and whether any model has been pulled. A server on localhost also
  needs `DAZYFLOW_ALLOW_PRIVATE_EGRESS` set on the daemon, since the SSRF guard
  blocks private addresses by default and that default is right.

  The model field is free text rather than a picker, because the catalogue is
  whatever you have pulled rather than anything we can list. Extract fields and
  Classify ask the model to call a tool, and need one that supports them
  (llama3.1, qwen2.5, mistral-nemo and similar). How reliably a forced tool
  call is honoured varies by model and by Ollama version, so when a model
  answers in prose instead, the step will try to read the JSON back out of the
  reply before giving up — a best effort against a moving target rather than a
  guarantee. If nothing usable comes back, the error names the model you ran
  and suggests one that works.

### Changed

- **The chrome around AI stopped naming two vendors.** "Connect Claude or
  ChatGPT" now reads "Connect an AI provider", and the assistant dialog no
  longer offers Claude as its worked example. Both were wording that framed AI
  in general while naming particular companies — already inaccurate the moment
  a third provider existed.

  The step catalogue still names Claude, ChatGPT and Ollama individually, and
  deliberately so. Each is a separate connection with its own key, and which
  one a step runs on decides what it costs and who sees the text; that is a
  choice to put in front of the author, not one to hide behind a generic
  label.

## [0.22.2] - 2026-08-28

### Changed

- **The welcome screen was reordered around what the reader actually needs.**
  It read: greeting, resume link, then the line introducing the page — so the
  one sentence explaining the screen arrived after the reader had already been
  handed something to click. The featured button repeated the mistake in
  miniature, with the sentence describing what pressing it would do set
  underneath it.

  It now goes greeting, what this screen is for, where you left off, then what
  you can start. The description of the demo moved inside the card it
  describes, so it is read before the click rather than after.

  The four ways into a new flow were the deeper problem. All four go to
  `/flows/new` and differ only by `?tab=`, but they were presented at three
  weights — an accent button, two 13px text links divided by a dot, and a
  bordered card at the foot of the page — and the ranking followed nothing.
  Describing a flow in plain English, the most distinctive of them, was the one
  in the box at the bottom. Template, AI and blank are now one row of equal
  cards, each with a line saying what it does, carrying the same icons as the
  tabs they open so the card and its destination read as one route.

  The zero-setup demo is now first-run only. Shown to someone with a flow of
  their own to resume, it put a demonstration above their actual work. The
  page also stops nesting: it was one card wrapping every element, which gave
  each of them a second border and made the option row look like a panel
  inside a panel.


### Fixed

- **The sign-in page no longer greets everyone with a notice about a missing
  page.** 0.22.1 added a line above the form for visitors who arrived at the
  signed-out catch-all. Two things were wrong with where it landed.

  The bare root had no signed-out route of its own, so it fell to that
  catch-all — meaning anyone who simply typed the domain, the most common
  arrival there is, was told the page they asked for did not exist. `/` now has
  an explicit route and shows the form plainly.

  The wording overclaimed, too. That catch-all covers two cases it cannot tell
  apart: a mistyped address, and a real app page the visitor is not
  authenticated for — a deep link from an email, a session that expired
  mid-page. The second is the common one, and "that page doesn't exist" is
  simply false about it. The notice now says only what holds for both, that
  signing in is the next step. Whether the page exists is settled after sign-in
  by the authenticated catch-all, which can answer it correctly.

## [0.22.1] - 2026-08-28

### Added

- **A render error shows a page instead of blanking one.** Neither the app nor
  the docs had an error boundary, and React's default for an error during
  render is to unmount the whole tree — so any such bug left an empty document:
  no message, no reload, nothing to distinguish it from the product having
  died.

  Both entries are now wrapped in one. It offers a reload (a full one — the
  tree that threw is still in memory, and re-rendering it walks back into the
  same error), a way home, and the error message collapsed under **Technical
  details**, which is the part that makes a support report actionable.

  It takes no dependencies on purpose: no i18n, no router, no design-system
  button. This renders precisely when something else has failed, and every
  import is another thing that can be the reason it cannot. `react-i18next` in
  particular reads a module-global a failed bootstrap may never have
  initialised, so a translated crash screen is one that vanishes exactly when a
  bootstrap error is what you needed to see. A test asserts that, so adding
  `useTranslation` to the fallback fails the suite.

- **Every signed-out screen says what it is.** The sign-in card read "Sign in"
  over an otherwise empty page — no mark, no wordmark, nothing naming the
  product. The one branded path, `signin-org`, needs a wildcard-subdomain
  deploy with an org icon set, so the hosted product's own front door was the
  one with no name on it. Someone following a password reset, an invite, or a
  mistyped link met an unlabelled box and had to infer where they were.

  All six auth screens — sign in, sign up, forgot and reset password, accept
  invite, verify email — now share one frame: the mark and wordmark above the
  card, a quiet Docs · Source · copyright line below it. The mark sits outside
  the card deliberately; inside, it competes with the form's own heading and
  pushes the first field down.

  `VerifyEmail` joined them on the way past. It was the odd one of the six —
  a `.card` with three inline styles at a width 20px off the others — and is
  now the same card, centred, with the difference that matters (its states are
  a glyph over prose, not a form) carried by one class.

- **The docs site has chrome of its own.** The header carried the product's
  wordmark and nothing else, so the one thing on screen never said which of the
  two sites you were in; and the page simply stopped at the end of the last
  table, with no answer to the two questions a reader most often reaches the
  bottom with — what is this, and where do I get it.

  The top bar now reads **Dazyflow · Docs** and carries quiet links to the
  product and to the source, left of the invite button. Below the prev/next
  pair there is a real footer: guide and reference columns, a Project column
  (product site, source, licence, contact, invite), and a legal line naming the
  AGPL. The link columns are derived from `NAV`, the same decision the reading
  order makes — a footer holding its own copy of the sidebar is a second list
  to keep in step, and its failure mode is pointing at a page that moved. Only
  the off-site addresses are written out, in one module the header and footer
  share.

  Scoped to `.docs-topbar` rather than the bare `.topbar` the docs borrow from
  the app: the shell renders the product's own chrome markup on purpose, and a
  rule written on the shared class would restyle the product from the docs
  stylesheet.

### Changed

- **Copyright is held by Angels' Ware.** Every `SPDX-FileCopyrightText` header
  in the tree — 1351 files — and the copyright line in both footers now name
  Angels' Ware rather than an individual. Nothing about the licence changes:
  the project stays AGPL-3.0-or-later, and the FSF's own notice on the licence
  text in `LICENSE` is untouched, since that is their copyright on the document
  rather than ours on the software.

- **The docs render light.** They were pinned dark to match the marketing site.
  But a reference page is read at length rather than glanced at, and light is
  what most readers' machines are already in — the same reasoning `theme.ts`
  gives for the app following the OS instead of defaulting to dark. Pinned
  rather than resolved from `prefers-color-scheme` because the docs ship no
  theme control: a reader who landed in the wrong one would have no way out.

  The shell's `<html data-theme>` and its `theme-color` meta moved with it;
  left behind, they painted the page dark for a beat before the module ran,
  which is the flash the pre-paint attribute exists to prevent. No stylesheet
  work was needed — `docs.css` holds 149 token references and not one
  hardcoded colour, so it had a light rendering already.

### Fixed

- **A dead link inside the app says so.** The signed-in catch-all was
  `<Navigate to="/flows" replace />`, so a bookmark to a deleted flow, a link
  from an old email, or a typo silently teleported the reader to the flow list
  — leaving them to work out whether they had mis-clicked, whether the thing
  was gone, or whether the product had just wandered off. There is now a page
  that says the address was not found and echoes it back, since half the time
  the path itself is the evidence: a truncated copy-paste, or an id the reader
  recognises as one they deleted.

- **A bad link while signed out no longer looks like a session expiry.** The
  catch-all route renders the sign-in form, so any mistyped or dead URL
  produced a bare password prompt the visitor had not asked for — which reads
  as "you have been logged out", a different and more alarming thing than a
  wrong address. It now says the page doesn't exist, above the form.

- **The docs' brand mark no longer leads to "Page not found".** It was an
  `<a href="/">`, and `/` is not in the page map — nginx answers every
  unmatched path with `index.html`, so the SPA booted at a route it had no
  entry for. The control a lost reader is most likely to press was the one
  that lost them. It now routes to the first guide page, the docs' actual
  front door.

## [0.22.0] - 2026-08-28

### Added

- **Steps can be imported from an OpenAPI spec.** Putting an org's own service
  in the palette meant typing every operation into the admin form by hand —
  method, path, each argument and its type — for a service that already
  publishes exactly that document. Ten operations was an afternoon, and the
  afternoon is why most catalogs stopped at one.

  **Admin → Web APIs** now reads an OpenAPI 3 document, either from an `https`
  address the daemon can reach or pasted in whole, and offers what it found:
  the API's own title and description as defaults for the catalog, an absolute
  `servers[0].url` as the base address, and every operation with its arguments
  already shaped. Pick them one at a time or by tag. Nothing is stored until
  the ordinary **Save** on the form below it, so an import is a draft the admin
  edits — the same descriptor the hand-built form produces, which is why
  validation, manifest synthesis and the executor are untouched by this.

  Importing *operations* rather than registering a *spec* is the whole design.
  Stripe's spec is around 500 operations and GitHub's around 900; filing all of
  them would put an org's entire API surface into the palette and into the flow
  generator's grounding, and a catalog holds 60 for that reason. The parser is
  ours rather than a library's, because a library validates a document as a
  whole and refuses it as a whole — one operation nobody can express would
  block the fifty that work. Here it is skipped with a warning naming it, and
  the rest import. That parser also has no fetcher in it, which is what makes
  "an external `$ref` is never followed" structural rather than a flag someone
  can flip. Swagger 2.0 is refused outright, with a note to convert it first:
  its parameter model differs enough that reading it as 3.x would produce
  operations that look right and send the wrong request.

- **Re-importing a spec says what it would take away, and asks first.** A spec
  is regenerated by whatever framework serves it, on a schedule the org reading
  it does not control, and an operation can vanish from it because someone
  deleted a handler last Tuesday. A refresh that simply replaced a catalog's
  operations would take the corresponding steps out of every flow referencing
  them — silently, because a step id that no longer resolves is not an error
  until the flow next runs.

  **Refresh from the spec** now reports the change as a diff — so many added,
  changed, removed, unchanged — and will not proceed until removals are
  confirmed by name, with the step id each one costs, which is the string a
  reader needs to search their flows for. Additions and changes carry no such
  prompt: an added operation is referenced by nothing yet, and a changed one
  keeps its step id, so flows keep resolving. The spec's address is remembered
  on the catalog (`spec_url`) so a refresh does not ask for it again.

- **A service with no public address can be called through a runner.** Dazyflow
  refuses to dial private addresses and always will, so an org's internal
  orders API — the most ordinary thing to want to automate — could not be
  reached at all. The block is right; there was simply no route past it.

  A catalog can now name one or more **runner tags**, and its calls are
  rendered as a small script and dispatched to a machine carrying all of them,
  the same way any other runner task is. That machine already sits inside the
  network and already asks the daemon for work. Everything above the last hop
  is unchanged — same descriptor, same synthesized manifests, same ports, same
  connection injection — which is why this is one field on a catalog rather
  than a second kind of app.

  The form is explicit about the trade, because it is a real one: a call made
  from the org's own machine skips the daemon's outbound checks (the
  private-address block, the allowed-hosts list, the rate limit). The response
  size limit still applies. Stored as `runner_tags` on `tenant_web_apis`,
  applied as an in-place migration.

- **The docs site reads like a document.** It was a file listing with prose in
  it: no way to see what was on a page, no way to get to the next one without
  going back up to the sidebar, and nothing to click on a 900-line example but
  a text selection.

  Each page now carries an **On this page** rail that tracks the heading you
  are in, and a previous/next pair at the foot derived from the sidebar's own
  order — so a page added to the nav joins the reading order without a second
  list to keep in step. Fenced blocks gained a caption naming the language and
  a copy button, which matters most on the generated catalog, where every page
  ends its examples in a settings object meant to be pasted onto a step. JSON
  examples are coloured by the product's own `tokenizeJSON`, so the object in
  the docs is painted in the same hues as the field the reader will type it
  into — 262 of the 268 fenced blocks are JSON, so that one reuse covers
  essentially the whole corpus. Asides written as an emoji-led blockquote
  (`> 🧭 **New to Dazyflow?**`) render with a real icon in the gutter instead
  of an emoji sitting in the first sentence, and section headings gained a
  self-link. The header's **Request an invite** button is now ghost rather than
  filled: someone deep in the step catalog is working, not evaluating.

### Fixed

- **A generated catalog page no longer opens with its generator's comment.**
  `<!-- Generated by cmd/docsgen … -->` heads all 43 catalog pages, and with no
  raw-HTML pass configured it was being escaped and printed rather than
  skipped — the literal first line a reader saw on every one of them. Comment
  nodes are now dropped outright. Two neighbours went with it: the catalog's
  Settings tables, which carry a full sentence per row and run far wider than a
  phone, now scroll inside their own box instead of pushing the whole page —
  and the nav with it — sideways; and react-markdown's mdast handle stopped
  reaching the DOM, where it had been stringified into a literal
  `node="[object Object]"` on every overridden element.

- **The overlay port is named rather than described.** Both the MCP and web API
  step families end their inputs with a port for extra params, labelled
  *"Optional JSON object merged with params before the call"* — 60 characters
  against a mean of 9 across the ~700 ports in the tree, in a slot the editor
  and the catalog's **Name** column both treat as a name. It is now **Extra
  params**; the tables showing it already carry *optional* and *JSON object* in
  their own columns, so nothing was lost in the shortening.

- **The authentication picker says which header it means.** *Bearer token* and
  *Custom header* named a concept and left the reader to guess the wire form
  they had to match. Both choices now show it — `Authorization: Bearer …` and
  `X-Api-Key: …` — on the MCP server and web API forms, in both languages.

## [0.21.0] - 2026-08-27

### Added

- **A web API can describe itself, and the description reaches the Apps page.**
  An org's own catalog appeared under **Apps** as a card with a name and nothing
  else: every built-in app's blurb is curated in the product, and there is
  nowhere to curate an org's — so the one app whose purpose only that org knows
  was the one app that could not say it.

  The admin form now takes a short paragraph about the service. It shows on the
  catalog's page under its name, is matched by the apps list's search box, and is
  reported as the integration's `summary` by the catalog API, which is what the
  flow assistant reads. Curated prose still wins where it exists, since that can
  be translated and edited without a release.

  Carried on the manifest (`integration_description`) and borrowed for the group
  by the first step that declares it — the same mechanism as `brand_logo`, since
  an integration is a derived view over manifests with no record of its own.
  Deliberately not folded into each step's description: the same paragraph on
  sixty manifests is what the flow generator would read instead of grounding.

- **The template gallery reads in the reader's language.** Its buttons and
  chrome were translated and its content was not, so a Swedish reader got
  Swedish controls over twelve English cards — the one screen most likely to be
  someone's first, since it is where a new flow starts.

  Every template's title, one-liner and group heading now resolves through a
  Swedish vocabulary, the same shape and the same drift guard the drop catalog
  uses: keyed by template id with a fingerprint of the English it was made from,
  so rewording `templates/index.json` shows the new English rather than a
  Swedish sentence about the old one. Cards still GROUP by the English category,
  so which cards sit together — and the `?category=` link that reproduces it —
  does not depend on the language.

  Coverage is now guarded by a test, for the reason the gap went unnoticed in
  the first place: every lookup falls back to English, so an untranslated
  template renders perfectly well and says nothing. A template added without a
  translation, or whose English drifts, now fails the suite.

### Fixed

- **An uncurated app is named the way it was typed.** The Apps page derived an
  app's display name by title-casing its URL slug, so a catalog an admin called
  *Order service* appeared as *Order Service* — a change nobody asked for and
  nothing explained. The name now comes off the manifest's own `integration`
  string, with the slug kept only as the fallback for steps that name no
  integration at all.

### Security

- **A registration token can no longer take over an existing runner.** A token
  minted from **Add a runner** is now *open*: it registers a machine whose name
  is not yet taken, and a collision is refused (`409`) instead of silently
  overwriting the runner that holds that name. Overwriting retired the running
  machine's credential — kicking it offline — and redirected its work,
  including any secrets a flow injects as env or stdin, to whoever redeemed the
  token; a token seen in a scrollback or a process list was enough. Replacing a
  machine in place is now a deliberate act: **Re-register this machine** on a
  runner's page mints a token scoped to that one name, and the install command
  it produces names the machine explicitly. The stored token gains a `name`
  column (blank for existing tokens, i.e. open — the safe reading), applied as
  an in-place migration. Agent bumped to 0.2.1 for the clearer registration
  errors.

## [0.20.0] - 2026-08-27

### Added

- **Web API steps wear your service's own logo.** A described API was the one
  step source that arrived with no artwork — a built-in step names a file in
  `/brands` and an MCP tool can declare icons in its manifest, so every
  operation an org imported wore the same grey globe and a flow calling three of
  its own services looked like three copies of one step.

  Saving a catalog now looks for the service's favicon and inlines it as the
  brand mark on every step the catalog contributes: the node, the palette, the
  inspector, and the catalog's page under **Apps**. The base URL's own host is
  tried first, then the domain one label up, since an API host
  (`api.example.com`) usually serves no site while `example.com` does; declared
  `<link rel="icon">` artwork is preferred over `/favicon.ico`, largest first,
  because the latter is often a 16×16 that renders as four grey pixels.

  It is a guess, and it fails soft: no favicon means the globe the step had
  before, and nothing about the save depends on the result. The fetch goes
  through the same guarded caller a web-API step uses — SSRF dial guard,
  per-tenant egress allowlist, rate limit, response cap — and the bytes are
  stored inlined as a `data:` URI rather than as a URL, so the app never asks a
  third party for an image and never tells one who opened a flow. The mark is
  resolved once and stored, so the reconcile loop stays a query and a map
  compare; pressing **Save** is the retry, and a catalog that already has an
  icon keeps it unless its address changes. Catalogs saved before this have no
  icon until their next save.

  A guess can also be overruled. The form's new **Icon** field offers three
  sources — the service's favicon, **an image you choose**, or **no icon** — and
  the three are distinct on purpose: a guess that found nothing is retried on
  the next save, while a glyph an admin chose must never be, or the wrong logo
  comes back every time. A chosen image also survives a change of address, which
  is exactly when a guess must not. Oversized uploads are redrawn to 64×64 in
  the browser before they are sent, so the storage limit is not something an
  admin has to think about; a very large SVG is refused instead, since there is
  nothing in one to shrink.

## [0.19.1] - 2026-08-27

### Fixed

- **Web API steps are captioned by their names, not their ids.** A described
  API's steps read *"order-service — get_order"* in the palette: the catalog's
  slug and the operation's id, both identifiers, where MCP steps have read
  *"Vendor Tools — Create an issue"* since titles landed.

  Two causes, one each way. The catalog's display name already existed on the
  admin row and simply was not passed into the descriptor the manifest is built
  from — so it was captioned by its id with nothing failing anywhere. And an
  operation had no name at all: id, method, path and a summary sentence, but no
  short label. Operations now take a **Display name**, shown first in the admin form
  since it is the field that decides how the step reads, and the manifest uses
  `Descriptor.DisplayName()` / `Operation.DisplayName()` for both halves of the
  caption. Generated prose names the catalog the same way.

  Neither name is an identifier: step ids still come from the ids, so naming
  something re-captions its step and moves no flow. Both fall back to the id
  when blank, and both are clamped to one short line — the summary is where a
  sentence belongs, and the save says so if you put one in the name.

- **A web API's service address is no longer settable from its Apps
  connection.** It appeared in two places — Admin → Web APIs, and again as a
  field on the connection card — and the second one won: a connection value is
  injected into the matching param, and an injected param beats the catalog's
  own address.

  That put the address behind the WRONG permission. A connection is writable
  with `secret:write`, which the plain editor role holds, while the catalog is
  edited behind `organization:admin` / `module:register`. Since the token is
  sent as a header to whatever address resolved, an editor could point the org's
  catalog at a host of their choosing and be handed the credential. The
  connection now carries the credential and nothing else.

  Nothing is lost: a catalog belongs to exactly one tenant and there is no
  instance-wide population, so there was never a shared-catalog-per-deployment
  case for the override to answer. A single step that needs a different host
  still sets the `base_url` param — flow-shaping power `graph:edit` already has.
  An address previously saved on a connection is simply ignored (injection is
  manifest-driven, so an undeclared field is never read) and can be left alone.

- **Deleting a web API now says what will actually break, like MCP servers do.**
  Admin → Web APIs shipped with the same unconditional warning MCP servers had
  ("flows using its steps will stop running", whether or not any did). It now
  names the flows, with published ones called out, and says plainly when nothing
  uses the catalog. Backed by `GET /admin/web-apis/{name}/usage`, and the delete
  audit record carries how many flows were affected.

  The scan and the warning are now SHARED rather than duplicated: the flow scan
  moved to `daemon/stepsourceusage.go` keyed on a step-id prefix (`mcp:<name>:`
  or `api:<name>:`), joining the slug and URL policy already shared in
  `stepsources.go`, and both admin pages render one `StepSourceRemoveWarning`.
  The states worth getting right are the easy ones to get wrong twice — a failed
  lookup must not read as "safe to delete", and flows the admin cannot see must
  be counted without being named.

- **MCP steps are no longer labelled "Built-in".** They carried no app, and the
  palette's fallback for a step with no app is a faint *Built-in* badge — so a
  tool from a server the org added deliberately read as one of our own
  primitives, and the Apps page filed it under the standard library. They now
  report **MCP** as their app: the palette badges it, and Apps gives them their
  own **MCP servers** group with prose explaining where the steps come from.

  One shared app rather than one per server, because a step's label already
  names its server (*Vendor Tools — Create an issue*) — a badge repeating that
  carries nothing, while a badge saying how the step got here does. The provider
  stays per-server, since that is what scopes a step to the server it came from.
  MCP steps are still never asked to be "connected" like a vendor app: that
  machinery keys on connection fields, which these manifests do not have, and
  the credential lives on the server row.

## [0.19.0] - 2026-08-27

### Added

- **Web APIs — describe your own service and get steps out of it.**
  Admin → Web APIs takes a base address and a list of operations (method, path,
  arguments) and turns each operation into a first-class step,
  `api:<catalog>:<operation>`: named in the palette, with typed pins for its
  scalar arguments, required fields the editor validates, and a description the
  flow generator can compose against. Previously an org's own service meant a
  hand-assembled `http_request` in every flow — with the address and
  `${secret.…}` re-typed on each step — or writing and hosting an MCP server.

  The address and the credential are NOT stored with the catalog: each described
  API gets a connection under Apps, so it is connected once, encrypted, and
  never visible inside a flow. Rotating a token is one edit instead of one per
  step.

  Idempotency comes from the HTTP method, so a retry edge that targets a GET,
  HEAD, PUT or DELETE validates and one that targets a POST does not — the
  honest answer, where an MCP tool has to be assumed unsafe. POSTs and PATCHes
  carry a stable `Idempotency-Key` so a retry whose response was lost dedupes on
  any service that honours it. Calls go through the same guarded path as
  `http_request`: SSRF dial guard, per-tenant egress allowlist, per-host rate
  limit and 429 cooldown, response cap.

  Steps resolve only for the org that configured them, and a catalog can be
  turned off without deleting it. An org cannot name its API after an app
  Dazyflow already has — connection fields are found by slug with first-match
  wins, so a collision would have made the real app's connection page show the
  wrong fields at random. Phases 2 (importing an OpenAPI spec) and 3 (reaching
  a service inside your own network through a runner) followed.

### Changed

- The rules a tenant-configured step source obeys — the id slug and its
  diacritic folding, the id charset, the numbered uniqueness pass, and the
  https-unless-private-egress URL policy — moved to `daemon/stepsources.go` and
  are now shared by MCP servers and web APIs instead of living in the MCP file.
  Port synthesis moved the same way, to `internal/schemaports`: which arguments
  earn a pin, in what order, and the twelve-pin cap are one policy about the
  editor, shared by both catalogs.

### Fixed

- **Deleting an MCP server now says what will actually break.** The
  confirmation warned that "flows using its steps will stop running" whether or
  not any flow used it — a warning nobody could act on, and one that made
  cleaning up a mistyped server feel dangerous. It now names the flows: *"2
  flows use its steps and will stop running: Nightly sync, Alerts"*, with
  published ones sorted first and called out, since those are running now. When
  nothing uses the server it says so.

  A flow the admin may not view is counted but never named — the blast radius
  is not a reason to show someone a private flow's title. A failed lookup falls
  back to the old unconditional warning rather than claiming safety it cannot
  verify, and never blocks the delete. The new
  `GET /admin/mcp-servers/{name}/usage` backs it, and the delete audit record
  now carries how many flows were affected.

- **A disconnected MCP server no longer makes flows look broken.** When a
  server's endpoint went down or its token was revoked, its tools left the
  catalog — and a tool's manifest is what tells the editor a step's PORTS. Every
  flow wired into that step opened with a bare in/out pair instead of the
  arguments its edges were attached to, so the wiring looked lost. Nothing on
  disk had changed, but it looked like corruption and one save in that state
  could have made it real.

  A failed handshake now REPLACES the connection with a description rather than
  removing the steps. The tool list from the last successful connect is
  persisted on the server row (surviving a daemon restart, which is exactly when
  an author would otherwise open a flow that looks broken), and the steps stay
  in the catalog fully specified — ports, params schema, caption, icon — marked
  unavailable. The card grows a **Needs connection** banner along its bottom
  edge linking to Admin → MCP servers, and the palette greys the steps out
  instead of dropping them.

  Describable is not runnable: an unavailable step refuses before it builds its
  arguments, failing with `mcp_disconnected` and the endpoint's own reason, so
  the author is pointed at the server rather than at a param. Flows can still be
  edited, saved and published while a server is down, and start working again
  when it does. A server that has never connected has nothing to describe and
  still contributes no steps. `connected` on the admin API now means a live
  session, not merely a registration.

## [0.18.1] - 2026-08-27

### Added

- **MCP steps use the tool's own display title.** The protocol has carried an
  optional `title` on every tool since revision 2025-06-18 — the one we already
  negotiate — and we were dropping it, captioning steps with the raw wire name.
  A server offering one now gets *GitHub — Create an issue* instead of
  *GitHub — create_issue*. The step id is untouched, so search by `create_issue`
  still works and no existing flow moves. A title is third-party text landing in
  a palette row, so it is taken as its first line and clamped to 60 characters;
  the description, which is shown in full, is where prose belongs.
- **A server's handshake note is shown to the admin who added it.** MCP servers
  may return `instructions` — their own guidance on how they expect to be used
  (*"ask in English; the index is English-only"*) — and it now appears under the
  endpoint in Admin → MCP servers. Live-only, like the connection state: it is
  read from the connection this process holds rather than stored, so it is
  always what the server says now. Rendered as text, and nothing acts on it.
- **MCP tools bring their own icons.** A server publishing icons (MCP revision
  2025-11-25, SEP-973) now gives its steps a real vendor mark in the palette
  instead of the generic *external* glyph.

  Icons are **fetched by the daemon at handshake and inlined**, never linked.
  That is not caution for its own sake: the app's CSP is
  `img-src 'self' data: blob:`, so a third party's URL would not have loaded at
  all. Inlining also means an icon host cannot see who is looking at which
  admin page, and a dead one costs nothing. The fetch goes through the same
  post-DNS SSRF guard that dials the MCP endpoint itself, refuses redirects and
  cleartext http, and is bounded: 32 KB an icon, 8 distinct sources a server, 3
  seconds for the whole phase, with identical sources fetched once. `data:`
  icons are decoded and re-encoded rather than passed through, so a src
  carrying anything but the image it claims cannot survive. Anything that fails
  any of those checks is simply skipped — the tool still works, wearing the
  category glyph.

  The HTTP transport now negotiates **2025-11-25** (was 2025-06-18), which is
  the revision icons arrived in. A server speaking an older one answers with
  the revision it will use and sends no icons; that revision is recorded on the
  connection and reported as `protocol_version` on the admin API, which is the
  first thing to check when icons do not appear. The stdio transport is
  unchanged.

## [0.18.0] - 2026-08-27

### Added

- **A 46elks template: "Web form → text me".** A hosted form (public link, or
  embedded on your own site) sends you an SMS for every submission — the
  sender's name, their number and what they wrote, so you can call straight
  back.

  It is the first template for any of the Nordic connectors that are the reason
  to pick Dazyflow over Zapier/Make/n8n, and it was chosen to fix a specific
  problem rather than just to demo one: **on a fresh self-hosted install, 8 of
  the 11 shipped templates were greyed out**, because they need Gmail, Sheets,
  Slack or Notion and those require an administrator to register OAuth client
  credentials first. The create page opens on the gallery, so a self-hoster's
  front door was mostly walls. 46elks is a static-key connector — the user
  pastes username and password on the Apps page themselves, with no admin step
  and no OAuth mapping, so the card stays enabled. Usable-on-arrival templates
  go from 3 to 4, and this is the only *form* template among them.

  (Fortnox, the other obvious Nordic demo, is OAuth — it would have landed in
  the greyed-out eight. Worth knowing when picking the next one.)

- **Every icon a step declares now actually renders.** A drop's
  `Manifest.Icon` is a name the frontend has to know: `iconFor()` looks it up in
  `web/src/icons.tsx` and, finding nothing, falls back to the step's CATEGORY
  glyph. Nothing fails when a name is missing — no build error, no console
  warning — so 30 names declared in Go had quietly drifted out of the registry,
  and **57 steps were wearing a glyph their author didn't choose**.

  Most visibly, **11 of the transformation primitives all shared one CPU chip**:
  base64, build_csv, date, expression, hash, json, parse_json, parse_xml, phone,
  regex and render_template. They now show binary, table, calendar-clock,
  function, fingerprint, braces, braces, code-xml, phone, regex and code
  respectively — for an unbranded primitive the glyph is the only thing telling
  two steps apart in the palette. `http_download` and `http_upload` had likewise
  been identical (both the `io` default) and are now a down- and an up-arrow.
  The other 42 are branded connectors, where a brand mark was already carrying
  the node card, so they change less.

  `function-square` maps to lucide's `SquareFunction`: lucide renamed that
  glyph, and the old name is a legacy alias, so the canonical component is
  imported while the manifest keeps the name it declares. The other 29 were
  already canonical.

- **A guard keeps the two sides in step.**
  `TestManifestIconsAreRegisteredInTheWebUI` walks the real registered
  manifests and parses the registry out of `icons.tsx` — rather than restating
  the list, which would drift in exactly the way it guards against. A new drop
  naming an unregistered icon now fails a test instead of silently losing its
  glyph. It also fails if the catalog doesn't register or the regex stops
  matching the file, so it can't pass vacuously.

- **Shipped templates now have their CEL expressions compiled by CI.** The
  template guard validated wiring, ports and param types but never compiled a
  `render_text` template, so an expression with a typo composed cleanly and
  failed at run time with `bad_param` — exactly the "runtime error they can't
  diagnose" that test exists to prevent. Expressions are compiled, not
  evaluated: a missing field on the probe row is expected and ignored, since
  only a parse failure is a defect whatever data arrives.

- **The guide now teaches the product, not just its vocabulary.** Five
  task-oriented pages join *How Dazyflow works* and the *Glossary*: **Build your
  first flow**, **Connect an app**, **Make a flow run by itself** (triggers,
  schedules, publishing), **Forms & webhooks**, and **When a run fails**.

  Until now `docs/guide/` held two conceptual pages plus two operator topics
  (runners, MCP servers), and everything else a reader could reach was the
  generated step catalog. So a stuck user got an explanation of what a step *is*
  and a reference for all 152 of them, with nothing in between telling them how
  to connect Gmail, why their published flow wasn't firing, or what Retry does
  that Replay doesn't. The new pages are written against the UI's actual labels
  and copy, and they say the things the app can only hint at in a tooltip — that
  Run always uses your draft, that Replay re-sends the emails Retry doesn't,
  that connecting Slack isn't enough without `/invite @Dazyflow`, and that a
  "monthly on the 31st" schedule skips February.

### Fixed

- **A brand-new account's Runs page no longer blames a filter nobody set.** An
  empty response rendered "No runs match this filter." in every case, including
  the first visit of someone who has never run anything — so the product's
  answer to "where are my runs?" was to imply the person had filtered them
  away. The status chip, the flow picker and the date range narrow the fetch
  server-side, so an empty result only means "nothing matched" when one of them
  is set; with none set it now reads "No runs yet", explains that a run appears
  for every flow run whether started by hand or by a trigger, and offers a way
  to the Flows page. (The dashboard already said "No runs yet." — the wording
  existed, this page just wasn't using it.)

- **The pre-run gate's button now names where it goes, and goes somewhere
  useful.** It said "Go to Connections" in every case — not a page this product
  has; the sidebar calls it **Apps** and the org's credential store is
  **Admin → Secrets** — and it always navigated to `/apps`.

  For a missing app that was merely vague. For a missing `${secret.NAME}` it was
  a dead end: nothing on the Apps page adds a secret. A secrets-only gap now
  goes to the secret store, deep-linked with `?focus=NAME` when there is exactly
  one — the convention SchemaForm's inline "Set up" link already used — and the
  button reads "Go to Secrets". Target and label are derived together by
  `setupDestination`, so they cannot drift apart again.

- **The hosted form now speaks the flow's language.** It hardcoded
  `<html lang="en">`, "Submit" and "Thanks! Your submission was received." —
  and it is the only surface of the product a stranger ever sees, so a Swedish
  business's contact form, embedded on their own site, addressed their customers
  in English.

  It now follows `core.Graph.Language`, which is the rule `internal/maillang`
  already documents for the mail a flow sends: *it is the flow speaking, and its
  author chose the language its steps write in*. A visitor has no account to
  hold a preference, so the flow's own language is the only answer available.
  The form's six strings moved into that catalogue, where the existing guards
  hold them to the same standard as every email — no language may be missing
  one, and Swedish may not be a copy of the English. `maillang.Primary` is new
  and exists so `<html lang>` is derived from the same resolution as the copy
  and cannot contradict it.

  What is deliberately NOT translated: the form's heading and its field labels.
  Those are the owner's own words, humanized from the field names they typed.

- **A failed form submission no longer throws away what the visitor typed.**
  The three failure paths answered with `http.Error` — an unstyled plain-text
  page, in English, with no way back — on the highest-stakes public page in the
  product. They now re-render the form itself with a banner on top and every
  value back in its field.

  The banner also distinguishes what a second attempt can fix. A transient
  failure says the details are still there and to try again; a refusal the
  OWNER has to act on (over the plan's run allowance, a suspended org, a flow
  that no longer validates) says to get in touch another way and answers 503,
  because retrying will fail identically and the visitor has no way to know
  that. Re-filling reflects a visitor's input into the page for the first time,
  so the contextual escaping that makes it safe is now pinned by a test.

- **In-page documentation links now actually jump.** Headings only got a DOM id
  when they carried the generated catalog's `{#custom-id}` anchor, so every
  cross-reference in a hand-written page pointed at nothing — all 19 of the
  Glossary's *"See also [Connection]"* links navigated to the page and then sat
  still. Headings without an explicit anchor now get a GitHub-style slug of
  their own text, which is the rule those links were already written against
  (`Cron / schedule` → `#cron--schedule`), with GitHub's `-1` suffix for a
  repeated heading so no page emits a duplicate id.

### Changed

- **MCP servers are named, not slugged.** The Name field in Admin → MCP servers
  now takes whatever you would actually call the server — `MCP Test`,
  `Kundregister (test)` — instead of demanding lowercase and hyphens. Dazyflow
  derives a short id from it once (`mcp-test`), shows it under the name in the
  list, and builds the step ids from that, so a tool is still
  `mcp:mcp-test:<tool>`. Two servers whose names derive the same id get
  `-2`, `-3`, and so on rather than colliding.
- **An MCP server can be renamed.** The derived id is what flows reference, so
  the display name is now free to change: its steps re-caption in the palette
  and every flow keeps working. The id itself is still fixed at creation.

## [0.17.0] - 2026-08-27

### Added

- **Each org can now add its own MCP servers, in Admin → MCP servers.** Point
  Dazyflow at an MCP endpoint and every tool it publishes becomes a step —
  `mcp:<server>:<tool>` — in that org's palette. No connector to write, nothing
  to install. Add, edit, pause, re-read the tool list, and remove, all from the
  page; saving handshakes with the endpoint straight away, so a wrong URL or a
  rejected token is reported while the person who pasted it is still there.

  Previously MCP servers existed only as `DAZYFLOW_MCP_SERVERS`, an operator's
  environment variable applying to the whole deployment — so an org could not
  bring its own tools without someone editing the daemon's config and
  restarting it.

  Tenant servers are **HTTP only**, and that is the security boundary rather
  than a limitation to lift later: the stdio transport starts a process on the
  daemon host, so exposing it to an org admin would be remote code execution by
  any customer. `DAZYFLOW_MCP_SERVERS` keeps the stdio path for operators, and
  those servers stay visible to every org. See
  [docs/guide/mcp-servers.md](docs/guide/mcp-servers.md).

- **An MCP tool's arguments are now ports, so they can be wired.** Previously
  every tool got a single untyped `input` port taking a whole JSON object, which
  meant supplying one argument from an earlier step required building an object
  with a transform first — the steps were in the palette but not really wired
  like the built-ins they sit next to.

  Top-level scalar arguments (text, number, yes/no) each get an input, ordered
  required-then-optional and alphabetical within each so the canvas never
  reshuffles across a restart. Objects and arrays deliberately stay params:
  a pin per nested leaf would invent names the tool never declared. Capped at
  twelve ports, required ones taken first. The `input` overlay port remains as
  the escape hatch, and precedence runs least-specific-first — params, then the
  overlay, then a wired argument.

  Two smaller corrections came with it: an argument named `input`, `pass` or
  `out`, or one whose name cannot be a port id, stays a param rather than
  silently shadowing a pin; and every MCP input is now marked inline-only, so a
  flow wiring a file into one is refused before the step runs (a `Ref` is a path
  on the daemon's disk, which means nothing to a server elsewhere) instead of
  the tool failing on a path it cannot see.

  Supporting work:
  - `engine/mcp` gained a streamable-HTTP transport (protocol `2025-06-18`),
    handling both response shapes a server may choose — a single JSON object,
    or the response carried among other events on an SSE stream.
  - The MCP catalog is now keyed by `(tenant, tool id)` the way the remote-module
    catalog already was, so an org's server resolves for that org and no other.
    This matters more than ordinary scoping: a job reaching a transport carries
    *resolved* secrets, so a lookup that could cross tenants is a place one
    org's credentials could be sent to another org's endpoint. A tenant may not
    take a name an instance-wide server already uses; the registration is
    refused rather than shadowed.
  - Tokens are sealed under the tenant's DEK, bound to `(tenant, "mcp_server",
    name)`, and the store's read columns exclude them — so no list or lookup can
    carry one back to a browser. A token entered as `${secret.NAME}` resolves
    through the org's secret store instead, so a credential rotated there is
    rotated here.
  - Requests to a tenant's endpoint go through the same SSRF dial guard as
    `http_request`: no loopback, private or link-local address, checked after
    DNS resolution so a hostname pointed inward is caught too.
  - Registrations live in Postgres and every daemon reconciles against them, so
    the feature works across replicas: a server added on one node is connected
    by the others within thirty seconds, and an edit or a deletion propagates
    the same way.
  - A tenant's MCP tools reach the platform-admin step killswitch (they are in
    `AllManifests` with their owning org) and the AI flow generator, which
    grounds on the tenant-scoped catalog — so an org can describe a flow in a
    sentence and have it composed against the tools it brought itself.

## [0.16.7] - 2026-08-27

### Fixed

- **A flow's language, and its failure notification, actually save now.**
  Setting either in Settings appeared to work and then reverted — reopen the
  dialog and it still said English.

  The editor keeps two hand-written lists of what a flow's settings are, and
  both were incomplete. It rebuilds the document it saves from its own React
  state (the canvas is the truth for steps and wires), so a field missing from
  that list is dropped by the next autosave; and the settings dialog passes its
  fields as explicit overrides, because a `setState` a few lines earlier hasn't
  applied yet — read state there and you save the value the user just replaced.
  Language was absent from both lists, so it never survived a save at all.
  **failure_notify was absent from one**, which meant a flow configured to
  alert you on failure stopped alerting after the next canvas edit.

  There is now one list, in `lib/graphMeta.ts`, and the compiler checks it: add
  a field to a flow and TypeScript names it until you say whether it's a
  setting or part of the flow's structure. Three tests cover the rest — the
  dialog saves the language you picked and shows it when reopened, and a flow
  with every setting populated gets them all back after a save.

  The language field's label is also properly associated with its control now;
  it was announcing as an unlabelled dropdown to a screen reader.

## [0.16.6] - 2026-08-27

### Fixed

- **A flow's language, and its failure notification, survive the next save.**
  Setting either in Settings appeared to work and then quietly reverted. The
  editor rebuilds the document it saves from its own state, field by field,
  rather than writing back the graph it loaded — deliberate, since the canvas
  is the truth for steps and wires — so a graph-level field it doesn't know
  about is dropped by the next autosave. Language was never added to that list;
  **failure_notify never was either**, which meant a flow configured to alert
  you on failure stopped alerting after the next canvas edit.

  A test now loads a flow with every graph-level setting populated and requires
  them all back after a save, so the next field added to Settings fails there
  rather than in someone's flow.

## [0.16.5] - 2026-08-27

### Fixed

- **A dropdown whose own "empty" choice wouldn't stick.** The Date & time
  step's Language field offered "Follow the flow's language" and then refused
  to hold it: picking it snapped back to "(not set)". The form adds a blank
  "(not set)" entry to any optional dropdown without a default, and this one
  already had a blank entry of its own — two options with the same value in one
  select, which the browser resolves by showing the first. The form now steps
  aside when a step names what empty means for itself, which also fixes
  Stripe's refund reason ("(none)") and the Fortnox invoice filter ("All").

  Following the flow's language is now the Date & time step's **default**, not
  just an option, so a step added to a Swedish flow writes Swedish without
  anyone setting it.

## [0.16.4] - 2026-08-27

### Fixed

- **0.16.3 didn't build.** The Swedish field vocabulary gained a second
  `"Language"` entry — the Date & time step's new field name, which the file
  already carried for another step — and two properties with the same name in
  one object literal is a TypeScript error, so `tsc` failed the web image
  (`TS1117`). The duplicate is gone; the label resolves through the entry that
  was always there.

## [0.16.3] - 2026-08-27

### Added

- **The platform's own emails speak Swedish.** Invitations, the welcome mail,
  email verification, password resets, flow-failure notices, approval requests
  and their outcomes, and the two support notifications now go out in the
  reader's language — including the dates inside them.

  Whose language it is depends on the email, and that is the point rather than
  an accident:

  - Mail to an account holder — verification, reset, welcome, a failed flow, a
    support reply — uses **that person's own** language preference.
  - An **invitation** has no account behind the address, so it follows the
    person doing the inviting, who knows who they are writing to. If the
    invitee already has an account, theirs wins.
  - Mail a **flow** sends (an approval request and its outcome) follows the
    **flow's** language, the same field the Date & time step reads. Its
    recipients are addresses typed into a step and often have no account here
    at all, so there is frequently no preference to read — and it is the flow
    speaking either way.

  Anything not translated falls back to English rather than to blanks, and a
  language lookup that fails sends the English mail rather than no mail.

  The two support-queue notices — a customer replied, a ticket was filed — stay
  English on purpose: they go to the operator's own staff at an address that
  comes from configuration, and a config-file address carries no language to
  read.

### Changed

- **An email's plain-text half is generated from the same content as its HTML.**
  Every notification used to hand-build the two separately — the same facts and
  the same link, written twice — and they had drifted: the text version of an
  approval request carried the run's URL and the HTML one didn't. One source
  now renders both, so they cannot disagree, and a translated string is
  translated once instead of twice.

### Added

- **Flows write dates in your language.** Set **Flow language** under Settings →
  General and the Date & time step's day and month names come out in it —
  `torsdag 27 augusti` rather than `Thursday 27 August`. Swedish is the second
  language; anything else reads as English.

  It is the *flow's* language, not yours, and that distinction is the whole
  point: a scheduled run has no one logged in, and the person receiving what a
  flow sends is rarely the person who built it. Your own interface language
  stays in account settings and changes only what you see.

  Casing follows the language rather than a rule — Swedish writes `måndag` and
  `augusti` lowercase, English capitalises — so `D MMMM` is right in both
  without anyone typing a different format. A single step can override the flow
  (its own **Language** field) for the one message that has to differ.

  **The machine formats stay English, deliberately.** ISO, Unix and Email/HTTP
  are read by machines, and RFC 1123 *mandates* English abbreviations: a
  Swedish `tors, 27 aug` in a `Date:` header is a malformed header, not a
  translation. A Go reference layout saved in an older flow stays English too —
  it asked for English by construction, and changing it would quietly alter
  what those flows already send.

### Fixed

- **A step card no longer shows `${…}` reference syntax.** A card chipped a
  field whose value was *entirely* one reference and printed raw text for
  everything else — so the exact shape the `{ }` menu produces, a reference
  inserted into a sentence at the cursor ("Deadline: `${upstream.date_1.out}`"),
  was the shape that leaked the syntax onto the canvas. References now render
  as chips wherever they appear in a value, with the text around them intact,
  and a reference to a secret is styled as one so a credential in a field is
  recognisable at a glance.

  Five other places on the card showed a reference as something worse than
  itself: a resource picker showed a never-ending "…" while it tried to resolve
  a reference as an id, a schedule ran a reference through the
  cron-to-words reader, an interval reported "no interval", and a location
  summary printed the raw token. All of them now show the reference.

  A dropdown whose value is a reference shows the reference's words too — an
  `<option>` can hold only text, so it can't be a chip, but it was printing the
  raw `${…}`.

## [0.16.2] - 2026-08-26

### Added

- **Weekday as a format.** Pick **Weekday** and a date comes out as "Thursday";
  **Weekday, short** gives "Thu". With an offset of `1d` on a Monday you get
  "Tuesday". It was already reachable as `dddd`/`ddd` in a custom format, which
  is no help to anyone who hasn't learned the tokens — and getting the day's
  name out of a date is common enough to be one of the named options. (The
  names come out in English, the same as a custom format's `dddd`.)

### Removed

- **"Move to weekday" is gone from the Date & time step.** It shipped in 0.16.1
  on a misreading of a request for the Weekday *format* above: it moved the
  date forward to the coming named day rather than writing the day's name, which
  next to a list of formats is not what the label sounded like. The format is
  what was wanted, so the field goes rather than sitting in every Date & time
  step as a thing to scroll past. A flow that set it keeps the stored value
  harmlessly; nothing reads it any more.

## [0.16.1] - 2026-08-26

### Added

- **"Move to weekday" on the Date & time step.** Pick Monday and you get the
  coming Monday's date; pick Monday with an offset of `1d` and you get Tuesday.
  Today counts as a match, so a flow that names "the coming Monday" and runs on
  a Monday morning means today rather than skipping a week. For the current
  week's Monday — the "week beginning" label — add an offset of `-7d`, which the
  field's own help says.

- **A searchable timezone picker.** Type "stockholm", "new york", "GMT" — every
  IANA zone your browser knows, each row showing its current offset, which is
  how people actually recognise a zone and what tells Europe/Dublin from
  Europe/London in the two months a year they differ. Arrow keys and Enter
  work; a name the list doesn't carry can still be typed in, so nothing the old
  text box could express is lost.

  The list comes from the browser rather than a table we ship: the tz database
  changes a few times a year, and a bundled list would be stale from the day it
  was written and need a release to fix.

  It's the picker for every timezone field, not just this one: Google
  Calendar's "List events" had already asked for one and had been rendering a
  plain text box for want of anything behind the name, and the Schedule
  trigger's own time zone now gets it too.

  Timezone now also decides the *calendar*, not just the label: the weekday
  jump and the day the clock is set on are both worked out in the zone you
  picked. Late on a Thursday in UTC it is already Friday in Sydney, and "the
  next Friday" differs by a week between those two readings.

### Changed

- **The two time formats are a matched pair.** "Time" and "Clock" — names that
  said nothing about each other, one with seconds and one without — are now
  **Time, 24-hour** (14:05:09) and **Time, 12-hour** (2:05:09 PM). The old
  pairing is what sent people off to write a custom format to get a 12-hour
  clock the step already had. Flows using the old names keep rendering exactly
  as they did.

### Fixed

- **A dropdown no longer misreports a value it doesn't list.** A param holding
  something the dropdown didn't offer — a retired option, or one of the many
  timezones no list can hold — displayed as the FIRST option while the flow
  still ran the stored value. The form said one thing, the run did another, and
  a single idle click made the form's version true. The stored value now gets
  an entry of its own. This is every step's dropdowns, not just Date & time's.

## [0.16.0] - 2026-08-26

### Changed

- **The Date & time step's format field is one you can actually type.** It took
  a Go reference layout — `02/01/2006` for a European date — and that failed in
  the two ways people actually write a format. `YYYY-MM-DD`, the spelling
  everyone reaches for and the one this step's own example was titled with, is
  not a layout: it came back verbatim, so the literal text "YYYY-MM-DD" went
  into the email, with no error anywhere. And a literal word sharing letters
  with the reference date was silently rewritten — `Due Monday 2 January`
  rendered as "Due Thursday 27 August".

  Format is now a named dropdown (ISO-8601, Date, Time, Date and time, Unix,
  Email/HTTP, Clock) plus **Custom**, where you write it from the tokens
  spreadsheets and date pickers already use: `DD/MM/YYYY`, `ddd D MMM YYYY`,
  `HH:mm`, `hh:mm A`. Literal words go in square brackets — `[week of] D MMM` —
  and are left exactly as typed, because a custom format is now rendered token
  by token rather than handed to Go. An unknown token **fails the step** and
  says what to write instead, rather than printing itself into your message; a
  near miss gets named ("mmm" → did you mean "MMM"?), including the
  case-sensitivity that trips everyone once (MM is the month, mm the minute).

  The Custom format field appears only once you pick Custom, so the everyday
  case is one dropdown and nothing else — a permanent second format box beside
  a format dropdown reads as two ways of saying the same thing, and invites
  filling in the wrong one. Switching away keeps whatever you typed, so
  flipping to Date and back doesn't lose it.

  Formats already saved in your flows keep working: a Go layout typed into the
  old field renders exactly as it did. And `YYYY-MM-DD` sitting in that field
  from before now renders as a date instead of as itself.

### Added

- **"At time of day" on the Date & time step.** The offset alone could only say
  "24 hours from now", so a deadline built from it drifted by however late in
  the day the flow happened to run. Set At to `09:00` and Offset to `1d` and you
  get tomorrow morning — in the step's own timezone, so nine means nine where
  the reader is. `00:00` doubles as "start of the day", for a date that doesn't
  carry the run's clock time.

### Fixed

- **The inspect button no longer hides on small screens.** Below 1100px the
  Inspector is a fullscreen overlay and the floating button at the top right is
  the only way to open it — but that button appeared only once a step was
  already selected, which hid the control behind the very interaction it exists
  to complete. It is now always on screen at those widths and simply disabled
  when nothing is selected, with a title that says to pick a step. The banner
  stack was also inset to clear it, so the button no longer sits on top of a
  banner's own actions.

- **"Publish changes" is clickable again, and publishing is reachable on a
  phone.** Two separate ways the editor's publish control went missing.

  The draft-vs-live readout under the toolbar — the line that says edits are
  saved but not live — offers a "Publish changes" link. It rendered, it
  underlined on hover, and clicking it did nothing: it sits in the canvas's
  banner overlay, which passes taps through to the nodes underneath, and this
  one action never opted itself back in. It now does, the same way the
  connection banner's buttons already did.

  And the Live switch and "Update live" button sat in the scrolling half of the
  toolbar, so on a phone — or any window with the Inspector open — they were
  off the right edge, with only a faint fade to suggest a sideways swipe
  existed. They are now pinned next to Run, which is what the toolbar's own
  layout comment already claimed. At phone widths the status chip goes
  icon-only to make room, so Run stays on screen.

## [0.15.9] - 2026-08-26

### Fixed

- **The Email app's From address takes a name, not just an address.** Typing
  `Reports <reports@example.com>` on the Email integration page — the form
  every mail client shows a sender in — got the send rejected by the mail
  server. The whole string was handed to the SMTP envelope as well as to the
  header, and `MAIL FROM:<Reports <reports@example.com>>` is not an address any
  server will accept.

  The two are now told apart: the display name rides the `From:` header, where
  the recipient's client reads it, while the envelope carries the bare address.
  A non-ASCII name is MIME-encoded on the way out, so it arrives as itself
  instead of as mojibake, and a plain address still goes out exactly as typed.

  This covers both places the app's sender is used — the Email step and the
  "Send test" button on an email template. It is the same split the platform's
  own transactional mail (`DAZYFLOW_SMTP_FROM`) has always done; all three now
  share one implementation, so they can't drift apart again.

  **Test connection** says so earlier too: a From address that isn't an address
  at all is caught there, rather than surfacing much later as a raw SMTP
  rejection in the middle of a run.

## [0.15.8] - 2026-08-26

### Changed

- **Dropdowns read as words, in your own language.** A choice field shows its
  raw value when the param carries no display names, and fourteen of them
  didn't: the Regex step offered `extract / replace / split / match`, Hash
  offered `sha256`, MQTT offered `0 / 1 / 2`, Drive offered `docx`. Those are
  API vocabulary, correct in the flow and wrong on screen.

  They now read as what they mean — "Extract matches", "SHA-256",
  "1 — at least once", "Word (.docx)" — translated like every other dropdown,
  with the units spelled out where a word alone didn't say enough ("Metric (°C,
  m/s)", "Kelvin (K, m/s)"). MQTT's QoS levels name their delivery guarantee,
  and Gmail's fetch detail says "Everything / Headers only / Smallest" instead
  of `full / metadata / minimal`. Deduplicate rows also gains the field label
  it never had.

  **The stored values are untouched**, so flows, the API and the MCP tools keep
  the names they always had. HTTP methods deliberately keep theirs on screen
  too: GET and POST are what a user knows them by, and "Fetch" would be worse
  than the raw value, not better.

  A test now asks every enum for its labels, so the next one can't ship without
  them — with those four HTTP fields named as the deliberate exception.

  The same sweep over the choices that aren't drop params found three more: a
  member's role read `viewer / editor / admin` in the org's member list and its
  invite form, though the words for those roles were already written and
  translated elsewhere; a platform tier's plan read `free / pro`; and an
  optional dropdown's blank entry read `(unset)` in every language. All three
  now show the same words the rest of the product uses. Flow-level choices —
  visibility, a connection's error handling — were already translated and are
  unchanged.

### Added

- **The Regex step can rewrite several different words in one pass.** Fill in
  its new Replacements table — `Clouds → Molnigt`, `Rain → Regn` — and each
  match becomes whatever its row says. A match no row mentions is left alone,
  because the table lists what to change and nothing else is the step's
  business.

  Leave Pattern empty and the words to look for are the table's own keys, so
  the everyday find-and-replace needs no regular expression at all. Write a
  pattern and it decides what counts as a match while the table decides what it
  becomes — which is how `(?i)clouds` catches "CLOUDS" and still writes
  "Molnigt" (a match is looked up exactly, then case-insensitively when exactly
  one row covers it; two rows differing only in case are left alone rather than
  guessed at). Keys are matched literally, so punctuation needs no escaping,
  and a longer key wins over a shorter one that starts the same way — RE2
  alternation is leftmost-first, and "Rain" listed before "Rain shower" would
  otherwise eat the first two words of the longer phrase.

  This replaces reaching for a conditional replacement like
  `(?1Molnigt)(?2Regn)`, which is Boost/PCRE syntax that Go's RE2 template has
  no notion of — it came out in the text verbatim. What that expresses is a
  lookup, and a table says it directly.

  Pattern has left the schema's required list, since the table can stand in for
  it. A step with neither is now caught by a lint rule instead, which is
  strictly better: it understands the mode, so it can say "give it an
  expression, or fill in its Replacements table" while the flow is still being
  written rather than failing at run time.

### Fixed

- **A run's ports are named the way the canvas names them.** The Inputs and
  Output sections listed each port by its wire id — `rows`, `html`, `out` —
  while the canvas names the same pins with the drop's own labels, translated:
  "Rader", "HTML-tabell". One run, read in two vocabularies. They now use the
  labels, with the id kept on hover for anyone writing a `${upstream.…}`
  reference by hand. A variadic port, which arrives as `items[0]` and `items[1]`
  and no manifest declares, reads as "Indata 1" and "Indata 2".

- **A run now shows what each step received, not just what it produced.** The
  step detail has had an Inputs section since it was written and it has never
  once appeared: it reads `inputs` off the node record, and a node record only
  ever carries what the node PRODUCED. The dispatcher enqueues a record holding
  the graph and node id, the engine assembles the inputs in memory when it
  executes, and nothing writes them back.

  They didn't need writing back — they're recoverable exactly. The run keeps its
  own graph, and every step keeps its outputs, which is everything the engine
  used to build the inputs in the first place; the API now rebuilds them through
  `engine.AssembleInput`, the same function the run itself went through. Calling
  the engine's own rather than re-deriving "the upstream output for each edge" is
  what keeps the explanation honest: variadic fan-in, fallback edges that carry
  no data, and the one→many auto-lift are all decisions that live there, and a
  second implementation would quietly disagree with the first.

  This makes a run readable in a way it wasn't: a sort step now shows the rows
  going in unsorted and coming out sorted, instead of one half of the story.
  Nothing new is exposed — an input value IS an upstream step's output value,
  already on screen a row below, behind the same permission check. Storing them
  instead would have duplicated every value once per consuming edge.

  Best-effort, so the section is absent rather than wrong: a run whose graph is
  gone, a step whose predecessor was pruned by retention, or a drop no longer
  registered leaves it out. Steps inside a for-each body stay empty too — the
  fan-out feeds them, not an edge.

- **A renamed step is called that everywhere, not just on its card.** Storing
  the name was half the job: three places derived a step's name from its drop's
  manifest and so kept showing the old one. The run timeline now names each step
  the way the canvas does — a run is where you go to read what happened, and
  reading it under different names is the whole problem — and so do the
  reference picker's `${upstream.…}` entries, on both the daemon and the editor
  side, where a reference reads as "<step> · <port>".

  One place deliberately still shows the drop's name: the flow a support agent
  can view. A step name is free text somebody typed, so it belongs with the
  params that view redacts rather than with the structure it keeps.

- **Renaming a step now sticks.** The inspector has always let you rename a
  step, and the name went nowhere: the save wrote a node's id, module, params,
  position and debug flags, and the load re-derived the name from the drop's
  manifest. So a rename reverted on the next reload — with an autosave in
  between, reporting "Saved".

  A step's name is now part of the node (`label`), and the editor keeps it
  through the paths that rebuild a node: the mount load, undo/redo, and the
  watch that reconciles an edit made elsewhere. Only a name you chose is
  stored; a step still called after its drop carries no label at all, because
  the default is the drop's own LOCALIZED label and writing that out would pin
  one language into the flow.

  It counts as canvas presentation rather than publishable behaviour — the
  engine never reads it and nothing outside the editor shows it — so renaming a
  step doesn't raise the "publish your changes" prompt, exactly like moving one.
  A flow's own name is deliberately not in that category: it reaches people
  through the flow list and failure mail.

- **A column's custom name is kept when you click away from the box.** Leaving
  the field by any route that moves focus — Tab, another field, another step —
  already saved. Clicking the canvas did not, and that is the ordinary way to
  leave a panel: React Flow calls preventDefault on the pane's mousedown so it
  can start a drag, so focus never leaves the box and no blur fires; the click
  then deselects the step and takes the panel with it, and React fires no blur
  on unmount either. The name was discarded without a trace.

  Anything still being typed is now written when the panel goes away — an open
  row's heading, a column re-pointed in an open row, or a column half-entered
  in the add row. Nothing is written when nothing was being typed, so opening a
  step and closing it still leaves the flow alone.


## [0.15.7] - 2026-08-26

### Fixed

- **"Run it once and I'll know your columns" now happens.** Make a table's
  column editor asked for the columns this step *received* — and a node record
  never carries them. The dispatcher enqueues a record whose Job holds only the
  graph and node id, the engine assembles the inputs in memory when it
  executes, and nothing writes them back. So the field it read was always
  empty, the advice in its empty state could never come true, and for any
  producer that can't declare its own fields — a JSON step, a query, an HTTP
  call — the list stayed empty for ever, leaving nothing to reorder, hide or
  rename.

  It now reads the columns off the PRODUCER's output, which the run does store:
  live from the run stream right after you press Run, and fetched back from the
  stored run after a reload. Make text's preview had the same dead read and the
  same fix — its real-rows path only ever worked when the canvas happened to
  hand the rows over live.

  Columns are also collected across all the rows rather than from the first one.
  A ragged CSV, an API that omits nulls, a merged rowset — the first row is a
  sample, not a schema, and a column that appears in row two was simply
  invisible.

### Changed

- **A table's columns and their headings are one list again.** The heading
  arrived last release as its own params field, which split one decision across
  two places: you added a column in the column editor and renamed it in a field
  below, with nothing on screen connecting the two. Reading the form told you
  neither that they were related nor which won.

  A row in the column editor is now the pair — the column as it appears in your
  data, an arrow, and an optional custom name for the heading over it. The add
  row carries both boxes, so a column and its heading are typed in one go;
  leaving the custom name blank uses the data's own name. Editing a row opens
  the same two boxes, and the column half is editable too, so a row can be
  re-pointed at a different field instead of only renamed.

  The `column_labels` param still works and is still the shape to write from an
  LLM or the API — it renames without restricting which columns show — it just
  isn't a second place to do it by hand. Two things the pair made checkable: the
  add row no longer commits when focus moves between its own boxes (which made
  the second box unreachable), and two rows can't be pointed at the same column.

## [0.15.6] - 2026-08-26

### Fixed

- **Renaming a table column was not reachable from the editor.** The previous
  release made a heading and a data column two separate things, but the only
  GUI for it was the canvas-side column editor — which can only offer columns
  it has discovered, and discovery works for almost nothing: the input-fields
  probe answers only for declared row sources (a form, a spreadsheet), and the
  "run it once" advice in its empty state cannot work at all, because a node
  record never stores the inputs it received. Feed a table from a JSON step, a
  query or an HTTP call and the list is permanently empty, with nothing to tap
  and rename.

  Make a table now has a **Column names** field in the form: a plain
  {column → heading} map. It needs no discovery and no column list, renaming
  one heading doesn't mean enumerating the rest, and it doesn't hide the
  columns you didn't mention — the trap in doing this through `columns`, which
  also restricts the table to what it lists. A heading set on a column in
  `columns` still wins over the map, the same most-specific-wins shape as Sort
  rows' Direction against its per-column prefixes.

  Two smaller things came with it: a name/value map field can now show an
  example in each box (`customer_email` → `Customer`), because two empty boxes
  and a "key" placeholder is what made this look absent; and the column
  editor's empty state now says what actually helps instead of promising a run
  that changes nothing.

## [0.15.5] - 2026-08-26

### Added

- **You can download your own data.** Settings → Your data hands you one JSON
  file: your profile and sign-in details, your org memberships and invitations,
  your API keys (when each was made and last used — never the key itself), the
  flows in your workspace and your recent runs. GDPR's right of access and to
  portability is a right to *receive* the data, so it can't require asking
  anyone, and the card lists what's in the file before you commit to a click.

  The daemon has assembled this document since the support work; nothing in the
  app ever asked for it, so the honest answer to "can I have my data?" was "yes,
  if you can write a curl command". The file is fetched and then saved from
  memory rather than linked to — the endpoint needs an Authorization header, so
  a plain link to it downloads a 401 — which also means a refusal lands in the
  card instead of inside a saved file. Filenames are dated, because a folder of
  files all called `dazyflow-my-data.json` says nothing about which is current.

  The full definition of every flow still comes with the organisation export,
  next to Delete in the organisation switcher. Collection rows and run logs are
  deliberately not in the personal export: rows your flows saved are usually
  *other people's* data that you are the controller of, not your own personal
  data, and they already download as CSV per collection.

- **Collections sorts by column, and the download follows.** Click a header to
  order by it: ascending, descending, then back to the order the rows were
  saved in — that third state matters, because insertion order is a real answer
  for a collection and there was otherwise no way back to it but a reload. The
  sorted column holds full ink with an arrow, and the header carries `aria-sort`
  so it isn't only the arrow that says so.

  The CSV button reads the same array the table renders, so a download can
  never disagree with the page it was asked for from — a spreadsheet ordered
  differently to the screen is one nobody can check. Search and sort share
  their scope (the loaded rows, as the "first N loaded" note already says).

  Values sort by what they are rather than how they're spelled: the store is
  all TEXT, so "9" would otherwise come after "100". Blanks stay at the top in
  both directions, so flipping the direction doesn't drag empty rows through
  the data — the same rule the Sort rows step applies. Text collates by the
  reader's own alphabet, which is the one deliberate difference from the engine
  comparator: å sorts after z for a Swedish reader and beside a for an English
  one.

- **A table can be named.** Make a table produced a bare grid: sending two of
  them in one email left the reader to work out which was which. The new Table
  name field renders as the table's `<caption>` — the element HTML has for
  exactly this — so the name travels inside the `<table>` and stays attached to
  its rows when the markup is pasted into an email body or a message, where a
  separate heading line comes apart. It takes a reference like every other text
  field, so the name can carry the run's own data
  ("Orders for ${upstream.today.out}").

  Left blank, the output is byte-identical to before. With zero rows the
  `empty` fallback still stands alone — a caption over nothing is a heading
  for a table that isn't there.

- **Sort rows has a direction toggle.** The step could already sort descending
  — by typing a `-` in front of the column name, a convention documented in
  the field's help text and nowhere a user would look. "Newest first" is the
  most ordinary thing to ask a sort for, and it was reachable only by knowing
  a punctuation trick.

  There is now a Direction control next to Sort by: Ascending or Descending,
  both visible, one click apart. It sets the direction for the whole sort, and
  a column that states its own — `-revenue`, or the new `+name` for ascending
  — keeps it, so a multi-column sort can still mix the two ("highest revenue
  first, ties alphabetical"). `+` exists because without it that sentence was
  unsayable once Direction was descending: every unprefixed column followed it
  down and the only escape hatch pointed the same way. Saved flows are
  untouched — no Direction means ascending, exactly as before — and a
  misspelled direction is now an error rather than a silent sort the wrong way
  round.

  The control is a segmented toggle rather than a dropdown, which two-value
  enums can now opt into (`"format":"toggle"`) instead of hiding half the
  answer behind an interaction.

- **A comment can be coloured.** The colour was already stored, round-tripped
  and drawn — the note's tint, its border and its resize handles all read it —
  but nothing could change it, so every note on every canvas was the same
  violet. Grouping two halves of a flow meant two boxes that looked identical.

  Selecting a note now shows a row of swatches next to its delete button: six
  hues, the ones the canvas already uses for step categories, so a coloured
  note reads as part of the drawing. A fixed palette rather than a colour
  picker, because the tint is mixed at 9% over the canvas and 55% into the
  border, and most of the values a picker offers come out as an invisible wash
  or an unreadable border in one of the two themes. The row wraps under the
  title on a note narrowed to its 140px minimum, and it stays out of the way
  entirely on a canvas you can't edit.

### Fixed

- **Collections shouted your column names back at you.** The table shares its
  styling with every other table in the app, where a header is a fixed label we
  wrote and small-caps reads well — `STATUS`, `STARTED`. In Collections the
  headers are data: your own column names, upper-cased into something that
  appears nowhere else. `orderTotal` came out as `ORDERTOTAL`, which is neither
  the name to type into a step nor the name the CSV button on the same page
  writes. Column names now print exactly as stored, case and all; every other
  table keeps its labels.

- **Renaming a column in Make a table emptied it.** The column editor has
  always offered "tap a column to rename it", and with only a name to write it
  wrote the new name into `columns` — which is the list of *fields the cells
  come from*. So renaming `customer_email` to "Customer" produced a table with
  a correct-looking "Customer" header and nothing at all underneath it: no row
  has a field by that name, so every cell in the column rendered blank. The
  table looked deliberate, and it went out in whatever email the flow sent.

  A column now carries both facts. `columns` takes
  `{"column":"customer_email","label":"Customer"}` beside the plain names it
  always took, the header reads the label and the cells still read the column,
  and the editor writes an object only for a column that has actually been
  renamed — so a flow that never renamed anything keeps the exact param it had.
  A renamed row in the editor names the field it reads underneath the header
  text, so a rename is visibly a rename rather than a re-point, and clearing
  the box goes back to the data's own name.

  Flows saved with a column renamed the old way can't be recovered
  automatically — the original field name was overwritten and isn't anywhere
  to read it back from. Those columns still render as they do today (blank);
  re-point them at the right field and the rename will hold.

- **The editor asked you to publish changes it then reported as no changes.**
  Tidying the canvas — moving a step, dropping a note, bending a wire — saves,
  and the toolbar took any save to mean the live version had fallen behind:
  "Published, but your draft has changes that aren't live yet". Opening the
  diff it links to answered "your draft matches the published version",
  because that view has always ignored cosmetics. Two readouts of the same
  pair of revisions, disagreeing, with no way to tell which one was lying.

  The prompt now asks the question it displays: would publishing change what
  the flow does? Canvas layout, notes and wire routing are excluded, as is the
  pause switch — pausing takes effect from the draft the moment it is saved, so
  it was never something to publish either, and pausing a flow used to raise
  the same phantom prompt. Everything else still counts, including a step's
  breakpoint, its non-critical flag and its timeout, which the diff view had
  not been itemizing. The two now share one definition (`core.BehaviorEqual`
  and the cosmetic set in `diffGraphs.ts`), a test fails if a new field on a
  flow slips past either of them, and the diff view reports "other" rather
  than claiming a match it cannot account for.

## [0.15.4] - 2026-08-26

### Added

- **The editor warns when a script's language contradicts what will run it.**
  A Text step set to Python feeding a Run-on-your-machine step set to run with
  `bash` is a flow that fails on the machine with a pile of syntax errors, and
  each node looks correct on its own. Nothing at run time can tell them apart —
  a script arrives as a string, and a string carries no language — so the
  contradiction is caught while it is still being written.

  A second finding covers the other shape: a script that says it is SQL, YAML or
  JSON. Those are data formats, so no choice of interpreter makes them runnable
  and the advice is different.

  It is a warning rather than the interpreter being picked automatically from
  the wire. Reading the language off the incoming script was the obvious
  alternative, and it would have made the most consequential fact in the product
  — which program runs on a machine you own — depend on a node you cannot see
  from the step. The step keeps saying what it runs; the editor says when that
  contradicts what it was given.

  It also needed no new value on the wire: the language is a param on the
  upstream node, so the rule is static graph analysis. Lint findings can now
  carry data (`values`) as well as field paths, so the sentence is localised
  rather than falling back to English.

## [0.15.3] - 2026-08-26

### Fixed

- **The JSON node's editor on a card had its caret offset from the text.** The
  overlay editors style their two layers with a bare class, `(0,1,0)` — which
  loses to `.dz-node-params textarea` at `(0,1,1)`, the rule that styles fields
  on a node card. So on the canvas the textarea took 4px/6px padding while the
  highlight layer behind it kept 8px, and the caret sat a few pixels off the
  text: the metrics matched in the stylesheet and not on the screen.

  Every layer is now written as a child of its own editor, putting the pair at
  `(0,2,0)` and out of reach of whatever ambient form-control rules exist
  wherever the editor is dropped. The overlay guard enforces that shape, so a
  layer cannot lose its scoping later — and it is what made it safe to put the
  highlighted editor on a card at all.

### Changed

- **A Text step that holds code says so on the canvas.** Its box on the card is
  the real editor when a language is set — monospace and syntax-coloured, the
  same treatment the JSON node's card box has always had — and the card carries
  a chip naming the language. A node holding a SQL query
  and one holding an email body are different nodes to anyone reading a flow,
  and they used to look identical.

  The chip appears wherever a step names its language, so the runner step's card
  shows the interpreter it will use too. "No language chosen" is the param's own
  default, which is how one rule covers both steps without knowing that Text
  spells it `plain` and the runner spells it `default`.

  Its icon says what KIND of thing the language is — a terminal, a database, a
  pair of braces — rather than which language it is. Only three of the seven on
  offer have a mark anyone would recognise (Python, JavaScript, PowerShell); SQL
  is a standard rather than a product, and YAML and shell have no logo at all,
  so a row of three real brand marks beside four invented ones would read as
  broken. The glyph groups; the label identifies.

## [0.15.2] - 2026-08-26

### Added

- **The Text step can hold code.** Set **Written in** to a language and the box
  becomes a code editor: monospace, syntax-coloured, and it stops wrapping long
  lines. Shell, Python, JavaScript, SQL, YAML, JSON and PowerShell. It is how
  you keep a query, a script or a chunk of config in a flow — and it feeds the
  runner step's **script** input, so a script can live in its own node and be
  shared between steps.

  A setting on the existing step rather than a new "Code" one, deliberately. The
  value-source family earns a separate node by what it PRODUCES — JSON emits a
  parsed object, Number a number, URL validates — and a code node would emit the
  same plain string Text already does. So this changes the editor and nothing
  else: what comes out is byte-identical either way, and every Text node that
  already holds a pasted script upgrades in place instead of needing to be
  swapped for a different node and rewired. Searching the palette for `code`,
  `script`, `sql` or `yaml` now finds it.

  It stays on **plain text** by default, because most of what goes in one of
  these is prose — a system prompt, an email body — and prose in a monospace box
  reads worse, not better.

### Changed

- **A released changelog section can no longer be edited by accident.**
  `make patch` promotes `[Unreleased]` under a new version heading and leaves a
  fresh empty one above it — so between releases, the heading that WAS the place
  to write becomes the one place you must not, and nothing about the file looks
  different. Writing under the old one breaks two things at once: the next
  release refuses with "[Unreleased] is empty", and the previous version's notes
  claim work that is not in its tag.

  `make check-changelog` compares the newest released section against the commit
  that released it and fails if they differ. It runs as part of `make check`,
  `make ci`, CI, and — where it matters most — as a prerequisite of every
  version bump, so a release cannot bake in an entry filed under the wrong
  heading.

- **Removing an environment variable asks first.** The value is often a
  `${secret.…}` reference, so a mis-click on the × cost a trip back to the
  secret picker to rebuild it. The row now stays on screen with a "Remove
  API_TOKEN?" prompt underneath it, so what is about to go is still visible
  while the question is being answered.

  Deliberately not on every name/value map in the app: a confirm on every row of
  every map is one people learn to click through, and then it is not protecting
  the one that matters. It is a per-field setting, turned on where the value
  costs something to reconstruct.

## [0.15.1] - 2026-08-26

### Fixed

- **Adding an environment variable was close to impossible.** The row was
  derived from the object it edits, so it existed only while its name did:
  clearing the name to retype it dropped the entry and the row vanished from
  under the cursor. Naming a variable was only possible by editing around the
  old text without ever emptying the box. The editor now owns the list of rows
  and hands up only the ones that have a name, so an empty name means "being
  typed" rather than "deleted". A row goes when its remove button is used.

  Two things came with it: a new row starts empty instead of pre-filled with
  `key`, `key2`, `key3` for you to select and delete first; and a name used
  twice is flagged, because the second one wins when the value is built and the
  first row quietly is not what runs. This is the shared name/value editor, so
  every map-shaped setting gets the fix.

- **The caret in the script box was still off after 0.15.0.** That release
  removed two contributors — an italic comment token, and a scrollbar reserved
  in one layer and not the other — but not the cause, and the caret still drifted
  from the text.

  The cause is the technique: it asks two different layout engines — CSS text
  layout in the highlight layer, the browser's text-control code in the textarea
  — to break a line at the same character. Identical CSS is necessary but not
  sufficient, and anything that narrows one content box and not the other moves
  every wrap point below it.

  So **the script box no longer wraps**. Long lines scroll sideways, which
  leaves nothing to disagree about beyond per-line font metrics — and is what a
  code box should do anyway: wrapping a shell pipeline mid-token to fit a narrow
  panel is worse than scrolling it. The overlay guard now covers the wrapping
  properties too, since a wrap disagreement is what actually broke rather than a
  font one.

## [0.15.0] - 2026-08-25

### Fixed

- **The caret sat off the text in the runner step's script box.** The box draws
  a transparent textarea over a highlighted copy of the same text, which only
  works while the two advance character-for-character — and the comment token
  was styled italic. An italic face is a different face with different advance
  widths (and a family with no true italic gets a synthesised oblique, which
  differs again), so everything after a comment on that line sat out of step
  with the caret, and because the line then wrapped at a different column than
  the textarea did, every line below it drifted too. Comments are now coloured
  and not slanted.

  A second, latent cause of the same drift is fixed with it: the textarea
  scrolls and the highlight layer does not, so once the content was tall enough
  for a scrollbar the textarea's text wrapped ~15px earlier than the layer
  behind it. Both layers now reserve the scrollbar's width whether or not one is
  showing.

  `npm test` now fails if a token span is given any property that changes glyph
  width, or if the two layers of one of these editors are given a metric
  separately. The bug looks right in a screenshot and only shows when you put
  the caret at the end of a long commented line, which is why it wants a guard
  rather than care.

### Added

- **A connection can say what happens when the step before it fails.** The
  engine has honoured this since the beginning and nothing in the editor could
  set it, so a flow author had to reach for the API, the MCP tools or the flow's
  JSON. Right-click a connection:

  - **Only if this step succeeds** — the default, unchanged.
  - **Whether it succeeds or fails** — the next step runs either way.
  - **Only if this step fails** — the error handler. Idle on every run that goes
    well.
  - **Retry this step on failure** — offered only where it would do something.
    A step whose drop declares no retry policy shows it greyed with the reason,
    because a setting that silently does nothing is worse than one that is
    visibly unavailable. (The runner step is exactly that case, on purpose:
    nobody can know whether a script already sent the invoices.)

  Each mode is **drawn** — its own colour and its own dash — so a flow's error
  handling is visible on the canvas instead of hidden in its JSON. The default
  is drawn exactly as before, so an existing flow looks identical and colour
  reads as "something was chosen here".

- **A step can be marked as one whose failure doesn't fail the run.** Right-click
  it: *Failing here doesn't fail the run*. For the "announce it everywhere"
  shape — Discord being down is no reason for the Slack post and the email not
  to go out. It carries a chip on the card, because a step that cannot turn a
  run red is a fact about the flow and invisible otherwise.

  It exists as a step setting because the mode above lives on CONNECTIONS, so a
  step at the end of a branch has nowhere to hang one.

  Both settings now survive a save from the editor. Until this release, opening
  an API-built flow and letting autosave run silently dropped them.

- **A script's exit code can be a flow signal instead of a failure.** A non-zero
  exit is not always a breakage — `2` might mean "nothing to invoice today", and
  the flow should take a quiet path rather than fail. Set **If the script exits
  non-zero** to *Carry on* and the step succeeds whatever the script returned,
  handing the flow three outputs to route on: **Output** (stdout as before),
  **Exit code** (as text, `"0"` is success) and **Error output** (stderr).

  All three are emitted either way — on a plain success as well as a handled
  failure — so turning the setting on cannot change what the existing wires
  carry, and a script that succeeded with warnings on stderr still hands them
  over. The default is unchanged: a non-zero exit fails the step, with the
  script's own error output attached.

  What *Carry on* deliberately does not cover: a machine that is switched off, an
  agent that refused the script, or a script the runner had to stop at the
  timeout. Those still fail the step, because there is no exit code to hand over
  and inventing one would send the flow down the "the script ran and said no"
  path when nothing ran at all.

- **Run on your machine can pass environment variables to the script**, and a
  credential can be one of them without ending up anywhere it should not.

  Set names and values on the step; they are merged over the machine's own
  environment, so `PATH` still works and a name you set wins. Use
  `${secret.NAME}` for anything sensitive and the reference is what gets saved —
  the value is substituted one step before the script starts, and is kept out of
  the flow definition (so not in the workspace's git history), encrypted at rest
  in the queued task, scrubbed from the run's output and live log if the script
  prints it, and reduced to a bare name in a support bundle.

  Which leaves exactly one place the value does exist, on purpose: the machine
  you sent it to. A runner is as trusted as the people who can edit your flows,
  and a secret you send one is as trusted as that machine.

  Names that an environment block cannot carry — empty, containing `=`, or
  containing control characters — are refused before the task is queued, rather
  than failing on the machine as something that looks unrelated.

### Changed

- **A run you start yourself no longer emails you when it fails.** Pressing Run
  in the editor, previewing a step in the inspector, firing a test trigger or
  retrying a failed run all put the failure on your screen — so a failure email
  about it is telling you something you are already looking at, and mail like
  that is how people learn to ignore the mail that matters. Runs that start on
  their own — a schedule, a webhook, a form — email exactly as before.

  Both email channels are off for those runs: the per-flow address (often a
  shared or on-call inbox, which really should not be paged because someone was
  testing) and the account-level owner email. **The per-flow failure webhook
  still fires**, because that is a machine channel the flow's author wired
  deliberately rather than a person being interrupted.

  Which kind a run is gets recorded on the run itself rather than inferred
  later: a run can be parked at the concurrency limit and started minutes
  afterwards by something that never saw who pressed the button.

- **A flow that keeps failing now emails once an hour, not once per run.** The
  other way to drown in failure mail: a poll trigger every five minutes against
  a service that is down is twelve identical emails an hour, and twelve
  identical emails teach the reader to filter the lot. The first failure in the
  window mails; the rest are silent, and the suppression is logged so an
  operator asking "why did I not get mail about that?" can find the answer.

  The rule is one clause — no other failure of that flow in the last hour — and
  it deliberately catches the flow that FLAPS (fail, succeed, fail, succeed) as
  well as the one that stays broken; a "first failure of a streak" rule would
  mail on every one of the former. It is per flow, so a noisy flow cannot
  silence a quiet one, and it is derived from the run history rather than from a
  record of what was sent, so it cannot drift out of step with the runs
  themselves. If the store cannot answer, the mail goes: a throttle that eats an
  alert is worse than one that sends a duplicate.

  The failure **webhook is not throttled** — it is a stream for machines, not an
  inbox. `DAZYFLOW_FAILURE_EMAIL_WINDOW` changes the window; `0` restores one
  mail per failure.

## [0.14.0] - 2026-08-25

### Changed

- **Online machines lead when several match.** Work already only ever reaches a
  machine that is running — the step queues the job and machines ask for work,
  so an offline one never receives anything and none of it needs re-routing when
  a machine goes away. What was missing was saying so at the two points where
  something is actually chosen:

  - The tag field now lists tags with a machine switched on first, and each tag
    says how many machines carry it and how many of those are on.
  - Choosing tags that only offline machines carry is now a warning naming those
    machines, instead of a `0 online` count inside a sentence that was easy to
    read past. It is a different problem from tags nothing carries, and it fails
    a run either way.
  - The step's progress line, while it waits, says how many of the matching
    machines are on — and when none is, that it is going to fail shortly unless
    one starts. It used to read "waiting for a machine tagged build" for the
    thirty seconds before giving up, which looks like progress.

- **Run on your machine says where with tags, and only tags.** The step had two
  fields for one question — a machine name and a label, mutually exclusive, each
  with its own matching rule and its own not-found error. There is now one:
  **Where to run it**, a list of tags the machine must carry.

  Three things make that a simplification rather than a loss:

  - **Every machine carries its own name as a tag.** So one tag does either job:
    `invoices-box` runs on that machine and nowhere else, `build` runs on
    whichever machine tagged `build` is free.
  - **All the tags must match.** `linux` `gpu` runs on a machine that is both,
    not on either. Tags narrow; they never widen. The field says how many
    machines carry the whole set and how many of those are switched on, because
    a set that narrows to nothing otherwise fails invisibly until a run.
  - **A tag no machine carries is told apart from a combination no machine has**
    — one is usually a typo, the other usually a second tag too many, and they
    need different fixes.

  Flows saved before this keep running: the old `runner` and `label` params are
  still honoured, each read as a single tag. Nothing needs re-saving, and a task
  already queued survives the deploy that brings this in.

- **Tags are assigned on a machine's own settings page.** Click a machine in
  **Admin → Runners** and you land on it: what it is, whether it is there, and
  the tags it carries. Add and remove them there. 0.13.0 put that editor in a row
  on the list, which was the wrong home for it twice over — on a phone a row is a
  sideways-scrolling strip, and a tag is the thing people come to this section to
  change.

  The page shows the machine's name as a tag it always carries and cannot lose,
  because that rule is invisible otherwise. Tagging one machine with another's
  name is refused: names are tags, so it would make one tag mean two machines and
  quietly send pinned work to the wrong one.

- **`--tags` is the flag; `--labels` still works.** The UI, the docs and the
  installer all say tags now. `--labels` is accepted unchanged, because it is in
  every install command written down so far, and the stored field keeps the name
  `labels` in the API — renaming a synonym is not worth a migration.

## [0.13.0] - 2026-08-25

### Added

- **Labels can be assigned from Admin → Runners.** A machine's labels — the pool
  names a step can target instead of one machine — used to be decided on the
  machine at install time with `--labels` and fixed there forever: moving an
  existing server into another pool meant a visit to it, or deleting the runner
  and re-installing with a fresh token. The label button on a row now opens them
  for editing. Registration is untouched and the credential does not change; a
  label is how this Dazyflow routes work, not something the machine knows about
  itself.

  They are stored lower-cased, trimmed and de-duplicated however they were typed
  — that is what a step has to spell to match one — so a label added as `Build `
  shows as `build` as soon as it saves. Needs `organization:admin` (or
  `module:register`) like adding a machine does, because it reroutes every step
  aimed at the label, and it is recorded in the audit log.

### Fixed

- **Wide tables could not be scrolled on a phone.** Below 640px a table keeps a
  readable minimum width instead of crushing its columns together, so it is
  deliberately wider than the screen — and something has to scroll it. On
  **Runners**, **API keys** and **Git credentials** the overflowing columns were
  clipped by the card instead (Status, Agent, expiry and the remove buttons were
  simply unreachable), and on **Usage** the table pushed past the card and
  scrolled the whole page sideways. All of them now scroll inside their card, the
  way the runs table already did. **Collection results** — the widest table here,
  since its columns are whatever the collection holds — scrolled already, and now
  does it through the same wrapper as everything else.

  A guard (`npm test`) now fails if a table is added without that wrapper. The
  failure was invisible in review, invisible on a desktop, and only ever
  reported by someone holding a phone.

## [0.12.0] - 2026-08-25

### Added

- **The Run on your machine step now asks which machine, and what should run
  the script.** Three fields that used to be typing are now choices:

  - **Machine** is a dropdown of the machines your organisation has registered,
    with the offline ones marked. The name existed in exactly one place a flow
    author could not see from the editor, so a typo surfaced only when a run
    failed with `no runner named "buld-box"`. **Or any machine labelled** is
    likewise a list of the labels those machines carry.
  - **Run it with** picks the interpreter: the machine's own shell (the
    default, and what a runner has always done), `sh`, `bash`, Python,
    PowerShell or Node. Choose one and the agent writes the script to a
    temporary file and starts that interpreter with it — so a Python step is
    Python, with standard input still carrying the value wired into the step
    and tracebacks that name a real file.
  - **Script** — renamed from **Command**, because it always was one — is a
    proper multi-line box, monospace, with the syntax coloured for the language
    you chose. It was a single-line input, which hid everything past the right
    edge of a thing that is many lines by nature.

  The script can also come from an earlier step now: connect the new **script**
  input and it supplies one, built from a template, read out of a table, or
  written by the AI step.

  **Upgrade your agents.** An agent installed before this release does not know
  about the interpreter choice and will use the machine's shell whatever the
  step says. Re-run the install command on each machine; **Admin → Runners**
  shows which version each one is running. An allow-listed agent (`--allow`)
  additionally has to name any interpreter a flow may start — `python`, not the
  script — which is worth reading as what it is: permission to run anything that
  language can do.

## [0.11.0] - 2026-08-25

### Added

- **Flows can run scripts on machines you own.** A **runner** is a machine
  with a small agent on it — a server, a laptop, a container on a network
  this server has never heard of. Add one in **Admin → Runners** and the new
  **Run on your machine** step sends it a command. Whatever is wired into the
  step arrives on the script's standard input; whatever the script prints
  comes back out, ready for the next step. A non-zero exit fails the step with
  the script's own error output attached, so a failing flow tells you what
  your script said rather than a number.

  This is the answer to "no built-in step does what I need". The existing
  answers — an MCP server, or a gRPC remote module — both mean writing and
  hosting a service that speaks a protocol. A runner is a script.

  Nothing has to reach the machine. The agent connects outward and asks for
  work, so a runner behind NAT or a firewall needs no open port, no inbound
  rule and no certificate. There is no form on the page either — no address,
  no key material — because everything Dazyflow knows about a runner arrives
  from the machine itself. Setup is the one command the page hands you:

  ```sh
  curl -fsSL https://your-dazyflow-server/runner.sh | sh -s -- --token dzrt_... --service
  ```

  It downloads one Python file plus a copy of `runner.sh`, registers the
  machine, and installs a systemd **user** service that restarts the agent
  within ten seconds of a crash. It installs no packages, adds no
  repositories and opens no ports; Python 3 is the only requirement. Neither
  file is a compiled binary on purpose — you are about to let them run
  commands on your machine, so both are meant to be read, and both are served
  as plain text so they also open in a browser. `sudo` appears exactly once in
  the whole procedure, for `loginctl enable-linger`, which is the difference
  between starting at boot and starting when you next log in; it is yours to
  run rather than the script's, and the script checks whether it is already
  done instead of telling you to do it again. Afterwards the same file manages
  the agent — `./runner.sh status | start | stop | restart | logs | install |
  uninstall` — so there is no second script to find and no systemd
  incantation to remember.

  Two secrets, with different jobs. A registration token (`dzrt_`) lives 30
  minutes and works once: it is the string pasted into a terminal, which makes
  it the one most likely to end up in a shell history or a chat message. The
  agent's own credential (`dzrc_`) is long-lived and never leaves the machine
  after registration. Both are stored as SHA-256 hashes, so a database dump is
  not a set of working runners — and the installed service file holds no token
  at all, because registration happens before it is written.

  **A script is never run twice.** If the machine goes quiet mid-script the
  step fails and names the machine that went away; the work is not handed to
  another runner and not retried when the machine comes back. Dazyflow cannot
  know how far a script got, and re-running one that had already sent the
  invoices is worse than failing, so retrying is a decision for whoever knows
  what the script does. For the same reason the step fails rather than waits
  when no eligible machine has checked in recently — a run hanging on a
  switched-off laptop looks alive and tells nobody — and work a step has given
  up on is closed rather than queued, so starting an agent after a weekend
  away never sets off a backlog of scripts whose runs finished days ago.

  Target a machine by name, or by **label** so a pool shares the work:
  register three build servers with `--labels linux,build` and a step aimed at
  `build` runs on whichever is free. The agent labels itself with its
  operating system and architecture, so `linux` and `arm64` work without
  anyone setting anything.

  What a runner never gets: another organization's work (it claims tasks for
  the organization whose token registered it, and the queue is keyed by that,
  not filtered by it), a way into Dazyflow (the agent asks for work and reports
  results — it holds no session, can read no flows and can enumerate nothing),
  or the server's secrets (it receives what the step's parameters carry and
  nothing else). Removing a runner revokes its credential immediately, whether
  or not anyone remembers to stop the agent on the machine, and the page asks
  before it does.

  A queued command carries whatever `${secret.…}` references it used, already
  resolved — so the queue encrypts the script, its input and its environment at
  rest under the organization's own key, the same envelope the secret store
  uses. A deployment with no `DAZYFLOW_MASTER_KEY` stores them in cleartext and
  says so at boot, which is the same posture as every other stored secret
  there. The installer that machines are set up with also carries the checksum
  of the agent it is about to download, and refuses to install one that does
  not match.

  A step's output comes back capped at 1 MiB per stream; a script that prints
  more fails with a message naming the limit rather than silently handing on
  half a document.

  What a runner *does* get is the part worth deciding deliberately rather than
  discovering: it runs whatever command a flow sends it, so anyone who can edit
  a flow in your organization can run commands on these machines, as the user
  the agent runs as. That is the same bargain a self-hosted CI runner makes,
  and it is what makes a runner useful — but it means a runner is as trusted as
  the people who can edit your flows. Starting the agent with `--allow` limits
  which programs it will invoke, and an allow-list also turns the shell off —
  the command is parsed by the agent and the program executed directly, so `;`,
  `|` and `&&` are characters in an argument rather than operators. (Without
  that, checking the first word and then handing the whole string to a shell
  would let `./allowed.sh ; anything-else` straight through.) If a command needs
  a pipe, put it in a script and allow the script. Be clear-eyed about what is
  left: this restricts the program, not what that program may then do, so
  allowing a shell allows everything. The page says all of this next to the
  install command rather than in a footnote. Adding or removing a runner needs `organization:admin` or an API
  key carrying `module:register`; using one in a flow needs `graph:edit`, the
  same as any other step.

  On the canvas the step carries the machine it will run on, under its title,
  and reads "no machine chosen" until one is picked. Wiring a secret into a
  step is the moment to know it is leaving the server, and the palette is long
  gone by then. The step's input takes a value, not a file: a file in Dazyflow
  is a path on the *server's* disk, so sending one would fail inside your
  script as a missing-file error you would reasonably read as your own bug. It
  is marked on the port and refused before the job is dispatched instead.

  Runners need Postgres. A registration outlives the daemon — the agent keeps
  its credential forever, so a daemon that forgot its runners on restart would
  tell every one of them it is no longer registered — and the task queue is the
  handoff between the agent's result and the step waiting for it. Without
  Postgres the endpoints answer 501 and the step reports that runners are not
  set up on this deployment, rather than half-working: a registered machine
  that cannot be handed work looks like a fault on the organization's side.
  Task rows follow the job retention window, or
  `DAZYFLOW_RUNNER_TASK_RETENTION` if you want them swept sooner. The full
  guide is **Runners** in the docs.

### Changed

- **A remote module declares every step it serves, and belongs to one
  organization.** This is a **breaking change to the gRPC node protocol**:
  `NodeService.GetManifest` is gone, replaced by `ListManifests`, which returns
  a list of manifests; `Job` now carries `drop_id` so a server hosting several
  steps knows which one a job means. An existing remote module renames one
  method and wraps its manifest in a one-element list —
  `examples/csv-pipeline` shows the shape.

  Plural from the outset, even though a server offering one step just returns
  one entry. Changing the shape later would mean every remote module already
  written updating in lockstep with the daemon, and the whole point of a remote
  module is that its binary is not the daemon's to release. Manifests also
  gained the presentation fields the built-ins have had all along — icon,
  category, subtitle, description, summary, tags — for the same reason: without
  them a remote step lands in the palette as a generic box with nothing for
  search to match on but its id, and adding them afterwards would be a second
  lockstep upgrade.

  The catalog is now keyed by (organization, step id) rather than by step id
  alone. A remote used to belong to nobody in particular, so resolution had to
  *choose* not to cross organizations; now it cannot, because there is no key
  to reach it by. An absent organization matches nothing rather than
  everything, so a background task or a test that forgot its context fails to
  resolve the module instead of quietly resolving someone else's. The palette
  and graph validation are scoped the same way — showing a step an organization
  cannot resolve is bad, and telling it a runner by that name exists somewhere
  is worse. `DAZYFLOW_REMOTES` (development only, as before) accordingly
  accepts `tenant/id=host:port`, and defaults to the seeded dev tenant when an
  entry names none, so the documented local workflow is unchanged. The
  `runner/` id prefix is now reserved: a future built-in claiming it would
  change what an existing flow runs, silently, on upgrade.

- **The Approvals inbox opens the run.** 0.10.3 swapped the Runs page's two
  targets and left this page disagreeing with it. The flow name in an approval
  card was its only link, and it went to the editor — the one surface that
  cannot help an approver, because the editor is read-only while a run is
  parked and its deliberate approve/reject control was removed precisely
  because the editor is graph-scoped rather than run-scoped. It now opens the
  run, which has the approval panel *and* the timeline of steps that already
  ran, which is the evidence the decision rests on. Editing stays one click
  away behind the same trailing pencil the runs list uses.

- **Icon sizes, spacing and stacking order come from scales.** Icons were the
  last unscaled dimension in the UI: 17 distinct pixel values across 491 call
  sites, and an icon inside a `<Button>` — one role — used seven of them across
  207 sites, which is how the Stop button's square came to be 15px in the
  editor and 13px on the run page. Spacing had drifted the same way: 235 raw
  numbers across 51 files, 26 of them off the scale entirely (3, 5, 10, 14, 18,
  30), and 115 of them one idiom — the gap between a button's icon and its
  label, hand-written at 3, 4, 5, 6 *and* 8px for the identical relationship.
  That gap now lives on the button itself, so a call site has nothing left to
  get wrong, and the 41 hand-tuned `verticalAlign` nudges under inline glyphs
  are one em-relative class that tracks the type size instead of approximating
  it per site. Every app-level `z-index` is a named token in one ordered list,
  so "what does this sit above?" is answered by reading it rather than by
  picking a number bigger than whatever was in the way; the two worst offenders
  (1000 and 9999, chosen to win rather than to mean anything) are 400 and 500
  now, with nothing that was between them, so the order is unchanged.

- **The Apps page no longer ends each step with a JSON dump of its params
  schema.** The schema's human-readable half — every field title, help line and
  dropdown option — is what the Inspector's form renders, and 870 of those
  strings are translated for it; the dump showed the untranslated original, so a
  Swedish user opening that disclosure met English JSON describing fields the
  editor shows them in Swedish. The machine-readable half is already served to
  the consumers that want it, by `GET /api/v1/catalog/drops/{id}` (which the MCP
  `describe_drop` tool proxies) and over gRPC. That left 122 schemas and roughly
  4,200 lines of JSON on a page whose job is "what does this app do". The
  disclosure keeps the input and output ports, and no step lost its details
  section: every step declaring a params schema declares ports too.

- **Two English strings had two Swedish translations each.** Collapsing
  duplicated interface text onto shared keys (see *Developer*) surfaced them:
  "Disable" was both *Stäng av* and *Inaktivera*, and "expires {{date}}" was
  both *löper ut* and *går ut* across three keys. One wording each is now used
  everywhere.

### Fixed

- **Fourteen dialogs now close on Escape, and every dialog announces itself as
  one.** Backdrop-click dismissal never needed attention, because it falls out
  of the markup — the backdrop is the element you hang the handler on, so you
  cannot build one without it. Escape and the ARIA attributes fall out of
  nothing, which is exactly why they drifted: of 36 dialogs, 22 closed on
  Escape and 14 did not, so a user who learned it works on the delete-flow
  confirm found it dead on the MCP connect wizard, the report-a-problem,
  reveal-secret, share-overview, issue-key and plan-limits dialogs, the
  editor's diff, test-event and connection-gate dialogs, and the Google, plan-
  tier and support-ticket dialogs in admin. All of them now close on Escape and
  carry `role="dialog"`/`aria-modal`, so a screen reader is told it is in a
  dialog rather than in the middle of the page.

- **A confirm dialog with more than a sentence in it now renders the markup it
  was given.** `ConfirmModal` wrapped its message in a `<p>`, but the message
  is a `ReactNode` and seven callers pass block content — the git-mirror
  "unrelated history" confirm sends a paragraph plus a `<pre>` of the server's
  own words, and the platform drop-killswitch and org/user detail confirms each
  send a `<div>` with a reason field in it. A browser closes a paragraph at the
  first block child, so all of that content escaped the `<p>` and rendered as
  the dialog body's own children: it lost the muted colour, the type size and
  the line height `.confirm-message` was there to give it, and the empty `<p>`
  left behind kept them. The wrapper is a `<div>` now, which is what a
  node-valued prop needed all along.

- **Loading placeholders are announced to screen readers.** Ten different
  hand-built wrappers rendered the one "Loading…" string, and not one of them
  carried `role="status"` — so every loading state in the app was silent to a
  screen reader, and a page that was fetching was indistinguishable from a page
  that was empty. They are now two shared primitives that carry it, along with
  a shared empty-state and quiet-notice pair whose padding, type size and
  wrapper element had each drifted apart across the 42 places that had built
  the same thing by hand.

### Developer

- **The web app is arranged by feature, and the flow editor has been broken
  up.** `pages/` and `components/` were flat directories of 53 and 77 entries;
  they are now grouped (`pages/{admin,auth,flows,runs,support}`,
  `components/{ui,dialogs,editor,fields,org,brand}`), 112 files moved, with no
  behaviour change in the move itself. `FlowEditor.tsx` then shed 1,208 lines
  — 5,872 down to 4,896 — into eleven named modules under `flows/editor/`:
  autosave, publishing, revisions, the run stream, the connection gate, the
  diff and test-event dialogs, the schedule timezone and lint-message helpers.
  Each is now directly testable, and five new test files (~1,400 lines) cover
  saving, editing, publishing, revisions and running.

- **Five new guards on the design system, each one written after the drift it
  now prevents.** `check-style-scales`, `check-icon-sizes` and
  `check-css-breakpoints` hold the spacing, type, icon and viewport scales
  (see *Changed*); the breakpoint one exists because CSS custom properties do
  not work inside media queries, so a width both a stylesheet and a component
  need must be written twice — two components had each declared their own
  `MOBILE_BREAK = 768` and the editor compared against a bare `1100` twice,
  with the obligation to match `app.css` recorded only in a prose comment.
  `check-modal-a11y` requires an Escape handler and the dialog ARIA per
  backdrop a file renders; `check-ui-primitives` forbids the hand-rolled quiet
  notice and the hand-rolled loading card. Two of the dialogs missing Escape
  had been extracted from inline JSX earlier in the same cleanup — the gap
  propagates the moment nothing checks for it.

  The two existing CSS guards grew a direction each. `check-css-tokens` now
  rejects a `var()` fallback on a token that *is* defined: there were 172 of
  them, all dead, and what they had drifted into is the argument — `--warning`
  appeared as six different oranges and `--success` as six different greens,
  while `--accent-ink` was written as both its light and its dark value in
  different files. Dead but not harmless, since they read as the token's value,
  so retuning the palette meant disbelieving 172 stale copies.
  `check-css-classes` now also asks the reverse of its original question — is
  there a rule nothing can match? — which immediately found the bug that
  motivated it: renaming `.settings-foot` to `.modal-foot` updated `app.css`
  and every component but missed two rules in `theme.css`, and
  `.confirm-dialog .settings-foot button.danger` was the only thing making the
  delete-confirm's button solid red. Nothing had failed, because every class a
  component asked for still resolved.

- **Duplicated interface text is on shared keys, and the locale bundles are
  checked structurally.** 153 keys whose English was identical to another
  key's collapsed onto 31 shared `common.*` keys, which is what surfaced the
  three divergent Swedish translations above. A new test asserts the two
  bundles carry exactly the same keys, that neither has an empty string, and
  that no translation drops a `{{interpolation}}` or a `<0>…</0>` `<Trans>`
  placeholder — a Swedish string that loses its `{{count}}` does not crash, it
  renders a sentence with a hole in it, in production, in the language the
  reviewer does not read.

- **The runner agent and its installer are tested, in CI.** They are Python and
  POSIX shell, so the Go suite can only check that the copies the daemon serves
  still match the source; everything that makes them trustworthy — a single-use
  token refused before anything spends it, the registration token never
  reaching a file, an allow-list surviving a reinstall, `runner.env` read rather
  than evaluated, a service actually wired to start at boot — is in 67 Python
  unit tests, run by `make runner-test` and by the build, before the Go race
  suite, because this is the one script every organization pastes into a
  terminal. `make runner-embed` refreshes the embedded copies.

## [0.10.3] - 2026-08-24

### Changed

- **A row on the Runs page opens the run.** It opened the flow EDITOR — a
  different object, on the page you go to when you want to change a flow, not
  when you want to know what one of its runs did — while the run itself sat
  behind a muted 14px glyph at the end of the row. So the page's whole purpose
  was its least discoverable action, and its obvious one led somewhere you
  can't even save (a live run locks the flow) and which shows no timing, no
  error explanation, no step timeline and no run log.

  The dashboard's recent-runs lists had always linked straight to the run, and
  the flow list leads to flows; the Runs page was the one that disagreed with
  both. The two targets are now swapped — the name opens the run, a pencil at
  the end of the row opens the flow in the editor. That trailing icon had also
  been an "external link" glyph, which conventionally means "leaves the app",
  on an internal route.

  The name link fills its cell rather than hugging its text, so the whole cell
  is the target. Not the whole row: an anchor can't wrap a `<tr>`, so a
  row-wide target would have meant a hand-rolled `role="link"` with its own
  keyboard handling — diverging from every other navigable surface in the app,
  all of which are real anchors (the dashboard's run rows, the flow cards), and
  losing middle-click and open-in-new-tab with them. The widened cell is scoped
  to this one table: `.run-table` is shared with four tables that navigate
  nowhere, and their shared row hover has to keep reading as a reading aid
  rather than promising a click they don't deliver.

## [0.10.2] - 2026-08-24

### Fixed

- **Swedish relative times now read as Swedish.** "Ago" is a circumfix there —
  `för … sedan` — and the strings only had the tail: 0.10.1 rendered "Startad 1
  dag sedan", which is English word order wearing Swedish words. It now reads
  "Startad för 1 dag sedan", in every place a relative time appears (the run
  page, the dashboard's recent runs, the flow cards, the wall display, the git
  mirror panel). `justNow` was "nu" — *now*, present tense, for a thing that
  already happened — and is now "nyss". The run page's label is the participle
  "Startad" rather than the past-tense "Startade": it labels a record, it
  doesn't narrate one.

  The flow cards' footer said "Senaste körning för 5 minuter sedan" — a noun
  phrase with a time bolted on. It now says "Senast kört", and the participles
  around it agree with the flow they describe: a *flöde* is neuter, so "Aldrig
  kört", "Skapat av", "Pausat", matching the "Inte publicerat" and "Flödet
  pausat" that were already right. The sort option orders many flows, so it
  takes the plural: "Senast körda". Run surfaces are untouched — a *körning* is
  common gender, so "Startad", "Köad" and "Avbruten" were correct already.

## [0.10.1] - 2026-08-24

### Changed

- **The run page introduces itself with when it ran, not 24 characters of hex.**
  Under the flow's name sat "Run 69a6f59b21aa3a4e7530df27" — the first thing
  anyone read on the page, and the one thing on it a non-technical user could
  do nothing with. Every other detail page uses that subtitle slot for a
  sentence about the page; the run page was alone in putting an identifier
  there, and the runs LIST had already decided the same ids were plumbing and
  kept them off the table.

  The slot now answers the question a person actually asks of a run — which one
  is this? — with "Started 5 minutes ago", localized through the same
  `formatRelative` the dashboard and flow cards use. It carries its own 30-second clock, because
  a finished run stops the live poll and a relative label with nothing driving
  it will still say "just now" an hour later. The exact instant is a hover away
  and remains spelled out in the details card, so the coarse label is
  orientation rather than the record.

  The id hasn't gone anywhere — support tickets, `dzctl` and bug reports need
  it — it moved into the details card with the other technical facts, and
  gained a copy button, since pasting it somewhere is the only thing anyone
  does with it.

- **Relative times are spelled out: "5 minutes ago", not "5m ago".** The
  abbreviated form was compact but read as machine shorthand on the surfaces
  non-technical users see most — the dashboard's recent runs, the flow cards'
  last-run line, the wall-display overview, and now the run page's subtitle.
  Spelling them out required proper plurals, so the strings gained `_one` /
  `_other` forms; that also fixes Swedish rendering "1 dagar sedan" (and now
  "1 minut" / "1 timme" / "1 dag" decline correctly).

### Added

- **"Waiting for approval" is a run status.** A run parked on a person
  reported *Running* — on the runs list and the run page alike —
  indistinguishable from one actually doing work, for however many days it sat
  there. The web UI had been built for this all along: the amber dot, both
  labels, the run page's Stop-while-awaiting gating, and a "Waiting" filter
  chip on the runs list that could never match a single row. What was missing
  was the daemon ever setting it: `awaiting` lived on the parked NODE record
  and never reached the run. It does now, so the dot goes amber, the run stops
  claiming to be busy, and the filter chip works.

  Only an approval pause counts. A subgraph step also parks as `awaiting`
  while its child graph runs, and that run isn't waiting on anybody — calling
  it "Waiting for approval" would send someone hunting for a decision that
  doesn't exist. The line is the same one the Approvals inbox and the approval
  mail already draw: the pause emitted a `pending_url`.

  A run holding two approvals open at once stays waiting until the last one is
  decided, rather than reporting whatever the most recent pause did. The
  stuck-run reaper now sweeps awaiting runs too, so parking can't hide a run
  from recovery; it leaves genuinely parked runs untouched.

### Changed

- **The step inspector no longer approves.** A step parked on an approval
  offered approve/reject in two places at once — the canvas card's bar and a
  panel in the inspector — and they weren't the same control: only the
  inspector's carried the comment box, so the fuller version of the action was
  the one hidden behind selecting the step. The inspector's panel is gone. The
  canvas keeps its decide-in-place bar for the run you're already watching, and
  the run page and Approvals inbox keep the full control, comment included.

  The inspector was also the one surface that couldn't say *which* run it was
  deciding: it is graph-scoped and binds to "whichever run is latest", and
  nothing stops a schedule from starting a second run while the first sits
  parked on the same step (`SubmitGraph` has no active-run gate). The other two
  surfaces are run-scoped by construction. A parked run additionally blocks
  saving the flow, so the inspector's real job — editing config — was disabled
  for exactly as long as the panel was on screen.

### Fixed

- **Duplicate approval emails — found and fixed.** 0.10.0 added a log line to
  tell an application double-send from a duplicate delivery downstream,
  because the send path looked airtight. It wasn't: the second copy came from
  the *park*, not the send. A node that pauses is announced once per committed
  park, and `awaiting → awaiting` was an accepted transition in both job
  stores — so when an expired lease let a second worker reclaim and re-execute
  a node the first was still running, both executions committed a park and
  both mailed the approvers the same request. The later park also overwrote
  the earlier one's result, so the link in the mail that already went out
  stopped matching the record. Parking is now refused on a record that is
  already parked (both stores, pinned by a conformance test); the worker
  already abandons a fenced park without notifying.

  The ownership fence should have caught the late writer, which is why this
  survived review — but `dzd` named every worker `dzd-dev-w<i>`, identical in
  every process, and the fence is a string compare on that name. Across two
  instances it compared equal and passed for the wrong owner, so a reclaimed
  node could be completed twice by design. Worker IDs are now unique per
  process (hostname + PID, or `DAZYFLOW_WORKER_ID`). That fence guarded more
  than email: usage metering, downstream dispatch and child-graph submission
  all hang off the same write.

  Reproduced by a test that parks a record out-of-band while a worker is mid
  execution — with the ownership fence deliberately left matching, as it was
  in production.

## [0.10.0] - 2026-08-23

### Added

- **Approve or reject from the run page.** A run parked on an approval now
  shows the decision controls directly under its header, one per awaiting
  step, with the step's question. Previously the run page's only action on an
  awaiting run was "Stop run" — you could see what the flow was waiting for
  and had no way to answer it, which made it a dead end for anyone arriving
  from an approval email without a signed one-click link. The existing live
  poll already covers `awaiting`, so a resolved approval clears itself.

### Fixed

- **The approval email's fallback link led somewhere you couldn't approve.**
  0.9.1 sent the request mail without a signed link by pointing the button at
  the flow's run page — on the belief that the run page carried Approve/Reject.
  It did not: its only action on an awaiting run was "Stop run", so the email
  delivered you to a page showing the thing waiting on you with no way to
  answer it. The button now points at the Approvals inbox, and the run page
  has gained the controls too (above), so both destinations work.

## [0.9.1] - 2026-08-23

### Fixed

- **The "approval needed" email never sent unless approval links were
  configured.** 0.9.0 shipped approval mail in both directions, but the
  request half was gated on the signed one-click link being available — and
  that link only exists when `DAZYFLOW_APPROVAL_HMAC_SECRET` is set, because
  `engine.ApprovalSigner` is nil otherwise and the step emits an empty
  `pending_url`. On every deployment without that secret the result was the
  worst possible shape: silence when a flow parked and needed you, followed by
  a perfectly good email once somebody found it in the inbox and decided.

  The request mail no longer depends on the link. With the secret set it still
  leads with the one-click URL; without it the button points at the flow's run
  page in the app, which needs a sign-in — a fine second best, and where the
  Approve/Reject controls already are. The "anyone with this link can approve,
  so don't forward it" warning is dropped in that case, because it is only
  true of the signed link and attaching it to an access-controlled page would
  teach people to ignore it where it matters.

  Setting `DAZYFLOW_APPROVAL_HMAC_SECRET` (with `DAZYFLOW_PUBLIC_BASE_URL`) is
  still worth doing — it is what makes approving straight from the email work
  at all.

- **`make env` no longer reports correct production settings as orphans.**
  `sync-env.sh` used one index to answer two different questions — "is this
  key set?" and "is this key documented?" — and a commented-out line in
  `.env.example` answers no to the first but yes to the second. The result was
  that `COMPOSE_FILE`, which the example ships commented BECAUSE a dev host
  must not merge the production overlay, was flagged as undocumented on every
  run of every prod host that had correctly set it. Documentation in a comment
  now counts for the orphan check; a genuinely unknown key is still reported,
  and commented keys are still never appended.

- **`ACME_EMAIL` is documented.** Read by Caddy from `deploy/Caddyfile` as the
  Let's Encrypt registration address, never by dzd, so it had never made it
  into `.env.example` — and every production host was told it was an orphan.

- **Approving something already decided no longer reads as a fault.** 0.9.0
  put a second approve control on the canvas card, next to the Inspector panel
  that already had one — so a step can now be resolved from two places on the
  same screen, and hitting the second after the first has landed returns a
  409. That surfaced as "it conflicts with something that already exists or is
  in use", which describes a collision the user has to go and fix. Nothing is
  wrong: the decision was simply already made, here or by someone holding the
  approval link. All three approve surfaces — canvas, Inspector and the
  Approvals inbox — now say "That was already decided — the flow has moved
  on." The generic conflict wording is unchanged everywhere else.

- **Approval mail now logs one line per notification.** Duplicate approval
  emails were reported and could not be reproduced: the recipient list
  dedupes, `Approve` refuses a second decision on an already-terminal record
  (verified under concurrent double-approve), the SMTP layer issues one DATA
  per call with no retry, and an end-to-end test counts exactly one message on
  the wire per decision. That left no way to tell an application double-send
  from a duplicate delivery downstream. `approval-notify(<kind>) <org>/<flow>:
  sending to N recipient(s)` is written before the sends, so the log answers
  it: one line for one decision means Dazyflow sent once.

## [0.9.0] - 2026-08-22

### Added

- **A flow waiting on a person now emails the people who can decide.** The
  `await_approval` step has always handed you an approval link on
  `pending_url` and left delivering it to you: you wired an ntfy or email step
  after it, or the run sat there until someone happened to look at the
  Approvals inbox. It now mails the approvers itself, when the run parks and
  again once a decision lands — so the same people who were asked are the
  people told the outcome, and a second approver doesn't go hunting for an
  item somebody already resolved.

  Who gets it is the step's new **Email these people** field: comma-separated
  addresses (semicolons and newlines are accepted too — a list that silently
  notified nobody because of the wrong separator is the worst failure this
  could have). It is also the only way to reach someone who isn't a member —
  an external reviewer, a shared ops alias.

  **Leave it blank and nothing is sent.** Existing approval steps are
  therefore untouched by the upgrade: they park and resume exactly as before,
  and you deliver the `pending_url` link yourself or let people work the
  Approvals inbox. Defaulting to "everyone in the organization who could act
  on it" was the other option and was rejected — approving is not
  permission-gated, so that set is wide, and it would have turned every
  already-deployed approval step into a mailshot the moment the daemon was
  upgraded. Opt-in costs one field and surprises nobody.

  Each message goes to one address at a time. A shared `To`/`Cc` would leak
  the organization's member list to every approver, including any external
  reviewer named on the step.

- **Approve or reject straight from the canvas.** An `await_approval` card
  that the run is parked on now carries the two buttons itself, in the same
  flush footer bar as the Connect and "needs configuration" banners. The
  Inspector panel stays — it is still where you add a comment with the
  decision — but the common case, "the flow is waiting on me, right there on
  screen", no longer needs you to select the step and read a side panel first.

- **Step and field help is reachable on a tablet.** Both the (i) on a step
  header and the one on every form field were native `title=` tooltips. A
  native tooltip does not fire on touch **at all**, so on a tablet every word
  of that guidance — 151 step descriptions and 605 field help texts — was
  simply absent; and it cannot be scrolled, which a 131-word step description
  needs. Both are now click-to-open popovers that stay put, scroll when they
  have to, close on Escape or an outside click, and hand focus back.

### Changed

- **The Swedish translation reads like Swedish software.** Around 270 strings
  across the UI catalogue were rewritten from a literal translation into the
  vocabulary Swedish applications actually use: *Redigerare* not *Redaktör*
  for the Editor role, *Läsare* not *Granskare* for Viewer, *Användning* not
  *Förbrukning*, *Blockera* not *Bannlysa*, *Logghistorik* not *Loggretention*,
  and *publicera* in place of the Swenglish *hosta*. Terms that had drifted
  into two, three and four different Swedish words — secret store, Reconnect,
  Retry, Copied, Delete, output, notification — were each settled on one. The
  step-catalogue category chips were translated from the raw engine enum
  (*In/ut*, *Nätverk*, *Omvandling*) rather than the product wording English
  shows, and now mirror it: *Filer och data*, *Appar och tjänster*,
  *Ändra data*.

- **Plain language across every surface a non-technical person reads.** A
  read-through of every page a viewer or editor can reach turned up two
  clusters and both are gone. The canvas talked like a circuit diagram —
  *wire*, *pin*, *upstream*, *downstream* — in the 151 step descriptions, the
  605 params-schema help texts, the runtime error strings and the UI
  catalogue; it now says *connect*, *input*/*output*, and *earlier*/*later
  steps*. And the Apps pages had never had the drop→step / node→step /
  graph→flow rename at all: "the Download drop exports them", "no key on the
  node", "embedding it in graph JSON". Both languages moved together. Genuine
  other senses were left alone on purpose — "Drop a meeting onto a calendar",
  "drop rows", a map pin, and `node`/`nodes` where they name an actual param
  key or JSON shape.

- **Implementation words are out of copy a business user reads.** Flow
  Settings → General no longer mentions the *daemon* ("the daemon's default
  still applies", "Stamped by the daemon"); Plan & usage no longer says
  "Usage metering is not enabled on this deployment"; and the form's
  last-resort message no longer reads "Top-level schema isn't a property bag".

### Removed

- **The "notify me on ntfy with the approval link" button.** It was a
  one-click shortcut that built an ntfy step next to an `await_approval` step
  and wired it up. Now that the step emails its approvers directly, the
  shortcut points at the harder path — and the manual route it automated is
  unchanged: `pending_url` still carries the link into any notify step you
  want.

### Fixed

- **Approvals taken from an email link were never recorded in the audit
  trail.** The authenticated inbox path audited through the caller's
  principal, but the HMAC `/approve/{run}/{node}` path — the one used from a
  notification, by whoever holds the URL, and therefore the one with no proven
  identity — wrote nothing but a line to the daemon log. It now appends to the
  same trail, with the tenant read off the graph record and the name recorded
  as `"someone@example.com (self-declared, via approval link)"` rather than
  passed off as a verified subject.

- **The Swedish UI documented a broken secret reference.** Flow Settings →
  Secrets told Swedish readers to write `${secret.NAMN}` — the code literal
  had been translated along with the prose, so the documented syntax simply
  did not work. It is `${secret.NAME}` again.

- **Engine vocabulary was showing on an admin page.** The organization's flow
  limits listed "Max antal grafnoder" (max graph nodes) where English said
  "Max steps per flow".

- **Two full-width buttons were centred when they should have been
  left-aligned.** The global `button {}` rule sets `justify-content: center`,
  and both `.help-link` and `.sidebar-collapse-toggle` set `display: flex`
  without overriding it — so Help → Contact support (a `<button>`) sat
  centred beside Documentation (an `<a>`, which defaults to `flex-start`),
  and the expanded sidebar's collapse row sat centred under nav items that
  were not. `text-align: left` was already on both, which does nothing to
  flex children.

- **Swedish gender agreement, and an email header where an account should
  be.** Around fifteen neuter/common errors (*ett stegtyp*, *steget … den*,
  *Steg tillagd*, *avstängd* for a step) are corrected, and the account menu's
  fallback no longer reads "(inget subjekt)" — the email-header sense of
  *subject* — where it means an unknown account.

- **Two English typos.** The file drop zones read "**Step** files to upload"
  and "**Step** a file here or click to browse", collateral from a global
  drop→step replace; both are drag-and-drop targets and say **Drop** again.

### Developer

- **The Swedish drop catalogue can no longer rot silently.** Every lookup in
  `dropText.ts` falls back to the English it was handed, so a missing or stale
  translation renders perfectly good English and nothing says so — which is
  how six untranslated step descriptions and four that had gone stale against
  reworded English sat unnoticed. `make catalogs` regenerates a snapshot of
  the live registry, `make catalogs-check` fails if it is out of date (CI runs
  it), and a new test names every step or app whose Swedish is missing, stale
  or orphaned. Coverage is 151/151 steps and 35/35 apps. `make ci` also runs
  the web test suite now, which it had skipped while calling itself a full
  mirror of CI.

## [0.8.0] - 2026-08-21

### Added

- **Mirror your flows to your own git.** A workspace's flows have always been
  stored as a real git repository — `graphs/<id>.json`, one commit per save
  with the author in the message, the live revision as a tag — but that
  repository only existed inside the daemon. It can now be pushed to a remote
  you own: GitHub, GitLab, Gitea, sr.ht, or a bare repo on your own box.
  Configure it under **Git credentials → Mirror to your own git**.

  The mirror is a real clone, not an export: full history, every flow, and the
  published-revision tags, so you can read it, diff it, review changes in a
  pull request, and restore from it. Choose whether it pushes when a flow is
  published (the default — the mirror tracks what you have actually shipped)
  or on every saved change.

  Authentication is an **SSH key only**, from one of the Git credentials you
  already manage; the credential picker lists only the ones that have a key.
  A PAT would work at the protocol level and the credential store holds one,
  but a deploy key is scoped to a single repository, which is what an
  unattended continuous push should be — it needs read *and* write access,
  since the push lists the remote's refs before transferring to work out
  which are stale. Host keys are verified against the same pinned set the
  `git_checkout` step uses — github.com, gitlab.com and git.sr.ht are built
  in; anything else needs a `known_hosts` line on the credential.

  The remote is treated as a **replica**: the push forces every ref and
  deletes the ones that no longer exist locally, so a commit made directly on
  the remote is overwritten by the next push. Forcing isn't incidental —
  editor autosaves amend the previous commit, so a workspace's history is
  legitimately rewritten during ordinary editing and a non-forced mirror
  would start rejecting pushes almost immediately. Point it at a repository
  nothing else writes to.

  Because that push is destructive, it will not touch a remote it shares no
  history with. The case that matters: a deployment whose data volume was
  lost comes back with an empty workspace and would otherwise mirror it over
  the very repository it should have been restored *from*, deleting every
  flow. A wrong URL and a repository someone else's project owns fail the
  same way. Automatic mirroring can never override this; **Push now** offers
  an explicit "overwrite the remote" confirmation for the case where
  repointing a mirror really is what you meant. One recognised ref is enough
  to count as shared history, so an ordinary push and an amended autosave
  both pass without a prompt.

  A failed push never fails the save or publish that triggered it. The
  outcome is recorded per workspace and shown on the settings panel, with the
  git error verbatim ("permission denied (publickey)", "host key mismatch")
  and the last time a push actually succeeded — so a mirror that quietly
  stopped working three weeks ago reads differently from one that failed a
  minute ago. **Push now** runs a push synchronously and reports the result,
  ignoring the automatic-mirroring switch, which is how you test a new remote
  or a rotated key before turning it on.

  One caveat worth knowing before pointing this at a **public** repository:
  webhook trigger keys live in a flow's JSON by design (the `/trigger`
  endpoint authenticates callers against them), so they travel to the mirror
  with everything else. Real credentials don't — those are `${secret.…}`
  references resolved at run time — and the existing lint still flags a
  provider token pasted into a step. Mirror to a private repository.

- **⌘K reaches settings and administration.** The command bar indexed
  workspace pages and flows only, so everything *configured* rather than
  browsed — credentials, secrets, API keys, the audit log, the new git mirror —
  could not be reached from it at all. Those destinations are listed now
  (behind the same permission bar the Admin index itself uses), each with
  search terms that don't appear in its name: "backup", "mirror" or "sync"
  find git mirroring, "logs" finds run history, "members" finds People. The
  aliases carry English and Swedish together, since matching is a substring
  test and the active locale is irrelevant — the same reasoning as the step
  palette's Swedish aliases.

### Fixed

- **The git page was unfindable for what most people want it for.** Its Admin
  card read "SSH keys or access tokens for cloning private repos", which says
  nothing about mirroring, so someone looking to back their flows up to git
  had no reason to click it — and no other route in except the `git_checkout`
  step's account picker. The card and the page are now "Git credentials &
  mirroring", and the editor's Settings → General points at it from the
  tenant/workspace field, where "where does this flow live?" is already the
  question on screen.

- **nShift and Roaring had blank app pages.** Both connectors shipped without
  the curated prose their `/apps/:slug` page renders, so the page showed a
  title and nothing else — no explanation of what the app does, how it
  authenticates, or what it costs you to get wrong (nShift books billable
  consignments). Both are written now, in English and Swedish. A new test
  fails the build when a connector is added without them, checked against a
  generated list of what the catalog actually ships (`make integration-catalog`),
  so the next one can't slip through the same way.

## [0.7.4] - 2026-08-21

### Fixed

- **The Apps index showed a green dot for a broken connection.** The grid
  only knew "set up" or "not set up", so an app whose account had stopped
  working sat under *Connected* with the same green dot as a healthy one —
  and the index is the first place someone looks when a flow starts failing.
  A connected app with an account that needs reconnecting now shows amber,
  with a tooltip saying so, matching what its own page reports.

## [0.7.3] - 2026-08-21

### Fixed

- **The Apps page waited for a run to notice a dead connection.** 0.7.1 could
  only learn that a grant was dead when something used the token, so the page
  went on saying "connected" until the next scheduled run happened to fail —
  which is precisely when someone is standing on that page wondering why their
  flow broke. Listing the connections now refreshes any account whose access
  token has already expired, which is the same work the next run would do,
  just sooner: a rejected refresh marks the account, a successful one clears it
  and stores the fresh token. An account whose token is still valid costs
  nothing — no network call, nothing to report.

## [0.7.2] - 2026-08-21

### Fixed

- **The fix-it link in the run timeline still went to the Apps index.** 0.7.1
  deep-linked the failure banner at the top of a run, but each step expanded in
  the timeline renders its error through its own component, which was still
  sending people to the list of every app. It now deep-links too — and more
  precisely than the banner, because it knows which step you opened rather than
  which step failed first.
- **Two undefined CSS variables broke the build.** The connection-account rows
  added in 0.7.1 referenced `--radius-md` and `--text-muted`, neither of which
  exists — they render as nothing, which is what `check-css-tokens` fails the
  build over. Now `--r-2` and `--warning`, the tokens the rest of the
  stylesheet uses.

## [0.7.1] - 2026-08-21

### Added

- **Reconnect, per account.** An app's connection card now lists each
  connected account on its own row, with its own state and its own
  **Reconnect** button, which re-authorises *that* account in place instead of
  adding a second one. Previously the card could only say "connected" and the
  only action on offer was "Connect another" — which, confusingly, was the
  reconnect people needed all along, just labelled as though it weren't.

### Fixed

- **A dead OAuth grant showed as connected.** When a Google (or any OAuth)
  grant is revoked, expires, or is invalidated by a password change, every run
  fails with a 401 from the provider — while the Apps page went on reporting
  the account as connected, because all it could see was whether a token
  existed. Nothing anywhere said the one thing that mattered: sign in again.
  The daemon now records the rejection at the moment it happens — a refused
  token refresh is the point where the grant is known to be dead rather than
  the call merely unlucky — and clears it as soon as a refresh works or a new
  token is stored. `GET /oauth/providers` reports those accounts as
  `needs_reconnect`, and the app's card marks them and offers the fix. This is
  distinct from the existing `stale_accounts`, which is about scopes added
  since; that check is deliberately skipped for providers authorised
  incrementally (Google), which is exactly where this gap bit.

  Known limit: the signal comes from a rejected *refresh*. A token stored
  without an expiry or a refresh token is never refreshed, so a grant that
  dies in that state is still only visible as a failing run. Marking on a 401
  from the API call itself is the remaining half.
- **A failed run's fix-it link went to the Apps index.** "Reconnect" on a
  failed run dropped the user on the list of every app, leaving them to work
  out which one broke — and then, on the right app, to work out which account.
  The error text can't say: "Gmail returned 401" names neither. The failing
  step does, though — its module gives the app and its settings give the
  account — so the button now lands on that app with that account called out
  (`/apps/gmail?reconnect=default`).

## [0.7.0] - 2026-08-21

### Added

- **The scenario corpus is now RUN, not just validated** (`tests/journey/usecases_run_test.go`).
  Eleven shapes — a loop handing a step structured data, a read-act-write-back
  round trip (on three different services), collecting loop results,
  transition-only firing, tolerating a dead channel, surviving an upstream
  outage and recovering from it, pausing for a person and resuming, an AI
  judgement routing the original submission, and dedupe across runs — are
  saved through the real API,
  published, fired and waited on, with every outside service mocked by a
  stateful fake (`fakesaas_test.go`, including a small SMTP server). The
  assertions are on what the world received. Every applicable test runs its
  flow a second time, because eleven scenarios promise nothing happens twice,
  and two inject faults. That layer found the two fixes below.
- **`sheets_update_cells` (Google Sheets — Update cells), plus row numbers on
  Read range.** A spreadsheet could be read and appended to, never changed —
  so no flow could mark a row done, and every "handle the new rows" job had to
  keep a private ledger of what it had already processed and filter against it.
  Read range can now include each row's real position in the sheet (`_row`,
  opt-in), and Update cells writes values back into exactly those rows, adding
  a column with its header when the sheet doesn't have it yet. Read what's
  outstanding → act → mark it done → skip it next time.
- **`gmail_get_thread` (Gmail — Read conversation).** "Has anyone answered?" is
  a question about a conversation, and every mail step worked on single
  messages; Gmail has no "unanswered" search operator either. This returns a
  thread's messages and answers the question directly — Replied is No while the
  newest message in the thread is still one of yours — plus a one-row Summary
  per conversation for collecting a table of what's outstanding.
- **`site_check` (Is it up?).** Watches a site and fires only on the
  transitions: once when it breaks, once when it recovers, nothing in between,
  so an outage pages you once rather than twelve times an hour. A site already
  down on the first check does fire. Optionally requires a phrase on the page,
  which catches a server answering 200 with an error page. Distinct from
  `web_watch`, which compares content and treats a bad response as a failure.
- **`stripe_get_customer` (Stripe — Get customer).** Every payment and
  subscription event names the customer by `cus_…` id, and Stripe's search
  cannot look up by id — so "email whoever just cancelled" had no path at all.
- **String helpers in formulas.** `substring`, `split`, `join`, `replace`,
  `trim`, `lowerAscii`, `upperAscii`, `indexOf`, `charAt` are now in scope for
  the row formulas (calculated columns, filters, routing) and the Expression
  step alike. Without them "the first ten characters of the date", "tidy these
  addresses" and "shorten this into a title" needed an extra step, or couldn't
  be said.
- **Steps can be marked non-critical** (`continue_on_error` on a node). The
  tolerate-this-failure policies live on connections, so a step at the end of a
  branch — a notification, a final write — had nowhere to hang one and always
  failed the whole run. In a fan-out that's wrong: Discord being down is no
  reason for the Slack post and the email not to count.
- **More fields take a wire.** Create event's start, end, description, location
  and attendees (start/end also accept relative values like `tomorrow+9h`, while
  an absolute plain date still means an all-day event); Slack's
  reply-in-thread, so a bot can answer under the message that summoned it;
  Drive's file name, so a weekly backup can be dated instead of overwriting
  itself.

- **A generator eval built on the scenario corpus** (`make flowgen-eval`). The
  thirty-five plain-language asks in `/SCENARIOS.md` are each already paired
  with a known-good graph, which makes them an eval set for the AI flow
  generator: feed each scenario's own words to the same generator the editor
  calls, and score the draft on whether it passes the save gate, picks the same
  kind of trigger, and reaches the services the job needs. The three are scored
  separately because a flow that does nothing passes the gate. Live runs are
  opt-in (they cost money and aren't deterministic) and report rather than
  fail; two parts run in ordinary CI with no model — one checks the corpus and
  the scorer stay honest, the other drives the whole eval against a scripted
  model so the live path can't rot.

### Fixed

- **The approval link never reached anyone.** The await-approval step tells you
  to put it before the step that notifies a person and wire its link into that
  notification — but the dispatcher treated a parked step as having produced
  nothing, so the notification only went out *after* the approval. Nobody was
  ever told there was something to approve. A parked step's emitted ports are
  now live at once, while the ports that arrive with the decision keep their
  branches waiting rather than skipping them; re-dispatch on resume is a no-op,
  so nobody is notified twice. Affects every approval flow and the
  `approve-before-refund` template.
- **A loop where every item failed now fails.** Carrying on past a bad row is
  right for one row among many; reporting success when nothing worked is an
  outage dressed up as a result — and a following step that records the work as
  done then records work that never happened. Partial failures continue and
  surface on the `errors` port exactly as before.
- **`make test` could not finish.** The daemon package takes about 13 minutes
  under the race detector, past Go's default 10-minute per-package ceiling, so
  the suite failed on a timeout and blamed whichever test was running when the
  alarm fired. `make test`, `make ci` and CI now allow 30 minutes (per package,
  so it is headroom for the slowest one, not a slow-suite allowance).
- **The flow generator's instructions couldn't build a loop.** Its hand-written
  guidance said to "wire for_each.body into the per-item step's input" — the
  documented footgun, since the body pin is a control pin and pointing it at a
  typed input injects the whole row where a string was expected — and never
  mentioned `${item.…}`, so nothing told the model how a step inside a loop
  reads the current item. A model could only recover by choosing to call
  describe_drop on for_each. The guidance now states the mechanism, including
  that a param whose whole value is one reference keeps the value's real type.
- **Compare reads numeric text as a number.** Steps report counts, status codes
  and spreadsheet cells as text, so "is this count greater than 0" failed with
  `non-numeric operand in <,> comparison: string vs float64` — a message the
  author can act on only by giving up, since no step converts text to a number.
  Numeric text is now read as the number it plainly is, on either side of the
  comparison; text that isn't a number still fails rather than counting as zero.
- **A loop can hand a step structured data.** A For each body's steps see the
  current item only through `${item.…}` in their own settings, and those were
  resolved as text — so a step needing an object or a list (a shipment address,
  an email template's merge data, a set of invoice lines) received JSON as a
  *string* and couldn't read it. "One X per row" only worked when X needed
  nothing but scalars. A setting whose whole value is one `${item.…}` reference
  now keeps the value's real shape, exactly as `${resource.…}` already did;
  inline references and scalars are unchanged.


## [0.6.0] - 2026-08-20

### Added

- **Attachments can be read off incoming email.** `gmail_get_attachments`
  (Gmail — Download attachments) saves the files attached to a message and
  hands them on: `first` is a file ref that wires straight into Upload to
  Drive / Write file / an email's Attachments, and `files` lists them all with
  name, type, size and path. "Only these types" takes just the PDFs and ignores
  signature images, which are skipped anyway (they carry no filename). Files
  land in the run's scratch area by default, or a workspace folder if one is
  named; sender-controlled filenames are sanitised, so `../../etc/passwd`
  cannot escape the sandbox. Dazyflow could already *send* attachments — this
  closes the other half, and with it the "file the invoices people email me"
  job, which previously had no path at all.
- **`web_watch` (Watch a page — tell me when it changes).** Pair it with an
  Interval trigger and it fetches a page, compares it with what it said last
  time, and emits `on_change` only when it actually changed — so, like an
  unused Branch port, everything downstream stays dormant on a quiet check. The
  first check baselines silently. It compares the visible words rather than the
  HTML by default, so a rotating CSRF token or an asset hash doesn't cry wolf,
  and a "Watch just this" pattern narrows it to one price or status line.
  Watching an arbitrary page previously meant four steps and a hand-rolled
  collection to diff against.
- **Five templates**, covering the requested jobs that were buildable but
  laborious to assemble: Stripe payment → thank-you/team ping/sales log, AI
  inbox triage, approval before a refund, invoices emailed to you → filed in
  Drive, and watch a page → ping my phone (which needs no connected account at
  all).
- **`join_rows` gained `kind: "anti"`** — the left rows with no match on the
  right, carrying only their own columns. This is the "which of these haven't I
  processed yet?" question every sync asks; see the matching fix below for why
  the left-join answer was a trap.
- **Relative time windows on Google Calendar.** `time_min`/`time_max` now
  accept `now`, `today`, `tomorrow`, `yesterday`, `+3d`, `-2h30m`,
  `tomorrow+9h` as well as absolute timestamps, take a `tz` for the day
  boundaries, and can be wired from an upstream step. A nightly reminder flow
  can finally say "tomorrow" and mean it on every run; before, the field took
  RFC3339 only, so the window had to be left wide open and filtered afterwards.
  The grammar lives in `drops/internal/reltime`, shared with the Date step's
  offset parser.
- **The Regex step's text can be typed on the step.** Inside a For each there
  is no upstream node to wire from, so `text: "${item.description}"` is now how
  a loop body reads a field. A wired input still wins.

### Fixed

- **Formulas can produce rows and objects again.** A CEL expression returning a
  map — `{'payment_id': input.id}`, or a `map()` over rows — came back from
  cel-go as `map[any]any`, which `encoding/json` refuses and every row consumer
  rejects with "expected object, got map[interface {}]interface {}". So the
  obvious way to shape data for a "log this" step passed every validation and
  then failed at run time. `unwrapCEL` now normalises composites recursively,
  which fixes both `expression` and a `compute_rows` column whose formula
  returns an object.
- **`group_aggregate` accepts the short form.** `{"revenue": "sum"}` — the op
  alone, with the output name doubling as the source column — now works
  alongside `{"revenue": {"op": "sum", "column": "revenue"}}`. `sort_rows`
  takes a friendly string, so the nested-object requirement next door was a
  stumble that only the editor's form hid.
- **ntfy's "Link to open" takes a wire.** Its own help text says to wire an
  approval step's link into it, but it was a typed setting with no input port,
  so the link had to be spliced into the message body by an extra step.


- **Emailed links now open in the right org.** The "View run details" link in a
  flow-failure email pointed at `/runs/<id>` with no org in it, and the customer
  side of the support-ticket emails pointed at `/support/<id>` the same way.
  Because the app's routes carry no org segment — the active org is browser state
  plus the session's scope — those links opened against whichever org that
  browser last used, so anyone who belongs to more than one org usually landed in
  the wrong one and was told the run or ticket did not exist (the tenant-scoped
  loaders answer `ticket_not_found` / no such run).

  Such links now carry `?org=<tenant>` (the same query key the sign-in page
  already uses), and the app honours it on boot: it re-scopes the session server
  side when needed and then lands on the deep-linked page rather than dumping the
  user at `/`. An `?org=` naming an org the user cannot act in is ignored, so a
  forwarded or hand-edited link can't disturb a working session.

  The Stripe checkout and billing-portal return URLs are pinned the same way:
  `/usage` is org-scoped and Stripe hands the user back to a browser whose active
  org may have moved on (switching org in another tab mid-checkout is enough), so
  an unpinned return could show the wrong org's usage right after an upgrade.
  Those two URLs also now trim a trailing slash off the configured base, so a
  `DAZYFLOW_PUBLIC_BASE_URL` ending in `/` no longer produces `//usage`.

  Only genuinely tenant-scoped links are pinned. The support **agent** queue
  resolves tickets cross-tenant by design, so its links are deliberately left
  unpinned — agents generally aren't members of the filing org, and moving them
  there would be wrong as well as useless. Token-bearing links (invite, email
  verification, password reset, signup) identify their org through the token and
  are unaffected.

### Changed

- **Shared the location connectors' coordinate helpers.** `weather`
  (OpenWeather), `openmeteo`, `smhi` and `geo` each carried their own copy of
  the same "lat,lon" parser, range check, numeric-param reader, unit symbols,
  number formatters and SSRF/transport error prologue — four copies whose
  bodies and user-facing error strings had never diverged. They now live once in
  `drops/internal/geoloc`, along with the one-shot `Probe` the OpenWeather and
  Open-Meteo connection verifiers both used to hand-roll. Connector behaviour
  and every error string are unchanged; the tests that pinned them moved to the
  shared package as the union of what the four asserted separately.
- **Split the HTTP gateway by concern.** `daemon/httpgateway.go` had grown to
  2,822 lines and 71 declarations covering the route table, static asset
  serving, session cookies, CORS/CSP, sign-in and role elevation, org profile
  routes, run listing, API keys, run control, SSE streams and the response
  writers. It is now twelve files named for those concerns, with the route table
  alone in `daemon/httproutes.go`. No behaviour change. `route_sweep_test.go`
  now globs the package's source files instead of naming two of them, so the
  sweep can't go stale when the gateway is split again (it also no longer scans
  `httpsecrets.go`, which had stopped registering routes).

## [0.5.0] - 2026-08-20

### Added

- **Undo/redo in the flow editor** — `Cmd/Ctrl+Z`, `Cmd/Ctrl+Shift+Z` and
  `Ctrl+Y`, plus toolbar buttons that stay visible (disabled) so the feature
  and its shortcut are discoverable before you need them. Built on
  whole-document snapshots rather than a command stack: the editor already
  maintains a complete serializer and deserializer that saving and loading
  depend on, so history cannot fall behind the document the way a stack of
  per-action inverses would. Snapshots are applied by reconciling the node and
  edge arrays, preserving object identity for everything unchanged, so undoing
  one step's move re-renders one card instead of the whole canvas. Continuous
  gestures coalesce — a drag or a run of keystrokes in one field is a single
  undo — and the stack is fenced whenever the document is replaced from
  outside the editor, including an edit arriving over the MCP flow-watch, so
  undo can never silently discard an assistant's change.

### Changed

- **"Drop" is now "step" (Swedish: *steg*) everywhere a person can read it.**
  The documentation already said step — 38 uses, a glossary entry, a "Step
  catalog" — while the UI said drop in 106 strings, so this closed a
  docs/UI mismatch rather than choosing new vocabulary. 104 English and 96
  Swedish strings changed, along with the MCP tool descriptions, the operator
  docs, `.env.example`, and the user-visible Go error strings. Swedish gender
  agreement moved with it: *steg* is neuter where *dropp* was common, so 49
  determiners and adjectives shifted (`den här droppen` → `det här steget`).
  Deliberately unchanged, and now the convention: the Go catalog and package
  paths, API routes and JSON field names, MCP tool *names* (`list_drops`,
  `describe_drop`), error codes (`drop_not_found`), audit action names, CSS
  classes, frontend identifiers — the contract every non-human consumer is
  grounded on — and the verb "to drop" ("drop rows", "drop a pin", "drop to
  upload"). `describe_drop`'s description now tells an assistant to say
  "step" to the user, so the split doesn't leak into a conversation.

- **`make patch` / `minor` / `major` promote the changelog themselves.** The
  release recipe moves `[Unreleased]` under the new version heading, leaves a
  fresh empty one, and commits it with `./VERSION` before tagging. This was
  previously a manual step the Makefile only *documented*, and it drifted:
  0.3.0, 0.3.1, 0.3.2 and 0.4.0 were all tagged with no changelog entry
  because nothing checked. An empty `[Unreleased]` now aborts the release,
  and a hand-written version heading is detected and left alone.

- Detail pages share one `BackLink` component instead of five hand-rolled
  copies. Two admin pages had been overriding the shared `.back-link` class
  with a duplicated inline style, putting them at double the bottom margin of
  every other detail page; labels now consistently name the parent
  ("Organizations") rather than mixing that with "Back to runs" and a bare
  "Back", which also reads better to a screen reader.

### Security

- **A store read failure could no longer be mistaken for "this flow doesn't
  exist".** `saveGraph` treated *any* error from the workspace store as the
  new-flow case, and the new-flow path skips the per-flow ownership gate and
  the active-run lock and enforces only the weaker `graph:edit`. A corrupt
  object or a transient I/O fault was therefore enough to let a non-owner
  overwrite an existing private flow. The store now returns a typed
  `ErrGraphNotFound` and every other error fails closed. The same fail-open
  shape in the delete path — which reported success without checking edit
  permission — was fixed with it.

- **Google sign-in is bound to the browser that started it.** The callback
  consumed its state token with no cookie check, so an attacker could complete
  a sign-in in a victim's browser for the attacker's account, and anything the
  victim went on to create or connect landed in the attacker's organization.
  The integrations OAuth flow already had the correct binding pattern; it had
  simply never been applied to the sign-in leg. The binding is mandatory here
  rather than skipped when absent, because sign-in has exactly one start path.

- **Org Google OAuth client secrets are encrypted at rest.**
  `org_auth.google_client_secret` was a plain `TEXT` column while every
  comparable secret in the system was encrypted, so a database dump exposed
  every organization's live client secret. Secrets now live in the per-tenant
  encrypted store under a per-tenant DEK, the column is written empty, and
  rows written before this migrate on first read with no operator step.

- **Secret redaction no longer leaks when one secret contains another.**
  Replacement ran in map-iteration order, so replacing a shorter secret first
  cut it out of the middle of a longer one and the longer secret's tail
  survived into the persisted run record in cleartext — intermittently, which
  is worse than always. Secrets are now replaced longest-first.

- The `DAZYFLOW_DEV_KEY` dev admin token is refused when the deployment
  doesn't look local (a public base URL or a remote database). It mints a
  publicly-known admin bearer token at every boot and was previously guarded
  only by a line in the documentation.

- bcrypt cost raised from 10 to 12, with an opportunistic re-hash on
  successful login — the only moment a correct plaintext is available, so the
  only place an old hash can be strengthened without involving the user.

- The audit trail no longer accepts forged entries. The failed-sign-in path
  records the email as typed — it must, that address is the credential-stuffing
  signal — and nothing had validated it, so a newline forged a second line in
  a compliance-relevant log.

- A Content-Security-Policy is set on the authenticated app surface, and the
  `shell` and `git` steps resolve their working directory through an
  `os.Root` handle instead of cleaning the path as a string, so a symlink
  planted inside the workspace can no longer be followed out of it.

### Fixed

- Postgres and the in-memory job store now agree on `core.ErrConflict` for a
  duplicate enqueue; they had diverged because the conformance test only
  asserted that *some* error came back.

- Error-to-HTTP-status mapping uses typed sentinels instead of matching
  substrings of user-facing messages, so rewording a message can no longer
  change a status code. Authorization failures that wrapped
  `core.ErrUnauthorized` without the exact phrase the matcher looked for had
  been returning 500 instead of 403.

- 27 steps that perform non-idempotent external writes now opt into
  engine-side write dedupe, up from 10 — so an expired-lease reclaim replays
  the recorded result instead of posting, charging or sending twice. Slack,
  Notion, Fortnox, GitHub, Stripe, Drive, calendar, MQTT, email, ntfy and
  webhook sends were all uncovered.

- MCP idempotency keys are hashed over canonical JSON. The key was hashed over
  raw argument bytes on the assumption that a retry sends byte-identical JSON,
  which the protocol doesn't guarantee — so a host that re-serialized its
  arguments produced a new key and a duplicated side effect, in exactly the
  situation the key exists to make safe.

- `on_error` is validated when a graph is saved, so an unrecognized value can
  no longer be accepted and then silently ignored, quietly downgrading a
  fallback edge to abort.

- Panics in drop-spawned goroutines are recovered instead of taking down the
  daemon — notably `for_each`, which runs arbitrary per-item node execution.
  The engine's recover only covers the calling goroutine.

- 31 steps declared no `meta` output port while emitting one, so a third of
  the catalog produced data the editor could not wire and an assistant reading
  the manifest could not see. A new contract sweep mutates one parameter of a
  valid worked example at a time, which reaches the connector code paths the
  previous adversarial sweep could never get past parameter validation.

- Unparseable `DAZYFLOW_*` values log the fallback instead of discarding the
  operator's setting silently; a missing database DSN is reported before a
  malformed SMTP URL can kill the process; and the HTTP listener's drain is
  awaited before exit so in-flight SSE streams and uploads aren't cut.

## [0.4.0] - 2026-08-19

Everything below shipped between 0.2.0 and 0.4.0. The 0.3.0, 0.3.1 and 0.3.2
tags were same-day interim cuts rather than distinct milestones, and none of
the four carried a changelog entry at the time — the release recipe only
documented the step instead of performing it (fixed in `[Unreleased]` above).
They are folded into this one heading rather than split retroactively, because
the boundaries can no longer be reconstructed accurately from the entries.

### Added

- **Support dashboard** (Phase 3 — the Support feature is now complete) — the
  cross-org queue grew the tools a support team actually works from: **assignment**
  (claim a ticket, hand it to a colleague, release it back to the pool — only
  provisioned support agents can be named), **ownership + status filters** on the
  queue (`?assignee=me`, `?unassigned=true`, `?status=`), and **stat tiles**
  counted server-side over the whole queue (`GET /support/tickets/summary`), each
  tile doubling as the filter for what it counts. **Role separation** was tightened
  in both directions: the customer's view of a ticket no longer carries the support
  organisation's internals (who owns it, which individual replied), the requester
  can close or reopen their own ticket but only support can declare it *resolved*,
  and a platform admin still isn't support staff (`platform:admin` does not imply
  `support:agent`). Every assignment is audited into the **org's own** log. The
  feature is now documented for operators: `DAZYFLOW_SUPPORT_ENABLED` in
  `.env.example` and a *Support tickets & consented flow access* section in
  `docs/DEPLOY.md`.
- **Support tickets + chat** (native, Phase 2 of the Support feature) — an org
  member can file a ticket about a flow and chat with support in-app; support
  agents work a cross-tenant queue and reply/resolve. Filing auto-attaches a
  **redacted** diagnostic bundle for the referenced flow/run (structure + error,
  no secrets or run data), so support can help the common case without a live
  read-only grant. Chat bodies are secret-scrubbed on ingest. New "Report a
  problem" action on the run-failure page and a Support section in the nav.
  Gated by `DAZYFLOW_SUPPORT_ENABLED`; `whoami` exposes `support_tickets_enabled`.
- **Actionable "contact support"** — the operator-configured support contact is
  now a real link beyond the Connections page: on generic errors (flow editor)
  and, prefilled with run diagnostics, on the run-failure page.

- **Fortnox connector** (`drops/fortnox/`) — Sweden's dominant SMB accounting:
  create customer, create invoice, list invoices (paid-invoice poll source),
  and a customer picker. OAuth 2.0 via `client_secret_basic` (new daemon
  support, below).
- **46elks connector** (`drops/elks/`) — send SMS via the Swedish/Nordic 46elks
  API. Static-credential (HTTP Basic) service connection; no daemon changes.
- **Klarna connector** (`drops/klarna/`) — Order Management: get order, capture
  (full/partial), refund (full/partial). Static-credential (HTTP Basic) service
  connection, region-hosted (EU/NA/OC × prod/playground); no daemon changes.
  Money-moving POSTs are retry-off + write-deduped (no upstream idempotency key).
- **nShift connector** (`drops/nshift/`) — Nordic multi-carrier shipping over the
  Unifaun ExtAPI: create a shipment (book), get a shipment (status/tracking), and
  delete an unprinted draft (cancel). Static-credential (Bearer API key) service
  connection, environment-hosted (integration/production, defaulting to the
  sandbox); no daemon changes. Booking/delete are retry-off + write-deduped.
- **Roaring connector** (`drops/roaring/`) — Nordic company-data enrichment:
  company overview (org number → registered name / status / full record) and
  company search (name → candidate matches). Uses Roaring's OAuth2
  client-credentials grant, exchanged for a bearer token by the connector itself
  and cached in-process — so it's a static-credential (Consumer Key + Secret)
  service connection with no daemon OAuth changes.
- **Phone value drop** (`drops/value/`) — validate and normalize a phone number
  to E.164 with a default-region setting (libphonenumber), emitting country,
  national number, and type; the flow editor shows a live country flag beside
  the field for international input. The SMS-input sibling of the `url` drop.
- **OAuth `client_secret_basic` support** in the daemon's OAuth registry
  (`daemon/oauth.go`) — token requests can present client credentials in an
  HTTP Basic header instead of the form body, selected per provider via
  `TokenAuthStyle: "basic"`. Fortnox requires it; all existing providers keep
  the default form-body behavior.
- **Runs date-range filter.** `GET /api/v1/me/runs` and
  `GET /api/v1/me/flows/{flow_id}/runs` accept `since` and `until` query params
  (RFC3339 timestamp or bare `YYYY-MM-DD`) bounding a run's enqueue time —
  `since` inclusive, `until` exclusive. The Runs page gains a From/To date
  picker that resolves a selected day to local-midnight instants, so filtering
  is server-side and paginates correctly instead of narrowing only the rows
  already loaded.

### Changed

- **Twilio, Discord, MQTT, and Stripe now use a first-class service connection**
  (`ConnectionFields`) instead of loose secret references, so each gets a proper
  entry form on the Apps page (the same shape as ntfy / Home Assistant / SMTP)
  and a "connected / needs setup" state — previously you had to create the
  secret by hand in the secrets manager. Credentials are entered once and are no
  longer node params, so they never appear in the graph.

  **BREAKING — re-enter credentials once after upgrading.** The credentials move
  to per-tenant connection storage; the old secret names are no longer read:
  - Twilio: `TWILIO_ACCOUNT_SID` / `TWILIO_AUTH_TOKEN` → `conn.twilio.*`
  - Discord: `DISCORD_WEBHOOK_URL` → `conn.discord.webhook_url`
  - MQTT: `MQTT_USERNAME` / `MQTT_PASSWORD` (and the per-node `broker` param) →
    `conn.mqtt.*` (broker is now part of the connection)
  - Stripe: `STRIPE_API_KEY` → `conn.stripe.api_key` (the webhook triggers'
    `STRIPE_WEBHOOK_SECRET` is unchanged — it's verified server-side)

  Open each integration on the Apps page and enter its credentials once. Flows
  themselves need no edits.
- Realigned the OpenAPI definition of `GET /api/v1/me/runs` to the parameters
  the endpoint actually accepts (`status`, `since`, `until`, `limit`, `offset`,
  `workspace`, `tenant`); it had drifted to aspirational `from`/`to` plus
  `PageToken`/`PageSize`/`Sort` that were never implemented.

### Security

- Bumped `github.com/go-git/go-git/v5` to v5.19.2, clearing GO-2026-6214 (path
  traversal via crafted reference names) and GO-2026-6213 (worktree operations
  following symlinks). Both were reachable from our own call graph — the
  reference paths through `workspace.Store.SetRevisionLabel` /
  `PromoteToEnvironment`, and the worktree paths through the `git_checkout` /
  `git_diff` drops — so CI's symbol-granularity `govulncheck` failed on them
  rather than reading them as uncalled.

- **Secret references are no longer resolvable out of flow data.** The
  whole-string `secret://NAME` form was matched AFTER `${upstream.…}` /
  `${item.…}` substitution, so data a flow ingested from the outside world — a
  webhook body, an HTTP response, a form field, a spreadsheet cell — was
  re-interpreted as a credential reference. Anyone able to influence that data
  could read any secret in the organization by supplying the literal text
  `secret://NAME`, connection credentials (`conn.<slug>.<field>`) included, and
  through the `vault://` / `aws://` / `gcp://` schemes anything in the tenant's
  cloud secret manager. Redaction did not contain it: the drop received the
  plaintext in its params regardless of what the persisted run detail showed.
  The reference is now matched against the raw parameter only, before any
  substitution runs — mirroring `SubstituteString`, which likewise never
  re-scans its own replacements. Author-written references are unaffected.
- Stored secrets and wrapped DEKs are now bound to their row with AES-GCM
  additional authenticated data (`(tenant, name)` for a secret, `tenant` for a
  DEK). Without the binding, GCM proved only "sealed under this tenant's DEK",
  so an attacker with database write access could relocate a ciphertext — copy
  `conn.stripe.api_key`'s blob into a secret their flow may read — and recover
  the plaintext through an ordinary reference. Existing ciphertext keeps
  decrypting (an unbound open is attempted as a fallback) and upgrades to the
  bound form as values are rewritten; `--rotate-master-key` upgrades every DEK.
- `Vary: Origin` is now sent on every response, not only when the request's
  Origin matched. A shared cache could otherwise store one origin's
  `Access-Control-Allow-Origin` and replay it to a different origin. A
  disallowed origin in credentialed mode now gets no ACAO header at all rather
  than the comma-joined allowlist, which was never a valid header value.
- The auth/webhook rate limiter now reclaims per-IP buckets that were left
  DEPLETED. Token counts are only updated inside `Allow`, so an abandoned
  bucket kept a stale near-zero count and never satisfied the sweep's
  "fully refilled" test — selecting against exactly the buckets worth expiring,
  since a scanner or credential-stuffer leaves its bucket drained and never
  returns. Those entries survived until the map hit its cap and every insert
  started paying an O(n) eviction scan.
- Run quota is now enforced atomically. The monthly run-cap gate was a
  read-then-increment, so concurrent submissions at the limit could all pass
  and exceed the cap; it now reserves a slot in a single atomic step
  (`AddRunIfUnder`) before the run is enqueued.
- Stripe webhook events are recorded as processed only AFTER the plan change is
  applied (was: before), so a transient apply failure can no longer ack a
  retried delivery without ever applying the subscription state change.
- The sign-in page now validates the `return_to` parameter before navigating,
  closing an open-redirect reachable via a crafted `/signin?return_to=` link.
- The public `/form/` endpoints are rate-limited and cap their request body,
  matching the `/trigger/` surface.
- Workspace file download now refuses the internal `.scratch` tree, like the
  other file operations.
- A panic while processing a node (resolve, template rendering over untrusted
  graph data, sandbox setup) or while fanning out a webhook/event trigger no
  longer crashes the whole multi-tenant daemon: the worker recovers and
  force-fails the node, and the detached trigger fan-out recovers and logs.
- Added `X-Frame-Options: DENY` and `Referrer-Policy` to the app surface
  (clickjacking hardening); the embeddable `/form/` surface is exempt.
- Unauthenticated, DB-touching public endpoints (invitation view, SSO/auth
  config, subdomain resolve, TLS-allow, Google sign-in, handoff) are now
  per-IP rate-limited, matching the rest of the public surface.

### Fixed

- The web UI no longer reports its version as `vdev` on a production deploy. The
  image version was stamped only when `VERSION` was exported into the
  environment of the `docker compose` call — which the Makefile targets do but
  the documented production command (`docker compose -f docker-compose.yml -f
  docker-compose.prod.yml up -d --build`) does not — and compose's `${VERSION:-dev}`
  default then baked a literal `dev` into the build arg. The build now falls back
  to a committed `./VERSION` file (kept in step with the tag by `make
  patch`/`minor`/`major`), so every build path stamps a real release. This also
  restores the admin System panel's update check for all operators: it reads the
  canonical instance's reported version, which an unstamped canonical build made
  unusable.
- `make upgrade` no longer tears down a production stack. It invoked
  `docker compose` with no `-f` flags, so on a host running the Caddy + docs
  overlay it recreated the stack from `docker-compose.yml` alone — dropping TLS
  termination and the docs site — and it picked the "latest" tag with
  `git tag --sort=-v:refname | head -1`, which sorts any non-version tag (a
  `nightly`) above every release. It now takes a `PROD=1` switch that merges the
  production overlay for every stack target, selects the newest bare `X.Y.Z` tag
  (skipping pre-releases), and stays on the deployed tag instead of returning to
  master. It also refuses to recreate the stack when `caddy` is running but the
  file set it would apply omits it — a check on what is running versus what is
  about to be applied, so a host that configures the overlay through compose's
  own `COMPOSE_FILE` (the recommended setup for a permanent production host) is
  not nagged about a flag it doesn't need. New `make latest` prints the selected
  tag for use in a deploy script.
- The sign-in form's submit button is no longer permanently disabled after a
  session expires. Clearing the token re-ran the identity bootstrap effect, whose
  cleanup could flip its `cancelled` guard before the in-flight `whoami`
  settled — so the `.finally()` that clears `loading` never ran and the button
  stayed gated on a stale `loading: true`, with a full page reload the only way
  back in.

- A graph run can no longer strand permanently when a worker shuts down. Once a
  node was claimed, some of its bookkeeping still ran on the claim context, so a
  SIGTERM could land between a terminal write and the dispatch of that node's
  dependents — leaving the node terminal, the dependents never enqueued, and the
  run "running" forever, which `ReapStuckGraphRuns` cannot recover because it
  bails on a MISSING node record. The same exposure let a shutdown fail the
  graph/predecessor READS and mark a node failed for no reason. All of a claimed
  job's store I/O now runs on a context detached from the claim loop; lease loss
  remains the only thing that aborts a claimed job.
- The scheduler no longer holds its mutex across the poll-outcome marker read,
  which is a store round-trip in production — a slow or hung secret store stalled
  rescan's map swap, leader re-anchoring, and `TrackedCount` behind it.
- `Engine.Run` keeps the results of a failed node's SIBLINGS. Merging and
  error-checking shared one pass, so nodes ordered after the failing one were
  dropped from `GraphResult.Nodes` — and since a layer is sorted by node ID,
  which results survived was decided alphabetically. Loop bodies read that map,
  so a body with one failing node was silently losing its other nodes' output.
- Graph/node `timeout_seconds` is clamped against int64 overflow — a hostile
  huge value previously wrapped negative and silently disabled the run/node
  timeout instead of capping it.
- Per-item write dedupe (engine) no longer aliases or re-fires across an
  auto-fanned node; the in-memory dedupe store returns isolated copies; the
  TOTP challenge attempt-counter increments atomically.
- Run-list pagination orders by `(enqueued_at, id)` so rows that share a
  timestamp can't duplicate or vanish across pages; added a `jobs(tenant,
  status)` index for the tenant-scoped hot paths.
- A `ListJobsForGraph` scope check no longer admits records with an empty
  tenant.
- The Postgres event bus no longer permanently drops an event whose row
  committed out of `BIGSERIAL` order (a lower id committing after the listener
  advanced past it): the listener re-scans a bounded trailing window and
  dedupes, so live run/node/progress UI events aren't skipped under multi-node
  load. (Run correctness was never affected — the job store is authoritative.)
- The Postgres pool now reserves headroom for the two permanently-held
  connections (event-bus listener + leader lock) so a low `max_conns` can't
  starve the workqueue.

## [0.2.0] - 2026-06-27

### Added

- **Flow duplicate.** `POST /api/v1/me/flows/{flow_id}/duplicate` copies a
  flow under a fresh ID (new trigger URLs, empty run history) and starts it as
  a disabled draft owned by the caller, so a copied cron/webhook can't fire
  before it's reviewed. Exposed as a per-card "Duplicate" action in the flow
  list that opens the copy in the editor.
- Licensed the project under the GNU Affero General Public License v3.0 or
  later (AGPL-3.0-or-later); added `LICENSE` and a README license section.
- SPDX license headers across all Go and TypeScript source files.

### Changed

- **Write dedupe is now Postgres-backed in multi-node deployments.** The
  engine's write-dedupe store (which suppresses a re-fire of a non-idempotent
  external write — Twilio SMS, Gmail/Discord/Sheets/Home Assistant — when a
  lease reclaim or crash recovery re-runs the same job) is now the shared
  `write_dedupe` table instead of a process-local map, so a reclaim by a
  *different* `dzd` replica sees the recorded result instead of sending the
  message twice. `dzd` fails to boot if the table can't be created, matching
  every other Postgres-backed store; the in-memory store remains the
  single-node/test implementation. The contract is unchanged (at-least-once).
- Consolidated 167 scattered coverage test files (`*_cov_test.go`,
  `*_cov2-4_test.go`, `*_coverage_test.go`, `*_extra_test.go`) into their
  per-subject `_test.go` files. No test functions were removed (3306 before
  and after); only the file layout changed.
- Decluttered the repository root: moved reference docs (`DEPLOY.md`,
  `COMPLIANCE.md`, `PRIVACY.md`, `SECURITY-SLA.md`, `TODO.md`) into `docs/`
  and the `Caddyfile` into `deploy/`, updating all cross-references.
  `README.md`, `LICENSE`, `CHANGELOG.md`, and `SECURITY.md` stay at the root
  by convention.

### Removed

- Stale planning docs `GDPR_FIXES.md` and `manual.md` (history retained in
  git); fixed the dangling links in `PRIVACY.md` and `COMPLIANCE.md`.
- Orphaned root `package-lock.json` stub (no root `package.json` exists).
- The dev-only `cmd/email-preview` template-preview generator and its
  generated `email-preview.html` artifact — unreferenced by the build, CI,
  and docs. Email templates are still previewable in the web UI.
- The `scripts/ha_loadtest` multi-node HA load-test harness — never wired
  into CI or the Makefile; leader-election and failover are covered by
  `daemon/leader_test.go`.

## [0.1.0] - 2026-06-08

Initial release.

### Added

- **Flow engine.** Graph-based flows with conditional branching, fan-out
  (`for_each`), reusable subgraphs, and per-node retry policies. Runs are
  persisted and observable end to end.
- **Connectors.** Built-in integrations for HTTP, Postgres, Slack, Gmail,
  GitHub, Git, Notion, Google Sheets, and Excel, plus shell and
  transform/value utility nodes.
- **AI steps.** Claude-backed LLM nodes for generation and transformation
  inside a flow.
- **Triggers.** Start flows from inbound webhooks or timezone-aware cron
  schedules.
- **Web UI.** Visual flow builder and run viewer, with light and dark
  themes.
- **MCP server.** Exposes the connector catalog so an LLM agent can
  discover, compose, and run flows.
- **Control plane.** gRPC API with the `dzctl` CLI, plus a REST surface
  documented by an OpenAPI spec under `/api/v1`.
- **Auth & multi-tenancy.** Organizations, role-based access control, TOTP
  two-factor auth, invitations, and a platform super-admin role.
- **Secrets.** Master-key-encrypted storage for connector credentials.
- **Deployment.** Docker Compose stack (daemon + Postgres) with a boot
  guard that refuses to start on insecure defaults.
- **Versioning.** Version metadata stamped into the binary at build time,
  surfaced on `GET /api/v1` and in the web UI; `make bin`/`major`/`minor`/
  `patch`/`upgrade` release targets.
