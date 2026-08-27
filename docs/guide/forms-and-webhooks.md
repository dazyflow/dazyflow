---
title: Forms & webhooks
sidebar_label: Forms & webhooks
---

# Forms & webhooks

Some flows should start when someone *outside* Dazyflow does something — a
customer fills in a form, your online shop records an order, a colleague's script
posts a file. That's what the webhook trigger is for, and it comes in two halves:
a form Dazyflow hosts for you, and an address other software can call.

You can use either, or both at once, on the same trigger.

---

## The hosted form

Add a webhook trigger, select it, and switch on **Host a form for me (no website
needed)**.

You immediately get a **Form link** you can share — in an email, a QR code, a
chat message. Anyone who opens it gets a real form; each submission starts a run
of your flow with what they typed.

Three things to set:

- **Form heading** — what the person filling it in sees at the top.
- **Form fields** — the questions. Each field you add here becomes something your
  flow can use, so a *Name* and *Email* field gives your later steps a name and
  an email to work with.
- **Customize the form** — the finishing touches, once the fields are right.

Because you declared the fields, the steps after the trigger know what's coming
before the first submission ever arrives. That's why a *Save to spreadsheet* step
can offer you your form's columns straight away, instead of waiting for real data
to learn them.

> **Anyone with the link can submit.** There's no sign-in — that's the point of a
> public form. Treat the link as public, and don't put a step behind it that you
> wouldn't want a stranger triggering.

### Put it on your own website

Under **Put this form on my own website** you'll find a snippet to paste into
your site's HTML, wherever you want the form to appear.

The form stays hosted by Dazyflow — you're embedding it, not rebuilding it — so
no key or secret ends up in your web page's source.

## The webhook address, for other systems

If the caller is software rather than a person, open **For developers** on the
same trigger.

Other systems start the flow by sending a request to the flow's address with a
**secret key**. Generate a key, and callers send it as a header:

```
Authorization: Bearer <a key>
```

The panel shows a ready-made **curl** command that works exactly as printed, so
you can check the whole path end to end before wiring up the real caller.

What arrives, arrives on the trigger's output:

- A **plain-text body** comes through as text.
- A **JSON body** (sent with `Content-Type: application/json`) comes through as
  structured data, so later steps can pick out individual fields.

This is how you connect anything that can send an HTTP request — Zapier,
Typeform, a shop platform, a shell script, your own backend.

### Rotating a key without downtime

A trigger can hold several keys, and **any** listed key works. So a key rotation
never needs a maintenance window:

1. **Generate a key.**
2. Switch your callers over to the new one.
3. Revoke the old one.

Revoking is immediate: anything still calling with that key stops working at
once. Other keys keep going. Revoke the *only* key and the flow stops accepting
webhook calls entirely — unless the hosted form is on, which is its own way in.

## Is it actually receiving?

The trigger tells you, in one line, without you having to reason about it:

- **Not receiving yet** — turn on the form, or add a secret key under **For
  developers**.
- **Receiving** — via the form link.
- **Receiving** — via the form link *and* the secret key for other systems.

Two things to remember beyond that line:

- **Publish.** An unpublished draft doesn't receive. Test with **Run** or **Send
  test event**, then publish. See [Make a flow run by
  itself](./triggers-and-schedules).
- **A paused trigger refuses deliveries** rather than accepting them and doing
  nothing. If callers are getting turned away, check whether the flow or that
  trigger step is paused.

## Test it with a realistic payload

Under **Test run with sample input** you can edit a JSON payload and fire it
through the trigger. The run uses your **current draft**, so this is how you
check a change before publishing it.

It's the honest test: the same path a real caller takes, with data you control.
Paste in a real example from whatever system will be calling you — you'll find
out about a field name mismatch now, rather than at 02:00 next Tuesday.

---

## Where next

- [Make a flow run by itself](./triggers-and-schedules) — publishing, pausing and schedules.
- [When a run fails](./when-a-flow-fails) — reading a failed delivery.
- [Step catalog: webhook](../reference/steps/webhook) — the trigger's inputs and outputs in detail.
