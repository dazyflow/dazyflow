// SPDX-FileCopyrightText: 2026 Joachim Klahr
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

// The second front end onto the descriptor.
//
// A hand-built step and an imported operation are the same object — that is the
// decision the design note exists to record — so this file produces exactly the
// []Operation the admin form produces, and everything downstream (validation,
// manifest synthesis, the executor) is untouched.
//
// ── Why a parser of our own rather than an OpenAPI library ──
//
// Because the feature is "import OPERATIONS", not "register a spec". A library
// validates a document as a whole and refuses it as a whole; a real-world spec
// with one operation we cannot express would then block the fifty we can. Here
// an operation that does not fit is SKIPPED with a warning naming it, and the
// admin imports the rest — which is the same curation stance the operations cap
// takes.
//
// It also makes the note's SSRF rule structural instead of configured: this
// parser has no fetcher in it, so an external $ref cannot be followed even by
// mistake. A library's equivalent is a flag someone can flip.
//
// The cost is honest: 3.1's JSON-Schema unions, discriminators and servers
// variables get a conservative reading rather than a complete one. Where that
// bites, it bites as a warning, never as a wrong request.

// SpecFormat is how far this parser will go.
//
// Swagger 2.0 is REFUSED rather than half-read. It is still everywhere, its
// parameter model differs (`in: body` is one parameter carrying a schema, not a
// set of fields), and reading it as if it were 3.x would produce operations
// that look right and send the wrong request. "We crashed on it" and "we
// silently mangled it" are both worse than saying so.
const swagger2Message = "this is a Swagger 2.0 document, which this importer does not read. " +
	"Convert it to OpenAPI 3 (most tools can) and import that."

// ImportWarning is one thing the parser declined to do, named well enough that
// an admin can decide whether they care.
//
// Every warning corresponds to something NOT imported. A parse that returns
// operations and warnings has done its job: the operations are importable and
// the warnings say what was left behind and why.
type ImportWarning struct {
	// Where is the operation this is about — "GET /orders/{id}" — or "" for a
	// warning about the document itself.
	Where  string `json:"where,omitempty"`
	Reason string `json:"reason"`
}

// SpecImport is everything a parsed document offers the admin form.
type SpecImport struct {
	// Title and Description are the API's own, offered as defaults for the
	// catalog's label and blurb.
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	// BaseURL is servers[0].url when the document declares an absolute one.
	// Relative server URLs ("/v1") are common and cannot stand alone, so they
	// are reported as a warning and the admin supplies the address.
	BaseURL string `json:"base_url,omitempty"`
	// Operations are importable as they stand: each has already been through
	// the same validation Save will apply.
	Operations []Operation `json:"operations"`
	// Tags are every tag the imported operations carry, sorted, so the form can
	// offer selection by tag — the note's "pick by tag, path prefix or
	// operation".
	Tags []string `json:"tags,omitempty"`
	// OperationTags maps operation id to the tags the spec gave it, so the
	// picker can offer "select everything tagged billing" without Operation —
	// a STORED type, where a new field changes what every persisted descriptor
	// means — having to grow a field that is only useful before saving.
	OperationTags map[string][]string `json:"operation_tags,omitempty"`
	Warnings      []ImportWarning     `json:"warnings,omitempty"`
}

// opTags carries an operation's spec tags out of the parser without widening
// Operation itself, which is a STORED type: adding a field to it changes what
// every persisted descriptor means. Selection happens before anything is
// stored, so the tags only have to survive as far as the picker.
type opTags map[string][]string

// ParseSpec reads an OpenAPI 3.x document — JSON or YAML — and returns the
// operations it can express.
//
// An error means the DOCUMENT is unusable (not OpenAPI, unreadable, Swagger 2).
// Anything narrower is a warning against an operation that was skipped.
func ParseSpec(raw []byte) (SpecImport, error) {
	var doc map[string]any
	// A JSON document is valid YAML, so one parser reads both and there is no
	// format sniffing to get wrong.
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
	// Sorted so an import is deterministic: the same document produces the same
	// operation order every time, which is what makes a refresh diff stable.
	for _, path := range sortedKeys(paths) {
		item, ok := paths[path].(map[string]any)
		if !ok {
			continue
		}
		// Path-level parameters apply to every operation under it.
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

// baseURLFrom reads servers[0].url. A relative one is normal in a spec served
// beside the API it describes, and useless on its own here.
func baseURLFrom(doc map[string]any, warnings []ImportWarning) (string, []ImportWarning) {
	servers, _ := doc["servers"].([]any)
	if len(servers) == 0 {
		return "", warnings
	}
	first, _ := servers[0].(map[string]any)
	// Not named `url`: this file imports net/url, and shadowing a package name
	// in the one function that does not use it is how the next edit here goes
	// wrong.
	addr, _ := first["url"].(string)
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", warnings
	}
	// Server variables ({region}.api.example.com) would need values this
	// importer has no way to ask for. Reported rather than guessed.
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

// maxRefDepth bounds $ref following. A spec that refers to itself — directly or
// through a chain — would otherwise spin here, and a hostile one is a plausible
// way to try it.
const maxRefDepth = 20

// resolve follows internal $refs. External ones are refused BY CONSTRUCTION:
// there is no fetcher in this package, which is the note's rule made structural
// rather than configured.
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
		// An external $ref is a URL this parser would have to fetch, which is
		// an SSRF vector wearing a document's clothes.
		return nil, fmt.Errorf("refers to %q, outside this document — external references are not followed", ref)
	}
	cur := any(p.doc)
	for _, seg := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		// JSON Pointer escapes, in the order the RFC specifies.
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

// schemaOf resolves a schema node and merges a shallow allOf.
//
// Shallow on purpose: allOf's real semantics are "satisfies all of these", which
// for object schemas is a property union, and that is what is implemented. A
// member that is not an object schema (a bare $ref to a scalar, a oneOf) is
// left alone rather than approximated — the argument keeps whatever type it
// already had, and the full schema is carried verbatim on Arg.Schema regardless.
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
	// The allOf node's own siblings win over its members, which is how a spec
	// narrows an inherited schema.
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

// operation turns one method under one path into an Operation, or explains why
// it cannot.
func (p *specParser) operation(method, path string, raw map[string]any, shared []Arg) (Operation, []string, error) {
	op := Operation{
		Method:     method,
		Path:       path,
		Deprecated: boolOf(raw["deprecated"]),
	}
	// OpenAPI has two prose fields and the descriptor has three, so the mapping
	// is a choice rather than a copy.
	//
	// `summary` is a short line naming the operation, which is exactly what
	// Title is for: it captions the palette row and the node. Summary is left
	// EMPTY so the subtitle falls back to "GET /orders/{id}" — the call itself,
	// which complements the caption instead of repeating it. Setting both from
	// `summary` put the same sentence on the card twice.
	//
	// `description` is the paragraph, and it also has to absorb `summary` when
	// there is no description: Description is what the flow generator grounds
	// on, and losing the one human sentence a spec wrote would make an imported
	// operation harder to find than a hand-built one.
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

	// Deduplicate by name, keeping the FIRST — a path-level parameter that the
	// operation restates, which specs do routinely. A genuine collision between
	// two different locations is caught below by the descriptor's own rule.
	op.Args = dedupeArgs(args)

	// Validated here rather than at Save so a spec's one unusable operation is
	// a skipped row with a reason, not a failed import of the other fifty.
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

// operationID prefers the spec's own operationId, because it is the stable
// identity a refresh matches on and the one thing a spec author controls.
func operationID(raw map[string]any, method, path string) (string, error) {
	if id, _ := raw["operationId"].(string); strings.TrimSpace(id) != "" {
		slug := slugID(id)
		if err := validName(slug); err != nil {
			return "", fmt.Errorf("operationId %q cannot be used as a step id: %v", id, err)
		}
		return slug, nil
	}
	// No operationId is legal and common. Deriving from method and path is
	// deterministic, which matters more than pretty: the same document must
	// produce the same id next time or a refresh would read as remove-and-add.
	derived := slugID(strings.ToLower(method) + "_" + path)
	if err := validName(derived); err != nil {
		return "", fmt.Errorf("no operationId, and one could not be derived from %s %s", method, path)
	}
	return derived, nil
}

// slugID reduces a spec's identifier to the descriptor's name rules.
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
			// Everything else — /, {, }, ., spaces — collapses to one separator,
			// so "/orders/{id}/lines" is orders_id_lines rather than a run of
			// underscores.
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

// parameters reads path/query/header parameters. Cookie parameters are skipped:
// there is nowhere to put them in a described call, and quietly sending one as a
// header would be worse than not sending it.
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
			Name: name,
			In:   loc,
			Type: schema["type"],
			// A path parameter is required by definition; the spec is allowed to
			// say so redundantly, and some forget.
			Required:    loc == InPath || boolOf(m["required"]),
			Description: desc,
			Schema:      rawSchema(schema),
		})
	}
	return out
}

// requestBody reads a JSON request body into per-field arguments.
//
// Only application/json, and only an object schema. A body that is an array, a
// scalar, or any other media type has no field-per-argument reading, so it
// becomes BodyRaw — the `request_body` port — which is a working step rather
// than a skipped one.
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
		// A GET with a requestBody is legal OpenAPI and unsendable here. The
		// operation is still worth importing without it.
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
		// An object with no declared properties, an array, a free-form blob —
		// all of them are "send what you're given".
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

// dedupeArgs keeps the first argument of each name. A path-level parameter
// restated on the operation is the common case and is not a conflict; a name
// used in two DIFFERENT locations is, and validate() reports it.
func dedupeArgs(in []Arg) []Arg {
	seen := make(map[string]ArgIn, len(in))
	out := make([]Arg, 0, len(in))
	for _, a := range in {
		if prev, dup := seen[a.Name]; dup {
			if prev == a.In {
				continue // the same parameter, restated
			}
			// Two locations, one name: kept so validate() refuses the operation
			// with its own message rather than this file inventing a winner.
			out = append(out, a)
			continue
		}
		seen[a.Name] = a.In
		out = append(out, a)
	}
	return out
}

// rawSchema carries a schema verbatim onto Arg.Schema so an enum or a pattern
// survives Type having reduced it to a word. Nil when there is nothing to keep.
func rawSchema(schema map[string]any) json.RawMessage {
	if len(schema) == 0 {
		return nil
	}
	// Only worth carrying when it says more than the type already does.
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

// ── Fetching a spec ─────────────────────────────────────────────────────────

const (
	// specBudget bounds the whole fetch. A spec is a document served beside an
	// API, not a slow computation; an admin waiting on a paste-or-fetch form
	// should be told it failed rather than left watching a spinner.
	specBudget = 20 * time.Second
	// maxSpecBytes caps what will be read. Stripe's spec is ~6 MB and GitHub's
	// larger, so this is generous on purpose — the operations CAP is what stops
	// a huge document becoming a huge catalog, and refusing to read one outright
	// would only push the admin to paste it instead.
	maxSpecBytes = 32 << 20
)

// FetchSpec retrieves a spec over the guarded caller and parses it.
//
// The URL goes through the same Doer a step's call does, which is the note's
// rule: a spec URL is tenant-supplied, so it gets the SSRF dial guard, the
// egress allowlist and the response cap exactly as any other tenant-supplied
// address would. Fetching it with a bare http.Client here would be the one
// unguarded request in the package.
func FetchSpec(ctx context.Context, specURL string) (SpecImport, error) {
	do, ok := currentDoer()
	if !ok {
		return SpecImport{}, fmt.Errorf("this deployment cannot fetch a spec: no guarded HTTP caller is wired. Paste the document instead")
	}
	u, err := url.Parse(strings.TrimSpace(specURL))
	if err != nil || u.Host == "" || !strings.EqualFold(u.Scheme, "https") {
		// https only, at the same boundary a catalog's base URL is held to. A
		// spec fetched over cleartext is a spec an intermediary can rewrite,
		// and what it would rewrite is where every step of this catalog calls.
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
