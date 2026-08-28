// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package webapi turns a described HTTP API into steps.
//
// One tenant-owned Descriptor names a base URL and a list of Operations; each
// operation becomes a step (`api:<catalog>:<operation>`) whose arguments are
// ports and params, exactly as an MCP tool does. The point is the difference
// from `http_request`: that step can call anything, and therefore describes
// nothing — no name in the palette, no required fields, no typed pins, and
// nothing the flow generator can ground on. An operation described once is a
// step an author (or the generator) can use without knowing the API.
//
// A Descriptor is data, and where it CAME from is not this package's business:
// an admin form filling one in by hand and an OpenAPI import parsing one out of
// a spec produce the same object. That is the whole design — see
// docs/own-service-steps-design.md. Only Arg.In has no counterpart in MCP's
// flat argument object, and it exists because HTTP splits one call's arguments
// across the path, the query, the headers and the body.
package webapi

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// AuthKind is how a catalog presents its credential. The three kinds are the
// ones daemon.MCPServer already stores, deliberately: an org configuring their
// own service should not meet a different vocabulary here, and the store this
// grows in commit 2 is modelled on that one.
type AuthKind string

const (
	AuthNone   AuthKind = "none"
	AuthBearer AuthKind = "bearer"
	AuthHeader AuthKind = "header"
)

// Auth names the credential shape. The credential VALUE is not here and never
// is: it arrives at run time in the job's params, put there by the engine's
// connection injection from the tenant's stored connection (see manifest.go).
// So this package holds no secrets, needs no sealing, and a Descriptor is safe
// to log.
type Auth struct {
	Kind AuthKind
	// Header is the header name for AuthHeader — a vendor's own
	// `X-API-Key`-style scheme. Ignored for the other kinds.
	Header string
}

// ArgIn is where one argument travels in the request. It is the field that
// makes this a different descriptor from an MCP tool rather than a reuse of
// one: assembling the call needs to know which arguments are path segments,
// which are query parameters, which are headers, and which are body fields.
type ArgIn string

const (
	InPath   ArgIn = "path"
	InQuery  ArgIn = "query"
	InHeader ArgIn = "header"
	InBody   ArgIn = "body"
)

// BodyMode is how an operation's request body is built.
type BodyMode string

const (
	// BodyNone sends no body.
	BodyNone BodyMode = "none"
	// BodyJSON marshals the operation's InBody arguments into a JSON object.
	BodyJSON BodyMode = "json"
	// BodyRaw sends whatever is wired into the `request_body` port verbatim —
	// the escape hatch for a body no argument list describes (a nested object,
	// an array, XML, a pre-rendered payload).
	BodyRaw BodyMode = "raw"
)

// Arg is one declared argument of an operation.
//
// The JSON tags are load-bearing: this type is both the admin API's request
// shape and the stored form of a catalog (daemon.WebAPI persists operations as
// JSON), so a field renamed without its tag would silently orphan every stored
// descriptor. Tagged explicitly rather than left to Go's field names for
// exactly that reason.
type Arg struct {
	Name string `json:"name"`
	In   ArgIn  `json:"in"`
	// Type is the JSON Schema type: a string, or an array for a union like
	// ["string","null"]. It decides whether the argument earns a port and how
	// its value is coerced into a JSON body.
	Type        any    `json:"type,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
	// Schema, when set, is this argument's full JSON Schema, rendered verbatim
	// by the params form. It is how an enum, a pattern, or a nested body object
	// keeps its detail after Type has reduced it to a word.
	Schema json.RawMessage `json:"schema,omitempty"`
}

// Operation is one HTTP call, described. Its JSON tags are load-bearing for the
// reason Arg's are.
type Operation struct {
	// ID is the step-id suffix: api:<catalog>:<ID>. An OpenAPI import takes it
	// from operationId. Renaming it is not an edit — it is a new step, and
	// flows referencing the old id stop resolving.
	ID string `json:"id"`
	// Title is the operation's display name — "Create an issue" where ID is
	// create_issue. Optional, and NOT an identifier: the step id keeps using
	// ID, so naming an operation re-captions its step without moving anything
	// a flow references.
	Title  string `json:"title,omitempty"`
	Method string `json:"method"`
	// Path is joined onto the catalog's base URL and may carry {placeholders},
	// each of which must have a required InPath argument of the same name.
	Path        string   `json:"path"`
	Summary     string   `json:"summary,omitempty"`
	Description string   `json:"description,omitempty"`
	Args        []Arg    `json:"args,omitempty"`
	BodyMode    BodyMode `json:"body_mode,omitempty"`
	// Deprecated marks an operation the API itself has deprecated. Surfaced in
	// the step's description, since core.Manifest has nowhere better yet.
	Deprecated bool `json:"deprecated,omitempty"`
}

// DisplayName is what to caption this operation with: its title when one was
// given, its id otherwise.
//
// Bounded for the same reason a palette row is: a name is typed by an admin,
// not validated by a schema, and a paragraph pasted into it would break the
// layout rather than inform anyone. The summary is where a sentence belongs,
// and it is already shown as the step's subtitle.
func (o Operation) DisplayName() string {
	return displayName(o.Title, o.ID)
}

// maxDisplayNameLen bounds a caption to what a palette row can show.
const maxDisplayNameLen = 60

// displayName trims a typed title to one short line, falling back to the id.
func displayName(title, id string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return id
	}
	if i := strings.IndexAny(title, "\r\n"); i >= 0 {
		title = strings.TrimSpace(title[:i])
	}
	if r := []rune(title); len(r) > maxDisplayNameLen {
		title = strings.TrimSpace(string(r[:maxDisplayNameLen])) + "…"
	}
	if title == "" {
		return id
	}
	return title
}

// Descriptor is one tenant-owned catalog of operations.
type Descriptor struct {
	// Tenant owns this catalog. Empty is refused: unlike an MCP server there is
	// no operator-configured instance-wide population here, and a catalog that
	// resolves for nobody is a configuration mistake worth reporting.
	Tenant string
	// Name is what steps are referenced by: api:<Name>:<operation>.
	Name string
	// Label is what a human called this catalog: "Order service". Display
	// only — Name carries the identity — so it is free to change.
	//
	// Empty falls back to Name, which is a slug and reads like one. Passing it
	// in is what stops a step being captioned "order-service — get_order".
	Label string
	// BaseURL is the service address, typed at import time and required.
	//
	// The catalog owns it, and only an admin can change it: it used to be
	// overridable through the tenant's connection, which put it behind
	// secret:write instead — see connectionFields for why that is gone. A node
	// param still overrides it for the one-step exception.
	BaseURL string
	// Integration is the palette/Apps grouping label. Empty falls back to Name.
	Integration string
	// Description is what this service IS, in the org's own words — shown on the
	// catalog's page under Apps, where the reader is someone deciding whether
	// the app is the one they want rather than someone reading a step.
	//
	// Separate from an operation's Description, which is about one call. This
	// one is about the service, and it is the only prose about an org's own API
	// that nobody else could write: a built-in integration's blurb is curated in
	// the app, and there is nowhere to curate an org's.
	Description string
	Auth        Auth
	Operations  []Operation
	// TimeoutMS bounds one call. Zero means DefaultTimeoutMS.
	TimeoutMS int
	// MaxBodyBytes caps a response. Zero means DefaultMaxBodyBytes.
	MaxBodyBytes int
	// Logo is the catalog's brand mark as a data: URI, and becomes every
	// operation's Manifest.BrandLogo — the image the node card, the palette and
	// the Apps page draw instead of the category glyph.
	//
	// A data: URI and nothing else, enforced by Validate. The app's CSP does not
	// load third-party images (see icon.go), so an https URL here would render
	// as a broken image rather than as a logo, and the descriptor is the last
	// boundary that can say so out loud. Empty is normal and means the globe.
	Logo string
	// Runner, when set, moves the last mile onto one of the org's own machines.
	// Empty — the normal case — means the daemon makes the call itself.
	Runner RunnerReach
}

// RunnerReach says to perform this catalog's calls from inside the org's
// network instead of from the daemon.
//
// It is one field rather than a fourth product because nothing else changes:
// the descriptor, the synthesized manifests, the ports and the connection are
// identical, and an HTTP request is already the one-shot shape a runner task
// has. See engine/webapi/runner.go.
//
// What it BUYS is the only thing phase 1 could not offer: a service with no
// public address. Dazyflow refuses to dial private ranges by design, so an
// internal API was previously reachable only by writing a script per operation.
//
// What it COSTS has to be stated plainly, because it is the trade a reader
// would otherwise have to discover from behaviour: a call made from a runner
// does not pass through the daemon's guarded Doer, so it has none of that
// path's protections — no SSRF dial guard (that is the point), no per-tenant
// egress allowlist, and no per-host rate limit. The response cap survives,
// re-imposed inside the script, because a runner streaming a gigabyte back
// through a task row is a different failure from the one the allowlist
// prevents. Setting this is therefore an admin decision about a machine the org
// already trusts to run arbitrary scripts, and it is gated on the same
// permission as the rest of the catalog.
type RunnerReach struct {
	// Tags select the machine: one carrying ALL of them. A machine's own name is
	// one of its tags, so pinning a catalog to a single box is a one-tag list —
	// the same vocabulary the run_on_runner step uses, deliberately, so there is
	// no second targeting concept to learn.
	//
	// Several machines matching is fine and needs no caveat: an HTTP request is
	// stateless, so any of them is as good as any other.
	Tags []string `json:"tags,omitempty"`
}

// Enabled reports whether this catalog is reached through a runner.
func (r RunnerReach) Enabled() bool { return len(r.Tags) > 0 }

// maxRunnerTags bounds the target. Every tag NARROWS the set of machines, so a
// long list is a catalog that will match nothing and fail at run time with
// "no runner carries all of these" — which is correct but late. Eight is more
// than any real targeting rule needs.
const maxRunnerTags = 8

// maxRunnerTagLen bounds one tag, matching what a runner may be labelled with.
const maxRunnerTagLen = 64

func (r RunnerReach) validate() error {
	if len(r.Tags) > maxRunnerTags {
		return fmt.Errorf("%d runner tags is more than one catalog may target (max %d) — every tag narrows the machines that match",
			len(r.Tags), maxRunnerTags)
	}
	for _, t := range r.Tags {
		if t == "" {
			return fmt.Errorf("runner tag: empty")
		}
		if len(t) > maxRunnerTagLen {
			return fmt.Errorf("runner tag %q is longer than %d characters", t, maxRunnerTagLen)
		}
		// NormalizeRunnerTags is expected to have run first, so anything still
		// upper-case or padded got here around the front door and would silently
		// match no machine — runner tags are lower-cased on the machine's side.
		if t != strings.ToLower(strings.TrimSpace(t)) {
			return fmt.Errorf("runner tag %q must be lower-case and unpadded", t)
		}
	}
	return nil
}

// NormalizeRunnerTags lower-cases, trims, drops blanks and de-duplicates, which
// is what makes a tag typed "Linux " in the admin form match a machine labelled
// linux.
//
// The same three lines live in drops/runner's targetTags, deliberately not
// shared: that one reads a step's params and this one an admin's form, and the
// only thing they have in common is the rule itself, which is one sentence long
// and stated in Runner.Tags (daemon/runners.go). A shared helper would mean
// engine/webapi and drops/runner reaching for each other to save six lines.
func NormalizeRunnerTags(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// DisplayName is what to caption this catalog with: its label when one was
// given, its id otherwise. See Label.
func (d Descriptor) DisplayName() string {
	return displayName(d.Label, d.Name)
}

const (
	// DefaultTimeoutMS matches http_request's default: the same call, so the
	// same patience.
	DefaultTimeoutMS = 30000
	// DefaultMaxBodyBytes matches http_request's default response cap.
	DefaultMaxBodyBytes = 10 << 20
)

// reservedParams are the names this package's own manifest spends. An argument
// carrying one of these is REFUSED at validation rather than silently demoted
// to params-only, because the collision is invisible at run time: an argument
// called `token` would be overwritten by the connection's credential, and one
// called `status` would be shadowed on the way out. An import that hits this
// has to rename the argument, and the message says so.
var reservedParams = map[string]bool{
	"base_url": true, "token": true,
	"timeout_ms": true, "expect_status": true,
	overlayPort: true, "request_body": true,
	"status": true, "response_body": true, "headers": true,
	"pass": true, "out": true,
}

// idempotentMethods are idempotent per RFC 9110 §9.2.2 — a property HTTP
// DECLARES, which is why a described API can set core.Manifest.Idempotent
// honestly where synthesizeManifest in engine/mcp has to hardcode false.
var idempotentMethods = map[string]bool{
	"GET": true, "HEAD": true, "PUT": true, "DELETE": true,
}

var knownMethods = map[string]bool{
	"GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true,
}

// Validate reports whether a descriptor describes calls that can actually be
// assembled. It runs at registration, before any step exists, so the whole
// catalog is refused rather than half-filed — and every message is written to
// be shown to the admin who pasted the spec.
func (d Descriptor) Validate() error {
	if d.Tenant == "" {
		return fmt.Errorf("web api catalog: Tenant required (a catalog with no tenant resolves for nobody)")
	}
	if err := validName(d.Name); err != nil {
		return fmt.Errorf("web api catalog: %w", err)
	}
	if d.BaseURL != "" {
		if err := validBaseURL(d.BaseURL); err != nil {
			return fmt.Errorf("web api catalog %q: %w", d.Name, err)
		}
	}
	switch d.Auth.Kind {
	case "", AuthNone, AuthBearer:
	case AuthHeader:
		if strings.TrimSpace(d.Auth.Header) == "" {
			return fmt.Errorf("web api catalog %q: auth kind %q needs a header name", d.Name, AuthHeader)
		}
	default:
		return fmt.Errorf("web api catalog %q: unknown auth kind %q", d.Name, d.Auth.Kind)
	}
	if d.Logo != "" && !strings.HasPrefix(d.Logo, "data:") {
		// Not a user-facing message in practice — the only writer is
		// ResolveLogo, which emits data: or nothing — so this is here to keep
		// that true for the next writer.
		return fmt.Errorf("web api catalog %q: logo must be an inlined data: URI", d.Name)
	}
	if err := d.Runner.validate(); err != nil {
		return fmt.Errorf("web api catalog %q: %w", d.Name, err)
	}
	if len(d.Operations) == 0 {
		return fmt.Errorf("web api catalog %q: no operations selected", d.Name)
	}
	seen := make(map[string]bool, len(d.Operations))
	for _, op := range d.Operations {
		if err := op.validate(); err != nil {
			return fmt.Errorf("web api catalog %q: %w", d.Name, err)
		}
		if seen[op.ID] {
			return fmt.Errorf("web api catalog %q: operation %q declared twice", d.Name, op.ID)
		}
		seen[op.ID] = true
	}
	return nil
}

func (op Operation) validate() error {
	if err := validName(op.ID); err != nil {
		return fmt.Errorf("operation: %w", err)
	}
	method := strings.ToUpper(op.Method)
	if !knownMethods[method] {
		return fmt.Errorf("operation %q: method %q is not one this catalog can call", op.ID, op.Method)
	}
	if !strings.HasPrefix(op.Path, "/") {
		return fmt.Errorf("operation %q: path %q must start with /", op.ID, op.Path)
	}
	switch op.BodyMode {
	case "", BodyNone, BodyJSON, BodyRaw:
	default:
		return fmt.Errorf("operation %q: unknown body mode %q", op.ID, op.BodyMode)
	}

	args := make(map[string]Arg, len(op.Args))
	for _, a := range op.Args {
		if a.Name == "" {
			return fmt.Errorf("operation %q: an argument has no name", op.ID)
		}
		if _, dup := args[a.Name]; dup {
			// Two arguments of the same name are the collision the design note
			// calls out: a query `id` and a body `id` cannot both be set from
			// one params map, and picking a winner here would be picking it
			// silently. The importer renames one, or the admin does.
			return fmt.Errorf("operation %q: argument %q declared twice — an argument name must be unique across path, query, header and body", op.ID, a.Name)
		}
		if reservedParams[a.Name] {
			return fmt.Errorf("operation %q: argument %q collides with a name this step already uses — rename it in the catalog", op.ID, a.Name)
		}
		switch a.In {
		case InHeader:
			if !validHeaderName(a.Name) {
				return fmt.Errorf("operation %q: %q is not a usable header name", op.ID, a.Name)
			}
		case InPath, InQuery:
		case InBody:
			if op.BodyMode != BodyJSON {
				return fmt.Errorf("operation %q: argument %q is a body field but the operation's body mode is %q", op.ID, a.Name, bodyModeOrNone(op.BodyMode))
			}
		default:
			return fmt.Errorf("operation %q: argument %q has unknown location %q", op.ID, a.Name, a.In)
		}
		args[a.Name] = a
	}

	// Every {placeholder} must have a required path argument, or rendering the
	// URL would leave a literal brace in it and the call would go somewhere
	// nobody meant. The reverse too: a path argument no placeholder mentions
	// would silently never be sent.
	holders := pathPlaceholders(op.Path)
	for _, name := range holders {
		a, ok := args[name]
		if !ok {
			return fmt.Errorf("operation %q: path names {%s} but no argument declares it", op.ID, name)
		}
		if a.In != InPath {
			return fmt.Errorf("operation %q: path names {%s} but argument %q is declared in %q", op.ID, name, name, a.In)
		}
		if !a.Required {
			return fmt.Errorf("operation %q: path argument %q must be required — the URL cannot be built without it", op.ID, name)
		}
	}
	inPath := map[string]bool{}
	for _, name := range holders {
		inPath[name] = true
	}
	for _, a := range op.Args {
		if a.In == InPath && !inPath[a.Name] {
			return fmt.Errorf("operation %q: argument %q is declared in the path but the path %q does not name it", op.ID, a.Name, op.Path)
		}
	}

	if op.BodyMode == BodyJSON && !methodTakesBody(method) {
		return fmt.Errorf("operation %q: %s cannot carry a JSON body", op.ID, method)
	}
	if op.BodyMode == BodyRaw && !methodTakesBody(method) {
		return fmt.Errorf("operation %q: %s cannot carry a request body", op.ID, method)
	}
	return nil
}

func bodyModeOrNone(m BodyMode) BodyMode {
	if m == "" {
		return BodyNone
	}
	return m
}

// methodTakesBody refuses a body on the verbs where one is either meaningless
// or actively mishandled by intermediaries. GET-with-a-body exists in the wild
// and is a trap: proxies drop it, so the call succeeds having sent nothing.
func methodTakesBody(method string) bool {
	switch method {
	case "POST", "PUT", "PATCH":
		return true
	default:
		return false
	}
}

// validName bounds a catalog or operation id: it lands inside a step id that a
// graph stores and a URL path can carry, and it has to stay readable in the
// editor.
func validName(name string) error {
	if name == "" {
		return fmt.Errorf("name required")
	}
	if len(name) > 64 {
		return fmt.Errorf("name %q is too long (max 64)", name)
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return fmt.Errorf("name %q may use only letters, digits, - and _", name)
		}
	}
	return nil
}

// validBaseURL refuses what should never be dialed, before a request is ever
// built. The IP-level guard still applies at dial time (the doer carries it);
// this is the readable half, so an admin sees "must be http(s)" at save time
// instead of a failed run later. Mirrors daemon.validMCPServerURL.
//
// Also called at RUN time, on the address the connection supplied: a base URL
// is a tenant-editable value, so it cannot be validated only at import.
func validBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("base URL %q is not a URL: %v", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("base URL %q must be http:// or https://", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("base URL %q has no host", raw)
	}
	// A query or fragment on the BASE would be silently destroyed: the
	// operation path and the encoded query are appended after it, so
	// "https://x/v1?debug=1" builds "https://x/v1?debug=1/orders?id=2" — a URL
	// nobody meant. Refuse it where it can be explained instead.
	if u.RawQuery != "" || u.ForceQuery {
		return fmt.Errorf("base URL %q must not carry a query string — operation paths are joined onto it", raw)
	}
	if u.Fragment != "" {
		return fmt.Errorf("base URL %q must not carry a #fragment", raw)
	}
	return nil
}

// validHeaderName bounds a header argument's name to an RFC 9110 field name.
//
// Go's transport rejects a malformed name when the request is written, so this
// is not the only line of defence — but a catalog is validated once and its
// calls run forever, and "the import told me at save time" beats "every run
// fails with a message about the wrong layer".
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
		default:
			return false
		}
	}
	return true
}

// pathPlaceholders lists the {names} in a path template, in order of
// appearance, ignoring an unterminated brace.
func pathPlaceholders(path string) []string {
	var out []string
	for i := 0; i < len(path); i++ {
		if path[i] != '{' {
			continue
		}
		end := strings.IndexByte(path[i:], '}')
		if end < 0 {
			break
		}
		name := path[i+1 : i+end]
		if name != "" {
			out = append(out, name)
		}
		i += end
	}
	return out
}

// operationIDs lists a descriptor's operation ids, sorted — for a status view
// that must not reshuffle between refreshes.
func (d Descriptor) operationIDs() []string {
	out := make([]string, 0, len(d.Operations))
	for _, op := range d.Operations {
		out = append(out, StepID(d.Name, op.ID))
	}
	sort.Strings(out)
	return out
}

// StepID is the module id one operation is referenced by in a graph.
//
// The `api:` prefix is what keeps this catalog from ever colliding with a
// native drop id, the way `mcp:` does for MCP tools — so there is no
// Reserved-style check to forget here. It is also unchangeable once a saved
// flow references it.
func StepID(catalog, operation string) string {
	return "api:" + catalog + ":" + operation
}
