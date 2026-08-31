---
title: Glossary
sidebar_label: Glossary
---

# Glossary

Plain-language definitions of the words you'll meet in Dazyflow and in the [Step
catalog](https://docs.dazyflow.app/reference/steps/). New here? Read [How Dazyflow works](./concepts.md)
first — it ties these together.

### App

An outside service you connect to Dazyflow — Gmail, Klarna, Slack, Fortnox, and
so on. You set one up once on the **Apps** page; then any flow can use it. See
also [Connection](#connection).

### Approval

A pause where a flow waits for a person to say yes or no. You add one with the
[Wait for approval](https://docs.dazyflow.app/reference/steps/flow-control) step and connect the thing
being decided into its `Value` input, so whoever decides can see what they're
deciding on. Anything waiting shows on the **Approvals** page. See
[Teams & approvals](./teams-and-approvals.md).

### Branch

A [step](#step) that sends your flow down one of two paths — "Yes" or "No" —
based on a [yes/no](#yesno) value. Pair it with [Compare](#compare) to decide
which way to go. Like a fork in the road.

### CEL / expression

CEL ("Common Expression Language") is a small, safe formula language — think of
an Excel formula. You use it in a few steps to compute a value from your data,
e.g. `row.price * 1.25` or `row.age >= 18`. You don't need it for most flows.

### Compare

A [step](#step) that checks two values (is A greater than B? does A contain B?)
and gives back a [yes/no](#yesno) answer, usually fed into a [Branch](#branch).

### Connection

Your saved login for an [app](#app) — the sign-in or key it needs. It's stored
separately from your flows and kept hidden, so credentials never appear inside a
flow you might share or export.

### Cron / schedule

A way to say "run this on a timetable" — every day at 09:00, every Monday, every
15 minutes. You pick it with a simple schedule editor; behind the scenes it's
stored as a short code called a *cron expression* (you rarely need to see it).

### Data / data record

Structured information with named fields — like one row of a spreadsheet, or an
object such as `{name: "Ada", email: "ada@shop.se"}`. Many steps pass **data**
between each other. A [list](#list) of data is many rows.

### Draft

A flow that hasn't been [published](#publish) yet. Drafts don't run on their own,
so you can build and test safely. Publish when you're ready to go live.

### E.164

The international standard way to write a phone number, with a country code and
no spaces — e.g. `+46701234567`. The **Phone** step converts local numbers (like
`070-123 45 67`) into this format so SMS steps accept them.

### Flow

Your automation: a set of [steps](#step) connected in order that Dazyflow runs
for you from start to finish. The central thing you build in Dazyflow.

### For each / loop

A [step](#step) that takes a [list](#list) and runs the same actions once for
**every** item in it — e.g. send one email per row. Use it whenever an earlier
step gives you *many* things and the next step handles *one at a time*.

### Header (HTTP)

Extra information attached to a web request that isn't the main content — things
like who sent it or what format it's in. Mostly relevant when connecting to
other systems.

### HMAC

A way to prove a message really came from who it claims to, using a shared secret
key. You'll only meet it when integrating with systems that "sign" their
requests; ignore it otherwise.

### Input

A value a [step](#step) needs to do its job. You can [connect](#wire--connect) an
input from an earlier step's [output](#output), or type it in as a
[setting](#setting).

### Invitation

A link that adds someone to your [organization](#organization) with a
[role](#role) you choose. Made on **Admin → People**, valid for 14 days, and
sendable by hand — it's shown with a **Copy link** button whether or not the
server can email it. An outstanding invitation holds a [seat](#seat).

### JSON

A common text format for structured [data](#data--data-record) — used when
talking to other systems. Dazyflow can read it into rows and write it back out;
you rarely have to write it by hand.

### List

**Many** values at once — e.g. all the rows from a spreadsheet, or every new
email. The catalog writes this as "list of …". To act on each item, use a
[For each](#for-each--loop) loop.

### Member

Someone who belongs to an [organization](#organization). Every member holds a
[role](#role) that decides what they can do, and occupies a [seat](#seat).

### Organization

The account everything belongs to: your flows, your files, your people. Most
people only ever have one — the one created with their account, whose creator is
its **Owner**. An organization can hold several [workspaces](#workspace).

### Output

What a [step](#step) produces when it finishes — the result it hands to the next
step. You [connect](#wire--connect) an output into the next step's
[input](#input).

### Poll / polling

Checking an outside service on a timer to see if something new has happened —
e.g. "look at Fortnox every few minutes for a newly paid invoice." A common way
to start a flow when the service can't notify Dazyflow directly.

### Port

A connection point on a step — the sockets you draw lines between. Inputs are
ports on the way in; outputs are ports on the way out.

### Publish

Turning a [draft](#draft) into a live flow. Once published, its
[trigger](#trigger) is active and the flow runs on its own.

### Regular expression (regex)

A pattern for finding or pulling text out of other text — e.g. "every number" or
"anything that looks like an invoice id". Powerful but fiddly; only needed for
advanced text work.

### Retry

Trying a failed step again. Steps that are **safe to retry** are re-tried
automatically by Dazyflow; steps that **run once** (like sending money) are not,
so nothing happens twice by accident.

### Role

What a [member](#member) is allowed to do — **Viewer** (open, run and approve),
**Editor** (also build, edit and manage secrets) or **Admin** (also invite people
and manage the organization). Set when you invite them, changeable afterwards on
**Admin → People**.

### Row

One record of [data](#data--data-record) — like a single line in a spreadsheet.
A [list](#list) of rows is a table.

### Run

One execution of a [flow](#flow). The **Runs** page lists them and shows what
each step did, so you can check results or find a problem.

### Seat

One person's place in your plan's member allowance. Members hold one, and so do
outstanding [invitations](#invitation) — a promise of a seat is still a seat, so
you find out the org is full when you invite, not when they try to join. Check
yours under **Plan and usage**.

### Secret

A sensitive value — a password, an API key — stored safely and hidden. Flows
refer to a secret by name (e.g. `${secret.MY_KEY}`) instead of containing the
value itself.

### Setting

A value you type directly on a [step](#step) (as opposed to
[connecting](#wire--connect) it from an earlier step). Good for values that don't
change between runs.

### Step

A single instruction in a [flow](#flow) — send an email, look up an order, filter
a list. You connect steps together to build a flow. (Some older text calls these
"nodes".)

### Timezone (IANA)

The named region used to interpret times — e.g. `Europe/Stockholm`. Setting it
makes "every day at 09:00" mean 9am *your* time, not UTC.

### Trigger

The [step](#step) that starts a [flow](#flow) — on a [schedule](#cron--schedule),
when a [webhook](#webhook) arrives, when an [app](#app) event happens, or when
you press **Run**.

### Webhook

A web address Dazyflow gives your flow so other systems (or its built-in form)
can start it by sending it data. Used when something should kick off the flow the
moment it happens.

### Wire / connect

Drawing a line from one step's [output](#output) to the next step's
[input](#input), so the value flows through. The heart of building a flow.

### Workspace

A container for your flows, files, and connections — like a folder for one
project or team. Your account can have more than one.

### Yes/no

A true-or-false value, used to make decisions. [Compare](#compare) produces one;
[Branch](#branch) acts on it.
