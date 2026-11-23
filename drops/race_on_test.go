// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build race

package drops_test

// raceSlowdown stretches runDropSafely's wall-clock budgets under the race
// detector, which runs CPU-bound drops several times slower (and more so when
// other packages' tests share the CPU). A genuine hang never returns, so it is
// still caught — just later.
const raceSlowdown = 4
