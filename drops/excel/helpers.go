// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package excel hosts the native Excel connectors (excel_read,
// excel_write), migrated from the scripted SheetJS drops back to Go using
// github.com/xuri/excelize/v2. Same ids, ports and params, so existing
// graphs keep resolving — and the ~1.9MB embedded SheetJS bundle goes away.
package excel

import (
	"fmt"
	"io"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/sandbox"
)

// wsPath strips the legacy "workspace://" scheme the old native drop used;
// the sandbox resolver wants a bare workspace-relative path (or scratch://).
func wsPath(p string) string {
	s := strings.TrimSpace(p)
	return strings.TrimPrefix(s, "workspace://")
}

// maxSandboxFileBytes caps how large a workspace file we'll pull fully into
// memory. An .xlsx is a zip, so the compressed file is normally far smaller
// than this; the cap stops a single oversized file from OOMing the shared
// daemon before excelize's own decompression limits even apply.
const maxSandboxFileBytes = 100 * 1024 * 1024 // 100 MiB

func readSandboxFile(job core.Job, p string) ([]byte, error) {
	root, rel, err := sandbox.OpenRoot(job, p)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// Read one byte past the cap so we can distinguish "exactly at the limit"
	// from "over it" and fail fast rather than truncating into a corrupt zip.
	data, err := io.ReadAll(io.LimitReader(f, maxSandboxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSandboxFileBytes {
		return nil, fmt.Errorf("file exceeds %d bytes", maxSandboxFileBytes)
	}
	return data, nil
}

func writeSandboxFile(job core.Job, p string, data []byte) error {
	root, rel, err := sandbox.OpenRoot(job, p)
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := root.Create(rel)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func sandboxFileExists(job core.Job, p string) bool {
	root, rel, err := sandbox.OpenRoot(job, p)
	if err != nil {
		return false
	}
	defer root.Close()
	if _, err := root.Stat(rel); err != nil {
		return false
	}
	return true
}

func normalizeRows(inline any) ([]map[string]any, error) {
	switch v := inline.(type) {
	case nil:
		return nil, nil
	case []map[string]any:
		return v, nil
	case []any:
		out := make([]map[string]any, 0, len(v))
		for i, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("row %d: expected object, got %T", i, item)
			}
			out = append(out, m)
		}
		return out, nil
	case map[string]any:
		return []map[string]any{v}, nil
	}
	return nil, fmt.Errorf("'rows' must be a JSON array of objects, got %T", inline)
}
