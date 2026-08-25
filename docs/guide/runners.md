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
`./runner.sh install` again.

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

- **Runner** — which machine, by name. Or leave it empty and set a **label**
instead, and whichever labelled machine is free takes the job.
- **Command** — what to run. It runs as the user the agent runs as, in the
agent's working directory.

Whatever you wire into the step's input arrives on the script's **standard
input**. Whatever the script prints comes back out, ready for the next step — an
email, a spreadsheet, a database.

A non-zero exit fails the step, and the script's own error output is attached,
so a failing flow tells you what your script said rather than just a number.

**A script is never run twice.** If the machine goes down while your script is
running, the step fails and says which machine went quiet — it is not handed to
another runner, and it is not retried when the machine comes back. Dazyflow
cannot know how far your script got, and re-running one that had already sent
the invoices would be worse than failing. Retrying is your call, not ours.

On the canvas the step carries the name of the machine it will run on, under its
title. Wiring a secret into a step is the moment to know it is leaving the
server, and the palette is long gone by then.

### Labels

Labels let a pool of machines share work. Register three build servers with
`--labels linux,build` and a step targeting `build` runs on whichever is free.

The agent labels itself with its operating system and architecture by default,
so `linux` and `arm64` work without anyone setting anything.

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

Be clear-eyed about what this buys. It restricts which **program** is invoked,
not what that program may then do. Allowing a shell — or a script that shells
out, or an interpreter that takes arbitrary code — allows everything. It is a
guard against a flow running the wrong thing, not a sandbox.

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
the command isn't on it.

**A step fails saying the runner "has not checked in recently".** The machine is
registered but the agent isn't running on it. Start it with
`~/.dazyflow/runner.sh start`, or see why with `./runner.sh status`. The step
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
