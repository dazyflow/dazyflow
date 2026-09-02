// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mailbox

import (
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/dazyflow/dazyflow/drops/internal/mailfiles"
)

// bytesForSection must key on the section path, not the position in the
// response. One FETCH asks for every attachment's section at once and the
// order they come back in is not specified — a server is free to answer 2.1
// before 2 — so matching by index would write one attachment's bytes into
// another's file. Silently: both files exist, both are the right size, and the
// contents are swapped.
//
// This is a unit test rather than an assertion on a live fetch on purpose: the
// in-memory test server answers in request order, so an end-to-end test passes
// either way and proves nothing. (Verified by mutation — matching by index
// leaves the end-to-end suite green.)
func TestBytesForSection_MatchesOnPathNotPosition(t *testing.T) {
	buffers := []imapclient.FetchBodySectionBuffer{
		{Section: &imap.FetchItemBodySection{Part: []int{3}}, Bytes: []byte("CCC")},
		{Section: &imap.FetchItemBodySection{Part: []int{1}}, Bytes: []byte("AAA")},
		{Section: &imap.FetchItemBodySection{Part: []int{2, 1}}, Bytes: []byte("BBB")},
	}
	for _, tc := range []struct {
		path []int
		want string
	}{
		{path: []int{1}, want: "AAA"},
		{path: []int{2, 1}, want: "BBB"},
		{path: []int{3}, want: "CCC"},
	} {
		if got := string(bytesForSection(buffers, tc.path)); got != tc.want {
			t.Errorf("bytesForSection(%v) = %q, want %q", tc.path, got, tc.want)
		}
	}

	// A path nothing was fetched for yields nothing, rather than the first
	// buffer that happens to be lying around.
	if got := bytesForSection(buffers, []int{9}); got != nil {
		t.Errorf("unknown path returned %q, want nil", got)
	}
	// A near-miss must not match: 2 is not 2.1.
	if got := bytesForSection(buffers, []int{2}); got != nil {
		t.Errorf("path [2] matched the [2 1] buffer: %q", got)
	}
}

// isAttachment decides what counts as a file someone attached. The inline case
// is the one that matters in practice: a signature logo carries a filename
// like any attachment, and without honouring the disposition every invoice
// flow would file the sender's logo alongside the invoice.
func TestIsAttachment(t *testing.T) {
	withDisposition := func(value, filename string) *imap.BodyStructureSinglePart {
		return &imap.BodyStructureSinglePart{
			Type: "image", Subtype: "png",
			Extended: &imap.BodyStructureSinglePartExt{
				Disposition: &imap.BodyStructureDisposition{
					Value:  value,
					Params: map[string]string{"filename": filename},
				},
			},
		}
	}
	if !isAttachment(withDisposition("attachment", "invoice.pdf")) {
		t.Error("an explicit attachment disposition must count")
	}
	if isAttachment(withDisposition("inline", "signature-logo.png")) {
		t.Error("an inline part must be skipped even though it has a filename")
	}
	if isAttachment(withDisposition("INLINE", "logo.png")) {
		t.Error("the disposition is case-insensitive")
	}
	// No disposition header at all: some senders omit it, so a filename in the
	// Content-Type is taken as the intent.
	named := &imap.BodyStructureSinglePart{Type: "application", Subtype: "pdf", Params: map[string]string{"name": "invoice.pdf"}}
	if !isAttachment(named) {
		t.Error("a part named only in Content-Type should count as an attachment")
	}
	if isAttachment(&imap.BodyStructureSinglePart{Type: "text", Subtype: "plain"}) {
		t.Error("an unnamed text part is the message body, not an attachment")
	}
}

// decodeTransferEncoding is the layer between "what the wire carried" and
// "the file someone attached".
func TestDecodeTransferEncoding(t *testing.T) {
	// Base64 in a MIME part is line-wrapped, and those breaks are not payload.
	if got := string(decodeTransferEncoding([]byte("aGVsbG8g\r\nd29ybGQ="), "base64")); got != "hello world" {
		t.Errorf("wrapped base64 decoded to %q", got)
	}
	if got := string(decodeTransferEncoding([]byte("Fakturan =E4r betald"), "quoted-printable")); got != "Fakturan \xe4r betald" {
		t.Errorf("quoted-printable decoded to %q", got)
	}
	// 7bit/8bit/binary and a missing header all mean "already the bytes".
	for _, enc := range []string{"", "7bit", "8bit", "binary", "BINARY"} {
		if got := string(decodeTransferEncoding([]byte("plain"), enc)); got != "plain" {
			t.Errorf("encoding %q changed the bytes to %q", enc, got)
		}
	}
	// Undecodable input degrades to what we were given rather than to nothing:
	// a partly readable attachment beats an error on a step whose job is to
	// hand someone their file.
	if got := string(decodeTransferEncoding([]byte("!!!not base64!!!"), "base64")); got == "" {
		t.Error("broken base64 decoded to nothing, want the raw bytes back")
	}
}

// overCap is what stops one email with a video in it from filling the run's
// scratch area — and it runs BEFORE anything is downloaded, which is the whole
// point of reading BODYSTRUCTURE first. Exercised here with fabricated part
// sizes rather than a real 32 MiB message, so the boundary is actually tested
// instead of approximated by a fixture nobody wants to build.
func TestOverCap(t *testing.T) {
	parts := func(sizes ...uint32) []part {
		out := make([]part, 0, len(sizes))
		for _, sz := range sizes {
			out = append(out, part{leaf: &imap.BodyStructureSinglePart{Size: sz}})
		}
		return out
	}

	if _, over := overCap(nil); over {
		t.Error("no attachments is not over the cap")
	}
	if total, over := overCap(parts(1024, 2048)); over || total != 3072 {
		t.Errorf("small attachments: total %d, over %v", total, over)
	}
	// Exactly at the limit is allowed; one byte past it is not. A cap that
	// refuses its own boundary value rejects a file someone was told fits.
	if total, over := overCap(parts(mailfiles.MaxBytes)); over {
		t.Errorf("a message exactly at the %d-byte cap was refused (total %d)", int64(mailfiles.MaxBytes), total)
	}
	if _, over := overCap(parts(mailfiles.MaxBytes, 1)); !over {
		t.Error("one byte over the cap must be refused")
	}
	// The sum is what matters, not any single part: fifty near-limit files are
	// the realistic way to blow the budget.
	if _, over := overCap(parts(mailfiles.MaxBytes/2, mailfiles.MaxBytes/2, 1024)); !over {
		t.Error("the cap applies to the total across parts")
	}
}
