// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package notion

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

type notionServer struct {
	*httptest.Server
	lastPath string
	lastBody map[string]any
	lastVer  string
	status   int
	resp     any
}

func newNotionServer(t *testing.T, status int, resp any) *notionServer {
	t.Helper()
	s := &notionServer{status: status, resp: resp}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.lastPath = r.URL.Path
		s.lastVer = r.Header.Get("Notion-Version")
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &s.lastBody)
		}
		w.WriteHeader(s.status)
		_ = json.NewEncoder(w).Encode(s.resp)
	}))
	t.Cleanup(s.Close)
	return s
}

func withNotionEnv(t *testing.T, base string) {
	t.Helper()
	SetHTTPBase(base)
	SetTokenLookup(func(_ context.Context, account string) (string, error) { return "ntn-" + account, nil })
	t.Cleanup(func() {
		SetHTTPBase("https://api.notion.com/v1")
		SetTokenLookup(nil)
	})
}

func TestNotionQuery_FlattensRowsAndKeepsRawKeys(t *testing.T) {
	srv := newNotionServer(t, 200, map[string]any{
		"results": []any{
			map[string]any{
				"id":  "p1",
				"url": "https://notion/p1",
				"properties": map[string]any{
					"Name":   map[string]any{"type": "title", "title": []any{map[string]any{"plain_text": "Task A"}}},
					"Status": map[string]any{"type": "status", "status": map[string]any{"name": "Todo"}},
					"Points": map[string]any{"type": "number", "number": 3},
					"Tags":   map[string]any{"type": "multi_select", "multi_select": []any{map[string]any{"name": "red"}, map[string]any{"name": "blue"}}},
					"Due":    map[string]any{"type": "date", "date": map[string]any{"start": "2026-06-10"}},
				},
			},
			map[string]any{"id": "p2"},
		},
		"next_cursor": "cur123",
		"has_more":    true,
	})
	withNotionEnv(t, srv.URL)

	res, err := executeNotionQueryDatabase(context.Background(), core.Job{
		Params: map[string]any{"database_id": "db-uuid", "page_size": 25},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if srv.lastPath != "/databases/db-uuid/query" || srv.lastVer != notionVersion {
		t.Errorf("path=%q ver=%q", srv.lastPath, srv.lastVer)
	}

	rows := res.Output["rows"].Inline.([]any)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	row := rows[0].(map[string]any)
	want := map[string]any{
		"Name": "Task A", "Status": "Todo", "Points": float64(3),
		"Tags": "red, blue", "Due": "2026-06-10",
		"id": "p1", "url": "https://notion/p1",
	}
	for k, v := range want {
		if row[k] != v {
			t.Errorf("row[%q] = %v, want %v", k, row[k], v)
		}
	}

	// Raw page objects, pagination and the full list response stay EMITTED
	// (run records / API callers) even though they're no longer pins.
	if len(res.Output["pages"].Inline.([]any)) != 2 {
		t.Errorf("pages = %+v", res.Output["pages"].Inline)
	}
	if res.Output["next_cursor"].Inline != "cur123" || res.Output["has_more"].Inline != true {
		t.Errorf("cursor/has_more = %v / %v", res.Output["next_cursor"].Inline, res.Output["has_more"].Inline)
	}
	if _, ok := res.Output["meta"]; !ok {
		t.Error("meta key missing from output")
	}
}

func TestNotionQuery_DatabaseIDInputOverridesParam(t *testing.T) {
	srv := newNotionServer(t, 200, map[string]any{"results": []any{}})
	withNotionEnv(t, srv.URL)

	res, _ := executeNotionQueryDatabase(context.Background(), core.Job{
		Params: map[string]any{"database_id": "param-db"},
		Input:  map[string]core.Ref{"database_id": {Inline: "wired-db"}},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if srv.lastPath != "/databases/wired-db/query" {
		t.Errorf("path = %q, want the wired database", srv.lastPath)
	}
}

func TestNotionQuery_MissingDatabaseID(t *testing.T) {
	withNotionEnv(t, "http://unused")
	res, _ := executeNotionQueryDatabase(context.Background(), core.Job{Params: map[string]any{}}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestNotionQuery_APIError(t *testing.T) {
	srv := newNotionServer(t, 400, map[string]any{"code": "validation_error", "message": "bad filter"})
	withNotionEnv(t, srv.URL)
	res, _ := executeNotionQueryDatabase(context.Background(), core.Job{
		Params: map[string]any{"database_id": "db"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "notion_error" {
		t.Fatalf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestPropertyPlain_CommonTypes(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want any
	}{
		{"checkbox", map[string]any{"type": "checkbox", "checkbox": true}, true},
		{"url", map[string]any{"type": "url", "url": "https://x"}, "https://x"},
		{"select", map[string]any{"type": "select", "select": map[string]any{"name": "High"}}, "High"},
		{"empty select", map[string]any{"type": "select", "select": nil}, nil},
		{"date range", map[string]any{"type": "date", "date": map[string]any{"start": "a", "end": "b"}}, "a → b"},
		{"people", map[string]any{"type": "people", "people": []any{map[string]any{"name": "Ada"}}}, "Ada"},
		{"formula", map[string]any{"type": "formula", "formula": map[string]any{"type": "string", "string": "ok"}}, "ok"},
		{"rich_text", map[string]any{"type": "rich_text", "rich_text": []any{map[string]any{"plain_text": "hi"}}}, "hi"},
	}
	for _, c := range cases {
		if got := propertyPlain(c.in); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNotionCreatePage_TitleParamBuildsTitleProperty(t *testing.T) {
	srv := newNotionServer(t, 200, map[string]any{
		"id":  "new-id",
		"url": "https://notion/new-id",
		"properties": map[string]any{
			"Name": map[string]any{"type": "title", "title": []any{map[string]any{"plain_text": "Follow up with Ada"}}},
		},
	})
	withNotionEnv(t, srv.URL)

	res, err := executeNotionCreatePage(context.Background(), core.Job{
		Params: map[string]any{"parent_database_id": "db", "title": "Follow up with Ada"},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}

	// The friendly Title param becomes the title property under Notion's
	// fixed "title" property ID.
	props, _ := srv.lastBody["properties"].(map[string]any)
	tp, _ := props["title"].(map[string]any)
	rt, _ := tp["title"].([]any)
	if len(rt) != 1 {
		t.Fatalf("title property = %+v", props)
	}
	txt := rt[0].(map[string]any)["text"].(map[string]any)["content"]
	if txt != "Follow up with Ada" {
		t.Errorf("title content = %v", txt)
	}

	// Friendly pins + the full page object still emitted under meta.
	if res.Output["title"].Inline != "Follow up with Ada" {
		t.Errorf("title out = %v", res.Output["title"].Inline)
	}
	if res.Output["url"].Inline != "https://notion/new-id" || res.Output["id"].Inline != "new-id" {
		t.Errorf("url/id = %v / %v", res.Output["url"].Inline, res.Output["id"].Inline)
	}
	if _, ok := res.Output["meta"]; !ok {
		t.Error("meta key missing from output")
	}
}

func TestNotionCreatePage_TitleInputOverridesParam(t *testing.T) {
	srv := newNotionServer(t, 200, map[string]any{"id": "new-id"})
	withNotionEnv(t, srv.URL)

	res, _ := executeNotionCreatePage(context.Background(), core.Job{
		Params: map[string]any{"parent_database_id": "db", "title": "typed"},
		Input:  map[string]core.Ref{"title": {Inline: "wired"}},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	props, _ := srv.lastBody["properties"].(map[string]any)
	rt := props["title"].(map[string]any)["title"].([]any)
	if got := rt[0].(map[string]any)["text"].(map[string]any)["content"]; got != "wired" {
		t.Errorf("title content = %v, want the wired value", got)
	}
}

func TestNotionCreatePage_RawTitlePropertyWinsOverParam(t *testing.T) {
	srv := newNotionServer(t, 200, map[string]any{"id": "new-id"})
	withNotionEnv(t, srv.URL)

	res, _ := executeNotionCreatePage(context.Background(), core.Job{
		Params: map[string]any{
			"parent_database_id": "db",
			"title":              "ignored",
			"properties":         map[string]any{"Name": map[string]any{"title": []any{map[string]any{"text": map[string]any{"content": "raw"}}}}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	props, _ := srv.lastBody["properties"].(map[string]any)
	if _, injected := props["title"]; injected {
		t.Errorf("Title param injected alongside a raw title property: %+v", props)
	}
	if _, ok := props["Name"]; !ok {
		t.Errorf("raw title property dropped: %+v", props)
	}
}

func TestNotionCreatePage_MissingTitleAndProperties(t *testing.T) {
	withNotionEnv(t, "http://unused")
	res, _ := executeNotionCreatePage(context.Background(), core.Job{
		Params: map[string]any{"parent_database_id": "db"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestNotionCreatePage_TextContentBecomesParagraphs(t *testing.T) {
	srv := newNotionServer(t, 200, map[string]any{"id": "new-id", "url": "https://notion/new-id"})
	withNotionEnv(t, srv.URL)

	res, err := executeNotionCreatePage(context.Background(), core.Job{
		Params: map[string]any{
			"parent_database_id": "db",
			"properties":         map[string]any{"Name": map[string]any{"title": []any{}}},
		},
		Input: map[string]core.Ref{"content": {Inline: "Hello world\n\nSecond para"}},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if res.Output["id"].Inline != "new-id" {
		t.Errorf("id = %v", res.Output["id"].Inline)
	}
	children, _ := srv.lastBody["children"].([]any)
	if len(children) != 2 {
		t.Fatalf("expected 2 paragraph blocks, got %d (%+v)", len(children), srv.lastBody["children"])
	}
}

func TestNotionCreatePage_ContentParamBecomesParagraphs(t *testing.T) {
	srv := newNotionServer(t, 200, map[string]any{"id": "new-id"})
	withNotionEnv(t, srv.URL)

	res, _ := executeNotionCreatePage(context.Background(), core.Job{
		Params: map[string]any{
			"parent_database_id": "db",
			"title":              "Note",
			"content":            "First para\n\nSecond para",
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	children, _ := srv.lastBody["children"].([]any)
	if len(children) != 2 {
		t.Fatalf("expected 2 paragraph blocks, got %d (%+v)", len(children), srv.lastBody["children"])
	}
}

func TestNotionCreatePage_RequiresExactlyOneParent(t *testing.T) {
	withNotionEnv(t, "http://unused")
	res, _ := executeNotionCreatePage(context.Background(), core.Job{
		Params: map[string]any{"parent_database_id": "db", "parent_page_id": "pg", "properties": map[string]any{}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestRichTextChunks_SplitsAtLimit(t *testing.T) {
	long := make([]rune, richTextLimit+50)
	for i := range long {
		long[i] = 'x'
	}
	chunks := richTextChunks(string(long))
	if len(chunks) != 2 {
		t.Errorf("got %d chunks, want 2", len(chunks))
	}
}

// withNotionAuthErr points the token lookup at a failing resolver so the
// auth branches in both connectors can be exercised.
func withNotionAuthErr(t *testing.T) {
	t.Helper()
	SetTokenLookup(func(_ context.Context, _ string) (string, error) {
		return "", errors.New("no token for account")
	})
	t.Cleanup(func() { SetTokenLookup(nil) })
}

func TestCovNotionError_Variants(t *testing.T) {
	long := strings.Repeat("z", 600)
	cases := []struct {
		name   string
		status int
		body   []byte
		want   string
	}{
		{"code and message", 400, []byte(`{"code":"validation_error","message":"bad filter"}`), "Notion validation_error: bad filter"},
		{"message only", 400, []byte(`{"message":"plain message"}`), "plain message"},
		{"empty message falls through", 404, []byte(`{"code":"object_not_found","message":""}`), "Notion returned 404: " + `{"code":"object_not_found","message":""}`},
		{"invalid json falls through", 500, []byte(`not json`), "Notion returned 500: not json"},
		{"empty body falls through", 502, []byte(``), "Notion returned 502: "},
		{"long body truncated", 500, []byte(long), "Notion returned 500: " + long[:512]},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := notionError(c.status, c.body); got != c.want {
				t.Errorf("notionError = %q, want %q", got, c.want)
			}
		})
	}
}

func TestCovNotionDo_HTTPErrorAndDefaults(t *testing.T) {
	// Non-2xx is still returned as (status, body, nil) — the caller maps it.
	srv := newNotionServer(t, 418, map[string]any{"code": "teapot", "message": "no coffee"})
	withNotionEnv(t, srv.URL)

	// timeoutMS <= 0 takes the default-timeout branch; nil body skips the
	// Content-Type header.
	status, body, err := notionDo(context.Background(), "GET", srv.URL+"/anything", "tok", nil, 0)
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if status != 418 {
		t.Errorf("status = %d, want 418", status)
	}
	if !strings.Contains(string(body), "no coffee") {
		t.Errorf("body = %s", body)
	}
}

func TestCovContentBlocks_AllShapes(t *testing.T) {
	cases := []struct {
		name    string
		in      any
		wantLen int
	}{
		{"nil", nil, 0},
		{"array passthrough", []any{map[string]any{"object": "block"}, map[string]any{"object": "block"}}, 2},
		{"single object wrapped", map[string]any{"object": "block", "type": "paragraph"}, 1},
		{"string to paragraphs", "one\n\ntwo", 2},
		{"number stringified", 42, 1},
		{"bool stringified", true, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := contentBlocks(c.in)
			if len(got) != c.wantLen {
				t.Errorf("contentBlocks(%v) len = %d, want %d", c.in, len(got), c.wantLen)
			}
		})
	}
}

func TestCovStringify(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"number", 42, "42"},
		{"bool", true, "true"},
		{"string", "hi", `"hi"`},
		{"unmarshalable returns empty", make(chan int), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stringify(c.in); got != c.want {
				t.Errorf("stringify(%v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestCovParagraphsToBlocks_SkipsBlankAndEmpty(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantLen int
	}{
		{"all blank", "   \n\n  ", 0},
		{"empty string", "", 0},
		{"blank middle skipped", "a\n\n   \n\nb", 2},
		{"single para", "hello", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := paragraphsToBlocks(c.in); len(got) != c.wantLen {
				t.Errorf("paragraphsToBlocks(%q) len = %d, want %d", c.in, len(got), c.wantLen)
			}
		})
	}
}

func TestCovPageTitle_Variants(t *testing.T) {
	cases := []struct {
		name string
		page map[string]any
		want string
	}{
		{"no properties", map[string]any{}, ""},
		{"non-map property skipped", map[string]any{"properties": map[string]any{"x": "notamap"}}, ""},
		{"non-title type", map[string]any{"properties": map[string]any{"Status": map[string]any{"type": "status"}}}, ""},
		{
			"finds title",
			map[string]any{"properties": map[string]any{
				"Status": map[string]any{"type": "status"},
				"Name":   map[string]any{"type": "title", "title": []any{map[string]any{"plain_text": "Hello"}}},
			}},
			"Hello",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pageTitle(c.page); got != c.want {
				t.Errorf("pageTitle = %q, want %q", got, c.want)
			}
		})
	}
}

func TestCovPropertyPlain_AllTypes(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want any
	}{
		{"non-map passthrough", "scalar", "scalar"},
		{"number", map[string]any{"type": "number", "number": float64(7)}, float64(7)},
		{"created_by", map[string]any{"type": "created_by", "created_by": map[string]any{"name": "Ada"}}, "Ada"},
		{"last_edited_by", map[string]any{"type": "last_edited_by", "last_edited_by": map[string]any{"name": "Bob"}}, "Bob"},
		{"relation joins", map[string]any{"type": "relation", "relation": []any{map[string]any{"id": "r1"}, map[string]any{"id": "r2"}}}, "r1, r2"},
		{"rollup nested", map[string]any{"type": "rollup", "rollup": map[string]any{"type": "number", "number": float64(9)}}, float64(9)},
		{"empty type passthrough", map[string]any{"type": "", "foo": "bar"}, nil},
		{"status name", map[string]any{"type": "status", "status": map[string]any{"name": "Done"}}, "Done"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := propertyPlain(c.in)
			if c.name == "empty type passthrough" {
				// Returns the same map value; just assert it's a map.
				if _, ok := got.(map[string]any); !ok {
					t.Errorf("empty type: got %T, want map", got)
				}
				return
			}
			if got != c.want {
				t.Errorf("propertyPlain(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestCovOptionName_Variants(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want any
	}{
		{"name wins", map[string]any{"name": "High", "id": "x"}, "High"},
		{"id fallback when no name", map[string]any{"id": "opt-1"}, "opt-1"},
		{"empty name uses id", map[string]any{"name": "", "id": "opt-2"}, "opt-2"},
		{"non-map passthrough", "raw", "raw"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := optionName(c.in); got != c.want {
				t.Errorf("optionName(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
	t.Run("no name no id returns nil", func(t *testing.T) {
		if got := optionName(map[string]any{"other": "x"}); got != nil {
			t.Errorf("optionName = %v, want nil", got)
		}
	})
}

func TestCovJoinPlain_Variants(t *testing.T) {
	t.Run("non-list passthrough", func(t *testing.T) {
		if got := joinPlain("notalist"); got != "notalist" {
			t.Errorf("joinPlain = %v", got)
		}
	})
	t.Run("skips nameless entries", func(t *testing.T) {
		in := []any{
			map[string]any{"name": "A"},
			map[string]any{"other": "x"}, // optionName -> nil, skipped
			map[string]any{"name": "B"},
		}
		if got := joinPlain(in); got != "A, B" {
			t.Errorf("joinPlain = %v, want %q", got, "A, B")
		}
	})
	t.Run("empty list", func(t *testing.T) {
		if got := joinPlain([]any{}); got != "" {
			t.Errorf("joinPlain = %v, want empty", got)
		}
	})
}

func TestCovDatePlain_Variants(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want any
	}{
		{"non-map passthrough", "raw", "raw"},
		{"start only", map[string]any{"start": "2026-01-01"}, "2026-01-01"},
		{"range", map[string]any{"start": "2026-01-01", "end": "2026-01-02"}, "2026-01-01 → 2026-01-02"},
		{"empty", map[string]any{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := datePlain(c.in); got != c.want {
				t.Errorf("datePlain(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestCovRichTextPlain_Variants(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"non-array", "notarray", ""},
		{"plain_text path", []any{map[string]any{"plain_text": "hi"}}, "hi"},
		{"text.content fallback", []any{map[string]any{"text": map[string]any{"content": "yo"}}}, "yo"},
		{"non-map entry skipped", []any{"skip", map[string]any{"plain_text": "ok"}}, "ok"},
		{"concatenates", []any{map[string]any{"plain_text": "a"}, map[string]any{"text": map[string]any{"content": "b"}}}, "ab"},
		{"missing both yields empty", []any{map[string]any{"annotations": map[string]any{}}}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := richTextPlain(c.in); got != c.want {
				t.Errorf("richTextPlain(%v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestCovFlattenNotionPage_Variants(t *testing.T) {
	t.Run("non-page passthrough", func(t *testing.T) {
		if got := flattenNotionPage("scalar"); got != "scalar" {
			t.Errorf("flattenNotionPage = %v", got)
		}
	})
	t.Run("timestamps and bare page", func(t *testing.T) {
		got := flattenNotionPage(map[string]any{
			"id":               "p9",
			"url":              "https://notion/p9",
			"created_time":     "2026-01-01T00:00:00Z",
			"last_edited_time": "2026-02-02T00:00:00Z",
			"properties": map[string]any{
				"Name": map[string]any{"type": "title", "title": []any{map[string]any{"plain_text": "T"}}},
			},
		}).(map[string]any)
		want := map[string]any{
			"id": "p9", "url": "https://notion/p9",
			"created_time": "2026-01-01T00:00:00Z", "last_edited_time": "2026-02-02T00:00:00Z",
			"Name": "T",
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("row[%q] = %v, want %v", k, got[k], v)
			}
		}
	})
	t.Run("no id url timestamps", func(t *testing.T) {
		got := flattenNotionPage(map[string]any{
			"properties": map[string]any{"N": map[string]any{"type": "number", "number": float64(1)}},
		}).(map[string]any)
		if _, ok := got["id"]; ok {
			t.Errorf("unexpected id key: %+v", got)
		}
		if got["N"] != float64(1) {
			t.Errorf("N = %v", got["N"])
		}
	})
}

func TestCovNotionCreatePage_BadTitleInput(t *testing.T) {
	withNotionEnv(t, "http://unused")
	res, _ := executeNotionCreatePage(context.Background(), core.Job{
		Params: map[string]any{"parent_database_id": "db", "title": "x"},
		// Non-text inline (a number) makes TextInputOr report not-ok.
		Input: map[string]core.Ref{"title": {Inline: 123}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestCovNotionCreatePage_AuthFailure(t *testing.T) {
	SetHTTPBase("http://unused")
	t.Cleanup(func() { SetHTTPBase("https://api.notion.com/v1") })
	withNotionAuthErr(t)
	res, _ := executeNotionCreatePage(context.Background(), core.Job{
		Params: map[string]any{"parent_database_id": "db", "title": "x"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "auth" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestCovNotionCreatePage_APIError(t *testing.T) {
	srv := newNotionServer(t, 401, map[string]any{"code": "unauthorized", "message": "bad token"})
	withNotionEnv(t, srv.URL)
	res, _ := executeNotionCreatePage(context.Background(), core.Job{
		Params: map[string]any{"parent_page_id": "pg", "title": "x"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "notion_error" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestCovNotionCreatePage_HTTPTransportError(t *testing.T) {
	// Point the base at a closed port so the dial fails (transport error
	// branch, not a non-2xx).
	SetHTTPBase("http://127.0.0.1:1")
	SetTokenLookup(func(_ context.Context, account string) (string, error) { return "tok", nil })
	t.Cleanup(func() {
		SetHTTPBase("https://api.notion.com/v1")
		SetTokenLookup(nil)
	})
	res, _ := executeNotionCreatePage(context.Background(), core.Job{
		Params: map[string]any{"parent_database_id": "db", "title": "x", "timeout_ms": 1000},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "notion_http_error" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestCovNotionCreatePage_ChildrenParamMerged(t *testing.T) {
	srv := newNotionServer(t, 200, map[string]any{"id": "new-id"})
	withNotionEnv(t, srv.URL)
	res, _ := executeNotionCreatePage(context.Background(), core.Job{
		Params: map[string]any{
			"parent_database_id": "db",
			"title":              "Note",
			"children":           []any{map[string]any{"object": "block", "type": "divider", "divider": map[string]any{}}},
			"content":            "body para",
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	children, _ := srv.lastBody["children"].([]any)
	// One from the children param + one from the content param.
	if len(children) != 2 {
		t.Errorf("children = %+v", srv.lastBody["children"])
	}
}

func TestCovNotionCreatePage_NonObjectProperties(t *testing.T) {
	srv := newNotionServer(t, 200, map[string]any{"id": "new-id"})
	withNotionEnv(t, srv.URL)
	// A non-object 'properties' passes through verbatim (mergedProperties
	// early-return branch); title is required-by-content so this still
	// succeeds because properties is non-empty.
	res, _ := executeNotionCreatePage(context.Background(), core.Job{
		Params: map[string]any{
			"parent_database_id": "db",
			"properties":         "not-an-object",
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if srv.lastBody["properties"] != "not-an-object" {
		t.Errorf("properties = %v, want verbatim passthrough", srv.lastBody["properties"])
	}
}

func TestCovNotionQuery_BadDatabaseIDInput(t *testing.T) {
	withNotionEnv(t, "http://unused")
	res, _ := executeNotionQueryDatabase(context.Background(), core.Job{
		Params: map[string]any{"database_id": "db"},
		Input:  map[string]core.Ref{"database_id": {Inline: 123}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestCovNotionQuery_AuthFailure(t *testing.T) {
	SetHTTPBase("http://unused")
	t.Cleanup(func() { SetHTTPBase("https://api.notion.com/v1") })
	withNotionAuthErr(t)
	res, _ := executeNotionQueryDatabase(context.Background(), core.Job{
		Params: map[string]any{"database_id": "db"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "auth" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestCovNotionQuery_HTTPTransportError(t *testing.T) {
	SetHTTPBase("http://127.0.0.1:1")
	SetTokenLookup(func(_ context.Context, account string) (string, error) { return "tok", nil })
	t.Cleanup(func() {
		SetHTTPBase("https://api.notion.com/v1")
		SetTokenLookup(nil)
	})
	res, _ := executeNotionQueryDatabase(context.Background(), core.Job{
		Params: map[string]any{"database_id": "db", "timeout_ms": 1000},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "notion_http_error" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestCovNotionQuery_FilterSortsCursorForwarded(t *testing.T) {
	srv := newNotionServer(t, 200, map[string]any{"results": []any{}})
	withNotionEnv(t, srv.URL)
	res, _ := executeNotionQueryDatabase(context.Background(), core.Job{
		Params: map[string]any{
			"database_id":  "db",
			"filter":       map[string]any{"property": "Status", "select": map[string]any{"equals": "Todo"}},
			"sorts":        []any{map[string]any{"property": "Created", "direction": "descending"}},
			"start_cursor": "cur-abc",
			"page_size":    10,
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if srv.lastBody["filter"] == nil || srv.lastBody["sorts"] == nil {
		t.Errorf("filter/sorts not forwarded: %+v", srv.lastBody)
	}
	if srv.lastBody["start_cursor"] != "cur-abc" {
		t.Errorf("start_cursor = %v", srv.lastBody["start_cursor"])
	}
	if srv.lastBody["page_size"] != float64(10) {
		t.Errorf("page_size = %v", srv.lastBody["page_size"])
	}
}

func TestCovNotionQuery_NullResultsBecomesEmpty(t *testing.T) {
	// A response with no results field leaves rows as an empty slice.
	srv := newNotionServer(t, 200, map[string]any{"has_more": false})
	withNotionEnv(t, srv.URL)
	res, _ := executeNotionQueryDatabase(context.Background(), core.Job{
		Params: map[string]any{"database_id": "db"},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	rows, ok := res.Output["rows"].Inline.([]any)
	if !ok || len(rows) != 0 {
		t.Errorf("rows = %v, want empty slice", res.Output["rows"].Inline)
	}
}
