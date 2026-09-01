---
title: Teams & approvals
sidebar_label: Teams & approvals
---

# Teams & approvals

Two things tend to arrive together once a flow is doing real work: you want
someone else in the workspace, and you want a person to check certain things
before they go out. This page covers both — inviting people, what each role can
do, and how to park a flow until someone says yes.

---

## Invite someone

People live under **Admin → People** (the account menu, bottom of the sidebar).
It's the page that lists who is in your organization, and it's where invitations
are made and revoked.

Press **Invite a teammate**, type their email, pick a role, and press **Create
invite**. You get back a link.

**Sending the link is up to you.** Dazyflow emails it when the server has a
mailer configured, but the invitation always shows its link on the People page
with a **Copy link** button — paste it into chat, an email you write yourself,
anywhere. Nothing about the invitation depends on our email reaching them.

The person opens the link, signs in or creates an account, and presses **Join
the organization**. They're in, with the role you picked.

> Invitations expire after **14 days**. Until one is accepted it sits under
> **Pending invitations**, where you can **Revoke** it — useful when you typo an
> address, or someone leaves before they ever joined.

An invitation is tied to the address you typed. If they sign in with a different
one, Dazyflow refuses it and says so, rather than quietly seating the wrong
person.

### If your own email isn't confirmed yet

New accounts get a banner asking them to confirm their address. Until you do,
Dazyflow won't send email *on your behalf* — so an invitation you create is
still created, and still gives you a link to send, but we won't email it for
you. Confirm your address and that goes away.

---

## What each role can do

You pick one when you invite someone, and you can change it later from the
dropdown next to their name on the People page.

| Role | Can do |
| --- | --- |
| **Viewer** | Open and run flows, read runs and collections, **approve and reject**. Can't edit anything or see secrets. |
| **Editor** | Everything a Viewer can, plus build and edit flows and manage secrets. Can't invite people. |
| **Admin** | Everything an Editor can, plus invite people and manage the organization. |

The person whose account created the organization is its **Owner**. They have
Admin rights over it and can't be removed — someone has to be able to let people
back in.

> **Viewer is the right role for someone who only signs things off.** Approving
> doesn't need edit access, so an approver never has to be able to change the
> flow they're approving, or read the credentials it uses. Give out Editor when
> someone needs to *build*, not when they need to *decide*.

---

## Seats

Your plan includes a number of people — see **Plan and usage** in the account
menu for yours, and what you've used.

Outstanding invitations count toward it. That's deliberate: an invitation is a
seat you've promised, so Dazyflow tells you the org is full when you create the
invitation, rather than letting the person you invited discover it when they try
to join. Revoking an invitation, or removing a member, frees the seat again.

---

## Pause a flow until a person says yes

Some steps shouldn't happen without a human glance — a reply to a customer, a
refund, a testimonial going on your website. Add a **Wait for approval** step
between the thing that decides and the thing that acts.

Add it with **Add step** (Ctrl/Cmd+K), search *approval*. Then wire it up:

1. **Connect what's being decided into its `Value` input.** This is the part
   worth getting right — whatever you connect here is what the approver is shown
   on their card. Wire in the form submission, the drafted reply, the order.
   Connect nothing and they see only which step is waiting, which is a hard
   thing to say yes to.
2. **Write a prompt** on the step: the question you want asked. *"Send this
   reply?"*, *"Refund this order?"*
3. **Connect the `Approved` output to whatever should happen next.** Everything
   downstream of `Approved` waits; nothing runs until someone approves. Use the
   `Rejected` output if a "no" should do something of its own — tell the
   requester, log it — and leave it unconnected if a no should simply stop.

The step also gives you an **Approver** and a **Comment** output, so a later
step can record who decided and what they said.

### Telling someone it's waiting

Anyone in the workspace will see it on the Approvals page next time they look.
To reach them sooner, you have two options:

- Fill in **Email these people** on the step. Dazyflow mails them the approval
  link when the flow arrives here, and mails them the outcome once it's decided.
  Leave it blank and no mail is sent.
- Or use the **Approval link** output yourself. Put the step *before* a notify
  step — Slack, ntfy, a text message — and connect the link into that message.

> **Anyone holding the approval link can decide.** The link is the permission;
> there's no per-person targeting on it. Send it only to the people who should
> be deciding. The flow records who clicked on the `Approver` output, so you can
> always see who it was afterwards.

### What the approver sees

Anything waiting shows up on the **Approvals** page, which appears in the
sidebar once you have something pending. Each card shows the question, the value
you connected, which flow it came from and how long it's been waiting, with
**Approve** and **Reject** and a box for an optional comment that travels with
the decision.

The flow resumes the moment someone decides. Until then the run sits at that
step — it isn't failing and it isn't burning anything; it's waiting.

Beneath the waiting cards, **Recent decisions** lists what the workspace has
already settled: the question, the value, who decided, which way, when, and the
comment they left. A card leaves the inbox the instant someone acts on it, so
this is where you look to find out whether a request was already handled, or
what was decided the last time a similar one came round. Each row links to its
run, which holds the full detail. It's a record — nothing there can be decided
again.

Requests that were never decided show up here too. **Cancel a run** while it
sits at an approval and the request is called off: it leaves the inbox, and
appears in the list marked *Cancelled* with the reason, naming nobody — because
nobody decided it. Without that it would simply vanish, which reads like
somebody handled it.

> A run parked at an approval keeps whatever it had already worked out. That's
> why the approver can be a different person, hours later, on a phone.

---

## Where next

- [Forms & webhooks](./forms-and-webhooks.md) — the intake side: a form anyone can
  fill in, feeding the flow you're approving.
- [When a run fails](./when-a-flow-fails.md) — reading a run that stopped for a
  reason other than waiting on you.
- [Step catalog: flow control](https://docs.dazyflow.app/reference/steps/flow-control) — the *Wait for
  approval* step's inputs and outputs in detail.
- [Glossary](./glossary.md) — approval, member, role, seat and the rest.
