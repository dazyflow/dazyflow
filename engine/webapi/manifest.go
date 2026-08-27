// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package webapi

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/internal/schemaports"
)

// overlayPort is the catch-all input: a whole JSON object merged over the
// params. Same name and same job as engine/mcp's, because it is the same
// affordance and an author moving between an MCP step and a web-API step should
// not have to learn a second word for it. It stays even though arguments get
// their own ports, because it is the only way to supply an argument the port
// synthesis declines to expose — a nested object, an array, a name that cannot
// be a port.
const overlayPort = "input"

// rawBodyPort carries a verbatim request body for BodyRaw operations. Named as
// http_request names it, for the same reason overlayPort is named as MCP names
// it.
const rawBodyPort = "request_body"

// synthesizeManifest turns one described operation into the manifest the engine
// validates against and the palette (and the flow generator) reads.
func synthesizeManifest(desc Descriptor, op Operation) core.Manifest {
	method := strings.ToUpper(op.Method)
	integration := desc.Integration
	if integration == "" {
		integration = desc.Name
	}

	inputs := schemaports.Build(portCandidates(op), schemaports.Options{
		// Belt and braces: descriptor validation already refuses an argument
		// named any of these (reservedParams), so this can only fire for a
		// descriptor built in a test. Cheap enough to keep, and the alternative
		// is a port that silently shadows an output.
		Reserved: []string{overlayPort, rawBodyPort, "status", "response_body", "headers", "out"},
	})
	if op.BodyMode == BodyRaw {
		inputs = append(inputs, core.Port{
			Port:  rawBodyPort,
			Label: "Body",
			// Every input here is inline-only: the service is on another
			// machine (or at least another process's network namespace), while
			// a Ref's path is on the DAEMON's disk. A job carrying one is
			// refused before the step runs, with that as the reason, rather
			// than a path being posted to a third party as a string.
			InlineOnly: true,
		})
	}
	inputs = append(inputs, core.Port{
		Port:       overlayPort,
		Label:      "Optional JSON object merged with params before the call",
		InlineOnly: true,
	})

	return core.Manifest{
		ID:             StepID(desc.Name, op.ID),
		Version:        "1.0",
		Label:          desc.Name + " — " + op.ID,
		Subtitle:       subtitle(op, method),
		Color:          "#5599ee",
		Icon:           "globe",
		Category:       "external",
		Provider:       "api:" + desc.Name,
		Integration:    integration,
		Tags:           []string{"api", "http", desc.Name},
		Description:    description(desc, op, method),
		Summary:        summary(desc, op, method),
		Examples:       []core.ParamsExample{example(desc, op, method)},
		ExecutionModel: core.ExecutionBatch,
		ProcessModel:   core.ProcessLongLived,
		Inputs:         inputs,
		Outputs: []core.Port{
			// The same three http_request emits, in the same order and for the
			// same reason: flows branch on the status code, so it is a port and
			// not buried in a meta blob. A typed step that hid it would be a
			// downgrade from the generic step it replaces.
			{Port: "status", Label: "Status", MIME: []string{"application/json"}},
			{Port: "response_body", Label: "Response"},
			{Port: "headers", Label: "Headers", MIME: []string{"application/json"}},
		},
		ParamsSchema:     paramsSchema(desc, op),
		ConnectionFields: connectionFields(desc),
		// Declared from the HTTP method, which is the one thing a described API
		// tells us that an MCP tool does not: GET/HEAD/PUT/DELETE are idempotent
		// per RFC 9110, so a retry edge that targets them validates, and one
		// that targets a POST fails validation instead of silently double-firing.
		Idempotent:  idempotentMethods[method],
		RetryPolicy: retryPolicy(method),
	}
}

// portCandidates decides which of an operation's arguments are even considered
// for a port.
//
// Header arguments are excluded on purpose. In a real spec they are
// content negotiation and versioning (`Accept-Language`, `X-Api-Version`) —
// set once for the whole catalog, not per run — and a pin for each would spend
// the port budget on the arguments least likely to be wired. They remain
// settable as params.
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
		// core.Manifest has no deprecation field yet, and inventing one is a
		// bigger change than this feature should carry. Saying it first in the
		// description is where an author will actually read it.
		b.WriteString("Deprecated by the service. ")
	}
	if op.Description != "" {
		b.WriteString(op.Description)
	} else if op.Summary != "" {
		b.WriteString(op.Summary)
	} else {
		fmt.Fprintf(&b, "Calls %s %s on %s.", method, op.Path, desc.Name)
	}
	fmt.Fprintf(&b, " (%s %s)", method, op.Path)
	return b.String()
}

func summary(desc Descriptor, op Operation, method string) string {
	if op.Summary != "" {
		return op.Summary
	}
	return fmt.Sprintf("Call %s %s on %s.", method, op.Path, desc.Name)
}

// example gives the flow generator a params shape to copy. Required arguments
// only: an example that filled in every optional field would teach the
// generator to send them.
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
	notes := fmt.Sprintf("The service address and credential come from the %s connection, so they are not params here.", desc.Name)
	if desc.Auth.Kind == AuthNone || desc.Auth.Kind == "" {
		notes = fmt.Sprintf("The service address comes from the %s connection, so it is not a param here.", desc.Name)
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

// connectionFields is the ergonomic point of this feature.
//
// Injection is manifest-driven at run time (engine/secrets.go) and the Apps
// page finds an integration by scanning manifests for the slug
// (daemon/connectionverify.go), and neither cares whether the manifest was
// written in Go or synthesized here. So declaring these puts a tenant's own
// service on an Apps page beside Gmail and Stripe: connected once, encrypted,
// injected into whichever of these params the author left unset, never visible
// inside a flow. That is what `http_request` structurally cannot do — there,
// the address and the ${secret.X} are re-typed in every step of every flow, and
// rotating the token is forty edits.
//
// Neither field is marked Required. Required here means "counts toward fully
// connected", and it does — but base_url has a descriptor default and both are
// injected rather than typed, so flagging them would mark every node incomplete
// until the connection exists. That check belongs to the connection, not the
// node.
func connectionFields(desc Descriptor) []core.ConnectionField {
	fields := []core.ConnectionField{{
		Key:         "base_url",
		Label:       "Service address",
		Placeholder: "https://api.example.com",
		Help:        "The base address of your service. Operation paths are joined onto it.",
	}}
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

// paramsSchema renders the params form: every argument, plus the two knobs the
// call itself takes. An argument's own Schema is used verbatim when it has one,
// so an enum stays a dropdown and a nested body object keeps its shape after
// Type reduced it to a word.
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
		// Sorted so the rendered schema is stable across restarts — the same
		// reason port order is.
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
		// Worth stating in the form: it explains why two arguments of the same
		// shape land in different places, and it is the only place the request
		// structure is visible to an author at all.
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
