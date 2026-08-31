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

Open **Customize the form** — that's where both of the settings worth changing
live:

- **Form fields** — the questions, comma-separated. Each field you add here
  becomes something your flow can use, so a *Name* and *Email* field gives your
  later steps a name and an email to work with. Leave it blank and you get
  *name*, *email*, *message*.
- **Form heading** — what the person filling it in sees at the top. Defaults to
  the flow's name.

A field whose name reads like a question rather than a column — *What you like
about us*, *Your feedback* — is drawn as a multi-line box, so there's room to
write a paragraph. *Email* and *Phone* get the matching keyboard on a phone.

Because you declared the fields, the steps after the trigger know what's coming
before the first submission ever arrives. That's why a *Save to spreadsheet* step
can offer you your form's columns straight away, instead of waiting for real data
to learn them.

> **Anyone with the link can submit.** There's no sign-in — that's the point of a
> public form. Treat the link as public, and don't put a step behind it that you
> wouldn't want a stranger triggering.

Two things guard it for you, with nothing to configure. The form carries a
hidden field a person never sees: an automated script that fills in every input
it finds completes that one too, and the submission is dropped without starting
a run. And submissions are rate-limited per caller, so nobody can hammer the
form fast enough to fill your collection — or burn through your monthly runs.

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

The panel shows a ready-made **curl** command with your key already in it, so
you can check the whole path end to end before wiring up the real caller. Two
things have to be true before it is accepted, and the panel says so when they
aren't: you need to have generated a key (before that the command carries a
placeholder), and the flow has to be **published** — a key you just generated is
part of your draft until you publish it, exactly like any other edit.

What arrives, arrives on the trigger's output:

- A **plain-text body** comes through as text.
- A **JSON body** (sent with `Content-Type: application/json`) comes through as
  structured data, so later steps can pick out individual fields.

If you'd rather post to the **form** address than the trigger one — because you
built the form's HTML yourself and don't want to manage a key — it accepts an
ordinary form submission (`application/x-www-form-urlencoded` or
`multipart/form-data`) and a flat JSON object. Send it anything else and it
answers `415` rather than accepting a submission it can't read.

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
  itself](./triggers-and-schedules.md).
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

- [Make a flow run by itself](./triggers-and-schedules.md) — publishing, pausing and schedules.
- [When a run fails](./when-a-flow-fails.md) — reading a failed delivery.
- [Teams & approvals](./teams-and-approvals.md) — have a person check a submission before the flow acts on it.
- [Step catalog: triggers](https://docs.dazyflow.app/reference/steps/triggers) — the webhook trigger's settings and outputs in detail.
- [Step catalog: webhook](https://docs.dazyflow.app/reference/steps/webhook) — the *outbound* step, for sending data to someone else's webhook.
