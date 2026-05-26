package secrets

import (
	"context"
	"encoding/json"
	"fmt"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/integrations/internal/params"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "secret_set",
			Version:        "1.0",
			Label:          "Set tenant secret",
			Color:          "#7c3aed",
			Icon:           "database",
			Category:       "system",
			Provider:       "internal",
			Tags:           []string{"secret", "cursor", "state", "store", "write"},
			Description:    "Writes a value to this tenant's encrypted secret store under params.name. The value comes from the 'value' input port (string) if connected, otherwise params.value. Pairs with the existing ${tenant://name} template substitution to read the value back from a downstream graph or the next fire — the missing inverse of secret READ. Typical use: cursor storage for poll_trigger graphs that want 'fire only on new items' semantics.",
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "value", Label: "Value to store (string; overrides params.value)"},
			},
			Outputs: []core.Port{
				{Port: "name", Label: "Echo of the name that was written", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":  {"type":"string","description":"Secret name. Stored under tenant://<name>. Allowed characters: [A-Za-z0-9_.-], up to 128 chars."},
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
// the SecretWriter hook hzd installed at startup. The drop never
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
			"This deployment doesn't have an encrypted secret store wired up, so secret_set can't write anything. Start hzd with --master-key (or $HAZYFLOW_MASTER_KEY) to enable the tenant:// store."), nil
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
