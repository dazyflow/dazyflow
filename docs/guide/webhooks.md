---
title: Webhooks
sidebar_label: Webhooks
---

# Webhooks

A webhook is how one system tells another that something happened: a payment
went through, a pull request opened, an order was placed. The sender doesn't
wait around — it delivers the news, gets an acknowledgement, and moves on.

That's what the **Webhook** step is for. Add one, give it a key, and anything
that can send an HTTP request can start your flow: Stripe, GitHub, Zapier, a
shop platform, a shell script, your own backend.

---

## The address and the key

Add a Webhook step and press **Generate** to create a secret key. Callers send
it as a header:

```
Authorization: Bearer <a key>
```

The panel shows a ready-made **curl** command with your key already in it, so
you can check the whole path end to end before wiring up the real caller. Two
things have to be true before it is accepted, and the panel says so when they
aren't: you need to have generated a key (before that the command carries a
placeholder), and the flow has to be **published** — a key you just generated is
part of your draft until you publish it, exactly like any other edit.

What arrives, arrives on the step's outputs:

- A **plain-text body** comes through as text.
- A **JSON body** (sent with `Content-Type: application/json`) comes through as
  structured data, so later steps can pick out individual fields.
- **Headers** carries the request's metadata, minus anything that would leak a
  credential.

## Rotating a key without downtime

A step can hold several keys, and **any** listed key works. So a key rotation
never needs a maintenance window:

1. **Generate a key.**
2. Switch your callers over to the new one.
3. Revoke the old one.

Revoking is immediate: anything still calling with that key stops working at
once. Other keys keep going. Revoke the *only* key and the flow stops accepting
webhook deliveries entirely.

## Is it actually receiving?

The step tells you, in one line:

- **Not receiving yet** — no key has been generated, so every delivery is
  rejected.
- **Receiving** — systems that send the key can start this flow.
- **Receiving, but** — senders reach the last published version, because your
  draft has moved on since.

Two things to remember beyond that line:

- **Publish.** An unpublished draft doesn't receive. Test with **Send test
  event**, then publish. See [Make a flow run by
  itself](./triggers-and-schedules.md).
- **A paused step refuses deliveries** rather than accepting them and doing
  nothing. If callers are getting turned away, check whether the flow or that
  step is paused.

## What it always answers

A webhook delivery is acknowledged the moment the run starts, and that
acknowledgement never carries a result. That's deliberate, and it's what senders
like Stripe and GitHub expect: hold their connection while your flow works and
you'll trip their delivery timeout, which for most of them means retrying — so
your flow runs twice.

If your caller *is* waiting for an answer, it wants the
[Request step](./request-and-reply.md) instead, which is a different address
with a different promise.

## Test it with a realistic payload

Under **Test run with sample input** you can edit a JSON payload and fire it
through the step. The run uses your **current draft**, so this is how you check
a change before publishing it.

Paste in a real example from whatever system will be calling you — you'll find
out about a field name mismatch now, rather than at 02:00 next Tuesday.

---

## Where next

- [Forms](./forms.md) — the same idea when the sender is a person.
- [Request & reply](./request-and-reply.md) — when the caller waits for an answer.
- [Make a flow run by itself](./triggers-and-schedules.md) — publishing, pausing and schedules.
- [When a run fails](./when-a-flow-fails.md) — reading a failed delivery.
- [Step catalog: webhook](https://docs.dazyflow.app/reference/steps/webhook) — the *outbound* step, for sending data to someone else's webhook.
