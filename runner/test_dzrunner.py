#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Joachim Klahr
# SPDX-License-Identifier: AGPL-3.0-or-later
"""Tests for the runner agent, against a stub Dazyflow server.

    python3 runner/test_dzrunner.py

A real HTTP server on a loopback port rather than a mocked urllib: the agent's
whole job is to talk to a server over HTTP, and a test that mocks that away
would be testing the parts that were never in doubt. This exercises the actual
requests — the bearer header, the 204-means-nothing-to-do branch, the result
payload — which is where the contract with the daemon actually lives.
"""

import json
import subprocess
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import dzrunner  # noqa: E402


class StubDaemon(BaseHTTPRequestHandler):
    """Enough of the daemon to hold up its end of the four endpoints."""

    # Set by the test before the server starts.
    state = None

    def _read(self):
        n = int(self.headers.get("Content-Length") or 0)
        return json.loads(self.rfile.read(n) or b"{}")

    def _send(self, status, body=None):
        payload = json.dumps(body).encode() if body is not None else b""
        self.send_response(status)
        if payload:
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        if payload:
            self.wfile.write(payload)

    def do_POST(self):  # noqa: N802 — BaseHTTPRequestHandler's naming
        s = self.state
        body = self._read()

        if self.path == "/api/v1/runner/register":
            s["registered"] = body
            if body.get("token") != "dzrt_good":
                self._send(401, {"error": "nope"})
                return
            self._send(200, {"name": body["name"], "credential": "dzrc_secret"})
            return

        # Everything below needs the credential, exactly as the daemon does.
        if self.headers.get("Authorization") != "Bearer dzrc_secret":
            s["unauthorized"] = s.get("unauthorized", 0) + 1
            self._send(401, {"error": "bad credential"})
            return

        if self.path == "/api/v1/runner/claim":
            s["claims"] = s.get("claims", 0) + 1
            if s["queue"]:
                self._send(200, s["queue"].pop(0))
            else:
                self._send(204)
            return

        if self.path.endswith("/progress"):
            s["beats"] = s.get("beats", 0) + 1
            self._send(204)
            return

        if self.path.endswith("/result"):
            s["results"].append(body)
            self._send(204)
            return

        self._send(404, {"error": "no such path"})

    def log_message(self, *_args):
        pass  # keep the test output readable


def serve(state):
    StubDaemon.state = state
    srv = HTTPServer(("127.0.0.1", 0), StubDaemon)
    t = threading.Thread(target=srv.serve_forever, daemon=True)
    t.start()
    return srv, f"http://127.0.0.1:{srv.server_port}"


class AgentTest(unittest.TestCase):
    def setUp(self):
        self.state = {"queue": [], "results": []}
        self.srv, self.url = serve(self.state)
        # shutdown stops serving; server_close releases the socket. Without the
        # second the test run fills with ResourceWarnings.
        self.addCleanup(self.srv.server_close)
        self.addCleanup(self.srv.shutdown)
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.cfg_path = str(Path(self.tmp.name) / "runner.json")

    def register(self, token="dzrt_good", labels=("linux",)):
        return dzrunner.load_or_register(self.cfg_path, self.url, token, "box", list(labels))

    # ---- registration -------------------------------------------------

    def test_registers_and_saves_the_credential(self):
        cfg = self.register()
        self.assertEqual(cfg["credential"], "dzrc_secret")
        self.assertEqual(self.state["registered"]["labels"], ["linux"])
        # The agent reports its version so the admin list can say which one a
        # machine is running.
        self.assertEqual(self.state["registered"]["version"], dzrunner.VERSION)

        saved = json.loads(Path(self.cfg_path).read_text())
        self.assertEqual(saved["credential"], "dzrc_secret")

    def test_credential_file_is_not_world_readable(self):
        self.register()
        mode = Path(self.cfg_path).stat().st_mode & 0o777
        self.assertEqual(mode, 0o600, f"credential saved {oct(mode)}")

    def test_second_run_reuses_the_saved_credential(self):
        self.register()
        before = self.state["registered"]
        # A second start with no token must not re-register: doing so would mint
        # a second identity for one machine and orphan the first.
        cfg = dzrunner.load_or_register(self.cfg_path, self.url, "", "box", [])
        self.assertEqual(cfg["credential"], "dzrc_secret")
        self.assertIs(self.state["registered"], before)

    def test_a_bad_token_says_what_to_do(self):
        with self.assertRaises(SystemExit) as caught:
            self.register(token="dzrt_stale")
        msg = str(caught.exception)
        self.assertIn("not valid", msg)
        # The message has to name the fix, because the cause is invisible: the
        # token looks fine, it is just spent or expired.
        self.assertIn("once", msg)

    def test_register_only_registers_and_stops(self):
        # The installer's whole strategy rests on this: spend the token in the
        # operator's terminal, then hand systemd a service that needs no secret.
        self.state["queue"].append(
            {"id": "t1", "script": "printf nope", "timeout_seconds": 30}
        )
        # On a thread with a deadline: if --register-only ever stopped
        # stopping, main() would fall into the poll loop and this test would
        # hang instead of failing, reporting nothing about what broke.
        out = {}

        def go():
            out["rc"] = dzrunner.main([
                "--url", self.url, "--token", "dzrt_good",
                "--name", "box", "--config", self.cfg_path, "--register-only",
            ])

        t = threading.Thread(target=go, daemon=True)
        t.start()
        t.join(15)
        self.assertFalse(t.is_alive(), "--register-only did not exit; it entered the work loop")
        self.assertEqual(out.get("rc"), 0)
        # Registered...
        saved = json.loads(Path(self.cfg_path).read_text())
        self.assertEqual(saved["credential"], "dzrc_secret")
        # ...and did not touch the queue, which is what "only" means. A task
        # run here would execute before the service that is meant to own it
        # even exists.
        self.assertEqual(self.state["results"], [])

    def test_register_only_reports_a_bad_token_rather_than_exiting_zero(self):
        # If this returned 0 the installer would go on to write and start a
        # service for a machine that is not registered.
        with self.assertRaises(SystemExit):
            dzrunner.main([
                "--url", self.url, "--token", "dzrt_stale",
                "--name", "box", "--config", self.cfg_path, "--register-only",
            ])

    def test_first_run_without_a_token_explains_itself(self):
        with self.assertRaises(SystemExit) as caught:
            dzrunner.load_or_register(self.cfg_path, self.url, "", "box", [])
        self.assertIn("--token", str(caught.exception))

    # ---- the work loop ------------------------------------------------

    def test_runs_a_task_and_reports_the_output(self):
        cfg = self.register()
        self.state["queue"].append(
            {"id": "t1", "script": "printf hello", "timeout_seconds": 30}
        )
        dzrunner.Agent(cfg, []).run(once=True)

        self.assertEqual(len(self.state["results"]), 1)
        res = self.state["results"][0]
        self.assertEqual(res["exit_code"], 0)
        self.assertEqual(res["stdout"], "hello")

    def test_wired_input_arrives_on_stdin(self):
        cfg = self.register()
        self.state["queue"].append({"id": "t1", "script": "cat", "stdin": "from the flow"})
        dzrunner.Agent(cfg, []).run(once=True)
        self.assertEqual(self.state["results"][0]["stdout"], "from the flow")

    def test_a_failing_command_reports_its_exit_code_and_stderr(self):
        cfg = self.register()
        self.state["queue"].append(
            {"id": "t1", "script": "echo trouble >&2; exit 3"}
        )
        dzrunner.Agent(cfg, []).run(once=True)
        res = self.state["results"][0]
        self.assertEqual(res["exit_code"], 3)
        # The command's own stderr is the author's message about what went
        # wrong; losing it would leave them with only a number.
        self.assertIn("trouble", res["stderr"])

    def test_the_environment_carries_task_values(self):
        cfg = self.register()
        self.state["queue"].append(
            {"id": "t1", "script": "printf %s \"$MONTH\"", "env": {"MONTH": "march"}}
        )
        dzrunner.Agent(cfg, []).run(once=True)
        self.assertEqual(self.state["results"][0]["stdout"], "march")

    def test_a_command_that_never_ends_is_stopped(self):
        cfg = self.register()
        self.state["queue"].append({"id": "t1", "script": "sleep 30", "timeout_seconds": 1})
        dzrunner.Agent(cfg, []).run(once=True)
        res = self.state["results"][0]
        self.assertIn("still running", res.get("error", ""))

    def test_a_missing_program_is_a_command_failure_not_a_crash(self):
        cfg = self.register()
        self.state["queue"].append({"id": "t1", "script": "definitely-not-a-real-binary"})
        dzrunner.Agent(cfg, []).run(once=True)
        # The shell reports this as a non-zero exit, which is the right shape:
        # the agent ran what it was asked and the command failed.
        self.assertNotEqual(self.state["results"][0].get("exit_code"), 0)

    def test_an_empty_queue_is_not_an_error(self):
        cfg = self.register()
        agent = dzrunner.Agent(cfg, [])
        self.assertIsNone(agent.claim())
        self.assertEqual(self.state["claims"], 1)
        self.assertEqual(self.state["results"], [])

    def test_a_revoked_runner_is_told_to_re_register(self):
        cfg = self.register()
        cfg["credential"] = "dzrc_revoked"

        # Run it on a thread with a deadline rather than calling it directly.
        # If the agent stops recognising a 401 as terminal it retries forever,
        # and a direct call would HANG the suite instead of failing it — which
        # is worse than a red test, because nobody learns anything from it.
        # Found by mutating the 401 branch away and watching this test hang.
        outcome = {}

        def attempt():
            try:
                dzrunner.Agent(cfg, []).run(once=True)
                outcome["exit"] = None
            except SystemExit as e:
                outcome["exit"] = str(e)

        t = threading.Thread(target=attempt, daemon=True)
        t.start()
        t.join(timeout=15)
        self.assertFalse(
            t.is_alive(),
            "the agent kept polling with a dead credential instead of stopping",
        )
        self.assertIn("no longer registered", outcome.get("exit") or "")

    # ---- the allow-list -----------------------------------------------

    def test_the_allow_list_refuses_anything_else(self):
        cfg = self.register()
        self.state["queue"].append({"id": "t1", "script": "curl http://evil"})
        dzrunner.Agent(cfg, ["./fetch.sh"]).run(once=True)
        res = self.state["results"][0]
        # Refused locally, and reported as the agent's decision — the flow
        # author must know it never ran, not that it ran and failed.
        self.assertIn("not allowed", res["error"])
        self.assertNotIn("exit_code", res)

    def test_the_allow_list_permits_arguments(self):
        cfg = self.register()
        self.state["queue"].append({"id": "t1", "script": "printf %s allowed"})
        dzrunner.Agent(cfg, ["printf"]).run(once=True)
        self.assertEqual(self.state["results"][0]["stdout"], "allowed")

    def test_no_allow_list_permits_everything(self):
        # The default, and the reason the agent warns about it on every start.
        self.assertIsNone(dzrunner.permitted("anything at all", []))

    def test_an_empty_command_is_refused_when_restricted(self):
        self.assertIn("empty", dzrunner.permitted("   ", ["ls"]))

    # ---- names --------------------------------------------------------

    def test_a_hostname_is_made_acceptable_rather_than_rejected(self):
        # A dotted, capitalised hostname is normal and must not fail
        # registration.
        self.assertEqual(dzrunner.sanitize_name("Build-Box.Corp.LOCAL"), "build-box-corp-local")
        self.assertEqual(dzrunner.sanitize_name("---"), "runner")
        self.assertLessEqual(len(dzrunner.sanitize_name("x" * 200)), 64)


class ScriptTest(unittest.TestCase):
    """The file has to work as a script, not only as an import."""

    def test_help_runs(self):
        out = subprocess.run(
            [sys.executable, str(Path(__file__).parent / "dzrunner.py"), "--help"],
            capture_output=True,
            text=True,
            timeout=30,
        )
        self.assertEqual(out.returncode, 0, out.stderr)
        self.assertIn("--token", out.stdout)


if __name__ == "__main__":
    unittest.main(verbosity=2)
