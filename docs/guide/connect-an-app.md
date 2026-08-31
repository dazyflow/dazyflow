---
title: Connect an app
sidebar_label: Connect an app
---

# Connect an app

To use an outside service in a flow — Gmail, Slack, Fortnox, Klarna, Stripe —
you connect it **once**. After that any flow in your organization can use it,
and you never paste a password into a flow.

---

## Where connections live

Open **Apps** in the sidebar. Every service Dazyflow can talk to has a page
there, with what it does and what it needs. Click through to the one you want
and connect it from that page.

There are two shapes of connection, and the app's page tells you which one you're
dealing with:

- **Sign in with the service.** Gmail, Google Sheets, Slack, Notion and friends
  send you to their own login screen, ask you to approve access, and send you
  back. You never type that password into Dazyflow.
- **Paste a key.** Fortnox, 46elks, Stripe, ntfy, Claude, your own mail server
  and most business APIs give you a key or token to copy in. The app's page names
  exactly which one it needs, and links to the service's own documentation for
  where to find it.

You can connect **more than one account** to the same app — two Gmail mailboxes,
say. Use **Connect another account**, and mark one as the **Default** so steps
that don't say otherwise use it.

## Your credentials are not in your flows

A connection is stored separately from the flows that use it, encrypted, and it's
never shown inside a flow. A step refers to *which* account to use, not to the
secret itself.

That's what makes a flow safe to duplicate, export, or hand to a colleague: what
travels is the recipe, not the keys.

## When a flow needs something you haven't connected

Press **Run** on a flow that isn't ready and Dazyflow stops you before it does
any damage:

> **Set up this flow first** — This flow uses apps or secrets you haven't set up
> yet. Add them so the run doesn't fail:

It lists exactly what's missing, and the button takes you straight to the place
that fixes it: **Go to Apps** — landing on the one app's own page when a single
app is all that's missing — or **Go to Secrets** when what's missing is a
secret rather than an app connection.

There's also **Run anyway**. The check is a good guess, not a certainty: a step
might get its credentials by a route the editor can't see, so you're never
locked out of your own flow. If you're not sure, connect the app instead — a run
that fails halfway has usually already done half its work.

The same check runs when you publish, because a flow published in this state
will simply fail on its own later, when nobody is watching.

### Slack needs one extra step

Connecting Slack is not enough on its own. The Dazyflow app also has to be a
member of every channel it posts to:

> Open the channel in Slack and type `/invite @Dazyflow`.

Miss this and the run fails with Slack telling you it can't find the channel.
Dazyflow warns you about it before the run when it can see which channels your
flow posts to.

## Secrets: for a key no app page covers

Sometimes a flow needs a credential that isn't an app connection at all — a
database URL, an API token for a service you're calling with a plain HTTP step.
Those go in **Secrets**, under **Admin**, and a flow refers to one by name as
`${secret.NAME}`.

Secrets are available to every flow in your organization. A single flow can
override one with its own value under that flow's **Settings → Secrets** — handy
when one flow talks to a test system and the rest talk to production.

Most people never need this page. If you're connecting Gmail or Slack, use
**Apps**.

If your IT team already runs a secret store — OpenBao/Vault, AWS Secrets
Manager, GCP Secret Manager — flows can resolve secrets against it instead of
storing them in Dazyflow. That's on the same page, under the advanced section.

## If you can't connect it yourself

Two different messages, two different fixes:

- **"You don't have permission to connect apps."** Your account can build flows
  but not hold credentials. Ask an admin in your organization to connect it.
- **"Your administrator needs to enable…"** The app isn't switched on for this
  Dazyflow server at all, so there's nothing for you to click. Whoever set
  Dazyflow up for you has to enable it first (for a self-hosted install, that's
  **Admin → Connector apps**).

The second one also shows in the template gallery: a template you can't use yet
says so on its card, rather than letting you build a flow that can't run.

---

## Where next

- [Make a flow run by itself](./triggers-and-schedules.md) — schedules and publishing.
- [When a run fails](./when-a-flow-fails.md) — including the "not connected" failures.
- [Step catalog](https://docs.dazyflow.app/reference/steps/) — every app and what its steps need.
