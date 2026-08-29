// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package value

import (
	"context"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func runEmail(t *testing.T, params map[string]any, input map[string]core.Ref) core.Result {
	t.Helper()
	res, err := executeEmail(context.Background(), core.Job{ID: "j1", Params: params, Input: input}, nil)
	if err != nil {
		t.Fatalf("executeEmail: %v", err)
	}
	return res
}

func TestEmail_SplitsAPlainAddress(t *testing.T) {
	res := runEmail(t, map[string]any{"email": "ada@acme.com"}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	if got := res.Output["out"].Inline; got != "ada@acme.com" {
		t.Errorf("out = %v, want ada@acme.com", got)
	}
	if got := res.Output["local"].Inline; got != "ada" {
		t.Errorf("local = %v, want ada", got)
	}
	if got := res.Output["domain"].Inline; got != "acme.com" {
		t.Errorf("domain = %v, want acme.com", got)
	}
	// Present but empty, so a template referencing it renders nothing rather
	// than failing on a missing pin.
	if got := res.Output["name"].Inline; got != "" {
		t.Errorf("name = %q, want empty", got)
	}
}

func TestEmail_LowercasesTheDomainButNotTheLocalPart(t *testing.T) {
	// The domain is case-insensitive by spec; a local part is the receiving
	// server's business, and folding it can turn a working address into a
	// bounce.
	res := runEmail(t, map[string]any{"email": "Ada.Lovelace@Acme.COM"}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	if got := res.Output["out"].Inline; got != "Ada.Lovelace@acme.com" {
		t.Errorf("out = %v, want Ada.Lovelace@acme.com", got)
	}
	if got := res.Output["local"].Inline; got != "Ada.Lovelace" {
		t.Errorf("local = %v, want Ada.Lovelace", got)
	}
}

func TestEmail_TakesTheAddressOutOfDisplayNameForm(t *testing.T) {
	res := runEmail(t, map[string]any{"email": "Ada Lovelace <ada@acme.com>"}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	// 'out' must be the bare address: it feeds a step that sends mail, and the
	// whole point is that it doesn't have to re-parse the string.
	if got := res.Output["out"].Inline; got != "ada@acme.com" {
		t.Errorf("out = %v, want ada@acme.com", got)
	}
	if got := res.Output["name"].Inline; got != "Ada Lovelace" {
		t.Errorf("name = %v, want Ada Lovelace", got)
	}
}

func TestEmail_QuotedLocalPartSurvives(t *testing.T) {
	// The case a regex gets wrong: legal per RFC 5322, and the last '@' is the
	// separator, not the first.
	res := runEmail(t, map[string]any{"email": `"ada@home"@acme.com`}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	if got := res.Output["domain"].Inline; got != "acme.com" {
		t.Errorf("domain = %v, want acme.com", got)
	}
}

func TestEmail_RejectsAMalformedAddress(t *testing.T) {
	for _, in := range []string{"ada", "ada@", "@acme.com", "ada acme.com"} {
		res := runEmail(t, map[string]any{"email": in}, nil)
		if res.Status == core.StatusOK {
			t.Errorf("%q was accepted, want a bad_param failure", in)
		}
	}
}

func TestEmail_RejectsADotlessDomain(t *testing.T) {
	// Legal in the RFC (an intranet host), essentially always a typo in a flow
	// about to send mail. The message has to say which, or it reads as a bug.
	res := runEmail(t, map[string]any{"email": "ada@acme"}, nil)
	if res.Status == core.StatusOK {
		t.Fatalf("ada@acme was accepted, want a bad_param failure")
	}
	if msg := res.Error.Message; !strings.Contains(msg, "no dot") {
		t.Errorf("message = %q, want it to explain the missing dot", msg)
	}
}

func TestEmail_RejectsAListOfAddresses(t *testing.T) {
	// This drop holds ONE address; a list silently taking the first would be
	// worse than refusing it.
	res := runEmail(t, map[string]any{"email": "ada@acme.com, bob@acme.com"}, nil)
	if res.Status == core.StatusOK {
		t.Fatalf("a list was accepted, want a bad_param failure")
	}
}

func TestEmail_WiredInputWinsOverTheParam(t *testing.T) {
	res := runEmail(t,
		map[string]any{"email": "typed@acme.com"},
		map[string]core.Ref{"email": {MIME: "text/plain", Inline: "wired@acme.com"}},
	)
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	if got := res.Output["out"].Inline; got != "wired@acme.com" {
		t.Errorf("out = %v, want the wired address to win", got)
	}
}

func TestEmail_MissingAddressSaysWhatToDo(t *testing.T) {
	res := runEmail(t, map[string]any{}, nil)
	if res.Status == core.StatusOK {
		t.Fatalf("an empty address was accepted")
	}
	if msg := res.Error.Message; !strings.Contains(msg, "connect") {
		t.Errorf("message = %q, want it to name both ways of supplying the address", msg)
	}
}

func TestEmail_TrimsSurroundingWhitespace(t *testing.T) {
	// A value pasted from a spreadsheet cell or a CSV column usually arrives
	// with something clinging to it.
	res := runEmail(t, map[string]any{"email": "  ada@acme.com\n"}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	if got := res.Output["out"].Inline; got != "ada@acme.com" {
		t.Errorf("out = %v, want ada@acme.com", got)
	}
}
