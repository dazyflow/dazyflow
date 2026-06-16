package drive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/textproto"
	"net/url"
	"path"
	"strings"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/mimetype"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	"git.sr.ht/~klahr/hazyflow/drops/internal/sandbox"
	"git.sr.ht/~klahr/hazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "drive_upload",
			Version:     "1.0",
			Label:       "Google Drive",
			Subtitle:    "Upload file",
			Summary:     "Upload a workspace file to Google Drive.",
			Description: "Upload a workspace file to Google Drive. Wire a file in (e.g. from Download file, an export step, or HTTP download), or give a workspace path. Optionally drop it into a specific folder. Returns the new file's id and a shareable link.",
			Integration: "Google Drive",
			Category:    "network",
			Icon:        "upload",
			BrandLogo:   "/brands/google-drive.svg",
			Color:       "#1FA463",
			Provider:    "internal",
			Tags:        []string{"drive", "google", "upload", "file"},
			Examples: []core.ParamsExample{
				{Title: "Upload into a folder", Params: json.RawMessage(`{"account":"default","path":"workspace://reports/report.pdf","folder_id":"REPLACE_WITH_FOLDER_ID"}`)},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "google", Note: "Google OAuth — drive.file scope."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				// A wired file (e.g. from drive_download / file_write) overrides
				// the 'path' param.
				{Port: "in", Label: "File"},
			},
			Outputs: []core.Port{
				{Port: "file_id", Label: "File ID", MIME: []string{"text/plain"}},
				{Port: "web_view_link", Label: "Link", MIME: []string{"text/plain"}},
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","default":"default"},
					"path":{"type":"string","title":"File to upload","format":"workspace-path","description":"The workspace file to send (or wire the File input). scratch:// supported."},
					"name":{"type":"string","title":"Name in Drive","description":"What to call the file in Drive. Defaults to the source file's own name."},
					"folder_id":{"type":"string","title":"Into folder","description":"Folder id to upload into. Leave blank for the account's My Drive root."},
					"mime_type":{"type":"string","title":"Content type","x_advanced":true,"description":"Content type to store. Defaults to a guess from the file extension."},
					"timeout_ms":{"type":"integer","default":120000,"minimum":1,"description":"Hard deadline for the upload, in milliseconds."}
				}
			}`),
			Idempotent: false,
		},
		Execute: executeUpload,
	})
}

func executeUpload(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	srcPath := uploadSrcPath(job)
	if strings.TrimSpace(srcPath) == "" {
		return params.Err(job, "bad_param", "path is required (params.path or the 'in' input)"), nil
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}

	// Read the source file from the sandbox (workspace or scratch://).
	root, rel, err := sandbox.OpenRoot(job, srcPath)
	if err != nil {
		if sandbox.IsEscape(err) {
			return params.Err(job, "sandbox_escape", "path escapes its sandbox root"), nil
		}
		return params.Err(job, "no_sandbox", err.Error()), nil
	}
	defer root.Close()
	f, err := root.Open(rel)
	if err != nil {
		return params.Err(job, "io", fmt.Sprintf("open %q: %v", srcPath, err)), nil
	}
	defer f.Close()
	content, err := io.ReadAll(io.LimitReader(f, maxResponseBytes+1))
	if err != nil {
		return params.Err(job, "io", fmt.Sprintf("read %q: %v", srcPath, err)), nil
	}
	if int64(len(content)) > maxResponseBytes {
		return params.Err(job, "too_large", fmt.Sprintf("file exceeds the %d byte upload limit", maxResponseBytes)), nil
	}

	name := strings.TrimSpace(params.StringDefault(job.Params, "name", ""))
	if name == "" {
		name = path.Base(rel)
	}
	mime := params.StringDefault(job.Params, "mime_type", "")
	if mime == "" {
		mime = mimetype.GuessByExt(rel)
	}

	metadata := map[string]any{"name": name}
	if folder := strings.TrimSpace(params.StringDefault(job.Params, "folder_id", "")); folder != "" {
		metadata["parents"] = []string{folder}
	}

	body, contentType, err := buildRelatedBody(metadata, mime, content)
	if err != nil {
		return params.Err(job, "drive_error", err.Error()), nil
	}

	q := url.Values{}
	q.Set("uploadType", "multipart")
	q.Set("fields", "id,name,webViewLink")
	endpoint := uploadBaseURL(job) + "/files?" + q.Encode()
	status, respBody, err := googleDo(ctx, "POST", endpoint, token, contentType, body, params.IntDefault(job.Params, "timeout_ms", 120000))
	if err != nil {
		return params.Err(job, "drive_error", err.Error()), nil
	}
	if status < 200 || status >= 300 {
		return params.Err(job, "drive_error", driveErr(respBody)), nil
	}

	var created driveFile
	if err := json.Unmarshal(respBody, &created); err != nil {
		return params.Err(job, "drive_error", fmt.Sprintf("files.create decode: %v", err)), nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"file_id":       {MIME: "text/plain", Inline: created.ID},
			"web_view_link": {MIME: "text/plain", Inline: created.WebViewLink},
			"meta": {MIME: "application/json", Inline: map[string]any{
				"file_id":       created.ID,
				"name":          created.Name,
				"web_view_link": created.WebViewLink,
				"bytes":         len(content),
				"mime":          mime,
			}},
		},
	}, nil
}

// uploadSrcPath takes the file path from the 'in' input ref (so an upstream
// drive_download / file_write can feed it) or params.path. Mirrors
// http_upload's resolver.
func uploadSrcPath(job core.Job) string {
	if in, ok := job.Input["in"]; ok && in.Ref != "" {
		return in.Ref
	}
	return params.StringDefault(job.Params, "path", "")
}

// buildRelatedBody assembles a Drive multipart/related upload body: a JSON
// metadata part followed by the raw media part, per the files.create
// uploadType=multipart protocol. Returns the body and the Content-Type header
// (which carries the generated boundary).
func buildRelatedBody(metadata map[string]any, mime string, content []byte) ([]byte, string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	metaHeader := textproto.MIMEHeader{}
	metaHeader.Set("Content-Type", "application/json; charset=UTF-8")
	mp, err := mw.CreatePart(metaHeader)
	if err != nil {
		return nil, "", err
	}
	if err := json.NewEncoder(mp).Encode(metadata); err != nil {
		return nil, "", err
	}

	mediaHeader := textproto.MIMEHeader{}
	if mime != "" {
		mediaHeader.Set("Content-Type", mime)
	}
	cp, err := mw.CreatePart(mediaHeader)
	if err != nil {
		return nil, "", err
	}
	if _, err := cp.Write(content); err != nil {
		return nil, "", err
	}
	if err := mw.Close(); err != nil {
		return nil, "", err
	}
	// Google expects multipart/related, not the form-data type multipart.Writer
	// defaults to; reuse its boundary.
	return buf.Bytes(), "multipart/related; boundary=" + mw.Boundary(), nil
}
