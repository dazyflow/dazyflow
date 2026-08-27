---
title: Make a flow run by itself
sidebar_label: Triggers & schedules
---

# Make a flow run by itself

A flow with no trigger only ever runs when you press **Run**. That's fine while
you're building, but the point of Dazyflow is that it does the work when you're
not looking. Two things make that happen: a **trigger**, and a **publish**.

---

## Add a trigger

A **trigger** is the step that starts a flow. Every flow needs one before it can
run on its own, and there are four kinds:

| Trigger | Starts the flow… |
| --- | --- |
| **Schedule** | On a clock — every morning at 08:00, every 15 minutes. |
| **Form or webhook** | When someone submits its built-in form, or another system sends it data. |
| **App event** | When something happens in a connected app — a Stripe payment, a new pull request. |
| **Manual** | Only when you press **Run**. |

Add one from the step catalog like any other step, then click it. Its settings
open in the panel on the right — that's where you set the time, or get the form
link.

A flow can have more than one trigger. A daily report that you also want to fire
by hand can have both.

## Set a schedule

Select the schedule trigger and pick from the presets:

- **Every hour** — choose the minute past the hour.
- **Every day** — choose the time.
- **Every week** — choose the days and the time.
- **Every month** — choose the day of the month and the time.
- **Custom** — for anything the presets don't cover.

**Times are in your timezone**, and the panel says which one it's using and when
the flow will next fire, so you don't have to do the arithmetic yourself.

> Picking a monthly schedule on day 29, 30 or 31? Dazyflow warns you: months
> without that day simply skip, so a "monthly" flow set to the 31st won't fire in
> February. Day 1 or 28 is safer.

**Custom** takes a five-field cron expression — minute, hour, day-of-month,
month, day-of-week. `0 9 * * 1-5` means 09:00 on weekdays. The panel shows the
next few fire times as you type, so you can check you've got it right before
trusting it. If cron means nothing to you, you don't need it — the presets cover
almost everything.

## Publish to make it live

This is the part people miss. A **draft** never fires on its own, no matter what
trigger it has. Saving is not publishing.

Click **Publish**, and:

> Its triggers will start running this version. Your draft stays editable — only
> publishing again pushes new edits live.

That's the whole model, and it's worth reading twice:

- **The published version is what runs.** Schedules, forms and app events all
  hit the version you last published.
- **Your draft is yours to break.** Edit freely; the live version doesn't change
  until you publish again.
- **Run always uses your draft.** So you can test an edit before anyone else
  meets it.

You can give a release a name when you publish it — *"Black Friday config"* —
which makes the version history readable when you need to look back at what
changed. Past versions stay available, so you can view an earlier one and return
to the current one.

## Test it without waiting for the clock

You don't have to wait until 08:00 to find out whether your morning report works:

- **Run** fires the flow once, immediately, using your draft. For a trigger that
  normally waits for something, the editor says it plainly: *"Runs once now, to
  test. To run automatically on new responses, click Publish."*
- **Send test event** fires the flow with a sample submission, end to end — the
  right way to test a form or webhook flow before pointing anything real at it.

Both write a normal entry to **Runs**, so you can read the result the same way
you'd read a real one.

## Pause without deleting

Sometimes you want a flow to stop firing for a while — a seasonal flow, or one
whose downstream system is having a bad day.

- **Pause the schedule** and it stops firing while the rest of the flow stays as
  it is. The **Flows** list shows **Paused** where it would otherwise show the
  next run time, with a **Resume** button right beside it.
- **Pause the whole flow** and nothing triggers it at all — the list says **Flow
  paused**.
- **Pause a single trigger step** and only that entry point stops; the flow's
  other triggers keep working.

A paused trigger genuinely stops: a paused form or webhook trigger refuses
incoming deliveries rather than accepting them and quietly doing nothing.
**Resume** puts it back exactly as it was.

Deleting a trigger, by contrast, is permanent — and removing a webhook trigger
also turns off its hosted form.

---

## Where next

- [Forms & webhooks](./forms-and-webhooks) — the form link, embedding it, and calling a flow from code.
- [When a run fails](./when-a-flow-fails) — what to do when an automatic run goes wrong at 03:00.
- [Step catalog: triggers](../reference/steps/triggers) — every trigger step in detail.
