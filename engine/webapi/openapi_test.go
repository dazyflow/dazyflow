// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package webapi_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/engine/webapi"
)

// ordersSpec is the running example, written the way a framework actually emits
// one: a $ref'd parameter, a $ref'd body schema, an allOf, a path-level
// parameter and an operation with no operationId.
const ordersSpec = `
openapi: 3.0.3
info:
  title: Order service
  description: The orders API.
servers:
  - url: https://api.example.com/v1
paths:
  /orders/{order_id}:
    parameters:
      - $ref: '#/components/parameters/OrderId'
    get:
      operationId: getOrder
      summary: Fetch one order
      tags: [orders]
      parameters:
        - name: expand
          in: query
          schema: {type: string, enum: [lines, customer]}
        - name: X-Region
          in: header
          required: true
          schema: {type: string}
      responses:
        '200': {description: ok}
    delete:
      summary: Cancel an order
      tags: [orders]
      responses:
        '204': {description: gone}
  /orders:
    post:
      operationId: createOrder
      summary: Place an order
      tags: [orders, write]
      requestBody:
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/NewOrder'
      responses:
        '201': {description: made}
components:
  parameters:
    OrderId:
      name: order_id
      in: path
      required: true
      schema: {type: string}
  schemas:
    Auditable:
      type: object
      properties:
        source: {type: string}
    NewOrder:
      allOf:
        - $ref: '#/components/schemas/Auditable'
        - type: object
          required: [sku, qty]
          properties:
            sku: {type: string}
            qty: {type: integer}
            gift: {type: boolean}
`

func parse(t *testing.T, spec string) webapi.SpecImport {
	t.Helper()
	got, err := webapi.ParseSpec([]byte(spec))
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	return got
}

func opByID(t *testing.T, in webapi.SpecImport, id string) webapi.Operation {
	t.Helper()
	for _, op := range in.Operations {
		if op.ID == id {
			return op
		}
	}
	t.Fatalf("no operation %q in %v", id, opIDs(in))
	return webapi.Operation{}
}

func opIDs(in webapi.SpecImport) []string {
	out := make([]string, 0, len(in.Operations))
	for _, op := range in.Operations {
		out = append(out, op.ID)
	}
	return out
}

func argByName(t *testing.T, op webapi.Operation, name string) webapi.Arg {
	t.Helper()
	for _, a := range op.Args {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("operation %q has no argument %q", op.ID, name)
	return webapi.Arg{}
}

func TestParseSpec_ReadsTheDocumentsOwnMetadata(t *testing.T) {
	got := parse(t, ordersSpec)
	if got.Title != "Order service" {
		t.Errorf("title = %q", got.Title)
	}
	if got.BaseURL != "https://api.example.com/v1" {
		t.Errorf("base_url = %q", got.BaseURL)
	}
	if strings.Join(got.Tags, ",") != "orders,write" {
		t.Errorf("tags = %v, want them sorted for the picker", got.Tags)
	}
}

func TestParseSpec_ResolvesRefsAndMergesAllOf(t *testing.T) {
	got := parse(t, ordersSpec)
	create := opByID(t, got, "createOrder")

	if create.BodyMode != webapi.BodyJSON {
		t.Fatalf("body mode = %q, want json", create.BodyMode)
	}
	// From the allOf's second member...
	if a := argByName(t, create, "sku"); !a.Required || a.In != webapi.InBody {
		t.Errorf("sku = %+v, want a required body arg", a)
	}
	if a := argByName(t, create, "qty"); a.Type != "integer" {
		t.Errorf("qty type = %v", a.Type)
	}
	if a := argByName(t, create, "gift"); a.Required {
		t.Error("gift is not in the required list and must not be required")
	}
	// ...and from the $ref'd member it inherits, which is the whole point of
	// merging allOf rather than reading only the last branch.
	if a := argByName(t, create, "source"); a.In != webapi.InBody {
		t.Errorf("source = %+v, want the inherited body arg", a)
	}
}

func TestParseSpec_PathLevelParametersReachEveryOperation(t *testing.T) {
	got := parse(t, ordersSpec)
	// Declared once on the path item, via a $ref, and needed by both the GET
	// and the DELETE under it.
	for _, id := range []string{"getOrder", "delete_orders_order_id"} {
		op := opByID(t, got, id)
		a := argByName(t, op, "order_id")
		if a.In != webapi.InPath || !a.Required {
			t.Errorf("%s: order_id = %+v, want a required path arg", id, a)
		}
	}
}

func TestParseSpec_DerivesAnIdWhenTheSpecOmitsOne(t *testing.T) {
	got := parse(t, ordersSpec)
	// The DELETE has no operationId. The derived one must be deterministic —
	// a refresh matches on it, so an id that varied would read as a removal
	// plus an addition on every import.
	op := opByID(t, got, "delete_orders_order_id")
	if op.Method != "DELETE" || op.Path != "/orders/{order_id}" {
		t.Errorf("derived op = %+v", op)
	}
	again := parse(t, ordersSpec)
	if strings.Join(opIDs(got), ",") != strings.Join(opIDs(again), ",") {
		t.Error("two parses of one document produced different ids")
	}
}

func TestParseSpec_KeepsEnumDetailOnTheArgument(t *testing.T) {
	got := parse(t, ordersSpec)
	expand := argByName(t, opByID(t, got, "getOrder"), "expand")
	if expand.Schema == nil {
		t.Fatal("the enum was dropped; the params form has nothing to render")
	}
	if !strings.Contains(string(expand.Schema), "lines") {
		t.Errorf("schema = %s", expand.Schema)
	}
	// A schema that says nothing the type does not is not worth carrying.
	region := argByName(t, opByID(t, got, "getOrder"), "X-Region")
	if region.Schema != nil {
		t.Errorf("a bare {type: string} was carried as a schema: %s", region.Schema)
	}
}

func TestParseSpec_RefusesDocumentsItCannotRead(t *testing.T) {
	cases := []struct{ name, spec, want string }{
		{"swagger 2.0", `{"swagger":"2.0","info":{},"paths":{}}`, "Swagger 2.0"},
		{"not openapi at all", `{"hello":"world"}`, "not an OpenAPI document"},
		{"a future major", `{"openapi":"4.0.0","paths":{}}`, "not a version"},
		{"unreadable", "\t\x00not yaml: [", "does not read"},
		{"no paths", `{"openapi":"3.0.0","paths":{}}`, "no paths"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := webapi.ParseSpec([]byte(tc.spec))
			if err == nil {
				t.Fatal("ParseSpec accepted it")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestParseSpec_RefusesExternalRefsWithoutFetchingThem(t *testing.T) {
	// An external $ref is a URL the parser would have to fetch — an SSRF vector
	// wearing a document's clothes. There is no fetcher in the package, so this
	// cannot be followed even by mistake; the test pins the message.
	const spec = `
openapi: 3.0.0
info: {title: X}
paths:
  /a:
    get:
      operationId: getA
      parameters:
        - $ref: 'https://evil.example/params.yaml#/Sneaky'
      responses: {'200': {description: ok}}
  /b:
    get:
      operationId: getB
      responses: {'200': {description: ok}}
`
	got := parse(t, spec)
	var found bool
	for _, w := range got.Warnings {
		if strings.Contains(w.Reason, "external references are not followed") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %+v, want the external ref refused", got.Warnings)
	}
	// And the rest of the document still imports: refusing a reference must not
	// cost the operations that never used it.
	if len(got.Operations) != 2 {
		t.Errorf("operations = %v, want both (the bad ref only drops the parameter)", opIDs(got))
	}
}

func TestParseSpec_SkipsOneBadOperationRatherThanTheDocument(t *testing.T) {
	// `token` is a name the synthesized step already spends, so an operation
	// declaring it cannot be registered. The other operation is fine, and a
	// library that validated the document as a whole would have refused both.
	const spec = `
openapi: 3.0.0
info: {title: X}
paths:
  /a:
    get:
      operationId: getA
      parameters:
        - {name: token, in: query, schema: {type: string}}
      responses: {'200': {description: ok}}
  /b:
    get:
      operationId: getB
      responses: {'200': {description: ok}}
`
	got := parse(t, spec)
	if strings.Join(opIDs(got), ",") != "getB" {
		t.Errorf("operations = %v, want only the importable one", opIDs(got))
	}
	if len(got.Warnings) == 0 || !strings.Contains(got.Warnings[0].Reason, "token") {
		t.Errorf("warnings = %+v, want one naming the argument", got.Warnings)
	}
	if got.Warnings[0].Where != "get /a" {
		t.Errorf("warning location = %q", got.Warnings[0].Where)
	}
}

func TestParseSpec_NonObjectBodyBecomesTheRawPort(t *testing.T) {
	const spec = `
openapi: 3.0.0
info: {title: X}
paths:
  /upload:
    post:
      operationId: upload
      requestBody:
        content:
          text/csv:
            schema: {type: string}
      responses: {'200': {description: ok}}
  /bulk:
    post:
      operationId: bulk
      requestBody:
        content:
          application/json:
            schema: {type: array, items: {type: object}}
      responses: {'200': {description: ok}}
`
	got := parse(t, spec)
	// Neither has a field-per-argument reading, so both become a working step
	// with a request_body port rather than a skipped one.
	for _, id := range []string{"upload", "bulk"} {
		if op := opByID(t, got, id); op.BodyMode != webapi.BodyRaw {
			t.Errorf("%s body mode = %q, want raw", id, op.BodyMode)
		}
	}
}

func TestParseSpec_RelativeServerIsReportedNotGuessed(t *testing.T) {
	const spec = `
openapi: 3.0.0
info: {title: X}
servers: [{url: /v1}]
paths:
  /a: {get: {operationId: getA, responses: {'200': {description: ok}}}}
`
	got := parse(t, spec)
	if got.BaseURL != "" {
		t.Errorf("base_url = %q, want it left for the admin to type", got.BaseURL)
	}
	if len(got.Warnings) == 0 || !strings.Contains(got.Warnings[0].Reason, "relative") {
		t.Errorf("warnings = %+v", got.Warnings)
	}
}

func TestParseSpec_SurvivesARefCycle(t *testing.T) {
	// A self-referential document is a plausible way to try to spin the parser.
	const spec = `
openapi: 3.0.0
info: {title: X}
paths:
  /a:
    get:
      operationId: getA
      parameters: [{$ref: '#/components/parameters/Loop'}]
      responses: {'200': {description: ok}}
components:
  parameters:
    Loop: {$ref: '#/components/parameters/Loop'}
`
	// The point is that this RETURNS. A hang is the failure.
	if _, err := webapi.ParseSpec([]byte(spec)); err != nil {
		t.Logf("refused, which is fine: %v", err)
	}
}

// --- refresh diff -----------------------------------------------------------

func ops(in ...webapi.Operation) []webapi.Operation { return in }

func op(id, method, path string, args ...webapi.Arg) webapi.Operation {
	return webapi.Operation{ID: id, Method: method, Path: path, Args: args}
}

func TestDiffOperations_ClassifiesEachOperation(t *testing.T) {
	stored := ops(
		op("getOrder", "GET", "/orders/{order_id}", webapi.Arg{Name: "order_id", In: webapi.InPath, Required: true}),
		op("listOrders", "GET", "/orders"),
		op("cancelOrder", "DELETE", "/orders/{order_id}", webapi.Arg{Name: "order_id", In: webapi.InPath, Required: true}),
	)
	incoming := ops(
		// unchanged
		op("getOrder", "GET", "/orders/{order_id}", webapi.Arg{Name: "order_id", In: webapi.InPath, Required: true}),
		// changed: a new query argument
		op("listOrders", "GET", "/orders", webapi.Arg{Name: "limit", In: webapi.InQuery}),
		// added
		op("refundOrder", "POST", "/orders/{order_id}/refund", webapi.Arg{Name: "order_id", In: webapi.InPath, Required: true}),
		// cancelOrder is gone
	)

	diff := webapi.DiffOperations("orders", stored, incoming)
	if diff.Added != 1 || diff.Changed != 1 || diff.Removed != 1 || diff.Unchanged != 1 {
		t.Fatalf("counts = %+v", diff)
	}
	if !diff.HasRemovals() {
		t.Error("HasRemovals = false with a removal present")
	}
	// A removal has to name the step id, because that is what a reader searches
	// their flows for.
	if got := diff.RemovedStepIDs(); len(got) != 1 || got[0] != "api:orders:cancelOrder" {
		t.Errorf("RemovedStepIDs = %v", got)
	}
}

func TestDiffOperations_ArgumentOrderIsNotAChange(t *testing.T) {
	stored := ops(op("x", "GET", "/x",
		webapi.Arg{Name: "a", In: webapi.InQuery}, webapi.Arg{Name: "b", In: webapi.InQuery}))
	incoming := ops(op("x", "GET", "/x",
		webapi.Arg{Name: "b", In: webapi.InQuery}, webapi.Arg{Name: "a", In: webapi.InQuery}))

	// Spec regeneration reorders things freely. Reporting that as a change
	// would train an admin to click through refreshes without reading them,
	// which is exactly what must not happen before a removal.
	if d := webapi.DiffOperations("orders", stored, incoming); d.Changed != 0 || d.Unchanged != 1 {
		t.Errorf("counts = %+v, want it unchanged", d)
	}
}

func TestDiffOperations_ARenamedOperationIsARemovalAndAnAddition(t *testing.T) {
	stored := ops(op("get_order", "GET", "/orders/{id}", webapi.Arg{Name: "id", In: webapi.InPath, Required: true}))
	incoming := ops(op("fetchOrder", "GET", "/orders/{id}", webapi.Arg{Name: "id", In: webapi.InPath, Required: true}))

	// Matched on id and nothing else: the id is what a flow references, so an
	// edited operationId genuinely does break flows and the admin must be told
	// rather than shielded by a clever path-based match.
	d := webapi.DiffOperations("orders", stored, incoming)
	if d.Added != 1 || d.Removed != 1 {
		t.Errorf("counts = %+v, want a removal and an addition", d)
	}
}

func TestApplyRefresh_KeepsRemovalsUntilTheyAreConfirmed(t *testing.T) {
	stored := ops(op("a", "GET", "/a"), op("gone", "GET", "/gone"))
	incoming := ops(op("a", "GET", "/a"), op("new", "GET", "/new"))

	// Unconfirmed, a refresh is strictly ADDITIVE. A vendor's build pipeline can
	// trigger a spec change; it must not be able to cost a flow its steps.
	kept := webapi.ApplyRefresh(stored, incoming, false)
	if len(kept) != 3 {
		t.Fatalf("unconfirmed refresh produced %d operations, want all three", len(kept))
	}
	var found bool
	for _, o := range kept {
		if o.ID == "gone" {
			found = true
		}
	}
	if !found {
		t.Error("the unconfirmed refresh dropped an operation flows may reference")
	}

	// Confirmed, the removal applies.
	applied := webapi.ApplyRefresh(stored, incoming, true)
	if len(applied) != 2 {
		t.Fatalf("confirmed refresh produced %d operations, want two", len(applied))
	}
	for _, o := range applied {
		if o.ID == "gone" {
			t.Error("a confirmed removal was not applied")
		}
	}
}

func TestApplyRefresh_TakesTheIncomingShapeForAChange(t *testing.T) {
	stored := ops(op("a", "GET", "/a"))
	incoming := ops(op("a", "GET", "/a", webapi.Arg{Name: "limit", In: webapi.InQuery}))
	got := webapi.ApplyRefresh(stored, incoming, false)
	if len(got) != 1 || len(got[0].Args) != 1 {
		t.Fatalf("got %+v, want the incoming operation's arguments", got)
	}
}

// A spec parsed and then diffed against itself is the refresh nobody needs to
// confirm. Worth pinning end to end: it is the common case, and if the parser
// were non-deterministic in any way this is where it would show.
func TestParseThenRefresh_IsAllUnchanged(t *testing.T) {
	first := parse(t, ordersSpec)
	second := parse(t, ordersSpec)
	d := webapi.DiffOperations("orders", first.Operations, second.Operations)
	if d.Changed != 0 || d.Added != 0 || d.Removed != 0 {
		t.Errorf("re-importing an unchanged spec reported %+v", d)
	}
}

// TestFetchSpec covers the guarded spec fetch. A spec URL is tenant-supplied,
// so the note's rule is that it goes through the same Doer a step's call does —
// never a bare http.Client. These pin the boundary conditions around that: no
// Doer wired, a non-https address, a non-2xx answer, and the happy path.
func TestFetchSpec(t *testing.T) {
	ctx := context.Background()

	// With no Doer installed the fetch must refuse rather than quietly fall
	// back to an unguarded client — that fallback would make every SSRF and
	// egress guard in the package opt-in.
	webapi.SetDoer(nil)
	if _, err := webapi.FetchSpec(ctx, "https://api.example.com/spec.yaml"); err == nil {
		t.Fatal("FetchSpec succeeded with no guarded caller wired")
	} else if !strings.Contains(err.Error(), "no guarded HTTP caller") {
		t.Errorf("error = %q, want it to name the missing caller", err)
	}

	// A Doer that records what it was asked for and returns the running example.
	var gotURL, gotMethod string
	var gotAccept string
	var gotMaxBytes int
	install := func(status int, body string) {
		webapi.SetDoer(func(_ context.Context, method, u string, headers map[string]string,
			_ []byte, _, maxBytes int) (int, []byte, http.Header, error) {
			gotMethod, gotURL = method, u
			gotAccept = headers["Accept"]
			gotMaxBytes = maxBytes
			return status, []byte(body), nil, nil
		})
		t.Cleanup(func() { webapi.SetDoer(nil) })
	}
	install(200, ordersSpec)

	// https only, held at the same boundary as a catalog's base URL: a spec
	// fetched over cleartext is one an intermediary can rewrite, and what it
	// would rewrite is where every step of the catalog calls.
	for _, bad := range []string{
		"http://api.example.com/spec.yaml",
		"HTTP://api.example.com/spec.yaml",
		"ftp://api.example.com/spec.yaml",
		"file:///etc/passwd",
		"//api.example.com/spec.yaml", // no scheme
		"https://",                    // no host
		"not a url at all",
		"",
		"   ",
	} {
		if _, err := webapi.FetchSpec(ctx, bad); err == nil {
			t.Errorf("FetchSpec(%q) was accepted", bad)
		} else if !strings.Contains(err.Error(), "https://") {
			t.Errorf("FetchSpec(%q) error = %q, want it to state the https rule", bad, err)
		}
	}
	// None of those should have reached the caller.
	if gotURL != "" {
		t.Errorf("a rejected address still hit the network: %q", gotURL)
	}

	// The happy path: parsed, and fetched through the guarded caller with the
	// response cap applied.
	got, err := webapi.FetchSpec(ctx, "  https://api.example.com/spec.yaml  ")
	if err != nil {
		t.Fatalf("FetchSpec: %v", err)
	}
	if len(got.Operations) == 0 {
		t.Error("FetchSpec returned no operations")
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	// The surrounding whitespace is trimmed before the request.
	if gotURL != "https://api.example.com/spec.yaml" {
		t.Errorf("url = %q, want it trimmed", gotURL)
	}
	if !strings.Contains(gotAccept, "application/json") || !strings.Contains(gotAccept, "yaml") {
		t.Errorf("Accept = %q, want it to offer json and yaml", gotAccept)
	}
	if gotMaxBytes <= 0 {
		t.Errorf("maxBytes = %d, want the response cap applied", gotMaxBytes)
	}

	// A non-2xx answer is reported with its status, not parsed as a document.
	for _, status := range []int{301, 404, 401, 500} {
		install(status, `{"error":"nope"}`)
		_, err := webapi.FetchSpec(ctx, "https://api.example.com/spec.yaml")
		if err == nil {
			t.Errorf("status %d was accepted", status)
			continue
		}
		if !strings.Contains(err.Error(), fmt.Sprint(status)) {
			t.Errorf("status %d error = %q, want it to name the status", status, err)
		}
	}

	// A transport failure surfaces as a fetch error, not a parse error.
	webapi.SetDoer(func(_ context.Context, _, _ string, _ map[string]string,
		_ []byte, _, _ int) (int, []byte, http.Header, error) {
		return 0, nil, nil, errors.New("dial guard blocked a private address")
	})
	if _, err := webapi.FetchSpec(ctx, "https://api.example.com/spec.yaml"); err == nil {
		t.Fatal("a transport failure was not reported")
	} else if !strings.Contains(err.Error(), "could not fetch") {
		t.Errorf("error = %q, want a fetch failure", err)
	}

	// A 200 carrying something that isn't a spec still fails at the parser.
	install(200, `{"hello":"world"}`)
	if _, err := webapi.FetchSpec(ctx, "https://api.example.com/spec.yaml"); err == nil {
		t.Fatal("a non-spec 200 body was accepted")
	}
}
