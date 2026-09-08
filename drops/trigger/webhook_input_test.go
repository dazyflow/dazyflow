// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package trigger

import (
	"context"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

func TestExecuteWebhookInput_ErrorsWithoutTrigger(t *testing.T) {
	res, err := executeWebhookInput(context.Background(), core.Job{ID: "job-1"}, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.JobID != "job-1" {
		t.Errorf("JobID = %q", res.JobID)
	}
	if res.Status != core.StatusError {
		t.Fatalf("status = %q, want error", res.Status)
	}
	if res.Error == nil || res.Error.Code != "no_trigger_data" {
		t.Fatalf("error = %+v", res.Error)
	}
	if !strings.Contains(res.Error.Message, "web address") {
		t.Errorf("message = %q", res.Error.Message)
	}
}
