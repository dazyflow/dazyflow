#!/bin/bash
# SPDX-FileCopyrightText: 2026 Angels' Ware
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Deploy the newest release tag onto this host.
#
# This is the entry point for a runner-driven deploy — the script a "Run on
# your machine" step invokes, and the one to name in that runner's allow-list
# (an allow-listed runner executes commands WITHOUT a shell, so the step
# cannot be `cd somewhere && make ...`; the cd has to live in here).
#
# `set -e` is the whole point of the file. Without it a failed deploy still
# exits 0 and the flow that ran it goes green over a box that is still serving
# the previous release — which is not hypothetical, it is how 0.25.0 reported
# a successful deploy onto images that were fifteen hours old.
set -euo pipefail

# Resolve the checkout from this script's own location rather than hardcoding
# a path. The runner's working directory is its own home, not the checkout,
# and this file should work in any deployment rather than only in /opt.
cd "$(dirname "$0")/.."

# Deliberately NO `git pull`. `make upgrade` leaves the tree DETACHED at the
# release tag so the working tree matches the running image, and `git pull` on
# a detached HEAD fails outright ("You are not currently on a branch") — which
# in a script without `set -e` is a silent non-event that everyone stops
# noticing. It is redundant besides: upgrade opens with
# `git fetch --tags --force --prune-tags`, and a tag is all the deploy selects
# on. This script is replaced by that checkout while it runs; that is safe,
# because git writes a new inode and this shell keeps reading the old one.
PROD=1 make upgrade
