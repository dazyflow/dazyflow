// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package webapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	yaml "go.yaml.in/yaml/v3"
)

// A hand-built step and an imported operation are the same object, so this
// produces exactly what the admin form does.
//
// A parser of our own rather than a library, because the feature is "import
// OPERATIONS": a library refuses a document as a whole, so one operation we
// cannot express would block the fifty we can. It also makes the SSRF rule
// structural — there is no fetcher in this parser, so an external $ref cannot be
// followed even by mistake.

// Swagger 2.0 is REFUSED, not half-read: its parameter model differs, so reading
// it as 3.x would produce operations that look right and send the wrong request.
const swagger2Message = "this is a Swagger 2.0 document, which this importer does not read. " +
	"Convert it to OpenAPI 3 (most tools can) and import that."

// Every warning corresponds to something NOT imported.
type ImportWarning struct {
	Where  string `json:"where,omitempty"`
	Reason string `json:"reason"`
}

type SpecImport struct {
	Title         string              `json:"title,omitempty"`
	Description   string              `json:"description,omitempty"`
	BaseURL       string              `json:"base_url,omitempty"`
	Operations    []Operation         `json:"operations"`
	Tags          []string            `json:"tags,omitempty"`
	OperationTags map[string][]string `json:"operation_tags,omitempty"`
	Warnings      []ImportWarning     `json:"warnings,omitempty"`
}

type opTags map[string][]string

func ParseSpec(raw []byte) (SpecImport, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return SpecImport{}, fmt.Errorf("this does not read as JSON or YAML: %v", err)
	}
	if doc == nil {
		return SpecImport{}, fmt.Errorf("the document is empty")
	}
	if _, isSwagger := doc["swagger"]; isSwagger {
		return SpecImport{}, fmt.Errorf("%s", swagger2Message)
	}
	version, _ := doc["openapi"].(string)
	if version == "" {
		return SpecImport{}, fmt.Errorf("this is not an OpenAPI document: it declares no `openapi` version")
	}
	if !strings.HasPrefix(version, "3.") {
		return SpecImport{}, fmt.Errorf("OpenAPI %s is not a version this importer reads (it reads 3.x)", version)
	}

	p := &specParser{doc: doc}
	out := SpecImport{}

	if info, ok := doc["info"].(map[string]any); ok {
		out.Title, _ = info["title"].(string)
		out.Description, _ = info["description"].(string)
	}
	out.BaseURL, p.warnings = baseURLFrom(doc, p.warnings)

	paths, _ := doc["paths"].(map[string]any)
	if len(paths) == 0 {
		return SpecImport{}, fmt.Errorf("the document declares no paths")
	}

	tags := opTags{}
	for _, path := range sortedKeys(paths) {
		item, ok := paths[path].(map[string]any)
		if !ok {
			continue
		}
		shared := p.parameters(item["parameters"], path)
		for _, method := range sortedKeys(item) {
			if !knownMethods[strings.ToUpper(method)] {
				continue // "parameters", "summary", "$ref", vendor extensions
			}
			raw, ok := item[method].(map[string]any)
			if !ok {
				continue
			}
			op, opTagList, err := p.operation(strings.ToUpper(method), path, raw, shared)
			if err != nil {
				p.warn(method+" "+path, err.Error())
				continue
			}
			out.Operations = append(out.Operations, op)
			tags[op.ID] = opTagList
		}
	}

	if len(out.Operations) == 0 {
		return SpecImport{}, fmt.Errorf("no operation in this document could be imported (%d skipped) — the warnings say why", len(p.warnings))
	}
	out.Tags = collectTags(tags)
	out.OperationTags = tags
	out.Warnings = p.warnings
	return out, nil
}

func baseURLFrom(doc map[string]any, warnings []ImportWarning) (string, []ImportWarning) {
	servers, _ := doc["servers"].([]any)
	if len(servers) == 0 {
		return "", warnings
	}
	first, _ := servers[0].(map[string]any)
	addr, _ := first["url"].(string)
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", warnings
	}
	if strings.Contains(addr, "{") {
		return "", append(warnings, ImportWarning{
			Reason: fmt.Sprintf("the server address %q uses variables, so it cannot be filled in automatically — type the address yourself", addr),
		})
	}
	if !strings.HasPrefix(addr, "http://") && !strings.HasPrefix(addr, "https://") {
		return "", append(warnings, ImportWarning{
			Reason: fmt.Sprintf("the document's server address %q is relative, so the full address has to be typed in", addr),
		})
	}
	return strings.TrimRight(addr, "/"), warnings
}

type specParser struct {
	doc      map[string]any
	warnings []ImportWarning
}

func (p *specParser) warn(where, reason string) {
	p.warnings = append(p.warnings, ImportWarning{Where: where, Reason: reason})
}

const maxRefDepth = 20

func (p *specParser) resolve(node any, depth int) (any, error) {
	m, ok := node.(map[string]any)
	if !ok {
		return node, nil
	}
	ref, isRef := m["$ref"].(string)
	if !isRef {
		return node, nil
	}
	if depth > maxRefDepth {
		return nil, fmt.Errorf("$ref chains more than %d deep", maxRefDepth)
	}
	if !strings.HasPrefix(ref, "#/") {
		return nil, fmt.Errorf("refers to %q, outside this document — external references are not followed", ref)
	}
	cur := any(p.doc)
	for _, seg := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		seg = strings.ReplaceAll(strings.ReplaceAll(seg, "~1", "/"), "~0", "~")
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("reference %q does not resolve in this document", ref)
		}
		cur, ok = obj[seg]
		if !ok {
			return nil, fmt.Errorf("reference %q does not resolve in this document", ref)
		}
	}
	return p.resolve(cur, depth+1)
}

func (p *specParser) schemaOf(node any) (map[string]any, error) {
	resolved, err := p.resolve(node, 0)
	if err != nil {
		return nil, err
	}
	m, ok := resolved.(map[string]any)
	if !ok {
		return map[string]any{}, nil
	}
	members, hasAllOf := m["allOf"].([]any)
	if !hasAllOf {
		return m, nil
	}
	merged := map[string]any{}
	props := map[string]any{}
	var required []any
	for _, member := range members {
		sub, err := p.schemaOf(member)
		if err != nil {
			return nil, err
		}
		for k, v := range sub {
			switch k {
			case "properties":
				if sp, ok := v.(map[string]any); ok {
					for name, schema := range sp {
						props[name] = schema
					}
				}
			case "required":
				if rq, ok := v.([]any); ok {
					required = append(required, rq...)
				}
			default:
				merged[k] = v
			}
		}
	}
	for k, v := range m {
		if k == "allOf" {
			continue
		}
		merged[k] = v
	}
	if len(props) > 0 {
		merged["properties"] = props
	}
	if len(required) > 0 {
		merged["required"] = required
	}
	return merged, nil
}

func (p *specParser) operation(method, path string, raw map[string]any, shared []Arg) (Operation, []string, error) {
	op := Operation{
		Method:     method,
		Path:       path,
		Deprecated: boolOf(raw["deprecated"]),
	}
	// Summary is left EMPTY so the subtitle falls back to "GET /orders/{id}":
	// setting both from `summary` put the same sentence on the card twice.
	specSummary, _ := raw["summary"].(string)
	specDescription, _ := raw["description"].(string)
	op.Title = strings.TrimSpace(specSummary)
	op.Description = strings.TrimSpace(specDescription)
	if op.Description == "" {
		op.Description = op.Title
	}

	id, err := operationID(raw, method, path)
	if err != nil {
		return Operation{}, nil, err
	}
	op.ID = id

	args := append([]Arg(nil), shared...)
	args = append(args, p.parameters(raw["parameters"], method+" "+path)...)

	bodyArgs, mode, err := p.requestBody(raw["requestBody"], method)
	if err != nil {
		return Operation{}, nil, err
	}
	op.BodyMode = mode
	args = append(args, bodyArgs...)

	op.Args = dedupeArgs(args)

	if err := op.validate(); err != nil {
		return Operation{}, nil, err
	}

	var tagList []string
	if tags, ok := raw["tags"].([]any); ok {
		for _, t := range tags {
			if s, ok := t.(string); ok && s != "" {
				tagList = append(tagList, s)
			}
		}
	}
	return op, tagList, nil
}

func operationID(raw map[string]any, method, path string) (string, error) {
	if id, _ := raw["operationId"].(string); strings.TrimSpace(id) != "" {
		slug := slugID(id)
		if err := validName(slug); err != nil {
			return "", fmt.Errorf("operationId %q cannot be used as a step id: %v", id, err)
		}
		return slug, nil
	}
	// Deterministic, or the same document reads as remove-and-add on a refresh.
	derived := slugID(strings.ToLower(method) + "_" + path)
	if err := validName(derived); err != nil {
		return "", fmt.Errorf("no operationId, and one could not be derived from %s %s", method, path)
	}
	return derived, nil
}

func slugID(in string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range in {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastUnderscore = false
		case r == '-' || r == '_':
			if !lastUnderscore {
				b.WriteRune('_')
				lastUnderscore = true
			}
		default:
			if !lastUnderscore && b.Len() > 0 {
				b.WriteRune('_')
				lastUnderscore = true
			}
		}
	}
	out := strings.Trim(b.String(), "_")
	if len(out) > 64 {
		out = strings.Trim(out[:64], "_")
	}
	return out
}

func (p *specParser) parameters(node any, where string) []Arg {
	list, ok := node.([]any)
	if !ok {
		return nil
	}
	var out []Arg
	for _, item := range list {
		resolved, err := p.resolve(item, 0)
		if err != nil {
			p.warn(where, "a parameter "+err.Error())
			continue
		}
		m, ok := resolved.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		in, _ := m["in"].(string)
		if name == "" || in == "" {
			continue
		}
		var loc ArgIn
		switch in {
		case "path":
			loc = InPath
		case "query":
			loc = InQuery
		case "header":
			loc = InHeader
		case "cookie":
			p.warn(where, fmt.Sprintf("cookie parameter %q is not something a described call can send", name))
			continue
		default:
			continue
		}
		schema, err := p.schemaOf(m["schema"])
		if err != nil {
			p.warn(where, fmt.Sprintf("parameter %q %v", name, err))
			continue
		}
		desc, _ := m["description"].(string)
		out = append(out, Arg{
			Name:        name,
			In:          loc,
			Type:        schema["type"],
			Required:    loc == InPath || boolOf(m["required"]),
			Description: desc,
			Schema:      rawSchema(schema),
		})
	}
	return out
}

func (p *specParser) requestBody(node any, method string) ([]Arg, BodyMode, error) {
	if node == nil {
		return nil, BodyNone, nil
	}
	resolved, err := p.resolve(node, 0)
	if err != nil {
		return nil, BodyNone, fmt.Errorf("request body %v", err)
	}
	body, ok := resolved.(map[string]any)
	if !ok {
		return nil, BodyNone, nil
	}
	content, _ := body["content"].(map[string]any)
	if len(content) == 0 {
		return nil, BodyNone, nil
	}
	if !methodTakesBody(method) {
		return nil, BodyNone, nil
	}
	jsonMedia, ok := content["application/json"]
	if !ok {
		return nil, BodyRaw, nil
	}
	media, ok := jsonMedia.(map[string]any)
	if !ok {
		return nil, BodyRaw, nil
	}
	schema, err := p.schemaOf(media["schema"])
	if err != nil {
		return nil, BodyNone, fmt.Errorf("request body %v", err)
	}
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return nil, BodyRaw, nil
	}
	required := map[string]bool{}
	if rq, ok := schema["required"].([]any); ok {
		for _, r := range rq {
			if s, ok := r.(string); ok {
				required[s] = true
			}
		}
	}
	var out []Arg
	for _, name := range sortedKeys(props) {
		field, err := p.schemaOf(props[name])
		if err != nil {
			return nil, BodyNone, fmt.Errorf("body field %q %v", name, err)
		}
		desc, _ := field["description"].(string)
		out = append(out, Arg{
			Name:        name,
			In:          InBody,
			Type:        field["type"],
			Required:    required[name],
			Description: desc,
			Schema:      rawSchema(field),
		})
	}
	return out, BodyJSON, nil
}

func dedupeArgs(in []Arg) []Arg {
	seen := make(map[string]ArgIn, len(in))
	out := make([]Arg, 0, len(in))
	for _, a := range in {
		if prev, dup := seen[a.Name]; dup {
			if prev == a.In {
				continue // the same parameter, restated
			}
			out = append(out, a)
			continue
		}
		seen[a.Name] = a.In
		out = append(out, a)
	}
	return out
}

func rawSchema(schema map[string]any) json.RawMessage {
	if len(schema) == 0 {
		return nil
	}
	if len(schema) == 1 {
		if _, only := schema["type"]; only {
			return nil
		}
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil
	}
	return raw
}

func collectTags(tags opTags) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, list := range tags {
		for _, t := range list {
			if _, dup := seen[t]; dup {
				continue
			}
			seen[t] = struct{}{}
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func boolOf(v any) bool {
	b, _ := v.(bool)
	return b
}

const (
	// An admin waits on this, so it fails rather than spins.
	specBudget = 20 * time.Second
	// Generous — Stripe's spec is ~6 MB — because the operations CAP is what stops a
	// huge document becoming a huge catalog.
	maxSpecBytes = 32 << 20
)

// A spec URL is tenant-supplied, so it gets the SSRF guard and the allowlist.
func FetchSpec(ctx context.Context, specURL string) (SpecImport, error) {
	do, ok := currentDoer()
	if !ok {
		return SpecImport{}, fmt.Errorf("this deployment cannot fetch a spec: no guarded HTTP caller is wired. Paste the document instead")
	}
	u, err := url.Parse(strings.TrimSpace(specURL))
	if err != nil || u.Host == "" || !strings.EqualFold(u.Scheme, "https") {
		return SpecImport{}, fmt.Errorf("the spec address must be an https:// URL")
	}
	ctx, cancel := context.WithTimeout(ctx, specBudget)
	defer cancel()

	status, body, _, err := do(ctx, http.MethodGet, u.String(),
		map[string]string{"Accept": "application/json, application/yaml, text/yaml, */*"},
		nil, int(specBudget/time.Millisecond), maxSpecBytes)
	if err != nil {
		return SpecImport{}, fmt.Errorf("could not fetch the spec: %v", err)
	}
	if status < 200 || status > 299 {
		return SpecImport{}, fmt.Errorf("the spec address answered %d", status)
	}
	return ParseSpec(body)
}
