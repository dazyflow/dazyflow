---
title: Runners — run scripts on your own machines sidebar_label: Runners
---

# Runners — run scripts on your own machines

A **runner** is a machine you own with a small agent on it. Add one and your
flows can run scripts there with the **Run on your machine** step.

Setting one up is one command. Nothing needs to reach the machine, so it works
on a server behind a firewall, on a laptop, or in a container on a network
Dazyflow has never heard of.

This is how you use a tool, a library, or a system that no built-in step covers:
write a script, and let a flow call it.

> If the thing you want already speaks **MCP**, there is a shorter route: add it
> in [Admin → MCP servers](./mcp-servers) and its tools become steps directly,
> with no machine to set up and no script to write. A runner is for what has no
> such interface — your own code, a local tool, something on your network.

---

## Set one up

**Admin → Runners → Add a runner.** You get a command:

```sh
curl -fsSL https://your-dazyflow-server/runner.sh | sh -s -- --token dzrt_... --service
```

Run it on the machine. That's the whole setup — the runner appears in the list
within a few seconds, and keeps running after a reboot.

The command downloads **one Python file** — the agent — plus a copy of
`runner.sh` itself, registers the machine, and installs a service. It installs
no packages, adds no repositories, and opens no ports. Python 3 is the only
requirement, and it is already on most systems.

The token lasts 30 minutes and works once. Add another runner to get a fresh
one.

### If you'd rather not pipe a script into a shell

Reasonable. Both files are deliberately readable, so read them first — starting
with `runner.sh`, since that is the one that runs:

```sh
curl -fsSL https://your-dazyflow-server/runner.sh -o runner.sh
curl -fsSL https://your-dazyflow-server/dzrunner.py -o dzrunner.py
less runner.sh dzrunner.py
sh runner.sh --token dzrt_... --service
```

`runner.sh` is POSIX shell; the agent is standard-library Python. That is the
reason neither is a compiled binary: you are about to let them run commands on
your machine, so you should be able to see what they do. Both are served as
plain text, so they also open in a browser.

The copy you downloaded already knows your server's address — it was filled in
when the server handed it over — so `--url` is only needed if you got the file
some other way.

### Keeping it running

The command above already includes `--service`, so this is done: it installs a
systemd **user** service and starts it. No root, and the agent runs as you
rather than as a privileged account nobody meant to hand a shell to. There is
nothing to run afterwards — `daemon-reload` and `enable` happen for you.

Drop `--service` if you would rather run it in the foreground and watch it. You
can install the service later with `./runner.sh install`.

One thing may be left, and the script tells you if so:

```sh
sudo loginctl enable-linger $(id -un)
```

That is the difference between starting **when you log in** and starting **at
boot**. On a server nobody logs into, it is the difference between a runner that
works and one that never appears. It is the only step needing `sudo`, which is
why it is yours to run — and if it is already done, the script says so instead
of telling you to do it again.

### What survives what

| | Comes back? |
|---|---|
| You close the terminal (no `--service`) | No — it was running in it |
| The agent crashes (`--service`) | Yes, within ten seconds |
| The machine reboots (`--service` + linger) | Yes, at boot |
| The machine reboots (`--service`, no linger) | When you next log in |
| Dazyflow restarts | Yes — nothing on your machine notices |

A step that was mid-script when the machine went down **fails**; it is not
re-run. See [Use it in a flow](#use-it-in-a-flow).

Two smaller things worth knowing. The service file holds **no token** — the
installer registers first and the agent keeps its own credential, so there is no
secret in it to leak or go stale. And `--service` on a machine without systemd
stops before registering, so the token is still good for another try.

### Managing it afterwards

`runner.sh` copied itself into `~/.dazyflow` on the way past. That copy is how
the runner is managed from then on — same file, now taking a verb:

```sh
cd ~/.dazyflow

./runner.sh status      # is it running?
./runner.sh start
./runner.sh stop
./runner.sh restart
./runner.sh logs        # follow what it is doing
./runner.sh install     # install the service (if you set up without --service)
./runner.sh uninstall   # stop it and remove the service
```

There is no second script to find and no systemd incantation to remember. To
change what the runner is allowed to run, edit `~/.dazyflow/runner.env` and run
`./runner.sh install` again — that rewrites the unit and restarts the agent, so
the new list is in force straight away. The agent finishes whatever it is
running first; stopping or restarting never kills a command halfway through.

> **Coming from a self-hosted CI runner?** Same shape, two differences. There is
> no `sudo ./runner.sh install` — this is a systemd **user** service, so it
> needs no root, and the agent runs as you, which is also the account whose
> files a flow's script can reach. Run it as a user you are willing to give
> that to.
>
> And `./runner.sh uninstall` stops the agent; it does **not** unregister the
> machine. Its credential keeps working until you remove it in
> **Admin → Runners**, and that is the step that actually revokes it.

---

## Use it in a flow

Add the **Run on your machine** step and fill in:

- **Where to run it** — tags the machine must carry, ticked from the tags your
machines actually have. **All of them must match**, and the field says how many
machines qualify and how many of those are switched on.
- **Run it with** — what starts the script: the machine's own shell, `sh`,
`bash`, Python, PowerShell or Node.
- **Script** — what to run, in a proper box with the syntax coloured for the
language you chose. It runs as the user the agent runs as, in the agent's
working directory.

There is no separate "which machine" field, because **a machine's name is always
one of its tags**. So one tag does either job:

| Tags on the step | Where it runs |
| --- | --- |
| `invoices-box` | that one machine, and nowhere else |
| `build` | whichever machine tagged `build` is free |
| `linux` `gpu` | a machine that is **both** — not either |

The last row is the one to read twice. Tags narrow; they do not widen. Two tags
mean fewer machines qualify, not more, and a pair no single machine carries
fails the step rather than picking the nearest thing.

### How one machine gets chosen

Nothing chooses. The step queues the job and every machine asks for work every
five seconds, so the one that runs it is **whichever tagged machine asks first**.

Which means a machine that is switched off is never sent anything — it simply
never asks. There is no "assigned to a machine that turned out to be down"
state, and none of the work needs re-routing when a machine goes away.

Two consequences worth knowing:

- **It is not load balancing.** Each agent runs one job at a time, so a busy
machine stops asking until it is done — which spreads work as a side effect. But
nothing prefers the idle machine, the fast one or the nearest one.
- **A busy pool is a queue.** If every machine with your tags is working, the
step waits (up to its own timeout), rather than starting somewhere it does not
belong.

The tag field leads with the tags that have a machine switched on, and says how
many machines carry the set you have chosen and how many of those are running —
so a step aimed at a sleeping pool is visible while you are writing it.

Whatever you wire into the step's input arrives on the script's **standard
input**. Whatever the script prints comes back out, ready for the next step — an
email, a spreadsheet, a database.

### Choosing what runs the script

The default, **the machine's own shell**, is `/bin/sh` on a unix box and `cmd`
on Windows — what a runner has always done, and what an existing step keeps
doing.

Choose anything else and the agent writes the script to a temporary file and
starts that interpreter with it. So a Python step is Python all the way down:

```python
import json, sys

order = json.load(sys.stdin)
print(order["total"] * 1.25)
```

Two things follow from the script being a file rather than something piped in.
**Standard input stays yours** — the value wired into the step reaches the
script, exactly as it does for a shell one. And the interpreter has a real
filename to talk about, so a mistake comes back as `line 4 of
dzrunner-x8f2.py` instead of `<string>`.

The interpreter has to be on the machine, and the agent says so plainly if it
is not: *"this runner was asked to run the script with node, which is not
installed on this machine"*. `python` means `python3` from the agent's `PATH`,
and PowerShell means `pwsh` if it is there and `powershell` otherwise.

**Upgrade the agent before using this.** An agent installed before this release
does not know about the choice and will use the machine's shell whatever the
step says — which for a Python script means a pile of shell syntax errors. Re-run
the install command on the machine to upgrade it; **Admin → Runners** shows which
version each one is running.

### Passing values in — and credentials

**Environment variables** on the step become environment variables for the
script: `$API_TOKEN` in a shell, `os.environ["API_TOKEN"]` in Python. They are
merged over the machine's own environment, so `PATH` and everything else the
agent has still works, and a name you set here wins.

For anything sensitive, reference a stored secret rather than typing the value:

| Name | Value |
| --- | --- |
| `API_TOKEN` | `${secret.BILLING_TOKEN}` |
| `MONTH` | `03` |

The reference is what gets saved in the flow. The **value** is substituted on the
way out, one step before the script starts, and it is kept out of every place
you would not want to find it:

- **The flow definition** stores `${secret.BILLING_TOKEN}`, not the token — so
it is not in the workspace's git history and not visible to anyone reading the
flow. (Type a literal credential instead and the save-time lint says so.)
- **The queued task** is encrypted at rest under your organisation's key, because
that row is the one place the substituted value has to sit for a moment.
- **The run's output** is scrubbed: a script that prints `$API_TOKEN`, on purpose
or in a stack trace, shows `[redacted:secret]` in the run record and in the live
log.
- **A support bundle** keeps the names and drops the values.

What no amount of this can do is protect the value once it is on the machine —
that is the point of sending it. The script has it, anything the script runs has
it, and a script that writes it to a file has written it to a file. A runner is
as trusted as the people who can edit your flows; a secret you send one is as
trusted as the machine.

A name cannot be empty, contain `=`, or contain control characters — an
environment block cannot carry those, and the step refuses before anything is
queued rather than letting the script fail on the machine for a reason that
looks unrelated.

### Building the script in an earlier step

The script does not have to be typed on the step. Connect the **script** input
and an earlier step supplies it — filled in from a template, read out of a
table, written by the AI step. Wired, it wins over the box; unwired, the box is
what runs.

It is a separate input from the value on **in** on purpose. One port carrying
either the program or its data would make "what did this run?" depend on which
upstream step happened to be connected.

A non-zero exit fails the step, and the script's own error output is attached,
so a failing flow tells you what your script said rather than just a number.

### Letting the flow handle the exit code

Sometimes a non-zero exit is not a breakage. `2` might mean "nothing to invoice
today", and you want the flow to take a quiet path rather than fail.

Set **If the script exits non-zero** to *Carry on — the flow checks the exit
code*. The step then succeeds whatever the script returned, and hands you three
outputs to route on:

| Output | Carries |
| --- | --- |
| **Output** | standard output, as always |
| **Exit code** | the number the script returned, as text — `"0"` is success |
| **Error output** | standard error |

All three are emitted either way, on a success as well as a handled failure, so
switching the setting cannot change what your wires carry — and a script that
succeeded with warnings on stderr still hands them over.

**What this deliberately does not cover.** It applies only to a script that RAN
and returned a code. A machine that is switched off, an agent that refused the
script, a script the runner had to stop at the timeout — those still fail the
step, because there is no exit code to give you, and inventing one would send
the flow down the "the script ran and said no" path when nothing ran at all. To
react to *those*, put an error handler on the wire out of the step: right-click
the connection and pick **Only if this step fails**. That branch stays idle on
every run that goes well, and runs when the step could not.

**A script is never run twice.** If the machine goes down while your script is
running, the step fails and says which machine went quiet — it is not handed to
another runner, and it is not retried when the machine comes back. Dazyflow
cannot know how far your script got, and re-running one that had already sent
the invoices would be worse than failing. Retrying is your call, not ours.

On the canvas the step carries the name of the machine it will run on, under its
title. Wiring a secret into a step is the moment to know it is leaving the
server, and the palette is long gone by then.

### Tags

Tags let a pool of machines share work. Register three build servers with
`--tags linux,build` and a step targeting `build` runs on whichever is free.

The agent tags itself with its operating system and architecture by default, so
`linux` and `arm64` work without anyone setting anything. And every machine
carries its own name, which is what lets a step name one machine without a
separate field for it.

**Retagging takes no visit to the machine.** In **Admin → Runners**, click the
machine: its settings page lists the tags it carries, and you add and remove
them there. Registration is not involved, the credential does not change, and
the agent never learns about it — a tag is how *this* Dazyflow routes work, not
something the machine knows about itself.

Tags are stored lower-cased, trimmed and de-duplicated, however they were typed,
because that is what a step has to spell to match one. So a tag added as `Build `
appears as `build` the moment it saves — which is the spelling to put on the
step. Two things are refused:

- **A comma.** `--tags a,b` splits on it, so a tag containing one could never
come from a machine, and would read as two tags everywhere it is shown.
- **Another machine's name.** Names are tags, so tagging machine B with machine
A's name would make one tag mean two machines — and a step written to pin work
to A would quietly start landing on B.

Changing a tag reroutes every step that targets it, which is why it needs
`organization:admin` (or an API key with `module:register`) — the same authority
as adding the machine in the first place — and why it is recorded in the audit
log.

> **`--labels` still works.** It is the older name for `--tags` and every install
> command written down so far uses it, so the agent and the installer accept
> both. The stored field is still called `labels` in the API for the same reason:
> renaming a synonym is not worth a migration.

### One limit worth knowing

A runner's input takes a **value, not a file**. Wiring a file in is refused
before the job is sent.

A file in Dazyflow is a path on the *server's* disk, and your machine is
somewhere else — sending the path would fail inside your script with a
missing-file error you'd reasonably read as your bug. If you need a script to
work on a file's contents, read it into a value first and wire that.

---

## Who can do what

**Adding or removing a runner** needs `organization:admin`, or an API key
carrying `module:register`.

**Using one in a flow** needs `graph:edit` — the same as any other step.

Read those two together, because the consequence is the point:

> A runner runs whatever command a flow sends it. Anyone who can edit a flow in
> your organisation can therefore run commands on these machines, as the user
> the agent runs as.

This is the same bargain a self-hosted CI runner makes, and it is not a defect —
it is what makes a runner useful. But it means **a runner is as trusted as the
people who can edit your flows**, and that is worth deciding deliberately rather
than discovering.

### Limiting what a runner will run

Start the agent with `--allow`:

```sh
python3 dzrunner.py --allow ./fetch-invoices.sh,./reconcile.sh
```

It will then refuse anything else, and the flow gets a clear failure saying so.

**An allow-list has to name an interpreter too.** A step that chooses Python
starts `python`, not the first word of the script, so an allow-list of
`./fetch-invoices.sh` refuses it — otherwise a runner permitted one shell script
would quietly be permitted every Python program ever written. Add `python`
(or `bash`, `node`, `powershell`) to the list if a flow should be able to run
that language here, and be clear about what that grants: permission to run
anything that language can do.

**An allow-list also turns the shell off.** Without one, whatever the flow sends
goes to a shell, because that is what the step promises. With one, the command
is parsed by the agent and the program is executed directly — so `;`, `|`, `&&`,
backticks and `$(...)` are ordinary characters in an argument rather than
operators. That is what makes the list mean anything: checking only the first
word and then handing the whole string to a shell would let
`./fetch-invoices.sh ; anything-else` through.

The practical consequence: if a command needs a pipe or a redirect, put it in a
script and allow the script.

Be clear-eyed about what this still does not buy. It restricts which **program**
is invoked, not what that program may then do. Allowing a shell — or a script
that shells out, or an interpreter that takes arbitrary code — allows
everything. It is a guard against a flow running the wrong thing, not a
sandbox.

If you need a real boundary, run the agent as an unprivileged user, in a
container, or on a machine you are willing to lose.

---

## When something is wrong

**The runner never appeared.** The command was probably never pasted, or it
failed. Run it without the pipe and read the output. A token that has expired or
already been used says so plainly.

**It says "Last seen" and a time in the past.** The agent stopped. Check it with
`~/.dazyflow/runner.sh status`, and read why with `./runner.sh logs`. A machine
that is asleep or shut down looks exactly like this.

**"This runner is no longer registered."** It was removed in Dazyflow, so its
credential no longer identifies anything. Add a runner again and re-run the
command.

**A step fails with "not allowed to run".** The agent has an `--allow` list and
the command isn't on it. If the command looks allowed, check for a pipe, a
redirect or a `;`: an allow-list runs commands without a shell, so those are
arguments rather than operators. Put them in a script and allow that. If the
step chose an interpreter under **Run it with**, the list has to name the
interpreter — `python`, not the script.

**A Python or PowerShell script fails with shell syntax errors.** The agent on
that machine predates the **Run it with** choice, so it ran the script through
the machine's shell. Re-run the install command there to upgrade it;
**Admin → Runners** shows each machine's agent version.

**A step fails saying the command printed too much.** The agent sends back at
most 1 MiB per stream. Have the script write large output to a file and print
the path, or print less.

**A step fails saying "no machine carries the tag …".** Nothing in this
organisation has that tag. Check the spelling against the machine's settings page
(tags are lower-case), or add it there.

**A step fails saying "no single machine carries all of …".** Each tag exists,
but no one machine has the whole set — the usual cause is a second tag that
narrowed the step to nothing. Every tag has to match.

**A step fails saying no machine tagged something "has not checked in
recently".** The machines with those tags are registered but their agents aren't
running. Start one with `~/.dazyflow/runner.sh start`, or see why with
`./runner.sh status`. The step
fails rather than waiting, so a run never hangs on a machine that is switched
off.

Switching that machine on later will **not** run the script. Work a step has
given up on is closed, not left in a queue — so starting an agent after a
weekend away never sets off a backlog of scripts whose runs are long finished.

**A step fails saying the runner "stopped responding".** The agent took the work
and then went away — the machine slept, rebooted, lost power, or the agent was
killed. The script was **not** re-run. Check whether it had already done its
work before deciding to run the flow again.

**A step fails but the script works when you run it by hand.** The agent runs
as its own user, from its own directory, with its own environment. A relative
path, a missing tool on `PATH`, or an environment variable you have and it
doesn't are the usual causes.

---

## What a runner never gets

- **Any other organisation's work.** A runner claims tasks for the
organisation whose token registered it, and the server cannot hand it anything
else.
- **A way into Dazyflow.** The agent asks for work and reports results. It
holds no session, can read no flows, and can enumerate nothing.
- **The server's secrets.** It receives exactly what the step's parameters
carry, and nothing more.

Restarting Dazyflow changes none of this. Registrations and queued work live in
the database, so your machines stay registered and their agents keep working
without anyone touching them.

And removing a runner revokes its credential immediately — a decommissioned
machine stops being able to claim work, whether or not anyone remembers to stop
the agent.
