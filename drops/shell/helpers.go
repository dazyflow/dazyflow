// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package shell

import (
	"fmt"
	"path/filepath"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
)


// sandboxRel cleans rel and rejects absolute paths or "../" escapes so
// callers can safely join it against job.WorkspaceRoot.
func sandboxRel(rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return ".", nil
	}
	cleaned := filepath.Clean(rel)
	if filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("absolute path %q not allowed", rel)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workspace", rel)
	}
	return cleaned, nil
}

func emitProgress(ch chan<- core.Progress, job core.Job, pct float64, msg string) {
	if ch == nil {
		return
	}
	select {
	case ch <- core.Progress{JobID: job.ID, NodeID: job.NodeID, Percent: &pct, Message: msg}:
	default:
	}
}

func emitLogProgress(ch chan<- core.Progress, job core.Job, stream, line string) {
	if ch == nil {
		return
	}
	select {
	case ch <- core.Progress{
		JobID:   job.ID,
		NodeID:  job.NodeID,
		Message: line,
		Data:    map[string]any{"stream": stream, "line": line},
	}:
	default:
	}
}
