// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package drive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "drive_list_files",
			Version:     "1.0",
			Label:       "Google Drive",
			Subtitle:    "List files",
			Summary:     "List or search files in Google Drive.",
			Description: "List files in Google Drive, optionally filtered by name, folder, or type. Trashed files are excluded by default. Each result is an object with id, name, mime_type, size, modified_time and web_view_link. Use the file id with the Download step to fetch the contents.",
			Integration: "Google Drive",
			Category:    "network",
			Icon:        "folder",
			BrandLogo:   "/brands/google-drive.svg",
			Color:       "#1FA463",
			Provider:    "internal",
			Tags:        []string{"drive", "google", "files", "list", "search"},
			Examples: []core.ParamsExample{
				{Title: "PDFs in a folder", Params: json.RawMessage(`{"account":"default","folder_id":"REPLACE_WITH_FOLDER_ID","mime_type":"application/pdf","limit":50}`)},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "google", Note: "Google OAuth — drive.readonly scope."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "files", Label: "Files", MIME: []string{"application/json"}},
				{Port: "count", Label: "Count", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","default":"default"},
					"name_contains":{"type":"string","title":"Name contains","description":"Match files whose name contains this text. Leave blank to match any name."},
					"folder_id":{"type":"string","format":"google-drive-folder","title":"In folder","description":"Restrict to files inside this folder — pick from your account's folders. Leave blank to search all of Drive."},
					"mime_type":{"type":"string","title":"Type","examples":["application/pdf","image/png","application/vnd.google-apps.folder"],"description":"Restrict to files of this MIME type. Leave blank for any type."},
					"query":{"type":"string","title":"Advanced query","x_advanced":true,"description":"Raw Drive query expression ANDed with the other filters (e.g. \"starred = true\")."},
					"include_trashed":{"type":"boolean","title":"Include trashed","default":false,"description":"Include files in the trash."},
					"order_by":{"type":"string","title":"Order by","default":"modifiedTime desc","examples":["modifiedTime desc","name"],"description":"Sort order (a Drive orderBy expression)."},
					"limit":{"type":"integer","title":"Max files","default":100,"minimum":1,"maximum":1000,"description":"Upper bound on files returned."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeListFiles,
	})
}

func executeListFiles(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	files, err := ListFiles(ctx, job)
	if err != nil {
		return params.Err(job, "drive_error", err.Error()), nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"files": {MIME: "application/json", Inline: files},
			"count": {MIME: "text/plain", Inline: strconv.Itoa(len(files))},
		},
	}, nil
}

// ListFiles queries Drive and returns the normalized file objects. Exported so
// the daemon can reuse the exact read (e.g. a resource picker).
func ListFiles(ctx context.Context, job core.Job) ([]map[string]any, error) {
	token, err := resolveToken(ctx, job)
	if err != nil {
		return nil, err
	}

	q := url.Values{}
	q.Set("q", buildQuery(job))
	q.Set("fields", "files(id,name,mimeType,modifiedTime,size,webViewLink)")
	q.Set("orderBy", params.StringDefault(job.Params, "order_by", "modifiedTime desc"))
	q.Set("pageSize", strconv.Itoa(params.IntDefault(job.Params, "limit", 100)))

	endpoint := apiBaseURL(job) + "/files?" + q.Encode()
	status, body, err := googleDo(ctx, "GET", endpoint, token, "", nil, params.IntDefault(job.Params, "timeout_ms", 15000))
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("%s", driveErr(body))
	}

	var parsed struct {
		Files []driveFile `json:"files"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("files.list decode: %w", err)
	}
	out := make([]map[string]any, 0, len(parsed.Files))
	for _, f := range parsed.Files {
		out = append(out, f.normalize())
	}
	return out, nil
}

// buildQuery assembles the Drive q= expression from the convenience filters
// (name_contains, folder_id, mime_type), the trashed gate, and any raw advanced
// query — all ANDed. Injected values are escaped per the Drive grammar.
func buildQuery(job core.Job) string {
	var clauses []string
	if v := strings.TrimSpace(params.StringDefault(job.Params, "name_contains", "")); v != "" {
		clauses = append(clauses, "name contains '"+quoteDriveValue(v)+"'")
	}
	if v := strings.TrimSpace(params.StringDefault(job.Params, "folder_id", "")); v != "" {
		clauses = append(clauses, "'"+quoteDriveValue(v)+"' in parents")
	}
	if v := strings.TrimSpace(params.StringDefault(job.Params, "mime_type", "")); v != "" {
		clauses = append(clauses, "mimeType = '"+quoteDriveValue(v)+"'")
	}
	if !params.BoolDefault(job.Params, "include_trashed", false) {
		clauses = append(clauses, "trashed = false")
	}
	if v := strings.TrimSpace(params.StringDefault(job.Params, "query", "")); v != "" {
		clauses = append(clauses, "("+v+")")
	}
	return strings.Join(clauses, " and ")
}
