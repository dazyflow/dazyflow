package drops_test

import (
	"encoding/json"
	"testing"
)

// Every registered built-in must ship a usable, self-consistent worked
// example: the registry already fails-closed on a drop with zero Examples
// (engine/registry.go), but it does not check that the example is actually
// shaped like the params the drop accepts. These tests close that gap for
// ALL built-ins at once — the discovery contract an LLM relies on when it
// copies an example to compose a flow.
//
// Companion to the safety sweep in invariants_test.go (no drop may panic /
// hang / break the Result contract): this is the *correctness* side — the
// declared examples line up with the declared param schema.

// paramSchema is the slice of a drop's ParamsSchema we assert on: the
// author-settable property names and which of them are required.
type paramSchema struct {
	Properties map[string]json.RawMessage `json:"properties"`
	Required   []string                   `json:"required"`
}

// TestAllDrops_ExamplesMatchSchema asserts, for every registered drop and
// every worked example it ships:
//
//   - the example has a non-empty Title (the catalog renders it verbatim),
//   - its Params decode to a JSON object,
//   - every param key it uses is declared in the drop's ParamsSchema — a
//     key that isn't is a typo or schema drift that would mislead an LLM,
//   - every *required* param is present — unless the drop draws params from
//     a configured connection (RequiresConnections / ConnectionFields), in
//     which case the credential params are injected at run time and a worked
//     example legitimately omits them (e.g. Stripe's api_key, supplied as a
//     ${secret.STRIPE_API_KEY}).
func TestAllDrops_ExamplesMatchSchema(t *testing.T) {
	for _, d := range allDrops(t) {
		d := d
		t.Run(d.id, func(t *testing.T) {
			m := d.manifest
			// Params come (partly) from a configured connection — a worked
			// example omits those credentials by design.
			connectionInjected := len(m.RequiresConnections) > 0 || len(m.ConnectionFields) > 0

			var schema paramSchema
			haveSchema := len(m.ParamsSchema) > 0
			if haveSchema {
				if err := json.Unmarshal(m.ParamsSchema, &schema); err != nil {
					t.Fatalf("ParamsSchema is not valid JSON: %v", err)
				}
			}

			if len(m.Examples) == 0 {
				// Defense in depth: registration already enforces this, but a
				// drop that reaches the registry by another path must not slip
				// through without a worked example.
				t.Fatalf("drop %q has no Examples", d.id)
			}

			for i, ex := range m.Examples {
				if ex.Title == "" {
					t.Errorf("example #%d: empty Title", i)
				}
				if len(ex.Params) == 0 {
					// An example with no params is only sensible for a drop
					// whose params are all optional.
					if haveSchema && len(schema.Required) > 0 && !connectionInjected {
						t.Errorf("example #%d %q: no params, but schema requires %v", i, ex.Title, schema.Required)
					}
					continue
				}
				var params map[string]json.RawMessage
				if err := json.Unmarshal(ex.Params, &params); err != nil {
					t.Errorf("example #%d %q: Params is not a JSON object: %v", i, ex.Title, err)
					continue
				}
				if !haveSchema {
					continue
				}
				for k := range params {
					if _, ok := schema.Properties[k]; !ok {
						t.Errorf("example #%d %q: param %q is not declared in ParamsSchema (typo or schema drift)", i, ex.Title, k)
					}
				}
				if connectionInjected {
					continue // required credentials are injected, not authored
				}
				for _, req := range schema.Required {
					if _, ok := params[req]; !ok {
						t.Errorf("example #%d %q: missing required param %q", i, ex.Title, req)
					}
				}
			}
		})
	}
}
