# Changelog

All notable changes to Dazyflow are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

The repository, Go module, and daemon binary are named `dazyflow` / `dzd`.
Versions here correspond to git tags `X.Y.Z` on
[git.sr.ht/~klahr/dazyflow](https://git.sr.ht/~klahr/dazyflow). The running
version is stamped into the binary at build time and surfaced on
`GET /api/v1` (the `build` block) and in the web UI's account menu.

Releasing: write what shipped under `[Unreleased]` as you go, then run
`make patch` (or `minor` / `major`). That target promotes `[Unreleased]`
under a new `[X.Y.Z] - YYYY-MM-DD` heading, leaves a fresh empty
`[Unreleased]`, and commits the changelog together with `./VERSION`
before tagging — so the tag lands on the commit that announces the
version. An empty `[Unreleased]` aborts the release. (`VERSION` matters
because the Docker build reads that file when it isn't handed a `VERSION`
build arg, so it is what a production `docker compose up --build` stamps
into the image.)

## [Unreleased]

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
