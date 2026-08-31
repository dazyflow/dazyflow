---
title: Web APIs — steps for your own service
sidebar_label: Web APIs
---

# Web APIs — steps for your own service

Your team runs a service. It has a REST API, and no built-in step covers it —
nor will one, because it is yours.

You can already call it with the **Web request** step. That works, and it is
where most people start. What it cannot do is name anything: every flow that
touches your service re-types the address, the method, the headers and the
`${secret.…}`, and nothing in the palette says what any of it does.

Describe the API once in **Admin → Web APIs** and each endpoint becomes a real
step instead.

---

## What you get

Describe a `GET /orders/{order_id}` on a service you call *Order service*, and
you get a step labelled *order-service — get_order*, whose id is
`api:order-service:get_order`. It appears in the catalog under **Apps &
services**, and it behaves like any built-in step: it branches, loops, retries
and shows up in the run log the same way.

Three things you did not have before:

- **The arguments are fields, and pins.** `order_id` is a required field the
  editor checks before a run, and a pin you can wire from an upstream step. Up to
  twelve of an operation's arguments become pins — the required ones first — and
  the rest stay fields.
- **The status, response and headers come out separately**, so a Branch can test
  the status code directly. Same three outputs as the Web request step.
- **The flow assistant can use it.** Ask for "when a form comes in, place an
  order" and it composes against your own endpoints, because it reads the same
  descriptions you typed.

### The address and the key are not in the flow

This is the part that saves the most work. Neither the address nor the
credential is typed into a flow: every step of every flow uses them without
holding either, and rotating a key is one edit, not one per step.

They live in two different places, on purpose:

| | Where it is set | Who can change it |
| --- | --- | --- |
| **Service address** | Admin → Web APIs, on the catalog | An org admin |
| **Credential** | The catalog's page under **Apps**, like Gmail or Stripe | Anyone who manages secrets |

That split is why the auth question on the admin page only asks for the *shape*
— bearer token, or a header the service names. The value itself is entered on
the Apps page after you save.

It is also a boundary worth knowing about. The token is sent to whatever address
the call resolves, so letting the address be set alongside the credential would
mean anyone who can manage secrets could point your calls at another host and be
handed the key. The address belongs to the catalog, and only an admin edits it.

If one step genuinely needs a different host — a staging instance, a one-off —
set the `base_url` param on that step. That changes one step in one flow, which
is the same kind of change as editing any other step.

---

## Names, and what they are not

Two of the fields on the admin page are **names** and the rest are
identifiers, and it is worth knowing which is which.

The catalog's **Name** and each operation's **Display name** are display only. They are
what the palette shows — *Order service — Fetch an order* — and you can change
them whenever you like: nothing references them, so renaming re-captions the
steps and every flow keeps working.

The catalog's **id** and each operation's **id** are what flows hold
(`api:<catalog>:<operation>`). Those are fixed: renaming one is a new step, and
flows using the old one stop resolving.

Leave a name blank and the step falls back to the id — which reads like the
identifier it is (*order-service — get_order*), so it is worth filling in for
anything a non-technical colleague will pick from the palette.

An operation's **Summary** is a different field again: a sentence, shown under
the name as the step's subtitle. Put the sentence there, not in the name.

### What this service is

The catalog also takes a **description** — a short paragraph about the service
itself, not about any one call. It appears on the service's page under **Apps**,
under its name, and it is searchable from the apps list.

This is the one piece of prose about your own API that nobody else can write.
Every built-in app's description ships with Dazyflow; yours has no such source,
so an undescribed catalog is a card with a name and nothing else on it — which is
what a colleague sees when they are trying to work out whether this is the app
they need.

Say what the service does, in the language the reader uses. The operations carry
their own summaries, so this is not the place to list them:

> Our order system. Look up an order, place one, or cancel one.

It is optional, and it can be edited or cleared at any time — nothing references
it.

### The icon

Every step a catalog contributes wears the same mark — on the node, in the
palette, and on the catalog's page under **Apps**. The **Icon** field on the
admin form picks where it comes from, and there are three answers.

**Taken from the service** (the default). When you save, Dazyflow looks for the
service's **favicon**: the base URL's own host first, then the domain one label
up, since an API host (`api.example.com`) usually serves no site while
`example.com` does. It prefers the artwork a page declares over
`/favicon.ico`, largest first — the latter is often a 16×16 that renders as four
grey pixels.

It is a guess, and it is allowed to fail: a service with no favicon keeps the
plain globe and nothing else changes. Pressing **Save** is how you retry, so a
site that was down the first time gets another chance, and changing the address
looks again. A catalog that already has an icon keeps it, so ordinary edits cost
nothing.

**An image you choose.** Pick a file and it wins over the guess, permanently:
the favicon is never consulted again for this catalog, not even when you change
its address. Use this when the service publishes no mark, or when the guess
borrowed the wrong one — a shared platform's logo, say, from that domain one
label up.

The mark is stored inside Dazyflow rather than linked, so a large file is not
just wasteful, it is copied onto every step in the catalog. Oversized images are
shrunk in your browser before they are sent, so in practice you can pick
whatever you have; a very large SVG is refused instead, because there is nothing
in an SVG to shrink.

**No icon.** The plain globe, on purpose — and, unlike a guess that found
nothing, it stays that way. This is the answer when the guess is wrong and you
have nothing of your own to upload.

> The icon shown next to a catalog's name in Admin → Web APIs is the one your
> flows will use. If a guess landed somewhere odd, that is where you will see it.

Catalogs saved before this shipped have no icon until their next save.

---

## Describing an operation

Each operation needs four things and benefits from a fifth.

**A step id.** Short, lowercase, stable: `get_order`, `create_order`. It is the
last part of the step id your flows store, so **renaming it later breaks the
flows that use it** — treat it the way you would a column name in a database.

**A method and a path.** The path is joined onto the address. A variable part
goes in braces:

```
/orders/{order_id}
```

Every `{name}` in the path must have a **required** argument of the same name,
placed *in the path*. Dazyflow refuses the save otherwise and names the
placeholder — a path with a brace left in it would send the call somewhere nobody
meant.

**Arguments, and where each one goes.** This is the part that makes it a step
rather than a web request. For each one: a name, whether it travels in the path,
the query string, a header or the body, its type, and whether it is required.

**A one-line summary** — *"Fetch one order"*. It is what you see on the step and
what the flow assistant searches. An operation without one is a step nobody can
find by describing what it does.

### Bodies

`POST`, `PUT` and `PATCH` can carry a body, and there are two ways to give them
one:

- **JSON built from the arguments.** Mark arguments as *in the body* and
  Dazyflow assembles the object, converting each value to the type you declared —
  so a quantity wired from an upstream step arrives as the number `2`, not the
  text `"2"`.
- **Whatever is wired into the Body pin.** For a payload no argument list
  describes: a nested object, an array, XML, something a previous step rendered.

`GET`, `HEAD` and `DELETE` cannot carry one. Proxies drop a GET body, so the call
would succeed having sent nothing — better refused than mysterious.

---

## What counts as a failure

By default any `2xx` is a success and anything else fails the step, with the
service's own error text in the run log.

When a non-2xx is a legitimate answer — a `404` meaning "no such order" that your
flow wants to branch on rather than fail — list it under **Accepted status
codes** on the step. The status still comes out on its own pin either way.

Retries follow HTTP: `GET`, `HEAD`, `PUT` and `DELETE` are safe to repeat, so a
retry edge on one of those validates. A `POST` or `PATCH` is not, so a retry edge
there is refused rather than silently sending the order twice. Dazyflow attaches
an `Idempotency-Key` to those two anyway, so a service that honours the
convention can dedupe a retry whose response was lost.

---

## Turning one off, and removing it

**Available in flows** is a switch: turn it off and the steps leave the palette
while everything you described stays. It is the reversible half of removing.

Removing is not reversible in the way that matters: flows referencing
`api:<name>:<operation>` stay valid flows, but their steps stop resolving and a
run fails at that step.

The page says so before it removes anything, and it says it with the actual
numbers: it scans your org's flows and names the ones that will stop working,
calling out any that are published and running now. When nothing uses the
catalog, it says that instead — which is the common case for one added by
mistake. A flow whose steps you cannot see is counted but not named.

---

## Importing from an OpenAPI spec

If your service publishes an OpenAPI document — `/openapi.json`, `/openapi.yaml`,
whatever FastAPI, Spring, NestJS or go-swagger generated for you — you do not
have to describe the operations by hand.

Open **Add a web API**, give it the spec's address or paste the document, and
press **Read the spec**. You get a list of every operation it describes; tick the
ones you want and press **Use N operations**, which fills in the form below.
Nothing is saved until you press **Save**, as usual.

**Picking is the point.** A vendor's spec can run to several hundred operations,
and every one you import is a step in your palette and a line in what the flow
builder reads when it looks for something to use. A catalog holds at most 60. For
your own service that limit will never come up; for someone else's, take the
handful you actually call. Selecting by tag is there for exactly that.

Some things will not import, and the panel says which and why rather than
failing the whole document:

- **Swagger 2.0 is refused.** It is a different format, not an older version of
  this one, and reading it as if it were OpenAPI 3 would produce operations that
  look right and send the wrong request. Convert it — most tools can — and import
  the result.
- **References to other documents are not followed.** A `$ref` pointing at
  another URL would mean Dazyflow fetching an address out of a file, which is not
  something it will do. References within the same document work normally.
- **An operation Dazyflow cannot express is skipped**, with a note saying which
  and why, and the rest import. A cookie parameter, or an argument named the same
  as something a step already uses, are the usual causes.
- **A relative server address** (`/v1`) cannot stand alone, so you type the full
  address yourself. Same for one with `{variables}` in it.

A body that is a JSON object becomes one input per field. Anything else — an
upload, a CSV, a bare array — becomes a single **Request body** input you connect
whatever you like into.

### Refreshing after the spec changes

Open the catalog for editing and press **Read the spec** again. Instead of a
plain list you get a comparison: how many operations are new, changed, unchanged
and **removed**.

Removals are the ones to read. An operation that has disappeared from the spec —
because someone deleted a handler — takes its step with it, and any flow using
that step stops working. So Dazyflow will not apply a removal until you tick the
box confirming it, and it names each step id so you can search your flows first.
Everything else applies without ceremony: a refresh you have not confirmed only
ever adds and updates.

## Reaching a service inside your own network

Dazyflow refuses to dial private addresses, deliberately, so by default a
described API has to be one the internet can reach. If yours is not — an orders
service on `10.0.0.x`, something only your office network can see — you do not
have to fall back to writing a script per operation.

Set **Reach it through a runner** on the catalog to one or more
[runner](./runners) tags, and every operation in it is performed *from* that
machine instead of from Dazyflow. Nothing else about the catalog changes: the
same steps, the same fields, the same connection, the same outputs. Only where
the call is made from.

A machine carrying **all** of the tags you list runs the request. A machine's
own name is one of its tags, so pinning a catalog to one box is a single tag —
`orders-box`. If several machines match, any of them may take it, which is fine:
an HTTP request carries no session.

Two things to know before you switch it on.

- **These calls skip the outbound checks.** The private-address block is the
  point, so it goes — but so do the allowed-hosts list and the rate limit, none
  of which exist on your own machine. The response size limit still applies. In
  practice this is the same trust you already extend to that runner, which can
  run any command you send it; but it is a real widening, and it is why only an
  admin can set the field.
- **The runner needs Python.** The request runs as a short script under the
  agent's own interpreter, which is there by definition — the agent is a Python
  program. The exception is a runner installed with `--allow` restricting what
  it will run: add `python` to that list, or the step will report that the
  machine refused to run it.

## When to use what

| You have | Use |
| --- | --- |
| Your own service with a REST API | **Web APIs** — this page |
| One call to make, once, in one flow | **Web request** |
| A tool that already speaks MCP | [MCP servers](./mcp-servers) |
| Code, a local tool, something on your network | [Runners](./runners) |
