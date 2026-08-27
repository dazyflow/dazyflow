---
title: Web APIs — steps for your own service sidebar_label: Web APIs
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

## What this is not (yet)

- **It does not import an OpenAPI spec.** If your service publishes
  `/openapi.json` — most modern frameworks generate one — that is the obvious
  next step and it is designed but not built. Today you describe the operations
  you actually want, which for an in-house service is usually a handful.
- **It cannot reach a service inside your own network.** Dazyflow refuses to dial
  private addresses, deliberately, and a described API is no exception. For a
  service with no public address, use a [runner](./runners) and a script.

## When to use what

| You have | Use |
| --- | --- |
| Your own service with a REST API | **Web APIs** — this page |
| One call to make, once, in one flow | **Web request** |
| A tool that already speaks MCP | [MCP servers](./mcp-servers) |
| Code, a local tool, something on your network | [Runners](./runners) |
