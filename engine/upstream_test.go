package engine

import (
	"context"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// ----------------------------------------------------------------------
// Unit tests for the upstream-path walker + substituter directly.
// Kept separate from the full resolveTemplates path so failures
// point at the specific component (path parsing vs job-wide walk).
// ----------------------------------------------------------------------

func TestUpstream_PortRoot(t *testing.T) {
	prior := map[string]core.Result{
		"loader": {Output: map[string]core.Ref{
			"meta": {Inline: map[string]any{"status": "ok", "count": int64(3)}},
		}},
	}
	got, err := resolveUpstreamPath(prior, "loader.meta")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	m, ok := got.(map[string]any)
	if !ok || m["status"] != "ok" {
		t.Errorf("got %+v, want the meta map", got)
	}
}

func TestUpstream_NestedField(t *testing.T) {
	prior := map[string]core.Result{
		"loader": {Output: map[string]core.Ref{
			"meta": {Inline: map[string]any{"status": "ok", "count": int64(42)}},
		}},
	}
	got, _ := resolveUpstreamPath(prior, "loader.meta.status")
	if got != "ok" {
		t.Errorf("got %v, want 'ok'", got)
	}
	got, _ = resolveUpstreamPath(prior, "loader.meta.count")
	if got != int64(42) {
		t.Errorf("got %v, want int64(42)", got)
	}
}

func TestUpstream_ArrayIndex(t *testing.T) {
	prior := map[string]core.Result{
		"reader": {Output: map[string]core.Ref{
			"headers": {Inline: []string{"id", "name", "age"}},
		}},
	}
	got, _ := resolveUpstreamPath(prior, "reader.headers[0]")
	if got != "id" {
		t.Errorf("got %v, want 'id'", got)
	}
	got, _ = resolveUpstreamPath(prior, "reader.headers[2]")
	if got != "age" {
		t.Errorf("got %v, want 'age'", got)
	}
}

func TestUpstream_ArrayThenField(t *testing.T) {
	// Mixed: index into a list of objects, then read a field.
	prior := map[string]core.Result{
		"q": {Output: map[string]core.Ref{
			"rows": {Inline: []map[string]any{
				{"id": int64(1), "name": "Alice"},
				{"id": int64(2), "name": "Bob"},
			}},
		}},
	}
	got, _ := resolveUpstreamPath(prior, "q.rows[1].name")
	if got != "Bob" {
		t.Errorf("got %v, want 'Bob'", got)
	}
}

func TestUpstream_PortWithBracketImmediately(t *testing.T) {
	// rows[0] — index applied directly to the port value.
	prior := map[string]core.Result{
		"q": {Output: map[string]core.Ref{
			"rows": {Inline: []any{"first", "second"}},
		}},
	}
	got, _ := resolveUpstreamPath(prior, "q.rows[0]")
	if got != "first" {
		t.Errorf("got %v, want 'first'", got)
	}
}

func TestUpstream_ErrorCases(t *testing.T) {
	prior := map[string]core.Result{
		"loader": {Output: map[string]core.Ref{
			"meta": {Inline: map[string]any{"s": "ok"}},
			"rows": {Inline: []any{"a"}},
		}},
	}
	cases := []struct {
		name, path, contains string
	}{
		{"empty", "", "empty path"},
		{"no port", "loader", "must include a port"},
		{"unknown node", "ghost.meta", "no result recorded"},
		{"unknown port", "loader.ghost", "no output port"},
		{"index off end", "loader.rows[5]", "out of range"},
		{"index into non-array", "loader.meta[0]", "expected array"},
		{"field into non-object", "loader.rows.name", "expected object"},
		{"bad index", "loader.rows[x]", "bad index"},
		{"unclosed bracket", "loader.rows[0", "unclosed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := resolveUpstreamPath(prior, c.path)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.contains)
			}
			if !contains(err.Error(), c.contains) {
				t.Errorf("err = %q, want substring %q", err.Error(), c.contains)
			}
		})
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ----------------------------------------------------------------------
// stringifyForTemplate — value → string conversion for substitution.
// ----------------------------------------------------------------------

func TestStringifyForTemplate(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"string", "hello", "hello"},
		{"empty string", "", ""},
		{"nil", nil, ""},
		{"int", int64(42), "42"},
		{"float", 3.14, "3.14"},
		{"bool true", true, "true"},
		{"bool false", false, "false"},
		{"bytes", []byte("raw"), "raw"},
		{"map → json", map[string]any{"k": "v"}, `{"k":"v"}`},
		{"slice → json", []any{1, 2, 3}, `[1,2,3]`},
		{"string slice → json", []string{"a", "b"}, `["a","b"]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := stringifyForTemplate(c.in)
			if got != c.want {
				t.Errorf("stringifyForTemplate(%v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// ----------------------------------------------------------------------
// End-to-end: resolveTemplates applies upstream substitution to
// job.Params just like it does for secrets. These are the contract
// tests for the user-facing feature.
// ----------------------------------------------------------------------

func TestResolveTemplates_UpstreamInlineSubstitution(t *testing.T) {
	prior := map[string]core.Result{
		"reader": {Output: map[string]core.Ref{
			"meta": {Inline: map[string]any{"count": int64(245), "source": "sales.xlsx"}},
		}},
	}
	job := &core.Job{
		Params: map[string]any{
			"message": "Loaded ${upstream:reader.meta.count} rows from ${upstream:reader.meta.source}",
		},
	}
	if err := resolveTemplates(t.Context(), nil, prior, job); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got := job.Params["message"].(string)
	want := "Loaded 245 rows from sales.xlsx"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveTemplates_UpstreamInNestedParams(t *testing.T) {
	// The walker recurses into nested maps/slices — confirm
	// upstream refs deep inside still resolve.
	prior := map[string]core.Result{
		"reader": {Output: map[string]core.Ref{
			"headers": {Inline: []string{"id", "name"}},
		}},
	}
	job := &core.Job{
		Params: map[string]any{
			"config": map[string]any{
				"columns": []any{
					"${upstream:reader.headers[0]}",
					"${upstream:reader.headers[1]}",
				},
			},
		},
	}
	if err := resolveTemplates(t.Context(), nil, prior, job); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	cfg := job.Params["config"].(map[string]any)
	cols := cfg["columns"].([]any)
	if cols[0] != "id" || cols[1] != "name" {
		t.Errorf("got %v, want [id name]", cols)
	}
}

func TestResolveTemplates_UpstreamMixedWithSecrets(t *testing.T) {
	// A param can use both schemes in one string. The chain runs
	// upstream first, then secrets — both should resolve in a single
	// pass.
	prior := map[string]core.Result{
		"q": {Output: map[string]core.Ref{
			"meta": {Inline: map[string]any{"id": "run-42"}},
		}},
	}
	providers := map[string]core.SecretProvider{
		"env": stubSecretProvider{vals: map[string]string{"WEBHOOK_TOKEN": "secret-xyz"}},
	}
	job := &core.Job{
		Params: map[string]any{
			"url": "https://hooks.example.com/${upstream:q.meta.id}?token=${env:WEBHOOK_TOKEN}",
		},
	}
	if err := resolveTemplates(t.Context(), providers, prior, job); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := "https://hooks.example.com/run-42?token=secret-xyz"
	if got := job.Params["url"].(string); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveTemplates_UpstreamRefMissingBecomesError(t *testing.T) {
	// Referencing a node that doesn't exist in prior must fail — we
	// don't want a typo'd nodeID to silently produce an empty value
	// that lands somewhere problematic (a DSN, a destination path).
	job := &core.Job{
		Params: map[string]any{"x": "${upstream:typo.field}"},
	}
	err := resolveTemplates(t.Context(), nil, map[string]core.Result{}, job)
	if err == nil {
		t.Fatal("expected an error for unknown upstream node")
	}
	if !contains(err.Error(), "typo") {
		t.Errorf("error doesn't name the missing node: %v", err)
	}
}

func TestResolveTemplates_NoPriorMeansUnknownScheme(t *testing.T) {
	// With prior=nil the upstream substituter reports "not my scheme"
	// — the placeholder survives the resolution pass intact. This
	// matters because secret resolution runs on jobs that don't
	// always have prior data (single-job submissions, tests).
	job := &core.Job{
		Params: map[string]any{"x": "${upstream:loader.meta.id}"},
	}
	if err := resolveTemplates(t.Context(), nil, nil, job); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := job.Params["x"].(string); got != "${upstream:loader.meta.id}" {
		t.Errorf("got %q, want placeholder preserved", got)
	}
}

func TestResolveTemplates_UpstreamInComplexJSON(t *testing.T) {
	// A common ETL pattern: webhook_send body templated with
	// upstream metadata.
	prior := map[string]core.Result{
		"loader": {Output: map[string]core.Ref{
			"meta": {Inline: map[string]any{"processed": int64(245), "table": "customers"}},
		}},
	}
	job := &core.Job{
		Params: map[string]any{
			"body": map[string]any{
				"text":    "Loaded ${upstream:loader.meta.processed} rows into ${upstream:loader.meta.table}",
				"channel": "#data-ops",
			},
		},
	}
	if err := resolveTemplates(t.Context(), nil, prior, job); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	body := job.Params["body"].(map[string]any)
	want := "Loaded 245 rows into customers"
	if got := body["text"].(string); got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
}

func TestResolveTemplates_UpstreamObjectStringifiesAsJSON(t *testing.T) {
	// Referencing a whole map should yield a JSON string — useful for
	// passing whole objects through to downstream drops that parse
	// JSON from a string param.
	prior := map[string]core.Result{
		"q": {Output: map[string]core.Ref{
			"meta": {Inline: map[string]any{"a": "1", "b": "2"}},
		}},
	}
	job := &core.Job{
		Params: map[string]any{"x": "${upstream:q.meta}"},
	}
	if err := resolveTemplates(t.Context(), nil, prior, job); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got := job.Params["x"].(string)
	// JSON object encoding is order-insensitive at the source level
	// but Go's encoder writes keys alphabetically — assert on that.
	want := `{"a":"1","b":"2"}`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// stubSecretProvider is a minimal SecretProvider for tests that mix
// upstream and secret resolution.
type stubSecretProvider struct {
	vals map[string]string
}

func (s stubSecretProvider) Scheme() string { return "env" }
func (s stubSecretProvider) Get(_ context.Context, key string) (string, error) {
	v, ok := s.vals[key]
	if !ok {
		return "", nil
	}
	return v, nil
}
