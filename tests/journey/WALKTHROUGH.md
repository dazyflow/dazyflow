# Newcomer walkthrough (lived experience)

This is the browser half of the experience test. I drove the real web app
as a brand-new, non-technical user (call her Nina, who runs a bakery and
has never seen Hazy Flow) and recorded where the journey was smooth and
where she would get stuck. Screenshots are in `screenshots/`.

Setup for the run: a freshly built `hzd` serving the freshly built
`web/dist`, signup enabled, on `http://localhost:8087`. The durable,
repeatable version of this journey lives in `journey_test.go`; this file
is the narrated, screenshot-backed pass a non-technical person actually
sees.

## The happy path

1. **Sign in screen** (`01-signin.png`). Nina lands on a clean sign-in
   page with an obvious "Create an account" link. No jargon. Good.
2. **Sign up** (`02-…`). Email, password with an "8 characters or more"
   hint, confirm password. Clear.
3. **Welcome page** (`03-welcome.png`). After signing up she lands on a
   genuinely good orientation page: "So, what are we building today?"
   with three plain-language paths, "Get notified when something
   happens", "Send a report on a schedule", "Move or organize my data",
   plus "Grab a template" and "Start from scratch". This is exactly right
   for someone non-technical: it speaks outcomes, not nodes.
4. **Templates** (`04-templates.png`). The template gallery is grouped by
   the same intents and reads in plain English ("Daily Google Sheet ->
   PDF in your inbox", "Save incoming emails to a spreadsheet"). These map
   one-to-one onto the scenarios in `scenarios.md`. This is where a
   non-technical user starts, not the blank canvas.
5. **Fork a no-setup template** (`05-editor-contact-form.png`). Nina picks
   "Collect contact-form entries (no setup)", which forks into a flow and
   opens the editor showing `Webhook input -> Save to built-in store`.
6. **Send a test event** (`06-test-event-succeeded.png`). One click on
   "Send test event" runs the flow end to end: both steps go green
   (succeeded). She has a working automation, signed up to first success,
   with zero configuration. This is the same flow as scenario 7
   (lead intake), and the same path `journey_test.go` runs headless.

## Where a newcomer gets stuck (and what it actually is)

None of the three snags below are app bugs. Two are self-host setup, one
was a stale local build. Worth knowing so we don't mis-file them.

- **Most templates are locked behind admin setup** (`04-templates.png`,
  `07-connections-not-configured.png`). Every template that touches Gmail,
  Sheets, Slack, or Notion shows "Available after your administrator
  enables …" and a disabled button. On a fresh self-host with no OAuth
  apps configured, that is 8 of our 10 scenarios. The Connections page
  says so honestly: "Setup isn't finished on this server yet … Contact
  whoever set up this Hazy Flow workspace." So the product communicates
  the wall well, but the wall is real: a non-technical user cannot connect
  Google/Slack/Notion until an admin registers OAuth client credentials
  and a secret store. The scenarios that need no accounts (contact form ->
  saved list, Excel -> built-in DB) work immediately; everything else
  waits on that one-time admin step.

- **CSRF origin must be set for self-host.** Running `hzd` without
  `HAZYFLOW_WEB_ORIGIN` made signup fail once a session cookie was present
  with "cookie-authenticated request from disallowed origin … (CSRF
  defense)". This is by design and documented in DEPLOY.md; the fix is to
  set `HAZYFLOW_WEB_ORIGIN` to the public origin. The app surfaces the
  error inline rather than failing silently, which is good, but the
  message is operator-speak a non-technical user can't act on alone.

- **A stale web bundle, not a shipped bug.** My first run served an
  out-of-date local `web/dist` that still called the retired
  `/api/v1/whoami` endpoint. Signup succeeded, then that 404 bounced Nina
  back to a sign-in screen showing "404 page not found"
  (`02-after-signup-404.png`). `web/dist` is a gitignored build artifact;
  the source already calls `/me`, and the Docker image rebuilds the bundle
  on every build, so production is fine. Rebuilding locally
  (`cd web && npm run build`) fixed it. The lesson is operational: never
  serve a `web/dist` older than the server after an API rename.

## Verdict

The app itself is strong and genuinely non-technical-friendly: the
welcome page, the intent-grouped templates, the one-click "Send test
event", and the honest "ask your admin" empty states are all well judged.
The friction a newcomer hits is self-host *setup* (register OAuth apps,
set the web origin), not the product. The fastest path to a first working
automation, and the one a clueless user can complete unaided, is a
no-setup template like the contact-form capture.
