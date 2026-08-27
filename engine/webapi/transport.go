// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package webapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/internal/schemaports"
)

// Transport is the per-operation core.Transport. It holds a COPY of the
// descriptor, so an edit that re-registers the catalog cannot change what a
// call already in flight sends.
type Transport struct {
	desc     Descriptor
	op       Operation
	manifest core.Manifest
}

func (t *Transport) Manifest() core.Manifest { return t.manifest }

func (t *Transport) Execute(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	do, ok := currentDoer()
	if !ok {
		// A wiring fault, not the author's mistake: no HTTP caller was
		// installed. Reported as an error too (not just a node error) because
		// nothing about the graph can fix it.
		err := fmt.Errorf("web api steps are not available on this deployment: no guarded HTTP caller is wired")
		return errResult(job, "webapi_unwired", err.Error()), err
	}

	args, err := schemaports.Assemble(job.Params, job.Input, t.manifest.Inputs, overlayPort)
	if err != nil {
		return errResult(job, "bad_input", err.Error()), nil
	}

	req, err := t.buildRequest(args, job)
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}

	timeout := t.desc.TimeoutMS
	if v, ok := intArg(args, "timeout_ms"); ok {
		timeout = v
	}
	if timeout <= 0 {
		timeout = DefaultTimeoutMS
	}
	maxBytes := t.desc.MaxBodyBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBodyBytes
	}

	emitProgress(progress, job, 0.1, req.method+" "+req.url)

	status, body, header, err := do(ctx, req.method, req.url, req.headers, req.body, timeout, maxBytes)
	if err != nil {
		if ctx.Err() != nil {
			return errResult(job, "cancelled", ctx.Err().Error()), ctx.Err()
		}
		// The doer's errors are already classified prose from the guards it
		// runs (egress_blocked, an SSRF refusal, a body cap). Passing the
		// message through beats re-deriving a code from its text here, which
		// would couple this package to drops/net's wording.
		return errResult(job, "http", err.Error()), nil
	}

	emitProgress(progress, job, 0.7, fmt.Sprintf("received %d", status))

	if !statusAccepted(status, intSliceArg(args, "expect_status")) {
		msg := fmt.Sprintf("got %d, expected %s", status, formatExpect(intSliceArg(args, "expect_status")))
		if snippet := bodySnippet(body); snippet != "" {
			msg += ": " + snippet
		}
		// A node error with no transport error, exactly as http_request
		// classifies it: the call completed, the service said no. The run
		// reports it; a retry edge decides whether to try again.
		return errResult(job, "unexpected_status", msg), nil
	}

	contentType := header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	var inline any
	if isTextMIME(contentType) {
		inline = string(body)
	} else {
		inline = body
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"response_body": {MIME: contentType, Inline: inline},
			// A bare JSON number so a Branch's numeric comparison can test it
			// with no parse step in between. Same as http_request.
			"status":  {MIME: "application/json", Inline: status},
			"headers": {MIME: "application/json", Inline: flattenHeaders(header)},
		},
	}, nil
}

// request is one assembled call.
type request struct {
	method  string
	url     string
	headers map[string]string
	body    []byte
}

// buildRequest turns the assembled arguments into an HTTP call.
//
// The order is load-bearing in one place: the base address is resolved before
// anything else, because everything else is relative to it and an empty base is
// the single most likely misconfiguration (a catalog imported before its
// connection was filled in).
func (t *Transport) buildRequest(args map[string]any, job core.Job) (request, error) {
	base := strings.TrimSpace(stringArg(args, "base_url"))
	if base == "" {
		base = strings.TrimSpace(t.desc.BaseURL)
	}
	if base == "" {
		return request{}, fmt.Errorf("no service address: set up the %s connection, or set the base_url param", t.desc.Name)
	}
	if err := validBaseURL(base); err != nil {
		return request{}, err
	}

	path, err := t.renderPath(args)
	if err != nil {
		return request{}, err
	}
	target := strings.TrimRight(base, "/") + path

	query := url.Values{}
	headers := map[string]string{}
	bodyFields := map[string]any{}
	for _, a := range t.op.Args {
		v, present := args[a.Name]
		if !present || v == nil {
			if a.Required && a.In != InPath {
				return request{}, fmt.Errorf("%s is required", a.Name)
			}
			continue
		}
		switch a.In {
		case InPath:
			// Already rendered into the path.
		case InQuery:
			s, ok := scalarString(v)
			if !ok {
				return request{}, fmt.Errorf("%s must be a single value, not %T", a.Name, v)
			}
			if s == "" && !a.Required {
				continue
			}
			query.Set(a.Name, s)
		case InHeader:
			s, ok := scalarString(v)
			if !ok {
				return request{}, fmt.Errorf("%s must be a single value, not %T", a.Name, v)
			}
			if s == "" && !a.Required {
				continue
			}
			// A header VALUE arrives at run time from a param or an upstream
			// node, so it is the one part of the request an author (or a
			// compromised upstream) can put a newline in. Go's transport would
			// reject it when writing the request; refusing it here makes the
			// message name the argument instead of the wire format.
			if strings.ContainsAny(s, "\r\n") {
				return request{}, fmt.Errorf("%s must not contain a line break", a.Name)
			}
			headers[a.Name] = s
		case InBody:
			// Coerced back to the declared type. This matters because a value
			// arriving over a port is text — numbers travel as text on every
			// port in the product — and a JSON body whose schema says number
			// must not carry "42". (engine/mcp has the same gap, uncorrected:
			// its tool arguments are handed over as whatever the port gave.
			// Worth fixing there separately, not by widening this commit.)
			coerced, err := coerceToType(v, a.Type)
			if err != nil {
				return request{}, fmt.Errorf("%s: %v", a.Name, err)
			}
			bodyFields[a.Name] = coerced
		}
	}
	if q := query.Encode(); q != "" {
		target += "?" + q
	}

	var body []byte
	switch t.op.BodyMode {
	case BodyJSON:
		raw, err := json.Marshal(bodyFields)
		if err != nil {
			return request{}, fmt.Errorf("build JSON body: %v", err)
		}
		body = raw
		headers["Content-Type"] = "application/json"
	case BodyRaw:
		if ref, ok := job.Input[rawBodyPort]; ok && ref.Inline != nil {
			raw, err := rawBody(ref.Inline)
			if err != nil {
				return request{}, err
			}
			body = raw
		}
		if _, set := headers["Content-Type"]; !set && body != nil {
			headers["Content-Type"] = "application/json"
		}
	}

	if _, set := headers["Accept"]; !set {
		headers["Accept"] = "application/json"
	}
	if err := t.applyAuth(args, headers); err != nil {
		return request{}, err
	}
	// A stable Idempotency-Key on the verbs HTTP does not make idempotent, so a
	// retry whose response was lost dedupes on any service honoring the
	// convention. Mirrors http_request and webhook_send; a service that ignores
	// the header is unaffected. Never overrides one the operation declared.
	method := strings.ToUpper(t.op.Method)
	if method == http.MethodPost || method == http.MethodPatch {
		if _, set := headers["Idempotency-Key"]; !set {
			headers["Idempotency-Key"] = job.IdempotencyKey()
		}
	}

	return request{method: method, url: target, headers: headers, body: body}, nil
}

// renderPath substitutes the path template's placeholders.
//
// Each value is path-escaped. That is the difference between an id and a path
// traversal: an argument of "../../admin" must reach the service as a segment
// it can reject, not as a URL that walked up out of the intended collection.
func (t *Transport) renderPath(args map[string]any) (string, error) {
	out := t.op.Path
	for _, name := range pathPlaceholders(t.op.Path) {
		v, ok := args[name]
		if !ok || v == nil {
			return "", fmt.Errorf("%s is required (it is part of the address)", name)
		}
		s, ok := scalarString(v)
		if !ok {
			return "", fmt.Errorf("%s must be a single value, not %T", name, v)
		}
		if strings.TrimSpace(s) == "" {
			return "", fmt.Errorf("%s is required (it is part of the address)", name)
		}
		out = strings.ReplaceAll(out, "{"+name+"}", url.PathEscape(s))
	}
	return out, nil
}

func (t *Transport) applyAuth(args map[string]any, headers map[string]string) error {
	token := strings.TrimSpace(stringArg(args, "token"))
	switch t.desc.Auth.Kind {
	case "", AuthNone:
		return nil
	case AuthBearer:
		if token == "" {
			return fmt.Errorf("no credential: set up the %s connection", t.desc.Name)
		}
		headers["Authorization"] = "Bearer " + token
	case AuthHeader:
		if token == "" {
			return fmt.Errorf("no credential: set up the %s connection", t.desc.Name)
		}
		headers[t.desc.Auth.Header] = token
	}
	return nil
}

// statusAccepted decides success. Empty expect means 2xx, matching
// http_request's default so the two steps agree about what a failure is.
func statusAccepted(status int, expect []int) bool {
	if len(expect) == 0 {
		return status >= 200 && status < 300
	}
	for _, want := range expect {
		if want == status {
			return true
		}
	}
	return false
}

func formatExpect(expect []int) string {
	if len(expect) == 0 {
		return "2xx"
	}
	parts := make([]string, 0, len(expect))
	for _, s := range expect {
		parts = append(parts, strconv.Itoa(s))
	}
	return strings.Join(parts, ", ")
}

// maxSnippet bounds how much of a failed response's body lands in the error
// message. Enough to carry the service's own explanation, little enough not to
// put a megabyte of HTML in a run record.
const maxSnippet = 500

func bodySnippet(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > maxSnippet {
		return s[:maxSnippet] + "…"
	}
	return s
}

// coerceToType converts a value to what the argument's declared type needs,
// accepting the text a port delivers.
func coerceToType(v any, declared any) (any, error) {
	mime, scalar := schemaports.ScalarMIME(declared)
	if !scalar {
		// An object or array argument: pass whatever the author supplied
		// through untouched. Its shape is the params form's business, and
		// guessing at it here would be inventing structure.
		return v, nil
	}
	if len(mime) > 0 && mime[0] == core.MIMEBool {
		switch x := v.(type) {
		case bool:
			return x, nil
		case string:
			b, err := strconv.ParseBool(strings.TrimSpace(x))
			if err != nil {
				return nil, fmt.Errorf("%q is not true or false", x)
			}
			return b, nil
		}
		return v, nil
	}
	if isNumeric(declared) {
		switch x := v.(type) {
		case float64:
			return x, nil
		case int:
			return x, nil
		case int64:
			return x, nil
		case json.Number:
			f, err := x.Float64()
			if err != nil {
				return nil, fmt.Errorf("%q is not a number", x.String())
			}
			return f, nil
		case string:
			f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
			if err != nil {
				return nil, fmt.Errorf("%q is not a number", x)
			}
			return f, nil
		}
		return v, nil
	}
	s, ok := scalarString(v)
	if !ok {
		return nil, fmt.Errorf("expected a single value, got %T", v)
	}
	return s, nil
}

// rawBody accepts the shapes a wired value arrives in for a verbatim body.
func rawBody(v any) ([]byte, error) {
	switch x := v.(type) {
	case string:
		return []byte(x), nil
	case []byte:
		return x, nil
	default:
		raw, err := json.Marshal(x)
		if err != nil {
			return nil, fmt.Errorf("%s: cannot be sent as a body (%T)", rawBodyPort, v)
		}
		return raw, nil
	}
}

// scalarString renders a single value as text. Objects and slices are refused
// rather than marshalled: a query parameter that silently became `{"a":1}` is a
// call that fails at the service with a message about the wrong thing.
func scalarString(v any) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "", true
	case string:
		return x, true
	case bool:
		return strconv.FormatBool(x), true
	case int:
		return strconv.Itoa(x), true
	case int64:
		return strconv.FormatInt(x, 10), true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case json.Number:
		return x.String(), true
	case []byte:
		return string(x), true
	default:
		return "", false
	}
}

func stringArg(args map[string]any, key string) string {
	if v, ok := args[key]; ok {
		if s, ok := scalarString(v); ok {
			return s
		}
	}
	return ""
}

func intArg(args map[string]any, key string) (int, bool) {
	v, ok := args[key]
	if !ok {
		return 0, false
	}
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return int(i), true
		}
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(x)); err == nil {
			return i, true
		}
	}
	return 0, false
}

func intSliceArg(args map[string]any, key string) []int {
	v, ok := args[key]
	if !ok {
		return nil
	}
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]int, 0, len(raw))
	for _, one := range raw {
		if i, ok := intArg(map[string]any{"v": one}, "v"); ok {
			out = append(out, i)
		}
	}
	return out
}

// isTextMIME mirrors drops/internal/mimetype.IsText, which this package cannot
// import: that tree is walled off to drops/ by Go's internal rule. A copy of
// seven lines beats moving a shared helper as a side effect of this commit —
// but it does belong in internal/ eventually, and this is the second caller
// that would use it.
func isTextMIME(mime string) bool {
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = mime[:i]
	}
	mime = strings.TrimSpace(mime)
	if strings.HasPrefix(mime, "text/") {
		return true
	}
	switch mime {
	case "application/json", "application/xml", "application/csv",
		"application/javascript", "application/x-yaml", "application/yaml":
		return true
	}
	return false
}

func flattenHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out
}

func errResult(job core.Job, code, msg string) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusError,
		Error:  &core.JobError{Code: code, Message: msg},
	}
}

func emitProgress(ch chan<- core.Progress, job core.Job, pct float64, msg string) {
	if ch == nil {
		return
	}
	p := pct
	select {
	case ch <- core.Progress{JobID: job.ID, NodeID: job.NodeID, Percent: &p, Message: msg}:
	default:
	}
}
