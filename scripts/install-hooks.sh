#!/usr/bin/env bash
# install-hooks.sh — link this repo's tracked git hooks into .git/hooks.
#
# Hooks under .git/hooks are NOT version-controlled, so the canonical copies
# live in scripts/git-hooks/ and this script symlinks them in (falling back
# to a copy if the filesystem refuses symlinks). Idempotent — safe to re-run.
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
src_dir="$root/scripts/git-hooks"
dst_dir="$root/.git/hooks"

mkdir -p "$dst_dir"
for src in "$src_dir"/*; do
	[ -f "$src" ] || continue
	name="$(basename "$src")"
	dst="$dst_dir/$name"
	if ln -sf "$src" "$dst" 2>/dev/null; then
		:
	else
		cp "$src" "$dst"
	fi
	chmod +x "$dst"
	echo "installed $name → $dst"
done
