// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package trigger

import (
	"context"
	"encoding/json"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:       "form_input",
			Version:  "1.0",
			Label:    "Form",
			Icon:     "clipboard-list",
			Category: "trigger",
			Provider: "internal",
			Tags: []string{"form", "trigger", "intake", "submission", "public", "feedback",
				"survey", "questionnaire", "contact", "page", "website", "link",
				"respond", "answers"},
			Description: "Starts the flow when someone fills in the form Dazyflow hosts for you — no website and no key needed, just a link you can share. Body carries what they typed, one entry per field. Anyone with the link can submit, so treat it as public.",
			Summary:     "Starts the flow when someone submits the form Dazyflow hosts for you.",
			Examples: []core.ParamsExample{
				{
					Title:  "A contact form",
					Params: json.RawMessage(`{"form_fields":["name","email","message"],"form_title":"Get in touch"}`),
					Notes:  "The form lives at /form/<tenant>/<workspace>/<flow> the moment the flow is published. Leave form_fields empty for the name/email/message default.",
				},
			},
			ExecutionModel: core.ExecutionTrigger,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "body", Label: "Body"},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"form_fields":{
						"type":"array",
						"items":{"type":"string"},
						"title":"Form fields",
						"description":"The questions the form asks, one per field. Each becomes a key on the Body output, so later steps know the columns before the first submission arrives. Defaults to name, email, message."
					},
					"form_title":{
						"type":"string",
						"title":"Form title",
						"description":"Heading shown at the top of the form. Defaults to the flow's name."
					}
				}
			}`),
			// Retry of a trigger is meaningless: a submission arrives once and
			// the seed can't be re-derived.
			Idempotent: false,
		},
		Execute: executeFormInput,
	})
}

// executeFormInput runs only when a graph containing this node is run WITHOUT
// a submission (the editor's Run button, a schedule). The /form handler
// pre-completes the node's JobRecord directly, so this never runs on the real
// path.
func executeFormInput(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusError,
		Error: &core.JobError{
			Code:    "no_trigger_data",
			Message: "nobody has filled in this flow's form — it starts from a submission; open the form link and submit it instead of pressing Run",
		},
	}, nil
}
