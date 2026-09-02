// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mailbox

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/mailfiles"
	"github.com/dazyflow/dazyflow/drops/internal/mailmsg"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/drops/internal/sandbox"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "imap_get_attachments",
			Version:     "1.0",
			Label:       "Mailbox",
			Subtitle:    "Download attachments",
			Summary:     "Save the files attached to an email so a later step can file them.",
			Description: "Take the files attached to an email and save them, ready to hand to a step that files them somewhere — Upload to Drive, Write file, or an email of your own. Connect Search emails' Matching emails into a For each and put this step in the loop body with Email = the row's id. Use 'Only these types' to take just the PDFs and ignore signature images. The First file output is the one to connect when each email carries a single document; the Files list carries them all.",
			Integration: integration,
			Category:    "network",
			Icon:        "paperclip",
			Color:       "#0ea5e9",
			Provider:    "internal",
			Tags:        []string{"imap", "email", "mailbox", "attachment", "file", "download", "invoice", "receipt"},
			Examples: []core.ParamsExample{
				{
					Title:  "Take the PDF off each invoice email (inside For each)",
					Params: json.RawMessage(`{"id":"${item.id}","only":"pdf"}`),
					Notes:  "Connect First file into Upload to Drive's File input to file it.",
				},
			},
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			ConnectionFields: connectionFields(),
			Inputs: []core.Port{
				// Same shape as Read email: a single id, or Search emails'
				// list wired straight in (then the FIRST match is used).
				{Port: "id", Label: "Email", MIME: []string{"text/plain", "application/json"}},
			},
			Outputs: []core.Port{
				// Untyped on purpose: the file's MIME is whatever was attached,
				// so the pin carries it per-run rather than declaring one.
				{Port: "first", Label: "First file"},
				{Port: "files", Label: "Files", MIME: []string{"application/json"}},
				{Port: "count", Label: "How many", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"folder":{"type":"string","title":"Folder","description":"Which folder the email is in, e.g. \"INBOX\". Leave blank to use the folder set on the Mailbox page."},
					"id":{"type":"string","title":"Email","description":"Which email to take the files from — the id from a Search emails match. Overridden by the 'Email' input when connected."},
					"only":{"type":"string","title":"Only these types","examples":["pdf","pdf,png"],"description":"Comma-separated file extensions to keep, e.g. \"pdf\". Leave blank to take every attachment. Inline signature images are always skipped."},
					"save_into":{"type":"string","title":"Save into","format":"workspace-path","description":"Workspace folder to save the files in, so they outlive the run. Leave blank to keep them in the run's scratch area — fine when a later step files them somewhere else."},
					"timeout_ms":{"type":"integer","default":60000,"minimum":1,"description":"Hard deadline for the whole download, in milliseconds."}
				},
				"required":["id"]
			}`),
			Idempotent: true,
		},
		Execute: executeIMAPGetAttachments,
	})
}

func executeIMAPGetAttachments(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	uid, bad := resolveUID(job)
	if bad != nil {
		return *bad, nil
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(params.TimeoutMS(job, 60000))*time.Millisecond)
	defer cancel()

	client, _, fail := openMailbox(ctx, job, true)
	if fail != nil {
		return *fail, nil
	}
	defer client.Close()

	// BODYSTRUCTURE first, bytes second: the tree tells us which parts are
	// files and how big each one is, so an oversized mail is refused before a
	// single byte of it is downloaded. Gmail's version has to add the sizes up
	// as it goes and bail part-way through, having already paid for what it
	// fetched.
	buf, fetchFail := fetchOneUID(client, job, uid, &imap.FetchOptions{
		UID:           true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
	})
	if fetchFail != nil {
		return *fetchFail, nil
	}

	wanted := mailfiles.KeepExtensions(params.StringDefault(job.Params, "only", ""))
	files := attachmentsToTake(leafParts(buf.BodyStructure), wanted)

	if declared, over := overCap(files); over {
		return params.Err(job, "too_large", fmt.Sprintf("the attachments on this email come to %d MiB, over the %d MiB limit", declared>>20, mailfiles.MaxBytes>>20)), nil
	}

	if len(files) == 0 {
		return emitFiles(job, nil, core.Ref{}), nil
	}

	// One FETCH for every part: they all belong to the same message, so the
	// sections ride on a single command rather than a round trip each.
	sections := make([]*imap.FetchItemBodySection, 0, len(files))
	for _, f := range files {
		sections = append(sections, sectionFor(f))
	}
	partBuf, partFail := fetchOneUID(client, job, uid, &imap.FetchOptions{BodySection: sections})
	if partFail != nil {
		return *partFail, nil
	}

	saveInto := strings.TrimSpace(params.StringDefault(job.Params, "save_into", ""))
	rows := make([]map[string]any, 0, len(files))
	var first core.Ref
	var written int64

	for i, f := range files {
		data := decodePart(bytesForSection(partBuf.BodySection, f.path), f.leaf)
		// The declared sizes were a promise, not a measurement — a server can
		// be wrong, and the decode changes the length anyway — so the real
		// total is checked too.
		written += int64(len(data))
		if written > mailfiles.MaxBytes {
			return params.Err(job, "too_large", fmt.Sprintf("the attachments on this email exceed the %d MiB limit", mailfiles.MaxBytes>>20)), nil
		}

		name := attachmentName(f, i)
		dest := mailfiles.Dest(saveInto, fmt.Sprint(uid), i, name)
		root, rel, serr := sandbox.OpenRoot(job, dest)
		if serr != nil {
			if sandbox.IsEscape(serr) {
				return params.Err(job, "sandbox_escape", fmt.Sprintf("%q escapes its sandbox root", dest)), nil
			}
			return params.Err(job, "no_sandbox", serr.Error()), nil
		}
		if saveInto != "" {
			if merr := root.MkdirAll(path.Dir(rel), 0o755); merr != nil && !sandbox.IsEscape(merr) {
				root.Close()
				return params.Err(job, "io", fmt.Sprintf("create folder for %q: %v", dest, merr)), nil
			}
		}
		fh, cerr := root.Create(rel)
		if cerr != nil {
			root.Close()
			if sandbox.IsEscape(cerr) {
				return params.Err(job, "sandbox_escape", fmt.Sprintf("%q escapes its sandbox root", dest)), nil
			}
			return params.Err(job, "io", fmt.Sprintf("save %q: %v", dest, cerr)), nil
		}
		_, werr := fh.Write(data)
		fh.Close()
		root.Close()
		if werr != nil {
			return params.Err(job, "io", fmt.Sprintf("save %q: %v", dest, werr)), nil
		}

		mime := f.leaf.MediaType()
		rows = append(rows, map[string]any{
			"name": name,
			"mime": mime,
			"size": len(data),
			"path": dest,
		})
		if i == 0 {
			first = core.Ref{MIME: mime, Ref: dest}
		}
	}

	return emitFiles(job, rows, first), nil
}

// emitFiles builds the result. The "first" pin is omitted when nothing was
// saved, so a downstream step wired to it goes dormant instead of being handed
// an empty file — matching Gmail's Download attachments.
func emitFiles(job core.Job, rows []map[string]any, first core.Ref) core.Result {
	if rows == nil {
		rows = []map[string]any{}
	}
	out := map[string]core.Ref{
		"files": {MIME: "application/json", Inline: rows, Headers: []string{"name", "mime", "size", "path"}},
		"count": {MIME: "text/plain", Inline: fmt.Sprint(len(rows))},
	}
	if len(rows) > 0 {
		out["first"] = first
	}
	return core.Result{JobID: job.ID, Status: core.StatusOK, Output: out}
}

// overCap adds up what the server says the parts weigh and reports whether
// that is more than one message may yield. Its value is the ORDER: refusing
// here means an oversized mail costs nothing, where Gmail's version has to
// total the sizes as it downloads and bail part-way, having already paid for
// what it fetched.
//
// The sizes are the encoded lengths, so they over-state the files slightly
// (base64 adds a third). Erring toward refusing is the right direction for a
// cap that exists to protect the run's scratch area.
func overCap(files []part) (declared int64, over bool) {
	for _, f := range files {
		declared += int64(f.leaf.Size)
	}
	return declared, declared > mailfiles.MaxBytes
}

// attachmentsToTake narrows a message's parts to the real attachments passing
// the extension filter.
func attachmentsToTake(parts []part, wanted map[string]bool) []part {
	out := make([]part, 0, len(parts))
	for i, p := range parts {
		if !isAttachment(p.leaf) {
			continue
		}
		if !mailfiles.Keep(attachmentName(p, i), wanted) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// attachmentName is what to call the file. A part with no filename still needs
// one — some senders attach a PDF with nothing but a Content-Type — so the
// media type supplies the extension and the index keeps the names distinct.
func attachmentName(p part, idx int) string {
	if name := strings.TrimSpace(p.leaf.Filename()); name != "" {
		return name
	}
	return fmt.Sprintf("attachment-%d%s", idx+1, mailmsg.ExtForMIME(p.leaf.MediaType()))
}

// bytesForSection finds the fetched bytes for one part. A FETCH with several
// sections returns them in an unspecified order, so they're matched by the
// section path rather than by position — matching by index would silently
// write one attachment's bytes into another's file.
func bytesForSection(buffers []imapclient.FetchBodySectionBuffer, want []int) []byte {
	for _, b := range buffers {
		if b.Section != nil && samePath(b.Section.Part, want) {
			return b.Bytes
		}
	}
	return nil
}

func samePath(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
