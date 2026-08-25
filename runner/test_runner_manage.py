#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Joachim Klahr
# SPDX-License-Identifier: AGPL-3.0-or-later
"""Tests for runner.sh in its MANAGE life — the copy the operator keeps.

Setup runs once. The same file manages the runner from then on, so its
verbs are a long-lived interface: `install`, `start`, `stop`, `restart`,
`status`, `logs`, `uninstall`. Losing or breaking one is a support problem
months later, in a file nobody is looking at.

Two behaviours matter more than the rest:

  It refuses to install a service for a machine that is not registered. Such a
  service would crash-loop against a server that has never heard of it.

  Uninstalling says out loud that the machine is still registered. Stopping an
  agent is not revoking it, and assuming otherwise leaves a credential live on
  a decommissioned box.
"""

import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
SCRIPT = HERE / "runner.sh"
SH = shutil.which("sh") or "/bin/sh"

REAL_TOOLS = ["mkdir", "chmod", "id", "cat", "rm", "mv", "sed", "tail", "dirname",
              "python3", "sh", "cut", "sha256sum"]

STUB_SYSTEMCTL = """#!/bin/sh
echo "$*" >> "$SYSTEMCTL_CALLS"
case "$*" in
*show-environment*) exit ${SYSTEMCTL_BUS_EXIT:-0} ;;
*daemon-reload*)    exit ${SYSTEMCTL_RELOAD_EXIT:-0} ;;
*restart*)          exit ${SYSTEMCTL_RESTART_EXIT:-0} ;;
*enable*)           exit ${SYSTEMCTL_ENABLE_EXIT:-0} ;;
*status*)           exit ${SYSTEMCTL_STATUS_EXIT:-0} ;;
esac
exit 0
"""

STUB_LOGINCTL = """#!/bin/sh
echo "${STUB_LINGER:-no}"
"""

STUB_JOURNALCTL = """#!/bin/sh
echo "$*" >> "$JOURNALCTL_CALLS"
"""


class ManageHarness(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        self.home = self.tmp / "home"
        self.home.mkdir()
        self.bin = self.tmp / "bin"
        self.bin.mkdir()
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

        # The runner directory, as setup would leave it: runner.sh next to the
        # agent. runner.sh finds everything relative to itself.
        self.rundir = self.home / ".dazyflow"
        self.rundir.mkdir()
        shutil.copy(SCRIPT, self.rundir / "runner.sh")
        (self.rundir / "runner.sh").chmod(0o755)
        (self.rundir / "dzrunner.py").write_text("# agent\n")
        self.svc = self.rundir / "runner.sh"

        self.systemctl_calls = self.tmp / "systemctl_calls"
        self.journalctl_calls = self.tmp / "journalctl_calls"
        self.env = {
            "HOME": str(self.home),
            "PATH": str(self.bin),
            "SYSTEMCTL_CALLS": str(self.systemctl_calls),
            "JOURNALCTL_CALLS": str(self.journalctl_calls),
        }
        self.stub("systemctl", STUB_SYSTEMCTL)
        self.stub("loginctl", STUB_LOGINCTL)
        self.stub("journalctl", STUB_JOURNALCTL)

    def stub(self, name, body):
        p = self.bin / name
        p.write_text(body)
        p.chmod(0o755)

    def unstub(self, name):
        (self.bin / name).unlink()

    def registered(self, yes=True):
        d = self.home / ".config/dazyflow"
        d.mkdir(parents=True, exist_ok=True)
        p = d / "runner.json"
        if yes:
            p.write_text(json.dumps(
                {"url": "https://dzd.example.com", "name": "box", "credential": "dzrc_x"}))
        elif p.exists():
            p.unlink()

    def env_file(self, **kv):
        lines = ["# test"] + [f"{k}={v}" for k, v in kv.items()]
        (self.rundir / "runner.env").write_text("\n".join(lines) + "\n")

    def run_svc(self, *args, **envextra):
        env = dict(self.env)
        env.update({k: str(v) for k, v in envextra.items()})
        return subprocess.run([SH, str(self.svc), *args],
                              env=env, capture_output=True, text=True, timeout=60)

    @property
    def unit(self):
        return self.home / ".config/systemd/user/dazyflow-runner.service"

    def calls(self, path):
        return path.read_text() if path.exists() else ""


class TestInstall(ManageHarness):
    def test_installs_and_starts(self):
        self.registered()
        r = self.run_svc("install")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertTrue(self.unit.exists())
        sc = self.calls(self.systemctl_calls)
        self.assertIn("daemon-reload", sc)
        # enable, and then RESTART. `enable --now` starts nothing on a unit
        # that is already active, so re-running install — the documented way to
        # tighten the allow-list — left the old, looser list running while the
        # script printed "Started."
        self.assertIn("enable dazyflow-runner", sc)
        self.assertIn("restart dazyflow-runner", sc)

    def test_refuses_a_machine_that_is_not_registered(self):
        # A service for an unregistered machine crash-loops against a server
        # that has never heard of it — every ten seconds, in a log nobody reads.
        self.registered(False)
        r = self.run_svc("install")
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("not registered", r.stderr)
        self.assertFalse(self.unit.exists())
        self.assertNotIn("enable", self.calls(self.systemctl_calls))

    def test_refuses_when_the_agent_is_missing(self):
        self.registered()
        (self.rundir / "dzrunner.py").unlink()
        r = self.run_svc("install")
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("no agent at", r.stderr)
        self.assertFalse(self.unit.exists())

    def test_the_unit_is_wired_for_boot_and_restart(self):
        self.registered()
        r = self.run_svc("install")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        unit = self.unit.read_text()
        # Without WantedBy, `enable` links nothing and the service starts once
        # and never comes back after a reboot.
        self.assertIn("WantedBy=default.target", unit)
        self.assertIn("Restart=always", unit)
        self.assertIn("dzrunner.py", unit)

    def test_the_unit_holds_no_secret(self):
        self.registered()
        self.env_file(DAZYFLOW_URL="https://dzd.example.com")
        r = self.run_svc("install")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        unit = self.unit.read_text()
        self.assertNotIn("--token", unit)
        self.assertNotIn("dzrt_", unit)
        self.assertNotIn("dzrc_", unit)


class TestSettingsFromRunnerEnv(ManageHarness):
    """runner.env carries what the saved credential cannot: what the agent is
    allowed to run. Dropping it would silently unrestrict the runner."""

    def test_an_allow_list_reaches_the_unit(self):
        self.registered()
        self.env_file(DAZYFLOW_ALLOW="./safe.sh")
        r = self.run_svc("install")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn('--allow "./safe.sh"', self.unit.read_text())

    def test_a_value_with_spaces_is_not_mangled(self):
        # Read, not sourced: `DAZYFLOW_ALLOW=./my script.sh` is a literal value,
        # not a broken shell assignment.
        self.registered()
        self.env_file(DAZYFLOW_ALLOW="./my script.sh")
        r = self.run_svc("install")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn('--allow "./my script.sh"', self.unit.read_text())

    def test_a_value_is_never_executed(self):
        # The file is not code. If it were sourced, this would run.
        self.registered()
        marker = self.tmp / "pwned"
        (self.rundir / "runner.env").write_text(
            f"DAZYFLOW_ALLOW=$(touch {marker})\n")
        r = self.run_svc("install")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertFalse(marker.exists(), "runner.env was evaluated as shell code")

    def test_no_env_file_is_fine(self):
        self.registered()
        r = self.run_svc("install")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertNotIn("--allow", self.unit.read_text())


class TestLifecycleVerbs(ManageHarness):
    def install(self):
        self.registered()
        r = self.run_svc("install")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.systemctl_calls.unlink(missing_ok=True)

    def test_start_stop_restart_reach_systemd(self):
        self.install()
        for verb, expected in [("start", "start dazyflow-runner"),
                               ("stop", "stop dazyflow-runner"),
                               ("restart", "restart dazyflow-runner")]:
            self.systemctl_calls.unlink(missing_ok=True)
            r = self.run_svc(verb)
            self.assertEqual(r.returncode, 0, f"{verb}: {r.stdout}{r.stderr}")
            self.assertIn(expected, self.calls(self.systemctl_calls))

    def test_status_passes_the_exit_code_through(self):
        # So it is usable in a check, not just for reading.
        self.install()
        r = self.run_svc("status", SYSTEMCTL_STATUS_EXIT=3)
        self.assertEqual(r.returncode, 3)
        self.assertIn("status dazyflow-runner", self.calls(self.systemctl_calls))

    def test_logs_follows_the_journal(self):
        self.install()
        r = self.run_svc("logs")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("dazyflow-runner", self.calls(self.journalctl_calls))

    def test_verbs_refuse_when_nothing_is_installed(self):
        # "Failed to start unit not found" from systemd is a worse answer than
        # naming the thing that has not happened yet.
        for verb in ["start", "stop", "restart", "status", "logs"]:
            r = self.run_svc(verb)
            self.assertNotEqual(r.returncode, 0, verb)
            self.assertIn("not installed", r.stderr, verb)


class TestUninstall(ManageHarness):
    def test_removes_the_unit(self):
        self.registered()
        self.assertEqual(self.run_svc("install").returncode, 0)
        r = self.run_svc("uninstall")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertFalse(self.unit.exists())
        self.assertIn("disable --now dazyflow-runner", self.calls(self.systemctl_calls))

    def test_says_the_machine_is_still_registered(self):
        # The surprising half. Stopping the agent is not revoking it: the
        # credential still works until someone removes it in Dazyflow.
        self.registered()
        self.run_svc("install")
        r = self.run_svc("uninstall")
        self.assertIn("still registered", r.stdout)
        self.assertIn("Admin", r.stdout)

    def test_uninstalling_nothing_is_not_an_error(self):
        r = self.run_svc("uninstall")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("nothing to remove", r.stdout)

    def test_remove_is_accepted_as_an_alias(self):
        self.registered()
        self.run_svc("install")
        r = self.run_svc("remove")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertFalse(self.unit.exists())


class TestItFindsItselfNotAFixedPath(ManageHarness):
    """A verb must work on the runner beside THIS copy of the script, not on
    whatever happens to live in ~/.dazyflow.

    Otherwise two runner directories on one machine would both drive the first
    one, and `--dir` would produce an install that cannot be managed."""

    def test_a_runner_outside_the_default_directory_is_managed(self):
        other = self.home / "elsewhere" / "runner-two"
        other.mkdir(parents=True)
        shutil.copy(SCRIPT, other / "runner.sh")
        (other / "runner.sh").chmod(0o755)
        (other / "dzrunner.py").write_text("# agent\n")
        (other / "runner.env").write_text("DAZYFLOW_ALLOW=./only-here.sh\n")
        # Nothing in the default location at all.
        shutil.rmtree(self.rundir)
        self.registered()

        r = subprocess.run([SH, str(other / "runner.sh"), "install"],
                           env=self.env, capture_output=True, text=True, timeout=60)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        unit = self.unit.read_text()
        # It used the agent and the settings next to itself.
        self.assertIn(str(other / "dzrunner.py"), unit)
        self.assertIn('--allow "./only-here.sh"', unit)
        self.assertNotIn(str(self.rundir), unit)


class TestNoSystemd(ManageHarness):
    def test_says_how_to_run_it_anyway(self):
        self.registered()
        self.unstub("systemctl")
        r = self.run_svc("install")
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("systemctl was not found", r.stderr)
        # Not a dead end: the agent still runs by hand.
        self.assertIn("python3", r.stderr)

    def test_no_user_bus_names_the_fix(self):
        self.registered()
        r = self.run_svc("install", SYSTEMCTL_BUS_EXIT=1)
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("user service manager", r.stderr)
        self.assertIn("enable-linger", r.stderr)


class TestUsage(ManageHarness):
    def test_no_argument_lists_the_verbs(self):
        r = self.run_svc()
        self.assertEqual(r.returncode, 0, r.stderr)
        for verb in ["install", "start", "stop", "status", "logs", "uninstall"]:
            self.assertIn(verb, r.stdout)

    def test_an_unknown_verb_is_an_error_with_the_list(self):
        # A mistyped verb falls through to setup, so the message has to name the
        # right mistake: they gave a command, not an option.
        r = self.run_svc("frobnicate")
        self.assertEqual(r.returncode, 2)
        self.assertIn("unknown command frobnicate", r.stderr)
        self.assertIn("install", r.stderr)

    def test_an_unknown_flag_is_reported_as_a_flag(self):
        r = self.run_svc("--frobnicate")
        self.assertEqual(r.returncode, 2)
        self.assertIn("unknown option --frobnicate", r.stderr)

    def test_it_says_it_needs_no_root(self):
        # People arriving from a CI runner expect `sudo ./runner.sh install`.
        r = self.run_svc("--help")
        self.assertIn("no root", r.stdout.lower())


class TestLinger(ManageHarness):
    def test_advises_when_linger_is_off(self):
        self.registered()
        r = self.run_svc("install", STUB_LINGER="no")
        self.assertIn("sudo loginctl enable-linger", r.stdout)

    def test_quiet_when_linger_is_on(self):
        self.registered()
        r = self.run_svc("install", STUB_LINGER="yes")
        self.assertNotIn("sudo loginctl enable-linger", r.stdout)
        self.assertIn("start at boot", r.stdout)


if __name__ == "__main__":
    unittest.main()


class TestInstallRestarts(ManageHarness):
    """Re-running install is the documented way to TIGHTEN the allow-list.

    docs/guide/runners.md: "edit ~/.dazyflow/runner.env and run ./runner.sh
    install again". `enable --now` starts nothing on a unit that is already
    active — systemd re-reads the file, but the running process keeps its
    original ExecStart argv — so the operator got "Started." while the old,
    looser list was still in force until the next reboot.
    """

    def test_a_second_install_restarts_rather_than_only_enabling(self):
        self.registered()
        self.assertEqual(self.run_svc("install").returncode, 0)
        self.systemctl_calls.write_text("")  # only look at the second run

        r = self.run_svc("install")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        sc = self.calls(self.systemctl_calls)
        self.assertIn("restart dazyflow-runner", sc)
        self.assertIn("in force now", r.stdout)

    def test_a_restart_that_fails_does_not_claim_to_have_started(self):
        self.registered()
        r = self.run_svc("install", SYSTEMCTL_RESTART_EXIT="1")
        self.assertNotEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertNotIn("Started.", r.stdout)
