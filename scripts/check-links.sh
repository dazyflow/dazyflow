#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Angels' Ware
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Every relative Markdown link in a .md file must resolve on disk. Covers files
# in the index AND new unstaged ones, since a brand-new document is exactly the
# one whose links have never been read by anybody.
#
# web/src/docs/content.test.ts covers docs/guide/ alone, and keeps the
# two-resolver rules that apply to guide pages (a sibling is ./slug.md, the
# generated step catalog is an absolute docs.dazyflow.app URL). This is the
# wide net.
#
# Out of scope on purpose: http(s) links (a network call is not a build gate)
# and #anchors (the docs SPA derives heading ids at render time, and
# content.test.ts already pins that derivation).
set -euo pipefail

cd "$(dirname "$0")/.."

python3 - <<'PY'
import os
import re
import subprocess
import sys
from urllib.parse import unquote

LINK = re.compile(r"\[(?:[^\][]|\[[^\]]*\])*\]\(\s*<?([^)>\s]+)>?[^)]*\)")

# A fenced block or an inline code span can legitimately SHOW markdown — this
# file's own docstring does, and so does the review record that quotes the
# README's `[docs/guide](docs/guide)`. Blank those spans before scanning, and
# blank them character-for-character so reported line numbers stay true.
FENCE = re.compile(r"^(?P<indent> {0,3})(?P<ticks>```+|~~~+)[^\n]*\n.*?^\1(?P=ticks)[^\n]*$",
                   re.DOTALL | re.MULTILINE)
CODESPAN = re.compile(r"(?<!`)(`+)(?!`).*?(?<!`)\1(?!`)", re.DOTALL)


def mask(text: str) -> str:
    """Replace code with spaces, preserving length and newlines."""
    def blank(m: "re.Match[str]") -> str:
        return "".join("\n" if c == "\n" else " " for c in m.group(0))

    return CODESPAN.sub(blank, FENCE.sub(blank, text))

# --others --exclude-standard as well as the index: a brand-new .md file is
# precisely the one whose links have never been checked, and `git ls-files`
# alone does not see it until it is staged. Ignored files stay out.
files = set(
    subprocess.run(
        ["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard", "*.md"],
        capture_output=True, text=True, check=True,
    ).stdout.split("\0")
)

bad = []
checked = 0
for path in sorted(filter(None, files)):
    try:
        text = open(path, encoding="utf-8").read()
    except OSError as exc:
        print(f"check-links: cannot read {path}: {exc}", file=sys.stderr)
        raise SystemExit(1)
    base = os.path.dirname(path)
    for m in LINK.finditer(mask(text)):
        href = m.group(1)
        if href.startswith(("http://", "https://", "mailto:", "#", "tel:")):
            continue
        # A link target is URL-ish, so "a%20b.md" means the file "a b.md".
        target = unquote(href.split("#", 1)[0])
        if not target:
            continue
        checked += 1
        # A directory link resolves the way a repository host resolves it: the
        # directory itself, whose README.md is what actually gets rendered.
        if not os.path.exists(os.path.normpath(os.path.join(base, target))):
            bad.append((path, text[: m.start()].count("\n") + 1, href))

if bad:
    print("check-links: broken relative Markdown links\n", file=sys.stderr)
    for path, line, href in bad:
        print(f"  {path}:{line}  ->  {href}", file=sys.stderr)
    print(
        f"\n{len(bad)} broken of {checked} relative links. "
        "Create the target, or fix the path.",
        file=sys.stderr,
    )
    raise SystemExit(1)

print(f"check-links: {checked} relative Markdown links all resolve.")
PY
