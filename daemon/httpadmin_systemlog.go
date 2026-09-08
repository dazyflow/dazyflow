// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

func (h *orgAPI) systemLogTail(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !isPlatformAdmin(p) {
		writeJSONError(rw, http.StatusForbidden, "platform:admin required")
		return
	}
	if h.LogTail == nil {
		writeJSONError(rw, http.StatusNotImplemented, "system log capture not enabled")
		return
	}
	flusher, ok := rw.(http.Flusher)
	if !ok {
		writeJSONError(rw, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	tail := 500
	if q := r.URL.Query().Get("tail"); q != "" {
		if n, err := strconv.Atoi(q); err == nil {
			tail = n
		}
	}

	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-cache")
	rw.Header().Set("X-Accel-Buffering", "no") // for nginx
	rw.WriteHeader(http.StatusOK)

	// Subscribe BEFORE snapshotting so a line written in the gap between
	// backfill and live forwarding isn't lost. A duplicate at the seam is
	// harmless; a dropped line would be a silent hole in the tail.
	lines, cancel := h.LogTail.Subscribe()
	defer cancel()

	if tail > 0 {
		for _, line := range h.LogTail.Snapshot(tail) {
			writeSSE(rw, "line", line)
		}
	}
	flusher.Flush()

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			_, _ = fmt.Fprintf(rw, ": ping\n\n")
			flusher.Flush()
		case line, ok := <-lines:
			if !ok {
				return
			}
			writeSSE(rw, "line", line)
			flusher.Flush()
		}
	}
}
