#!/bin/sh
# SPDX-FileCopyrightText: 2026 Angels' Ware
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# The Dazyflow runner: set it up, then manage it. One file, two lives.
#
# FIRST TIME — piped from your Dazyflow server, which is the line the admin
# page gives you:
#
#   curl -fsSL https://dazyflow.example.com/runner.sh | sh -s -- --token dzrt_... --service
#
# The server fills in its own address before serving this, so the token is the
# only thing you have to paste. It checks for python3, downloads the agent and a
# copy of itself, registers this machine, and starts it. It installs no packages,
# adds no repositories, and opens no ports.
#
# AFTERWARDS — the copy it left in ~/.dazyflow is how the runner is managed:
#
#   ./runner.sh status | start | stop | restart | logs | install | uninstall
#
# Which life it is in depends on the first argument: a verb manages, anything
# else sets up. There is no second script to find, and no systemd incantation to
# remember.
#
# POSIX sh on purpose — this runs on whatever the org happens to have, which is
# not always bash.

set -eu

# DAZYFLOW_URL is substituted by the server. The fallback keeps the script
# usable straight from the repository, where nothing has substituted anything.
URL="${DAZYFLOW_URL:-@@DAZYFLOW_URL@@}"
# The server substitutes the checksum of the agent it is serving. Left as the
# placeholder when this file comes straight from the repository, where there is
# no server to vouch for anything and the check is skipped with a word about it.
AGENT_SHA256="@@DAZYFLOW_AGENT_SHA256@@"
TOKEN=""
NAME=""
LABELS=""
ALLOW=""
SERVICE=""

# How long systemd waits for the agent to finish its current task on stop.
# Covers the agent's own default script timeout (600s) with room to report the
# result, so a drain is never cut short by a SIGKILL.
STOP_TIMEOUT="660s"

UNIT_NAME="dazyflow-runner"
UNIT_DIR="$HOME/.config/systemd/user"
UNIT="$UNIT_DIR/$UNIT_NAME.service"
# Where the agent keeps its credential. Managing the service has to agree with
# the agent about this, or "is it registered?" gets the wrong answer.
CONFIG="${XDG_CONFIG_HOME:-$HOME/.config}/dazyflow/runner.json"

usage() {
	cat <<'EOF'
The Dazyflow runner.

Set up (from your Dazyflow server):
  curl -fsSL https://SERVER/runner.sh | sh -s -- --token dzrt_... --service

  --token TOKEN   registration token from Admin -> Runners in Dazyflow
  --url URL       Dazyflow server (already set if you piped this from one)
  --name NAME     name for this machine (default: this host's name)
  --tags A,B      tags a flow can target this machine by (its name is always one)
  --allow A,B     only let these programs run. Strongly recommended
  --dir PATH      where to install (default: $HOME/.dazyflow)
  --service       install and start a systemd user service, so it survives a reboot

Manage (from where it installed itself):
  ./runner.sh install      install the service and start it
  ./runner.sh status       is it running?
  ./runner.sh start        start it
  ./runner.sh stop         stop it
  ./runner.sh restart      stop it, then start it
  ./runner.sh logs         follow what it is doing
  ./runner.sh uninstall    stop it and remove the service

The service is a systemd USER service: no root, and the agent runs as you.
EOF
}

die() {
	echo "runner.sh: $1" >&2
	exit 1
}

# ---- which life is this? ----------------------------------------------
#
# A verb manages an existing install; anything else (flags, or nothing) sets one
# up. Nothing overlaps: setup is entirely flag-driven, so there is no argument
# that could plausibly mean both.

MODE="setup"
case "${1:-}" in
install | uninstall | remove | start | stop | restart | status | logs)
	MODE="manage"
	;;
esac

if [ "$MODE" = "manage" ]; then
	# Managing means we are the copy on disk, beside the agent. Finding it
	# relative to this script rather than a fixed path is what lets a second
	# runner directory be a genuinely separate runner.
	DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
else
	DIR="${DAZYFLOW_RUNNER_DIR:-$HOME/.dazyflow}"
fi
AGENT="$DIR/dzrunner.py"
ENV_FILE="$DIR/runner.env"

# ---- shared helpers ---------------------------------------------------

# A user service needs a user service manager to talk to. Checked before
# anything that needs it, because the failure is identical and confusing for
# every verb.
need_systemd() {
	command -v systemctl >/dev/null 2>&1 ||
		die "this needs systemd, and systemctl was not found.
Start the agent from whatever this machine uses to supervise processes:
  python3 $AGENT"
	systemctl --user show-environment >/dev/null 2>&1 ||
		die "cannot reach your user service manager.
This usually means the session has no user bus — common under 'su', in a
container, or over SSH for an account that is not lingering. Try:
  sudo loginctl enable-linger $(id -un)
then log in again."
}

# read_env pulls one value out of runner.env.
#
# Read, not sourced. Sourcing would mean a value containing a space needed
# quoting to survive, a value containing $ would expand, and the file would be
# executable code that anything able to write it could run as this user. Taking
# everything after the first '=' literally has none of those problems.
read_env() {
	[ -f "$ENV_FILE" ] || return 0
	sed -n "s/^$1=//p" "$ENV_FILE" | tail -n 1
}

installed() {
	[ -f "$UNIT" ]
}

require_installed() {
	installed || die "the service is not installed. Install it with:
  $0 install"
}

# ---- the service ------------------------------------------------------

write_unit() {
	# ALLOW is the one setting nothing else persists: the agent's saved
	# credential carries the server and the name, but not what it may run. Setup
	# records it in runner.env so re-installing the service keeps the
	# restriction rather than quietly dropping it.
	allow=$(read_env DAZYFLOW_ALLOW)
	url=$(read_env DAZYFLOW_URL)

	mkdir -p "$UNIT_DIR"
	{
		echo "[Unit]"
		echo "Description=Dazyflow runner agent"
		echo "After=network-online.target"
		echo
		echo "[Service]"
		# Quoted, because systemd splits ExecStart on whitespace: an unquoted
		# path or allow-list containing a space would silently become two
		# arguments and the service would fail complaining about the wrong
		# thing. (A literal % would still confuse systemd, which reads it as a
		# specifier; paths with % are rare enough to leave.)
		printf 'ExecStart=%s "%s"' "$(command -v python3)" "$AGENT"
		[ -n "$url" ] && printf ' --url "%s"' "$url"
		[ -n "$allow" ] && printf ' --allow "%s"' "$allow"
		echo
		# The agent's SIGTERM handler stops it BETWEEN tasks, so a stop or a
		# restart drains rather than killing a command halfway through — but
		# only if systemd lets it. The default KillMode=control-group SIGTERMs
		# every process in the cgroup, which is the running script and its
		# children, so the promise in the agent's handler and in `restart`
		# below was not kept. KillMode=mixed sends the signal to the agent
		# alone and leaves the script to finish.
		echo "KillMode=mixed"
		# Long enough to cover the step's own ceiling (the agent's default
		# timeout is 600s), or systemd SIGKILLs the script it just spared.
		echo "TimeoutStopSec=$STOP_TIMEOUT"
		echo "Restart=always"
		echo "RestartSec=10"
		echo
		echo "[Install]"
		# Without this `enable` has nothing to link, and the service would
		# start once and never come back after a reboot.
		echo "WantedBy=default.target"
	} >"$UNIT"
	echo "Wrote $UNIT"
}

# Linger is the difference between starting when you log in and starting at
# boot. On a machine nobody logs into, that is the difference between a runner
# that works and one that never appears.
linger_advice() {
	state="unknown"
	if command -v loginctl >/dev/null 2>&1; then
		state=$(loginctl show-user "$(id -un)" --property=Linger --value 2>/dev/null || echo unknown)
	fi
	if [ "$state" = "yes" ]; then
		echo "It will also start at boot, without anyone logging in."
		return
	fi
	echo
	echo "One step left, and the only one needing sudo:"
	echo "  sudo loginctl enable-linger $(id -un)"
	echo
	echo "Without it the runner starts when you log in, not when the machine"
	echo "boots — so a server nobody logs into would never come back."
}

cmd_install() {
	need_systemd
	[ -f "$AGENT" ] || die "no agent at $AGENT.
This script expects to sit next to dzrunner.py, where setup put it."
	# Registering is a separate step with a separate secret, so a service that
	# starts before it would crash-loop against a server that has never heard
	# of this machine.
	[ -f "$CONFIG" ] || die "this machine is not registered yet.
Register it first — get a token from Admin -> Runners and run:
  curl -fsSL SERVER/runner.sh | sh -s -- --token dzrt_... --service"

	write_unit
	systemctl --user daemon-reload ||
		die "systemd would not reload its unit files. Finish with:
  systemctl --user daemon-reload
  systemctl --user enable --now $UNIT_NAME"
	# enable --now STARTS a unit; on one that is already active it does
	# nothing, so the running process keeps its original ExecStart argv. That
	# matters because re-running install is the documented way to TIGHTEN the
	# allow-list: without the restart the operator gets "Started." while the
	# old, looser list is still in force until the next reboot.
	if systemctl --user enable "$UNIT_NAME" && systemctl --user restart "$UNIT_NAME"; then
		echo "Started. It will come back on its own if it stops."
		echo "(Restarted, so any change to runner.env is in force now. The"
		echo " agent finishes its current task first.)"
	else
		die "the service was written but would not start.
This machine is registered, so there is no need for a new token. See why with:
  $DIR/runner.sh status
  $DIR/runner.sh logs"
	fi
	linger_advice
}

cmd_uninstall() {
	need_systemd
	installed || {
		echo "Not installed; nothing to remove."
		exit 0
	}
	# Best effort: a unit that is already stopped, or was never enabled, must
	# not stop us from removing the file.
	systemctl --user disable --now "$UNIT_NAME" >/dev/null 2>&1 || true
	rm -f "$UNIT"
	systemctl --user daemon-reload >/dev/null 2>&1 || true
	echo "Removed $UNIT"
	# Said plainly because it is the surprising half: the machine is still
	# listed in Dazyflow, and its credential still works, until someone removes
	# it there. Stopping the agent is not revoking it.
	echo
	echo "This machine is still registered in Dazyflow. To revoke it, remove it"
	echo "from Admin -> Runners; that is what stops its credential working."
}

# ---- setup ------------------------------------------------------------

fetch() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$1" -o "$2"
	elif command -v wget >/dev/null 2>&1; then
		wget -qO "$2" "$1"
	else
		die "need curl or wget to download the agent."
	fi
}

# sha256_of prints a file's checksum, or nothing if this machine has no tool
# for it. Three spellings because the one that exists depends on the OS:
# GNU/BusyBox coreutils, the BSDs, and machines with only openssl.
sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d" " -f1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d" " -f1
	elif command -v openssl >/dev/null 2>&1; then
		openssl dgst -sha256 "$1" | sed "s/.*= *//"
	fi
}

# verify_agent checks the downloaded agent against the checksum the server
# published, and refuses to continue if they disagree.
#
# This file is about to chmod +x something and run it as a service, so what
# vouches for those bytes matters. Without the check the answer is "the
# transport did" — which behind a proxy that terminates TLS and does not say so
# can mean a plain-HTTP download.
#
# Not a signature: the checksum comes down the same channel as the script, so it
# does not help against someone who controls the whole channel. It does catch
# the split channel, a tampered mirror, a truncated download, and an agent that
# has drifted from the server it will talk to.
verify_agent() {
	case "$AGENT_SHA256" in
	"" | *@@*)
		echo "Note: no checksum published for the agent (running from a repository copy)."
		return 0
		;;
	esac
	got=$(sha256_of "$1")
	if [ -z "$got" ]; then
		echo "Note: no sha256 tool on this machine, so the agent was not verified."
		return 0
	fi
	[ "$got" = "$AGENT_SHA256" ] || die "the downloaded agent does not match the checksum $URL published.
  expected $AGENT_SHA256
  got      $got
Nothing was installed and your token has not been used. This means the file
changed between the server and this machine — check the URL and try again."
}

cmd_setup() {
	while [ $# -gt 0 ]; do
		case "$1" in
		--token) TOKEN="$2"; shift 2 ;;
		--url) URL="$2"; shift 2 ;;
		--name) NAME="$2"; shift 2 ;;
		# --labels is the old name for --tags. Still accepted: it is in every
		# install command anyone has written down, and breaking those to rename a
		# synonym would be a poor trade.
		--tags|--labels) LABELS="$2"; shift 2 ;;
		--allow) ALLOW="$2"; shift 2 ;;
		--dir) DIR="$2"; shift 2 ;;
		--service) SERVICE=1; shift ;;
		-h | --help) usage; exit 0 ;;
		*)
			# A bare word here is almost always a mistyped verb — dispatch
			# only reaches setup when the first argument is not one — so say
			# "command" rather than blaming an option they did not give.
			case "$1" in
			-*) echo "runner.sh: unknown option $1" >&2 ;;
			*) echo "runner.sh: unknown command $1" >&2 ;;
			esac
			usage >&2
			exit 2
			;;
		esac
	done
	# --dir moves everything, so re-derive what hangs off it.
	AGENT="$DIR/dzrunner.py"
	ENV_FILE="$DIR/runner.env"

	if [ -z "$TOKEN" ]; then
		echo "runner.sh: --token is required." >&2
		echo "Get one from Admin -> Runners in Dazyflow; they last 30 minutes." >&2
		exit 2
	fi
	case "$URL" in
	"" | *@@*)
		echo "runner.sh: no server address." >&2
		echo "Pass --url https://your-dazyflow-server" >&2
		exit 2
		;;
	esac

	# python3 rather than a compiled binary is the whole point: the agent runs
	# arbitrary commands on this machine, so you should be able to read it first.
	command -v python3 >/dev/null 2>&1 ||
		die "python3 is required and was not found.
It ships with most systems; install it and run this again."

	# Check systemd BEFORE anything spends the token.
	#
	# A registration token works once and lasts thirty minutes. Discovering
	# after registration that this machine has no usable service manager would
	# leave the operator needing a fresh token to try again — so every reason
	# --service cannot work has to be found while the token is still unused.
	if [ -n "$SERVICE" ]; then
		if ! command -v systemctl >/dev/null 2>&1; then
			echo "runner.sh: --service needs systemd, and systemctl was not found." >&2
			echo "Run without --service and start the agent from whatever this machine uses" >&2
			echo "to supervise processes. Your token has not been used." >&2
			exit 1
		fi
		if ! systemctl --user show-environment >/dev/null 2>&1; then
			echo "runner.sh: cannot reach your user service manager." >&2
			echo "This usually means the session has no user bus — common under 'su'," >&2
			echo "in a container, or over SSH for an account that is not lingering." >&2
			echo >&2
			echo "Try, as a user who can sudo:" >&2
			echo "  sudo loginctl enable-linger $(id -un)" >&2
			echo "then log in again and re-run this. Your token has not been used." >&2
			exit 1
		fi
	fi

	mkdir -p "$DIR"
	echo "Downloading the agent to $AGENT"
	# Verified BEFORE it lands on its final path, so a failed check leaves no
	# half-trusted file behind and nothing has been made executable.
	fetch "$URL/dzrunner.py" "$AGENT.new"
	verify_agent "$AGENT.new"
	chmod +x "$AGENT.new"
	mv -f "$AGENT.new" "$AGENT"

	# And a copy of ourselves, because this one came down a pipe and is not on
	# disk anywhere. That copy is what manages the runner from now on.
	#
	# Downloaded beside the target and moved into place, never written over it.
	# After the first install the only copy the operator has is
	# $DIR/runner.sh, and the script itself invites a re-run ("consider
	# re-running with --allow"). Writing over it would truncate and rewrite the
	# file /bin/sh is reading by byte offset, so the rest of this run would
	# execute whatever now sits at that position — after the single-use token
	# had already been spent. rename(2) is atomic and leaves the open file
	# intact.
	fetch "$URL/runner.sh" "$DIR/runner.sh.new"
	chmod +x "$DIR/runner.sh.new"
	mv -f "$DIR/runner.sh.new" "$DIR/runner.sh"

	# Show what is about to be trusted. One line, but it points at a file the
	# operator can actually read — which is the reason it is a script.
	echo "The agent is one file, standard library only. Read it: $AGENT"

	set -- --url "$URL" --token "$TOKEN"
	[ -n "$NAME" ] && set -- "$@" --name "$NAME"
	[ -n "$LABELS" ] && set -- "$@" --tags "$LABELS"
	[ -n "$ALLOW" ] && set -- "$@" --allow "$ALLOW"

	if [ -z "$ALLOW" ]; then
		echo
		echo "NOTE: with no --allow list, any command a flow sends will run here."
		echo "      Consider re-running with --allow ./your-script.sh"
		echo
	fi

	# Record what the service cannot work out for itself, on BOTH paths.
	#
	# The saved credential carries the server and this machine's name. It does
	# not carry what the agent may RUN — so without this, someone who starts the
	# agent in the foreground with --allow and later decides to make it a
	# service would get a service with no allow-list at all. Silently
	# unrestricting a runner because the operator changed how it starts is the
	# wrong way round.
	#
	# Plain KEY=VALUE, unquoted on purpose: read_env reads this rather than
	# sourcing it, taking everything after the first '=' literally. So a path
	# with a space in it needs no escaping here and cannot break there.
	{
		echo "# Written by runner.sh setup. Edit and re-run: ./runner.sh install"
		echo "DAZYFLOW_URL=$URL"
		echo "DAZYFLOW_ALLOW=$ALLOW"
	} >"$ENV_FILE"

	if [ -n "$SERVICE" ]; then
		# Register in the foreground FIRST, then hand a credential-only service
		# to systemd. Two reasons, both about what the operator sees:
		#
		#   A bad token fails here, in their terminal, where they can read it
		#   and get another. Baked into the unit it would fail inside the
		#   service on every restart, ten seconds apart, in a log nobody is
		#   watching.
		#
		#   The token never reaches a file. The agent saves its own credential
		#   to a 0600 config, so the unit needs no secret at all — nothing to
		#   leak from a world-readable unit file, and nothing to go stale in it.
		echo "Registering this machine"
		if ! python3 "$AGENT" --register-only "$@"; then
			echo >&2
			echo "runner.sh: registration failed, so no service was installed." >&2
			exit 1
		fi
		cmd_install
		exit 0
	fi

	echo "Registering and starting. Press Ctrl-C to stop."
	echo "To keep it running as a service instead: $DIR/runner.sh install"
	echo
	exec python3 "$AGENT" "$@"
}

# ---- dispatch ---------------------------------------------------------

case "${1:-}" in
install)
	cmd_install
	;;
uninstall | remove)
	cmd_uninstall
	;;
start | stop | restart)
	need_systemd
	require_installed
	# The agent stops between tasks, so stopping does not kill a command
	# halfway through — the unit sets KillMode=mixed and a TimeoutStopSec that
	# covers a full-length task, which is what makes that true rather than
	# merely intended.
	systemctl --user "$1" "$UNIT_NAME"
	echo "Done ($1)."
	;;
status)
	need_systemd
	require_installed
	# Exit code passed through, so this is usable in a check.
	systemctl --user status "$UNIT_NAME" --no-pager
	;;
logs)
	need_systemd
	require_installed
	command -v journalctl >/dev/null 2>&1 || die "journalctl was not found."
	journalctl --user -u "$UNIT_NAME" -f -n 50
	;;
"" | -h | --help | help)
	# Nothing to do: show both lives rather than guessing at one.
	usage
	;;
*)
	cmd_setup "$@"
	;;
esac
