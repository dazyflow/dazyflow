// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"fmt"
	"net/url"
	"strings"

	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

// Shared rules for anything an org configures that contributes STEPS — an MCP
// server, a described web API, a runner. Adding a source of executable steps is a
// bigger power than editing a flow, and the rules must not drift between them.

const maxStepSourceNameLen = 48

// Derived server-side: two clients slugging differently would be two ids.
func slugStepSourceName(label string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(label)) {
		if folded, ok := asciiFold[r]; ok {
			b.WriteString(folded)
			prevDash = false
			continue
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > maxStepSourceNameLen {
		out = strings.Trim(out[:maxStepSourceNameLen], "-")
	}
	return out
}

// So an accented name still yields a usable id rather than being rejected.
var asciiFold = map[rune]string{
	'á': "a", 'à': "a", 'â': "a", 'ä': "a", 'ã': "a", 'å': "a", 'ā': "a",
	'æ': "ae",
	'ç': "c", 'ć': "c", 'č': "c",
	'é': "e", 'è': "e", 'ê': "e", 'ë': "e", 'ē': "e", 'ę': "e",
	'í': "i", 'ì': "i", 'î': "i", 'ï': "i", 'ī': "i",
	'ñ': "n", 'ń': "n",
	'ó': "o", 'ò': "o", 'ô': "o", 'ö': "o", 'õ': "o", 'ø': "o", 'ō': "o",
	'ú': "u", 'ù': "u", 'û': "u", 'ü': "u", 'ū': "u",
	'ý': "y", 'ÿ': "y",
	'ß': "ss",
	'đ': "d", 'ð': "d",
	'ł': "l",
	'ś': "s", 'š': "s",
	'ż': "z", 'ź': "z", 'ž': "z",
	'þ': "th",
}

// The name lands inside a step id a graph stores, so it is bounded on both.
func validStepSourceName(name string) error {
	if name == "" {
		return fmt.Errorf("name is empty")
	}
	if len(name) > maxStepSourceNameLen {
		return fmt.Errorf("name too long (max %d)", maxStepSourceNameLen)
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return fmt.Errorf("name may use lowercase letters, digits, - and _ only")
		}
	}
	return nil
}

// A collision would silently rebind an existing source's steps.
func uniqueStepSourceName(base, fallback string, taken map[string]bool, limit int) (string, error) {
	if base == "" {
		base = fallback
	}
	if !taken[base] {
		return base, nil
	}
	for n := 2; n <= limit+1; n++ {
		suffix := fmt.Sprintf("-%d", n)
		stem := base
		if len(stem)+len(suffix) > maxStepSourceNameLen {
			stem = strings.Trim(stem[:maxStepSourceNameLen-len(suffix)], "-")
		}
		candidate := stem + suffix
		if !taken[candidate] {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not derive a free name from %q — every variant is taken", base)
}

// Refused before the request is built, so an admin sees why at save time; the
// IP-level guard still applies at dial time.
func validStepSourceURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("URL is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("URL is not valid: %w", err)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !hfnet.PrivateEgressAllowed() {
			return fmt.Errorf("URL must be https — a token sent over http is readable in transit")
		}
	default:
		return fmt.Errorf("URL must start with https://")
	}
	if u.Host == "" {
		return fmt.Errorf("URL has no host")
	}
	return nil
}
