// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package webapi

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/internal/schemaports"
)

const overlayPort = "input"

const rawBodyPort = "request_body"

func synthesizeManifest(desc Descriptor, op Operation) core.Manifest {
	method := strings.ToUpper(op.Method)
	integration := desc.Integration
	if integration == "" {
		integration = desc.Name
	}

	inputs := schemaports.Build(portCandidates(op), schemaports.Options{
		// Belt and braces: descriptor validation already refuses these names, so this
		// can only fire for a descriptor built in a test. The alternative is a port that
		// silently shadows an output.
		Reserved: []string{overlayPort, rawBodyPort, "status", "response_body", "headers", "out"},
	})
	if op.BodyMode == BodyRaw {
		inputs = append(inputs, core.Port{
			Port:  rawBodyPort,
			Label: "Body",
			// Every input is inline-only: the service is on another machine while a Ref's
			// path is on the DAEMON's disk, and a job carrying one is refused before the step
			// runs rather than a path being posted to a third party as a string.
			InlineOnly: true,
		})
	}
	inputs = append(inputs, core.Port{
		Port:       overlayPort,
		Label:      "Extra params",
		InlineOnly: true,
	})

	return core.Manifest{
		ID:          StepID(desc.Name, op.ID),
		Version:     "1.0",
		Label:       desc.DisplayName() + " — " + op.DisplayName(),
		Subtitle:    subtitle(op, method),
		Color:       "#5599ee",
		Icon:        "globe",
		BrandLogo:   desc.Logo,
		Category:    "external",
		Provider:    "api:" + desc.Name,
		Integration: integration,
		// The catalog's blurb reaches the Apps page through the integration group, and
		// is deliberately NOT folded into the step's Description: the same paragraph on
		// sixty manifests is what the flow generator would read instead of grounding, and
		// the reader of a step wants the call, not the service.
		IntegrationDescription: desc.Description,
		Tags:                   []string{"api", "http", desc.Name},
		Description:            description(desc, op, method),
		Summary:                summary(desc, op, method),
		Examples:               []core.ParamsExample{example(desc, op, method)},
		ExecutionModel:         core.ExecutionBatch,
		ProcessModel:           core.ProcessLongLived,
		Inputs:                 inputs,
		Outputs: []core.Port{
			// The same three http_request emits, in the same order: flows branch on the
			// status code, so it is a port rather than buried in a meta blob.
			{Port: "status", Label: "Status", MIME: []string{"application/json"}},
			{Port: "response_body", Label: "Response"},
			{Port: "headers", Label: "Headers", MIME: []string{"application/json"}},
		},
		ParamsSchema:     paramsSchema(desc, op),
		ConnectionFields: connectionFields(desc),
		// Declared from the HTTP method, the one thing a described API tells us that an
		// MCP tool does not: a retry edge targeting GET/HEAD/PUT/DELETE validates, and one
		// targeting a POST fails validation instead of silently double-firing.
		Idempotent:  idempotentMethods[method],
		RetryPolicy: retryPolicy(method),
	}
}

// portCandidates excludes header arguments on purpose: in a real spec they are
// content negotiation and versioning, set once for the whole catalog rather than
// per run, so a pin for each would spend the port budget on the arguments least
// likely to be wired. They remain settable as params.
func portCandidates(op Operation) []schemaports.Candidate {
	out := make([]schemaports.Candidate, 0, len(op.Args))
	for _, a := range op.Args {
		if a.In == InHeader {
			continue
		}
		out = append(out, schemaports.Candidate{
			Name:     a.Name,
			Label:    a.Label,
			Type:     a.Type,
			Required: a.Required,
		})
	}
	return out
}

func subtitle(op Operation, method string) string {
	if op.Summary != "" {
		return op.Summary
	}
	return method + " " + op.Path
}

func description(desc Descriptor, op Operation, method string) string {
	var b strings.Builder
	if op.Deprecated {
		b.WriteString("Deprecated by the service. ")
	}
	if op.Description != "" {
		b.WriteString(op.Description)
	} else if op.Summary != "" {
		b.WriteString(op.Summary)
	} else {
		fmt.Fprintf(&b, "Calls %s %s on %s.", method, op.Path, desc.DisplayName())
	}
	fmt.Fprintf(&b, " (%s %s)", method, op.Path)
	return b.String()
}

func summary(desc Descriptor, op Operation, method string) string {
	if op.Summary != "" {
		return op.Summary
	}
	return fmt.Sprintf("Call %s %s on %s.", method, op.Path, desc.DisplayName())
}

// example carries required arguments only: filling in every optional field would
// teach the generator to send them.
func example(desc Descriptor, op Operation, method string) core.ParamsExample {
	params := map[string]any{}
	for _, a := range op.Args {
		if !a.Required {
			continue
		}
		params[a.Name] = placeholderFor(a)
	}
	raw, err := json.Marshal(params)
	if err != nil {
		raw = json.RawMessage(`{}`)
	}
	notes := fmt.Sprintf("The credential comes from the %s connection, so it is not a param here. The service address comes from the catalog itself.", desc.DisplayName())
	if desc.Auth.Kind == AuthNone || desc.Auth.Kind == "" {
		notes = "The service address comes from the catalog itself, so it is not a param here."
	}
	return core.ParamsExample{
		Title:  fmt.Sprintf("%s %s", method, op.Path),
		Params: raw,
		Notes:  notes,
	}
}

func placeholderFor(a Arg) any {
	mime, ok := schemaports.ScalarMIME(a.Type)
	switch {
	case !ok:
		return map[string]any{}
	case len(mime) > 0 && mime[0] == core.MIMEBool:
		return true
	}
	if isNumeric(a.Type) {
		return 0
	}
	return "…"
}

func isNumeric(declared any) bool {
	switch t := declared.(type) {
	case string:
		return t == "number" || t == "integer"
	case []any:
		for _, one := range t {
			if s, ok := one.(string); ok && s != "null" {
				return isNumeric(s)
			}
		}
	}
	return false
}

func retryPolicy(method string) core.RetryPolicy {
	if idempotentMethods[method] {
		return core.RetryExponentialBackoff
	}
	return ""
}

// connectionFields puts a tenant's own service on the Apps page beside Gmail and
// Stripe: connected once, encrypted, injected at run time into whichever params
// the author left unset, never visible in a flow.
//
// The credential and NOTHING ELSE. The service address is deliberately not a
// connection field: connections are writable with secret:write, the catalog
// address is set behind organization:admin, and an injected connection value
// beats the descriptor's own — so making the address one would let the less
// privileged source redirect the token to a host of its choosing.
//
// Not marked Required: it is injected rather than typed, so flagging it would
// mark every node incomplete until the connection exists.
func connectionFields(desc Descriptor) []core.ConnectionField {
	var fields []core.ConnectionField
	switch desc.Auth.Kind {
	case AuthBearer:
		fields = append(fields, core.ConnectionField{
			Key:    "token",
			Label:  "API token",
			Secret: true,
			Help:   "Sent as Authorization: Bearer <token>.",
		})
	case AuthHeader:
		fields = append(fields, core.ConnectionField{
			Key:    "token",
			Label:  "API key",
			Secret: true,
			Help:   "Sent as the " + desc.Auth.Header + " header.",
		})
	}
	return fields
}

func paramsSchema(desc Descriptor, op Operation) json.RawMessage {
	props := map[string]json.RawMessage{}
	var required []string
	for _, a := range op.Args {
		props[a.Name] = argSchema(a)
		if a.Required {
			required = append(required, a.Name)
		}
	}
	props["base_url"] = mustJSON(map[string]any{
		"type":        "string",
		"title":       "Service address",
		"description": "Overrides the address from the connection. Leave empty to use it.",
		"x_advanced":  true,
	})
	if desc.Auth.Kind == AuthBearer || desc.Auth.Kind == AuthHeader {
		props["token"] = mustJSON(map[string]any{
			"type":        "string",
			"title":       "Credential",
			"description": "Overrides the credential from the connection. Leave empty to use it.",
			"x_advanced":  true,
		})
	}
	props["timeout_ms"] = mustJSON(map[string]any{
		"type":        "integer",
		"title":       "Timeout (ms)",
		"default":     DefaultTimeoutMS,
		"minimum":     1,
		"x_advanced":  true,
		"description": "Hard deadline for the full request.",
	})
	props["expect_status"] = mustJSON(map[string]any{
		"type":        "array",
		"title":       "Accepted status codes",
		"items":       map[string]any{"type": "integer"},
		"x_advanced":  true,
		"description": "Status codes treated as success. Empty defaults to 2xx. Set this to accept a 404 as an answer rather than a failure.",
	})

	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		// Sorted so the rendered schema is stable across restarts, as port order is.
		sort.Strings(required)
		schema["required"] = required
	}
	return mustJSON(schema)
}

func argSchema(a Arg) json.RawMessage {
	if len(a.Schema) > 0 {
		return a.Schema
	}
	m := map[string]any{}
	if a.Type != nil {
		m["type"] = a.Type
	}
	if a.Label != "" {
		m["title"] = a.Label
	}
	if a.Description != "" {
		m["description"] = a.Description
	}
	if a.In != InBody {
		m["x_location"] = string(a.In)
	}
	return mustJSON(m)
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}
