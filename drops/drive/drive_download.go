package drive

import (
	"context"
	"encoding/json"
	"net/url"
	"path"
	"slices"
	"strings"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	"git.sr.ht/~klahr/hazyflow/drops/internal/sandbox"
	"git.sr.ht/~klahr/hazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "drive_download",
			Version:     "1.0",
			Label:       "Google Drive",
			Subtitle:    "Download file",
			Summary:     "Download a Google Drive file into the workspace.",
			Description: "Download a Google Drive file's contents into the workspace, ready for the next step (e.g. emailing it as an attachment, or uploading elsewhere). Give the file id (from the List files step). The file lands in the run's scratch space by default; override the name via 'path'. Google-editor documents (Docs/Sheets/Slides) have no raw file, so they're exported to a concrete format instead — PDF by default, or pick another via 'Export as'.",
			Integration: "Google Drive",
			Category:    "network",
			Icon:        "download",
			BrandLogo:   "/brands/google-drive.svg",
			Color:       "#1FA463",
			Provider:    "internal",
			Tags:        []string{"drive", "google", "download", "file"},
			Examples: []core.ParamsExample{
				{Title: "Download a file by id", Params: json.RawMessage(`{"account":"default","file_id":"REPLACE_WITH_FILE_ID"}`)},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "google", Note: "Google OAuth — drive.readonly scope."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				// Optional: wire a file id in from an upstream List files step.
				{Port: "file_id", Label: "File ID", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "out", Label: "Downloaded file"},
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","default":"default"},
					"file_id":{"type":"string","format":"google-drive-file","title":"File","description":"The Drive file to download — pick from your account's files. The File ID input overrides this when connected."},
					"path":{"type":"string","title":"File name","description":"What to call the downloaded file — also the attachment name when emailed. Leave blank to use the file's own name in the run's scratch space."},
					"format":{"type":"string","enum":["pdf","docx","xlsx","pptx","csv","txt","html"],"default":"pdf","title":"Export as","description":"Only used for Google Docs, Sheets and Slides (which have no raw download) — they're exported to this format. PDF works for all; docx/txt/html for Docs, xlsx/csv for Sheets, pptx for Slides. Ignored for regular files."},
					"timeout_ms":{"type":"integer","default":60000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["file_id"]
			}`),
			Idempotent: true,
		},
		Execute: executeDownload,
	})
}

func executeDownload(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	id := resolveFileID(job)
	if id == "" {
		return params.Err(job, "bad_param", "'file_id' is required"), nil
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}
	timeoutMS := params.IntDefault(job.Params, "timeout_ms", 60000)

	// Fetch metadata first: we need the name (for the default save path) and
	// the mimeType (to reject Google-native docs, which have no media bytes).
	meta, err := fileMetadata(ctx, job, id, token, timeoutMS)
	if err != nil {
		return params.Err(job, "drive_error", err.Error()), nil
	}
	// Google-editor docs (Docs/Sheets/Slides) have no media bytes — there's
	// nothing to fetch via alt=media. Instead of failing, export them to a
	// concrete format ('format' param, default PDF) so the run can carry on.
	if isGoogleNative(meta.MimeType) {
		return exportNative(ctx, job, id, meta, token, timeoutMS)
	}

	// Download the media bytes.
	endpoint := apiBaseURL(job) + "/files/" + url.PathEscape(id) + "?alt=media"
	status, body, err := googleDo(ctx, "GET", endpoint, token, "", nil, timeoutMS)
	if err != nil {
		return params.Err(job, "drive_error", err.Error()), nil
	}
	if status < 200 || status >= 300 {
		return params.Err(job, "drive_error", driveErr(body)), nil
	}

	dest := destPath(job, meta.Name, id)
	root, rel, err := sandbox.OpenRoot(job, dest)
	if err != nil {
		if sandbox.IsEscape(err) {
			return params.Err(job, "sandbox_escape", "save path escapes its sandbox root"), nil
		}
		return params.Err(job, "sandbox", err.Error()), nil
	}
	defer root.Close()
	f, err := root.Create(rel)
	if err != nil {
		return params.Err(job, "sandbox", err.Error()), nil
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		return params.Err(job, "sandbox", err.Error()), nil
	}
	if err := f.Close(); err != nil {
		return params.Err(job, "sandbox", err.Error()), nil
	}

	mime := meta.MimeType
	if mime == "" {
		mime = "application/octet-stream"
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			// Ref preserves the scheme so a downstream node resolves it the same.
			"out": {MIME: mime, Ref: dest},
			"meta": {MIME: "application/json", Inline: map[string]any{
				"file_id": id,
				"name":    meta.Name,
				"path":    dest,
				"bytes":   len(body),
				"mime":    mime,
			}},
		},
	}, nil
}

// exportNative exports a Google-editor doc (Docs/Sheets/Slides/Drawing) to a
// concrete format via Drive's /files/{id}/export endpoint, picking the target
// from the 'format' param (default: the type's first supported format, PDF).
func exportNative(ctx context.Context, job core.Job, id string, meta driveFile, token string, timeoutMS int) (core.Result, error) {
	allowed, ok := nativeExportable[meta.MimeType]
	if !ok {
		// Folders, Forms, Sites, shortcuts… nothing to download or export.
		return params.Err(job, "not_downloadable", "file "+id+" is a "+friendlyNative(meta.MimeType)+" — it can't be downloaded or exported"), nil
	}
	format := strings.ToLower(strings.TrimSpace(params.StringDefault(job.Params, "format", "")))
	if format == "" {
		format = allowed[0]
	}
	if !slices.Contains(allowed, format) {
		return params.Err(job, "bad_param", "can't export this "+friendlyNative(meta.MimeType)+" as "+format+" — choose one of: "+strings.Join(allowed, ", ")), nil
	}
	ef := exportFormats[format]

	q := url.Values{}
	q.Set("mimeType", ef.mime)
	endpoint := apiBaseURL(job) + "/files/" + url.PathEscape(id) + "/export?" + q.Encode()
	status, body, err := googleDo(ctx, "GET", endpoint, token, "", nil, timeoutMS)
	if err != nil {
		return params.Err(job, "drive_error", err.Error()), nil
	}
	if status < 200 || status >= 300 {
		return params.Err(job, "drive_error", driveErr(body)), nil
	}

	dest := exportDestPath(job, meta.Name, id, ef.ext)
	root, rel, err := sandbox.OpenRoot(job, dest)
	if err != nil {
		if sandbox.IsEscape(err) {
			return params.Err(job, "sandbox_escape", "save path escapes its sandbox root"), nil
		}
		return params.Err(job, "sandbox", err.Error()), nil
	}
	defer root.Close()
	f, err := root.Create(rel)
	if err != nil {
		return params.Err(job, "sandbox", err.Error()), nil
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		return params.Err(job, "sandbox", err.Error()), nil
	}
	if err := f.Close(); err != nil {
		return params.Err(job, "sandbox", err.Error()), nil
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"out": {MIME: ef.mime, Ref: dest},
			"meta": {MIME: "application/json", Inline: map[string]any{
				"file_id":       id,
				"name":          meta.Name,
				"path":          dest,
				"bytes":         len(body),
				"mime":          ef.mime,
				"exported_from": meta.MimeType,
				"format":        format,
			}},
		},
	}, nil
}

// fileMetadata fetches the name + mimeType + size for a file id.
func fileMetadata(ctx context.Context, job core.Job, id, token string, timeoutMS int) (driveFile, error) {
	q := url.Values{}
	q.Set("fields", "id,name,mimeType,size")
	endpoint := apiBaseURL(job) + "/files/" + url.PathEscape(id) + "?" + q.Encode()
	status, body, err := googleDo(ctx, "GET", endpoint, token, "", nil, timeoutMS)
	if err != nil {
		return driveFile{}, err
	}
	if status < 200 || status >= 300 {
		return driveFile{}, &driveAPIError{msg: driveErr(body)}
	}
	var f driveFile
	if err := json.Unmarshal(body, &f); err != nil {
		return driveFile{}, err
	}
	return f, nil
}

type driveAPIError struct{ msg string }

func (e *driveAPIError) Error() string { return e.msg }

// resolveFileID prefers a wired 'file_id' input port over the param.
func resolveFileID(job core.Job) string {
	if in, ok := job.Input["file_id"]; ok && in.Inline != nil {
		switch v := in.Inline.(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		case []byte:
			if s := strings.TrimSpace(string(v)); s != "" {
				return s
			}
		}
	}
	return strings.TrimSpace(params.StringDefault(job.Params, "file_id", ""))
}

// destPath picks where the download lands. An explicit 'path' wins (a bare name
// goes to scratch space; an explicit scheme passes through). Otherwise the
// file's own name is used, sanitized to a safe basename, in scratch space.
func destPath(job core.Job, name, id string) string {
	dest := strings.TrimSpace(params.StringDefault(job.Params, "path", ""))
	if dest == "" {
		dest = safeBase(name)
		if dest == "" {
			dest = "drive-" + id
		}
	}
	if !strings.Contains(dest, "://") {
		dest = sandbox.Scheme + dest
	}
	return dest
}

// exportDestPath is destPath for an exported Google-editor doc: same rules,
// but the export extension (.pdf/.docx/…) is appended when the chosen name
// lacks it, since a Google doc's own name carries no file extension.
func exportDestPath(job core.Job, name, id, ext string) string {
	dest := destPath(job, name, id)
	if !strings.HasSuffix(strings.ToLower(dest), ext) {
		dest += ext
	}
	return dest
}

// safeBase reduces a Drive file name to a single safe path element: it strips
// any directory components and rejects traversal, so a hostile file name can't
// steer the write outside scratch space (the sandbox root also enforces this,
// but a clean name avoids surprising nested dirs).
func safeBase(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	base := path.Base(filepathSlashes(name))
	if base == "." || base == ".." || base == "/" {
		return ""
	}
	return base
}

// filepathSlashes normalizes Windows-style backslashes to forward slashes so
// path.Base sees the final element regardless of separator.
func filepathSlashes(s string) string {
	return strings.ReplaceAll(s, `\`, "/")
}
