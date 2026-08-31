// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package schemaports turns a set of declared arguments into node ports.
//
// It exists because two catalogs need the same answer to the same question.
// An MCP tool arrives as one flat JSON Schema (engine/mcp); a web-API
// operation arrives as a list of arguments split across path, query, header
// and body (engine/webapi). Both then have to decide which of those arguments
// becomes a pin on a box on a canvas — and the rules for that are a POLICY
// about the editor, not about either protocol:
//
//   - a scalar earns a pin; an object or array does not
//   - required arguments first, so a cap can never hide one that must be set
//   - a hard cap, because a node with forty pins is unreadable
//   - deterministic order, because ports are addressed by position
//   - reserved ids stay reserved
//
// Two copies of that policy would drift the first time one was edited. The
// rules and their reasons live here; each catalog keeps only its own way of
// producing Candidates.
package schemaports

import (
	"encoding/json"
	"sort"

	"github.com/dazyflow/dazyflow/core"
)

// DefaultMax caps how many arguments become ports.
//
// A node is a box on a canvas. A tool declaring forty arguments would produce
// one nobody can read, and the arguments past the first handful are nearly
// always the optional long tail — still settable as params, just not worth a
// pin each. Required arguments are taken first, so the cap never hides one
// that must be supplied.
const DefaultMax = 12

// Candidate is one declared argument that might earn a port.
//
// Type is the JSON Schema "type" as it appears: a string, or an array for a
// union like ["string","null"]. Kept as `any` rather than normalized by the
// caller so the union rule below is applied in ONE place — a caller that
// flattened it first would have to reimplement that rule to do so.
type Candidate struct {
	Name     string
	Label    string
	Type     any
	Required bool
}

// Options bounds one synthesis.
type Options struct {
	// Max is the port cap. Zero means DefaultMax.
	Max int
	// Reserved names may not become ports. Callers pass the ids their own
	// manifest already uses (an overlay port, an output port); core.PassPort
	// is always reserved and need not be listed.
	Reserved []string
}

// Build turns candidates into ports.
//
// Every port is InlineOnly, unconditionally and not as an option. Both callers
// send the value to something that cannot read the daemon's disk — another
// process for MCP, another machine for a web API — so a Ref path there is
// meaningless. Making it a flag would make it a check a third caller could
// forget, and the failure mode of forgetting is a file path silently posted to
// a third party as a string.
func Build(cands []Candidate, opts Options) []core.Port {
	max := opts.Max
	if max <= 0 {
		max = DefaultMax
	}
	reserved := make(map[string]bool, len(opts.Reserved)+1)
	reserved[core.PassPort] = true
	for _, r := range opts.Reserved {
		reserved[r] = true
	}

	type kept struct {
		cand Candidate
		mime []string
	}
	var req, opt []kept
	for _, c := range cands {
		if !Portable(c.Name) || reserved[c.Name] {
			continue
		}
		mime, ok := ScalarMIME(c.Type)
		if !ok {
			continue
		}
		if c.Label == "" {
			c.Label = c.Name
		}
		k := kept{cand: c, mime: mime}
		if c.Required {
			req = append(req, k)
		} else {
			opt = append(opt, k)
		}
	}
	// Order has to be deterministic and independent of the caller's iteration
	// order: ports are identified by position in the editor's layout, so a set
	// that reshuffled between restarts would move every wire on the canvas.
	byName := func(s []kept) func(i, j int) bool {
		return func(i, j int) bool { return s[i].cand.Name < s[j].cand.Name }
	}
	sort.Slice(req, byName(req))
	sort.Slice(opt, byName(opt))

	ports := make([]core.Port, 0, max)
	for _, k := range append(req, opt...) {
		if len(ports) == max {
			break
		}
		ports = append(ports, core.Port{
			Port:       k.cand.Name,
			Label:      k.cand.Label,
			MIME:       k.mime,
			Required:   k.cand.Required,
			InlineOnly: true,
		})
	}
	if len(ports) == 0 {
		// nil rather than an empty slice: a manifest's Inputs is appended to by
		// callers, and an empty non-nil slice reads as "declared none" where
		// nil reads as "none to declare". Only the serialized form differs, but
		// it is the catalog API that serializes it.
		return nil
	}
	return ports
}

// Portable reports whether an argument name can be a port id.
//
// It must be spellable as an id — a property called "user name" or "a/b" would
// produce an edge nothing can address. Names a manifest already uses are the
// CALLER's business (Options.Reserved), because they differ per catalog; this
// is only the charset rule. Such an argument stays a param, which still
// reaches the tool.
func Portable(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// ScalarMIME maps a JSON Schema type to a port type, and reports whether it is
// one this synthesis exposes at all.
//
// A union is read through its first non-null member, so the ["string","null"]
// that generators emit for an optional argument lands as text rather than
// being skipped for not being a plain string.
func ScalarMIME(declared any) ([]string, bool) {
	switch t := declared.(type) {
	case string:
		switch t {
		case "string":
			return []string{"text/plain"}, true
		case "number", "integer":
			// Numbers travel as text on a port, the same as every built-in
			// numeric input (a Twilio amount, a Gmail count).
			return []string{"text/plain"}, true
		case "boolean":
			return []string{core.MIMEBool}, true
		default:
			// object, array, null, or something unknown: params only.
			return nil, false
		}
	case []any:
		for _, one := range t {
			s, ok := one.(string)
			if !ok || s == "null" {
				continue
			}
			return ScalarMIME(s)
		}
		return nil, false
	default:
		// No declared type at all. The value could be anything, so the port
		// would have to be untyped — and an untyped pin next to typed ones
		// reads as a mistake. Params only.
		return nil, false
	}
}

// objectSchema is the slice of JSON Schema this package reads. Everything else
// a schema may declare is left to the params form, which renders the raw
// schema.
type objectSchema struct {
	Properties map[string]json.RawMessage `json:"properties"`
	Required   []string                   `json:"required"`
}

// property is the slice of one property's schema that decides its port.
type property struct {
	Type        any    `json:"type"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// FromJSONSchema reads an object schema's top-level properties as candidates.
//
// Deliberately shallow: a top-level property gets a candidate and nothing
// nested does. An object or array argument keeps its structure, and a port per
// leaf would either flatten that structure into invented names or produce a
// node shaped like the schema rather than like a step — so those stay params.
// This maps the common case fully and refuses to guess at the rest.
//
// A schema that cannot be read yields no candidates and no error: the step
// still works through params and an overlay port, and reporting it would fail
// a whole server's registration over one tool's unusual schema.
func FromJSONSchema(raw json.RawMessage) []Candidate {
	if len(raw) == 0 {
		return nil
	}
	var s objectSchema
	if err := json.Unmarshal(raw, &s); err != nil || len(s.Properties) == 0 {
		return nil
	}
	required := make(map[string]bool, len(s.Required))
	for _, r := range s.Required {
		required[r] = true
	}
	out := make([]Candidate, 0, len(s.Properties))
	for name, rawProp := range s.Properties {
		var p property
		if err := json.Unmarshal(rawProp, &p); err != nil {
			continue
		}
		out = append(out, Candidate{
			Name:     name,
			Label:    p.Title,
			Type:     p.Type,
			Required: required[name],
		})
	}
	return out
}

// Assemble merges the three layers of a step's arguments into one map.
//
// Least specific first:
//
//	params        — what the author typed on the step
//	overlay port  — a whole object wired into one catch-all pin
//	argument port — a value wired into that one argument
//
// Most-specific-last is the rule the rest of the product already states: "a
// connected input, when present, overrides the typed setting". A value wired
// into `title` is a statement about `title` and beats an object that merely
// happens to contain one.
//
// Driven by PORTS rather than by whatever the job happens to carry: the ports
// the manifest declared are exactly the argument names an input may set, so a
// stray input key cannot introduce an argument the step never declared. This is
// the counterpart of Build — the same policy, read from the other end — which
// is why it lives beside it rather than in each catalog.
func Assemble(params map[string]any, input map[string]core.Ref, ports []core.Port, overlayPort string) (map[string]any, error) {
	args := make(map[string]any, len(params))
	for k, v := range params {
		args[k] = v
	}
	if ref, ok := input[overlayPort]; ok {
		overlay, err := overlayObject(ref.Inline)
		if err != nil {
			return nil, err
		}
		for k, v := range overlay {
			args[k] = v
		}
	}
	for _, port := range ports {
		if port.Port == overlayPort || port.Port == core.PassPort {
			continue
		}
		ref, ok := input[port.Port]
		if !ok || ref.Inline == nil {
			continue
		}
		args[port.Port] = ref.Inline
	}
	return args, nil
}

// overlayObject coerces a wired value into an object. A JSON string or byte
// slice is decoded; anything else is round-tripped through JSON so a struct the
// engine happened to pass along still works.
func overlayObject(v any) (map[string]any, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case map[string]any:
		return x, nil
	case string:
		var m map[string]any
		if err := json.Unmarshal([]byte(x), &m); err != nil {
			return nil, errNotObject("input is a string but not a JSON object")
		}
		return m, nil
	case []byte:
		var m map[string]any
		if err := json.Unmarshal(x, &m); err != nil {
			return nil, errNotObject("input is bytes but not a JSON object")
		}
		return m, nil
	default:
		data, err := json.Marshal(x)
		if err != nil {
			return nil, errNotObject("input is not convertible to an object")
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, errNotObject("input is not a JSON object")
		}
		return m, nil
	}
}

// errNotObject is the overlay coercion failure. A distinct type so a caller can
// tell "the author wired the wrong shape" (a node error the run should report)
// from a transport fault, without matching on message text.
type errNotObject string

func (e errNotObject) Error() string { return "overlay input: " + string(e) }
