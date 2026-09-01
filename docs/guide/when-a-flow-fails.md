---
title: When a run fails
sidebar_label: When a run fails
---

# When a run fails

Flows fail. A service goes down, a password expires, someone renames a
spreadsheet column. What matters is that you find out, understand it in a
sentence, and fix it without redoing work that already succeeded.

---

## Find the failed run

Open **Runs** in the sidebar. Every run of every flow is listed with its status:

| Status | Means |
| --- | --- |
| **Succeeded** | Ran to the end. |
| **Failed** | Stopped at a step that couldn't finish. |
| **Running** | In progress right now. |
| **Waiting for approval** | Paused at an approval step, waiting for a person. |
| **Queued** | Accepted, about to start. |
| **Stopped** | You stopped it. |

Filter by **Failed** to see just the problems, or search by flow name and narrow
by date when you're chasing something specific.

Click a run to open it.

## Read what happened

The run page shows every step in the order it happened, and says where things
went wrong:

> **Failed at "Send invoice email" after 3 attempts**

Then it explains the cause in plain language — not an error code. Some of the
ones you'll actually meet:

- *"Gmail isn't connected yet on your Dazyflow workspace."*
- *"This flow uses a secret called "FORTNOX_TOKEN", but it hasn't been added yet."*
- *"Slack couldn't find the channel this flow tries to post in. The bot might also
  need to be invited to it."*
- *"The step took too long and was stopped."*

Most failures are one of a handful of causes, and nearly all of them are setup
rather than logic — an app not connected, a Slack channel the bot was never
invited to, a key that expired. [Connect an app](./connect-an-app.md) covers the
fixes for those.

If a step won't retry by itself, the page says so directly: *"This step won't
retry on its own — fix the cause above, then use Retry from failure."*

## Retry, Replay, Stop

Three buttons, and the difference between the first two matters:

**Retry** resumes from the failed step, reusing the work that already succeeded.
This is almost always what you want. If a flow processed 400 rows and failed on
the email at the end, Retry doesn't process the 400 rows again.

**Replay** re-runs the whole flow from the start. Everything happens again —
**including side effects**. Emails already sent get sent again; messages already
posted get posted again. Dazyflow asks you to confirm and says exactly that,
because "just run it again" is the instinct that sends a customer two invoices.
Use Replay when you want a genuinely fresh run, not to recover from a failure.

If a webhook or a hosted form started the run, Replay sends the flow the same
data again: the body that arrived with the original request is kept with the
run, so a re-run starts from it rather than from nothing. (A run nobody sent
anything to has no delivery to replay — Dazyflow says so instead of starting a
run that would die on its first step. Press **Run** on the flow, or use **Test
event** in the editor, to try it with data of your own.)

**Stop** halts a run that's still going. Steps already done stay done; the rest
don't run. You can retry it afterwards.

You can also select several failed runs on the **Runs** page and retry them
together — useful after fixing one cause that broke a morning's worth of runs.
The same side-effect warning applies, so read the count before you confirm.

## Some steps retry themselves, some never will

Every step in the [catalog](https://docs.dazyflow.app/reference/steps/) carries a **Behaviour** line,
and it tells you what happens on a hiccup:

- **Safe to retry** — if it fails because the network coughed, Dazyflow quietly
  tries again. Nothing for you to do. That's the *"after 3 attempts"* in the
  message above.
- **Runs once** — it does something that must not happen twice: sending a
  message, charging a card, moving money. Dazyflow will **not** repeat it on its
  own. It waits for you.

That's why a failed run sometimes shows three attempts and sometimes one. It
isn't inconsistency — it's the difference between a step that's safe to repeat
and one that isn't.

## Get told, so you don't have to check

A flow that runs at 03:00 is no use if you find out on Friday that it stopped
on Monday.

In the flow's **Settings → Notifications**, switch on **Email on failure**. You
get a plain-text summary at your account email when a run of that flow fails.
Two deliberate limits:

- **Only runs that started on their own** — a schedule, a webhook, a form. A run
  you started yourself is one you're already watching.
- **At most once an hour per flow**, however many times it fails. A broken flow
  firing every five minutes sends you one email, not two hundred.

This needs the server's email delivery to be set up. If nothing arrives, that's
the thing to ask your administrator about.

Under **Advanced: send to Slack, Teams, or another service** you can instead
have failures POSTed to a URL, with the flow id, run id, error code and message
in the payload — for routing into an on-call channel or an incident tool.

## Still stuck

The run page has **Get help with this run**, which opens a support request with
the run already identified — so you don't have to describe which of your runs you
mean, or copy identifiers by hand.

---

## Where next

- [Connect an app](./connect-an-app.md) — the fix for most failures.
- [Make a flow run by itself](./triggers-and-schedules.md) — pausing a flow while you investigate.
- [Glossary](./glossary.md) — any word here you'd like pinned down.
