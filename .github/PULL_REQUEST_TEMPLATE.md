<!--
SPDX-FileCopyrightText: 2026 Angels' Ware
SPDX-License-Identifier: AGPL-3.0-or-later
-->

## What this changes

<!-- What a user or operator experiences differently. One or two sentences. -->

## Why

<!-- The problem. If a review or an issue prompted it, link that. -->

## Checklist

- [ ] `make check` passes (`make ci` if this touches `web/` or `runner/`)
- [ ] Entry added under `## [Unreleased]` in `CHANGELOG.md` — never under a
      released heading
- [ ] New/changed source files carry the SPDX header
- [ ] `make catalogs` re-run and committed, if a drop was added or reworded
- [ ] New `DAZYFLOW_*` knobs documented in `.env.example`
- [ ] User-facing copy says **step**, not **drop**
      (see `docs/decisions/2026-08-20-step-vocabulary.md`)

<!-- Security issue? Don't open a PR — see SECURITY.md for private reporting. -->
