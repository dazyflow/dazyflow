// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package knowledge

import "strings"

// A document has to be cut up before it can be embedded: one vector for a
// forty-page handbook says only that the handbook is about the company, which
// answers no question anyone would ask it.
//
// The cut follows the document's own seams — paragraph, then line, then
// sentence, then word — because a chunk that ends mid-sentence embeds as
// something slightly other than what it says. Consecutive chunks overlap, so a
// fact that straddles a seam survives whole in at least one of them.
const (
	defaultChunkSize = 1200
	defaultOverlap   = 150
	minChunkSize     = 100
	maxChunkSize     = 8000
)

// splitText cuts text into overlapping chunks of at most size characters.
func splitText(text string, size, overlap int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if size < minChunkSize {
		size = minChunkSize
	}
	if size > maxChunkSize {
		size = maxChunkSize
	}
	// Overlap has to leave the window moving, or the loop never advances.
	if overlap < 0 {
		overlap = 0
	}
	if overlap >= size {
		overlap = size / 4
	}

	runes := []rune(text)
	if len(runes) <= size {
		return []string{text}
	}

	var out []string
	for start := 0; start < len(runes); {
		end := start + size
		if end >= len(runes) {
			if piece := strings.TrimSpace(string(runes[start:])); piece != "" {
				out = append(out, piece)
			}
			break
		}
		cut := seam(runes, start, end)
		if piece := strings.TrimSpace(string(runes[start:cut])); piece != "" {
			out = append(out, piece)
		}
		next := cut - overlap
		if next <= start {
			next = cut // a seam that gave no ground; take the whole window
		}
		start = next
	}
	return out
}

// seam finds the last natural break in the back third of the window, so a
// chunk ends where the text does rather than mid-word. Nothing to find means
// the hard cut stands — text without a space in 1200 characters is a blob,
// and cutting it anywhere is equally arbitrary.
func seam(runes []rune, start, end int) int {
	floor := start + (end-start)*2/3
	for _, sep := range []string{"\n\n", "\n", ". ", ".", " "} {
		if at := lastIndexRunes(runes, start, end, sep); at > floor {
			return at + len([]rune(sep))
		}
	}
	return end
}

func lastIndexRunes(runes []rune, start, end int, sep string) int {
	s := []rune(sep)
	for i := end - len(s); i >= start; i-- {
		if matchAt(runes, i, s) {
			return i
		}
	}
	return -1
}

func matchAt(runes []rune, at int, sep []rune) bool {
	if at+len(sep) > len(runes) {
		return false
	}
	for i, r := range sep {
		if runes[at+i] != r {
			return false
		}
	}
	return true
}
