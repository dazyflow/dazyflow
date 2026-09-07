---
title: Request & reply
sidebar_label: Request & reply
---

# Request & reply

A webhook is a notification: something happened, here are the details, carry on.
The caller wants an immediate "got it" and nothing more — which is exactly what
the [webhook trigger](./webhooks.md) gives them.

Sometimes the caller is asking a *question* instead. Is this postcode in our
delivery area? What's this customer's balance? Was that order accepted? They
send a request and wait for the answer, the way any API works.

That's the **Request** step, and its other half, **Reply**.

---

## The two steps

**Request** starts the flow when a system calls its address — and holds that
call open. **Reply** is what gets sent back.

A flow that answers looks like this:

```
Request  →  (look something up)  →  Reply
```

Add a Request step, press **Generate** to create a secret key, publish the flow,
and the panel shows you the address plus a ready-made `curl`. The caller sends:

```
Authorization: Bearer <a key>
```

Or on the end of the address, for a caller whose settings are a URL box and
nothing else:

```
https://your.dazyflow/call/<tenant>/<workspace>/<flow>?key=<a key>
```

Same key rules as the webhook: a step can hold several, any listed key works, and
revoking one is immediate — so you can rotate without a maintenance window. The
header wins if a caller sends both.

If a caller can send neither, **Answer calls with no key** on the Request step
opens the address to anyone. Think harder about this one than about the same
switch on a Webhook step: an open `/trigger` lets a stranger start a flow, while
an open `/call` also hands them whatever your Reply produces. It is off until
you turn it on.

What arrives, arrives on the Request step's output, exactly like the webhook: a
JSON body comes through as structured data, plain text as text, and **Headers**
carries the request's metadata.

## What goes back

Put a **Reply** step wherever the answer is ready.

- **Type it in.** The *What to send back* box takes text, and `${...}`
  references work in it — so `Thanks ${trigger.body.name}, we got it` sends the
  caller's own name back to them.
- **Wire it in.** Connect a value to the Reply step's **Body** input instead and
  it keeps its own type: a record or a list is sent back as JSON, text as text.
  A wired value wins over the typed one.

**Response code** is under the same step, and 200 is right until it isn't — 201
when you created something, 422 when you're turning the request down.

## Reply, then keep working

A Reply step doesn't end the flow. It answers the caller and the flow carries on
from the pass-through pin, which is the whole point of it being a step rather
than a setting.

That matters more than it sounds. Slack gives a slash command **three seconds**
to respond before it shows the user an error — but the work you want to do might
take a minute. So answer first and work second:

```
Request  →  Reply ("On it!")  →  (the slow part)  →  Send a message
```

The caller has their answer in milliseconds. The flow keeps running behind them.

Only the first Reply a run reaches answers. Two Reply steps on two branches — a
success answer and a rejection answer — is a normal shape and works exactly as
you'd expect, because only one branch runs.

## When there's no answer

Two cases, and neither is an error.

**The run ends without reaching a Reply.** A branch skipped it, or the flow has
none. The caller gets the run's id and status as JSON instead of an answer, so
they still know what happened.

**The run is still going when the wait runs out.** The address holds a call for
30 seconds. Past that the caller gets `202` with the run's id — and the run keeps
going. Nothing is cancelled; you were just told to stop waiting.

A caller that must never block can add `?wait=0` to the address and get that
`202` immediately, without waiting at all.

> **Don't put an approval behind a Request.** A step that waits for a human — an
> approval, a long delay — can't answer inside any caller's patience. The flow
> works, but every caller will time out and get the `202`. The editor warns you
> about a Request with no Reply at all; this one it can't see coming.

## If your caller retries

A client whose timeout is shorter than your flow will give up and try again —
and by default that second attempt is a second run, with every side effect
happening twice.

Send an **`Idempotency-Key`** header to stop that. Any string your caller
invents per operation will do:

```
Idempotency-Key: order-8891-confirm
```

Now the same key means the same operation, for 24 hours:

- **The first call is still running.** The retry waits on *that* run and gets its
  answer. Nothing is started twice.
- **The first call already finished.** The retry gets the same answer back,
  marked with `Idempotency-Replay: true`.
- **The same key with a different body.** Refused with `422` — that's a bug in
  the caller, and replaying the first answer would hide it.

A run that failed is remembered too, on the same reasoning: it already happened,
and its side effects already happened with it. A caller that has fixed something
and genuinely wants another attempt sends a new key.

Keys belong to the flow, not to the caller — anyone holding a key for the flow
shares its namespace, which is the same trust boundary as being able to call it.

## Which one do I want?

| | Webhook | Request |
|---|---|---|
| The caller is | telling you something | asking you something |
| They get | an immediate acknowledgement | your Reply |
| Typical caller | Stripe, GitHub, a form tool | your own backend, an AI assistant |
| Address | `/trigger/...` | `/call/...` |

If the caller is a service that retries on a slow response — a payment provider,
a source-control host — you want the webhook. Holding *their* connection while
your flow works is how you end up with duplicate deliveries.

## Trying it before it's live

Press **Send test event** in the editor and the flow runs with a payload you can
edit, exactly as a real call would. The Reply step lights up with what it *would*
have sent, so you can get the answer right before anyone calls for real.

The same is true of a scheduled or manually-started run of the same flow: nobody
is waiting, so the Reply step records what it would have sent and the flow
carries on. It never fails a run for want of a caller.

## Read next

- [Webhooks](./webhooks.md) — the fire-and-forget half.
- [Forms](./forms.md) — a page Dazyflow hosts, for when the caller is a person.
- [Web APIs](./web-apis.md) — the other direction: calling someone else's API
  from inside a flow.
- [MCP servers](./mcp-servers.md) — exposing a flow to an AI assistant, which is
  the same question-and-answer shape with the plumbing done for you.
