// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package webapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine/webapi"
)

// seenCall is what the fake doer recorded.
type seenCall struct {
	method  string
	url     string
	headers map[string]string
	body    []byte
	timeout int
	maxBody int
}

// fakeDoer installs a doer that records the call and answers with the given
// status/content-type/body. Cleared on cleanup — SetDoer is process-wide, so no
// test here may run in parallel with another.
func fakeDoer(t *testing.T, status int, contentType, body string) *seenCall {
	t.Helper()
	var seen seenCall
	webapi.SetDoer(func(ctx context.Context, method, url string, headers map[string]string, reqBody []byte, timeoutMS, maxBytes int) (int, []byte, http.Header, error) {
		seen = seenCall{method: method, url: url, headers: headers, body: reqBody, timeout: timeoutMS, maxBody: maxBytes}
		h := http.Header{}
		if contentType != "" {
			h.Set("Content-Type", contentType)
		}
		h.Set("X-Request-Id", "abc123")
		return status, []byte(body), h, nil
	})
	t.Cleanup(func() { webapi.SetDoer(nil) })
	return &seen
}

// ordersDescriptor is the running example: an org's own service, one GET with a
// path + query + header argument and one POST with a JSON body.
func ordersDescriptor() webapi.Descriptor {
	return webapi.Descriptor{
		Tenant:  "acme",
		Name:    "orders",
		BaseURL: "https://api.example.com/v1",
		Auth:    webapi.Auth{Kind: webapi.AuthBearer},
		Operations: []webapi.Operation{
			{
				ID:      "get_order",
				Method:  "GET",
				Path:    "/orders/{order_id}",
				Summary: "Fetch one order",
				Args: []webapi.Arg{
					{Name: "order_id", In: webapi.InPath, Type: "string", Required: true},
					{Name: "expand", In: webapi.InQuery, Type: "string"},
					{Name: "X-Region", In: webapi.InHeader, Type: "string"},
				},
			},
			{
				ID:       "create_order",
				Method:   "POST",
				Path:     "/orders",
				BodyMode: webapi.BodyJSON,
				Args: []webapi.Arg{
					{Name: "sku", In: webapi.InBody, Type: "string", Required: true},
					{Name: "qty", In: webapi.InBody, Type: "integer", Required: true},
					{Name: "gift", In: webapi.InBody, Type: "boolean"},
				},
			},
		},
	}
}

func mustRegister(t *testing.T, desc webapi.Descriptor) *webapi.Catalog {
	t.Helper()
	cat := webapi.NewCatalog()
	if err := cat.Register(desc); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return cat
}

func transport(t *testing.T, cat *webapi.Catalog, tenant, id string) core.Transport {
	t.Helper()
	tr, ok := cat.Get(tenant, id)
	if !ok {
		t.Fatalf("no transport for %q in tenant %q", id, tenant)
	}
	return tr
}

func portNames(ports []core.Port) []string {
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		out = append(out, p.Port)
	}
	return out
}

// --- registration -----------------------------------------------------------

func TestRegister_RefusesDescriptorsThatCannotBeCalled(t *testing.T) {
	base := func(mutate func(*webapi.Descriptor)) webapi.Descriptor {
		d := ordersDescriptor()
		mutate(&d)
		return d
	}
	cases := []struct {
		name string
		desc webapi.Descriptor
		want string
	}{
		{"no tenant", base(func(d *webapi.Descriptor) { d.Tenant = "" }), "Tenant required"},
		{"no name", base(func(d *webapi.Descriptor) { d.Name = "" }), "name required"},
		{"unspellable name", base(func(d *webapi.Descriptor) { d.Name = "my api" }), "may use only letters"},
		{"no operations", base(func(d *webapi.Descriptor) { d.Operations = nil }), "no operations selected"},
		{"bad base URL scheme", base(func(d *webapi.Descriptor) { d.BaseURL = "ftp://x/y" }), "must be http"},
		{"header auth with no header", base(func(d *webapi.Descriptor) {
			d.Auth = webapi.Auth{Kind: webapi.AuthHeader}
		}), "needs a header name"},
		{"unknown auth kind", base(func(d *webapi.Descriptor) {
			d.Auth = webapi.Auth{Kind: "oauth2"}
		}), "unknown auth kind"},
		{"unknown method", base(func(d *webapi.Descriptor) { d.Operations[0].Method = "TRACE" }), "is not one this catalog can call"},
		{"relative path", base(func(d *webapi.Descriptor) { d.Operations[0].Path = "orders" }), "must start with /"},
		{"duplicate operation", base(func(d *webapi.Descriptor) {
			d.Operations = append(d.Operations, d.Operations[0])
		}), "declared twice"},
		{"placeholder with no argument", base(func(d *webapi.Descriptor) {
			d.Operations[0].Path = "/orders/{order_id}/lines/{line_id}"
		}), "no argument declares it"},
		{"path argument the path never names", base(func(d *webapi.Descriptor) {
			d.Operations[0].Args = append(d.Operations[0].Args,
				webapi.Arg{Name: "ghost", In: webapi.InPath, Type: "string", Required: true})
		}), "does not name it"},
		{"optional path argument", base(func(d *webapi.Descriptor) {
			d.Operations[0].Args[0].Required = false
		}), "must be required"},
		{"argument named twice", base(func(d *webapi.Descriptor) {
			d.Operations[1].Args = append(d.Operations[1].Args,
				webapi.Arg{Name: "sku", In: webapi.InQuery, Type: "string"})
		}), "must be unique across path, query, header and body"},
		{"argument colliding with a reserved name", base(func(d *webapi.Descriptor) {
			d.Operations[1].Args = append(d.Operations[1].Args,
				webapi.Arg{Name: "token", In: webapi.InQuery, Type: "string"})
		}), "collides with a name this step already uses"},
		{"body field without a JSON body", base(func(d *webapi.Descriptor) {
			d.Operations[1].BodyMode = webapi.BodyNone
		}), "body mode"},
		{"JSON body on a GET", base(func(d *webapi.Descriptor) {
			d.Operations[1].Method = "GET"
		}), "cannot carry a JSON body"},
		{"unknown argument location", base(func(d *webapi.Descriptor) {
			d.Operations[0].Args[1].In = "cookie"
		}), "unknown location"},
		{"unusable header name", base(func(d *webapi.Descriptor) {
			d.Operations[0].Args[2].Name = "X Region"
		}), "not a usable header name"},
		{"base URL with a query string", base(func(d *webapi.Descriptor) {
			d.BaseURL = "https://api.example.com/v1?debug=1"
		}), "must not carry a query string"},
		{"base URL with a fragment", base(func(d *webapi.Descriptor) {
			d.BaseURL = "https://api.example.com/v1#x"
		}), "must not carry a #fragment"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := webapi.NewCatalog().Register(tc.desc)
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// A refused descriptor must file nothing at all — one bad operation refuses the
// import rather than half-registering it.
func TestRegister_RefusalFilesNothing(t *testing.T) {
	desc := ordersDescriptor()
	desc.Operations[1].Method = "TRACE"
	cat := webapi.NewCatalog()
	if err := cat.Register(desc); err == nil {
		t.Fatal("want an error")
	}
	if _, ok := cat.Get("acme", "api:orders:get_order"); ok {
		t.Error("the good operation was filed anyway")
	}
	if got := cat.CatalogsFor("acme"); len(got) != 0 {
		t.Errorf("catalogs = %+v, want none", got)
	}
}

func TestCatalog_TenantIsolation(t *testing.T) {
	cat := mustRegister(t, ordersDescriptor())
	if _, ok := cat.Get("acme", "api:orders:get_order"); !ok {
		t.Error("the owning tenant cannot resolve its own step")
	}
	if _, ok := cat.Get("other", "api:orders:get_order"); ok {
		t.Error("another tenant resolved a step it does not own")
	}
	if _, ok := cat.Get("", "api:orders:get_order"); ok {
		t.Error("a tenant-less caller resolved a tenant's step")
	}
	if got := cat.ManifestsFor("other"); len(got) != 0 {
		t.Errorf("ManifestsFor(other) = %+v, want none", got)
	}
	if got := cat.ManifestsFor(""); len(got) != 0 {
		t.Errorf("ManifestsFor(\"\") = %+v, want none", got)
	}
	if got := cat.CatalogsFor("other"); len(got) != 0 {
		t.Errorf("CatalogsFor(other) = %+v, want none", got)
	}
}

// Re-registering replaces: an edited catalog takes effect without the org
// deleting it first, and an operation dropped from the new descriptor stops
// resolving.
func TestRegister_ReplacesAndDropsRemovedOperations(t *testing.T) {
	cat := mustRegister(t, ordersDescriptor())
	desc := ordersDescriptor()
	desc.BaseURL = "https://staging.example.com"
	desc.Operations = desc.Operations[:1]
	if err := cat.Register(desc); err != nil {
		t.Fatal(err)
	}
	if _, ok := cat.Get("acme", "api:orders:create_order"); ok {
		t.Error("an operation removed by a re-import still resolves")
	}
	if _, ok := cat.Get("acme", "api:orders:get_order"); !ok {
		t.Error("a surviving operation stopped resolving")
	}
	status := cat.CatalogsFor("acme")
	if len(status) != 1 || status[0].BaseURL != "https://staging.example.com" {
		t.Fatalf("status = %+v, want the new base URL", status)
	}
	if len(status[0].StepIDs) != 1 || status[0].StepIDs[0] != "api:orders:get_order" {
		t.Errorf("step ids = %v", status[0].StepIDs)
	}
}

func TestUnregister(t *testing.T) {
	cat := mustRegister(t, ordersDescriptor())
	cat.Unregister("acme", "orders")
	if _, ok := cat.Get("acme", "api:orders:get_order"); ok {
		t.Error("step still resolves after Unregister")
	}
	// Unknown pairs are not an error — it is how an org clears up a catalog
	// that never registered.
	cat.Unregister("acme", "nope")
}

func TestAllManifests_SpansTenantsForTheKillswitch(t *testing.T) {
	cat := mustRegister(t, ordersDescriptor())
	other := ordersDescriptor()
	other.Tenant = "beta"
	if err := cat.Register(other); err != nil {
		t.Fatal(err)
	}
	manifests, tenants := cat.AllManifests()
	if _, ok := manifests["api:orders:get_order"]; !ok {
		t.Fatal("missing manifest")
	}
	got := tenants["api:orders:get_order"]
	if len(got) != 2 || got[0] != "acme" || got[1] != "beta" {
		t.Fatalf("tenants = %v, want [acme beta]", got)
	}
}

// --- manifest synthesis -----------------------------------------------------

func TestManifest_Ports(t *testing.T) {
	cat := mustRegister(t, ordersDescriptor())
	m := transport(t, cat, "acme", "api:orders:get_order").Manifest()

	// order_id (required) first, then the optional query arg, then the overlay.
	// X-Region is a header argument and gets no pin.
	if got := portNames(m.Inputs); strings.Join(got, ",") != "order_id,expand,input" {
		t.Fatalf("inputs = %v", got)
	}
	for _, p := range m.Inputs {
		if !p.InlineOnly {
			t.Errorf("port %q is not InlineOnly", p.Port)
		}
	}
	if got := portNames(m.Outputs); strings.Join(got, ",") != "status,response_body,headers" {
		t.Fatalf("outputs = %v", got)
	}
}

func TestManifest_RawBodyGetsItsOwnPort(t *testing.T) {
	desc := ordersDescriptor()
	desc.Operations = []webapi.Operation{{
		ID: "push", Method: "POST", Path: "/push", BodyMode: webapi.BodyRaw,
	}}
	cat := mustRegister(t, desc)
	m := transport(t, cat, "acme", "api:orders:push").Manifest()
	if got := portNames(m.Inputs); strings.Join(got, ",") != "request_body,input" {
		t.Fatalf("inputs = %v", got)
	}
}

// The one thing a described API tells us that MCP cannot: which calls are safe
// to replay.
func TestManifest_IdempotencyFromMethod(t *testing.T) {
	for _, tc := range []struct {
		method string
		want   bool
	}{
		{"GET", true}, {"HEAD", true}, {"PUT", true}, {"DELETE", true},
		{"POST", false}, {"PATCH", false},
	} {
		t.Run(tc.method, func(t *testing.T) {
			desc := ordersDescriptor()
			desc.Operations = []webapi.Operation{{ID: "op", Method: tc.method, Path: "/x"}}
			cat := mustRegister(t, desc)
			m := transport(t, cat, "acme", "api:orders:op").Manifest()
			if m.Idempotent != tc.want {
				t.Fatalf("Idempotent = %v, want %v", m.Idempotent, tc.want)
			}
			if tc.want && m.RetryPolicy != core.RetryExponentialBackoff {
				t.Errorf("RetryPolicy = %q, want backoff on an idempotent method", m.RetryPolicy)
			}
			if !tc.want && m.RetryPolicy != "" {
				t.Errorf("RetryPolicy = %q, want none on a non-idempotent method", m.RetryPolicy)
			}
		})
	}
}

// The palette and the flow generator both read these; a synthesized manifest
// that skipped them would be a step nobody can find.
func TestManifest_DiscoveryMetadata(t *testing.T) {
	cat := mustRegister(t, ordersDescriptor())
	m := transport(t, cat, "acme", "api:orders:get_order").Manifest()
	if m.Summary == "" {
		t.Error("Summary is empty")
	}
	if len(m.Examples) == 0 || len(m.Examples[0].Params) == 0 {
		t.Error("no params example for the generator to copy")
	}
	var example map[string]any
	if err := json.Unmarshal(m.Examples[0].Params, &example); err != nil {
		t.Fatalf("example params are not JSON: %v", err)
	}
	if _, ok := example["order_id"]; !ok {
		t.Errorf("example = %v, want the required argument in it", example)
	}
	if _, ok := example["expand"]; ok {
		t.Errorf("example = %v, want optional arguments left out", example)
	}
	if m.Integration != "orders" {
		t.Errorf("Integration = %q, want the catalog name as a fallback", m.Integration)
	}
	if m.Provider != "api:orders" {
		t.Errorf("Provider = %q", m.Provider)
	}
	if !strings.Contains(m.Description, "GET /orders/{order_id}") {
		t.Errorf("Description = %q, want the method and path in it", m.Description)
	}
}

func TestManifest_DeprecationIsSaidFirst(t *testing.T) {
	desc := ordersDescriptor()
	desc.Operations[0].Deprecated = true
	cat := mustRegister(t, desc)
	m := transport(t, cat, "acme", "api:orders:get_order").Manifest()
	if !strings.HasPrefix(m.Description, "Deprecated") {
		t.Errorf("Description = %q, want it to open with the deprecation", m.Description)
	}
}

// The ergonomic point of the feature: the tenant's own service gets an Apps-page
// connection instead of a ${secret.X} in every step.
func TestManifest_ConnectionFields(t *testing.T) {
	cat := mustRegister(t, ordersDescriptor())
	m := transport(t, cat, "acme", "api:orders:get_order").Manifest()
	keys := map[string]core.ConnectionField{}
	for _, f := range m.ConnectionFields {
		keys[f.Key] = f
	}
	// The credential and nothing else. The service address is the catalog's,
	// set by an admin — a connection is writable with secret:write, so
	// declaring the address here would let an editor repoint the org's calls
	// (and be handed the token, which is sent to whatever address resolved).
	if _, ok := keys["base_url"]; ok {
		t.Errorf("the service address is a connection field: %+v", m.ConnectionFields)
	}
	if len(m.ConnectionFields) != 1 {
		t.Errorf("fields = %+v, want only the credential", m.ConnectionFields)
	}
	tok, ok := keys["token"]
	if !ok {
		t.Fatalf("fields = %+v, want token", m.ConnectionFields)
	}
	if !tok.Secret {
		t.Error("the credential field is not marked Secret — it would render unmasked and skip redaction")
	}
	if m.Integration == "" {
		t.Error("connection injection keys off Integration; empty means no connection can ever attach")
	}
}

func TestManifest_NoCredentialFieldWithoutAuth(t *testing.T) {
	desc := ordersDescriptor()
	desc.Auth = webapi.Auth{Kind: webapi.AuthNone}
	cat := mustRegister(t, desc)
	m := transport(t, cat, "acme", "api:orders:get_order").Manifest()
	// Nothing to connect at all: no credential, and the address is not a
	// connection field either, so the Apps page has no form to offer.
	if len(m.ConnectionFields) != 0 {
		t.Errorf("fields = %+v, want none for a catalog with no auth", m.ConnectionFields)
	}
}

// TestTransport_ConnectionCannotRepointTheService is the privilege boundary the
// field removal exists for.
//
// A connection value reaches a step by being injected into a param of the same
// name (engine/secrets.go), and an injected param beats the descriptor. So the
// test simulates exactly that: a base_url arriving as a param, as an injected
// connection value would. The address must still be the catalog's.
//
// It is a deliberate asymmetry, not an oversight: an author with graph:edit CAN
// override one step this way, and that is flow-shaping power they already have.
// What must not happen is secret:write doing it for the whole org at once.
func TestTransport_ConnectionCannotRepointTheService(t *testing.T) {
	cat := mustRegister(t, ordersDescriptor())
	m := transport(t, cat, "acme", "api:orders:get_order").Manifest()
	for _, f := range m.ConnectionFields {
		if f.Key == "base_url" {
			t.Fatal("base_url is injectable from a connection again — an editor can repoint the org's calls")
		}
	}
	// And the params schema still offers the per-step override, so the
	// documented escape hatch has not gone with it.
	if !strings.Contains(string(m.ParamsSchema), "base_url") {
		t.Errorf("the per-step base_url override is gone: %s", m.ParamsSchema)
	}
}

func TestManifest_ParamsSchema(t *testing.T) {
	cat := mustRegister(t, ordersDescriptor())
	m := transport(t, cat, "acme", "api:orders:get_order").Manifest()
	var schema struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(m.ParamsSchema, &schema); err != nil {
		t.Fatalf("ParamsSchema is not JSON: %v", err)
	}
	if schema.Type != "object" {
		t.Errorf("type = %q", schema.Type)
	}
	for _, want := range []string{"order_id", "expand", "X-Region", "base_url", "token", "timeout_ms", "expect_status"} {
		if _, ok := schema.Properties[want]; !ok {
			t.Errorf("ParamsSchema is missing %q", want)
		}
	}
	// A header argument has no port, so the params form is the ONLY place it can
	// be set — and it must say where the value goes.
	if !strings.Contains(string(schema.Properties["X-Region"]), `"x_location":"header"`) {
		t.Errorf("X-Region schema = %s, want its location", schema.Properties["X-Region"])
	}
	if len(schema.Required) != 1 || schema.Required[0] != "order_id" {
		t.Errorf("required = %v, want only the required argument", schema.Required)
	}
}

func TestManifest_ArgumentSchemaKeptVerbatim(t *testing.T) {
	desc := ordersDescriptor()
	desc.Operations[0].Args[1].Schema = json.RawMessage(`{"type":"string","enum":["lines","customer"]}`)
	cat := mustRegister(t, desc)
	m := transport(t, cat, "acme", "api:orders:get_order").Manifest()
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(m.ParamsSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(schema.Properties["expand"]), `"enum"`) {
		t.Errorf("expand = %s, want the declared enum preserved", schema.Properties["expand"])
	}
}

// --- execution --------------------------------------------------------------

func TestExecute_AssemblesTheCall(t *testing.T) {
	seen := fakeDoer(t, 200, "application/json", `{"id":"o-1"}`)
	cat := mustRegister(t, ordersDescriptor())
	tr := transport(t, cat, "acme", "api:orders:get_order")

	res, err := tr.Execute(context.Background(), core.Job{
		ID: "j1",
		Params: map[string]any{
			"order_id": "o-1",
			"expand":   "lines",
			"X-Region": "eu",
			// Injected by the engine from the tenant's connection in a real run.
			"token": "tok-abc",
		},
	}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, error = %+v", res.Status, res.Error)
	}
	if seen.method != "GET" {
		t.Errorf("method = %q", seen.method)
	}
	if seen.url != "https://api.example.com/v1/orders/o-1?expand=lines" {
		t.Errorf("url = %q", seen.url)
	}
	if seen.headers["Authorization"] != "Bearer tok-abc" {
		t.Errorf("Authorization = %q", seen.headers["Authorization"])
	}
	if seen.headers["X-Region"] != "eu" {
		t.Errorf("X-Region = %q", seen.headers["X-Region"])
	}
	if seen.headers["Accept"] != "application/json" {
		t.Errorf("Accept = %q", seen.headers["Accept"])
	}
	if seen.body != nil {
		t.Errorf("body = %q, want none on a GET", seen.body)
	}
	if res.Output["status"].Inline != 200 {
		t.Errorf("status port = %v, want a bare 200", res.Output["status"].Inline)
	}
	if got := res.Output["response_body"].Inline; got != `{"id":"o-1"}` {
		t.Errorf("response_body = %v", got)
	}
	if got := res.Output["headers"].Inline.(map[string]string)["X-Request-Id"]; got != "abc123" {
		t.Errorf("headers port = %v", res.Output["headers"].Inline)
	}
}

// A path argument is escaped, so an id cannot walk out of the collection it
// belongs to.
func TestExecute_PathValuesAreEscaped(t *testing.T) {
	seen := fakeDoer(t, 200, "application/json", `{}`)
	cat := mustRegister(t, ordersDescriptor())
	tr := transport(t, cat, "acme", "api:orders:get_order")
	if _, err := tr.Execute(context.Background(), core.Job{
		ID:     "j1",
		Params: map[string]any{"order_id": "../../admin", "token": "t"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	// The dots survive and are harmless: traversal needs SLASHES, and those are
	// escaped, so the whole value reaches the service as one path segment it can
	// reject rather than as a URL that walked up out of /orders.
	if strings.Contains(strings.TrimPrefix(seen.url, "https://api.example.com/v1/orders/"), "/") {
		t.Fatalf("url = %q, want the value to stay a single path segment", seen.url)
	}
	if !strings.Contains(seen.url, "%2F") {
		t.Errorf("url = %q, want the slashes escaped", seen.url)
	}
}

// A JSON body must carry the declared types, not the text a port delivers.
func TestExecute_JSONBodyCoercesPortText(t *testing.T) {
	seen := fakeDoer(t, 201, "application/json", `{"id":"o-2"}`)
	cat := mustRegister(t, ordersDescriptor())
	tr := transport(t, cat, "acme", "api:orders:create_order")

	res, err := tr.Execute(context.Background(), core.Job{
		ID:     "j1",
		Params: map[string]any{"token": "t"},
		Input: map[string]core.Ref{
			"sku":  {Inline: "ABC-123"},
			"qty":  {Inline: "2"},    // numbers travel as text on a port
			"gift": {Inline: "true"}, // and so do booleans
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, error = %+v", res.Status, res.Error)
	}
	var body map[string]any
	if err := json.Unmarshal(seen.body, &body); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, seen.body)
	}
	if body["sku"] != "ABC-123" {
		t.Errorf("sku = %v", body["sku"])
	}
	if body["qty"] != float64(2) {
		t.Errorf("qty = %#v, want the number 2 (a string would be rejected by a schema-checking service)", body["qty"])
	}
	if body["gift"] != true {
		t.Errorf("gift = %#v, want the boolean true", body["gift"])
	}
	if seen.headers["Content-Type"] != "application/json" {
		t.Errorf("Content-Type = %q", seen.headers["Content-Type"])
	}
	// POST is not idempotent under HTTP, so a lost-response retry needs a key
	// the service can dedupe on.
	if seen.headers["Idempotency-Key"] == "" {
		t.Error("no Idempotency-Key on a POST")
	}
}

func TestExecute_BadNumberIsANodeError(t *testing.T) {
	fakeDoer(t, 200, "application/json", `{}`)
	cat := mustRegister(t, ordersDescriptor())
	tr := transport(t, cat, "acme", "api:orders:create_order")
	res, err := tr.Execute(context.Background(), core.Job{
		ID:     "j1",
		Params: map[string]any{"sku": "x", "qty": "two", "token": "t"},
	}, nil)
	if err != nil {
		t.Fatalf("a bad param must not be a transport error: %v", err)
	}
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(res.Error.Message, "qty") {
		t.Errorf("message = %q, want it to name the argument", res.Error.Message)
	}
}

func TestExecute_RawBody(t *testing.T) {
	seen := fakeDoer(t, 200, "text/plain", "ok")
	desc := ordersDescriptor()
	desc.Operations = []webapi.Operation{{
		ID: "push", Method: "POST", Path: "/push", BodyMode: webapi.BodyRaw,
	}}
	cat := mustRegister(t, desc)
	tr := transport(t, cat, "acme", "api:orders:push")
	if _, err := tr.Execute(context.Background(), core.Job{
		ID:     "j1",
		Params: map[string]any{"token": "t"},
		Input:  map[string]core.Ref{"request_body": {Inline: `{"anything":[1,2,3]}`}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if string(seen.body) != `{"anything":[1,2,3]}` {
		t.Fatalf("body = %q, want it verbatim", seen.body)
	}
}

// params < overlay < port, the same precedence the rest of the product states.
func TestExecute_ArgumentPrecedence(t *testing.T) {
	seen := fakeDoer(t, 200, "application/json", `{}`)
	cat := mustRegister(t, ordersDescriptor())
	tr := transport(t, cat, "acme", "api:orders:get_order")
	if _, err := tr.Execute(context.Background(), core.Job{
		ID:     "j1",
		Params: map[string]any{"order_id": "from-params", "expand": "from-params", "token": "t"},
		Input: map[string]core.Ref{
			"input":    {Inline: map[string]any{"order_id": "from-overlay", "expand": "from-overlay"}},
			"order_id": {Inline: "from-port"},
		},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seen.url, "/orders/from-port") {
		t.Errorf("url = %q, want the port to win for order_id", seen.url)
	}
	if !strings.Contains(seen.url, "expand=from-overlay") {
		t.Errorf("url = %q, want the overlay to beat the param for expand", seen.url)
	}
}

func TestExecute_UnexpectedStatus(t *testing.T) {
	fakeDoer(t, 404, "application/json", `{"error":"no such order"}`)
	cat := mustRegister(t, ordersDescriptor())
	tr := transport(t, cat, "acme", "api:orders:get_order")
	res, err := tr.Execute(context.Background(), core.Job{
		ID:     "j1",
		Params: map[string]any{"order_id": "o-1", "token": "t"},
	}, nil)
	if err != nil {
		t.Fatalf("the service saying no is not a transport error: %v", err)
	}
	if res.Status != core.StatusError || res.Error.Code != "unexpected_status" {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(res.Error.Message, "no such order") {
		t.Errorf("message = %q, want the service's own explanation in it", res.Error.Message)
	}
}

// expect_status is how a 404 becomes an answer rather than a failure.
func TestExecute_ExpectStatusWidens(t *testing.T) {
	fakeDoer(t, 404, "application/json", `{}`)
	cat := mustRegister(t, ordersDescriptor())
	tr := transport(t, cat, "acme", "api:orders:get_order")
	res, err := tr.Execute(context.Background(), core.Job{
		ID: "j1",
		Params: map[string]any{
			"order_id":      "o-1",
			"token":         "t",
			"expect_status": []any{200, 404},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("result = %+v, want the 404 accepted", res)
	}
	if res.Output["status"].Inline != 404 {
		t.Errorf("status port = %v", res.Output["status"].Inline)
	}
}

func TestExecute_MissingCredentialAndAddress(t *testing.T) {
	fakeDoer(t, 200, "application/json", `{}`)
	t.Run("no credential", func(t *testing.T) {
		cat := mustRegister(t, ordersDescriptor())
		tr := transport(t, cat, "acme", "api:orders:get_order")
		res, _ := tr.Execute(context.Background(), core.Job{
			ID: "j1", Params: map[string]any{"order_id": "o-1"},
		}, nil)
		if res.Status != core.StatusError || res.Error.Code != "bad_param" {
			t.Fatalf("result = %+v", res)
		}
		if !strings.Contains(res.Error.Message, "connection") {
			t.Errorf("message = %q, want it to point at the connection", res.Error.Message)
		}
	})
	t.Run("no address", func(t *testing.T) {
		desc := ordersDescriptor()
		desc.BaseURL = ""
		cat := mustRegister(t, desc)
		tr := transport(t, cat, "acme", "api:orders:get_order")
		res, _ := tr.Execute(context.Background(), core.Job{
			ID: "j1", Params: map[string]any{"order_id": "o-1", "token": "t"},
		}, nil)
		if res.Status != core.StatusError || !strings.Contains(res.Error.Message, "no service address") {
			t.Fatalf("result = %+v", res)
		}
	})
}

// The connection's address wins over the one typed at import time — that is how
// staging and production differ without re-importing.
func TestExecute_BaseURLFromParamsOverridesDescriptor(t *testing.T) {
	seen := fakeDoer(t, 200, "application/json", `{}`)
	cat := mustRegister(t, ordersDescriptor())
	tr := transport(t, cat, "acme", "api:orders:get_order")
	if _, err := tr.Execute(context.Background(), core.Job{
		ID: "j1",
		Params: map[string]any{
			"order_id": "o-1", "token": "t",
			"base_url": "https://staging.example.com/",
		},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if seen.url != "https://staging.example.com/orders/o-1" {
		t.Fatalf("url = %q", seen.url)
	}
}

func TestExecute_TimeoutAndCapPassedThrough(t *testing.T) {
	seen := fakeDoer(t, 200, "application/json", `{}`)
	desc := ordersDescriptor()
	desc.MaxBodyBytes = 4096
	cat := mustRegister(t, desc)
	tr := transport(t, cat, "acme", "api:orders:get_order")
	if _, err := tr.Execute(context.Background(), core.Job{
		ID:     "j1",
		Params: map[string]any{"order_id": "o-1", "token": "t", "timeout_ms": 1234},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if seen.timeout != 1234 {
		t.Errorf("timeout = %d, want the param honored", seen.timeout)
	}
	if seen.maxBody != 4096 {
		t.Errorf("maxBody = %d, want the descriptor's cap", seen.maxBody)
	}
}

func TestExecute_BinaryBodyStaysBytes(t *testing.T) {
	fakeDoer(t, 200, "application/octet-stream", "\x00\x01binary")
	cat := mustRegister(t, ordersDescriptor())
	tr := transport(t, cat, "acme", "api:orders:get_order")
	res, err := tr.Execute(context.Background(), core.Job{
		ID: "j1", Params: map[string]any{"order_id": "o-1", "token": "t"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.Output["response_body"].Inline.([]byte); !ok {
		t.Fatalf("response_body = %T, want bytes for a non-text MIME", res.Output["response_body"].Inline)
	}
}

// With no doer wired, a step must fail loudly rather than fall back to an
// unguarded HTTP client — the guards are the reason the doer is injected.
func TestExecute_NoDoerWiredFailsLoudly(t *testing.T) {
	webapi.SetDoer(nil)
	cat := mustRegister(t, ordersDescriptor())
	tr := transport(t, cat, "acme", "api:orders:get_order")
	res, err := tr.Execute(context.Background(), core.Job{
		ID: "j1", Params: map[string]any{"order_id": "o-1", "token": "t"},
	}, nil)
	if err == nil {
		t.Fatal("want a transport error: nothing about the graph can fix this")
	}
	if res.Error == nil || res.Error.Code != "webapi_unwired" {
		t.Fatalf("result = %+v", res)
	}
}

// A live call must not see a later edit to the catalog it came from.
func TestExecute_TransportHoldsItsOwnDescriptorCopy(t *testing.T) {
	seen := fakeDoer(t, 200, "application/json", `{}`)
	cat := mustRegister(t, ordersDescriptor())
	tr := transport(t, cat, "acme", "api:orders:get_order")

	edited := ordersDescriptor()
	edited.BaseURL = "https://elsewhere.example.com"
	if err := cat.Register(edited); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Execute(context.Background(), core.Job{
		ID: "j1", Params: map[string]any{"order_id": "o-1", "token": "t"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(seen.url, "https://api.example.com/v1") {
		t.Fatalf("url = %q, want the transport's own copy of the base URL", seen.url)
	}
}

func TestExecute_HeaderAuthUsesTheNamedHeader(t *testing.T) {
	seen := fakeDoer(t, 200, "application/json", `{}`)
	desc := ordersDescriptor()
	desc.Auth = webapi.Auth{Kind: webapi.AuthHeader, Header: "X-Api-Key"}
	cat := mustRegister(t, desc)
	tr := transport(t, cat, "acme", "api:orders:get_order")
	if _, err := tr.Execute(context.Background(), core.Job{
		ID: "j1", Params: map[string]any{"order_id": "o-1", "token": "k-1"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if seen.headers["X-Api-Key"] != "k-1" {
		t.Fatalf("headers = %v", seen.headers)
	}
	if _, set := seen.headers["Authorization"]; set {
		t.Error("Authorization must not be set for header auth")
	}
}

// A header value arrives at run time, so it is the one part of the request that
// can carry a line break. It must be refused with a message naming the
// argument, not deep in the transport.
func TestExecute_HeaderValueLineBreakRefused(t *testing.T) {
	fakeDoer(t, 200, "application/json", `{}`)
	cat := mustRegister(t, ordersDescriptor())
	tr := transport(t, cat, "acme", "api:orders:get_order")
	res, err := tr.Execute(context.Background(), core.Job{
		ID: "j1",
		Params: map[string]any{
			"order_id": "o-1", "token": "t",
			"X-Region": "eu\r\nX-Admin: true",
		},
	}, nil)
	if err != nil {
		t.Fatalf("this is the author's mistake, not a transport fault: %v", err)
	}
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(res.Error.Message, "X-Region") {
		t.Errorf("message = %q, want it to name the argument", res.Error.Message)
	}
}

// A base URL supplied by the connection at run time gets the same check as one
// typed at import: it is a tenant-editable value either way.
func TestExecute_RuntimeBaseURLIsValidated(t *testing.T) {
	fakeDoer(t, 200, "application/json", `{}`)
	cat := mustRegister(t, ordersDescriptor())
	tr := transport(t, cat, "acme", "api:orders:get_order")
	for _, bad := range []string{"file:///etc/passwd", "https://api.example.com/v1?debug=1", "not a url at all"} {
		res, err := tr.Execute(context.Background(), core.Job{
			ID:     "j1",
			Params: map[string]any{"order_id": "o-1", "token": "t", "base_url": bad},
		}, nil)
		if err != nil {
			t.Fatalf("%q: unexpected transport error: %v", bad, err)
		}
		if res.Status != core.StatusError {
			t.Errorf("%q was accepted as a service address", bad)
		}
	}
}

// TestManifest_CaptionedByHumanNames is the difference between a palette a
// non-technical author can read and one full of identifiers. The step ids are
// unchanged by it — a name is display only.
func TestManifest_CaptionedByHumanNames(t *testing.T) {
	desc := ordersDescriptor()
	desc.Label = "Order service"
	desc.Operations[0].Title = "Fetch an order"
	cat := mustRegister(t, desc)

	m := transport(t, cat, "acme", "api:orders:get_order").Manifest()
	if m.Label != "Order service — Fetch an order" {
		t.Errorf("Label = %q, want the typed names", m.Label)
	}
	// The id is built from the ids, not the names.
	if m.ID != "api:orders:get_order" {
		t.Errorf("ID = %q, want it unmoved by naming", m.ID)
	}
	if m.Provider != "api:orders" {
		t.Errorf("Provider = %q", m.Provider)
	}
}

// TestManifest_ProseNamesTheCatalogByItsName covers the generated-prose branch:
// an operation with no summary of its own gets a sentence built from the
// catalog, and that sentence should name it the way a human does.
func TestManifest_ProseNamesTheCatalogByItsName(t *testing.T) {
	desc := ordersDescriptor()
	desc.Label = "Order service"
	desc.Operations[0].Summary = "" // force the generated sentence
	cat := mustRegister(t, desc)

	m := transport(t, cat, "acme", "api:orders:get_order").Manifest()
	if !strings.Contains(m.Description, "Order service") {
		t.Errorf("Description names the slug, not the catalog: %q", m.Description)
	}
	if !strings.Contains(m.Summary, "Order service") {
		t.Errorf("Summary names the slug, not the catalog: %q", m.Summary)
	}
}

// TestManifest_CaptionFallsBackToIDs: naming is optional, and an unnamed
// catalog must still be captioned with something.
func TestManifest_CaptionFallsBackToIDs(t *testing.T) {
	cat := mustRegister(t, ordersDescriptor())
	m := transport(t, cat, "acme", "api:orders:get_order").Manifest()
	if m.Label != "orders — get_order" {
		t.Errorf("Label = %q, want the ids as the fallback", m.Label)
	}
}

// TestDisplayName bounds what a typed name can do to a palette row.
func TestDisplayName(t *testing.T) {
	cases := []struct {
		title, id, want string
	}{
		{"", "get_order", "get_order"},
		{"  Fetch an order  ", "get_order", "Fetch an order"},
		{"   ", "get_order", "get_order"},
		// A pasted paragraph is not a caption: first line only.
		{"Fetch an order\nUse the id from the previous step.", "get_order", "Fetch an order"},
		{strings.Repeat("x", 200), "get_order", strings.Repeat("x", 60) + "…"},
	}
	for _, c := range cases {
		got := webapi.Operation{ID: c.id, Title: c.title}.DisplayName()
		if got != c.want {
			t.Errorf("Operation{%q,%q}.DisplayName() = %q, want %q", c.id, c.title, got, c.want)
		}
		// The catalog half uses the same rule.
		if d := (webapi.Descriptor{Name: c.id, Label: c.title}).DisplayName(); d != c.want {
			t.Errorf("Descriptor{%q,%q}.DisplayName() = %q, want %q", c.id, c.title, d, c.want)
		}
	}
}
