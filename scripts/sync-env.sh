#!/usr/bin/env bash
# sync-env.sh — bring .env into sync with .env.example.
#
# Behavior:
#   * .env does not exist  → copy .env.example verbatim.
#   * .env exists          → append, at the end of .env, every key that
#                            is in .env.example but missing from .env,
#                            using the example's default value. Each
#                            appended key inherits its .env.example
#                            section header so the docs come along
#                            for the ride.
#
# What it never does:
#   * Modify or reorder any existing line in .env. Operator edits, even
#     odd ones, survive untouched.
#   * Remove keys that aren't in .env.example. Orphans are reported as
#     a non-fatal warning so the operator can decide what to do.
#
# Usage:  scripts/sync-env.sh [example-path] [target-path]
#         defaults: .env.example  .env
#
# Idempotent. Safe to re-run. Exits non-zero only on bad arguments.

set -euo pipefail

example="${1:-.env.example}"
target="${2:-.env}"

if [ ! -f "$example" ]; then
	echo "sync-env: $example not found" >&2
	exit 1
fi

if [ ! -f "$target" ]; then
	cp "$example" "$target"
	echo "sync-env: $target did not exist, copied $example verbatim."
	exit 0
fi

# index_keys reads a .env-style file and fills the named associative
# array with the keys that are actually set (commented-out lines are
# documentation, not "set"). Splits on the first '=' so values with
# '=' survive intact.
index_keys() {
	local -n out="$1"
	local file="$2"
	local line key
	while IFS= read -r line || [ -n "$line" ]; do
		case "$line" in
			''|'#'*) continue ;;
		esac
		key="${line%%=*}"
		out["$key"]=1
	done < "$file"
}

# index_documented is index_keys' counterpart for the ORPHAN check, and the
# distinction matters: "is this key set?" and "is this key documented?" are
# different questions, and a commented-out example line answers no to the
# first and yes to the second. Sharing one indexer meant every key the example
# ships commented — COMPOSE_FILE, which MUST stay commented so a dev host
# doesn't merge the prod overlay — was reported as an undocumented orphan on
# every run of every production host that correctly set it.
index_documented() {
	local -n out="$1"
	local file="$2"
	local line
	while IFS= read -r line || [ -n "$line" ]; do
		# Strip leading blanks, one '#', then blanks again, so both
		# "#KEY=v" and "#   KEY=v" are recognised.
		line="${line#"${line%%[![:space:]]*}"}"
		line="${line#\#}"
		line="${line#"${line%%[![:space:]]*}"}"
		# Only lines that actually look like an assignment — prose in a
		# comment can contain '=' and must not register as a key.
		if [[ "$line" =~ ^([A-Za-z_][A-Za-z0-9_]*)= ]]; then
			out["${BASH_REMATCH[1]}"]=1
		fi
	done < "$file"
}

declare -A have example_keys documented
index_keys have "$target"
index_keys example_keys "$example"
index_documented documented "$example"

# Walk the example, capture missing entries plus their preceding
# section header (the most recent `# ---- ... ----` line).
new_block="$(mktemp)"
trap 'rm -f "$new_block"' EXIT

section=""
section_emitted=""
added=()
while IFS= read -r line || [ -n "$line" ]; do
	case "$line" in
		'# ---- '*)
			section="$line"
			section_emitted=""
			continue
			;;
		''|'#'*) continue ;;
	esac
	key="${line%%=*}"
	if [ -z "${have[$key]+x}" ]; then
		if [ -n "$section" ] && [ -z "$section_emitted" ]; then
			printf '\n%s\n' "$section" >> "$new_block"
			section_emitted=1
		fi
		printf '%s\n' "$line" >> "$new_block"
		added+=("$key")
	fi
done < "$example"

# Orphan keys: set in target but not documented anywhere in the example —
# commented-out documentation counts (see index_documented).
orphans=()
for k in "${!have[@]}"; do
	if [ -z "${documented[$k]+x}" ]; then
		orphans+=("$k")
	fi
done

if [ "${#added[@]}" -eq 0 ]; then
	echo "sync-env: $target is already in sync with $example."
else
	{
		printf '\n# ---- New from %s on %s ----\n' "$example" "$(date -u +%FT%TZ)"
		cat "$new_block"
	} >> "$target"
	echo "sync-env: appended ${#added[@]} new key(s) to $target:"
	printf '  %s\n' "${added[@]}"
fi

if [ "${#orphans[@]}" -gt 0 ]; then
	echo "sync-env: warning, $target has ${#orphans[@]} key(s) not documented in $example:"
	printf '  %s\n' "${orphans[@]}" | sort
fi
