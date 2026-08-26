// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package datetime

import (
	"fmt"
	"git.sr.ht/~klahr/dazyflow/internal/datenames"
	"strconv"
	"strings"
	"time"
)

// The custom-format vocabulary.
//
// Go's own layouts are a reference date ("2006-01-02") rather than tokens, and
// exposing that as the format field failed in the two ways a person actually
// types. `YYYY-MM-DD` — the spelling everyone reaches for, and the one this
// drop's own example title uses — is not a layout, so time.Format echoed it
// verbatim: no error, the literal text "YYYY-MM-DD" in the email. And any
// literal word sharing letters with the reference date was silently rewritten,
// so "Due Monday 2 January" rendered as "Due Thursday 27 August".
//
// So a custom format is rendered token by token here instead of being
// translated into a Go layout: only what the table below matches is
// substituted, everything else is copied out untouched, and an unrecognised
// run of letters is an ERROR rather than output. Translating to a Go layout
// could not fix the second bug — the literal text in the translated layout
// would still go through time.Format and still be eaten.
//
// The vocabulary is the LDML/moment one (YYYY, MM, DD, HH, mm, ss), because
// that is what spreadsheets, most date pickers and most other automation tools
// use, so it is the one a user has already met.

// formatToken is one vocabulary entry: the token a user writes and the Go
// reference layout that renders that piece. A token whose output is a NAME
// (month, weekday) carries no layout — Go's names are English-only, so those
// four come from the localized table instead (see names.go).
type formatToken struct {
	tok    string
	layout string
	name   nameKind
}

// nameKind marks the tokens rendered from the name table rather than by
// time.Format.
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

// renderCustom renders t through a user-written format: tokens from
// formatTokens are substituted, [bracketed text] is copied out literally, and
// anything else that is not a letter (punctuation, spaces, digits) passes
// through. A letter run that is not a token is an error — that is the whole
// point, since the alternative is emitting it verbatim into someone's email.
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

// unknownTokenErr explains a letter run the vocabulary doesn't accept, naming
// the near-miss when there is one rather than reciting the whole table at
// someone who was one keystroke away.
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

// matchToken returns the longest vocabulary token at the start of s.
func matchToken(s string) (formatToken, bool) {
	for _, t := range formatTokens {
		if strings.HasPrefix(s, t.tok) {
			return t, true
		}
	}
	return formatToken{}, false
}

// isFormatLetter reports whether c could be part of a token. ASCII only: the
// vocabulary is ASCII, so a letter outside it (å, é) is literal text and needs
// no brackets.
func isFormatLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// letterRun returns the leading run of ASCII letters, so an error names the
// whole word the user wrote ("yyyy") rather than its first letter.
func letterRun(s string) string {
	n := 0
	for n < len(s) && isFormatLetter(s[n]) {
		n++
	}
	return s[:n]
}

// renderLegacyFormat renders a format typed into the `format` param itself,
// which before the Format dropdown existed meant a Go reference layout. Those
// are saved in existing graphs, so they keep working exactly as they did: the
// layout goes to time.Format verbatim.
//
// The fallback is the fix for the silent echo. When time.Format consumed
// NOTHING — the output is the format string, character for character, which is
// what happened to every `YYYY-MM-DD` ever typed here — the format is tried
// again as the custom vocabulary. So the spelling that used to leak into the
// message now renders, and a format that is neither reports an error instead
// of shipping itself.
func renderLegacyFormat(t time.Time, format string, names datenames.Names) (string, error) {
	// A Go reference layout is English by construction ("Mon 2 Jan 2006" names
	// English months), and a graph that saved one asked for exactly that
	// output. Localizing it would silently change what those flows send, so
	// the legacy path stays English; only the token vocabulary localizes.
	if out := t.Format(format); out != format {
		return out, nil
	}
	return renderCustom(t, format, names)
}

// parseClock reads a time of day: "9:00", "09:00", "17:30:15". Seconds are
// optional; anything out of range is an error rather than a rollover, since
// "25:00" is a typo and not a request for tomorrow at one.
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
