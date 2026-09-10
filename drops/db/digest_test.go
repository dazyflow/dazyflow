// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	_ "modernc.org/sqlite"
)

func addToDigest(t *testing.T, root, name string, in any) core.Result {
	t.Helper()
	res, err := executeDigestAdd(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"digest": name},
		Input:         map[string]core.Ref{"rows": {Inline: in}},
	}, nil)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("add status=%q err=%+v", res.Status, res.Error)
	}
	return res
}

func takeDigest(t *testing.T, root, name string) core.Result {
	t.Helper()
	res, err := executeDigestTake(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"digest": name},
	}, nil)
	if err != nil {
		t.Fatalf("take: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("take status=%q err=%+v", res.Status, res.Error)
	}
	return res
}

// The whole point of the pair: entries pile up across runs, and one take
// hands over everything and leaves the pile empty.
func TestDigest_PilesUpThenHandsOverOnce(t *testing.T) {
	root := t.TempDir()

	addToDigest(t, root, "leads", map[string]any{"name": "Alice"})
	addToDigest(t, root, "leads", map[string]any{"name": "Bob"})
	added := addToDigest(t, root, "leads", []map[string]any{{"name": "Cleo"}, {"name": "Dev"}})
	if got, _ := added.Output["added"].Inline.(int); got != 2 {
		t.Errorf("added = %v, want 2 for a two-row entry", added.Output["added"].Inline)
	}

	taken := takeDigest(t, root, "leads")
	rows, ok := taken.Output["rows"].Inline.([]map[string]any)
	if !ok {
		t.Fatalf("rows is %T, want a row list", taken.Output["rows"].Inline)
	}
	if len(rows) != 4 {
		t.Fatalf("took %d entries, want 4", len(rows))
	}
	if got, _ := taken.Output["count"].Inline.(int); got != 4 {
		t.Errorf("count = %v, want 4", taken.Output["count"].Inline)
	}
	if _, fired := taken.Output["empty"]; fired {
		t.Error("'Nothing there' fired on a digest that had entries")
	}
	// Every entry carries the stamp the store writes, which is what lets a
	// roundup say when things arrived.
	if _, stamped := rows[0]["saved_at"]; !stamped {
		t.Errorf("entry has no saved_at: %v", rows[0])
	}

	// Taken means gone: the second take finds nothing.
	again := takeDigest(t, root, "leads")
	if _, fired := again.Output["empty"]; !fired {
		t.Fatalf("a drained digest did not fire 'Nothing there': %v", again.Output)
	}
	if _, present := again.Output["rows"]; present {
		t.Error("an empty digest still fired Entries — a quiet night would send an empty email")
	}
	if got, _ := again.Output["count"].Inline.(int); got != 0 {
		t.Errorf("count = %v, want 0", again.Output["count"].Inline)
	}
}

// A digest nothing has ever been added to is a quiet night, not a broken flow.
func TestDigest_TakingOneThatNeverExistedIsQuiet(t *testing.T) {
	res := takeDigest(t, t.TempDir(), "never_used")
	if _, fired := res.Output["empty"]; !fired {
		t.Errorf("output = %v, want 'Nothing there'", res.Output)
	}
}

// "Remember this line" is half of what a digest is for, and a bare string is
// not a row until something wraps it.
func TestDigest_TakesABareValue(t *testing.T) {
	root := t.TempDir()
	addToDigest(t, root, "errors", "disk nearly full")

	rows := takeDigest(t, root, "errors").Output["rows"].Inline.([]map[string]any)
	if len(rows) != 1 || rows[0]["value"] != "disk nearly full" {
		t.Errorf("rows = %v, want one entry with a value column", rows)
	}
}

// Digests live under a prefix, so naming one after a collection you keep
// cannot empty that collection.
func TestDigest_CannotEmptyACollectionOfTheSameName(t *testing.T) {
	root := t.TempDir()

	if _, err := executeBuiltinStoreAppend(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"table": "leads"},
		Input:         map[string]core.Ref{"rows": {Inline: []map[string]any{{"name": "Keep me"}}}},
	}, nil); err != nil {
		t.Fatalf("seed collection: %v", err)
	}
	addToDigest(t, root, "leads", map[string]any{"name": "Digest entry"})
	takeDigest(t, root, "leads")

	res, err := executeBuiltinStoreFind(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"table": "leads"},
	}, nil)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	rows, _ := res.Output["rows"].Inline.([]map[string]any)
	if len(rows) != 1 || rows[0]["name"] != "Keep me" {
		t.Errorf("collection = %v, want the row it started with", rows)
	}
}

// The store's safety is the quoting, not a character allowlist, so an
// injection attempt has to survive as a name rather than be refused.
func TestDigest_NameCannotEscapeIntoSQL(t *testing.T) {
	root := t.TempDir()
	if _, err := executeBuiltinStoreAppend(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"table": "leads"},
		Input:         map[string]core.Ref{"rows": {Inline: []map[string]any{{"name": "Keep me"}}}},
	}, nil); err != nil {
		t.Fatalf("seed collection: %v", err)
	}

	hostile := `x"; DROP TABLE leads; --`
	addToDigest(t, root, hostile, map[string]any{"note": "still just a name"})
	rows, _ := takeDigest(t, root, hostile).Output["rows"].Inline.([]map[string]any)
	if len(rows) != 1 || rows[0]["note"] != "still just a name" {
		t.Errorf("digest = %v, want the one entry", rows)
	}

	res, err := executeBuiltinStoreFind(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"table": "leads"},
	}, nil)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("the leads collection is gone: %+v", res.Error)
	}
}

func TestDigest_RejectsAnEmptyName(t *testing.T) {
	res, err := executeDigestAdd(t.Context(), core.Job{
		WorkspaceRoot: t.TempDir(),
		Params:        map[string]any{"digest": "   "},
		Input:         map[string]core.Ref{"rows": {Inline: map[string]any{"a": 1}}},
	}, nil)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Fatalf("status=%q err=%+v, want bad_param", res.Status, res.Error)
	}
	if !strings.Contains(res.Error.Message, "digest name") {
		t.Errorf("message = %q", res.Error.Message)
	}
}
