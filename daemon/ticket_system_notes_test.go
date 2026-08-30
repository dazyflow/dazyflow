// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/daemon/support"
)

// System notes are composed here in English and stored in Body — right for an
// API reader, an email digest, or an agent grepping the table, and wrong for
// the web UI, which is translated and was rendering "The customer closed this
// ticket." in the middle of a Swedish thread.
//
// The fix is a code alongside the prose, so the UI can say the same thing in
// the reader's language. That only works if every note actually carries one,
// and a note is a single line in a handler that nothing forces to be complete
// — which is what these cover.

func ticketNoteHarness(t *testing.T) (*gatewayHarness, time.Time) {
	t.Helper()
	h := newGatewayHarness(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	h.gw.Tickets = support.NewMemTicketStore()
	h.gw.supportNow = func() time.Time { return now }
	return h, now
}

func TestSystemNote_CarriesACodeForTheUIToTranslate(t *testing.T) {
	h, now := ticketNoteHarness(t)
	ctx := context.Background()
	tk := core.Ticket{
		ID: "tk1", Tenant: "t", Workspace: "ws", CreatedBy: "alice",
		Subject: "s", Status: core.TicketAwaitingSupport, CreatedAt: now, UpdatedAt: now,
	}
	if err := h.gw.Tickets.Create(ctx, tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := h.gw.appendSystemNote(ctx, "tk1", core.NoteCustomerClosed,
		"The customer closed this ticket.", now); err != nil {
		t.Fatalf("append: %v", err)
	}
	msgs, err := h.gw.Tickets.ListMessages(ctx, "tk1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	m := msgs[0]
	if m.SystemCode != core.NoteCustomerClosed {
		t.Errorf("SystemCode = %q, want %q", m.SystemCode, core.NoteCustomerClosed)
	}
	// The English survives: it is what an API reader and an older UI get.
	if m.Body != "The customer closed this ticket." {
		t.Errorf("Body = %q, want the English prose kept as the fallback", m.Body)
	}
	if m.AuthorKind != core.AuthorSystem || m.Author != "" {
		t.Errorf("a system note must have no author: %+v", m)
	}
}

func TestMarkedNote_IsOneCodePerStatus(t *testing.T) {
	// One whole sentence per status rather than a code plus an interpolated
	// status label: Swedish inflects around the insertion point, and a flat
	// code is greppable from either side of the stack.
	for _, s := range []core.TicketStatus{
		core.TicketOpen, core.TicketAwaitingUser, core.TicketAwaitingSupport,
		core.TicketResolved, core.TicketClosed,
	} {
		got := core.MarkedNote(s)
		if want := core.SystemNote("marked_" + s); got != want {
			t.Errorf("MarkedNote(%q) = %q, want %q", s, got, want)
		}
	}
}

func TestSystemNote_EmptyBodyStillWritesNothing(t *testing.T) {
	// The reopen path passes an empty note for "handed back to support", which
	// needs no narration. A code must not resurrect the skipped row.
	h, now := ticketNoteHarness(t)
	ctx := context.Background()
	if err := h.gw.Tickets.Create(ctx, core.Ticket{
		ID: "tk1", Tenant: "t", Workspace: "ws", CreatedBy: "alice",
		Subject: "s", Status: core.TicketAwaitingSupport, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := h.gw.appendSystemNote(ctx, "tk1", core.SystemNote(""), "", now); err != nil {
		t.Fatalf("append: %v", err)
	}
	msgs, _ := h.gw.Tickets.ListMessages(ctx, "tk1")
	if len(msgs) != 0 {
		t.Errorf("wrote %d message(s) for an empty note, want 0", len(msgs))
	}
}

// TestSystemNote_NoHandlerBypassesTheHelper is the one that keeps this fixed.
//
// appendSystemNote is the only place that pairs a code with its prose. A new
// handler calling appendTicketMessage with AuthorSystem instead would compile,
// pass review, store a perfectly good English sentence — and be untranslatable
// in exactly the way this whole change exists to end, silently.
func TestSystemNote_NoHandlerBypassesTheHelper(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	var offenders []string
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		checked++
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "appendTicketMessage" {
				return true
			}
			for _, arg := range call.Args {
				a, ok := arg.(*ast.SelectorExpr)
				if ok && a.Sel.Name == "AuthorSystem" {
					offenders = append(offenders,
						name+":"+fset.Position(call.Pos()).String())
				}
			}
			return true
		})
	}
	if checked < 10 {
		t.Fatalf("only parsed %d files — the walk is not finding the package", checked)
	}
	if len(offenders) > 0 {
		t.Errorf("these write a system note without a code, so the UI cannot "+
			"translate it — use appendSystemNote instead: %v", offenders)
	}
}
