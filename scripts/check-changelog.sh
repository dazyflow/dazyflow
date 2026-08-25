#!/bin/sh
# SPDX-FileCopyrightText: 2026 Joachim Klahr
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Guard on the changelog's one invariant: a RELEASED section is immutable.
#
# `make patch` promotes [Unreleased] under a new [X.Y.Z] heading and leaves a
# fresh empty [Unreleased] above it. So between one release and the next, the
# heading that WAS the place to write becomes the one place you must not — and
# nothing about the file looks different. Write under the old heading and two
# things go wrong at once: the next release refuses ("[Unreleased] is empty —
# nothing to release"), and the previous version's notes claim work that is not
# in its tag.
#
# That is not a mistake anyone catches by reading, because the entry is correct
# prose in a plausible place. So it is checked: the newest released section must
# still match the commit that released it.
#
# Only the newest one. Older sections are equally immutable, but re-verifying
# every version on every run means walking the whole history for a rule that can
# only be broken at the top — and a guard that takes ten seconds is a guard
# people stop running.
set -eu

CHANGELOG=CHANGELOG.md

# The newest released heading, e.g. "## [0.15.1] - 2026-08-26".
head_line=$(grep -n '^## \[[0-9]' "$CHANGELOG" | head -1 || true)
if [ -z "$head_line" ]; then
	echo "changelog: no released section yet — nothing to check"
	exit 0
fi
version=$(printf '%s' "$head_line" | sed 's/.*## \[\([^]]*\)\].*/\1/')

# The commit that released it. `make patch` commits the changelog and VERSION
# together with the message "Release X.Y.Z", which is what makes this findable
# without depending on the tag having been pushed.
release_commit=$(git log --format=%H --grep="^Release ${version}$" -1 2>/dev/null || true)
if [ -z "$release_commit" ]; then
	echo "changelog: no 'Release ${version}' commit found — skipping (nothing to compare against)"
	exit 0
fi

section() {
	# Everything from the newest released heading up to the next one.
	awk -v v="## [$version]" '
		index($0, v) == 1 { inside = 1; print; next }
		inside && /^## \[/ { exit }
		inside { print }
	'
}

if git show "${release_commit}:${CHANGELOG}" | section > /tmp/changelog-released.$$ 2>/dev/null &&
	section < "$CHANGELOG" > /tmp/changelog-now.$$ &&
	diff -u /tmp/changelog-released.$$ /tmp/changelog-now.$$ > /tmp/changelog-diff.$$; then
	rm -f /tmp/changelog-released.$$ /tmp/changelog-now.$$ /tmp/changelog-diff.$$
	echo "changelog: ok ([$version] matches the commit that released it)"
	exit 0
fi

echo "changelog: the released [$version] section has been edited." >&2
echo >&2
sed -n '3,60p' /tmp/changelog-diff.$$ >&2
echo >&2
echo "A released section describes what is in its tag and must not change." >&2
echo "New entries go under [Unreleased], which is the heading ABOVE it." >&2
rm -f /tmp/changelog-released.$$ /tmp/changelog-now.$$ /tmp/changelog-diff.$$
exit 1
