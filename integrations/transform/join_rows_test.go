package transform

import (
	"sort"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// runJoin is a thin shim — every test builds a job, calls executeJoinRows,
// then asserts on output. Centralising avoids per-test boilerplate while
// keeping each test's intent in plain view.
func runJoin(t *testing.T, params map[string]any, leftRows, rightRows []map[string]any, leftHeaders, rightHeaders []string) core.Result {
	t.Helper()
	in := map[string]core.Ref{
		"left_rows":  {Inline: leftRows},
		"right_rows": {Inline: rightRows},
	}
	if leftHeaders != nil {
		in["left_headers"] = core.Ref{Inline: leftHeaders}
	}
	if rightHeaders != nil {
		in["right_headers"] = core.Ref{Inline: rightHeaders}
	}
	res, err := executeJoinRows(t.Context(), core.Job{ID: "j", Params: params, Input: in}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return res
}

// outRows pulls the rows output off a result with type assertion. Lets
// individual tests do `len(outRows(res))` without re-parsing.
func outRows(t *testing.T, res core.Result) []map[string]any {
	t.Helper()
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	rows, ok := res.Output["rows"].Inline.([]map[string]any)
	if !ok {
		t.Fatalf("rows output not []map: %T", res.Output["rows"].Inline)
	}
	return rows
}

func outHeaders(t *testing.T, res core.Result) []string {
	t.Helper()
	h, ok := res.Output["headers"].Inline.([]string)
	if !ok {
		t.Fatalf("headers output not []string: %T", res.Output["headers"].Inline)
	}
	return h
}

// findRow returns the first row matching predicate, or nil. Used to
// assert "the joined row for user 2 has these values" without baking
// in slice ordering between left- and outer-join branches.
func findRow(rows []map[string]any, key string, value any) map[string]any {
	for _, r := range rows {
		if r[key] == value {
			return r
		}
	}
	return nil
}

// ---- Inner ----------------------------------------------------------

func TestJoinRows_InnerHappyPath(t *testing.T) {
	left := []map[string]any{
		{"id": 1, "name": "alice"},
		{"id": 2, "name": "bob"},
		{"id": 3, "name": "carol"},
	}
	right := []map[string]any{
		{"user_id": 1, "country": "SE"},
		{"user_id": 3, "country": "NO"},
	}
	res := runJoin(t,
		map[string]any{"on": map[string]any{"id": "user_id"}},
		left, right, nil, nil,
	)
	rows := outRows(t, res)
	if len(rows) != 2 {
		t.Fatalf("len(rows)=%d want 2; rows=%+v", len(rows), rows)
	}
	if got := findRow(rows, "name", "alice"); got == nil || got["country"] != "SE" {
		t.Errorf("alice's join: %+v", got)
	}
	if got := findRow(rows, "name", "carol"); got == nil || got["country"] != "NO" {
		t.Errorf("carol's join: %+v", got)
	}
	// Right's join-key column user_id must NOT appear in the output —
	// the left's `id` already carries the value.
	for _, r := range rows {
		if _, ok := r["user_id"]; ok {
			t.Errorf("right key column leaked into output: %+v", r)
		}
	}
}

func TestJoinRows_KindCoercionAcrossTypes(t *testing.T) {
	// Excel and JSON inputs often differ on numeric vs string —
	// fmt.Sprint coercion lets a row with id=30 (int) join a row
	// with user_id="30" (string) without forcing a pre-cast.
	left := []map[string]any{{"id": 30, "name": "x"}}
	right := []map[string]any{{"user_id": "30", "country": "SE"}}
	res := runJoin(t,
		map[string]any{"on": map[string]any{"id": "user_id"}},
		left, right, nil, nil,
	)
	rows := outRows(t, res)
	if len(rows) != 1 {
		t.Fatalf("expected match across int/string; rows=%+v", rows)
	}
	if rows[0]["country"] != "SE" {
		t.Errorf("country=%v", rows[0]["country"])
	}
}

// ---- Left / right / outer ------------------------------------------

func TestJoinRows_LeftEmitsUnmatchedLeftsWithNilRightCols(t *testing.T) {
	left := []map[string]any{
		{"id": 1, "name": "alice"},
		{"id": 2, "name": "bob"},
	}
	right := []map[string]any{
		{"user_id": 1, "country": "SE"},
	}
	res := runJoin(t,
		map[string]any{"on": map[string]any{"id": "user_id"}, "kind": "left"},
		left, right, nil, nil,
	)
	rows := outRows(t, res)
	if len(rows) != 2 {
		t.Fatalf("len=%d want 2", len(rows))
	}
	bob := findRow(rows, "name", "bob")
	if bob == nil {
		t.Fatal("bob missing from left join")
	}
	if got, ok := bob["country"]; !ok || got != nil {
		t.Errorf("bob.country should be nil (present), got %v ok=%v", got, ok)
	}
}

func TestJoinRows_RightEmitsUnmatchedRightsWithReconstitutedKey(t *testing.T) {
	left := []map[string]any{{"id": 1, "name": "alice"}}
	right := []map[string]any{
		{"user_id": 1, "country": "SE"},
		{"user_id": 99, "country": "ZZ"},
	}
	res := runJoin(t,
		map[string]any{"on": map[string]any{"id": "user_id"}, "kind": "right"},
		left, right, nil, nil,
	)
	rows := outRows(t, res)
	if len(rows) != 2 {
		t.Fatalf("len=%d want 2", len(rows))
	}
	// Unmatched right row: left columns nil except the join key,
	// which must be reconstituted from the right's user_id.
	tail := rows[1]
	if tail["id"] != 99 {
		t.Errorf("unmatched right's `id` should be 99 (from user_id); got %v", tail["id"])
	}
	if tail["name"] != nil {
		t.Errorf("unmatched right's `name` should be nil; got %v", tail["name"])
	}
	if tail["country"] != "ZZ" {
		t.Errorf("country = %v", tail["country"])
	}
}

func TestJoinRows_OuterCoversBothUnmatchedSides(t *testing.T) {
	left := []map[string]any{
		{"id": 1, "name": "alice"},
		{"id": 2, "name": "bob"},
	}
	right := []map[string]any{
		{"user_id": 1, "country": "SE"},
		{"user_id": 99, "country": "ZZ"},
	}
	res := runJoin(t,
		map[string]any{"on": map[string]any{"id": "user_id"}, "kind": "outer"},
		left, right, nil, nil,
	)
	rows := outRows(t, res)
	// Expect: alice+SE, bob+nil, nil+ZZ — 3 rows.
	if len(rows) != 3 {
		t.Fatalf("len=%d want 3; rows=%+v", len(rows), rows)
	}
}

// ---- Multi-column key ----------------------------------------------

func TestJoinRows_MultiColumnKey(t *testing.T) {
	left := []map[string]any{
		{"a": 1, "b": "x", "v": "L1"},
		{"a": 1, "b": "y", "v": "L2"},
		{"a": 2, "b": "x", "v": "L3"},
	}
	right := []map[string]any{
		{"p": 1, "q": "x", "w": "R1"},
		{"p": 1, "q": "y", "w": "R2"},
		// (2, "y") has no left match.
		{"p": 2, "q": "y", "w": "R3"},
	}
	res := runJoin(t,
		map[string]any{"on": map[string]any{"a": "p", "b": "q"}},
		left, right, nil, nil,
	)
	rows := outRows(t, res)
	if len(rows) != 2 {
		t.Fatalf("inner join on (a,b)=(p,q): want 2, got %d (%+v)", len(rows), rows)
	}
}

// ---- Cartesian within key group ------------------------------------

func TestJoinRows_CartesianWhenRightHasDuplicateKey(t *testing.T) {
	// One left row, three right rows sharing the key → 3 output rows
	// (SQL inner-join behavior).
	left := []map[string]any{{"id": 1, "name": "alice"}}
	right := []map[string]any{
		{"user_id": 1, "role": "admin"},
		{"user_id": 1, "role": "editor"},
		{"user_id": 1, "role": "viewer"},
	}
	res := runJoin(t,
		map[string]any{"on": map[string]any{"id": "user_id"}},
		left, right, nil, nil,
	)
	rows := outRows(t, res)
	if len(rows) != 3 {
		t.Fatalf("expected cartesian within key group; len=%d", len(rows))
	}
	roles := []string{}
	for _, r := range rows {
		if s, ok := r["role"].(string); ok {
			roles = append(roles, s)
		}
	}
	sort.Strings(roles)
	if got := roles; len(got) != 3 || got[0] != "admin" || got[1] != "editor" || got[2] != "viewer" {
		t.Errorf("roles=%v", roles)
	}
}

// ---- Header collisions ---------------------------------------------

func TestJoinRows_CollidingColumnGetsRightSuffix(t *testing.T) {
	left := []map[string]any{{"id": 1, "country": "SE-left"}}
	right := []map[string]any{{"user_id": 1, "country": "SE-right"}}
	res := runJoin(t,
		map[string]any{"on": map[string]any{"id": "user_id"}},
		left, right, nil, nil,
	)
	rows := outRows(t, res)
	if rows[0]["country"] != "SE-left" {
		t.Errorf("left's country should win the unsuffixed slot; got %v", rows[0]["country"])
	}
	if rows[0]["country_right"] != "SE-right" {
		t.Errorf("right's country should be suffixed; got %v", rows[0]["country_right"])
	}
	headers := outHeaders(t, res)
	// Headers must contain both — order: left's verbatim, then right's
	// non-key cols (suffixed when colliding).
	sort.Strings(headers)
	want := []string{"country", "country_right", "id"}
	if len(headers) != len(want) || headers[0] != want[0] || headers[1] != want[1] || headers[2] != want[2] {
		t.Errorf("headers=%v want %v", headers, want)
	}
}

func TestJoinRows_CustomRightSuffix(t *testing.T) {
	left := []map[string]any{{"id": 1, "v": "L"}}
	right := []map[string]any{{"user_id": 1, "v": "R"}}
	res := runJoin(t,
		map[string]any{
			"on":           map[string]any{"id": "user_id"},
			"right_suffix": "_r",
		},
		left, right, nil, nil,
	)
	rows := outRows(t, res)
	if rows[0]["v_r"] != "R" {
		t.Errorf("custom suffix not applied; row=%+v", rows[0])
	}
}

func TestJoinRows_SharedKeyColumnNameDoesNotDuplicate(t *testing.T) {
	// When both sides name the key the same thing, the right copy
	// is dropped (same as differently-named keys — the right's key
	// column is redundant by construction).
	left := []map[string]any{{"id": 1, "name": "alice"}}
	right := []map[string]any{{"id": 1, "country": "SE"}}
	res := runJoin(t,
		map[string]any{"on": map[string]any{"id": "id"}},
		left, right, nil, nil,
	)
	rows := outRows(t, res)
	if got := rows[0]; got["id"] != 1 || got["country"] != "SE" || got["name"] != "alice" {
		t.Errorf("row=%+v", got)
	}
	headers := outHeaders(t, res)
	// id should appear ONCE, not twice or as id_right.
	count := 0
	for _, h := range headers {
		if h == "id" {
			count++
		}
		if h == "id_right" {
			t.Errorf("shared key column got suffixed when it shouldn't: %v", headers)
		}
	}
	if count != 1 {
		t.Errorf("id should appear once in headers, got %d (%v)", count, headers)
	}
}

// ---- Empty sides ---------------------------------------------------

func TestJoinRows_EmptyLeft(t *testing.T) {
	right := []map[string]any{{"user_id": 1, "country": "SE"}}
	// Inner with empty left → empty output, no error.
	res := runJoin(t,
		map[string]any{"on": map[string]any{"id": "user_id"}},
		nil, right, []string{"id", "name"}, nil,
	)
	rows := outRows(t, res)
	if len(rows) != 0 {
		t.Errorf("inner with empty left: want 0, got %d", len(rows))
	}
	// Outer with empty left → all right rows surface as unmatched.
	res = runJoin(t,
		map[string]any{"on": map[string]any{"id": "user_id"}, "kind": "outer"},
		nil, right, []string{"id", "name"}, nil,
	)
	rows = outRows(t, res)
	if len(rows) != 1 {
		t.Errorf("outer with empty left: want 1 (right row), got %d", len(rows))
	}
}

func TestJoinRows_EmptyRight(t *testing.T) {
	left := []map[string]any{{"id": 1, "name": "alice"}}
	// Inner with empty right → empty output.
	res := runJoin(t,
		map[string]any{"on": map[string]any{"id": "user_id"}},
		left, nil, nil, []string{"user_id", "country"},
	)
	if got := len(outRows(t, res)); got != 0 {
		t.Errorf("inner with empty right: want 0, got %d", got)
	}
	// Left with empty right → all left rows pass through.
	res = runJoin(t,
		map[string]any{"on": map[string]any{"id": "user_id"}, "kind": "left"},
		left, nil, nil, []string{"user_id", "country"},
	)
	rows := outRows(t, res)
	if len(rows) != 1 {
		t.Fatalf("len=%d want 1", len(rows))
	}
	if rows[0]["name"] != "alice" {
		t.Errorf("left row didn't pass through: %+v", rows[0])
	}
}

// ---- Error paths ---------------------------------------------------

func TestJoinRows_MissingOnParamFails(t *testing.T) {
	left := []map[string]any{{"id": 1}}
	right := []map[string]any{{"user_id": 1}}
	res := runJoin(t, map[string]any{}, left, right, nil, nil)
	if res.Status != core.StatusError {
		t.Fatalf("expected error")
	}
	if res.Error.Code != "bad_param" {
		t.Errorf("code=%q", res.Error.Code)
	}
}

func TestJoinRows_EmptyOnMapFails(t *testing.T) {
	left := []map[string]any{{"id": 1}}
	right := []map[string]any{{"user_id": 1}}
	res := runJoin(t,
		map[string]any{"on": map[string]any{}},
		left, right, nil, nil,
	)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("expected bad_param, got %+v", res)
	}
}

func TestJoinRows_BadKindFails(t *testing.T) {
	res := runJoin(t,
		map[string]any{"on": map[string]any{"id": "user_id"}, "kind": "diagonal"},
		[]map[string]any{{"id": 1}}, []map[string]any{{"user_id": 1}}, nil, nil,
	)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("expected bad_param, got %+v", res)
	}
}

func TestJoinRows_MissingKeyColumnOnLeftFails(t *testing.T) {
	left := []map[string]any{{"id": 1, "name": "alice"}}
	right := []map[string]any{{"user_id": 1}}
	res := runJoin(t,
		map[string]any{"on": map[string]any{"missing": "user_id"}},
		left, right, nil, nil,
	)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("expected bad_input, got %+v", res)
	}
}

func TestJoinRows_MissingKeyColumnOnRightFails(t *testing.T) {
	left := []map[string]any{{"id": 1}}
	right := []map[string]any{{"user_id": 1}}
	res := runJoin(t,
		map[string]any{"on": map[string]any{"id": "missing"}},
		left, right, nil, nil,
	)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("expected bad_input, got %+v", res)
	}
}

// ---- Output port contract pinning ----------------------------------

func TestJoinRows_RowsAndHeadersBothEmitted(t *testing.T) {
	res := runJoin(t,
		map[string]any{"on": map[string]any{"id": "user_id"}},
		[]map[string]any{{"id": 1, "name": "x"}},
		[]map[string]any{{"user_id": 1, "country": "SE"}},
		nil, nil,
	)
	if _, ok := res.Output["rows"]; !ok {
		t.Error("rows port missing")
	}
	if _, ok := res.Output["headers"]; !ok {
		t.Error("headers port missing")
	}
}
