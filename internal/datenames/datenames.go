// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package datenames

import (
	"fmt"
	"strings"
	"time"
)

// Package datenames holds localized day and month names — the words Go's time
// package cannot produce: t.Format("Monday") is always English, and there is no
// locale to hand it. golang.org/x/text/date is a dead end (generated CLDR
// tables, no exported API), and Intl is a browser thing.
//
// A table rather than a CLDR dependency because the set is small: twelve months
// and seven days per language, in a product whose UI ships two. It is shared
// because both the Date & time drop and the daemon's transactional email need
// it, so a language added for one is added for both.
//
// CASING IS PART OF THE DATA, not a rule applied afterwards. English
// capitalises day and month names; Swedish does not — "måndag", "augusti" — and
// no post-processing knows which is which.

// Names is one language's day and month names.
type Names struct {
	Days        [7]string // indexed by time.Weekday: Sunday..Saturday
	DaysShort   [7]string
	Months      [12]string // indexed by time.Month-1: January..December
	MonthsShort [12]string
}

// englishNames is also the fallback for any language we don't carry, so a
// locale we've never heard of reads as English rather than as blanks.
// English is also the fallback for any language the table does not carry.
var English = Names{
	Days:      [7]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"},
	DaysShort: [7]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"},
	Months: [12]string{
		"January", "February", "March", "April", "May", "June",
		"July", "August", "September", "October", "November", "December",
	},
	MonthsShort: [12]string{
		"Jan", "Feb", "Mar", "Apr", "May", "Jun",
		"Jul", "Aug", "Sep", "Oct", "Nov", "Dec",
	},
}

var Swedish = Names{
	Days:      [7]string{"söndag", "måndag", "tisdag", "onsdag", "torsdag", "fredag", "lördag"},
	DaysShort: [7]string{"sön", "mån", "tis", "ons", "tors", "fre", "lör"},
	Months: [12]string{
		"januari", "februari", "mars", "april", "maj", "juni",
		"juli", "augusti", "september", "oktober", "november", "december",
	},
	MonthsShort: [12]string{
		"jan", "feb", "mars", "apr", "maj", "juni",
		"juli", "aug", "sep", "okt", "nov", "dec",
	},
}

// For resolves a language code to its name set. Only the primary subtag
// is read, so "sv", "sv-SE" and "SV" all reach Swedish — the region does not
// change day or month names in the languages we carry, and rejecting "sv-SE"
// for having a region would be a trap rather than a check. Anything unknown
// (including empty) is English.
func For(locale string) Names {
	primary := strings.ToLower(strings.TrimSpace(locale))
	if i := strings.IndexAny(primary, "-_"); i >= 0 {
		primary = primary[:i]
	}
	switch primary {
	case "sv":
		return Swedish
	default:
		return English
	}
}

func FormatDate(t time.Time, locale string) string {
	n := For(locale)
	return fmt.Sprintf("%d %s %d", t.Day(), n.Months[int(t.Month())-1], t.Year())
}

func FormatDateTime(t time.Time, locale string) string {
	return FormatDate(t, locale) + t.Format(", 15:04 MST")
}
