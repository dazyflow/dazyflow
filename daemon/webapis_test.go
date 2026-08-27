// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
	"git.sr.ht/~klahr/dazyflow/engine/webapi"
)

func webAPIService(t *testing.T) *WebAPIs {
	t.Helper()
	return &WebAPIs{Store: NewMemWebAPIStore(), Catalog: webapi.NewCatalog()}
}

func sampleOps() []webapi.Operation {
	return []webapi.Operation{{
		ID:      "get_order",
		Method:  "GET",
		Path:    "/orders/{order_id}",
		Summary: "Fetch one order",
		Args: []webapi.Arg{
			{Name: "order_id", In: webapi.InPath, Type: "string", Required: true},
		},
	}}
}

func sampleInput() WebAPIInput {
	return WebAPIInput{
		Label:      "Order service",
		BaseURL:    "https://api.example.com/v1",
		AuthKind:   webapi.AuthBearer,
		Operations: sampleOps(),
		Enabled:    true,
	}
}

// A save is the whole of the feedback: no handshake, so a saved catalog is a
// registered catalog and its steps are resolvable immediately.
func TestWebAPIs_SaveRegisters(t *testing.T) {
	m := webAPIService(t)
	saved, err := m.Save(context.Background(), "acme", "alice", sampleInput())
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.Name != "order-service" {
		t.Errorf("name = %q, want it derived from the label", saved.Name)
	}
	if saved.Label != "Order service" {
		t.Errorf("label = %q", saved.Label)
	}
	if _, ok := m.Catalog.Get("acme", "api:order-service:get_order"); !ok {
		t.Error("the step is not resolvable after a successful save")
	}
	if _, ok := m.Catalog.Get("globex", "api:order-service:get_order"); ok {
		t.Error("another org can resolve it")
	}
}

// The engine's descriptor validation is the daemon's validation — called, not
// reimplemented — so a placeholder with no argument is refused at save time
// with the engine's own message.
func TestWebAPIs_SaveUsesTheEngineValidation(t *testing.T) {
	m := webAPIService(t)
	in := sampleInput()
	in.Operations[0].Path = "/orders/{order_id}/lines/{line_id}"
	_, err := m.Save(context.Background(), "acme", "alice", in)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "no argument declares it") {
		t.Errorf("error = %q, want the engine's descriptor message", err)
	}
	// And nothing was stored: a refused save must not leave a row that the
	// reconcile loop would then fail to register forever.
	rows, err := m.Store.List(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %+v, want none", rows)
	}
}

func TestWebAPIs_SavePolicyChecks(t *testing.T) {
	cases := []struct {
		name string
		in   func(*WebAPIInput)
		want string
	}{
		{"no base URL", func(in *WebAPIInput) { in.BaseURL = "" }, "URL is empty"},
		{"no operations", func(in *WebAPIInput) { in.Operations = nil }, "at least one operation"},
		{"no label and no name", func(in *WebAPIInput) { in.Label = "" }, "name is empty"},
		{"bad explicit name", func(in *WebAPIInput) { in.Name = "Order Service" }, "lowercase letters"},
		{"header auth with a bad header", func(in *WebAPIInput) {
			in.AuthKind = webapi.AuthHeader
			in.AuthHeader = "X Api Key"
		}, "header name may use"},
		{"label too long", func(in *WebAPIInput) { in.Label = strings.Repeat("a", maxWebAPILabelLen+1) }, "too long"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := webAPIService(t)
			in := sampleInput()
			tc.in(&in)
			_, err := m.Save(context.Background(), "acme", "alice", in)
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// Cleartext http is refused because the credential rides in a header — with the
// operator's private-egress opt-in as the one exception, which is how someone
// reaches a service on their own laptop. daemon/main_test.go turns that opt-in
// ON for the whole package, so this test has to restore the production default
// to see the refusal at all.
func TestWebAPIs_SaveRefusesCleartextHTTP(t *testing.T) {
	hfnet.SetAllowPrivateEgress(false)
	defer hfnet.SetAllowPrivateEgress(true)

	m := webAPIService(t)
	in := sampleInput()
	in.BaseURL = "http://api.example.com"
	_, err := m.Save(context.Background(), "acme", "alice", in)
	if err == nil || !strings.Contains(err.Error(), "must be https") {
		t.Fatalf("err = %v, want a refusal of cleartext http", err)
	}

	// With the opt-in back on it is accepted again, which is the developer case.
	hfnet.SetAllowPrivateEgress(true)
	if _, err := m.Save(context.Background(), "acme", "alice", in); err != nil {
		t.Fatalf("private egress is on, so http should be accepted: %v", err)
	}
}

// The curation guard the design note argues for: an import that would file too
// many operations is refused with the instruction to select fewer, rather than
// drowning the palette and the generator's grounding.
func TestWebAPIs_OperationCap(t *testing.T) {
	m := webAPIService(t)
	in := sampleInput()
	in.Operations = nil
	for i := 0; i <= maxWebAPIOperations; i++ {
		in.Operations = append(in.Operations, webapi.Operation{
			ID: "op_" + string(rune('a'+i%26)) + strings.Repeat("x", i/26), Method: "GET", Path: "/x",
		})
	}
	_, err := m.Save(context.Background(), "acme", "alice", in)
	if err == nil || !strings.Contains(err.Error(), "more than one catalog may hold") {
		t.Fatalf("error = %v, want the operation cap", err)
	}
}

func TestWebAPIs_ArgCap(t *testing.T) {
	m := webAPIService(t)
	in := sampleInput()
	for i := 0; i <= maxWebAPIArgs; i++ {
		in.Operations[0].Args = append(in.Operations[0].Args, webapi.Arg{
			Name: "a" + strings.Repeat("b", i), In: webapi.InQuery, Type: "string",
		})
	}
	_, err := m.Save(context.Background(), "acme", "alice", in)
	if err == nil || !strings.Contains(err.Error(), "arguments (max") {
		t.Fatalf("error = %v, want the argument cap", err)
	}
}

func TestWebAPIs_PerTenantCap(t *testing.T) {
	m := webAPIService(t)
	store := m.Store.(*MemWebAPIStore)
	for i := 0; i < maxWebAPIsPerTenant; i++ {
		row := WebAPI{Tenant: "acme", Name: "filler-" + strings.Repeat("x", i%20) + string(rune('a'+i%26)), Enabled: true}
		if err := store.Put(context.Background(), row); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Save(context.Background(), "acme", "alice", sampleInput()); err == nil ||
		!strings.Contains(err.Error(), "the maximum") {
		t.Fatalf("error = %v, want the per-tenant cap", err)
	}
}

// A derived id collides with an existing one, so it is numbered rather than
// silently replacing the other catalog.
func TestWebAPIs_DerivedNamesAreUnique(t *testing.T) {
	m := webAPIService(t)
	first, err := m.Save(context.Background(), "acme", "alice", sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Save(context.Background(), "acme", "alice", sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	if first.Name == second.Name {
		t.Fatalf("both catalogs got the id %q", first.Name)
	}
	if second.Name != "order-service-2" {
		t.Errorf("second name = %q, want a numbered variant", second.Name)
	}
}

// Saving under an existing name replaces it — and an operation the new
// descriptor drops stops resolving, which is what a re-import means.
func TestWebAPIs_SaveReplacesAndDropsOperations(t *testing.T) {
	m := webAPIService(t)
	in := sampleInput()
	in.Operations = append(in.Operations, webapi.Operation{
		ID: "list_orders", Method: "GET", Path: "/orders",
	})
	saved, err := m.Save(context.Background(), "acme", "alice", in)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Catalog.Get("acme", "api:order-service:list_orders"); !ok {
		t.Fatal("second operation missing")
	}

	edit := sampleInput()
	edit.Name = saved.Name
	edit.Label = "" // blank keeps the stored label
	if _, err := m.Save(context.Background(), "acme", "alice", edit); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Catalog.Get("acme", "api:order-service:list_orders"); ok {
		t.Error("an operation removed by the edit still resolves")
	}
	stored, err := m.Store.Get(context.Background(), "acme", saved.Name)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Label != "Order service" {
		t.Errorf("label = %q, want the stored one kept when the edit sent none", stored.Label)
	}
	if !stored.CreatedAt.Equal(saved.CreatedAt) || stored.CreatedBy != "alice" {
		t.Errorf("creation metadata changed on edit: %+v", stored)
	}
}

// Disabling keeps the row and takes the steps out of the palette — the
// reversible half of deleting.
func TestWebAPIs_DisableAndReEnable(t *testing.T) {
	m := webAPIService(t)
	saved, err := m.Save(context.Background(), "acme", "alice", sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	off := sampleInput()
	off.Name = saved.Name
	off.Enabled = false
	if _, err := m.Save(context.Background(), "acme", "alice", off); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Catalog.Get("acme", "api:order-service:get_order"); ok {
		t.Error("a disabled catalog still contributes steps")
	}
	if rows, _ := m.Store.List(context.Background(), "acme"); len(rows) != 1 {
		t.Errorf("rows = %+v, want the row kept", rows)
	}
	on := sampleInput()
	on.Name = saved.Name
	if _, err := m.Save(context.Background(), "acme", "alice", on); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Catalog.Get("acme", "api:order-service:get_order"); !ok {
		t.Error("re-enabling did not restore the steps")
	}
}

func TestWebAPIs_Delete(t *testing.T) {
	m := webAPIService(t)
	if _, err := m.Save(context.Background(), "acme", "alice", sampleInput()); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(context.Background(), "acme", "order-service"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Catalog.Get("acme", "api:order-service:get_order"); ok {
		t.Error("steps survive a delete")
	}
	if err := m.Delete(context.Background(), "acme", "order-service"); err == nil {
		t.Error("deleting twice should report not-found")
	}
}

// Reconcile is what puts an org's steps back after a restart: the rows are in
// the store, the engine catalog is empty, and one pass rebuilds it.
func TestWebAPIs_ReconcileRebuildsAfterRestart(t *testing.T) {
	store := NewMemWebAPIStore()
	first := &WebAPIs{Store: store, Catalog: webapi.NewCatalog()}
	if _, err := first.Save(context.Background(), "acme", "alice", sampleInput()); err != nil {
		t.Fatal(err)
	}

	// A fresh process: same store, new (empty) catalog.
	second := &WebAPIs{Store: store, Catalog: webapi.NewCatalog()}
	if _, ok := second.Catalog.Get("acme", "api:order-service:get_order"); ok {
		t.Fatal("the new catalog is not empty; the test proves nothing")
	}
	if err := second.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := second.Catalog.Get("acme", "api:order-service:get_order"); !ok {
		t.Fatal("reconcile did not restore the org's steps")
	}
}

// An edit on another replica carries a newer UpdatedAt, which is how this one
// notices. A row that has not changed is skipped.
func TestWebAPIs_ReconcilePicksUpAnotherReplicasEdit(t *testing.T) {
	store := NewMemWebAPIStore()
	clock := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	here := &WebAPIs{Store: store, Catalog: webapi.NewCatalog(), Now: func() time.Time { return clock }}
	if _, err := here.Save(context.Background(), "acme", "alice", sampleInput()); err != nil {
		t.Fatal(err)
	}
	if err := here.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Another replica edits: same store, later UpdatedAt, an extra operation.
	there := &WebAPIs{Store: store, Catalog: webapi.NewCatalog(), Now: func() time.Time { return clock.Add(time.Minute) }}
	edit := sampleInput()
	edit.Name = "order-service"
	edit.Operations = append(edit.Operations, webapi.Operation{ID: "list_orders", Method: "GET", Path: "/orders"})
	if _, err := there.Save(context.Background(), "acme", "alice", edit); err != nil {
		t.Fatal(err)
	}

	if err := here.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := here.Catalog.Get("acme", "api:order-service:list_orders"); !ok {
		t.Fatal("this replica did not pick up the other's edit")
	}
}

// A disabled or deleted row is taken down on the next pass, wherever the change
// was made.
func TestWebAPIs_ReconcileTakesDownWhatTheStoreNoLongerWants(t *testing.T) {
	store := NewMemWebAPIStore()
	m := &WebAPIs{Store: store, Catalog: webapi.NewCatalog()}
	if _, err := m.Save(context.Background(), "acme", "alice", sampleInput()); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(context.Background(), "acme", "order-service"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Catalog.Get("acme", "api:order-service:get_order"); ok {
		t.Error("a row deleted elsewhere still contributes steps here")
	}
}

// The one case LastError exists for: a stored descriptor the current code
// refuses. The org's steps are missing, and the row says why instead of the
// palette going quiet.
func TestWebAPIs_ReconcileRecordsAnUnregisterableRow(t *testing.T) {
	store := NewMemWebAPIStore()
	// Written straight to the store, bypassing Save — which is exactly what a
	// release that tightened validation leaves behind.
	if err := store.Put(context.Background(), WebAPI{
		Tenant: "acme", Name: "broken", BaseURL: "https://api.example.com",
		Operations: []webapi.Operation{{ID: "op", Method: "GET", Path: "/x/{missing}"}},
		Enabled:    true, UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	m := &WebAPIs{Store: store, Catalog: webapi.NewCatalog()}
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("one bad row must not fail the whole pass: %v", err)
	}
	row, err := store.Get(context.Background(), "acme", "broken")
	if err != nil {
		t.Fatal(err)
	}
	if row.LastError == "" {
		t.Fatal("nothing recorded: the org would see missing steps and no reason")
	}
	if !strings.Contains(row.LastError, "no argument declares it") {
		t.Errorf("last_error = %q, want the validation reason", row.LastError)
	}
}

// A pass must not fail the healthy rows because one row is broken.
func TestWebAPIs_ReconcileContinuesPastABadRow(t *testing.T) {
	store := NewMemWebAPIStore()
	if err := store.Put(context.Background(), WebAPI{
		Tenant: "acme", Name: "broken", BaseURL: "https://api.example.com",
		Operations: []webapi.Operation{{ID: "op", Method: "GET", Path: "/x/{missing}"}},
		Enabled:    true, UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	m := &WebAPIs{Store: store, Catalog: webapi.NewCatalog()}
	if _, err := m.Save(context.Background(), "acme", "alice", sampleInput()); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Catalog.Get("acme", "api:order-service:get_order"); !ok {
		t.Error("a healthy catalog was dropped because another row was broken")
	}
}

// Operations survive the store's encoding. The memory store round-trips through
// the same JSON a deployment writes, so a tag mismatch fails here too.
func TestWebAPIs_OperationsSurviveStorage(t *testing.T) {
	m := webAPIService(t)
	in := sampleInput()
	in.Operations[0].BodyMode = webapi.BodyNone
	in.Operations[0].Args[0].Label = "Order id"
	in.Operations[0].Args[0].Schema = []byte(`{"type":"string","pattern":"^o-"}`)
	saved, err := m.Save(context.Background(), "acme", "alice", in)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := m.Store.Get(context.Background(), "acme", saved.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Operations) != 1 {
		t.Fatalf("operations = %+v", stored.Operations)
	}
	op := stored.Operations[0]
	if op.ID != "get_order" || op.Method != "GET" || op.Path != "/orders/{order_id}" {
		t.Errorf("operation lost fields in storage: %+v", op)
	}
	if len(op.Args) != 1 || op.Args[0].Name != "order_id" || op.Args[0].In != webapi.InPath ||
		!op.Args[0].Required || op.Args[0].Label != "Order id" {
		t.Errorf("argument lost fields in storage: %+v", op.Args)
	}
	if !strings.Contains(string(op.Args[0].Schema), "pattern") {
		t.Errorf("argument schema lost in storage: %s", op.Args[0].Schema)
	}
}

func TestWebAPIs_UnconfiguredIsReported(t *testing.T) {
	var m *WebAPIs
	if _, err := m.List(context.Background(), "acme"); err != ErrWebAPIsUnconfigured {
		t.Errorf("List err = %v", err)
	}
	if _, err := m.Save(context.Background(), "acme", "a", sampleInput()); err != ErrWebAPIsUnconfigured {
		t.Errorf("Save err = %v", err)
	}
	if err := m.Delete(context.Background(), "acme", "x"); err != ErrWebAPIsUnconfigured {
		t.Errorf("Delete err = %v", err)
	}
	if err := m.Reconcile(context.Background()); err != ErrWebAPIsUnconfigured {
		t.Errorf("Reconcile err = %v", err)
	}
}

// A tenant must not be able to claim a built-in app's name. Connection fields
// are found by slug, first match wins over a map, so a collision would make the
// real app's page show these fields on some requests and its own on others.
func TestWebAPIs_RefusesAReservedIntegrationName(t *testing.T) {
	m := webAPIService(t)
	m.ReservedIntegration = func(slug string) bool { return slug == "gmail" }

	in := sampleInput()
	in.Integration = "Gmail"
	_, err := m.Save(context.Background(), "acme", "alice", in)
	if err == nil || !strings.Contains(err.Error(), "already has") {
		t.Fatalf("err = %v, want a refusal of the built-in app name", err)
	}

	// The fallback is checked too, not just an explicit value: with no app name
	// the label supplies one, and it must face the same rule.
	in = sampleInput()
	in.Integration = ""
	in.Label = "gmail"
	if _, err := m.Save(context.Background(), "acme", "alice", in); err == nil ||
		!strings.Contains(err.Error(), "already has") {
		t.Fatalf("err = %v, want the derived app name refused as well", err)
	}
}

// Two of an org's own catalogs cannot share one connection either: an address
// typed for one would be sent by the other.
func TestWebAPIs_RefusesADuplicateIntegrationWithinTheOrg(t *testing.T) {
	m := webAPIService(t)
	first := sampleInput()
	first.Integration = "Internal services"
	if _, err := m.Save(context.Background(), "acme", "alice", first); err != nil {
		t.Fatal(err)
	}
	second := sampleInput()
	second.Label = "Pricing service"
	second.Integration = "internal services" // same slug, different capitalisation
	_, err := m.Save(context.Background(), "acme", "alice", second)
	if err == nil || !strings.Contains(err.Error(), "already uses that app name") {
		t.Fatalf("err = %v, want the duplicate app name refused", err)
	}

	// But editing the SAME catalog keeps its own app name, which would otherwise
	// look like a collision with itself.
	edit := sampleInput()
	edit.Name = "order-service"
	edit.Integration = "Internal services"
	if _, err := m.Save(context.Background(), "acme", "alice", edit); err != nil {
		t.Fatalf("editing a catalog collided with itself: %v", err)
	}

	// Another org is unaffected: connections are per-tenant.
	other := sampleInput()
	other.Integration = "Internal services"
	if _, err := m.Save(context.Background(), "globex", "bob", other); err != nil {
		t.Fatalf("another org's identical app name was refused: %v", err)
	}
}

// The app name is what the connection page is keyed by, so a value with nothing
// slug-able in it is refused rather than stored as a row nothing can connect.
func TestWebAPIs_RefusesAnUnusableIntegrationName(t *testing.T) {
	m := webAPIService(t)
	// It ends up inside a secret name (conn.<slug>.<field>), whose validator
	// allows only [A-Za-z0-9_.-] — so a name that slugs outside that set has to
	// be refused HERE, not later on a connection page the admin cannot associate
	// with what they typed.
	for _, bad := range []string{"Ordrar!", "orders/v2", "a.b", "!!! ???"} {
		in := sampleInput()
		in.Integration = bad
		if _, err := m.Save(context.Background(), "acme", "alice", in); err == nil {
			t.Errorf("%q was accepted as an app name", bad)
		}
	}
}

// A duplicate LABEL is legal — that is what the numbered ids are for — so the
// derived app name falls back to the catalog's own id rather than refusing the
// second catalog outright.
func TestWebAPIs_DerivedIntegrationFallsBackToTheID(t *testing.T) {
	m := webAPIService(t)
	first, err := m.Save(context.Background(), "acme", "alice", sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Save(context.Background(), "acme", "alice", sampleInput())
	if err != nil {
		t.Fatalf("a second catalog with the same label was refused: %v", err)
	}
	if first.Integration != "Order service" {
		t.Errorf("first integration = %q, want the label", first.Integration)
	}
	if second.Integration != second.Name {
		t.Errorf("second integration = %q, want it to fall back to the id %q", second.Integration, second.Name)
	}
}

// The stored row carries the app name explicitly, so what a connection attaches
// to is readable rather than re-derived downstream.
func TestWebAPIs_StoresTheIntegrationExplicitly(t *testing.T) {
	m := webAPIService(t)
	saved, err := m.Save(context.Background(), "acme", "alice", sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	if saved.Integration != "Order service" {
		t.Fatalf("integration = %q, want it defaulted from the label", saved.Integration)
	}
	tr, ok := m.Catalog.Get("acme", "api:order-service:get_order")
	if !ok {
		t.Fatal("step missing")
	}
	man := tr.Manifest()
	if man.Integration != "Order service" {
		t.Errorf("manifest integration = %q — connection injection keys off this", man.Integration)
	}
	if len(man.ConnectionFields) == 0 {
		t.Error("no connection fields: the org would have nowhere to enter the address")
	}
}

// An edit must not move the Apps page this catalog is connected on. The org has
// already entered an address and a credential there; re-deriving the app name
// from an edit that never mentioned it would orphan both.
func TestWebAPIs_EditKeepsTheIntegration(t *testing.T) {
	m := webAPIService(t)
	saved, err := m.Save(context.Background(), "acme", "alice", sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	if saved.Integration != "Order service" {
		t.Fatalf("integration = %q", saved.Integration)
	}

	// The shape an API caller changing only the address sends: no label, no app
	// name.
	edit := WebAPIInput{
		Name:       saved.Name,
		BaseURL:    "https://staging.example.com",
		AuthKind:   webapi.AuthBearer,
		Operations: sampleOps(),
		Enabled:    true,
	}
	after, err := m.Save(context.Background(), "acme", "alice", edit)
	if err != nil {
		t.Fatal(err)
	}
	if after.Integration != "Order service" {
		t.Errorf("integration = %q after an edit that never mentioned it, want it unchanged", after.Integration)
	}

	// And a relabel keeps it too: renaming what people see must cost nothing.
	relabel := sampleInput()
	relabel.Name = saved.Name
	relabel.Label = "Orders (production)"
	after, err = m.Save(context.Background(), "acme", "alice", relabel)
	if err != nil {
		t.Fatal(err)
	}
	if after.Integration != "Order service" {
		t.Errorf("integration = %q after a relabel, want it unchanged", after.Integration)
	}
	if after.Label != "Orders (production)" {
		t.Errorf("label = %q, want the new one", after.Label)
	}

	// Asking for a different app name explicitly is honoured — it is a move the
	// caller stated.
	moved := sampleInput()
	moved.Name = saved.Name
	moved.Integration = "Orders API"
	after, err = m.Save(context.Background(), "acme", "alice", moved)
	if err != nil {
		t.Fatal(err)
	}
	if after.Integration != "Orders API" {
		t.Errorf("integration = %q, want the explicit move honoured", after.Integration)
	}
}

// TestWebAPIs_LabelReachesTheManifest guards the wiring the caption depends on.
//
// The label lives on the row and the manifest is built from the Descriptor, so
// a Descriptor() that forgets to pass it leaves every step captioned by its
// slug — with nothing failing anywhere. Exactly the kind of gap a test has to
// hold, because the feature still "works".
func TestWebAPIs_LabelReachesTheManifest(t *testing.T) {
	m := webAPIService(t)
	in := sampleInput()
	in.Label = "Order service"
	saved, err := m.Save(context.Background(), "acme", "admin@acme.test", in)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.Name != "order-service" {
		t.Fatalf("derived name = %q", saved.Name)
	}
	if got := saved.Descriptor().DisplayName(); got != "Order service" {
		t.Fatalf("Descriptor().DisplayName() = %q, want the label", got)
	}
	man, ok := m.Catalog.ManifestsFor("acme")["api:order-service:get_order"]
	if !ok {
		t.Fatalf("no manifest: %v", m.Catalog.ManifestsFor("acme"))
	}
	if man.Label != "Order service — get_order" {
		t.Errorf("Label = %q, want the catalog's human name", man.Label)
	}
}

// TestWebAPIs_OperationNameCaptionsTheStep is the other half: a per-operation
// name, typed in the admin form, reaching the palette.
func TestWebAPIs_OperationNameCaptionsTheStep(t *testing.T) {
	m := webAPIService(t)
	in := sampleInput()
	in.Label = "Order service"
	in.Operations[0].Title = "Fetch an order"
	if _, err := m.Save(context.Background(), "acme", "a", in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	man := m.Catalog.ManifestsFor("acme")["api:order-service:get_order"]
	if man.Label != "Order service — Fetch an order" {
		t.Errorf("Label = %q, want both typed names", man.Label)
	}
	// And the step id is untouched, so naming an operation never moves a flow.
	if man.ID != "api:order-service:get_order" {
		t.Errorf("ID = %q, want it unmoved by naming", man.ID)
	}
}

// TestWebAPIs_OperationNameIsBounded: a name captions a palette row, and the
// summary is the field for a sentence.
func TestWebAPIs_OperationNameIsBounded(t *testing.T) {
	m := webAPIService(t)
	in := sampleInput()
	in.Operations[0].Title = strings.Repeat("x", maxWebAPIOpTitleLen+1)
	_, err := m.Save(context.Background(), "acme", "a", in)
	if err == nil || !strings.Contains(err.Error(), "summary") {
		t.Fatalf("err = %v, want a complaint pointing at the summary field", err)
	}
}

// The catalog's brand mark is guessed once, from the base URL, and reaches
// every step this catalog contributes — the point of the guess.
func TestWebAPIs_SaveResolvesTheLogo(t *testing.T) {
	m := webAPIService(t)
	var asked []string
	m.ResolveLogo = func(_ context.Context, baseURL string) string {
		asked = append(asked, baseURL)
		return "data:image/png;base64,AAAA"
	}
	saved, err := m.Save(context.Background(), "acme", "alice", sampleInput())
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.Logo != "data:image/png;base64,AAAA" {
		t.Errorf("logo = %q", saved.Logo)
	}
	if len(asked) != 1 || asked[0] != "https://api.example.com/v1" {
		t.Errorf("resolver asked %v, want the catalog's base URL once", asked)
	}
	transport, ok := m.Catalog.Get("acme", "api:order-service:get_order")
	if !ok {
		t.Fatal("the step is not registered")
	}
	if got := transport.Manifest().BrandLogo; got != saved.Logo {
		t.Errorf("the step's BrandLogo = %q, want the catalog's logo", got)
	}
}

// An ordinary edit must not cost outbound requests: a relabel or one more
// operation is not a new service, and a save should not be able to hang on
// someone else's web server for a decoration it already has.
func TestWebAPIs_SaveKeepsTheLogoWhenTheAddressHasNotMoved(t *testing.T) {
	m := webAPIService(t)
	calls := 0
	m.ResolveLogo = func(_ context.Context, baseURL string) string {
		calls++
		return "data:image/png;base64," + baseURL
	}
	first, err := m.Save(context.Background(), "acme", "alice", sampleInput())
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	relabel := sampleInput()
	relabel.Name = first.Name
	relabel.Label = "Orders"
	again, err := m.Save(context.Background(), "acme", "alice", relabel)
	if err != nil {
		t.Fatalf("re-Save: %v", err)
	}
	if calls != 1 {
		t.Errorf("resolver ran %d times across two saves of the same address", calls)
	}
	if again.Logo != first.Logo {
		t.Errorf("logo = %q, want the stored one kept", again.Logo)
	}

	// A new address is a new service, and gets a fresh guess.
	moved := relabel
	moved.BaseURL = "https://api.example.org/v2"
	after, err := m.Save(context.Background(), "acme", "alice", moved)
	if err != nil {
		t.Fatalf("moved Save: %v", err)
	}
	if calls != 2 {
		t.Errorf("resolver ran %d times, want a second guess for the new address", calls)
	}
	if after.Logo != "data:image/png;base64,https://api.example.org/v2" {
		t.Errorf("logo = %q, want one resolved from the new address", after.Logo)
	}
}

// A catalog with nothing to keep tries again, which is what makes "press Save"
// the retry for a service whose site was down the first time.
func TestWebAPIs_SaveRetriesAnUnresolvedLogo(t *testing.T) {
	m := webAPIService(t)
	calls := 0
	m.ResolveLogo = func(context.Context, string) string {
		calls++
		return ""
	}
	first, err := m.Save(context.Background(), "acme", "alice", sampleInput())
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if first.Logo != "" {
		t.Errorf("logo = %q, want none", first.Logo)
	}
	again := sampleInput()
	again.Name = first.Name
	if _, err := m.Save(context.Background(), "acme", "alice", again); err != nil {
		t.Fatalf("re-Save: %v", err)
	}
	if calls != 2 {
		t.Errorf("resolver ran %d times, want the second save to try again", calls)
	}
}

// A guess that fails is not a save that fails: the whole feature is decoration.
func TestWebAPIs_SaveSurvivesAResolverThatFindsNothing(t *testing.T) {
	m := webAPIService(t)
	m.ResolveLogo = func(context.Context, string) string { return "" }
	saved, err := m.Save(context.Background(), "acme", "alice", sampleInput())
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, ok := m.Catalog.Get("acme", "api:"+saved.Name+":get_order"); !ok {
		t.Error("the step is not registered")
	}
}

// pngLogo is a real 1x1 PNG as a data: URI — the shape an admin's upload
// arrives in, and valid enough to survive the normaliser.
func pngLogo() string {
	png := []byte{
		0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
		0, 0, 0, 0x0d, 'I', 'H', 'D', 'R',
		0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0,
		0x1f, 0x15, 0xc4, 0x89,
		0, 0, 0, 0x0a, 'I', 'D', 'A', 'T',
		0x78, 0x9c, 0x63, 0, 1, 0, 0, 5, 0, 1,
		0x0d, 0x0a, 0x2d, 0xb4,
		0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
}

func logoMode(m WebAPILogoMode) *WebAPILogoMode { return &m }

// An uploaded mark wins over the guess, and keeps winning: the address moving is
// exactly when a guess must be re-run and an admin's choice must not be.
func TestWebAPIs_CustomLogoOutranksTheGuess(t *testing.T) {
	m := webAPIService(t)
	guesses := 0
	m.ResolveLogo = func(context.Context, string) string {
		guesses++
		return "data:image/png;base64,GUESS"
	}
	in := sampleInput()
	chosen := pngLogo()
	in.Logo = &chosen
	saved, err := m.Save(context.Background(), "acme", "alice", in)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.Logo != chosen {
		t.Errorf("logo = %.40q…, want the chosen image", saved.Logo)
	}
	// An image and no mode is a statement: use this.
	if saved.LogoMode != WebAPILogoCustom {
		t.Errorf("mode = %q, want %q", saved.LogoMode, WebAPILogoCustom)
	}
	if guesses != 0 {
		t.Errorf("guessed %d times for a catalog that supplied its own mark", guesses)
	}

	moved := sampleInput()
	moved.Name = saved.Name
	moved.BaseURL = "https://api.example.org/v2"
	after, err := m.Save(context.Background(), "acme", "alice", moved)
	if err != nil {
		t.Fatalf("moved Save: %v", err)
	}
	if after.Logo != chosen || after.LogoMode != WebAPILogoCustom {
		t.Errorf("logo/mode = %.20q…/%q after moving the address, want the upload kept",
			after.Logo, after.LogoMode)
	}
	if guesses != 0 {
		t.Errorf("guessed %d times, want none while a custom mark is set", guesses)
	}
}

// The plain glyph, chosen. It must not be mistaken for a guess that found
// nothing, or the wrong logo comes back on the next save.
func TestWebAPIs_LogoModeNoneIsNotRetried(t *testing.T) {
	m := webAPIService(t)
	guesses := 0
	m.ResolveLogo = func(context.Context, string) string {
		guesses++
		return "data:image/png;base64,GUESS"
	}
	in := sampleInput()
	in.LogoMode = logoMode(WebAPILogoNone)
	saved, err := m.Save(context.Background(), "acme", "alice", in)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.Logo != "" || saved.LogoMode != WebAPILogoNone {
		t.Fatalf("logo/mode = %q/%q", saved.Logo, saved.LogoMode)
	}
	again := sampleInput()
	again.Name = saved.Name
	after, err := m.Save(context.Background(), "acme", "alice", again)
	if err != nil {
		t.Fatalf("re-Save: %v", err)
	}
	if after.Logo != "" || after.LogoMode != WebAPILogoNone {
		t.Errorf("logo/mode = %q/%q, want the choice kept", after.Logo, after.LogoMode)
	}
	if guesses != 0 {
		t.Errorf("guessed %d times for a catalog that asked for no mark", guesses)
	}
}

// Going back to automatic drops the upload and looks again — the point of
// choosing automatic is to see what the service publishes.
func TestWebAPIs_BackToAutoDropsTheUpload(t *testing.T) {
	m := webAPIService(t)
	m.ResolveLogo = func(context.Context, string) string { return "data:image/png;base64,GUESS" }
	in := sampleInput()
	chosen := pngLogo()
	in.Logo = &chosen
	saved, err := m.Save(context.Background(), "acme", "alice", in)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	back := sampleInput()
	back.Name = saved.Name
	back.LogoMode = logoMode(WebAPILogoAuto)
	after, err := m.Save(context.Background(), "acme", "alice", back)
	if err != nil {
		t.Fatalf("re-Save: %v", err)
	}
	if after.Logo != "data:image/png;base64,GUESS" || after.LogoMode != WebAPILogoAuto {
		t.Errorf("logo/mode = %.40q…/%q, want the guess", after.Logo, after.LogoMode)
	}
}

// An edit of something else must not blank a mark the admin uploaded.
func TestWebAPIs_EditKeepsACustomLogo(t *testing.T) {
	m := webAPIService(t)
	m.ResolveLogo = func(context.Context, string) string { return "data:image/png;base64,GUESS" }
	in := sampleInput()
	chosen := pngLogo()
	in.Logo = &chosen
	saved, err := m.Save(context.Background(), "acme", "alice", in)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	relabel := sampleInput()
	relabel.Name = saved.Name
	relabel.Label = "Orders"
	after, err := m.Save(context.Background(), "acme", "alice", relabel)
	if err != nil {
		t.Fatalf("re-Save: %v", err)
	}
	if after.Logo != chosen || after.LogoMode != WebAPILogoCustom {
		t.Errorf("logo/mode = %.20q…/%q, want the upload kept", after.Logo, after.LogoMode)
	}
}

// An admin is a likelier source of a bad image than a favicon host is, and the
// refusal has to say what was wrong rather than fall back to the globe.
func TestWebAPIs_RefusesABadCustomLogo(t *testing.T) {
	cases := map[string]string{
		"a link rather than the image": "https://example.com/logo.png",
		"not an image at all":          "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("nope")),
		"a type we cannot show":        "data:application/pdf;base64,JVBERi0=",
	}
	for name, logo := range cases {
		t.Run(name, func(t *testing.T) {
			m := webAPIService(t)
			m.ResolveLogo = func(context.Context, string) string { return "" }
			in := sampleInput()
			in.Logo = &logo
			_, err := m.Save(context.Background(), "acme", "alice", in)
			if err == nil {
				t.Fatal("Save accepted it")
			}
			if !strings.Contains(err.Error(), "icon") {
				t.Errorf("error = %v, want it to name the field", err)
			}
			if _, ok := m.Catalog.Get("acme", "api:order-service:get_order"); ok {
				t.Error("the catalog registered despite the refusal")
			}
		})
	}
}

// Choosing "uploaded" with nothing uploaded is a mistake worth a message, not a
// silently empty mark.
func TestWebAPIs_RefusesCustomWithNoImage(t *testing.T) {
	m := webAPIService(t)
	in := sampleInput()
	in.LogoMode = logoMode(WebAPILogoCustom)
	if _, err := m.Save(context.Background(), "acme", "alice", in); err == nil {
		t.Fatal("Save accepted a custom icon with no image")
	}
}

func TestWebAPIs_RefusesAnUnknownLogoMode(t *testing.T) {
	m := webAPIService(t)
	in := sampleInput()
	in.LogoMode = logoMode("favicon-maybe")
	_, err := m.Save(context.Background(), "acme", "alice", in)
	if err == nil || !strings.Contains(err.Error(), "auto") {
		t.Fatalf("error = %v, want a refusal naming the three sources", err)
	}
}
