// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package secrets

import (
	"context"
	"encoding/json"
	"fmt"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "secret_set",
			Version:     "1.0",
			Label:       "Secrets",
			Subtitle:    "Set secret",
			Color:       "#7c3aed",
			Icon:        "database",
			Category:    "system",
			Provider:    "internal",
			Tags:        []string{"secret", "cursor", "state", "store", "write"},
			Description: "Save a value to your tenant's encrypted secret store under the given name. Pair it with template substitution (${secret.name}) to read the value back from later flow runs — the classic use is cursor storage for polling flows that need to remember 'what was the last thing I processed' across restarts.",
			Summary:     "Write a value to the tenant's encrypted secret store under the given name.",
			Examples: []core.ParamsExample{
				{
					Title:  "Persist a polling cursor",
					Params: json.RawMessage(`{"name":"github_last_seen_issue"}`),
					Notes:  "Wire the upstream step's latest-id output into the 'value' input port; later runs read it back with ${secret.github_last_seen_issue}.",
				},
				{
					Title:  "Store a literal value",
					Params: json.RawMessage(`{"name":"deploy_target","value":"production"}`),
					Notes:  "Inline values are fine when you don't need the input port — handy for one-time configuration writes.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				// Overrides params.value when wired (see executeSecretSet).
				{Port: "value", Label: "Value"},
			},
			Outputs: []core.Port{
				// Echoes the name that was written — never the value, so a
				// downstream node can't leak the secret into a Result.
				{Port: "name", Label: "Secret name", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":  {"type":"string","description":"Secret name. Stored as an organization secret; reference it as ${secret.<name>}. Allowed characters: [A-Za-z0-9_.-], up to 128 chars."},
					"value": {"type":"string","description":"Value to store. Overridden by the 'value' input port when connected."}
				},
				"required":["name"]
			}`),
			// PUT semantics — same (tenant, name, value) gives same
			// state. Marking idempotent lets the engine retry the
			// write on transient failure without an at-least-once
			// "wrote twice" concern (the second write replaces with
			// the same value).
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeSecretSet,
	})
}

// executeSecretSet resolves (tenant, name, value) and writes through
// the SecretWriter hook dzd installed at startup. The drop never
// echoes the value in its output — only the name — so a downstream
// node accidentally wired to it can't leak the secret into a Result
// the run-detail page persists.
func executeSecretSet(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	if job.Tenant == "" {
		return params.Err(job, "bad_input",
			"secret_set needs to know which tenant to write under, but this run has no tenant attached. This usually means the run was started outside a tenant-scoped context (e.g. an internal system fire); set a tenant on the principal or workspace before retrying."), nil
	}

	name, ok := params.StringOpt(job.Params, "name")
	if !ok || name == "" {
		return params.Err(job, "bad_input",
			"secret_set needs a 'name' param — the key it will store the value under in this tenant's secret store."), nil
	}
	if err := validSecretName(name); err != nil {
		return params.ErrDetails(job, "bad_input",
			"The secret name is invalid. Names may contain letters, digits, '.', '_' and '-' only, and must be 1–128 characters.",
			fmt.Sprintf("Validator rejected name %q: %v", name, err)), nil
	}

	value, hasParamValue := params.StringOpt(job.Params, "value")
	if input, ok := job.Input["value"]; ok && input.Inline != nil {
		switch v := input.Inline.(type) {
		case string:
			value = v
		case []byte:
			value = string(v)
		default:
			return params.ErrDetails(job, "bad_input",
				"secret_set's 'value' input needs a string, but the upstream node is sending a structured value. Wire a transform between them that renders the value as a string (e.g. a Template node, or a JSON-encode step).",
				fmt.Sprintf("Received type %T on input port 'value'; expected string or []byte.", v)), nil
		}
		hasParamValue = true
	}
	if !hasParamValue {
		return params.Err(job, "bad_input",
			"secret_set has nothing to store: wire something into its 'value' input or set the 'value' param."), nil
	}

	write := currentWriter()
	if write == nil {
		return params.Err(job, "not_configured",
			"This deployment doesn't have an encrypted secret store wired up, so secret_set can't write anything. Start dzd with --master-key (or $DAZYFLOW_MASTER_KEY) to enable the encrypted secret store."), nil
	}
	if err := write(ctx, job.Tenant, name, value); err != nil {
		return params.ErrDetails(job, "write_failed",
			"Writing the secret failed.",
			err.Error()), nil
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"name": {MIME: "text/plain", Inline: name},
		},
	}, nil
}
