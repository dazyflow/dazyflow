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

func TestNotionQuery_ReturnsPagesAndCursor(t *testing.T) {
	srv := newNotionServer(t, 200, map[string]any{
		"results":     []any{map[string]any{"id": "p1"}, map[string]any{"id": "p2"}},
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
	if len(res.Output["pages"].Inline.([]any)) != 2 {
		t.Errorf("pages = %+v", res.Output["pages"].Inline)
	}
	if res.Output["next_cursor"].Inline != "cur123" || res.Output["has_more"].Inline != "true" {
		t.Errorf("cursor/has_more = %v / %v", res.Output["next_cursor"].Inline, res.Output["has_more"].Inline)
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
