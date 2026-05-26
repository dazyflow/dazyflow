package notion

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// fakeNotion stands in for the real Notion API. Each handler
// records the last received request shape and headers so tests can
// assert on auth + payload, then returns whatever's pre-configured.
type fakeNotion struct {
	server *httptest.Server

	mu                    sync.Mutex
	lastCreatePageReq     map[string]any
	lastCreatePageAuth    string
	lastCreatePageVersion string
	createPageResp        string
	createPageStatus      int

	lastQueryDBReq     map[string]any
	lastQueryDBPath    string
	lastQueryDBVersion string
	queryDBResp        string
	queryDBStatus      int
}

func newFakeNotion(t *testing.T) *fakeNotion {
	t.Helper()
	f := &fakeNotion{
		createPageResp:   `{"object":"page","id":"page-uuid-1","url":"https://notion.so/page-1","properties":{}}`,
		createPageStatus: 200,
		queryDBResp:      `{"object":"list","results":[{"id":"row-1"},{"id":"row-2"}],"next_cursor":"cur-2","has_more":true}`,
		queryDBStatus:    200,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/pages", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		_ = json.Unmarshal(body, &f.lastCreatePageReq)
		f.lastCreatePageAuth = r.Header.Get("Authorization")
		f.lastCreatePageVersion = r.Header.Get("Notion-Version")
		status := f.createPageStatus
		resp := f.createPageResp
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, resp)
	})
	mux.HandleFunc("/databases/", func(w http.ResponseWriter, r *http.Request) {
		// Path: /databases/{id}/query — we capture both the path and
		// the body so tests can verify the DB id is in the URL, not
		// the body, AND the body shape (filter/sorts) is what we sent.
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		_ = json.Unmarshal(body, &f.lastQueryDBReq)
		f.lastQueryDBPath = r.URL.Path
		f.lastQueryDBVersion = r.Header.Get("Notion-Version")
		status := f.queryDBStatus
		resp := f.queryDBResp
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, resp)
	})

	f.server = httptest.NewServer(mux)
	prev := currentHTTPBase()
	SetHTTPBase(f.server.URL)
	t.Cleanup(func() {
		f.server.Close()
		SetHTTPBase(prev)
	})
	return f
}

// ---- notion_create_page --------------------------------------------

func TestNotionCreatePage_ToDatabase(t *testing.T) {
	fn := newFakeNotion(t)
	res, err := executeNotionCreatePage(t.Context(), core.Job{
		Params: map[string]any{
			"token":              "secret_test",
			"parent_database_id": "db-uuid-1",
			"properties":         map[string]any{"Name": map[string]any{"title": []any{map[string]any{"text": map[string]any{"content": "Hi"}}}}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}

	fn.mu.Lock()
	defer fn.mu.Unlock()
	if fn.lastCreatePageAuth != "Bearer secret_test" {
		t.Errorf("auth=%q", fn.lastCreatePageAuth)
	}
	if fn.lastCreatePageVersion != notionAPIVersion {
		t.Errorf("Notion-Version=%q want %q", fn.lastCreatePageVersion, notionAPIVersion)
	}
	parent, _ := fn.lastCreatePageReq["parent"].(map[string]any)
	if parent["database_id"] != "db-uuid-1" {
		t.Errorf("parent.database_id=%v", parent["database_id"])
	}

	if id, _ := res.Output["id"].Inline.(string); id != "page-uuid-1" {
		t.Errorf("id port=%q", id)
	}
	if u, _ := res.Output["url"].Inline.(string); u != "https://notion.so/page-1" {
		t.Errorf("url port=%q", u)
	}
}

func TestNotionCreatePage_ToParentPage(t *testing.T) {
	fn := newFakeNotion(t)
	_, _ = executeNotionCreatePage(t.Context(), core.Job{
		Params: map[string]any{
			"token":          "secret_test",
			"parent_page_id": "page-parent-1",
			"properties":     map[string]any{"title": "x"},
		},
	}, nil)
	fn.mu.Lock()
	defer fn.mu.Unlock()
	parent, _ := fn.lastCreatePageReq["parent"].(map[string]any)
	if parent["page_id"] != "page-parent-1" {
		t.Errorf("parent.page_id=%v", parent["page_id"])
	}
	if _, hasDB := parent["database_id"]; hasDB {
		t.Errorf("parent shouldn't carry database_id when parent_page_id is set: %+v", parent)
	}
}

func TestNotionCreatePage_RequiresExactlyOneParent(t *testing.T) {
	_ = newFakeNotion(t)
	// Neither set.
	res, _ := executeNotionCreatePage(t.Context(), core.Job{
		Params: map[string]any{
			"token":      "secret_test",
			"properties": map[string]any{"title": "x"},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("missing-parent should fail bad_param, got %+v", res)
	}
	// Both set.
	res2, _ := executeNotionCreatePage(t.Context(), core.Job{
		Params: map[string]any{
			"token":              "secret_test",
			"parent_database_id": "db-1",
			"parent_page_id":     "page-1",
			"properties":         map[string]any{"title": "x"},
		},
	}, nil)
	if res2.Status != core.StatusError || res2.Error.Code != "bad_param" {
		t.Errorf("both-parents should fail bad_param, got %+v", res2)
	}
}

func TestNotionCreatePage_MissingPropertiesFails(t *testing.T) {
	_ = newFakeNotion(t)
	res, _ := executeNotionCreatePage(t.Context(), core.Job{
		Params: map[string]any{
			"token":              "secret_test",
			"parent_database_id": "db-1",
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("missing properties should fail bad_param, got %+v", res)
	}
}

func TestNotionCreatePage_ErrorResponseSurfacesCode(t *testing.T) {
	fn := newFakeNotion(t)
	fn.createPageStatus = 400
	fn.createPageResp = `{"object":"error","status":400,"code":"validation_error","message":"body.parent.database_id should be defined"}`

	res, _ := executeNotionCreatePage(t.Context(), core.Job{
		Params: map[string]any{
			"token":              "secret_test",
			"parent_database_id": "db-1",
			"properties":         map[string]any{},
		},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("expected error")
	}
	if res.Error.Code != "notion_error" {
		t.Errorf("code=%q", res.Error.Code)
	}
	if !strings.Contains(res.Error.Message, "validation_error") {
		t.Errorf("message should include notion error code: %q", res.Error.Message)
	}
}

func TestNotionCreatePage_TokenLookupOnAccount(t *testing.T) {
	fn := newFakeNotion(t)
	prev := tokenLookup
	SetTokenLookup(func(_ context.Context, account string) (string, error) {
		if account != "main" {
			t.Errorf("got account=%q", account)
		}
		return "secret_lookup_main", nil
	})
	t.Cleanup(func() { SetTokenLookup(prev) })

	_, _ = executeNotionCreatePage(t.Context(), core.Job{
		Params: map[string]any{
			"account":            "main",
			"parent_database_id": "db-1",
			"properties":         map[string]any{},
		},
	}, nil)
	fn.mu.Lock()
	defer fn.mu.Unlock()
	if fn.lastCreatePageAuth != "Bearer secret_lookup_main" {
		t.Errorf("auth=%q", fn.lastCreatePageAuth)
	}
}

// ---- notion_query_database -----------------------------------------

func TestNotionQueryDatabase_HappyPath(t *testing.T) {
	fn := newFakeNotion(t)
	res, err := executeNotionQueryDatabase(t.Context(), core.Job{
		Params: map[string]any{
			"token":       "secret_test",
			"database_id": "db-uuid-1",
			"page_size":   25,
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}

	fn.mu.Lock()
	defer fn.mu.Unlock()
	if fn.lastQueryDBPath != "/databases/db-uuid-1/query" {
		t.Errorf("path=%q", fn.lastQueryDBPath)
	}
	if fn.lastQueryDBVersion != notionAPIVersion {
		t.Errorf("Notion-Version=%q", fn.lastQueryDBVersion)
	}

	pages, _ := res.Output["pages"].Inline.([]map[string]any)
	if len(pages) != 2 {
		t.Errorf("pages len=%d want 2", len(pages))
	}
	if cur, _ := res.Output["next_cursor"].Inline.(string); cur != "cur-2" {
		t.Errorf("next_cursor=%q", cur)
	}
	if more, _ := res.Output["has_more"].Inline.(string); more != "true" {
		t.Errorf("has_more=%q want true", more)
	}
}

func TestNotionQueryDatabase_FilterAndSortsRoundTrip(t *testing.T) {
	fn := newFakeNotion(t)
	_, _ = executeNotionQueryDatabase(t.Context(), core.Job{
		Params: map[string]any{
			"token":        "secret_test",
			"database_id":  "db-1",
			"filter":       map[string]any{"property": "Status", "select": map[string]any{"equals": "Done"}},
			"sorts":        []any{map[string]any{"property": "Created", "direction": "descending"}},
			"start_cursor": "prev-cursor",
		},
	}, nil)
	fn.mu.Lock()
	defer fn.mu.Unlock()
	if fn.lastQueryDBReq["start_cursor"] != "prev-cursor" {
		t.Errorf("start_cursor not propagated: %+v", fn.lastQueryDBReq)
	}
	if _, ok := fn.lastQueryDBReq["filter"]; !ok {
		t.Errorf("filter missing: %+v", fn.lastQueryDBReq)
	}
	if _, ok := fn.lastQueryDBReq["sorts"]; !ok {
		t.Errorf("sorts missing: %+v", fn.lastQueryDBReq)
	}
}

func TestNotionQueryDatabase_MissingDatabaseIDFails(t *testing.T) {
	_ = newFakeNotion(t)
	res, _ := executeNotionQueryDatabase(t.Context(), core.Job{
		Params: map[string]any{"token": "secret_test"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("expected bad_param, got %+v", res)
	}
}

func TestNotionQueryDatabase_ErrorResponseSurfaces(t *testing.T) {
	fn := newFakeNotion(t)
	fn.queryDBStatus = 404
	fn.queryDBResp = `{"object":"error","status":404,"code":"object_not_found","message":"Could not find database with ID: db-1"}`

	res, _ := executeNotionQueryDatabase(t.Context(), core.Job{
		Params: map[string]any{
			"token":       "secret_test",
			"database_id": "db-1",
		},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("expected error")
	}
	if !strings.Contains(res.Error.Message, "object_not_found") {
		t.Errorf("message should surface Notion code: %q", res.Error.Message)
	}
}

// ---- Auth path -----------------------------------------------------

func TestNotion_NoTokenAndNoLookupFails(t *testing.T) {
	_ = newFakeNotion(t)
	// Save and clear the lookup.
	prev := tokenLookup
	SetTokenLookup(nil)
	t.Cleanup(func() { SetTokenLookup(prev) })

	res, _ := executeNotionCreatePage(t.Context(), core.Job{
		Params: map[string]any{
			"parent_database_id": "db-1",
			"properties":         map[string]any{},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "auth" {
		t.Errorf("expected auth error, got %+v", res)
	}
}
