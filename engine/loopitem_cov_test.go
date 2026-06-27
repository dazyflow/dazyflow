package engine

import (
	"context"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func TestTraverseItemPath_Cov(t *testing.T) {
	root := map[string]any{
		"name": "Ada",
		"tags": []any{"x", "y"},
		"meta": map[string]any{"age": 42},
	}
	tests := []struct {
		name    string
		path    string
		want    any
		wantErr bool
	}{
		{name: "empty returns whole", path: "", want: root},
		{name: "map key", path: "name", want: "Ada"},
		{name: "nested map", path: "meta.age", want: 42},
		{name: "slice index", path: "tags.1", want: "y"},
		{name: "missing key", path: "nope", wantErr: true},
		{name: "bad index not number", path: "tags.x", wantErr: true},
		{name: "index out of range", path: "tags.9", wantErr: true},
		{name: "traverse scalar", path: "name.x", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := traverseItemPath(root, tt.path)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			switch want := tt.want.(type) {
			case map[string]any:
				if _, ok := got.(map[string]any); !ok {
					t.Fatalf("got %T, want map", got)
				}
			default:
				if got != want {
					t.Errorf("got %v, want %v", got, want)
				}
			}
		})
	}
}

func TestStringifyItemValue_Cov(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{name: "string", in: "hi", want: "hi"},
		{name: "nil", in: nil, want: ""},
		{name: "bool true", in: true, want: "true"},
		{name: "bool false", in: false, want: "false"},
		{name: "float", in: float64(3.5), want: "3.5"},
		{name: "int", in: 7, want: "7"},
		{name: "int64", in: int64(9), want: "9"},
		{name: "map json", in: map[string]any{"a": 1}, want: `{"a":1}`},
		{name: "slice json", in: []any{1, 2}, want: `[1,2]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stringifyItemValue(tt.in); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestItemSubstituter_Cov(t *testing.T) {
	// No item on ctx: ${item.…} is an unknown scheme, ok=false.
	sub := itemSubstituter(context.Background())
	if _, ok, _ := sub(context.Background(), "item", "name"); ok {
		t.Error("no item on ctx should report ok=false")
	}

	ctx := WithLoopItem(context.Background(), map[string]any{"name": "Bo", "n": 3})
	sub = itemSubstituter(ctx)

	if v, ok, err := sub(ctx, "item", "name"); err != nil || !ok || v != "Bo" {
		t.Errorf("item.name = %q ok=%v err=%v", v, ok, err)
	}
	if v, ok, err := sub(ctx, "item", "n"); err != nil || !ok || v != "3" {
		t.Errorf("item.n = %q ok=%v err=%v", v, ok, err)
	}
	// Non-item scheme falls through.
	if _, ok, _ := sub(ctx, "secret", "x"); ok {
		t.Error("non-item scheme should report ok=false")
	}
	// Bad path resolves but errors (ok=true, err!=nil).
	if _, ok, err := sub(ctx, "item", "missing"); !ok || err == nil {
		t.Errorf("missing path want ok=true err!=nil, got ok=%v err=%v", ok, err)
	}
}

func TestWithLoopRunID_Cov(t *testing.T) {
	// Empty run ID is a no-op: same context back, nothing stored.
	base := context.Background()
	if WithLoopRunID(base, "") != base {
		t.Error("empty run ID should return ctx unchanged")
	}
	if got := loopRunIDFromContext(base); got != "" {
		t.Errorf("absent run ID = %q, want empty", got)
	}
	ctx := WithLoopRunID(base, "run-7")
	if got := loopRunIDFromContext(ctx); got != "run-7" {
		t.Errorf("run ID = %q, want run-7", got)
	}
}

func TestWithBodyRunner_Cov(t *testing.T) {
	base := context.Background()
	// Nil runner is a no-op.
	if WithBodyRunner(base, nil) != base {
		t.Error("nil runner should return ctx unchanged")
	}
	if _, ok := BodyRunnerFromContext(base); ok {
		t.Error("absent runner should report ok=false")
	}

	called := false
	runner := func(_ context.Context, _ core.Ref) (GraphResult, error) {
		called = true
		return GraphResult{}, nil
	}
	ctx := WithBodyRunner(base, runner)
	got, ok := BodyRunnerFromContext(ctx)
	if !ok {
		t.Fatal("runner should be present")
	}
	if _, err := got(ctx, core.Ref{}); err != nil {
		t.Fatalf("runner: %v", err)
	}
	if !called {
		t.Error("runner closure was not invoked")
	}
}
