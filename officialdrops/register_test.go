package officialdrops

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"git.sr.ht/~klahr/hazyflow/engine/containerdrop"
	"git.sr.ht/~klahr/hazyflow/engine/jsdrop"
)

// Registering transpiles every embedded .ts and validates its embedded manifest
// — the only place a syntax or manifest error in an official drop is caught,
// since .ts isn't compiled by the Go build.
func TestRegister_AllOfficialDropsCompile(t *testing.T) {
	cat := jsdrop.NewCatalog()
	if err := Register(cat); err != nil {
		t.Fatalf("register official drops: %v", err)
	}
	mans := cat.Manifests()
	want := []string{
		"gmail_send_email",
		"gmail_get_message",
		"gmail_search_messages",
		"slack_send_message",
		"slack_list_channels",
		"claude",
		"sheets_read_range",
		"sheets_append_row",
		"sheets_export_pdf",
		"ntfy",
		"webhook_send",
		"github_add_comment",
		"github_create_issue",
		"github_list_issues",
		"notion_create_page",
		"notion_query_database",
		"excel_read",
		"excel_write",
	}
	for _, id := range want {
		if _, ok := mans[id]; !ok {
			t.Errorf("official drop %q not registered (compile or manifest error?)", id)
		}
	}
}

// The embedded manifests.json must match what the Node host actually emits for
// each drop's source — otherwise a dev edits a manifest, forgets
// `go generate ./officialdrops`, and the daemon gates/displays a stale manifest
// while a different one runs. Register only checks source/manifest COUNT parity;
// this catches field-level drift. Skips without node.
func TestManifestsJSON_NotStale(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	drophost, err := filepath.Abs("../engine/containerdrop/nodehost/drophost.mjs")
	if err != nil {
		t.Fatal(err)
	}
	extract := containerdrop.NodeManifestExtractor(node, drophost)

	raw, err := fsys.ReadFile("manifests.json")
	if err != nil {
		t.Fatal(err)
	}
	var embedded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &embedded); err != nil {
		t.Fatalf("decode manifests.json: %v", err)
	}

	entries, err := fsys.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".ts" {
			continue
		}
		mraw, ok := embedded[name]
		if !ok {
			t.Errorf("%s: no embedded manifest — run `go generate ./officialdrops`", name)
			continue
		}
		want, err := jsdrop.ParseManifest(mraw)
		if err != nil {
			t.Errorf("%s: embedded manifest invalid: %v", name, err)
			continue
		}
		src, err := fsys.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		got, err := extract(name, string(src))
		if err != nil {
			t.Errorf("%s: live extract failed: %v", name, err)
			continue
		}
		if !reflect.DeepEqual(want, got) {
			t.Errorf("%s: embedded manifest is STALE vs the drop source — run `go generate ./officialdrops`", name)
		}
	}
}
