// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/drops/internal/sandbox"
	"github.com/dazyflow/dazyflow/engine"
)

// maxAttachmentBytes caps the total a single message may yield, so one mail
// with a video in it can't fill the run's scratch area.
const maxAttachmentBytes = 32 << 20 // 32 MiB

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "gmail_get_attachments",
			Version:     "1.0",
			Label:       "Gmail",
			Subtitle:    "Download attachments",
			Summary:     "Save the files attached to an email so a later step can file them.",
			Description: "Take the files attached to an email and save them, ready to hand to a step that files them somewhere — Upload to Drive, Write file, or an email of your own. Connect Search emails' Matching emails into a For each and put this step in the loop body with Email = the row's id. Use 'Only these types' to take just the PDFs and ignore signature images. The First file output is the one to connect when each email carries a single document; the Files list carries them all.",
			Integration: "Gmail",
			Category:    "network",
			Icon:        "paperclip",
			BrandLogo:   "/brands/gmail.svg",
			Color:       "#D14836",
			Provider:    "internal",
			Tags:        []string{"gmail", "email", "attachment", "file", "download", "invoice", "receipt"},
			Examples: []core.ParamsExample{
				{
					Title:  "Take the PDF off each invoice email (inside For each)",
					Params: json.RawMessage(`{"account":"default","id":"${item.id}","only":"pdf"}`),
					Notes:  "Connect First file into Upload to Drive's File input to file it.",
				},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "google", Note: "Google OAuth — gmail.readonly scope."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				// Same shape as Read email: a single id, or Search emails'
				// list wired straight in (then the FIRST match is used).
				{Port: "id", Label: "Email", MIME: []string{"text/plain", "application/json"}},
			},
			Outputs: []core.Port{
				{Port: "first", Label: "First file"},
				{Port: "files", Label: "Files", MIME: []string{"application/json"}},
				{Port: "count", Label: "How many", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"account":{"type":"string","default":"default"},
					"token":{"type":"string","description":"Raw access token; overrides 'account'."},
					"id":{"type":"string","title":"Email","description":"Which email to take the files from. Overridden by the Email input when connected."},
					"only":{"type":"string","title":"Only these types","examples":["pdf","pdf,png"],"description":"Comma-separated file extensions to keep, e.g. \"pdf\". Leave blank to take every attachment. Inline signature images are always skipped."},
					"folder":{"type":"string","title":"Save into","format":"workspace-path","description":"Workspace folder to save the files in, so they outlive the run. Leave blank to keep them in the run's scratch area — fine when a later step files them somewhere else."},
					"timeout_ms":{"type":"integer","default":60000,"minimum":1,"description":"Hard deadline for each download, in milliseconds."}
				},
				"required":["id"]
			}`),
			Idempotent: true,
		},
		Execute: executeGmailGetAttachments,
	})
}

// attachmentPart is one candidate file found while walking a message payload.
type attachmentPart struct {
	Filename     string
	MIME         string
	AttachmentID string
	Size         int64
}

func executeGmailGetAttachments(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	id, ok := resolveMessageID(job)
	if !ok {
		return params.Err(job, "bad_input", "input port 'id' must be a message ID or a list of matches"), nil
	}
	if id == "" {
		return params.Err(job, "bad_param", "'id' is required"), nil
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}
	timeout := params.IntDefault(job.Params, "timeout_ms", 60000)

	q := url.Values{}
	q.Set("format", "full")
	endpoint := baseURL(job) + "/users/me/messages/" + url.PathEscape(id) + "?" + q.Encode()
	status, body, err := gmailDo(ctx, "GET", endpoint, token, "", nil, timeout)
	if err != nil {
		return params.Err(job, "gmail_http_error", err.Error()), nil
	}
	if status < 200 || status >= 300 {
		return params.Err(job, "gmail_error", extractGmailError(body)), nil
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return params.Err(job, "gmail_error", "could not parse message: "+err.Error()), nil
	}
	payload, _ := raw["payload"].(map[string]any)
	wanted := keepExtensions(params.StringDefault(job.Params, "only", ""))
	parts := collectAttachments(payload, wanted)

	folder := strings.TrimSpace(params.StringDefault(job.Params, "folder", ""))
	rows := make([]map[string]any, 0, len(parts))
	var first core.Ref
	var total int64

	for i, p := range parts {
		dest := attachmentDest(folder, id, i, p.Filename)
		root, rel, serr := sandbox.OpenRoot(job, dest)
		if serr != nil {
			if sandbox.IsEscape(serr) {
				return params.Err(job, "sandbox_escape", fmt.Sprintf("%q escapes its sandbox root", dest)), nil
			}
			return params.Err(job, "no_sandbox", serr.Error()), nil
		}

		data, derr := fetchAttachment(ctx, job, token, id, p.AttachmentID, timeout)
		if derr != nil {
			root.Close()
			return params.Err(job, "gmail_error", fmt.Sprintf("%s: %v", p.Filename, derr)), nil
		}
		total += int64(len(data))
		if total > maxAttachmentBytes {
			root.Close()
			return params.Err(job, "too_large", fmt.Sprintf("the attachments on this email exceed the %d MiB limit", maxAttachmentBytes>>20)), nil
		}
		if folder != "" {
			if merr := root.MkdirAll(path.Dir(rel), 0o755); merr != nil && !sandbox.IsEscape(merr) {
				root.Close()
				return params.Err(job, "io", fmt.Sprintf("create folder for %q: %v", dest, merr)), nil
			}
		}
		f, cerr := root.Create(rel)
		if cerr != nil {
			root.Close()
			if sandbox.IsEscape(cerr) {
				return params.Err(job, "sandbox_escape", fmt.Sprintf("%q escapes its sandbox root", dest)), nil
			}
			return params.Err(job, "io", fmt.Sprintf("save %q: %v", dest, cerr)), nil
		}
		_, werr := f.Write(data)
		f.Close()
		root.Close()
		if werr != nil {
			return params.Err(job, "io", fmt.Sprintf("save %q: %v", dest, werr)), nil
		}

		rows = append(rows, map[string]any{
			"name": p.Filename,
			"mime": p.MIME,
			"size": len(data),
			"path": dest,
		})
		if i == 0 {
			first = core.Ref{MIME: p.MIME, Ref: dest}
		}
	}

	out := map[string]core.Ref{
		"files": {MIME: "application/json", Inline: rows, Headers: []string{"name", "mime", "size", "path"}},
		"count": {MIME: "text/plain", Inline: fmt.Sprint(len(rows))},
	}
	if len(rows) > 0 {
		out["first"] = first
	}
	return core.Result{JobID: job.ID, Status: core.StatusOK, Output: out}, nil
}

// fetchAttachment pulls one attachment's bytes. Gmail hands them back
// base64url-encoded inside a JSON envelope rather than as a raw body.
func fetchAttachment(ctx context.Context, job core.Job, token, msgID, attID string, timeoutMS int) ([]byte, error) {
	endpoint := baseURL(job) + "/users/me/messages/" + url.PathEscape(msgID) +
		"/attachments/" + url.PathEscape(attID)
	status, body, err := gmailDo(ctx, "GET", endpoint, token, "", nil, timeoutMS)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("%s", extractGmailError(body))
	}
	var env struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("could not parse attachment: %w", err)
	}
	dec, err := base64.RawURLEncoding.DecodeString(stripB64Pad(env.Data))
	if err != nil {
		return nil, fmt.Errorf("could not decode attachment: %w", err)
	}
	return dec, nil
}

// collectAttachments walks the MIME tree for parts that are real attachments:
// a filename and an attachmentId. Inline parts (a signature logo, an embedded
// image) carry a Content-ID and no filename, so they fall out naturally.
func collectAttachments(payload map[string]any, wanted map[string]bool) []attachmentPart {
	var out []attachmentPart
	var walk func(m map[string]any)
	walk = func(m map[string]any) {
		if m == nil {
			return
		}
		name := strings.TrimSpace(str(m["filename"]))
		if body, ok := m["body"].(map[string]any); ok && name != "" {
			if attID := str(body["attachmentId"]); attID != "" && keepFile(name, wanted) {
				size := int64(0)
				if f, ok := body["size"].(float64); ok {
					size = int64(f)
				}
				out = append(out, attachmentPart{
					Filename:     name,
					MIME:         str(m["mimeType"]),
					AttachmentID: attID,
					Size:         size,
				})
			}
		}
		parts, _ := m["parts"].([]any)
		for _, p := range parts {
			if sub, ok := p.(map[string]any); ok {
				walk(sub)
			}
		}
	}
	walk(payload)
	return out
}

// keepExtensions parses the "only" setting into a lowercase extension set.
// An empty setting means "keep everything".
func keepExtensions(s string) map[string]bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	out := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		ext := strings.ToLower(strings.TrimSpace(part))
		ext = strings.TrimPrefix(ext, "*")
		ext = strings.TrimPrefix(ext, ".")
		if ext != "" {
			out[ext] = true
		}
	}
	return out
}

func keepFile(name string, wanted map[string]bool) bool {
	if len(wanted) == 0 {
		return true
	}
	return wanted[strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))]
}

// attachmentDest names the saved file. The message id prefixes the name so two
// emails whose invoice is called "invoice.pdf" don't overwrite each other, and
// the index disambiguates within one message.
func attachmentDest(folder, msgID string, idx int, filename string) string {
	safe := sanitizeFilename(filename)
	name := fmt.Sprintf("%s-%d-%s", sanitizeFilename(msgID), idx+1, safe)
	if folder == "" {
		return sandbox.Scheme + name
	}
	return path.Join(strings.TrimSuffix(folder, "/"), name)
}

// sanitizeFilename reduces a sender-controlled filename to something safe to
// join onto a path: no separators, no traversal, no leading dot.
func sanitizeFilename(name string) string {
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '.' || r == '-' || r == '_':
			return r
		}
		return '-'
	}, path.Base(name))
	name = strings.TrimLeft(name, ".-")
	if name == "" {
		return "attachment"
	}
	if len(name) > 120 {
		name = name[:120]
	}
	return name
}
