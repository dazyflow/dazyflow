// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"fmt"
	"net/url"
	"strings"

	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

// Shared rules for a STEP SOURCE — an org-configured thing that contributes
// steps to its palette. Two exist: an MCP server (mcp:<name>:<tool>) and a
// described web API (api:<name>:<operation>).
//
// The rules here are the ones both must obey because both put a tenant-chosen
// name inside a step id that flows then store, and both send a credential to a
// tenant-supplied address. They were written for MCP servers and lifted when
// the second source arrived, rather than copied: two of these would drift the
// first time one was edited, and the drift would be invisible — an id rule that
// diverged would only show up as one feature accepting a name the other's
// palette cannot render.
//
// requireStepSourceAdmin (httprunners.go) is the authorization half of the same
// idea and predates this file.

// maxStepSourceNameLen bounds a generated id. It is a component of every step
// id the source contributes, and those are read in a palette.
const maxStepSourceNameLen = 48

// slugStepSourceName derives a step-id-safe name from what a human typed.
//
// "MCP Test" → "mcp-test", "Kundregister (test)" → "kundregister-test". The
// point is that an admin never has to think about the id rule: they name the
// thing the way they would name anything else, and the identifier the flows
// hold is generated once and then frozen.
//
// Diacritics fold rather than vanish, so "Bokföring" is "bokforing" and not
// "bokf-ring" — the id is read by people, in a palette and in flow JSON.
// Anything left over collapses to single hyphens. A label with nothing
// slug-able in it at all (say, entirely CJK) yields "", and the caller falls
// back to a generic base rather than saving an empty id.
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
			// Underscore is legal in an id but a hyphen is what a typed name
			// wants; one separator keeps generated ids uniform.
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

// asciiFold maps the accented Latin letters a European org actually types onto
// their base letter. Deliberately a table and not a Unicode normalisation pass:
// the set that matters here is small and closed, and a table cannot pull a
// text-segmentation dependency into the daemon.
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

// validStepSourceName keeps a name usable inside a step id and readable in a
// palette.
//
// Stricter than it looks for one reason: the name goes into "mcp:<name>:<tool>"
// or "api:<name>:<operation>", so a colon in it would produce an id that splits
// two ways. Lowercase letters, digits, hyphen and underscore only — the same
// shape a runner name takes, so an admin does not have to learn two rules.
//
// Admins do not meet this rule any more: they type a label and the id is
// derived from it by slugStepSourceName. It still guards the API, which accepts
// an explicit name from a caller that wants to choose its own id.
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

// uniqueStepSourceName resolves a generated base to a name nothing in taken is
// using.
//
// Numbered rather than random: the id shows up in the palette and in flow JSON,
// so "mcp-test-2" is worth the lookup that an "mcp-test-x7f2" would save. The
// caller supplies the taken set, because what counts as taken differs — an MCP
// server also collides with the operator's instance-wide ones, a web API
// collides only with the org's own.
func uniqueStepSourceName(base, fallback string, taken map[string]bool, limit int) (string, error) {
	if base == "" {
		base = fallback
	}
	if !taken[base] {
		return base, nil
	}
	// One more than the per-tenant cap, so the loop cannot run out of numbers
	// before the caller runs out of rows it would allow.
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

// validStepSourceURL refuses what should never be dialed before the request is
// ever made.
//
// The dial guard is the real defence — it sees the resolved IP and so catches a
// hostname pointed inward — but a URL is worth refusing at SAVE time when it
// can be: an admin who typos gets told immediately rather than watching a
// source sit permanently in error.
//
// Cleartext http is refused because the credential rides in a header. The one
// exception is a deployment that opted into private egress, which is how a
// developer reaches a service on their own laptop; on such a deployment the
// operator has already said the network is trusted.
//
// This is POLICY, and it is why engine/webapi has a laxer check of its own:
// that layer decides whether a URL can be assembled into a request at all, and
// must stay usable by a unit test dialing loopback. Deciding what an org may be
// pointed at is the daemon's job, and this is where it is decided.
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
