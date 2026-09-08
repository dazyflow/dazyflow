// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Shared by engine/mcp and engine/webapi so the two cannot drift: which arguments
// earn a pin is a policy about the EDITOR, not about either protocol.
package schemaports

import (
	"encoding/json"
	"sort"

	"github.com/dazyflow/dazyflow/core"
)

// A card with forty pins is unreadable, and the rest stay settable as params.
const DefaultMax = 12

type Candidate struct {
	Name     string
	Label    string
	Type     any
	Required bool
}

type Options struct {
	Max      int
	Reserved []string
}

// Required arguments first, then declaration order, so the pick is stable.
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
	// Deterministic and independent of the caller's iteration order.
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
		return nil
	}
	return ports
}

// A name that cannot be a port id stays params-only rather than being mangled.
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

func ScalarMIME(declared any) ([]string, bool) {
	switch t := declared.(type) {
	case string:
		switch t {
		case "string":
			return []string{"text/plain"}, true
		case "number", "integer":
			return []string{"text/plain"}, true
		case "boolean":
			return []string{core.MIMEBool}, true
		default:
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
		// No declared type: the port must accept anything.
		return nil, false
	}
}

type objectSchema struct {
	Properties map[string]json.RawMessage `json:"properties"`
	Required   []string                   `json:"required"`
}

type property struct {
	Type        any    `json:"type"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

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

type errNotObject string

func (e errNotObject) Error() string { return "overlay input: " + string(e) }
