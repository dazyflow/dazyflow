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
import signal
import socket
import stat
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
from pathlib import Path

VERSION = "0.1.0"

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
    except urllib.error.URLError as e:
        # A server that is down or restarting is normal operational weather,
        # not a crash: say what happened and let the caller retry.
        raise HTTPError(0, f"could not reach {url}: {e.reason}") from e


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
    """Write the credential 0600.

    It is a long-lived secret and this often runs on a machine with other
    users on it, so the mode is set explicitly rather than left to umask.
    """
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(cfg, indent=2) + "\n")
    path.chmod(stat.S_IRUSR | stat.S_IWUSR)


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


def permitted(script, allowed):
    """Enforce the local allow-list, returning None or a refusal message.

    It checks the PROGRAM — the first word — not the whole command, so an
    allowed script can still take arguments.

    Be clear-eyed about what that buys: it restricts what gets INVOKED, not
    what that program may then do. Allowing a shell, or a program that shells
    out, allows everything. It is a guard against a flow running the wrong
    thing, not a sandbox.
    """
    if not allowed:
        return None
    parts = script.split()
    if not parts:
        return "this runner was sent an empty command"
    if parts[0] in allowed:
        return None
    return (
        f'this runner is not allowed to run "{parts[0]}" '
        f"(permitted: {', '.join(allowed)})"
    )


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
        """Run one task and capture what it produced."""
        refusal = permitted(task.get("script", ""), self.allowed)
        if refusal:
            # Refused locally, and reported as the agent's decision rather than
            # a script failure — the flow author needs to know it never ran.
            return {"error": refusal}

        timeout = int(task.get("timeout_seconds") or 0) or DEFAULT_TIMEOUT_SECONDS
        env = dict(os.environ)
        # Not scrubbed: unlike a plugin inside the daemon, this process is the
        # org's own machine and its environment is theirs to arrange.
        env.update(task.get("env") or {})

        stop_beat = self.heartbeat(task["id"])
        try:
            proc = subprocess.run(
                task["script"],
                shell=True,
                input=task.get("stdin") or "",
                capture_output=True,
                text=True,
                timeout=timeout,
                env=env,
            )
        except subprocess.TimeoutExpired as e:
            return {
                "stdout": _text(e.stdout),
                "stderr": _text(e.stderr),
                "error": f"the command was still running after {timeout}s and was stopped",
            }
        except OSError as e:
            # Could not start at all — a missing interpreter, a permission
            # problem. Distinct from a command that ran and failed.
            return {"error": f"could not run the command: {e}"}
        finally:
            stop_beat()

        return {
            "exit_code": proc.returncode,
            "stdout": proc.stdout,
            "stderr": proc.stderr,
        }

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
        post(self.url(f"/api/v1/runner/tasks/{task_id}/result"), result, self.cfg["credential"])

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
                self._sleep(POLL_SECONDS)
                continue

            log(f"task {task['id']}: {task.get('script', '')}")
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


def main(argv=None):
    p = argparse.ArgumentParser(
        prog="dzrunner",
        description="Run Dazyflow steps on this machine.",
    )
    p.add_argument("--url", default="", help="Dazyflow server URL, e.g. https://dazyflow.example.com")
    p.add_argument("--token", default="", help="registration token from Admin → Runners (first run only)")
    p.add_argument("--name", default=default_name(), help="name for this machine, as it appears in Dazyflow")
    p.add_argument("--labels", default=default_labels(), help="comma-separated labels a flow can target instead of the name")
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
    args = p.parse_args(argv)

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
