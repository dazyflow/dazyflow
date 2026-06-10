package notion

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
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
