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

// systemLogTail streams the daemon's own log output to a platform admin as
// Server-Sent Events — the live "System log" viewer. It first backfills the
// retained ring buffer (so the viewer isn't blank on connect) and then
// forwards each new log line as it's written. Mirrors jobEvents' SSE
// plumbing: text/event-stream headers, a flush after every frame, a 25s
// keep-alive ping, and disconnect on request-context cancel.
//
// Each line is one `event: line` frame whose data is the raw log line encoded
// as a JSON string — so embedded quotes, tabs, and ANSI escapes survive
// transport intact and the client just JSON.parses it. Regex filtering is
// done client-side in the viewer (responsive, no reconnect on filter change).
//
// "?tail=N" caps the backfill (default 500; N<=0 means no backfill, stream
// live only). platform:admin only — the daemon log is instance-wide.
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
			// SSE comment line — dropped by EventSource but keeps the
			// connection alive through idle-timeout proxies.
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
