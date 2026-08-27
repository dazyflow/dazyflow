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

This is the part that saves the most work. A described API gets its own page
under **Apps**, exactly like Gmail or Stripe: you fill in the address and the
credential **once**, and every step of every flow uses them without holding
either. Rotating a key is one edit, not one per step.

That is also why the auth question on the admin page only asks for the *shape* —
bearer token, or a header the service names. The value itself is entered on the
Apps page after you save.

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
run fails at that step. The page says so before it removes anything.

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
