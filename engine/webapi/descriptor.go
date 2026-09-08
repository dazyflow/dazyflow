// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package webapi

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

type AuthKind string

const (
	AuthNone   AuthKind = "none"
	AuthBearer AuthKind = "bearer"
	AuthHeader AuthKind = "header"
)

// The VALUE is never here, so this package holds no secrets and is safe to log.
type Auth struct {
	Kind   AuthKind
	Header string
}

type ArgIn string

const (
	InPath   ArgIn = "path"
	InQuery  ArgIn = "query"
	InHeader ArgIn = "header"
	InBody   ArgIn = "body"
)

type BodyMode string

const (
	BodyNone BodyMode = "none"
	BodyJSON BodyMode = "json"
	BodyRaw  BodyMode = "raw"
)

// Load-bearing: a field renamed without its tag orphans every stored descriptor.
type Arg struct {
	Name        string          `json:"name"`
	In          ArgIn           `json:"in"`
	Type        any             `json:"type,omitempty"`
	Required    bool            `json:"required,omitempty"`
	Label       string          `json:"label,omitempty"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
}

type Operation struct {
	ID          string   `json:"id"`
	Title       string   `json:"title,omitempty"`
	Method      string   `json:"method"`
	Path        string   `json:"path"`
	Summary     string   `json:"summary,omitempty"`
	Description string   `json:"description,omitempty"`
	Args        []Arg    `json:"args,omitempty"`
	BodyMode    BodyMode `json:"body_mode,omitempty"`
	Deprecated  bool     `json:"deprecated,omitempty"`
}

// Typed by an admin, not validated by a schema; a pasted paragraph breaks layout.
func (o Operation) DisplayName() string {
	return displayName(o.Title, o.ID)
}

const maxDisplayNameLen = 60

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

type Descriptor struct {
	Tenant string
	Name   string
	Label  string
	// Admin-only: it used to be overridable through the tenant's connection, which
	// put it behind secret:write instead.
	BaseURL      string
	Integration  string
	Description  string
	Auth         Auth
	Operations   []Operation
	TimeoutMS    int
	MaxBodyBytes int
	// A data: URI only: the app's CSP will not load a third-party image.
	Logo   string
	Runner RunnerReach
}

// Reaches a service with no public address, which Dazyflow otherwise refuses to
// dial. The COST has to be stated: a runner call bypasses the guarded Doer, so it
// has no SSRF guard (that is the point), no egress allowlist and no rate limit.
type RunnerReach struct {
	Tags []string `json:"tags,omitempty"`
}

func (r RunnerReach) Enabled() bool { return len(r.Tags) > 0 }

const maxRunnerTags = 8

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
		if t != strings.ToLower(strings.TrimSpace(t)) {
			return fmt.Errorf("runner tag %q must be lower-case and unpadded", t)
		}
	}
	return nil
}

// Makes a tag typed "Linux " match a machine labelled linux. The same three lines
// live in drops/runner, deliberately not shared: one reads a step's params, the
// other an admin's form.
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

func (d Descriptor) DisplayName() string {
	return displayName(d.Label, d.Name)
}

const (
	DefaultTimeoutMS    = 30000
	DefaultMaxBodyBytes = 10 << 20
)

// Refused rather than silently demoted: an argument called `token` would be
// overwritten by the connection's credential, invisibly at run time.
var reservedParams = map[string]bool{
	"base_url": true, "token": true,
	"timeout_ms": true, "expect_status": true,
	overlayPort: true, "request_body": true,
	"status": true, "response_body": true, "headers": true,
	"pass": true, "out": true,
}

// A property HTTP DECLARES, which is why this can set Idempotent honestly.
var idempotentMethods = map[string]bool{
	"GET": true, "HEAD": true, "PUT": true, "DELETE": true,
}

var knownMethods = map[string]bool{
	"GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true,
}

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

func methodTakesBody(method string) bool {
	switch method {
	case "POST", "PUT", "PATCH":
		return true
	default:
		return false
	}
}

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
	if u.RawQuery != "" || u.ForceQuery {
		return fmt.Errorf("base URL %q must not carry a query string — operation paths are joined onto it", raw)
	}
	if u.Fragment != "" {
		return fmt.Errorf("base URL %q must not carry a #fragment", raw)
	}
	return nil
}

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

func (d Descriptor) operationIDs() []string {
	out := make([]string, 0, len(d.Operations))
	for _, op := range d.Operations {
		out = append(out, StepID(d.Name, op.ID))
	}
	sort.Strings(out)
	return out
}

func StepID(catalog, operation string) string {
	return "api:" + catalog + ":" + operation
}
