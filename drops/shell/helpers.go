// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package shell

import (
	"github.com/dazyflow/dazyflow/core"
)

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
