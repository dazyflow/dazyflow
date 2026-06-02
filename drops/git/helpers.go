package git

import (
	"fmt"
	"path/filepath"
	"strings"

	"git.sr.ht/~klahr/hazyflow/core"
)

// sandboxRel cleans rel and rejects absolute paths or "../" escapes so
// callers can safely join it against job.WorkspaceRoot. go-git and
// os/exec both demand absolute paths, so the os.Root sandbox file_read
// uses isn't available here — we validate by hand.
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
