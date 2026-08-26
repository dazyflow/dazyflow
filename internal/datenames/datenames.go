// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package datenames

import (
	"fmt"
	"strings"
	"time"
)

// Package datenames holds localized day and month names — the words Go's time
// package cannot produce.
//
// Shared rather than owned by whoever needed it first: the Date & time drop
// formats them into a flow's output, and the daemon's transactional email
// formats them into an invitation's expiry date. One table means a language
// added for one surface is added for both, and the casing rule below is
// decided once.
//
// Go's time package has no localization: t.Format("Monday") is always English,
// and there is no locale to hand it. The obvious shortcut is a dead end —
// golang.org/x/text/date ships generated CLDR tables but exports no API at all
// (its gen.go is //go:build ignore), and Intl is a browser thing while this
// runs on the server. So the names live here, in a table we own.
//
// A table rather than a CLDR dependency because the set is small and the cost
// of being wrong is visible: twelve months and seven days per language, in a
// product whose UI ships two languages. If that count grows past a handful,
// a library earns its keep; at two it would be a dependency for forty strings.
//
// CASING IS PART OF THE DATA, not a rule applied afterwards. English
// capitalises day and month names; Swedish does not — "måndag", "augusti". A
// formatter that capitalised both would be wrong in Swedish in exactly the way
// it would be wrong in English to lowercase them, and no amount of
// post-processing knows which is which. The table stores each language's names
// as that language writes them.
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

// swedishNames: lowercase throughout, which is how Swedish writes them. The
// short forms are the three-letter ones CLDR gives (with "maj" unabbreviated
// because it is already three letters).
// Swedish: lowercase throughout, which is how Swedish writes them.
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

// FormatDate writes a human date the way each language does: "27 August 2026",
// "27 augusti 2026". Day-month-year in both — which is what Swedish uses and
// what the English emails already wrote ("2 January 2006"), so no caller
// changes shape as languages are added.
func FormatDate(t time.Time, locale string) string {
	n := For(locale)
	return fmt.Sprintf("%d %s %d", t.Day(), n.Months[int(t.Month())-1], t.Year())
}

// FormatDateTime is FormatDate plus a clock and the zone abbreviation — for a
// deadline where the hour matters (a password-reset link that expires the same
// day). The clock is 24-hour in both languages: Swedish has no 12-hour
// convention, and an English reader is not confused by one.
func FormatDateTime(t time.Time, locale string) string {
	return FormatDate(t, locale) + t.Format(", 15:04 MST")
}
