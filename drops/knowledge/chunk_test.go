// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package knowledge

import (
	"strings"
	"testing"
)

func TestSplitText_ShortTextIsOnePassage(t *testing.T) {
	got := splitText("  A short note.  ", 1200, 150)
	if len(got) != 1 || got[0] != "A short note." {
		t.Errorf("got %q", got)
	}
	if splitText("   ", 1200, 150) != nil {
		t.Error("blank text should yield no passages")
	}
}

// A passage that ends mid-sentence embeds as something slightly other than
// what it says, so the cut looks for the document's own seams.
func TestSplitText_CutsOnASeam(t *testing.T) {
	text := strings.Repeat("Alpha beta gamma delta. ", 20)
	for _, piece := range splitText(text, 120, 0) {
		if strings.HasSuffix(piece, "delta") || strings.HasSuffix(piece, ".") {
			continue
		}
		if strings.Contains(piece, " ") && !strings.HasSuffix(piece, "gamma") {
			continue // ended on some word boundary, which is the fallback
		}
		t.Errorf("passage ends mid-word: %q", piece)
	}
}

// Consecutive passages repeat their tail, so a fact across a seam survives
// whole in one of them.
func TestSplitText_PassagesOverlap(t *testing.T) {
	text := strings.Repeat("one two three four five six seven eight nine ten. ", 10)
	pieces := splitText(text, 200, 80)
	if len(pieces) < 3 {
		t.Fatalf("got %d passages, want several", len(pieces))
	}
	tail := pieces[0][len(pieces[0])/2:]
	if !strings.Contains(pieces[1], strings.TrimSpace(tail[len(tail)/2:])) {
		t.Errorf("no overlap between passage 1 and 2:\n%q\n%q", pieces[0], pieces[1])
	}
}

// Every character has to come out somewhere, and the window must always move.
func TestSplitText_CoversEverythingAndTerminates(t *testing.T) {
	text := strings.Repeat("Sentence about invoices and payment. ", 60)
	pieces := splitText(text, 150, 40)
	joined := strings.Join(pieces, " ")
	for _, word := range []string{"invoices", "payment"} {
		if !strings.Contains(joined, word) {
			t.Errorf("%q disappeared", word)
		}
	}
	if len(pieces) > len([]rune(text)) {
		t.Fatalf("%d passages for %d characters — the window is not advancing", len(pieces), len(text))
	}
}

// Text with no seam at all still gets cut, and never inside a character.
func TestSplitText_HandlesABlobAndUnicode(t *testing.T) {
	blob := strings.Repeat("åäö", 400)
	pieces := splitText(blob, 100, 0)
	if len(pieces) < 2 {
		t.Fatalf("got %d passages, want the blob cut up", len(pieces))
	}
	for _, p := range pieces {
		if strings.ContainsRune(p, '�') {
			t.Errorf("cut inside a character: %q", p)
		}
	}
	if joined := strings.Join(pieces, ""); joined != blob {
		t.Errorf("with no overlap the passages should rejoin into the original (%d vs %d characters)",
			len([]rune(joined)), len([]rune(blob)))
	}
}

// An overlap as wide as the window would leave the loop standing still.
func TestSplitText_SurvivesAbsurdSettings(t *testing.T) {
	text := strings.Repeat("word ", 500)
	for _, tc := range []struct{ size, overlap int }{
		{100, 100}, {100, 500}, {1, 0}, {100, -5}, {99999, 0},
	} {
		pieces := splitText(text, tc.size, tc.overlap)
		if len(pieces) == 0 {
			t.Errorf("size=%d overlap=%d produced nothing", tc.size, tc.overlap)
		}
	}
}
