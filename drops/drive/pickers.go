// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package drive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
)

const folderMIME = "application/vnd.google-apps.folder"

// ListFolders lists the connected account's Drive folders (most-recent first)
// as {id, name} options — the backend for the folder pickers on
// drive_list_files ('In folder') and drive_upload ('Into folder'). Reads
// account/timeout_ms from job.Params.
func ListFolders(ctx context.Context, job core.Job) ([]core.AccountResource, error) {
	return listForPicker(ctx, job, "mimeType = '"+folderMIME+"' and trashed = false")
}

// ListFilesForPicker lists the account's non-folder Drive files (most-recent
// first) as {id, name} options — the backend for the drive_download 'File'
// picker. Folders are excluded so the file picker shows actual files.
func ListFilesForPicker(ctx context.Context, job core.Job) ([]core.AccountResource, error) {
	return listForPicker(ctx, job, "mimeType != '"+folderMIME+"' and trashed = false")
}

// listForPicker runs a Drive files.list with the given query and projects the
// results to {id, name} account resources for a dropdown.
func listForPicker(ctx context.Context, job core.Job, q string) ([]core.AccountResource, error) {
	token, err := resolveToken(ctx, job)
	if err != nil {
		return nil, err
	}
	qv := url.Values{}
	qv.Set("q", q)
	qv.Set("fields", "files(id,name)")
	qv.Set("orderBy", "modifiedTime desc")
	qv.Set("pageSize", "100")
	endpoint := apiBaseURL(job) + "/files?" + qv.Encode()
	status, body, err := googleDo(ctx, "GET", endpoint, token, "", nil, params.IntDefault(job.Params, "timeout_ms", 15000))
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("%s", driveErr(body))
	}
	var parsed struct {
		Files []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"files"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("files.list decode: %w", err)
	}
	out := make([]core.AccountResource, 0, len(parsed.Files))
	for _, f := range parsed.Files {
		out = append(out, core.AccountResource{ID: f.ID, Name: f.Name})
	}
	return out, nil
}
