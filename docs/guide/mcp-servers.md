---
title: MCP servers — bring your own catalogue of steps sidebar_label: MCP servers
---

# MCP servers — bring your own catalogue of steps

Dazyflow ships a few hundred steps. It will never ship one for every tool your
org uses, and waiting for us to write one is not a plan.

An **MCP server** is somebody else's catalogue of tools, published at a single
web address. Point Dazyflow at one and **every tool it offers becomes a step**
in your palette — wired, validated and run like any other. Nobody writes a
connector.

Add one in **Admin → MCP servers**. You need the address and, usually, a token.

---

## What you get

A server called `github` publishing a `create_issue` tool gives you a step
called `mcp:github:create_issue`. It appears in the catalog under **Apps &
services**, and hands its result to the next step. Search the palette for the
server's name to find everything it brought.

Nothing else about the flow changes. The step branches, loops, retries and
appears in the run log exactly like a built-in one.

### The tool's arguments are ports

Each of the tool's arguments becomes an input you can **wire** — so a title can
come from an earlier step rather than being typed. You can also just fill it in
as a setting, exactly as elsewhere in Dazyflow: type a default, connect one when
you have something better.

Not every argument gets a pin, and the rules are worth knowing:

| The argument is… | Where you set it |
| --- | --- |
| text, a number, or a yes/no | its own input, and a setting |
| an object or a list | a setting only |
| beyond the twelfth | a setting only — required ones are never cut |

Objects and lists stay settings on purpose. A pin per nested field would either
flatten the structure into names the tool never declared, or give you a node
shaped like a schema instead of like a step.

There is also one catch-all input, **`input`**, that takes a whole JSON object
and merges it over the settings. It is the escape hatch for the arguments in the
right-hand column above. If you supply the same argument two ways, the more
specific one wins: a value wired into `title` beats an object that happens to
contain a `title`, which in turn beats what you typed.

## Adding one

**Admin → MCP servers → Add a server.**

| Field | What to put in it |
| --- | --- |
| **Name** | Short, lowercase — `github`, `linear`, `our-warehouse`. It becomes part of every step id. |
| **Endpoint URL** | The server's MCP address, over `https`. |
| **Authentication** | **Bearer token** for most; **custom header** for the vendors that use `X-Api-Key` or similar; **none** for an open one. |
| **Token** | Pasted once. Stored encrypted, and never shown again. |

Press **Save and connect**. Dazyflow talks to the server there and then, reads
its tool list, and tells you what happened — including *"the server refused the
credential"*, which is the one you will actually hit. A server that will not
connect is still saved, so you fix the field that was wrong rather than typing
everything again.

> **The name is permanent.** Your flows reference its steps by it, so renaming
> would quietly break every one of them. The form locks the name once a server
> exists; to use a different one, add a second server.

### Using a token you already keep here

Instead of pasting the token, you can enter `${secret.NAME}` to point at a
secret from **Admin → Secrets**. Rotating it there then rotates it here, and the
credential lives in exactly one place.

## Keeping it working

**Re-read its tools** (the ↻ button) handshakes again and refreshes the list. A
tool list is a snapshot taken when the server connected, so a server that has
gained a tool since needs this — or a wait, since every Dazyflow reconnects
each server periodically anyway.

**Use this server** unticked takes the steps out of the palette and keeps the
server and its token. It is the reversible half of deleting: right for a server
misbehaving in a way you have not diagnosed yet.

**Remove** deletes the registration and the stored token. Flows referencing its
steps stay valid, but a run reaches the missing step and **fails**. That is the
same bargain as deleting a runner, and the page warns before it happens.

### What the status column means

| | What it is telling you |
| --- | --- |
| **Connected** | Handshaken, tools loaded, steps in the palette. |
| **Not connecting** | The last attempt failed. Hover it for the reason. |
| **Connecting…** | Saved and not yet handshaken here — normal for a few seconds. |
| **Turned off** | You unticked *Use this server*. Not a fault. |

If your deployment runs more than one Dazyflow, a server added a moment ago may
read *Connecting…* on one of them for up to thirty seconds. Each one connects
independently, on its own schedule.

---

## What to know before you add one

**A server you add receives what your flows send it.** Its tools run against
your org's data, and the arguments a step passes include anything you wired in.
Add endpoints you trust, the way you would a connector you installed.

**Give each server its own token.** That is what lets you revoke one server
without disturbing the rest — and revocation is on the vendor's side, so a
token you cannot revoke is a server you cannot really remove.

**Your server is yours alone.** Its steps resolve for your org and no other, and
another org cannot see the server exists. This is enforced by how the catalog is
keyed, not by a check somewhere that could be forgotten.

### Two things Dazyflow will not do

**It will not run a program for you.** MCP also has a mode where the server is a
command the host starts — `npx some-mcp-server`. Dazyflow does not offer that
here, because the command would run on the *Dazyflow* machine, as the Dazyflow
user. Anyone who could add a server could then run anything. Operators can still
configure such servers for the whole deployment via `DAZYFLOW_MCP_SERVERS`; an
org admin gets a URL.

If the tool you want is only distributed as a command, run it on a machine of
yours and put it behind a URL — or reach it with a
[runner](./runners), which exists for exactly this.

**It will not reach inside your network.** The address is checked at connection
time against the same rules as any outbound request: no loopback, no private
range, no link-local. A hostname that resolves to an internal address is
refused too. An MCP server on your own network needs to be reachable at a real
address, or fronted by one.

### Files

An MCP step's inputs take **values, not files**. A file in Dazyflow is a path on
the Dazyflow machine, and that means nothing to a server somewhere else — so a
flow that wires a file into one is refused before the step runs, with that as
the reason, rather than the tool failing on a path it cannot see. Read the file
into a value first if the tool wants its contents.

### Retries

MCP does not say whether a tool is safe to run twice, so Dazyflow assumes **it
is not**. Steps from an MCP server are treated as *"runs once"*: a step that
fails is not silently retried, because it might have already sent the email or
moved the money. This is the safe assumption, and it is the same one applied to
any step that does not declare otherwise.

## Limits

Fifty servers per org. Each one is a live connection every Dazyflow keeps up, so
the ceiling is about what the daemon carries rather than about you — nobody
legitimate meets it.
