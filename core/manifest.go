// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"encoding/json"
	"slices"
	"strings"
)

func ConnectionSlug(integration string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(integration)), " ", "-")
}

// Dots, not slashes: the secret-name validator only allows [A-Za-z0-9_.-].
func ConnectionSecretKey(integration, fieldKey string) string {
	return "conn." + ConnectionSlug(integration) + "." + fieldKey
}

type ExecutionModel string

const (
	ExecutionBatch   ExecutionModel = "batch"
	ExecutionStream  ExecutionModel = "stream"
	ExecutionTrigger ExecutionModel = "trigger"
)

type ProcessModel string

const (
	ProcessSpawnPerJob   ProcessModel = "spawn_per_job"
	ProcessLongLived     ProcessModel = "long_lived"
	ProcessPreRegistered ProcessModel = "pre_registered"
)

type RetryPolicy string

const (
	RetryNever              RetryPolicy = "never"
	RetryExponentialBackoff RetryPolicy = "exponential_backoff"
)

type ConnectionRequirement struct {
	Kind string `json:"kind" xml:"kind"`                     // "oauth" (start_connection) | "secret" (set_secret)
	Name string `json:"name" xml:"name"`                     // provider ID OR recommended secret name
	Note string `json:"note,omitempty" xml:"note,omitempty"` // human-readable, e.g. "Anthropic API key"
}

// A drop declares ConnectionFields OR RequiresConnections, never both. Values
// are injected at run time into any node param the author left unset.
type ConnectionField struct {
	Key      string `json:"key" xml:"key"`
	Label    string `json:"label" xml:"label"`
	Secret   bool   `json:"secret,omitempty" xml:"secret,omitempty"`     // mask in the UI + route through secret redaction
	Required bool   `json:"required,omitempty" xml:"required,omitempty"` // counts toward "fully connected"

	Placeholder string `json:"placeholder,omitempty" xml:"placeholder,omitempty"`
	Help        string `json:"help,omitempty" xml:"help,omitempty"`

	Options []string `json:"options,omitempty" xml:"options>option,omitempty"` // non-empty → enum: a dropdown of these plus a blank "default"
}

type ParamsExample struct {
	Title  string          `json:"title" xml:"title"`
	Params json.RawMessage `json:"params" xml:"params"`
	Notes  string          `json:"notes,omitempty" xml:"notes,omitempty"`
}

type Port struct {
	Port     string   `json:"port" xml:"port"`
	MIME     []string `json:"mime" xml:"mime>type"`
	Label    string   `json:"label" xml:"label"`
	Required bool     `json:"required" xml:"required"`
	Variadic bool     `json:"variadic" xml:"variadic"`
	Min      *int     `json:"min,omitempty" xml:"min,omitempty"`
	Max      *int     `json:"max,omitempty" xml:"max,omitempty"`

	List bool `json:"list,omitempty" xml:"list,omitempty"`

	// Illustrative payload, NOT UI copy, so it is never translated: an author wiring
	// ${…subject} needs whatever key the upstream API really uses.
	Example json.RawMessage `json:"example,omitempty" xml:"example,omitempty"`

	// A Ref's path is on the daemon's own disk, which means nothing to a tenant
	// runner. The engine refuses such a job before the step runs.
	InlineOnly bool `json:"inline_only,omitempty" xml:"inline_only,omitempty"`
}

type PortKind string

const (
	KindItem   PortKind = "item" // a record: a {field: value} object (application/json)
	KindText   PortKind = "text" // human/plain text (text/plain, text/html)
	KindBool   PortKind = "bool" // a yes/no (application/x-bool)
	KindFile   PortKind = "file" // a file/blob (pdf, spreadsheet, octet-stream, …)
	KindAny    PortKind = "any"  // untyped pass-through — matches anything
	KindNumber PortKind = "number"
)

type Cardinality string

const (
	One  Cardinality = "one"
	Many Cardinality = "many"
)

func (p Port) Kind() PortKind {
	if len(p.MIME) == 0 {
		return KindAny
	}
	has := func(m string) bool { return slices.Contains(p.MIME, m) }
	switch {
	case has("application/json"), has("application/x-dazyflow-list+json"):
		return KindItem
	case has("text/plain"), has("text/html"):
		return KindText
	case has("application/x-bool"):
		return KindBool
	default:
		return KindFile
	}
}

func (p Port) Cardinality() Cardinality {
	if p.List {
		return Many
	}
	return One
}

type Manifest struct {
	ID      string `json:"id" xml:"id"`
	Version string `json:"version" xml:"version"`
	Label   string `json:"label" xml:"label"`

	Subtitle       string          `json:"subtitle,omitempty" xml:"subtitle,omitempty"`
	Color          string          `json:"color" xml:"color"`
	ExecutionModel ExecutionModel  `json:"execution_model" xml:"execution_model"`
	ProcessModel   ProcessModel    `json:"process_model" xml:"process_model"`
	Inputs         []Port          `json:"inputs" xml:"inputs>port"`
	Outputs        []Port          `json:"outputs" xml:"outputs>port"`
	ParamsSchema   json.RawMessage `json:"params_schema" xml:"params_schema"`
	Idempotent     bool            `json:"idempotent" xml:"idempotent"`
	RetryPolicy    RetryPolicy     `json:"retry_policy" xml:"retry_policy"`

	MaxRetries int `json:"max_retries,omitempty" xml:"max_retries,omitempty"`

	DedupeWrites bool `json:"dedupe_writes,omitempty" xml:"dedupe_writes,omitempty"`

	CompatibleWith []string `json:"compatible_with" xml:"compatible_with>id"`

	Category string `json:"category,omitempty" xml:"category,omitempty"`

	Provider string `json:"provider,omitempty" xml:"provider,omitempty"`

	Integration string `json:"integration,omitempty" xml:"integration,omitempty"`

	IntegrationDescription string `json:"integration_description,omitempty" xml:"integration_description,omitempty"`

	Tags []string `json:"tags,omitempty" xml:"tags>tag,omitempty"`

	SearchBoost int `json:"search_boost,omitempty" xml:"search_boost,omitempty"`

	Description string `json:"description,omitempty" xml:"description,omitempty"`

	Summary string `json:"summary,omitempty" xml:"summary,omitempty"`

	Examples []ParamsExample `json:"examples,omitempty" xml:"examples>example,omitempty"`

	RequiresConnections []ConnectionRequirement `json:"requires_connections,omitempty" xml:"requires_connections>connection,omitempty"`

	ConnectionFields []ConnectionField `json:"connection_fields,omitempty" xml:"connection_fields>field,omitempty"`

	ConnectionVerifiable bool `json:"connection_verifiable,omitempty" xml:"connection_verifiable,omitempty"`

	Disabled bool `json:"disabled,omitempty" xml:"disabled,omitempty"`

	// Registered but unreachable, as against Disabled's deliberate switch-off. Such
	// a drop is still described in full, because a flow referencing it must keep its
	// wiring — losing the description is what makes a disconnected server look like a
	// flow that lost its edges.
	Unavailable bool `json:"unavailable,omitempty" xml:"unavailable,omitempty"`

	Egress []string `json:"egress,omitempty" xml:"egress>host,omitempty"`

	Icon string `json:"icon,omitempty" xml:"icon,omitempty"`

	BrandLogo string `json:"brand_logo,omitempty" xml:"brand_logo,omitempty"`

	AwaitsApproval bool `json:"awaits_approval,omitempty" xml:"awaits_approval,omitempty"`

	SubmitsChildGraph bool `json:"submits_child_graph,omitempty" xml:"submits_child_graph,omitempty"`

	DynamicPorts bool `json:"dynamic_ports,omitempty" xml:"dynamic_ports,omitempty"`

	NoPassthrough bool `json:"no_passthrough,omitempty" xml:"no_passthrough,omitempty"`

	ValueSource bool `json:"value_source,omitempty" xml:"value_source,omitempty"`

	NodeState *NodeState `json:"node_state,omitempty" xml:"node_state,omitempty"`
}

type NodeState struct {
	Label     string `json:"label" xml:"label"`
	ResetHint string `json:"reset_hint,omitempty" xml:"reset_hint,omitempty"`
}

func (m Manifest) Input(name string) (Port, bool) {
	for _, p := range m.Inputs {
		if p.Port == name {
			return p, true
		}
	}
	return Port{}, false
}

func (m Manifest) Output(name string) (Port, bool) {
	for _, p := range m.Outputs {
		if p.Port == name {
			return p, true
		}
	}
	return Port{}, false
}

const PassPort = "pass"

const MIMEBool = "application/x-bool"

// Adding the pass input is itself the signal the frontend keys off — a drop
// renders as a value source iff it has no declared inputs — so the two stay in
// lock-step without a separate frontend rule.
func WithPassthrough(m Manifest) Manifest {
	if m.NoPassthrough {
		return m // predicates/routers opt out — see NoPassthrough.
	}
	if _, ok := m.Input(PassPort); ok {
		return m
	}
	if m.ExecutionModel == ExecutionTrigger || m.Category == "trigger" {
		return m // triggers originate flows; nothing upstream to thread from.
	}
	if m.ValueSource {
		return m // literal value source (Text, Number) — authored, not wired.
	}
	pin := Port{Port: PassPort, Label: "Pass-through"}
	m.Inputs = append([]Port{pin}, m.Inputs...)
	m.Outputs = append([]Port{pin}, m.Outputs...)
	return m
}

var listPortNames = map[string]bool{
	"rows":          true,
	"headers":       true,
	"responses":     true,
	"messages":      true,
	"issues":        true,
	"events":        true,
	"subscriptions": true,
	"customers":     true,
	"items":         true,
	"results":       true,
	"records":       true,
}

func MarkListPorts(m Manifest) Manifest {
	mark := func(ports []Port) []Port {
		out := ports
		for i := range ports {
			if ports[i].List || !listPortNames[ports[i].Port] {
				continue
			}
			if &out[0] == &ports[0] {
				out = make([]Port, len(ports))
				copy(out, ports)
			}
			out[i].List = true
		}
		return out
	}
	m.Inputs = mark(m.Inputs)
	m.Outputs = mark(m.Outputs)
	return m
}

func ApplyPassthrough(input map[string]Ref, result *Result) {
	if result == nil || result.Status != StatusOK {
		return
	}
	ref, ok := input[PassPort]
	if !ok {
		return
	}
	if result.Output == nil {
		result.Output = map[string]Ref{}
	}
	if _, exists := result.Output[PassPort]; !exists {
		result.Output[PassPort] = ref
	}
}
