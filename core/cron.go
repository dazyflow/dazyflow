// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"fmt"

	"github.com/netresearch/go-cron"
)

// CronParser is shared by the scheduler, the validate endpoint and the trigger
// lint, so a spec the editor calls valid is one the scheduler will run.
//
// Five fields, no descriptors. RobfigCompat keeps the semantics existing specs
// were written against (robfig/cron): day-of-month and day-of-week are ORed as
// in Vixie cron, rather than go-cron's default AND.
var CronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.RobfigCompat)

// ParseCron parses a user-authored spec. A panic inside the parser becomes an
// error, so one malformed flow cannot take down the scheduler or a request.
func ParseCron(p cron.Parser, spec string) (sched cron.Schedule, err error) {
	defer func() {
		if r := recover(); r != nil {
			sched, err = nil, fmt.Errorf("invalid cron spec %q: %v", spec, r)
		}
	}()
	return p.Parse(spec)
}
