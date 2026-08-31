---
title: Build your first flow
sidebar_label: First flow
---

# Build your first flow

Ten minutes, no accounts, nothing to install. You'll watch a flow run, read what
it did, change it, and then set it live. If a word here is new to you, [How
Dazyflow works](./concepts) explains the ideas behind it and the
[Glossary](./glossary) defines them one at a time.

---

## 1. Create a flow

Open **Flows** in the sidebar and click **New flow**. There are three ways in:

| | Best for |
| --- | --- |
| **From a template** | Your first few flows. You get a working flow and adjust it. |
| **AI assisted** | Describing what you want in a sentence and letting Dazyflow draft it. Needs a connected AI provider. |
| **Blank** | An empty canvas, once you know your way around. |

**From a template** opens first, and it's the one to start with. A blank canvas
is the hardest way to begin — it's easier to change a flow that already works
than to invent one from nothing.

## 2. Pick the template that needs no setup

In the gallery, find **See a flow run (no setup)** under the **Try it now**
category, and click **Use this template**.

Every other template needs an account connected somewhere — a Google Sheet, a
Slack channel. This one carries its own sample table, so it needs nothing.

You land in the **flow editor**, looking at two steps joined by a line: a sample
table, feeding a step that formats it into a tidy summary.

## 3. Press Run

Click **Run** in the toolbar.

A moment later you get **"Done — the flow ran."** and the text the last step
produced. That's a complete [run](./glossary#run): Dazyflow carried out every
step in order and handed you the result.

Click **See the full run** for the detail: every step, in the order it happened,
with what each produced. This is the page you'll live on when something
misbehaves — worth a look now, while everything works.

> **Run** is for testing. It runs the flow once, immediately, using whatever is
> on your canvas right now — including unsaved edits. It never waits for a
> schedule.

## 4. Change something

Click the second step. A panel opens on the right with that step's settings —
this is where you configure a step once it's on the canvas.

Change the heading text, then press **Run** again. The output changes. Nothing
you do here can break anything: this flow talks to no outside service, so it has
nothing to send, post, or delete.

While you're in there, notice that some values can be **filled in** on the step
and others **connected** from an earlier step. That distinction is the single
most useful thing to understand about building flows, and [How Dazyflow
works](./concepts) covers it properly.

## 5. Save, then publish when you mean it

**Save** stores your edits. Your flow is still a **draft**, and a draft never
fires on its own — in the sidebar, a small pencil beside the flow's name means
*Draft — not published*. That's deliberate: you can build and test in peace
without emailing real customers by accident.

**Publish** makes the current version live, so its trigger starts running. If a
flow has a trigger but hasn't been published, the editor tells you plainly:

> This flow has a trigger but hasn't been published — nothing runs it until you
> publish. (Run still works for testing.)

Publishing doesn't lock anything. Your draft stays editable, and only publishing
again pushes new edits live — so the version your customers hit never changes
under you while you're mid-edit.

This first flow has no trigger, so there's nothing to publish yet. That's the
next page.

---

## What to build second

Now build something real. Three good second flows, all in the gallery:

- **Watch a page → ping my phone.** Checks a web page every hour and notifies you
  only when the words on it actually change. Needs no account — just an
  [ntfy](https://ntfy.sh) topic name.
- **Web form → text me.** Gives you a public form link (and a snippet to embed it
  on your own site) and sends you an SMS for every submission, with the sender's
  number so you can call back. Needs a 46elks key, which you paste in yourself.
- **Web form → Google Sheet.** The same form, with each submission landing as a
  row in a sheet instead. Needs a Google account connected, which on a
  self-hosted install means an administrator has set Google up first.

Anything that touches an outside service needs it connected first. If you press
**Run** and Dazyflow says **"Set up this flow first"**, that's what it's telling
you — see [Connect an app](./connect-an-app).

## Where next

- [Connect an app](./connect-an-app) — sign in to Gmail, Slack, Fortnox and the rest, once.
- [Make a flow run by itself](./triggers-and-schedules) — schedules, triggers and publishing.
- [When a run fails](./when-a-flow-fails) — reading a failure and retrying it.
- [Step catalog](../reference/steps/) — everything you can add to a flow.
