---
title: Runners — your own code as a step
sidebar_label: Runners
---

# Runners — your own code as a step

A **runner** is a small service you host yourself. Register one and the steps
it offers appear in your palette next to the built-in ones. Your code stays in
your binary, on your hardware: nothing is uploaded, compiled, or linked into
Dazyflow.

This is the answer to "I have a library I want to use, and it isn't a
built-in step." Write a runner around it, and a flow can call it like anything
else — then pipe the result into an email, a spreadsheet, or a database.

---

## When you want one

A runner is worth the setup when:

- You have a **library** — in Go, Python, whatever — that does something no
  built-in step does.
- The work has to happen **inside your network**, against a system Dazyflow
  cannot reach.
- You want a step whose behaviour **you** control and can deploy on your own
  schedule.

If all you need is an HTTP call, use the built-in **HTTP request** step first.
A runner earns its keep when you need real code, not just a request.

---

## What you build

Two methods, over gRPC. The service definition lives in
`api/proto/node.proto`; a worked example is in `examples/csv-pipeline`.

```go
// ListManifests declares every step this runner offers.
// Serving one step means returning a list of one.
func (s *server) ListManifests(ctx context.Context, _ *nodepb.ListManifestsRequest) (*nodepb.ListManifestsResponse, error) {
    return &nodepb.ListManifestsResponse{Manifests: []*nodepb.Manifest{{
        Id:      "fetch_invoices",   // what your step is called
        Version: "1.0",
        Label:   "Fetch invoices",
        Inputs:  []*nodepb.Port{{Id: "since", Required: true}},
        Outputs: []*nodepb.Port{{Id: "out"}},

        // How it presents itself. Skip these and your step appears as an
        // unnamed box that palette search cannot find.
        Icon:        "receipt",
        Category:    "network",
        Subtitle:    "from the ledger",
        Summary:     "Fetch invoices issued since a date.",
        Description: "Longer text for the step's inspector panel.",
        Tags:        []string{"invoice", "billing"},
    }}}, nil
}
```

`Category` groups your step in the palette. Use one of `ai`,
`flow_control`, `io`, `logic`, `network`, `system`, `transformation`.

`trigger` is **not** available. A trigger is what a flow *starts* from — the
scheduler polls it, the webhook router dispatches to it — and none of that
machinery reaches a process outside the daemon. A runner declaring it would
give you a flow that looks startable and never fires, so the category is
dropped on registration. Anything else unrecognised is dropped too, leaving
your step ungrouped rather than taking the runner offline over a typo.

```go
// Execute runs one job and streams back progress, then a result.
func (s *server) Execute(job *nodepb.Job, stream nodepb.NodeService_ExecuteServer) error {
    // job.DropId says WHICH of your steps to run — ignorable if you serve one.
    // job.Input["since"].Inline is the value wired into that port.
    // job.Params holds the step's configured parameters.
    //
    // Send Progress events as you go; finish with a Result.
}
```

`ListManifests` is plural even if you only ever serve one step. That is
deliberate: it means you can add a second step later without the daemon and
every existing runner having to be updated together.

---

## Certificates

Both sides prove who they are, and **neither trusts a public certificate
authority**. You supply both halves:

| What | Who holds it | What it does |
| --- | --- | --- |
| Your runner's certificate | You paste it into Dazyflow | The daemon trusts this exact certificate and nothing else |
| A client certificate + key | Dazyflow presents it; you install the certificate on your runner | Lets your runner recognise the daemon |

Pinning one known certificate is stricter than trusting anything a CA has
signed: no certificate authority can be persuaded to issue one for your
hostname and get in.

The address you register must match a name in your runner's certificate — a
DNS name or IP in its subject alternative names. If it doesn't, the connection
is refused, which is the pinning working.

Generating a pair with `openssl` or [`step`](https://smallstep.com/cli/) is
enough. Dazyflow issues nothing and runs no certificate authority.

---

## Registering one

**Admin → Runners → Register a runner.**

Fill in a name, the address, and the three pieces of certificate material, then
press **Test connection** before saving. The test shows two things:

- **Who answered** — the subject of the certificate you pasted.
- **What it offers** — the steps the runner declared.

The second is the useful one. A green tick only tells you something is
listening; the list of steps tells you it is *yours*.

Testing does not save anything, so a failed attempt leaves nothing behind.

### Who is allowed to

Registering a runner requires **organization:admin**, or an API key carrying
**module:register**.

It is deliberately not part of `graph:edit`. A runner receives a step's
parameters with secrets already filled in, so whoever can register one can
receive every credential any flow in your org passes to that step. That is a
larger power than editing a flow, and it is gated separately.

---

## Using it in a flow

Registered steps appear in the palette under your own runners, marked as
running on your hardware. Drag one in and wire it like any other step.

Step ids look like `runner/<runner>/<step>` — for example
`runner/invoices/fetch_invoices`. The prefix is reserved, so a future built-in
can never take over one of your steps.

Your steps are also **marked** wherever they appear: they sort to the top of the
palette under "Your runner", and on the canvas each one carries the runner's
name under its title. That is deliberate — wiring a secret into a step is the
moment to know it is being sent to hardware you run, and by then the palette is
long gone.

Your organisation name is **not** part of the id, so a flow using a runner can
still be copied between workspaces.

### One limit worth knowing

A runner's inputs take **values, not files**. Hovering an input pin says so,
and wiring a file in is refused before the job is sent.

The reason is simple: a file in Dazyflow is a path on the daemon's own disk,
and your runner is somewhere else. Sending the path would fail inside your code
with a missing-file error you would reasonably read as your bug. If you need a
runner to work on a file's contents, read it into a value first and wire that.

---

## When something is wrong

A runner that will not connect stays **listed** and marked unreachable, with
the connection error next to it. It does not vanish — a step that is broken is
much easier to act on than one that has silently disappeared from your palette.

Dazyflow keeps retrying on a backoff. Fixing the runner and re-registering it
retries immediately rather than waiting.

Common causes, in rough order of likelihood:

- **The address doesn't match the certificate.** The name you registered has to
  appear in your runner's certificate.
- **The certificate expired.** The runners list warns before this happens;
  re-register with a new one.
- **The client certificate isn't installed on your runner**, so it rejects the
  daemon.
- **Nothing is listening**, or a firewall is in the way.

If a step disappears from your palette while the runner is connected, the
runner has stopped declaring it — `ListManifests` is the source of truth, and a
step it no longer returns is retired.

---

## What a runner never gets

- Any other organisation's anything. Runners are scoped to the org that
  registered them; the daemon cannot resolve one from another org even by
  mistake.
- A way back into Dazyflow. A runner receives jobs and returns results. It
  holds no token and can enumerate nothing.
- The daemon's own secrets. It receives exactly what the step's parameters
  carry, and nothing else.
