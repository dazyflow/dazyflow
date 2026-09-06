---
title: Forms
sidebar_label: Forms
---

# Forms

Some flows should start when a *person* tells you something — a customer fills
in a contact form, a colleague files a request, someone leaves feedback. Add a
**Form** step and Dazyflow hosts the page for you. No website, no key, no code:
just a link you can share.

---

## The link

Add a Form step and you immediately get a **Form link** you can share — in an
email, a QR code, a chat message. Anyone who opens it gets a real form; each
submission starts a run of your flow with what they typed.

There is nothing to switch on. Adding the step *is* the opt-in — a form takes no
key, so the only thing standing between the link and a visitor is publishing the
flow.

Open **Customize the form** for the two settings worth changing:

- **Form fields** — the questions, comma-separated. Each field you add here
  becomes something your flow can use, so a *Name* and *Email* field gives your
  later steps a name and an email to work with. Leave it blank and you get
  *name*, *email*, *message*.
- **Form heading** — what the person filling it in sees at the top. Defaults to
  the flow's name.

A field whose name reads like a question rather than a column — *What you like
about us*, *Your feedback* — is drawn as a multi-line box, so there's room to
write a paragraph. *Email* and *Phone* get the matching keyboard on a phone.

Because you declared the fields, the steps after the Form step know what's
coming before the first submission ever arrives. That's why a *Save to
spreadsheet* step can offer you your form's columns straight away, instead of
waiting for real data to learn them.

> **Anyone with the link can submit.** There's no sign-in — that's the point of
> a public form. Treat the link as public, and don't put a step behind it that
> you wouldn't want a stranger triggering.

Two things guard it for you, with nothing to configure. The form carries a
hidden field a person never sees: an automated script that fills in every input
it finds completes that one too, and the submission is dropped without starting
a run. And submissions are rate-limited per caller, so nobody can hammer the
form fast enough to fill your collection — or burn through your monthly runs.

## Put it on your own website

Under **Put this form on my own website** you'll find a snippet to paste into
your site's HTML, wherever you want the form to appear.

The form stays hosted by Dazyflow — you're embedding it, not rebuilding it — so
no key or secret ends up in your web page's source.

## Is it actually open?

The step tells you, in one line:

- **Not open yet** — the link returns *"not available"* until you publish the
  flow.
- **Open** — anyone with the link can fill it in and start this flow.
- **Open, but** — visitors still get the last published version, because your
  draft has moved on since. Publish to make your latest changes live.

Two things to remember beyond that line:

- **Publish.** An unpublished draft doesn't receive. Test with **Send test
  event**, then publish. See [Make a flow run by
  itself](./triggers-and-schedules.md).
- **A paused step refuses submissions** rather than accepting them and doing
  nothing. If people report the link not working, check whether the flow or the
  Form step is paused.

## Say something back

By default a submitter sees a simple *Thanks!* — the flow then runs behind them.

Add a [**Reply** step](./request-and-reply.md) and you decide what they see
instead: a booking reference, a summary of what they sent, a next step. Same
step that answers an API caller, and on a form it becomes the confirmation page.

## Test it with a realistic payload

Under **Test run with sample input** you can edit a payload shaped to your
declared fields and fire it through. The run uses your **current draft**, so
this is how you check a change before publishing it.

It's the honest test: the same path a real submission takes, with data you
control.

---

## Where next

- [Webhooks](./webhooks.md) — the same idea when the sender is a system rather
  than a person.
- [Request & reply](./request-and-reply.md) — answering a caller that's asking
  your flow a question.
- [Make a flow run by itself](./triggers-and-schedules.md) — publishing, pausing and schedules.
- [Teams & approvals](./teams-and-approvals.md) — have a person check a submission before the flow acts on it.
