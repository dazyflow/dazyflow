#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Joachim Klahr
# SPDX-License-Identifier: AGPL-3.0-or-later
"""Tests for runner.sh in its SETUP life — the one file every organisation runs.

Setting up a runner is a single pasted command, so the installer IS the
onboarding experience. These tests care about two things above all:

  Nothing spends the registration token unless it can finish. A token works
  once and lasts thirty minutes, so failing halfway costs the operator a trip
  back to the admin page for another one.

  The token never reaches a file. The agent keeps its own credential, so the
  service needs no secret.

The environment is a sandbox: PATH holds only stubs plus the handful of real
tools the script needs, so `systemctl` can be made absent, broken, or fine
without touching this machine.
"""

import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
SCRIPT = HERE / "runner.sh"

# The interpreter is found outside the sandbox on purpose: PATH below is
# restricted to control what the SCRIPT can find, not how it is started.
SH = shutil.which("sh") or "/bin/sh"

# The external commands runner.sh legitimately uses. Everything else it needs
# is a shell builtin.
# `sh` is on the list because runner.sh is re-run as a child during setup, the
# same way it invokes the agent through python3 rather than relying on a
# shebang.
REAL_TOOLS = ["mkdir", "chmod", "id", "cat", "python3", "env", "rm", "sh", "sed", "tail", "dirname"]

# A stand-in for the agent, delivered by the stub downloader. It records how it
# was invoked and exits however the test asks.
FAKE_AGENT = """#!/usr/bin/env python3
import json, os, sys
args = sys.argv[1:]
with open(os.environ["AGENT_CALLS"], "a") as fh:
    fh.write(" ".join(args) + "\\n")
rc = int(os.environ.get("AGENT_EXIT", "0"))
if rc == 0 and "--register-only" in args:
    # Registering saves the credential, which is what runner.sh looks for before
    # it will install a service.
    base = os.environ.get("XDG_CONFIG_HOME") or os.path.join(os.environ["HOME"], ".config")
    d = os.path.join(base, "dazyflow")
    os.makedirs(d, exist_ok=True)
    with open(os.path.join(d, "runner.json"), "w") as fh:
        json.dump({"url": "https://dzd.example.com", "name": "box",
                   "credential": "dzrc_x"}, fh)
sys.exit(rc)
"""

STUB_CURL = """#!/bin/sh
# Serves local files instead of fetching. runner.sh is the REAL one, so a setup
# that goes on to install a service exercises the actual code path.
dest=""
url=""
while [ $# -gt 0 ]; do
    case "$1" in
    -o) dest="$2"; shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
    esac
done
[ -n "$dest" ] || exit 1
case "$url" in
*runner.sh) cat "$REAL_SCRIPT" > "$dest" ;;
*) cat "$FAKE_AGENT_SRC" > "$dest" ;;
esac
"""

STUB_SYSTEMCTL = """#!/bin/sh
echo "$*" >> "$SYSTEMCTL_CALLS"
case "$*" in
*show-environment*) exit ${SYSTEMCTL_BUS_EXIT:-0} ;;
*daemon-reload*)    exit ${SYSTEMCTL_RELOAD_EXIT:-0} ;;
*enable*)           exit ${SYSTEMCTL_ENABLE_EXIT:-0} ;;
esac
exit 0
"""

STUB_LOGINCTL = """#!/bin/sh
echo "$*" >> "$LOGINCTL_CALLS"
echo "${STUB_LINGER:-no}"
"""


class SetupHarness(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        self.home = self.tmp / "home"
        self.home.mkdir()
        self.bin = self.tmp / "bin"
        self.bin.mkdir()

        # Only the real tools the script needs, so PATH is fully controlled and
        # a missing systemctl really is missing.
        for tool in REAL_TOOLS:
            found = shutil.which(tool)
            # An error, not a skip. Every name here is a POSIX tool that must
            # exist; its absence means the environment is broken, and skipping
            # would let CI pass green having tested nothing at all. Failing also
            # beats the alternative of carrying on, where it surfaces as
            # "command not found" from inside the script under test and reads
            # like a bug in the script.
            if not found:
                raise RuntimeError(
                    f"this environment has no {tool!r}; the sandbox PATH cannot be built")
            os.symlink(found, self.bin / tool)

        (self.tmp / "fake_agent.py").write_text(FAKE_AGENT)
        self.agent_calls = self.tmp / "agent_calls"
        self.systemctl_calls = self.tmp / "systemctl_calls"
        self.loginctl_calls = self.tmp / "loginctl_calls"

        self.env = {
            "HOME": str(self.home),
            "PATH": str(self.bin),
            "FAKE_AGENT_SRC": str(self.tmp / "fake_agent.py"),
            "REAL_SCRIPT": str(HERE / "runner.sh"),
            
            "AGENT_CALLS": str(self.agent_calls),
            "SYSTEMCTL_CALLS": str(self.systemctl_calls),
            "LOGINCTL_CALLS": str(self.loginctl_calls),
            "DAZYFLOW_URL": "https://dzd.example.com",
        }
        self.stub("curl", STUB_CURL)
        self.stub("systemctl", STUB_SYSTEMCTL)
        self.stub("loginctl", STUB_LOGINCTL)

    def stub(self, name, body):
        p = self.bin / name
        p.write_text(body)
        p.chmod(0o755)

    def unstub(self, name):
        (self.bin / name).unlink()

    def run_install(self, *args, **envextra):
        env = dict(self.env)
        env.update({k: str(v) for k, v in envextra.items()})
        return subprocess.run(
            [SH, str(SCRIPT), *args],
            env=env, capture_output=True, text=True, timeout=60,
        )

    # helpers ------------------------------------------------------------
    @property
    def unit(self):
        return self.home / ".config/systemd/user/dazyflow-runner.service"

    def calls(self, path):
        return path.read_text() if path.exists() else ""


class TestServiceInstall(SetupHarness):
    def test_installs_registers_and_starts(self):
        r = self.run_install("--token", "dzrt_good", "--service")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertTrue(self.unit.exists(), "no unit file was written")

        # It registered while the operator was watching.
        self.assertIn("--register-only", self.calls(self.agent_calls))

        # And it did the two steps that used to be homework.
        sc = self.calls(self.systemctl_calls)
        self.assertIn("daemon-reload", sc)
        self.assertIn("enable --now dazyflow-runner", sc)

    def test_the_unit_carries_no_token(self):
        r = self.run_install("--token", "dzrt_secret", "--service")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        unit = self.unit.read_text()
        # The whole reason registration happens first: a service that starts
        # from a saved credential needs no secret in a world-readable file.
        self.assertNotIn("dzrt_secret", unit)
        self.assertNotIn("--token", unit)
        self.assertIn("ExecStart=", unit)
        self.assertIn("dzrunner.py", unit)
        self.assertIn("Restart=always", unit)

    def test_the_unit_is_wired_to_start_at_boot(self):
        # The entire point of --service. Without [Install]/WantedBy there is
        # nothing for `enable` to link, so the service installs, starts once,
        # and silently never comes back after a reboot.
        r = self.run_install("--token", "t", "--service")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        unit = self.unit.read_text()
        self.assertIn("[Install]", unit)
        self.assertIn("WantedBy=default.target", unit)
        # And Restart, which covers the agent dying rather than the machine.
        self.assertIn("Restart=always", unit)
        self.assertIn("enable --now dazyflow-runner", self.calls(self.systemctl_calls))

    def test_an_allow_list_reaches_the_service(self):
        # --allow is the one setting the saved credential does not carry, so
        # losing it here would silently unrestrict the runner on every reboot.
        r = self.run_install("--token", "t", "--service", "--allow", "./safe.sh")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn('--allow "./safe.sh"', self.unit.read_text())

    def test_paths_and_values_with_spaces_survive_the_unit(self):
        # systemd splits ExecStart on whitespace, so an unquoted value becomes
        # extra arguments and the service fails to start complaining about
        # something unrelated.
        d = self.home / "my runner dir"
        r = self.run_install("--token", "t", "--service",
                             "--dir", str(d), "--allow", "./my script.sh")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        unit = self.unit.read_text()
        self.assertIn('"./my script.sh"', unit)
        self.assertIn('my runner dir', unit)
        # The agent path is quoted as one token, not split at the space.
        exec_line = [l for l in unit.splitlines() if l.startswith("ExecStart=")][0]
        self.assertIn('"%s"' % (d / "dzrunner.py"), exec_line)

    def test_no_allow_list_means_no_allow_flag(self):
        r = self.run_install("--token", "t", "--service")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertNotIn("--allow", self.unit.read_text())


class TestRefusesBeforeSpendingTheToken(SetupHarness):
    """The token is single-use. Every reason --service cannot work has to be
    found before anything spends it."""

    def assert_token_unspent(self, r):
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("token has not been used", r.stderr)
        self.assertEqual(self.calls(self.agent_calls), "",
                         "the agent ran despite the pre-flight failing")
        self.assertFalse((self.home / ".dazyflow/dzrunner.py").exists(),
                         "the agent was downloaded despite the pre-flight failing")
        self.assertFalse(self.unit.exists())

    def test_no_systemd_at_all(self):
        self.unstub("systemctl")
        r = self.run_install("--token", "t", "--service")
        self.assert_token_unspent(r)
        self.assertIn("systemctl was not found", r.stderr)

    def test_no_user_bus(self):
        # The `su` / container / non-lingering-SSH case.
        r = self.run_install("--token", "t", "--service", SYSTEMCTL_BUS_EXIT=1)
        self.assert_token_unspent(r)
        self.assertIn("user service manager", r.stderr)
        # And it names the fix, since this one is recoverable.
        self.assertIn("enable-linger", r.stderr)

    def test_a_foreground_install_still_records_the_allow_list(self):
        # Otherwise someone who starts the agent by hand with --allow and later
        # runs `runner.sh install` gets a service with NO restriction — a silent
        # downgrade caused only by changing how it starts.
        r = self.run_install("--token", "t", "--allow", "./safe.sh")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        env = (self.home / ".dazyflow/runner.env").read_text()
        self.assertIn("DAZYFLOW_ALLOW=./safe.sh", env)
        # And no secret in it.
        self.assertNotIn("dzrt_", env)
        self.assertNotIn("--token", env)

    def test_it_leaves_a_copy_of_itself_even_without_service(self):
        # It is the file they keep; deciding later must not need this installer.
        r = self.run_install("--token", "t")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertTrue((self.home / ".dazyflow/runner.sh").exists())
        self.assertIn("runner.sh install", r.stdout)

    def test_without_service_no_systemd_is_needed(self):
        # Plain installs must not care about systemd at all.
        self.unstub("systemctl")
        r = self.run_install("--token", "t")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("--token t", self.calls(self.agent_calls))
        self.assertFalse(self.unit.exists())


class TestFailuresAfterRegistering(SetupHarness):
    def test_a_bad_token_installs_no_service(self):
        r = self.run_install("--token", "dzrt_bad", "--service", AGENT_EXIT=1)
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("registration failed", r.stderr)
        # Nothing half-installed: no unit, and systemd never touched.
        self.assertFalse(self.unit.exists())
        self.assertNotIn("daemon-reload", self.calls(self.systemctl_calls))

    def test_a_service_that_will_not_start_says_where_to_look(self):
        r = self.run_install("--token", "t", "--service", SYSTEMCTL_ENABLE_EXIT=1)
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("would not start", r.stderr)
        # Points at the operator's own commands, not a systemd incantation they
        # would have to remember — that is what the verbs are for.
        self.assertIn("runner.sh status", r.stderr)
        self.assertIn("runner.sh logs", r.stderr)
        # The machine IS registered, and the operator is told so — otherwise
        # they would sensibly try to register it again and burn another token.
        self.assertIn("registered", r.stderr)
        self.assertTrue(self.unit.exists())

    def test_a_failed_reload_does_not_pretend_to_have_started(self):
        r = self.run_install("--token", "t", "--service", SYSTEMCTL_RELOAD_EXIT=1)
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("reload", r.stderr)
        self.assertNotIn("enable --now", self.calls(self.systemctl_calls))


class TestLinger(SetupHarness):
    def test_tells_you_when_the_last_step_is_missing(self):
        r = self.run_install("--token", "t", "--service", STUB_LINGER="no")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("sudo loginctl enable-linger", r.stdout)
        # And says why, because "run this" without a reason gets skipped.
        self.assertIn("boot", r.stdout)

    def test_stays_quiet_when_it_is_already_done(self):
        r = self.run_install("--token", "t", "--service", STUB_LINGER="yes")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertNotIn("sudo loginctl enable-linger", r.stdout)
        self.assertIn("start at boot", r.stdout)

    def test_no_loginctl_is_not_fatal(self):
        self.unstub("loginctl")
        r = self.run_install("--token", "t", "--service")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        # Cannot tell, so it advises rather than staying silent.
        self.assertIn("enable-linger", r.stdout)


class TestArgumentHandling(SetupHarness):
    def test_a_missing_token_explains_where_to_get_one(self):
        r = self.run_install("--service")
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("--token is required", r.stderr)
        self.assertIn("30 minutes", r.stderr)

    def test_name_and_labels_reach_registration(self):
        r = self.run_install("--token", "t", "--service",
                             "--name", "invoices-box", "--labels", "linux,build")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        calls = self.calls(self.agent_calls)
        self.assertIn("--name invoices-box", calls)
        self.assertIn("--labels linux,build", calls)

    def test_help_needs_no_token(self):
        r = self.run_install("--help")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("--service", r.stdout)


if __name__ == "__main__":
    unittest.main()
