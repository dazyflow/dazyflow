// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package datetime

import (
	"fmt"
	"github.com/dazyflow/dazyflow/internal/datenames"
	"strconv"
	"strings"
	"time"
)

// The custom-format vocabulary.
//
// Go's own layouts are a reference date ("2006-01-02") rather than tokens, and
// exposing that as the format field failed in the two ways a person actually
// types. `YYYY-MM-DD` is not a layout, so time.Format echoed it verbatim into
// the email; and any literal word sharing letters with the reference date was
// silently rewritten, so "Due Monday 2 January" rendered as "Due Thursday 27
// August".
//
// So a custom format is rendered token by token here rather than translated into
// a Go layout: only what the table matches is substituted, everything else is
// copied out untouched, and an unrecognised run of letters is an ERROR. A
// translated layout could not fix the second bug — its literal text would still
// go through time.Format and still be eaten.
//
// The vocabulary is the LDML/moment one (YYYY, MM, DD, HH, mm, ss), because that
// is what spreadsheets, date pickers and other automation tools use.

type formatToken struct {
	tok    string
	layout string
	name   nameKind
}

type nameKind uint8

const (
	nameNone nameKind = iota
	nameMonthLong
	nameMonthShort
	nameDayLong
	nameDayShort
)

// formatTokens is scanned in order at each position, so a token must never
// precede a LONGER token it is a prefix of (YYYY before YY, MMMM before MM
// before M) or the shorter one would win and swallow half the longer.
//
// Deliberately absent: an unpadded 24-hour hour. Go has no layout for one
// ("15" is always padded), so `H` would have to be faked; it errors instead,
// with the hint to use HH. Week numbers are absent for the same reason.
var formatTokens = []formatToken{
	{"YYYY", "2006", nameNone},
	{"YY", "06", nameNone},
	{"MMMM", "", nameMonthLong},
	{"MMM", "", nameMonthShort},
	{"MM", "01", nameNone},
	{"M", "1", nameNone},
	{"dddd", "", nameDayLong},
	{"ddd", "", nameDayShort},
	{"DD", "02", nameNone},
	{"D", "2", nameNone},
	{"HH", "15", nameNone},
	{"hh", "03", nameNone},
	{"h", "3", nameNone},
	{"mm", "04", nameNone},
	{"m", "4", nameNone},
	{"ss", "05", nameNone},
	{"s", "5", nameNone},
	{"A", "PM", nameNone},
	{"a", "pm", nameNone},
	{"ZZ", "-0700", nameNone},
	{"Z", "-07:00", nameNone},
	{"z", "MST", nameNone},
}

// tokenHints answers the near-misses worth naming rather than listing the
// whole vocabulary at someone who was one keystroke away. Lowercase spellings
// are the common one (the tokens are case-sensitive: MM is the month, mm the
// minute), and `H` is the hour Go cannot render unpadded.
var tokenHints = map[string]string{
	"yyyy": "YYYY", "yy": "YY", "y": "YYYY",
	"mmmm": "MMMM", "mmm": "MMM",
	"dd": "DD", "d": "DD",
	"H": "HH", "hh24": "HH",
	"SS": "ss", "S": "ss",
	"YYYYY": "YYYY",
}

func renderCustom(t time.Time, format string, names datenames.Names) (string, error) {
	var sb strings.Builder
	for i := 0; i < len(format); {
		// [literal] — the escape hatch for words made of token letters.
		if format[i] == '[' {
			end := strings.IndexByte(format[i:], ']')
			if end < 0 {
				return "", fmt.Errorf("format %q has a [ with no closing ] — literal words go inside brackets, e.g. \"[week of] D MMM\"", format)
			}
			sb.WriteString(format[i+1 : i+end])
			i += end + 1
			continue
		}
		if tok, ok := matchToken(format[i:]); ok {
			// One more letter of the SAME kind after a full token means a
			// longer token that doesn't exist — "mmm", "YYYYY". Tokenising
			// greedily would split "mmm" into mm+m and render the minute
			// twice ("055"), which is worse than useless because it looks
			// like a number. Adjacent DIFFERENT tokens are fine, so
			// "YYYYMMDD" still works.
			if next := i + len(tok.tok); next < len(format) && format[next] == tok.tok[len(tok.tok)-1] {
				return "", unknownTokenErr(letterRun(format[i:]))
			}
			sb.WriteString(tok.render(t, names))
			i += len(tok.tok)
			continue
		}
		if isFormatLetter(format[i]) {
			return "", unknownTokenErr(letterRun(format[i:]))
		}
		// Anything else is literal. Copied a byte at a time, which is safe for
		// UTF-8: a continuation byte is never an ASCII letter or a '[', so a
		// multi-byte rune passes through intact.
		sb.WriteByte(format[i])
		i++
	}
	return sb.String(), nil
}

func unknownTokenErr(run string) error {
	if want, ok := tokenHints[run]; ok {
		return fmt.Errorf("%q isn't a format token — did you mean %q? (tokens are case-sensitive: MM is the month, mm the minute)", run, want)
	}
	return fmt.Errorf("%q isn't a format token — use YYYY MM DD for the date, HH mm ss for the time, MMM/MMMM for a month name, ddd/dddd for a weekday, and put literal words in brackets like \"[on] D MMM\"", run)
}

// render renders one token: from the localized table when it is a name, and
// through time.Format otherwise (digits and offsets are the same in every
// language, so there is nothing to localize about them).
func (tok formatToken) render(t time.Time, names datenames.Names) string {
	switch tok.name {
	case nameMonthLong:
		return names.Months[int(t.Month())-1]
	case nameMonthShort:
		return names.MonthsShort[int(t.Month())-1]
	case nameDayLong:
		return names.Days[int(t.Weekday())]
	case nameDayShort:
		return names.DaysShort[int(t.Weekday())]
	}
	return t.Format(tok.layout)
}

func matchToken(s string) (formatToken, bool) {
	for _, t := range formatTokens {
		if strings.HasPrefix(s, t.tok) {
			return t, true
		}
	}
	return formatToken{}, false
}

func isFormatLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func letterRun(s string) string {
	n := 0
	for n < len(s) && isFormatLetter(s[n]) {
		n++
	}
	return s[:n]
}

func parseClock(s string) (hour, min, sec int, err error) {
	fields := strings.Split(strings.TrimSpace(s), ":")
	if len(fields) < 2 || len(fields) > 3 {
		return 0, 0, 0, fmt.Errorf("%q isn't a time of day — write it as \"09:00\" or \"17:30:15\"", s)
	}
	limits := [3]int{23, 59, 59}
	var out [3]int
	for i, f := range fields {
		n, convErr := strconv.Atoi(strings.TrimSpace(f))
		if convErr != nil || n < 0 || n > limits[i] {
			return 0, 0, 0, fmt.Errorf("%q isn't a time of day — write it as \"09:00\" or \"17:30:15\" (00-23 hours, 00-59 minutes)", s)
		}
		out[i] = n
	}
	return out[0], out[1], out[2], nil
}
