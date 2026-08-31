---
title: How Dazyflow works
sidebar_label: Concepts
---

# How Dazyflow works

This page explains the handful of ideas everything else builds on. You don't
need to be technical — if you can follow a recipe, you can build a flow. Read it
once and the rest of the docs will make sense. Any unfamiliar word is defined in
the [Glossary](./glossary.md).

---

## A flow is a recipe that runs itself

A **flow** is a set of instructions Dazyflow carries out for you, start to
finish, without you watching. Think of a recipe card: a list of steps, in order,
that turns ingredients into a finished dish.

> *"When a customer pays an invoice, look up their phone number and text them a
> thank-you."* — that's a flow.

You build a flow by dragging **steps** onto a canvas and connecting them, like
drawing a little flowchart. Dazyflow then runs it whenever you want — on a
schedule, when something happens, or when you press **Run**.

## Steps are the individual instructions

Each **step** does one small thing: send an email, look up an order, filter a
list, wait for approval. A flow is just several steps connected in order.

Every step has three parts:

- **Inputs** — what it needs to do its job (a phone number, a message).
- **Settings** — choices you make on the step (which mailbox, what format).
- **Outputs** — what it hands to the next step when it's done (a message id, an
  order's status).

You'll see steps grouped in the catalog as **Apps & services** (Gmail, Klarna,
Slack…), **Triggers** (what starts a flow), and **Building blocks** (the
general-purpose tools for shaping data).

## Connecting steps: outputs into inputs

Steps pass their results down the line. The **output** of one step becomes the
**input** of the next — you draw a line between them. This is called **wiring**
(or "connecting").

> The *Get order* step outputs a **phone number**; you connect that into the
> *Send SMS* step's **To** input. Now the SMS goes to whoever placed the order —
> automatically, every time, without you typing anything.

## Two ways to give a step a value: fill it in, or connect it

This is the single most useful thing to understand:

1. **Fill it in (a Setting).** You type a fixed value on the step — e.g. always
   send from *orders@myshop.se*. Good for things that never change.
2. **Connect it (an Input).** You wire in a value from an earlier step — e.g.
   the customer's phone number, which is different every run. Good for things
   that change each time.

In the step catalog you'll see the same value listed under both **Settings** and
**Inputs**. That's not a mistake: a connected input, when present, overrides the
typed setting. Type a default; connect one when you have something better.

## The kinds of value that flow between steps

Values come in a few plain types. The catalog labels each input and output with
one:

| Type | What it is |
| --- | --- |
| **Text** | Plain words — a message, a name, an address. |
| **Item** | Structured info — a row from a spreadsheet, or an object with named fields (like `{name, email}`). |
| **Items (a table)** | *Many* items at once — many rows. **Texts** and **Files** are the same idea for the other types. |
| **Yes / no** | A true-or-false answer, used to make decisions. |
| **File** | A document — a PDF, a spreadsheet, an image. |
| **Anything** | The step accepts whatever you connect. |

These are the exact words you'll see on a step's pins when you hover one, and in
the step catalog — so what you read here is what the product says back to you.

When a step produces **Items** (many rows) and the next step handles **one at a
time**, wrap it in a **For each** loop — see the Glossary.

## Triggers decide when a flow starts

Every flow begins with a **trigger** — the step that kicks it off. There are a
few kinds:

- **Schedule / Interval** — on a clock: every morning at 8, or every 15 minutes.
- **Webhook** — when another system sends it something, or someone submits its
  built-in form.
- **App events** — when something happens in a connected app (a Stripe payment,
  a new GitHub pull request).
- **Manual** — only when you press **Run**. Handy while you're building.

## A run is one execution of a flow

Each time a flow fires, that's a **run**. The **Runs** page shows every run,
step by step, so you can see what happened and spot anything that went wrong.

Steps tell you how they behave if something fails. In the catalog you'll see a
**Behaviour** line:

- *"Safe to retry"* — if it fails (say the network hiccups), Dazyflow quietly
  tries again. Nothing for you to do.
- *"Runs once"* — it does something that shouldn't happen twice, like sending a
  message or moving money, so Dazyflow won't repeat it on its own.

When a run does need your attention, Dazyflow explains the problem in plain
language and tells you whether it will retry or needs you to step in.

## Apps and connections: log in once, safely

To use an outside service (Gmail, Klarna, Fortnox…) you first **connect** it on
the **Apps** page — sign in, or paste the key it gives you. You do this **once**;
after that, any flow can use that app.

Your passwords and keys live in the connection, **not** in the flow. They're
kept hidden and are never shown in the flow itself — so you can share or export
a flow without leaking a credential.

## Drafts vs published

A brand-new flow is a **draft**. Drafts don't fire on their own — so you can
build and test in peace without, say, emailing real customers by accident. When
you're happy, you **publish** it, and from then on its trigger is live.

## Putting it together

Here's the thank-you example as a full flow, in plain words:

1. **Trigger — When an invoice is paid** (checks Fortnox every few minutes).
2. **Look up the customer** to get their phone number.
3. **Phone** step tidies the number into a proper international format.
4. **Send SMS** (46elks) — its *To* is connected from step 3, its message typed
   on the step.

Four steps, wired output-to-input, running on their own. That's Dazyflow.

---

## Where next

- **[Build your first flow](./first-flow.md)** — ten minutes, no accounts needed.
- **[Connect an app](./connect-an-app.md)** — so a flow can reach Gmail, Slack, Fortnox.
- **[Triggers & schedules](./triggers-and-schedules.md)** — make a flow run by itself.
- **[Step catalog](https://docs.dazyflow.app/reference/steps/)** — everything you can add.
- **[Glossary](./glossary.md)** — any word you didn't recognise above.
